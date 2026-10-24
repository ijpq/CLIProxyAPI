package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

type capturePortalEmail struct {
	to, body string
	failure  error
}

func (s *capturePortalEmail) Send(_ context.Context, to, subject, body string) error {
	s.to, s.body = to, body
	return s.failure
}

type capturePortalNotice struct{ messages []string }

func (s *capturePortalNotice) Send(_ context.Context, message string) {
	s.messages = append(s.messages, message)
}

func TestPortalEmailLoginConfigurationAndDelivery(t *testing.T) {
	f := newPortalDBFixture(t)
	for _, path := range []string{"/login/code/request", "/login/code/verify"} {
		requirePortalStatus(t, f.request(t, "POST", path, "", gin.H{}), 503)
	}
	sender := &capturePortalEmail{}
	f.module.SetEmailLogin(sender, billing.NewLoginCodeStore(time.Minute, time.Minute))
	w := f.request(t, "GET", "/config", "", nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"email_login":true`) {
		t.Fatal("email configuration not advertised")
	}
	for _, path := range []string{"/login/code/request", "/login/code/verify"} {
		requirePortalStatus(t, f.rawRequest("POST", path, "", []byte("{")), 400)
	}
	requirePortalStatus(t, f.request(t, "POST", "/login/code/request", "", gin.H{"email": "invalid"}), 400)
	requirePortalStatus(t, f.request(t, "POST", "/login/code/request", "", gin.H{"email": " Delivery@Example.Test "}), 200)
	if sender.to != "delivery@example.test" || !strings.Contains(sender.body, "验证码") {
		t.Fatal("email delivery was not normalized")
	}
	requirePortalStatus(t, f.request(t, "POST", "/login/code/request", "", gin.H{"email": "delivery@example.test"}), 429)
	sender.failure = errors.New("synthetic SMTP internals")
	w = f.request(t, "POST", "/login/code/request", "", gin.H{"email": "send-failure@example.test"})
	requirePortalStatus(t, w, 502)
	if strings.Contains(w.Body.String(), "SMTP internals") {
		t.Fatal("email error exposed")
	}
	requirePortalStatus(t, f.request(t, "POST", "/login/code/verify", "", gin.H{"email": "delivery@example.test", "code": "wrong"}), 401)
}

func TestPortalEmailLoginCreatesAccountAndIssuesSession(t *testing.T) {
	f := newPortalDBFixture(t)
	notice := &capturePortalNotice{}
	f.module.SetNotifier(notice)
	codes := billing.NewLoginCodeStore(time.Minute, time.Minute)
	f.module.SetEmailLogin(noOpPortalEmail{}, codes)
	code, err := codes.Generate("new-code@example.test")
	if err != nil {
		t.Fatal(err)
	}
	w := f.request(t, "POST", "/login/code/verify", "", gin.H{"email": "NEW-CODE@example.test", "code": code})
	requirePortalStatus(t, w, 200)
	session := decodePortal[struct {
		Token string `json:"token"`
	}](t, w)
	requirePortalStatus(t, f.request(t, "GET", "/me", session.Token, nil), 200)
	if len(notice.messages) != 1 {
		t.Fatal("new passwordless account was not announced")
	}
	requirePortalStatus(t, f.request(t, "POST", "/login/code/verify", "", gin.H{"email": "new-code@example.test", "code": code}), 401)
	if _, err := f.store.GetUserByEmail(context.Background(), "new-code@example.test"); err != nil {
		t.Fatal(err)
	}
}

func TestPortalEmailLoginStorageAndTokenFailures(t *testing.T) {
	f := newPortalDBFixture(t)
	for i, tc := range []struct {
		operation string
		failure   error
	}{{"GetUserByEmail", errors.New("synthetic read failure")}, {"CreateUser", errors.New("synthetic write failure")}, {"CreateUser", store.ErrEmailTaken}} {
		email := []string{"read@example.test", "write@example.test", "conflict@example.test"}[i]
		codes := billing.NewLoginCodeStore(time.Minute, time.Minute)
		f.module.SetEmailLogin(noOpPortalEmail{}, codes)
		code, _ := codes.Generate(email)
		f.module.store = &failureStore{Store: f.store, operation: tc.operation, failure: tc.failure}
		requirePortalStatus(t, f.request(t, "POST", "/login/code/verify", "", gin.H{"email": email, "code": code}), 500)
	}
	f.module.store = f.store
	u, _ := f.register(t, "token-failure@example.test")
	codes := billing.NewLoginCodeStore(time.Minute, time.Minute)
	f.module.SetEmailLogin(noOpPortalEmail{}, codes)
	code, _ := codes.Generate(u.Email)
	f.module.tokens = nil
	requirePortalStatus(t, f.request(t, "POST", "/login/code/verify", "", gin.H{"email": u.Email, "code": code}), 500)
	requirePortalStatus(t, invokePortalHandler(f.module.handleLogin, u.ID, false, `{"email":"token-failure@example.test","password":"Disposable-test-password-9"}`, ""), 500)
	requirePortalStatus(t, invokePortalHandler(f.module.handleRegister, u.ID, false, `{"email":"token-register@example.test","password":"Disposable-test-password-9"}`, ""), 500)
}

func TestPortalModuleNilAndViewBoundaries(t *testing.T) {
	var m *Module
	m.SetWalletChangeHook(nil)
	m.SetNotifier(nil)
	m.SetEmailLogin(nil, nil)
	m.RegisterRoutes(nil)
	if m.emailLoginEnabled() {
		t.Fatal("nil module enabled login")
	}
	if got := keyPrefix("short"); got != "short" {
		t.Fatalf("short key prefix=%q", got)
	}
	if id, _, _, ok := (&Module{}).accountForHandle(context.Background(), store.User{}, ""); ok || id != "" {
		t.Fatal("empty handle accepted")
	}
	for _, header := range []string{"", "Basic abc", "Bearer", "Bearer "} {
		if extractBearer(header) != "" {
			t.Fatalf("invalid bearer header accepted: %q", header)
		}
	}
}
