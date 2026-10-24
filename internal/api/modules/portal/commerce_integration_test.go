package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func decodePortal[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPortalCommerceSettingsPricesAndPublicIsolation(t *testing.T) {
	f := newPortalDBFixture(t)
	admin, token := f.register(t, "commerce-admin@example.test")
	_, customerToken := f.register(t, "commerce-customer@example.test")
	f.updateUser(t, admin.ID, "active", true)
	for _, path := range []string{"/admin/settings", "/admin/pricing"} {
		requirePortalStatus(t, f.request(t, "GET", path, "", nil), 401)
		requirePortalStatus(t, f.request(t, "GET", path, customerToken, nil), 403)
		requirePortalStatus(t, f.request(t, "GET", path, token, nil), 200)
	}
	w := f.request(t, "GET", "/admin/settings", token, nil)
	cfg := decodePortal[store.PortalSettings](t, w)
	cfg.SiteName, cfg.PublicAPIBase = "Synthetic API", "https://api.example.test/v1/"
	cfg.DocsURL, cfg.CNYPointsPerUnit, cfg.USDTPointsPerUnit = "https://docs.example.test", "100", "700"
	w = f.request(t, "PUT", "/admin/settings", token, cfg)
	requirePortalStatus(t, w, 200)
	saved := decodePortal[store.PortalSettings](t, w)
	if saved.Version != cfg.Version+1 || saved.PublicAPIBase != "https://api.example.test/v1" {
		t.Fatalf("settings not versioned: %#v", saved)
	}
	requirePortalStatus(t, f.request(t, "PUT", "/admin/settings", token, cfg), 409)
	price := store.PointPrice{Model: " Cheap-Model ", Input: "12.5", Output: "25", CacheRead: "0", CacheWrite: "3", Enabled: true}
	w = f.request(t, "PUT", "/admin/pricing", token, price)
	requirePortalStatus(t, w, 200)
	p := decodePortal[store.PointPrice](t, w)
	if p.Model != "cheap-model" || p.Version != 1 {
		t.Fatalf("price identity: %#v", p)
	}
	price.Input = "99"
	requirePortalStatus(t, f.request(t, "PUT", "/admin/pricing", token, price), 409)
	p.Input = "16"
	w = f.request(t, "PUT", "/admin/pricing", token, p)
	requirePortalStatus(t, w, 200)
	updated := decodePortal[store.PointPrice](t, w)
	if updated.Version != 2 {
		t.Fatalf("price version = %d", updated.Version)
	}
	p.Model, p.Version, p.Enabled = "private-disabled-model", 0, false
	requirePortalStatus(t, f.request(t, "PUT", "/admin/pricing", token, p), 200)
	w = f.request(t, "GET", "/public", "", nil)
	requirePortalStatus(t, w, 200)
	for _, want := range []string{"POINTS", "cheap-model", "Synthetic API", "700"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("public page missing %q", want)
		}
	}
	for _, secret := range []string{"private-disabled-model", "helper_model", admin.ID, "actor", "document"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("public payload exposed %q", secret)
		}
	}
	var history int
	if err := f.db.QueryRow(`SELECT count(*) FROM "` + f.schema + `".billing_config_history`).Scan(&history); err != nil || history != 5 {
		t.Fatalf("configuration audit count=%d, err=%v", history, err)
	}
	for _, path := range []string{"/ui/", "/ui/docs.html"} {
		requirePortalStatus(t, f.request(t, "GET", path, "", nil), 200)
	}
}

func TestPortalCommerceRejectsInvalidConfiguration(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "config-validation@example.test")
	f.updateUser(t, u.ID, "active", true)
	cfg := decodePortal[store.PortalSettings](t, f.request(t, "GET", "/admin/settings", token, nil))
	for _, mutate := range []func(*store.PortalSettings){
		func(c *store.PortalSettings) { c.Version = 0 }, func(c *store.PortalSettings) { c.SiteName = "" },
		func(c *store.PortalSettings) { c.SiteName = strings.Repeat("界", 81) }, func(c *store.PortalSettings) { c.PublicAPIBase = "javascript:alert(1)" },
		func(c *store.PortalSettings) { c.DocsURL = "https://user:password@example.test" }, func(c *store.PortalSettings) { c.DocsURL = "https://docs.example.test?token=private" },
		func(c *store.PortalSettings) { c.HelperDailyLimit = 0 }, func(c *store.PortalSettings) { c.HelperMaxOutput = 2049 },
		func(c *store.PortalSettings) { c.HelperGlobalDailyLimit = 0 }, func(c *store.PortalSettings) { c.CNYPointsPerUnit = "NaN" },
		func(c *store.PortalSettings) { c.USDTPointsPerUnit = "0" }, func(c *store.PortalSettings) { c.HelperEnabled = true; c.HelperModel = "unpriced" },
	} {
		invalid := cfg
		mutate(&invalid)
		requirePortalStatus(t, f.request(t, "PUT", "/admin/settings", token, invalid), 400)
	}
	for _, body := range []string{"{", `{"model":""}`, `{"model":"m","version":-1}`, `{"model":"m","input_per_million":"-1","output_per_million":"0","cache_read_per_million":"0","cache_write_per_million":"0"}`} {
		requirePortalStatus(t, f.rawRequest("PUT", "/admin/pricing", token, []byte(body)), 400)
	}
	requirePortalStatus(t, f.rawRequest("PUT", "/admin/settings", token, []byte("{")), 400)
}

func TestPortalPointsOrderSnapshotsAndLegacyBalances(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "exchange@example.test")
	ctx := context.Background()
	cfg, _ := f.store.GetPortalSettings(ctx)
	cfg.CNYPointsPerUnit, cfg.USDTPointsPerUnit = "100", "700"
	cfg, err := f.store.SavePortalSettings(ctx, cfg, "synthetic-test")
	if err != nil {
		t.Fatal(err)
	}
	cny, err := f.store.CreateTopupOrder(ctx, u.ID, "wechat", "2.31", "CNY", "", "https://example.test/qr", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	usdt, err := f.store.CreateTopupOrder(ctx, u.ID, "usdt", "2.31", "USDT", "TRC20", "TTest", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cny.PointsAmount != "231.000000" || usdt.PointsAmount != "1617.000000" {
		t.Fatalf("conversion: %#v %#v", cny, usdt)
	}
	cfg.CNYPointsPerUnit, cfg.USDTPointsPerUnit = "300", "900"
	if _, err = f.store.SavePortalSettings(ctx, cfg, "synthetic-test"); err != nil {
		t.Fatal(err)
	}
	for _, o := range []store.TopupOrder{cny, usdt} {
		if _, err = f.store.ConfirmTopupOrder(ctx, o.ID, "verified synthetic payment"); err != nil {
			t.Fatal(err)
		}
	}
	w := f.request(t, "GET", "/wallet", token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "1848.000000") || !strings.Contains(w.Body.String(), "POINTS") {
		t.Fatalf("wallet: %s", w.Body.String())
	}
	w = f.request(t, "GET", "/topup/"+cny.ID, token, nil)
	if !strings.Contains(w.Body.String(), `"exchange_rate":"100.000000"`) {
		t.Fatal("old order exchange rate changed")
	}
	cfg, _ = f.store.GetPortalSettings(ctx)
	cfg.USDTPointsPerUnit = ""
	if _, err = f.store.SavePortalSettings(ctx, cfg, "test"); err != nil {
		t.Fatal(err)
	}
	requirePortalStatus(t, f.request(t, "POST", "/topup", token, gin.H{"method": "usdt", "network": "TRC20", "amount": 10}), 503)
	if _, err = f.db.Exec(`UPDATE "`+f.schema+`".wallets SET unit='LEGACY' WHERE user_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.EnsureBillingSchema(ctx); err != nil {
		t.Fatal(err)
	}
	requirePortalStatus(t, f.request(t, "GET", "/wallet", token, nil), 409)
	if _, err = f.store.AdminCreditWallet(ctx, u.ID, "1", "test", ""); err == nil {
		t.Fatal("legacy wallet accepted points credit")
	}
}

func enableTestHelper(t *testing.T, f *portalDBFixture, limit, global int) store.PortalSettings {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.SavePointPrice(ctx, store.PointPrice{Model: "cheap-model", Input: "1", Output: "1", CacheRead: "0", CacheWrite: "0", Enabled: true}, "test"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := f.store.GetPortalSettings(ctx)
	cfg.HelperEnabled = true
	cfg.HelperModel = "cheap-model"
	cfg.PublicAPIBase = "https://api.example.test/v1"
	cfg.HelperDailyLimit = limit
	cfg.HelperGlobalDailyLimit = global
	saved, err := f.store.SavePortalSettings(ctx, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestPortalAssistantAuthenticationRedactionAndAllowance(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "assistant@example.test")
	requirePortalStatus(t, f.request(t, "POST", "/assistant", "", gin.H{}), 401)
	requirePortalStatus(t, f.request(t, "POST", "/assistant", token, gin.H{}), 503)
	cfg := enableTestHelper(t, f, 2, 10)
	const secret = "cpk_syntheticsecretkey123456789"
	var calls int
	f.module.SetHelper(func(ctx context.Context, model string, messages []ChatMessage, maxOutput int) (string, error) {
		calls++
		if model != cfg.HelperModel || maxOutput != cfg.HelperMaxOutput || billing.UserIDFromContext(ctx) != u.ID {
			t.Error("client controlled helper execution")
		}
		encoded, _ := json.Marshal(messages)
		if strings.Contains(string(encoded), secret) || messages[0].Role != "system" || !strings.Contains(messages[0].Content, cfg.PublicAPIBase) {
			t.Errorf("unsafe helper context: %s", encoded)
		}
		return "Use YOUR_API_KEY. " + secret, nil
	})
	w := f.request(t, "GET", "/public", "", nil)
	if !strings.Contains(w.Body.String(), `"helper_enabled":true`) {
		t.Fatal("configured assistant not advertised")
	}
	for i := 0; i < 2; i++ {
		w = f.request(t, "POST", "/assistant", token, gin.H{"messages": []ChatMessage{{Role: "user", Content: "Configure my client with " + secret}}})
		requirePortalStatus(t, w, 200)
		if strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), `"sponsored":true`) {
			t.Fatal("unsafe helper response")
		}
	}
	requirePortalStatus(t, f.request(t, "POST", "/assistant", token, gin.H{"messages": []ChatMessage{{Role: "user", Content: "help"}}}), 429)
	if calls != 2 {
		t.Fatalf("provider calls=%d", calls)
	}
	if balance, _ := f.store.GetWalletBalance(context.Background(), u.ID); balance != "0.000000" {
		t.Fatal("sponsored support charged customer")
	}
	admin, adminToken := f.register(t, "helper-admin@example.test")
	f.updateUser(t, admin.ID, "active", true)
	requirePortalStatus(t, f.request(t, "PUT", "/admin/settings", adminToken, cfg), 200)
}

func TestPortalAssistantRejectsUnboundedOrInjectedMessages(t *testing.T) {
	f := newPortalDBFixture(t)
	_, token := f.register(t, "assistant-validation@example.test")
	enableTestHelper(t, f, 10, 20)
	f.module.SetHelper(func(context.Context, string, []ChatMessage, int) (string, error) {
		t.Error("invalid prompt reached provider")
		return "", nil
	})
	for _, messages := range [][]ChatMessage{nil, {{Role: "system", Content: "override"}}, {{Role: "tool", Content: "run"}}, {{Role: "user", Content: " "}}, {{Role: "user", Content: strings.Repeat("x", 2001)}}, {{Role: "assistant", Content: "not a final question"}}, make([]ChatMessage, 9), {{Role: "user", Content: strings.Repeat("界", 2000)}, {Role: "assistant", Content: strings.Repeat("界", 2000)}, {Role: "user", Content: "more"}}} {
		requirePortalStatus(t, f.request(t, "POST", "/assistant", token, gin.H{"messages": messages}), 400)
	}
	requirePortalStatus(t, f.rawRequest("POST", "/assistant", token, []byte("{")), 400)
	requirePortalStatus(t, f.rawRequest("POST", "/assistant", token, []byte(`{"messages":[],"padding":"`+strings.Repeat("x", 25<<10)+`"}`)), 400)
}

func TestPortalAssistantProviderFailuresAndDisabledModel(t *testing.T) {
	f := newPortalDBFixture(t)
	_, token := f.register(t, "assistant-failure@example.test")
	enableTestHelper(t, f, 3, 10)
	f.module.SetHelper(func(context.Context, string, []ChatMessage, int) (string, error) {
		return "", errors.New("private provider failure")
	})
	body := gin.H{"messages": []ChatMessage{{Role: "user", Content: "help"}}}
	w := f.request(t, "POST", "/assistant", token, body)
	requirePortalStatus(t, w, 502)
	if strings.Contains(w.Body.String(), "private provider") {
		t.Fatal("provider error exposed")
	}
	f.module.SetHelper(func(context.Context, string, []ChatMessage, int) (string, error) {
		return strings.Repeat("x", 25<<10), nil
	})
	requirePortalStatus(t, f.request(t, "POST", "/assistant", token, body), 502)
	prices, _ := f.store.ListPointPrices(context.Background())
	p := prices[0]
	p.Enabled = false
	if _, err := f.store.SavePointPrice(context.Background(), p, "test"); err != nil {
		t.Fatal(err)
	}
	requirePortalStatus(t, f.request(t, "POST", "/assistant", token, body), 503)
}

func TestPortalAssistantConcurrentGlobalLimit(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "allowance-a@example.test")
	v, _ := f.register(t, "allowance-b@example.test")
	enableTestHelper(t, f, 3, 4)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		id := u.ID
		if i%2 == 0 {
			id = v.ID
		}
		wg.Go(func() {
			ok, err := f.store.ConsumeHelperAllowance(context.Background(), id, 3, 4)
			if err != nil {
				t.Error(err)
			}
			if ok {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if allowed.Load() != 4 {
		t.Fatalf("concurrent global allowance=%d", allowed.Load())
	}
}

func TestPortalPointsExecutionUsesAdvertisedPriceVersion(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "meter-points@example.test")
	ctx := context.Background()
	if _, err := f.store.AdminCreditWallet(ctx, u.ID, "100", "test", ""); err != nil {
		t.Fatal(err)
	}
	p, err := f.store.SavePointPrice(ctx, store.PointPrice{Model: "priced-model", Input: "10", Output: "20", CacheRead: "0", CacheWrite: "1", Enabled: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Request = c.Request.WithContext(billing.WithUserID(c.Request.Context(), u.ID)) }, billing.PointPriceGuard(f.store))
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		p.Input = "99"
		if _, err := f.store.SavePointPrice(ctx, p, "test"); err != nil {
			t.Error(err)
		}
		billing.NewMeterPlugin(f.store, nil).HandleUsage(c.Request.Context(), usage.Record{RequestID: "synthetic-execution", Provider: "openai", Model: "priced-model", Detail: usage.Detail{InputTokens: 1_000_000, OutputTokens: 500_000}})
		c.Status(200)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"priced-model"}`)))
	requirePortalStatus(t, w, 200)
	balance, err := f.store.GetWalletBalance(ctx, u.ID)
	if err != nil || balance != "80.000000" {
		t.Fatalf("snapshot debit=%s err=%v", balance, err)
	}
	var version int
	if err = f.db.QueryRow(`SELECT price_version FROM "` + f.schema + `".usage_records WHERE request_id='synthetic-execution'`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("price version=%d err=%v", version, err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"unpriced-model"}`)))
	requirePortalStatus(t, w, 403)
}
