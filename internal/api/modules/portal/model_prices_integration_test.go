package portal

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestPortalDiscountCatalogMatchesRequestBilling(t *testing.T) {
	f := newPortalDBFixture(t)
	admin, adminToken := f.register(t, "discount-admin@example.test")
	f.updateUser(t, admin.ID, "active", true)
	u, token := f.register(t, "discount-customer@example.test")
	p := store.PointPrice{Model: "offer-model", Enabled: true, DiscountPercent: 10, StandardPrice: &store.PointPriceRates{Input: "100", Output: "200", CacheRead: "0", CacheWrite: "10"}}
	w := f.request(t, "PUT", "/admin/pricing", adminToken, p)
	requirePortalStatus(t, w, 200)
	saved := decodePortal[store.PointPrice](t, w)
	if saved.Input != "90.000000" || saved.Output != "180.000000" || saved.CacheWrite != "9.000000" {
		t.Fatalf("incorrect effective rates: %#v", saved)
	}
	for _, path := range []string{"/public", "/api-key-models", "/admin/pricing"} {
		w = f.request(t, "GET", path, adminToken, nil)
		requirePortalStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), `"input_per_million":"90.000000"`) || !strings.Contains(w.Body.String(), `"discount_percent":10`) || !strings.Contains(w.Body.String(), `"standard_price"`) {
			t.Fatalf("catalog lost the effective/reference price at %s", path)
		}
	}
	requirePortalStatus(t, f.request(t, "PUT", "/admin/pricing", token, p), 403)
	p.DiscountPercent = 100
	requirePortalStatus(t, f.request(t, "PUT", "/admin/pricing", adminToken, p), 400)
	p.DiscountPercent = 10
	p.StandardPrice = nil
	requirePortalStatus(t, f.request(t, "PUT", "/admin/pricing", adminToken, p), 400)
	p.StandardPrice = saved.StandardPrice
	requirePortalStatus(t, f.request(t, "PUT", "/admin/pricing", adminToken, p), 409)
	if _, err := f.store.AdminCreditWallet(context.Background(), u.ID, "500", "test", ""); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Request = c.Request.WithContext(billing.WithUserID(c.Request.Context(), u.ID)) }, billing.PointPriceGuard(f.store))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		saved.DiscountPercent = 20
		if _, err := f.store.SavePointPrice(context.Background(), saved, "test"); err != nil {
			t.Fatal(err)
		}
		billing.NewMeterPlugin(f.store, nil).HandleUsage(c.Request.Context(), usage.Record{RequestID: "discount-snapshot", Provider: "openai", Model: "offer-model", Detail: usage.Detail{InputTokens: 1_000_000, OutputTokens: 500_000}})
		c.Status(200)
	})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"offer-model"}`)))
	requirePortalStatus(t, w, 200)
	balance, err := f.store.GetWalletBalance(context.Background(), u.ID)
	if err != nil || balance != "320.000000" {
		t.Fatalf("snapshot charged a different price: %s %v", balance, err)
	}
}

func TestPortalModelPricesRespectAccountPolicy(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "price-scope@example.test")
	for _, model := range []string{"allowed", "not-allowed", "disabled"} {
		if _, err := f.store.SavePointPrice(context.Background(), store.PointPrice{Model: model, Input: "1", Output: "2", CacheRead: "0", CacheWrite: "0", Enabled: model != "disabled"}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.SetUserLimits(context.Background(), u.ID, false, []string{"allowed"}, nil, false); err != nil {
		t.Fatal(err)
	}
	w := f.request(t, "GET", "/api-key-models", token, nil)
	requirePortalStatus(t, w, 200)
	var catalog struct {
		Models []string           `json:"models"`
		Prices []store.PointPrice `json:"prices"`
	}
	catalog = decodePortal[struct {
		Models []string           `json:"models"`
		Prices []store.PointPrice `json:"prices"`
	}](t, w)
	if len(catalog.Models) != 1 || len(catalog.Prices) != 1 || catalog.Prices[0].Model != "allowed" {
		t.Fatalf("prices escaped account scope: %#v", catalog)
	}
}

func TestPortalPriceOfferMigrationPreservesExistingRates(t *testing.T) {
	f := newPortalDBFixture(t)
	ctx := context.Background()
	table := `"` + f.schema + `".billing_point_prices`
	if _, err := f.db.Exec(`ALTER TABLE ` + table + ` DROP COLUMN discount_percent, DROP COLUMN standard_price`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO ` + table + `(model,input,output,cache_read,cache_write,enabled,version) VALUES ('legacy-priced',123.456789,250,0,5,true,7)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.store.EnsureBillingSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	prices, err := f.store.ListPointPrices(ctx)
	if err != nil || len(prices) != 1 {
		t.Fatalf("migration prices: %#v %v", prices, err)
	}
	p := prices[0]
	if p.Input != "123.456789" || p.Output != "250.000000" || p.CacheRead != "0.000000" || p.CacheWrite != "5.000000" || p.Version != 7 || p.DiscountPercent != 0 || p.StandardPrice != nil {
		t.Fatalf("migration changed an existing price: %#v", p)
	}
	p.DiscountPercent = 10
	p.StandardPrice = &store.PointPriceRates{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite}
	offer, err := f.store.SavePointPrice(ctx, p, "test")
	if err != nil {
		t.Fatal(err)
	}
	offer.Input, offer.Output, offer.CacheRead, offer.CacheWrite = p.Input, p.Output, p.CacheRead, p.CacheWrite
	offer.DiscountPercent = 0
	restored, err := f.store.SavePointPrice(ctx, offer, "test")
	if err != nil || restored.Input != p.Input || restored.StandardPrice != nil || restored.Version != 9 {
		t.Fatalf("removing discount did not restore standard rates: %#v %v", restored, err)
	}
}
