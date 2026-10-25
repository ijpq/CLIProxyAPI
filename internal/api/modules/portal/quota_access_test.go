package portal

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
)

func TestQuotaAccessRequiresPrivilegedMembership(t *testing.T) {
	f := newPortalDBFixture(t)
	private, privateToken := f.register(t, "quota-private@example.test")
	customer, customerToken := f.register(t, "quota-customer@example.test")
	// None of these independent privileges grant private-site membership.
	f.updateUser(t, customer.ID, "active", true)
	if err := f.store.SetUserLimits(t.Context(), customer.ID, true, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	p, err := NewSitePolicy("portal.example.test", "private.example.test", private.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.module.SetSitePolicy(p)
	const account = "quota-synthetic@example.test.json"
	var probes atomic.Int32
	billing.SetAccountLister(func() []billing.Account {
		return []billing.Account{{ID: account, Provider: "antigravity"}}
	})
	billing.SetAuthResolver(func(id string) (billing.UpstreamAuth, bool) {
		probes.Add(1)
		return billing.UpstreamAuth{ID: id, Provider: "antigravity", AntigravityCredits: &billing.AntigravityCredits{Known: true, Available: true, CreditAmount: 10}}, true
	})
	t.Cleanup(func() { billing.SetAccountLister(nil); billing.SetAuthResolver(nil) })
	capability := func(host, token string, want bool) {
		t.Helper()
		w := siteRequest(t, f, host, "GET", "/me", token, nil)
		requirePortalStatus(t, w, 200)
		me := decodePortal[struct {
			Allowed *bool `json:"can_view_upstream_quota"`
		}](t, w)
		if me.Allowed == nil || *me.Allowed != want {
			t.Fatalf("unexpected upstream quota permission for %s", host)
		}
	}
	capability("private.example.test", privateToken, true)
	capability("portal.example.test", customerToken, false)
	for _, route := range []struct{ method, path string }{
		{"GET", "/quota"},
		{"GET", "/quota/" + billing.AccountHandle(account)},
		{"POST", "/quota/" + billing.AccountHandle(account) + "/reset"},
	} {
		for _, tc := range []struct {
			host, token string
			status      int
		}{
			{"portal.example.test", customerToken, 403},
			{"private.example.test", customerToken, 403},
			{"portal.example.test", privateToken, 403},
			{"unknown.example.test", privateToken, 404},
			{"private.example.test", "", 401},
		} {
			requirePortalStatus(t, siteRequest(t, f, tc.host, route.method, route.path, tc.token, nil), tc.status)
		}
	}
	if probes.Load() != 0 {
		t.Fatal("denied quota request reached the provider")
	}
	for _, path := range []string{"/quota", "/quota/" + billing.AccountHandle(account)} {
		w := siteRequest(t, f, "private.example.test", "GET", path, privateToken, nil)
		requirePortalStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), billing.AccountHandle(account)) || strings.Contains(w.Body.String(), account) {
			t.Fatal("privileged quota response lost its opaque account boundary")
		}
	}
	requirePortalStatus(t, siteRequest(t, f, "private.example.test", "POST", "/quota/"+billing.AccountHandle(account)+"/reset", privateToken, nil), 403)
	if probes.Load() != 2 {
		t.Fatal("reset permission was widened")
	}

	// A still-valid session does not retain revoked membership.
	p, err = NewSitePolicy("portal.example.test", "private.example.test", customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.module.SetSitePolicy(p)
	requirePortalStatus(t, siteRequest(t, f, "private.example.test", "GET", "/quota", privateToken, nil), 403)
	capability("portal.example.test", privateToken, false)
	requirePortalStatus(t, siteRequest(t, f, "portal.example.test", "GET", "/quota", privateToken, nil), 403)

	// Legacy installations without a site allowlist allow only current admins.
	f.module.SetSitePolicy(nil)
	capability("legacy.example.test", privateToken, false)
	capability("legacy.example.test", customerToken, true)
	requirePortalStatus(t, f.request(t, "GET", "/quota", privateToken, nil), 403)
	requirePortalStatus(t, f.request(t, "GET", "/quota", customerToken, nil), 200)
	f.updateUser(t, customer.ID, "active", false)
	capability("legacy.example.test", customerToken, false)
	requirePortalStatus(t, f.request(t, "GET", "/quota", customerToken, nil), 403)
}
