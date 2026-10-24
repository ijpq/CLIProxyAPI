package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

func TestSetupBillingRejectsIncompleteInitialization(t *testing.T) {
	for _, tc := range []struct {
		name, secret, pricing, want string
		shared                      *store.PostgresStore
	}{
		{name: "missing signing secret", want: "BILLING_JWT_SECRET"},
		{name: "missing database", secret: "test-only", want: "BILLING_DATABASE_URL"},
		{name: "unavailable schema", secret: "test-only", shared: &store.PostgresStore{}, want: "ensure schema"},
		{name: "missing explicit pricing", secret: "test-only", pricing: "missing.json", want: "pricing initialization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BILLING_ENABLED", "true")
			t.Setenv("BILLING_DATABASE_URL", "")
			t.Setenv("BILLING_JWT_SECRET", tc.secret)
			pricing := ""
			if tc.pricing != "" {
				pricing = filepath.Join(t.TempDir(), tc.pricing)
			}
			t.Setenv("BILLING_PRICING_FILE", pricing)
			opts, err := setupBilling(context.Background(), tc.shared)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(opts) != 0 {
				t.Fatalf("options=%d, err=%v; want startup error containing %q", len(opts), err, tc.want)
			}
		})
	}
}

func TestSetupBillingRejectsInvalidPricingBeforeDatabaseRegistration(t *testing.T) {
	t.Setenv("BILLING_ENABLED", "true")
	t.Setenv("BILLING_JWT_SECRET", "test-only")
	t.Setenv("BILLING_DATABASE_URL", "")
	path := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(path, []byte(`{"test-model":{"input_per_million":-1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BILLING_PRICING_FILE", path)
	if _, err := setupBilling(context.Background(), &store.PostgresStore{}); err == nil || !strings.Contains(err.Error(), "pricing initialization") {
		t.Fatalf("expected pricing error before any database/global side effects, got %v", err)
	}
}

func TestResolveBillingStoreDoesNotExposeMalformedDSN(t *testing.T) {
	const sensitive = "synthetic-private-password"
	t.Setenv("BILLING_DATABASE_URL", "postgres://user:"+sensitive+"@bad%host/db")
	pg, err := resolveBillingStore(context.Background(), nil)
	if pg != nil || err == nil {
		t.Fatalf("expected a database initialization error")
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatal("database configuration leaked in startup error")
	}
}

func TestSetupBillingExplicitlyDisabled(t *testing.T) {
	t.Setenv("BILLING_ENABLED", "false")
	opts, err := setupBilling(context.Background(), nil)
	if err != nil || len(opts) != 0 {
		t.Fatalf("disabled billing: options=%d, err=%v", len(opts), err)
	}
}

func TestSetupBillingRejectsUnverifiedAdministratorBootstrap(t *testing.T) {
	t.Setenv("BILLING_ENABLED", "true")
	t.Setenv("BILLING_JWT_SECRET", "test-only")
	t.Setenv("BILLING_DATABASE_URL", "")
	t.Setenv("BILLING_PRICING_FILE", "")
	t.Setenv("BILLING_ADMIN_EMAIL", "unverified@example.test")
	t.Setenv("BILLING_ADMIN_USER_ID", "")
	if _, err := setupBilling(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "email-only") {
		t.Fatalf("email bootstrap accepted: %v", err)
	}
	t.Setenv("BILLING_ADMIN_USER_ID", "not-a-uuid")
	if _, err := setupBilling(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "approved UUID") {
		t.Fatalf("unvalidated user ID accepted: %v", err)
	}
	t.Setenv("BILLING_ADMIN_EMAIL", "")
	t.Setenv("BILLING_ADMIN_USER_ID", "")
	t.Setenv("BILLING_USDT_AUTO_CONFIRM", "true")
	if _, err := setupBilling(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "payment ownership") {
		t.Fatalf("shared-address auto-confirm accepted: %v", err)
	}
}
