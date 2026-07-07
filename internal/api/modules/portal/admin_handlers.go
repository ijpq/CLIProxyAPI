package portal

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type adminCreditRequest struct {
	UserID string  `json:"user_id" binding:"required"`
	Amount float64 `json:"amount" binding:"required"`
	Note   string  `json:"note"`
}

type adminPrivilegedRequest struct {
	Privileged bool `json:"privileged"`
}

func (m *Module) handleAdminListUsers(c *gin.Context) {
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	users, err := m.store.ListAllUsers(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list users failed"})
		return
	}
	out := make([]gin.H, 0, len(users))
	for _, u := range users {
		bal, _ := m.store.GetWalletBalance(c.Request.Context(), u.ID)
		out = append(out, gin.H{
			"id":            u.ID,
			"email":         u.Email,
			"display_name":  u.DisplayName,
			"status":        u.Status,
			"is_admin":      u.IsAdmin,
			"is_privileged": u.IsPrivileged,
			"balance":       bal,
			"created_at":    u.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

func (m *Module) handleAdminCredit(c *gin.Context) {
	var req adminCreditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	amountStr := strconv.FormatFloat(req.Amount, 'f', 6, 64)
	newBalance, err := m.store.AdminCreditWallet(c.Request.Context(), req.UserID, amountStr, "", req.Note)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "credit failed"})
		return
	}
	if m.onWalletChanged != nil {
		m.onWalletChanged(req.UserID)
	}
	m.notify(c.Request.Context(), fmt.Sprintf("💰 管理员充值: 用户 %s, 金额 %s, 备注: %s", req.UserID, amountStr, req.Note))
	c.JSON(http.StatusOK, gin.H{"balance": newBalance})
}

// handleAdminSetPrivileged toggles the is_privileged flag for a user.
func (m *Module) handleAdminSetPrivileged(c *gin.Context) {
	userID := c.Param("id")
	var req adminPrivilegedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := m.store.SetUserPrivileged(c.Request.Context(), userID, req.Privileged); err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update privileged failed"})
		return
	}
	m.notify(c.Request.Context(), fmt.Sprintf("🔑 特权变更: 用户 %s, privileged=%t", userID, req.Privileged))
	c.JSON(http.StatusOK, gin.H{"id": userID, "is_privileged": req.Privileged})
}

func (m *Module) handleUsageStats(c *gin.Context) {
	userID := userIDFromGin(c)
	days := 30
	if raw := c.Query("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			days = n
		}
	}
	daily, err := m.store.AggregateUsageByDay(c.Request.Context(), userID, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "daily stats failed"})
		return
	}
	byModel, err := m.store.AggregateUsageByModel(c.Request.Context(), userID, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "model stats failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"daily": daily, "by_model": byModel})
}
