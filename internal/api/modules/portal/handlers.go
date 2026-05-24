package portal

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type registerRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Password    string `json:"password" binding:"required,min=8"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type createKeyRequest struct {
	Name string `json:"name"`
}

func (m *Module) handleRegister(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := billing.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "password hash failed"})
		return
	}
	user, err := m.store.CreateUser(c.Request.Context(), req.Email, hash, req.DisplayName)
	switch {
	case errors.Is(err, store.ErrEmailTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create user failed"})
		return
	}
	token, err := m.tokens.Issue(user.ID, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue token failed"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"token": token,
		"user":  userView(user),
	})
}

func (m *Module) handleLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := m.store.GetUserByEmail(c.Request.Context(), req.Email)
	if errors.Is(err, store.ErrUserNotFound) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	if user.Status != "" && user.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "account suspended"})
		return
	}
	if err := billing.ComparePassword(user.PasswordHash, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	token, err := m.tokens.Issue(user.ID, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue token failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user":  userView(user),
	})
}

func (m *Module) handleMe(c *gin.Context) {
	userID := userIDFromGin(c)
	user, err := m.store.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": userView(user)})
}

func (m *Module) handleWallet(c *gin.Context) {
	userID := userIDFromGin(c)
	bal, err := m.store.GetWalletBalance(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "wallet lookup failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"balance": bal})
}

func (m *Module) handleUsage(c *gin.Context) {
	userID := userIDFromGin(c)

	limit := 100
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	var before time.Time
	if raw := c.Query("before"); raw != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			before = t
		}
	}

	records, err := m.store.ListUsage(c.Request.Context(), userID, before, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage lookup failed"})
		return
	}
	out := make([]gin.H, 0, len(records))
	for _, r := range records {
		out = append(out, usageView(r))
	}
	c.JSON(http.StatusOK, gin.H{"records": out})
}

func (m *Module) handleListKeys(c *gin.Context) {
	userID := userIDFromGin(c)
	keys, err := m.store.ListAPIKeys(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list keys failed"})
		return
	}
	out := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		out = append(out, apiKeyView(k))
	}
	c.JSON(http.StatusOK, gin.H{"keys": out})
}

func (m *Module) handleCreateKey(c *gin.Context) {
	userID := userIDFromGin(c)
	var req createKeyRequest
	_ = c.ShouldBindJSON(&req) // name is optional

	raw, err := m.keyGen()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate key failed"})
		return
	}
	rec, err := m.store.CreateAPIKey(c.Request.Context(), userID, store.HashAPIKey(raw), keyPrefix(raw), req.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create key failed"})
		return
	}
	view := apiKeyView(rec)
	view["key"] = raw // returned only on creation
	c.JSON(http.StatusCreated, view)
}

func (m *Module) handleRevokeKey(c *gin.Context) {
	userID := userIDFromGin(c)
	keyID := c.Param("id")
	if err := m.store.RevokeAPIKey(c.Request.Context(), userID, keyID); err != nil {
		if errors.Is(err, store.ErrAPIKeyNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revoke key failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

func userView(u store.User) gin.H {
	return gin.H{
		"id":           u.ID,
		"email":        u.Email,
		"display_name": u.DisplayName,
		"status":       u.Status,
		"is_admin":     u.IsAdmin,
		"created_at":   u.CreatedAt,
	}
}

func apiKeyView(k store.APIKeyRecord) gin.H {
	return gin.H{
		"id":           k.ID,
		"name":         k.Name,
		"key_prefix":   k.KeyPrefix,
		"created_at":   k.CreatedAt,
		"last_used_at": nullableTime(k.LastUsedAt),
		"revoked_at":   nullableTime(k.RevokedAt),
	}
}

func usageView(r store.UsageRecord) gin.H {
	return gin.H{
		"id":                 r.ID,
		"api_key_id":         nullableString(r.APIKeyID),
		"request_id":         r.RequestID,
		"provider":           r.Provider,
		"model":              r.Model,
		"input_tokens":       r.InputTokens,
		"output_tokens":      r.OutputTokens,
		"cache_read_tokens":  r.CacheReadTokens,
		"cache_write_tokens": r.CacheWriteTokens,
		"cost":               r.Cost,
		"status":             r.Status,
		"error_message":      r.ErrorMessage,
		"created_at":         r.CreatedAt,
	}
}

func nullableTime(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return t.Time
}

func nullableString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}
