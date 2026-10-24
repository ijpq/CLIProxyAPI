package portal

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

func TestPortalAdminCatalogAliasCreditAndUsageFilters(t *testing.T) {
	f := newPortalDBFixture(t)
	admin, adminToken := f.register(t, "admin-flows@example.test")
	customer, token := f.register(t, "customer-flows@example.test")
	f.updateUser(t, admin.ID, "active", true)
	var invalidations atomic.Int32
	f.module.SetWalletChangeHook(func(id string) {
		if id != customer.ID {
			t.Error("wrong wallet invalidated")
		}
		invalidations.Add(1)
	})
	billing.SetModelLister(nil)
	requirePortalStatus(t, f.request(t, "GET", "/admin/models", adminToken, nil), 200)
	const account = "synthetic-private-account@example.test.json"
	billing.SetAccountLister(func() []billing.Account {
		return []billing.Account{{ID: account, Provider: "antigravity", Label: "private", Status: "active"}}
	})
	billing.SetModelLister(func() []string { return []string{"test-model"} })
	billing.SetAuthResolver(func(id string) (billing.UpstreamAuth, bool) {
		return billing.UpstreamAuth{ID: id, Provider: "antigravity", AntigravityCredits: &billing.AntigravityCredits{Known: true, Available: true, CreditAmount: 12}}, true
	})
	t.Cleanup(func() { billing.SetAccountLister(nil); billing.SetModelLister(nil); billing.SetAuthResolver(nil) })
	requirePortalStatus(t, f.request(t, "GET", "/admin/models", adminToken, nil), 200)
	requirePortalStatus(t, f.request(t, "POST", "/admin/accounts/alias", adminToken, gin.H{"id": account, "label": " Customer label "}), 200)
	w := f.request(t, "GET", "/admin/accounts", adminToken, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "Customer label") {
		t.Fatal("account alias not persisted")
	}
	requirePortalStatus(t, f.request(t, "POST", "/admin/credit", adminToken, gin.H{"user_id": customer.ID, "amount": 5, "note": "synthetic points"}), 200)
	if invalidations.Load() != 1 {
		t.Fatal("credit did not invalidate wallet cache")
	}
	keyResponse := f.request(t, "POST", "/api-keys", token, gin.H{"name": "usage"})
	requirePortalStatus(t, keyResponse, 201)
	key := decodePortal[struct {
		ID string `json:"id"`
	}](t, keyResponse)
	if err := f.store.RecordUsageAndDebit(context.Background(), store.MeteredUsage{UserID: customer.ID, APIKeyID: key.ID, Model: "test-model", AuthID: account, Cost: "0", SkipDebit: true}); err != nil {
		t.Fatal(err)
	}
	w = f.request(t, "GET", "/usage/filters", adminToken, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), customer.Email) || !strings.Contains(w.Body.String(), account) {
		t.Fatal("admin usage filters missing authorized records")
	}
	w = f.request(t, "GET", "/usage", token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), key.ID) {
		t.Fatal("usage lost API key reference")
	}
	for _, path := range []string{"/quota", "/quota/" + billing.AccountHandle(account)} {
		w = f.request(t, "GET", path, token, nil)
		requirePortalStatus(t, w, 200)
		if strings.Contains(w.Body.String(), account) {
			t.Fatal("unrestricted quota exposed private account ID")
		}
	}
	if err := f.store.SetUserLimits(context.Background(), customer.ID, false, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	// A non-Codex account cannot consume a Codex reset credit; no HTTP is sent.
	requirePortalStatus(t, f.request(t, "POST", "/quota/"+billing.AccountHandle(account)+"/reset", token, nil), 502)
}

func TestPortalTopupSubmissionAndAdministratorOrderQueue(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "submission-flow@example.test")
	f.updateUser(t, u.ID, "active", true)
	requirePortalStatus(t, f.request(t, "GET", "/topup/methods", token, nil), 200)
	o := f.order(t, u.ID, "3")
	w := f.request(t, "POST", "/topup/"+o.ID+"/submit", token, gin.H{"tx_hash": strings.Repeat("a1", 32)})
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"status":"submitted"`) {
		t.Fatal("submitted order did not retain state")
	}
	w = f.request(t, "GET", "/admin/topup?limit=3&user_id="+u.ID, token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), o.ID) {
		t.Fatal("administrator pending order queue missed submission")
	}
	requirePortalStatus(t, f.request(t, "GET", "/me", "malformed-token", nil), 401)
	requirePortalStatus(t, f.request(t, "POST", "/login", "", gin.H{"email": "not-registered@example.test", "password": "disposable"}), 401)
	f.updateUser(t, u.ID, "suspended", false)
	requirePortalStatus(t, f.request(t, "POST", "/login", "", gin.H{"email": u.Email, "password": "Disposable-test-password-9"}), 403)
}

func TestPortalUniqueAmountRetriesAreBounded(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "suffix-retry@example.test")
	method := billing.PaymentMethod{Method: "wechat", Currency: "CNY", QRCodeURL: "https://payments.example.test/qr", RequiresUniqueAmount: true}
	f.module.store = &failureStore{Store: f.store, operation: "CreateTopupOrder", failure: store.ErrTopupAmountCollision}
	if _, err := f.module.createOrderWithUniqueSuffix(context.Background(), u.ID, method, 10); !errors.Is(err, store.ErrTopupAmountCollision) {
		t.Fatalf("collision retry: %v", err)
	}
	f.module.store = &failureStore{Store: f.store, operation: "CreateTopupOrder", failure: errors.New("synthetic database outage")}
	if _, err := f.module.createOrderWithUniqueSuffix(context.Background(), u.ID, method, 10); err == nil || errors.Is(err, store.ErrTopupAmountCollision) {
		t.Fatalf("non-collision error was retried: %v", err)
	}
	f.module.store = f.store
	requirePortalStatus(t, f.request(t, "POST", "/register", "", gin.H{"email": "long-password@example.test", "password": strings.Repeat("p", 73)}), 500)
	requirePortalStatus(t, f.request(t, "POST", "/change-password", token, gin.H{"old_password": "Disposable-test-password-9", "new_password": strings.Repeat("p", 73)}), 500)
}
