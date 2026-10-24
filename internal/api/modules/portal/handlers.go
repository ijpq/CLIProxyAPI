package portal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
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

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8"`
}

type createKeyRequest struct {
	store.APIKeyPolicy
	Name string `json:"name"`
}

func (m *Module) handleRegister(c *gin.Context) {
	if !m.registrationAllowed(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "此入口仅供已有特权账户登录，不开放注册"})
		return
	}
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
	m.notify(c.Request.Context(), fmt.Sprintf("🆕 新用户注册: %s", user.Email))
	c.JSON(http.StatusCreated, gin.H{
		"token": token,
		"user":  customerUserView(user),
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
	if user.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "account suspended"})
		return
	}
	if err := billing.ComparePassword(user.PasswordHash, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if !m.enforceUserSite(c, user.ID) {
		return
	}
	token, err := m.tokens.Issue(user.ID, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue token failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user":  customerUserView(user),
	})
}

func (m *Module) handleChangePassword(c *gin.Context) {
	userID := userIDFromGin(c)
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := m.store.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err := billing.ComparePassword(user.PasswordHash, req.OldPassword); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "原密码错误"})
		return
	}
	hash, err := billing.HashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "password hash failed"})
		return
	}
	if err := m.store.UpdateUserPassword(c.Request.Context(), userID, hash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update password failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "密码已修改"})
}

func (m *Module) handleMe(c *gin.Context) {
	userID := userIDFromGin(c)
	user, err := m.store.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": customerUserView(user)})
}

func (m *Module) handleWallet(c *gin.Context) {
	userID := userIDFromGin(c)
	bal, err := m.store.GetWalletBalance(c.Request.Context(), userID)
	if errors.Is(err, store.ErrLegacyBalance) {
		c.JSON(http.StatusConflict, gin.H{"error": "historical balance requires administrator reconciliation", "unit": "LEGACY"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "wallet lookup failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"balance": bal, "unit": "POINTS"})
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

	// Optional filters: by model, by upstream account, and (admin only) by the
	// owning portal user.
	filter := store.UsageFilter{
		Model:  strings.TrimSpace(c.Query("model")),
		AuthID: strings.TrimSpace(c.Query("auth_id")),
	}

	// The authentication middleware resolves current administrator access.
	// Everyone else sees only their own usage.
	admin := m.isAdmin(c)
	var records []store.UsageRecord
	var err error
	if admin {
		filter.UserID = strings.TrimSpace(c.Query("user_id"))
		records, err = m.store.ListAllUsage(c.Request.Context(), before, limit, filter)
	} else {
		if filter.AuthID != "" {
			_, ids, _, lookupErr := m.store.UsageFilterOptions(c.Request.Context(), userID)
			if lookupErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "filter options failed"})
				return
			}
			resolved := ""
			for _, id := range ids {
				if billing.AccountHandle(id) == filter.AuthID {
					resolved = id
					break
				}
			}
			if resolved == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account filter"})
				return
			}
			filter.AuthID = resolved
		}
		records, err = m.store.ListUsage(c.Request.Context(), userID, before, limit, filter)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage lookup failed"})
		return
	}
	out := make([]gin.H, 0, len(records))
	var aliases map[string]string
	if !admin {
		aliases, _ = m.store.AccountAliases(c.Request.Context())
	}
	for _, r := range records {
		v := usageView(r)
		if !admin && r.AuthID != "" {
			v["auth_id"] = billing.AccountHandle(r.AuthID)
			v["auth_label"] = billing.SafeAccountLabel(r.AuthID, aliases)
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, gin.H{"records": out, "scope": map[bool]string{true: "all", false: "self"}[admin]})
}

// isAdmin reads the current database-backed decision from AuthMiddleware.
func (m *Module) isAdmin(c *gin.Context) bool {
	return isAdminFromGin(c)
}

// handleUsageFilters returns the distinct filter values present in the caller's
// visible usage (models, upstream accounts, and — for admins — portal users), so
// the UI can populate its filter dropdowns with only meaningful choices.
func (m *Module) handleUsageFilters(c *gin.Context) {
	userID := userIDFromGin(c)
	admin := m.isAdmin(c)
	scopeUser := userID
	if admin {
		scopeUser = "" // all users
	}
	models, authIDs, users, err := m.store.UsageFilterOptions(c.Request.Context(), scopeUser)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "filter options failed"})
		return
	}
	aliases, _ := m.store.AccountAliases(c.Request.Context())
	accounts := make([]gin.H, 0, len(authIDs))
	for _, id := range authIDs {
		viewID := id
		if !admin {
			viewID = billing.AccountHandle(id)
		}
		accounts = append(accounts, gin.H{"id": viewID, "label": billing.SafeAccountLabel(id, aliases)})
	}
	resp := gin.H{
		"scope":    map[bool]string{true: "all", false: "self"}[admin],
		"models":   models,
		"accounts": accounts,
	}
	if admin {
		us := make([]gin.H, 0, len(users))
		for _, u := range users {
			us = append(us, gin.H{"id": u.ID, "email": u.Email})
		}
		resp["users"] = us
	}
	c.JSON(http.StatusOK, resp)
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
	c.JSON(http.StatusOK, gin.H{"keys": out, "max_active_keys": store.MaxActiveAPIKeys})
}

func (m *Module) handleCreateKey(c *gin.Context) {
	userID := userIDFromGin(c)
	var req createKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}

	policy, err := store.NormalizeKeyPolicy(req.APIKeyPolicy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model policy"})
		return
	}
	// Key policy can only narrow the owner's effective access.
	raw, err := m.keyGen()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate key failed"})
		return
	}
	rec, err := m.store.CreateAPIKey(c.Request.Context(), userID, store.HashAPIKey(raw), keyPrefix(raw), req.Name, policy)
	if errors.Is(err, store.ErrAPIKeyLimit) {
		c.JSON(http.StatusConflict, gin.H{"error": "最多可创建 10 个有效 API Key，请先吊销不用的 Key。"})
		return
	}
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

// handleMyAccountLabels returns customer-safe display names for the current
// user's allowed accounts (admin alias, or provider + short hash — never the
// raw credential filename, which may contain the operator's email).
func (m *Module) handleMyAccountLabels(c *gin.Context) {
	userID := userIDFromGin(c)
	user, err := m.store.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	aliases, _ := m.store.AccountAliases(c.Request.Context())
	labels := make(map[string]string, len(user.AllowedAuthIDs))
	for _, id := range user.AllowedAuthIDs {
		labels[billing.AccountHandle(id)] = billing.SafeAccountLabel(id, aliases)
	}
	c.JSON(http.StatusOK, gin.H{"labels": labels})
}

// quotaLookupTimeout bounds the live upstream quota probes so a single slow
// provider cannot stall the 额度 page. This is a control-plane lookup, not the
// proxy data path, so bounding it does not violate the upstream-timeout rule.
const quotaLookupTimeout = 15 * time.Second

// handleAccountQuota returns each allowed account's live upstream quota (Claude
// 5h/weekly windows, Codex 5h/weekly windows, Antigravity credits). Accounts are
// always identified by their customer-safe label, never the raw credential id.
func (m *Module) handleAccountQuota(c *gin.Context) {
	userID := userIDFromGin(c)
	user, err := m.store.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	aliases, _ := m.store.AccountAliases(c.Request.Context())

	// Resolve the account set the user may see: their allowed accounts, or every
	// account when unrestricted (empty allowed set = access to all).
	all := billing.Accounts()
	providerByID := make(map[string]string, len(all))
	for _, a := range all {
		providerByID[a.ID] = a.Provider
	}
	ids := user.AllowedAuthIDs
	if len(ids) == 0 {
		ids = make([]string, 0, len(all))
		for _, a := range all {
			ids = append(ids, a.ID)
		}
	}

	targets := make([]billing.AccountQuota, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, billing.AccountQuota{
			AuthID:   id,
			Provider: providerByID[id],
			Label:    billing.SafeAccountLabel(id, aliases),
		})
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), quotaLookupTimeout)
	defer cancel()
	results := billing.FetchAccountQuotas(ctx, targets)

	canReset := userCanReset(user)
	out := make([]gin.H, 0, len(results))
	for _, r := range results {
		out = append(out, accountQuotaView(r, canReset))
	}
	c.JSON(http.StatusOK, gin.H{"accounts": out})
}

// accountQuotaView is the customer-facing per-account payload. It exposes an
// opaque handle (for refresh/reset) but never the raw credential id/email. The
// reset button is only offered when the account supports it AND the user has
// been granted reset permission by the super admin.
func accountQuotaView(q billing.AccountQuota, canReset bool) gin.H {
	return gin.H{
		"handle":        billing.AccountHandle(q.AuthID),
		"provider":      q.Provider,
		"label":         q.Label,
		"plan_type":     q.PlanType,
		"details":       q.Details,
		"windows":       q.Windows,
		"reset_credits": q.ResetCredits,
		"can_reset":     q.CanReset && canReset,
		"note":          q.Note,
		"error":         q.Error,
	}
}

// accountForHandle resolves an opaque account handle back to a concrete upstream
// account, restricted to the accounts the user is allowed to see. Returns the
// raw id, provider and customer-safe label.
func (m *Module) accountForHandle(ctx context.Context, user store.User, handle string) (id, provider, label string, ok bool) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return "", "", "", false
	}
	all := billing.Accounts()
	providerByID := make(map[string]string, len(all))
	for _, a := range all {
		providerByID[a.ID] = a.Provider
	}
	ids := user.AllowedAuthIDs
	if len(ids) == 0 {
		ids = make([]string, 0, len(all))
		for _, a := range all {
			ids = append(ids, a.ID)
		}
	}
	for _, candidate := range ids {
		if billing.AccountHandle(candidate) == handle {
			aliases, _ := m.store.AccountAliases(ctx)
			return candidate, providerByID[candidate], billing.SafeAccountLabel(candidate, aliases), true
		}
	}
	return "", "", "", false
}

// handleRefreshAccountQuota re-probes a single account's live quota.
func (m *Module) handleRefreshAccountQuota(c *gin.Context) {
	userID := userIDFromGin(c)
	user, err := m.store.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	id, provider, label, ok := m.accountForHandle(c.Request.Context(), user, c.Param("handle"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), quotaLookupTimeout)
	defer cancel()
	c.JSON(http.StatusOK, accountQuotaView(billing.FetchAccountQuota(ctx, id, provider, label), userCanReset(user)))
}

// handleResetAccountCredit redeems one Codex rate-limit reset credit for the
// account, then returns its refreshed quota. Codex accounts only, and only for
// users the super admin granted reset permission.
func (m *Module) handleResetAccountCredit(c *gin.Context) {
	userID := userIDFromGin(c)
	user, err := m.store.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if !userCanReset(user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无重置权限，请联系管理员开通"})
		return
	}
	id, provider, label, ok := m.accountForHandle(c.Request.Context(), user, c.Param("handle"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), quotaLookupTimeout)
	defer cancel()
	if err := billing.ConsumeCodexResetCredit(ctx, id); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	m.notify(c.Request.Context(), fmt.Sprintf("♻️ 重置额度: 用户 %s 消耗了账号 %s 的一个重置额度", userID, label))
	c.JSON(http.StatusOK, accountQuotaView(billing.FetchAccountQuota(ctx, id, provider, label), true))
}

// customerUserView uses the same opaque account references as quota endpoints.
// Administrative ACL editing retains raw IDs through userView.
func customerUserView(u store.User) gin.H {
	v := userView(u)
	handles := make([]string, 0, len(u.AllowedAuthIDs))
	for _, id := range u.AllowedAuthIDs {
		handles = append(handles, billing.AccountHandle(id))
	}
	v["allowed_auth_ids"] = handles
	return v
}

func userView(u store.User) gin.H {
	models := u.AllowedModels
	if models == nil {
		models = []string{}
	}
	auths := u.AllowedAuthIDs
	if auths == nil {
		auths = []string{}
	}
	return gin.H{
		"id":               u.ID,
		"email":            u.Email,
		"display_name":     u.DisplayName,
		"status":           u.Status,
		"is_admin":         u.IsAdmin,
		"unbilled":         u.Unbilled,
		"allowed_models":   models,
		"allowed_auth_ids": auths,
		"allow_reset":      u.AllowReset,
		"created_at":       u.CreatedAt,
	}
}

// userCanReset reports whether the user may consume provider reset credits.
// The super admin always may; other users only when granted.
func userCanReset(u store.User) bool {
	return u.IsAdmin || u.AllowReset
}

func apiKeyView(k store.APIKeyRecord) gin.H {
	return gin.H{
		"id":             k.ID,
		"name":           k.Name,
		"key_prefix":     k.KeyPrefix,
		"model_scope":    k.ModelScope,
		"allowed_models": append([]string{}, k.AllowedModels...),
		"created_at":     k.CreatedAt,
		"last_used_at":   nullableTime(k.LastUsedAt),
		"revoked_at":     nullableTime(k.RevokedAt),
	}
}

func usageView(r store.UsageRecord) gin.H {
	return gin.H{
		"id":                 r.ID,
		"api_key_id":         nullableString(r.APIKeyID),
		"user_email":         r.UserEmail,
		"request_id":         r.RequestID,
		"provider":           r.Provider,
		"model":              r.Model,
		"input_tokens":       r.InputTokens,
		"output_tokens":      r.OutputTokens,
		"cache_read_tokens":  r.CacheReadTokens,
		"cache_write_tokens": r.CacheWriteTokens,
		"cost":               r.Cost,
		"price_version":      r.PriceVersion,
		"unit":               r.Unit,
		"status":             r.Status,
		"error_message":      r.ErrorMessage,
		"auth_id":            r.AuthID,
		"auth_label":         r.AuthLabel,
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
