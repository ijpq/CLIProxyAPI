package portal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

type portalDBFixture struct {
	store  *store.PostgresStore
	db     *sql.DB
	schema string
	module *Module
	router *gin.Engine
}

// Tests require an explicit disposable database; production settings are never read.
func newPortalDBFixture(t *testing.T) *portalDBFixture {
	t.Helper()
	dsn := os.Getenv("BILLING_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BILLING_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	if err != nil || (!strings.Contains(u.Path, "test") && !strings.Contains(u.Path, "audit")) {
		t.Fatal("integration database name must contain test or audit")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "portal_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	pg, err := store.NewPostgresStore(context.Background(), store.PostgresStoreConfig{DSN: dsn, Schema: schema, SpoolDir: t.TempDir()})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pg.Close()
		if _, err := db.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	if err := pg.EnsureBillingSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pg.GetPortalSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.CNYPointsPerUnit, cfg.USDTPointsPerUnit = "1", "1"
	if _, err = pg.SavePortalSettings(context.Background(), cfg, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureBillingSchema(context.Background()); err != nil {
		t.Fatalf("schema is not idempotent: %v", err)
	}
	t.Setenv("BILLING_USDT_TRC20", "TDisposableTestWallet")
	m := New(pg, billing.NewTokenIssuer("disposable-portal-test-secret", time.Hour), billing.LoadTopupConfigFromEnv())
	r := gin.New()
	m.RegisterRoutes(r.Group("/portal"))
	return &portalDBFixture{store: pg, db: db, schema: schema, module: m, router: r}
}

func (f *portalDBFixture) request(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	return f.rawRequest(method, path, token, data)
}

func (f *portalDBFixture) rawRequest(method, path, token string, data []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/portal"+path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func requirePortalStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; response = %s", w.Code, want, w.Body.String())
	}
}

func (f *portalDBFixture) register(t *testing.T, email string) (store.User, string) {
	t.Helper()
	w := f.request(t, "POST", "/register", "", gin.H{"email": email, "password": "Disposable-test-password-9"})
	requirePortalStatus(t, w, http.StatusCreated)
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	u, err := f.store.GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return u, response.Token
}

func (f *portalDBFixture) updateUser(t *testing.T, id, status string, admin bool) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE "`+f.schema+`".users SET status=$2, is_admin=$3 WHERE id=$1`, id, status, admin); err != nil {
		t.Fatal(err)
	}
}

func (f *portalDBFixture) order(t *testing.T, userID, amount string) store.TopupOrder {
	t.Helper()
	o, err := f.store.CreateTopupOrder(context.Background(), userID, "usdt", amount, "USDT", "TRC20", "TDisposableTestWallet", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPortalPostgresRegistrationAndKeys(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "Alice@example.test")
	_, otherToken := f.register(t, "bob@example.test")
	requirePortalStatus(t, f.request(t, "POST", "/register", "", gin.H{"email": "alice@example.test", "password": "Disposable-test-password-9"}), 409)
	requirePortalStatus(t, f.request(t, "POST", "/login", "", gin.H{"email": "alice@example.test", "password": "wrong"}), 401)
	requirePortalStatus(t, f.request(t, "POST", "/login", "", gin.H{"email": "ALICE@example.test", "password": "Disposable-test-password-9"}), 200)
	w := f.request(t, "POST", "/api-keys", token, gin.H{"name": "test key"})
	requirePortalStatus(t, w, 201)
	var key struct {
		ID  string `json:"id"`
		Raw string `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	lookup, err := f.store.LookupAPIKey(context.Background(), store.HashAPIKey(key.Raw))
	if err != nil || lookup.UserID != u.ID {
		t.Fatalf("key owner: %#v, %v", lookup, err)
	}
	w = f.request(t, "GET", "/api-keys", token, nil)
	requirePortalStatus(t, w, 200)
	if strings.Contains(w.Body.String(), key.Raw) || strings.Contains(w.Body.String(), "key_hash") {
		t.Fatal("key list exposes key material")
	}
	requirePortalStatus(t, f.request(t, "DELETE", "/api-keys/"+key.ID, otherToken, nil), 404)
	requirePortalStatus(t, f.request(t, "DELETE", "/api-keys/"+key.ID, token, nil), 204)
	if _, err := f.store.LookupAPIKey(context.Background(), store.HashAPIKey(key.Raw)); !errors.Is(err, store.ErrAPIKeyNotFound) {
		t.Fatalf("revoked key accepted: %v", err)
	}
	requirePortalStatus(t, f.request(t, "GET", "/wallet", "", nil), 401)
	requirePortalStatus(t, f.request(t, "GET", "/admin/users", token, nil), 403)
}

func TestPortalPostgresSessionRevalidation(t *testing.T) {
	for _, tc := range []struct {
		name, status             string
		claimAdmin, currentAdmin bool
		path                     string
		want                     int
	}{
		{"suspended session", "suspended", false, false, "/wallet", 403},
		{"demoted administrator", "active", true, false, "/admin/users", 403},
		{"newly promoted administrator", "active", false, true, "/admin/users", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPortalDBFixture(t)
			u, _ := f.register(t, "session@example.test")
			token, err := f.module.tokens.Issue(u.ID, tc.claimAdmin)
			if err != nil {
				t.Fatal(err)
			}
			f.updateUser(t, u.ID, tc.status, tc.currentAdmin)
			requirePortalStatus(t, f.request(t, "GET", tc.path, token, nil), tc.want)
		})
	}
}

func TestPortalPostgresSuspendedAPIKeys(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "suspended@example.test")
	w := f.request(t, "POST", "/api-keys", token, gin.H{})
	requirePortalStatus(t, w, 201)
	var key struct {
		ID  string `json:"id"`
		Raw string `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	f.updateUser(t, u.ID, "suspended", false)
	for _, lookup := range []func() (store.APIKeyLookup, error){
		func() (store.APIKeyLookup, error) {
			return f.store.LookupAPIKey(context.Background(), store.HashAPIKey(key.Raw))
		},
		func() (store.APIKeyLookup, error) { return f.store.LookupAPIKeyByID(context.Background(), key.ID) },
	} {
		if _, err := lookup(); !errors.Is(err, store.ErrAPIKeyNotFound) {
			t.Errorf("suspended owner key accepted: %v", err)
		}
	}
}

type noOpPortalEmail struct{}

func (noOpPortalEmail) Send(context.Context, string, string, string) error { return nil }

func TestPortalPostgresEmailLoginRejectsSuspendedUser(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "email@example.test")
	f.updateUser(t, u.ID, "suspended", false)
	codes := billing.NewLoginCodeStore(time.Minute, time.Minute)
	f.module.SetEmailLogin(noOpPortalEmail{}, codes)
	code, err := codes.Generate(u.Email)
	if err != nil {
		t.Fatal(err)
	}
	requirePortalStatus(t, f.request(t, "POST", "/login/code/verify", "", gin.H{"email": u.Email, "code": code}), 403)
}

func TestPortalPostgresMalformedMutations(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "malformed@example.test")
	f.updateUser(t, u.ID, "active", true)
	adminToken, err := f.module.tokens.Issue(u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	o := f.order(t, u.ID, "9")
	requirePortalStatus(t, f.rawRequest("POST", "/api-keys", token, []byte("{")), 400)
	requirePortalStatus(t, f.rawRequest("POST", "/admin/topup/"+o.ID+"/confirm", adminToken, []byte("{")), 400)
	if bal, err := f.store.GetWalletBalance(context.Background(), u.ID); err != nil || bal != "0.000000" {
		t.Fatalf("malformed confirmation changed balance: %s, %v", bal, err)
	}
}

func TestPortalPostgresTopupHashUniquenessAndImmutability(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "hash@example.test")
	one, two := f.order(t, u.ID, "5"), f.order(t, u.ID, "6")
	hash := strings.Repeat("ab", 32)
	if _, err := f.store.SubmitTopupTxHash(context.Background(), u.ID, one.ID, strings.ToUpper(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SubmitTopupTxHash(context.Background(), u.ID, two.ID, hash); !errors.Is(err, store.ErrTopupTxHashTaken) {
		t.Errorf("case variant reused payment: %v", err)
	}
	if _, err := f.store.SubmitTopupTxHash(context.Background(), u.ID, one.ID, strings.Repeat("cd", 32)); !errors.Is(err, store.ErrTopupOrderNotPending) {
		t.Errorf("submitted evidence was replaced: %v", err)
	}
}

func TestPortalPostgresExpiredTopupTransitions(t *testing.T) {
	for _, transition := range []string{"submit", "confirm"} {
		t.Run(transition, func(t *testing.T) {
			f := newPortalDBFixture(t)
			u, _ := f.register(t, "expired@example.test")
			o := f.order(t, u.ID, "8")
			if _, err := f.db.Exec(`UPDATE "`+f.schema+`".topup_orders SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, o.ID); err != nil {
				t.Fatal(err)
			}
			var err error
			if transition == "submit" {
				_, err = f.store.SubmitTopupTxHash(context.Background(), u.ID, o.ID, strings.Repeat("ef", 32))
			} else {
				_, err = f.store.ConfirmTopupOrder(context.Background(), o.ID, "")
			}
			if !errors.Is(err, store.ErrTopupOrderNotPending) {
				t.Fatalf("expired order transitioned: %v", err)
			}
		})
	}
}

func TestPortalPostgresConcurrentConfirmation(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "concurrent@example.test")
	o := f.order(t, u.ID, "7")
	var callbacks atomic.Int32
	f.module.SetWalletChangeHook(func(string) { callbacks.Add(1) })
	f.updateUser(t, u.ID, "active", true)
	token, err := f.module.tokens.Issue(u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan int, 12)
	for range 12 {
		go func() {
			<-start
			w := f.rawRequest("POST", "/admin/topup/"+o.ID+"/confirm", token, []byte("{}"))
			results <- w.Code
		}()
	}
	close(start)
	success := 0
	for range 12 {
		code := <-results
		if code == 200 {
			success++
		} else if code != 409 {
			t.Errorf("unexpected confirmation status %d", code)
		}
	}
	if success != 1 || callbacks.Load() != 1 {
		t.Fatalf("successes=%d, cache invalidations=%d", success, callbacks.Load())
	}
	bal, err := f.store.GetWalletBalance(context.Background(), u.ID)
	if err != nil || bal != "7.000000" {
		t.Fatalf("balance=%s, err=%v", bal, err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM "`+f.schema+`".transactions WHERE reference=$1`, o.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ledger entries=%d, err=%v", count, err)
	}
}

func TestPortalPostgresWalletUsageAndTenantIsolation(t *testing.T) {
	f := newPortalDBFixture(t)
	a, tokenA := f.register(t, "usage-a@example.test")
	_, tokenB := f.register(t, "usage-b@example.test")
	if _, err := f.store.AdminCreditWallet(context.Background(), a.ID, "10", "test-credit", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordUsageAndDebit(context.Background(), store.MeteredUsage{UserID: a.ID, RequestID: "synthetic-request", Provider: "openai", Model: "test-model", InputTokens: 100, OutputTokens: 10, Cost: "2.5"}); err != nil {
		t.Fatal(err)
	}
	w := f.request(t, "GET", "/wallet", tokenA, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "7.500000") {
		t.Fatalf("incorrect wallet: %s", w.Body.String())
	}
	for _, path := range []string{"/usage", "/usage/filters", "/usage/stats?days=7"} {
		w = f.request(t, "GET", path, tokenA, nil)
		requirePortalStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), "test-model") {
			t.Fatalf("missing usage at %s: %s", path, w.Body.String())
		}
		w = f.request(t, "GET", path+map[bool]string{true: "&", false: "?"}[strings.Contains(path, "?")]+"user_id="+a.ID, tokenB, nil)
		requirePortalStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "test-model") {
			t.Fatalf("cross-tenant usage at %s", path)
		}
	}
	if err := f.store.RecordUsageAndDebit(context.Background(), store.MeteredUsage{UserID: a.ID, Model: "free-audit", Cost: "1", SkipDebit: true}); err != nil {
		t.Fatal(err)
	}
	if bal, err := f.store.GetWalletBalance(context.Background(), a.ID); err != nil || bal != "7.500000" {
		t.Fatalf("unbilled record changed balance: %s %v", bal, err)
	}
}

func TestPortalPostgresTopupOwnership(t *testing.T) {
	f := newPortalDBFixture(t)
	a, _ := f.register(t, "order-a@example.test")
	_, tokenB := f.register(t, "order-b@example.test")
	o := f.order(t, a.ID, "3")
	requirePortalStatus(t, f.request(t, "GET", "/topup/"+o.ID, tokenB, nil), 404)
	requirePortalStatus(t, f.request(t, "POST", "/topup/"+o.ID+"/cancel", tokenB, nil), 409)
	requirePortalStatus(t, f.request(t, "POST", "/topup/"+o.ID+"/submit", tokenB, gin.H{"tx_hash": strings.Repeat("af", 32)}), 409)
	unchanged, err := f.store.GetTopupOrder(context.Background(), o.ID, a.ID)
	if err != nil || unchanged.Status != "pending" {
		t.Fatalf("foreign order changed: %#v %v", unchanged, err)
	}
}

func TestPortalPostgresInvalidCreditMetadata(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "metadata@example.test")
	if _, err := f.store.AdminCreditWallet(context.Background(), u.ID, "1", "test", "control\x01character"); err != nil {
		t.Fatalf("credit note is not JSON-safe: %v", err)
	}
}

func TestPortalPostgresQRCodeSurvivesOrderReload(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "qr@example.test")
	order, err := f.store.CreateTopupOrder(context.Background(), u.ID, "wechat", "10.37", "CNY", "", "https://payments.example.test/test-qr.png", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w := f.request(t, "GET", "/topup/"+order.ID, token, nil)
	requirePortalStatus(t, w, 200)
	var response struct {
		QRCodeURL string `json:"qr_code_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.QRCodeURL != order.WalletAddress {
		t.Fatalf("reloaded QR code missing: %q", response.QRCodeURL)
	}
}

func TestPortalPostgresCustomerAccountPrivacyAndFiltering(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "privacy@example.test")
	other, _ := f.register(t, "privacy-other@example.test")
	const privateID = "synthetic-operator@example.test.json"
	const otherID = "synthetic-other-account@example.test.json"
	if err := f.store.SetUserLimits(context.Background(), u.ID, false, nil, []string{privateID}, false); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ userID, authID, model string }{
		{u.ID, privateID, "own-model"}, {other.ID, otherID, "foreign-model"},
	} {
		if err := f.store.RecordUsageAndDebit(context.Background(), store.MeteredUsage{
			UserID: item.userID, Model: item.model, AuthID: item.authID, AuthLabel: item.authID,
			Cost: "0", SkipDebit: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/me", "/account-labels", "/usage", "/usage/filters"} {
		w := f.request(t, "GET", path, token, nil)
		requirePortalStatus(t, w, 200)
		if strings.Contains(w.Body.String(), privateID) || strings.Contains(w.Body.String(), otherID) {
			t.Errorf("upstream credential filename exposed at %s", path)
		}
		if !strings.Contains(w.Body.String(), billing.AccountHandle(privateID)) {
			t.Errorf("customer account handle missing at %s", path)
		}
	}
	w := f.request(t, "GET", "/usage?auth_id="+billing.AccountHandle(privateID), token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "own-model") || strings.Contains(w.Body.String(), "foreign-model") {
		t.Errorf("account handle filter did not resolve only own usage: %s", w.Body.String())
	}
	requirePortalStatus(t, f.request(t, "GET", "/usage?auth_id="+billing.AccountHandle(otherID), token, nil), 400)
	f.updateUser(t, u.ID, "active", true)
	w = f.request(t, "GET", "/admin/users", token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), privateID) {
		t.Fatal("administrator ACL editor lost raw account IDs")
	}
}

func TestPortalPostgresAdminLimitsAndQuotaAuthorization(t *testing.T) {
	f := newPortalDBFixture(t)
	admin, adminToken := f.register(t, "admin-limits@example.test")
	u, token := f.register(t, "limits@example.test")
	f.updateUser(t, admin.ID, "active", true)
	policy, err := NewSitePolicy("portal.example.test", "example.com", admin.ID+","+u.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.module.SetSitePolicy(policy)
	const ownID, foreignID = "private-own@example.test.json", "private-foreign@example.test.json"
	billing.SetAccountLister(func() []billing.Account {
		return []billing.Account{{ID: ownID, Provider: "antigravity"}, {ID: foreignID, Provider: "antigravity"}}
	})
	billing.SetModelLister(func() []string { return []string{"allowed-model"} })
	var quotaLookups atomic.Int32
	billing.SetAuthResolver(func(id string) (billing.UpstreamAuth, bool) {
		quotaLookups.Add(1)
		if id != ownID {
			t.Errorf("quota resolver received unauthorized account")
		}
		return billing.UpstreamAuth{ID: id, Provider: "antigravity", AntigravityCredits: &billing.AntigravityCredits{Known: true, Available: true, CreditAmount: 10}}, true
	})
	t.Cleanup(func() {
		billing.SetAccountLister(nil)
		billing.SetModelLister(nil)
		billing.SetAuthResolver(nil)
	})
	path := "/admin/users/" + u.ID + "/limits"
	limits := gin.H{"allowed_models": []string{"allowed-model"}, "allowed_auth_ids": []string{ownID}, "allow_reset": false}
	requirePortalStatus(t, f.request(t, "POST", path, token, limits), 403)
	requirePortalStatus(t, f.request(t, "POST", path, adminToken, gin.H{"allowed_models": []string{"unknown-model"}}), 400)
	requirePortalStatus(t, f.request(t, "POST", path, adminToken, gin.H{"allowed_auth_ids": []string{"unknown-account"}}), 400)
	requirePortalStatus(t, f.request(t, "POST", path, adminToken, limits), 200)
	updated, err := f.store.GetUserByID(context.Background(), u.ID)
	if err != nil || len(updated.AllowedAuthIDs) != 1 || updated.AllowedAuthIDs[0] != ownID || len(updated.AllowedModels) != 1 || updated.AllowReset {
		t.Fatalf("unexpected persisted limits: %#v, %v", updated, err)
	}
	for _, path := range []string{"/quota/" + billing.AccountHandle(foreignID), "/quota/unknown"} {
		requirePortalStatus(t, f.request(t, "GET", path, token, nil), 404)
	}
	requirePortalStatus(t, f.request(t, "POST", "/quota/"+billing.AccountHandle(ownID)+"/reset", token, nil), 403)
	if quotaLookups.Load() != 0 {
		t.Fatal("denied request reached upstream quota resolver")
	}
	for _, path := range []string{"/quota", "/quota/" + billing.AccountHandle(ownID)} {
		w := f.request(t, "GET", path, token, nil)
		requirePortalStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), billing.AccountHandle(ownID)) || strings.Contains(w.Body.String(), ownID) || strings.Contains(w.Body.String(), foreignID) {
			t.Fatalf("quota response violates account visibility: %s", w.Body.String())
		}
	}
	limits["allow_reset"] = true
	requirePortalStatus(t, f.request(t, "POST", path, adminToken, limits), 200)
	requirePortalStatus(t, f.request(t, "POST", "/quota/"+billing.AccountHandle(foreignID)+"/reset", token, nil), 404)
	if quotaLookups.Load() != 2 {
		t.Fatal("forbidden reset reached quota resolver")
	}
}

func TestPortalPostgresPasswordChange(t *testing.T) {
	f := newPortalDBFixture(t)
	_, token := f.register(t, "password@example.test")
	change := gin.H{"old_password": "wrong-password", "new_password": "New-disposable-password-8"}
	requirePortalStatus(t, f.request(t, "POST", "/change-password", token, change), 401)
	change["old_password"] = "Disposable-test-password-9"
	requirePortalStatus(t, f.request(t, "POST", "/change-password", token, change), 200)
	requirePortalStatus(t, f.request(t, "POST", "/login", "", gin.H{"email": "password@example.test", "password": "Disposable-test-password-9"}), 401)
	requirePortalStatus(t, f.request(t, "POST", "/login", "", gin.H{"email": "password@example.test", "password": change["new_password"]}), 200)
}

func TestPortalPostgresSessionsRejectDeletedUserAndDatabaseFailure(t *testing.T) {
	for _, scenario := range []string{"deleted user", "database failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPortalDBFixture(t)
			u, token := f.register(t, "session-failure@example.test")
			want := 401
			if scenario == "deleted user" {
				if _, err := f.db.Exec(`DELETE FROM "`+f.schema+`".users WHERE id=$1`, u.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
				want = 503
			}
			requirePortalStatus(t, f.request(t, "GET", "/wallet", token, nil), want)
		})
	}
}

func TestPortalPostgresTopupCreationCancellationAndQRReload(t *testing.T) {
	f := newPortalDBFixture(t)
	_, token := f.register(t, "payment-flow@example.test")
	t.Setenv("BILLING_WECHAT_QR_URL", "https://payments.example.test/current-qr.png")
	f.module.topup = billing.LoadTopupConfigFromEnv()
	requirePortalStatus(t, f.request(t, "POST", "/topup", token, gin.H{"method": "usdt", "network": "TRC20", "amount": -1}), 400)
	requirePortalStatus(t, f.request(t, "POST", "/topup", token, gin.H{"method": "unknown", "amount": 10}), 400)
	w := f.request(t, "POST", "/topup", token, gin.H{"method": "wechat", "amount": 10})
	requirePortalStatus(t, w, 201)
	var order struct {
		ID, Amount, Currency string
		QRCodeURL            string `json:"qr_code_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	amount, err := strconv.ParseFloat(order.Amount, 64)
	if err != nil || amount <= 10 || amount > 10.99 || order.Currency != "CNY" || order.QRCodeURL == "" {
		t.Fatalf("incorrect unique-amount order: %#v %v", order, err)
	}
	// Reconfiguration must not replace payment instructions for an existing order.
	t.Setenv("BILLING_WECHAT_QR_URL", "https://payments.example.test/rotated-qr.png")
	f.module.topup = billing.LoadTopupConfigFromEnv()
	w = f.request(t, "GET", "/topup/"+order.ID, token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), order.QRCodeURL) {
		t.Fatal("payment address snapshot changed during rotation")
	}
	requirePortalStatus(t, f.request(t, "POST", "/topup/"+order.ID+"/cancel", token, nil), 204)
	requirePortalStatus(t, f.request(t, "POST", "/topup/"+order.ID+"/cancel", token, nil), 409)
	w = f.request(t, "GET", "/topup", token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "cancelled") {
		t.Fatal("cancelled order absent from history")
	}
}

func TestPortalPostgresConcurrentDebitsPreserveLedger(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "concurrent-debits@example.test")
	if _, err := f.store.AdminCreditWallet(context.Background(), u.ID, "10", "test-credit", ""); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 16)
	for i := range 16 {
		go func() {
			<-start
			results <- f.store.RecordUsageAndDebit(context.Background(), store.MeteredUsage{
				UserID: u.ID, RequestID: "concurrent-" + strconv.Itoa(i), Cost: "0.125", Model: "concurrent-model",
			})
		}()
	}
	close(start)
	for range 16 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if balance, err := f.store.GetWalletBalance(context.Background(), u.ID); err != nil || balance != "8.000000" {
		t.Fatalf("lost a concurrent debit: balance=%s err=%v", balance, err)
	}
	var usageCount, ledgerCount int
	var ledgerSum string
	if err := f.db.QueryRow(`SELECT count(*) FROM "`+f.schema+`".usage_records WHERE user_id=$1`, u.ID).Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT count(*), sum(amount)::text FROM "`+f.schema+`".transactions WHERE user_id=$1 AND type='consume'`, u.ID).Scan(&ledgerCount, &ledgerSum); err != nil {
		t.Fatal(err)
	}
	if usageCount != 16 || ledgerCount != 16 || ledgerSum != "-2.000000" {
		t.Fatalf("usage=%d ledger=%d amount=%s", usageCount, ledgerCount, ledgerSum)
	}
}

func TestPortalPostgresFailedDebitRollsBackUsageAndWallet(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "rollback@example.test")
	if _, err := f.store.AdminCreditWallet(context.Background(), u.ID, "10", "test-credit", ""); err != nil {
		t.Fatal(err)
	}
	// Force a failure after usage insertion and wallet subtraction, before commit.
	if _, err := f.db.Exec(`ALTER TABLE "` + f.schema + `".transactions ADD CONSTRAINT audit_reject_consumption CHECK (type <> 'consume')`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordUsageAndDebit(context.Background(), store.MeteredUsage{UserID: u.ID, RequestID: "must-rollback", Cost: "2"}); err == nil {
		t.Fatal("injected ledger failure was ignored")
	}
	if balance, err := f.store.GetWalletBalance(context.Background(), u.ID); err != nil || balance != "10.000000" {
		t.Fatalf("failed transaction changed wallet: balance=%s err=%v", balance, err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM "` + f.schema + `".usage_records WHERE request_id='must-rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed transaction left partial usage: count=%d err=%v", count, err)
	}
}
