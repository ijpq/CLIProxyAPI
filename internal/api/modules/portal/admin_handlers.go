package portal

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type adminCreditRequest struct {
	UserID string  `json:"user_id" binding:"required"`
	Amount float64 `json:"amount" binding:"required"`
	Note   string  `json:"note"`
}

type adminLimitsRequest struct {
	Unbilled       bool     `json:"unbilled"`
	AllowedModels  []string `json:"allowed_models"`
	AllowedAuthIDs []string `json:"allowed_auth_ids"`
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
		v := userView(u)
		v["balance"] = bal
		out = append(out, v)
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

// handleAdminSetUserLimits sets the per-user access controls (unbilled +
// allowed models + allowed upstream accounts). Super admin only.
func (m *Module) handleAdminSetUserLimits(c *gin.Context) {
	userID := c.Param("id")
	var req adminLimitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	models := billing.ValidModels(req.AllowedModels)
	auths := billing.ValidAccountIDs(req.AllowedAuthIDs)
	if err := m.store.SetUserLimits(c.Request.Context(), userID, req.Unbilled, models, auths); err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update limits failed"})
		return
	}
	m.notify(c.Request.Context(), fmt.Sprintf("🔧 权限更新: 用户 %s (unbilled=%t, models=%d, accounts=%d)", userID, req.Unbilled, len(models), len(auths)))
	c.JSON(http.StatusOK, gin.H{
		"id":               userID,
		"unbilled":         req.Unbilled,
		"allowed_models":   models,
		"allowed_auth_ids": auths,
	})
}

// handleAdminListModels returns every client-visible model name for the admin's
// per-user model whitelist picker.
func (m *Module) handleAdminListModels(c *gin.Context) {
	models := billing.Models()
	if models == nil {
		models = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// handleAdminListAccounts returns every upstream account for the admin's
// per-user account whitelist picker.
func (m *Module) handleAdminListAccounts(c *gin.Context) {
	accounts := billing.Accounts()
	if accounts == nil {
		accounts = []billing.Account{}
	}
	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
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
