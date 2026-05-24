package portal

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type createTopupRequest struct {
	Amount  float64 `json:"amount" binding:"required"`
	Method  string  `json:"method"`
	Network string  `json:"network"`
}

type submitTopupRequest struct {
	TxHash string `json:"tx_hash" binding:"required"`
}

type adminConfirmRequest struct {
	Note string `json:"note"`
}

func (m *Module) handleListTopupMethods(c *gin.Context) {
	if m.topup == nil {
		c.JSON(http.StatusOK, gin.H{"methods": []any{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"methods": m.topup.Methods()})
}

func (m *Module) handleCreateTopupOrder(c *gin.Context) {
	if m.topup == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "top-up not configured"})
		return
	}
	var req createTopupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := m.topup.ValidateAmount(req.Amount); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = "usdt"
	}
	chosen, ok := m.topup.Lookup(method, req.Network)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported method/network"})
		return
	}

	userID := userIDFromGin(c)
	order, err := m.store.CreateTopupOrder(
		c.Request.Context(),
		userID, chosen.Method,
		strconv.FormatFloat(req.Amount, 'f', 6, 64),
		chosen.Currency, chosen.Network, chosen.WalletAddress,
		m.topup.OrderTTL(),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create order failed"})
		return
	}
	c.JSON(http.StatusCreated, topupView(order))
}

func (m *Module) handleListTopupOrders(c *gin.Context) {
	userID := userIDFromGin(c)
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	orders, err := m.store.ListTopupOrders(c.Request.Context(), userID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list orders failed"})
		return
	}
	out := make([]gin.H, 0, len(orders))
	for _, o := range orders {
		out = append(out, topupView(o))
	}
	c.JSON(http.StatusOK, gin.H{"orders": out})
}

func (m *Module) handleGetTopupOrder(c *gin.Context) {
	userID := userIDFromGin(c)
	order, err := m.store.GetTopupOrder(c.Request.Context(), c.Param("id"), userID)
	if errors.Is(err, store.ErrTopupOrderNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get order failed"})
		return
	}
	c.JSON(http.StatusOK, topupView(order))
}

func (m *Module) handleSubmitTopupTxHash(c *gin.Context) {
	userID := userIDFromGin(c)
	var req submitTopupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	order, err := m.store.SubmitTopupTxHash(c.Request.Context(), userID, c.Param("id"), req.TxHash)
	switch {
	case errors.Is(err, store.ErrTopupOrderNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": "order is not pending"})
		return
	case errors.Is(err, store.ErrTopupTxHashTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "tx hash already used"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "submit tx hash failed"})
		return
	}
	c.JSON(http.StatusOK, topupView(order))
}

func (m *Module) handleCancelTopupOrder(c *gin.Context) {
	userID := userIDFromGin(c)
	err := m.store.CancelTopupOrder(c.Request.Context(), userID, c.Param("id"))
	switch {
	case errors.Is(err, store.ErrTopupOrderNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": "order is not cancellable"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cancel failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (m *Module) handleAdminListTopupOrders(c *gin.Context) {
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	scopeUser := strings.TrimSpace(c.Query("user_id"))
	orders, err := m.store.ListTopupOrders(c.Request.Context(), scopeUser, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list orders failed"})
		return
	}
	out := make([]gin.H, 0, len(orders))
	for _, o := range orders {
		out = append(out, topupView(o))
	}
	c.JSON(http.StatusOK, gin.H{"orders": out})
}

func (m *Module) handleAdminConfirmTopupOrder(c *gin.Context) {
	var req adminConfirmRequest
	_ = c.ShouldBindJSON(&req)
	order, err := m.store.ConfirmTopupOrder(c.Request.Context(), c.Param("id"), req.Note)
	switch {
	case errors.Is(err, store.ErrTopupOrderNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	case errors.Is(err, store.ErrTopupOrderNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": "order already settled"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "confirm failed"})
		return
	}
	if m.onWalletChanged != nil {
		m.onWalletChanged(order.UserID)
	}
	c.JSON(http.StatusOK, topupView(order))
}

func topupView(o store.TopupOrder) gin.H {
	return gin.H{
		"id":             o.ID,
		"user_id":        o.UserID,
		"method":         o.Method,
		"amount":         o.Amount,
		"currency":       o.Currency,
		"network":        o.Network,
		"wallet_address": o.WalletAddress,
		"tx_hash":        o.TxHash,
		"status":         o.Status,
		"notes":          o.Notes,
		"created_at":     o.CreatedAt,
		"submitted_at":   nullableTime(o.SubmittedAt),
		"confirmed_at":   nullableTime(o.ConfirmedAt),
		"expires_at":     nullableTime(o.ExpiresAt),
	}
}
