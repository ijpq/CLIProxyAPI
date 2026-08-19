package dbaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestProviderAttachesPerKeyOwnerRestrictions(t *testing.T) {
	const rawKey = "cpk_portal-test-key"
	provider := newProvider("", func(_ context.Context, keyHash string) (store.APIKeyLookup, error) {
		if want := store.HashAPIKey(rawKey); keyHash != want {
			t.Fatalf("key hash = %q, want %q", keyHash, want)
		}
		return store.APIKeyLookup{
			ID:             "key-1",
			UserID:         "user-1",
			Unbilled:       true,
			AllowedModels:  []string{"model-a", "model-b"},
			AllowedAuthIDs: []string{"auth-file-a", "auth-file-b"},
		}, nil
	}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer "+rawKey)

	result, authErr := provider.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if result == nil || result.Principal != "user-1" {
		t.Fatalf("Authenticate() result = %#v, want user-1", result)
	}
	wantMetadata := map[string]string{
		"source":                          "authorization",
		billing.MetadataKeyUserID:         "user-1",
		billing.MetadataKeyAPIKeyID:       "key-1",
		billing.MetadataKeyUnbilled:       "1",
		billing.MetadataKeyAllowedModels:  "model-a,model-b",
		billing.MetadataKeyAllowedAuthIDs: "auth-file-a,auth-file-b",
	}
	for key, want := range wantMetadata {
		if got := result.Metadata[key]; got != want {
			t.Errorf("metadata[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestProviderRevalidatesViaAuthenticatedKeyHash(t *testing.T) {
	const rawKey = "cpk_portal-revalidation-key"
	revoked := false
	current := store.APIKeyLookup{
		ID:            "key-1",
		UserID:        "user-1",
		AllowedModels: []string{"model-initial"},
	}
	provider := newProvider("", func(_ context.Context, keyHash string) (store.APIKeyLookup, error) {
		if keyHash != store.HashAPIKey(rawKey) {
			t.Fatalf("key hash = %q, want authenticated key hash", keyHash)
		}
		if revoked {
			return store.APIKeyLookup{}, store.ErrAPIKeyNotFound
		}
		return current, nil
	}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer "+rawKey)
	previous, authErr := provider.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}

	current.AllowedModels = []string{"model-current"}
	result, authErr := provider.Revalidate(context.Background(), previous)
	if authErr != nil {
		t.Fatalf("Revalidate() error = %v", authErr)
	}
	if got := result.Metadata[billing.MetadataKeyAllowedModels]; got != "model-current" {
		t.Fatalf("allowed models = %q, want model-current", got)
	}

	revoked = true
	result, authErr = provider.Revalidate(context.Background(), previous)
	if result != nil || authErr == nil || authErr.HTTPStatusCode() != http.StatusUnauthorized {
		t.Fatalf("Revalidate(revoked) = %#v, %#v; want HTTP 401", result, authErr)
	}
}

func TestProviderRevalidatesCurrentRestrictionsAndRevocation(t *testing.T) {
	revoked := false
	provider := newProviderWithRevalidation("", nil, func(_ context.Context, keyID string) (store.APIKeyLookup, error) {
		if keyID != "key-1" {
			t.Fatalf("key id = %q, want key-1", keyID)
		}
		if revoked {
			return store.APIKeyLookup{}, store.ErrAPIKeyNotFound
		}
		return store.APIKeyLookup{
			ID:             "key-1",
			UserID:         "user-1",
			AllowedModels:  []string{"model-current"},
			AllowedAuthIDs: []string{"auth-current"},
		}, nil
	}, nil)
	previous := &sdkaccess.Result{
		Provider:  provider.Identifier(),
		Principal: "user-1",
		Metadata:  map[string]string{billing.MetadataKeyAPIKeyID: "key-1"},
	}

	result, authErr := provider.Revalidate(context.Background(), previous)
	if authErr != nil {
		t.Fatalf("Revalidate() error = %v", authErr)
	}
	if result.Metadata[billing.MetadataKeyAllowedModels] != "model-current" || result.Metadata[billing.MetadataKeyAllowedAuthIDs] != "auth-current" {
		t.Fatalf("Revalidate() metadata = %#v", result.Metadata)
	}

	revoked = true
	result, authErr = provider.Revalidate(context.Background(), previous)
	if result != nil || authErr == nil || authErr.HTTPStatusCode() != http.StatusUnauthorized {
		t.Fatalf("Revalidate(revoked) = %#v, %#v; want HTTP 401", result, authErr)
	}
}

func TestProviderRejectsRevokedOrUnknownPortalKey(t *testing.T) {
	provider := newProvider("", func(context.Context, string) (store.APIKeyLookup, error) {
		return store.APIKeyLookup{}, store.ErrAPIKeyNotFound
	}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("X-Api-Key", "revoked-key")

	result, authErr := provider.Authenticate(context.Background(), request)
	if result != nil {
		t.Fatalf("Authenticate() result = %#v, want nil", result)
	}
	if authErr == nil || authErr.HTTPStatusCode() != http.StatusUnauthorized {
		t.Fatalf("Authenticate() error = %#v, want HTTP 401", authErr)
	}
}
