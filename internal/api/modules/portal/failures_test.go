package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

// The decorator injects failures at storage boundaries; successful paths use PostgreSQL.
type failureStore struct {
	Store
	operation string
	failure   error
}

func (s *failureStore) CreateUser(ctx context.Context, email, hash, name string) (store.User, error) {
	if s.operation == "CreateUser" {
		var zero store.User
		return zero, s.failure
	}
	return s.Store.CreateUser(ctx, email, hash, name)
}
func (s *failureStore) GetUserByEmail(ctx context.Context, email string) (store.User, error) {
	if s.operation == "GetUserByEmail" {
		var zero store.User
		return zero, s.failure
	}
	return s.Store.GetUserByEmail(ctx, email)
}
func (s *failureStore) GetUserByID(ctx context.Context, id string) (store.User, error) {
	if s.operation == "GetUserByID" {
		var zero store.User
		return zero, s.failure
	}
	return s.Store.GetUserByID(ctx, id)
}
func (s *failureStore) UpdateUserPassword(ctx context.Context, id, hash string) error {
	if s.operation == "UpdateUserPassword" {
		return s.failure
	}
	return s.Store.UpdateUserPassword(ctx, id, hash)
}
func (s *failureStore) CreateAPIKey(ctx context.Context, id, hash, prefix, name string, policies ...store.APIKeyPolicy) (store.APIKeyRecord, error) {
	if s.operation == "CreateAPIKey" {
		var zero store.APIKeyRecord
		return zero, s.failure
	}
	return s.Store.CreateAPIKey(ctx, id, hash, prefix, name, policies...)
}
func (s *failureStore) ListAPIKeys(ctx context.Context, id string) ([]store.APIKeyRecord, error) {
	if s.operation == "ListAPIKeys" {
		var zero []store.APIKeyRecord
		return zero, s.failure
	}
	return s.Store.ListAPIKeys(ctx, id)
}
func (s *failureStore) RevokeAPIKey(ctx context.Context, id, key string) error {
	if s.operation == "RevokeAPIKey" {
		return s.failure
	}
	return s.Store.RevokeAPIKey(ctx, id, key)
}
func (s *failureStore) GetWalletBalance(ctx context.Context, id string) (string, error) {
	if s.operation == "GetWalletBalance" {
		var zero string
		return zero, s.failure
	}
	return s.Store.GetWalletBalance(ctx, id)
}
func (s *failureStore) ListUsage(ctx context.Context, id string, before time.Time, limit int, filter store.UsageFilter) ([]store.UsageRecord, error) {
	if s.operation == "ListUsage" {
		var zero []store.UsageRecord
		return zero, s.failure
	}
	return s.Store.ListUsage(ctx, id, before, limit, filter)
}
func (s *failureStore) ListAllUsage(ctx context.Context, before time.Time, limit int, filter store.UsageFilter) ([]store.UsageRecord, error) {
	if s.operation == "ListAllUsage" {
		var zero []store.UsageRecord
		return zero, s.failure
	}
	return s.Store.ListAllUsage(ctx, before, limit, filter)
}
func (s *failureStore) CreateTopupOrder(ctx context.Context, id, method, amount, currency, network, address string, ttl time.Duration) (store.TopupOrder, error) {
	if s.operation == "CreateTopupOrder" {
		var zero store.TopupOrder
		return zero, s.failure
	}
	return s.Store.CreateTopupOrder(ctx, id, method, amount, currency, network, address, ttl)
}
func (s *failureStore) GetTopupOrder(ctx context.Context, id, user string) (store.TopupOrder, error) {
	if s.operation == "GetTopupOrder" {
		var zero store.TopupOrder
		return zero, s.failure
	}
	return s.Store.GetTopupOrder(ctx, id, user)
}
func (s *failureStore) ListTopupOrders(ctx context.Context, id string, limit int) ([]store.TopupOrder, error) {
	if s.operation == "ListTopupOrders" {
		var zero []store.TopupOrder
		return zero, s.failure
	}
	return s.Store.ListTopupOrders(ctx, id, limit)
}
func (s *failureStore) SubmitTopupTxHash(ctx context.Context, user, id, hash string) (store.TopupOrder, error) {
	if s.operation == "SubmitTopupTxHash" {
		var zero store.TopupOrder
		return zero, s.failure
	}
	return s.Store.SubmitTopupTxHash(ctx, user, id, hash)
}
func (s *failureStore) CancelTopupOrder(ctx context.Context, user, id string) error {
	if s.operation == "CancelTopupOrder" {
		return s.failure
	}
	return s.Store.CancelTopupOrder(ctx, user, id)
}
func (s *failureStore) ConfirmTopupOrder(ctx context.Context, id, note string) (store.TopupOrder, error) {
	if s.operation == "ConfirmTopupOrder" {
		var zero store.TopupOrder
		return zero, s.failure
	}
	return s.Store.ConfirmTopupOrder(ctx, id, note)
}
func (s *failureStore) ListAllUsers(ctx context.Context, limit int) ([]store.User, error) {
	if s.operation == "ListAllUsers" {
		var zero []store.User
		return zero, s.failure
	}
	return s.Store.ListAllUsers(ctx, limit)
}
func (s *failureStore) AdminCreditWallet(ctx context.Context, user, amount, reference, note string) (string, error) {
	if s.operation == "AdminCreditWallet" {
		var zero string
		return zero, s.failure
	}
	return s.Store.AdminCreditWallet(ctx, user, amount, reference, note)
}
func (s *failureStore) SetUserLimits(ctx context.Context, user string, unbilled bool, models, auths []string, reset bool) error {
	if s.operation == "SetUserLimits" {
		return s.failure
	}
	return s.Store.SetUserLimits(ctx, user, unbilled, models, auths, reset)
}
func (s *failureStore) SetAccountAlias(ctx context.Context, id, label string) error {
	if s.operation == "SetAccountAlias" {
		return s.failure
	}
	return s.Store.SetAccountAlias(ctx, id, label)
}
func (s *failureStore) AggregateUsageByDay(ctx context.Context, id string, days int) ([]store.DailyUsageStat, error) {
	if s.operation == "AggregateUsageByDay" {
		var zero []store.DailyUsageStat
		return zero, s.failure
	}
	return s.Store.AggregateUsageByDay(ctx, id, days)
}
func (s *failureStore) AggregateUsageByModel(ctx context.Context, id string, days int) ([]store.ModelUsageStat, error) {
	if s.operation == "AggregateUsageByModel" {
		var zero []store.ModelUsageStat
		return zero, s.failure
	}
	return s.Store.AggregateUsageByModel(ctx, id, days)
}
func (s *failureStore) GetPortalSettings(ctx context.Context) (store.PortalSettings, error) {
	if s.operation == "GetPortalSettings" {
		var zero store.PortalSettings
		return zero, s.failure
	}
	return s.Store.GetPortalSettings(ctx)
}
func (s *failureStore) SavePortalSettings(ctx context.Context, cfg store.PortalSettings, actor string) (store.PortalSettings, error) {
	if s.operation == "SavePortalSettings" {
		var zero store.PortalSettings
		return zero, s.failure
	}
	return s.Store.SavePortalSettings(ctx, cfg, actor)
}
func (s *failureStore) ListPointPrices(ctx context.Context) ([]store.PointPrice, error) {
	if s.operation == "ListPointPrices" {
		var zero []store.PointPrice
		return zero, s.failure
	}
	return s.Store.ListPointPrices(ctx)
}
func (s *failureStore) SavePointPrice(ctx context.Context, price store.PointPrice, actor string) (store.PointPrice, error) {
	if s.operation == "SavePointPrice" {
		var zero store.PointPrice
		return zero, s.failure
	}
	return s.Store.SavePointPrice(ctx, price, actor)
}
func (s *failureStore) ConsumeHelperAllowance(ctx context.Context, user string, limit, global int) (bool, error) {
	if s.operation == "ConsumeHelperAllowance" {
		var zero bool
		return zero, s.failure
	}
	return s.Store.ConsumeHelperAllowance(ctx, user, limit, global)
}
func (s *failureStore) UsageFilterOptions(ctx context.Context, user string) ([]string, []string, []store.UserRef, error) {
	if s.operation == "UsageFilterOptions" {
		return nil, nil, nil, s.failure
	}
	return s.Store.UsageFilterOptions(ctx, user)
}

func invokePortalHandler(handler gin.HandlerFunc, userID string, admin bool, body, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test"+query, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(ginCtxUserID, userID)
	c.Set(ginCtxIsAdmin, admin)
	c.Params = gin.Params{{Key: "id", Value: userID}, {Key: "handle", Value: "unknown"}}
	handler(c)
	return w
}

func TestPortalStorageFailuresReturnBoundedErrors(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "failure-injection@example.test")
	cfg := enableTestHelper(t, f, 10, 20)
	configJSON, _ := json.Marshal(cfg)
	f.module.SetHelper(func(context.Context, string, []ChatMessage, int) (string, error) {
		t.Error("storage failure reached provider")
		return "", nil
	})
	m := f.module
	cases := []struct {
		name, operation string
		handler         gin.HandlerFunc
		body, query     string
		admin           bool
		want            int
	}{
		{"register", "CreateUser", m.handleRegister, `{"email":"new@example.test","password":"disposable-password"}`, "", false, 500},
		{"login", "GetUserByEmail", m.handleLogin, `{"email":"failure-injection@example.test","password":"password"}`, "", false, 500},
		{"profile", "GetUserByID", m.handleMe, "{}", "", false, 404},
		{"change password user", "GetUserByID", m.handleChangePassword, `{"old_password":"old-pass","new_password":"new-password"}`, "", false, 404},
		{"change password update", "UpdateUserPassword", m.handleChangePassword, `{"old_password":"Disposable-test-password-9","new_password":"new-password"}`, "", false, 500},
		{"wallet", "GetWalletBalance", m.handleWallet, "{}", "", false, 500},
		{"usage", "ListUsage", m.handleUsage, "{}", "?limit=2&before=2026-10-23T00:00:00Z", false, 500},
		{"admin usage", "ListAllUsage", m.handleUsage, "{}", "?limit=2", true, 500},
		{"usage filter lookup", "UsageFilterOptions", m.handleUsage, "{}", "?auth_id=some-handle", false, 500},
		{"usage filters", "UsageFilterOptions", m.handleUsageFilters, "{}", "", false, 500},
		{"keys list", "ListAPIKeys", m.handleListKeys, "{}", "", false, 500},
		{"key creation", "CreateAPIKey", m.handleCreateKey, "{}", "", false, 500},
		{"key revoke", "RevokeAPIKey", m.handleRevokeKey, "{}", "", false, 500},
		{"labels", "GetUserByID", m.handleMyAccountLabels, "{}", "", false, 404},
		{"quota", "GetUserByID", m.handleAccountQuota, "{}", "", false, 404},
		{"quota refresh", "GetUserByID", m.handleRefreshAccountQuota, "{}", "", false, 404},
		{"quota reset", "GetUserByID", m.handleResetAccountCredit, "{}", "", false, 404},
		{"create order", "CreateTopupOrder", m.handleCreateTopupOrder, `{"amount":2,"network":"TRC20"}`, "", false, 500},
		{"list orders", "ListTopupOrders", m.handleListTopupOrders, "{}", "?limit=2", false, 500},
		{"get order", "GetTopupOrder", m.handleGetTopupOrder, "{}", "", false, 500},
		{"submit transfer", "SubmitTopupTxHash", m.handleSubmitTopupTxHash, `{"tx_hash":"synthetic"}`, "", false, 500},
		{"cancel order", "CancelTopupOrder", m.handleCancelTopupOrder, "{}", "", false, 500},
		{"admin order list", "ListTopupOrders", m.handleAdminListTopupOrders, "{}", "?limit=2", true, 500},
		{"admin confirm", "ConfirmTopupOrder", m.handleAdminConfirmTopupOrder, "{}", "", true, 500},
		{"admin users", "ListAllUsers", m.handleAdminListUsers, "{}", "?limit=2", true, 500},
		{"admin credit", "AdminCreditWallet", m.handleAdminCredit, `{"user_id":"synthetic-user","amount":2}`, "", true, 500},
		{"admin limits", "SetUserLimits", m.handleAdminSetUserLimits, "{}", "", true, 500},
		{"admin aliases", "SetAccountAlias", m.handleAdminSetAccountAlias, `{"id":"synthetic-account","label":"name"}`, "", true, 500},
		{"daily stats", "AggregateUsageByDay", m.handleUsageStats, "{}", "?days=7", false, 500},
		{"model stats", "AggregateUsageByModel", m.handleUsageStats, "{}", "?days=7", false, 500},
		{"public settings", "GetPortalSettings", m.handlePublicSite, "{}", "", false, 503},
		{"public pricing", "ListPointPrices", m.handlePublicSite, "{}", "", false, 503},
		{"admin settings", "GetPortalSettings", m.handleAdminGetSettings, "{}", "", true, 503},
		{"save settings", "SavePortalSettings", m.handleAdminSaveSettings, string(configJSON), "", true, 503},
		{"settings helper price validation", "ListPointPrices", m.handleAdminSaveSettings, string(configJSON), "", true, 503},
		{"get prices", "ListPointPrices", m.handleAdminGetPrices, "{}", "", true, 503},
		{"save price", "SavePointPrice", m.handleAdminSavePrice, `{"model":"test-model","input_per_million":"1","output_per_million":"1","cache_read_per_million":"0","cache_write_per_million":"0"}`, "", true, 503},
		{"assistant settings", "GetPortalSettings", m.handleAssistant, `{"messages":[{"role":"user","content":"help"}]}`, "", false, 503},
		{"assistant prices", "ListPointPrices", m.handleAssistant, `{"messages":[{"role":"user","content":"help"}]}`, "", false, 503},
		{"assistant allowance", "ConsumeHelperAllowance", m.handleAssistant, `{"messages":[{"role":"user","content":"help"}]}`, "", false, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.store = &failureStore{Store: f.store, operation: tc.operation, failure: errors.New("synthetic-private-database-failure")}
			defer func() { m.store = f.store }()
			w := invokePortalHandler(tc.handler, u.ID, tc.admin, tc.body, tc.query)
			requirePortalStatus(t, w, tc.want)
			if strings.Contains(w.Body.String(), "synthetic-private") {
				t.Fatal("storage internals exposed")
			}
		})
	}
	m.store = f.store
}

func TestPortalMalformedBodiesAndExplicitConflicts(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "bad-body@example.test")
	f.updateUser(t, u.ID, "active", true)
	for _, path := range []string{"/register", "/login", "/change-password", "/api-keys", "/topup", "/topup/unknown/submit", "/admin/credit", "/admin/users/unknown/limits", "/admin/accounts/alias"} {
		requirePortalStatus(t, f.rawRequest("POST", path, token, []byte("{")), 400)
	}
	for _, tc := range []struct {
		operation string
		handler   gin.HandlerFunc
		failure   error
		want      int
		body      string
	}{
		{"SetUserLimits", f.module.handleAdminSetUserLimits, store.ErrUserNotFound, 404, "{}"},
		{"SubmitTopupTxHash", f.module.handleSubmitTopupTxHash, store.ErrTopupTxHashTaken, 409, `{"tx_hash":"synthetic"}`},
		{"ConfirmTopupOrder", f.module.handleAdminConfirmTopupOrder, store.ErrTopupOrderNotFound, 404, "{}"},
		{"CreateTopupOrder", f.module.handleCreateTopupOrder, store.ErrTopupAmountCollision, 409, `{"amount":10,"network":"TRC20"}`},
	} {
		f.module.store = &failureStore{Store: f.store, operation: tc.operation, failure: tc.failure}
		requirePortalStatus(t, invokePortalHandler(tc.handler, u.ID, true, tc.body, ""), tc.want)
	}
	f.module.store = f.store
	f.module.keyGen = func() (string, error) { return "", errors.New("synthetic-entropy-failure") }
	requirePortalStatus(t, f.request(t, "POST", "/api-keys", token, gin.H{}), 500)
	f.module.topup = nil
	requirePortalStatus(t, f.request(t, "GET", "/topup/methods", token, nil), 200)
	requirePortalStatus(t, f.request(t, "POST", "/topup", token, gin.H{"amount": 1}), 503)
}
