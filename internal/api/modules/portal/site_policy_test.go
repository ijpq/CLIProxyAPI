package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

const policyTestID = "f84bb752-bd9b-4e4a-a681-7067036a902b"

func TestSitePolicyConfigurationAndHostBoundary(t *testing.T) {
	for _, tc := range []struct {
		public, private, ids string
		valid                bool
	}{
		{"", "", "", true},
		{" PORTAL.EXAMPLE.TEST ", "private.example.test", policyTestID + "," + policyTestID, true},
		{"portal.example.test", "private.example.test", policyTestID, true},
		{"portal.example.test", "", policyTestID, false},
		{"portal.example.test", "private.example.test", "", false},
		{"portal.example.test", "portal.example.test", policyTestID, false},
		{"https://portal.example.test", "private.example.test", policyTestID, false},
		{"portal.example.test:443", "private.example.test", policyTestID, false},
		{"portal.example.test", "*.example.test", policyTestID, false},
		{strings.Repeat("a", 254), "private.example.test", policyTestID, false},
		{"portal.example.test", "private.example.test", "invalid-private-identifier", false},
		{"portal.example.test", "private.example.test", "00000000-0000-0000-0000-000000000000", false},
		{"portal.example.test", "private.example.test", policyTestID + ",", false},
	} {
		p, err := NewSitePolicy(tc.public, tc.private, tc.ids)
		if (err == nil) != tc.valid {
			t.Fatalf("valid=%v, error=%v", tc.valid, err)
		}
		if err != nil && strings.Contains(err.Error(), tc.ids) && tc.ids != "" {
			t.Fatal("identifier leaked in error")
		}
		if p != nil && len(p.privilegedIDs) != 1 {
			t.Fatal("approved IDs not deduplicated")
		}
	}
	p, err := NewSitePolicy("portal.example.test", "private.example.test", policyTestID)
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, nil, nil)
	m.SetSitePolicy(p)
	for _, tc := range []struct {
		host, mode string
		allowed    bool
	}{
		{"portal.example.test", siteCustomer, false},
		{"PRIVATE.EXAMPLE.TEST:443", sitePrivileged, true},
		{"private.example.test.", sitePrivileged, true},
		{"private.example.test.evil.test", siteUnknown, false},
		{"127.0.0.1:8317", siteUnknown, false},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/portal/me", nil)
		c.Request.Host = tc.host
		c.Request.Header.Set("X-Forwarded-Host", "private.example.test")
		if m.siteMode(c) != tc.mode || m.siteAllowsUser(c, policyTestID) != tc.allowed {
			t.Fatalf("wrong host boundary for %s", tc.host)
		}
		if m.loginCodeKey(c, "test@example.test") == "test@example.test" {
			t.Fatal("site login code was not scoped")
		}
	}
}

func siteRequest(t *testing.T, f *portalDBFixture, host, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "/portal"+path, bytes.NewReader(data))
	r.Host = host
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-Host", "private.example.test")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func TestPortalSitesSeparateExistingAccountsFromCustomers(t *testing.T) {
	f := newPortalDBFixture(t)
	private, privateToken := f.register(t, "private@example.test")
	customer, customerToken := f.register(t, "customer@example.test")
	if err := f.store.PromoteUserIDToAdmin(t.Context(), private.ID); err != nil {
		t.Fatal(err)
	}
	// Fee exemption is deliberately not private-site membership.
	if err := f.store.SetUserLimits(t.Context(), customer.ID, true, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	p, err := NewSitePolicy("portal.example.test", "private.example.test", private.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.module.SetSitePolicy(p)
	for _, tc := range []struct {
		host, token string
		want        int
	}{
		{"private.example.test", privateToken, 200}, {"portal.example.test", customerToken, 200},
		{"private.example.test", customerToken, 403}, {"portal.example.test", privateToken, 403},
		{"unknown.example.test", privateToken, 404},
	} {
		requirePortalStatus(t, siteRequest(t, f, tc.host, "GET", "/me", tc.token, nil), tc.want)
	}
	for _, tc := range []struct {
		host, email string
		want        int
	}{
		{"private.example.test", private.Email, 200}, {"portal.example.test", customer.Email, 200},
		{"private.example.test", customer.Email, 403}, {"portal.example.test", private.Email, 403},
	} {
		requirePortalStatus(t, siteRequest(t, f, tc.host, "POST", "/login", "", gin.H{"email": tc.email, "password": "Disposable-test-password-9"}), tc.want)
	}
	requirePortalStatus(t, siteRequest(t, f, "portal.example.test", "GET", "/admin/settings", privateToken, nil), 404)
	requirePortalStatus(t, siteRequest(t, f, "private.example.test", "GET", "/admin/settings", privateToken, nil), 200)
	requirePortalStatus(t, siteRequest(t, f, "private.example.test", "POST", "/register", "", gin.H{"email": "blocked@example.test", "password": "test-password-9"}), 403)
	if _, err := f.store.GetUserByEmail(t.Context(), "blocked@example.test"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatal("private registration created an account")
	}
	requirePortalStatus(t, siteRequest(t, f, "portal.example.test", "POST", "/register", "", gin.H{"email": "new@example.test", "password": "test-password-9", "is_admin": true, "unbilled": true}), 201)
	user, err := f.store.GetUserByEmail(t.Context(), "new@example.test")
	if err != nil || user.IsAdmin || user.Unbilled {
		t.Fatal("registration granted privileged flags")
	}
	for _, tc := range []struct {
		host    string
		allowed bool
	}{{"private.example.test", false}, {"portal.example.test", true}} {
		w := siteRequest(t, f, tc.host, "GET", "/config", "", nil)
		requirePortalStatus(t, w, 200)
		var config struct {
			Registration bool `json:"registration_enabled"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
			t.Fatal(err)
		}
		if config.Registration != tc.allowed {
			t.Fatal("wrong registration policy advertised")
		}
	}
}

func TestPortalSiteEmailCodesCannotCreateOrCrossPrivateAccounts(t *testing.T) {
	f := newPortalDBFixture(t)
	private, _ := f.register(t, "private@example.test")
	customer, _ := f.register(t, "customer@example.test")
	p, err := NewSitePolicy("portal.example.test", "private.example.test", private.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.module.SetSitePolicy(p)
	sender := &capturePortalEmail{}
	f.module.SetEmailLogin(sender, billing.NewLoginCodeStore(time.Minute, 0))
	for _, tc := range []struct{ host, email string }{{"private.example.test", "unknown@example.test"}, {"private.example.test", customer.Email}, {"portal.example.test", private.Email}} {
		sender.to = ""
		requirePortalStatus(t, siteRequest(t, f, tc.host, "POST", "/login/code/request", "", gin.H{"email": tc.email}), 200)
		if sender.to != "" {
			t.Fatal("sent a code for a disallowed site/account")
		}
	}
	for _, tc := range []struct {
		host, email, key string
		want             int
	}{
		{"private.example.test", private.Email, "privileged:", 200},
		{"portal.example.test", customer.Email, "customer:", 200},
		{"portal.example.test", "new-code@example.test", "customer:", 200},
		{"private.example.test", customer.Email, "privileged:", 403},
		{"private.example.test", "unknown@example.test", "privileged:", 403},
		{"portal.example.test", private.Email, "customer:", 403},
	} {
		code, err := f.module.loginCodes.Generate(tc.key + tc.email)
		if err != nil {
			t.Fatal(err)
		}
		wrong := "private.example.test"
		if tc.host == wrong {
			wrong = "portal.example.test"
		}
		requirePortalStatus(t, siteRequest(t, f, wrong, "POST", "/login/code/verify", "", gin.H{"email": tc.email, "code": code}), 401)
		requirePortalStatus(t, siteRequest(t, f, tc.host, "POST", "/login/code/verify", "", gin.H{"email": tc.email, "code": code}), tc.want)
	}
	if _, err := f.store.GetUserByEmail(t.Context(), "unknown@example.test"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatal("private code login created an account")
	}
	f.module.SetEmailLogin(sender, billing.NewLoginCodeStore(time.Minute, 0))
	for _, tc := range []struct{ host, email string }{{"private.example.test", private.Email}, {"portal.example.test", "another-new@example.test"}} {
		sender.to = ""
		requirePortalStatus(t, siteRequest(t, f, tc.host, "POST", "/login/code/request", "", gin.H{"email": tc.email}), 200)
		if sender.to != tc.email {
			t.Fatal("eligible code request was not delivered")
		}
	}
	f.module.store = &failureStore{Store: f.store, operation: "GetUserByEmail", failure: errors.New("synthetic failure")}
	requirePortalStatus(t, siteRequest(t, f, "portal.example.test", "POST", "/login/code/request", "", gin.H{"email": "failure@example.test"}), 503)
}

func TestCustomerSiteAssetsAndPrivateUIKeepSeparatePaths(t *testing.T) {
	m := New(nil, nil, nil)
	p, err := NewSitePolicy("portal.example.test", "private.example.test", policyTestID)
	if err != nil {
		t.Fatal(err)
	}
	m.SetSitePolicy(p)
	r := gin.New()
	m.RegisterRoutes(r.Group("/portal"))
	for _, tc := range []struct {
		host, asset string
		want        int
		contains    string
	}{
		{"portal.example.test", "index.html", 200, "/assets/app.js"},
		{"portal.example.test", "docs.html", 200, "/assets/docs.js"},
		{"portal.example.test", "app.js", 200, "/api/portal"},
		{"portal.example.test", "docs.js", 200, "/console/keys"},
		{"portal.example.test", "config.yaml", 404, ""},
		{"private.example.test", "index.html", 404, ""},
	} {
		req := httptest.NewRequest("GET", "/portal/site/"+tc.asset, nil)
		req.Host = tc.host
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		requirePortalStatus(t, w, tc.want)
		if tc.contains != "" && !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatal("incorrect asset URLs")
		}
	}
	for _, path := range []string{"/portal/ui/", "/portal/ui/index.html"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "private.example.test"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		requirePortalStatus(t, w, 200)
		for _, expected := range []string{
			`data-portal-site="privileged"`, `id="page-home" class="landing hidden"`,
			`id="page-auth" class="auth-wrap"`, `id="auth-title">特权账户登录`,
			`id="auth-home" class="btn btn-ghost hidden"`, `id="tab-register" class="tab hidden"`,
			`src="app.js?v=`,
		} {
			if !strings.Contains(w.Body.String(), expected) {
				t.Fatalf("private login HTML missing %s", expected)
			}
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private login response can be cached")
		}
		head := httptest.NewRequest("HEAD", path, nil)
		head.Host = req.Host
		hw := httptest.NewRecorder()
		r.ServeHTTP(hw, head)
		requirePortalStatus(t, hw, 200)
		if hw.Body.Len() != 0 || hw.Header().Get("Content-Length") != w.Header().Get("Content-Length") {
			t.Fatal("private entry HEAD response does not match GET metadata")
		}
	}
}
