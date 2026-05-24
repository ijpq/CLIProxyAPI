// Package dbaccess implements an access.Provider backed by the Postgres
// billing store. It validates inbound API keys against the api_keys table and
// surfaces the owning user id so downstream metering can attribute usage.
package dbaccess

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

// Lookup resolves a SHA-256 hashed API key to its owning row. Implementations
// must return store.ErrAPIKeyNotFound when no active key matches; any other
// error is treated as an internal failure.
type Lookup func(ctx context.Context, keyHash string) (store.APIKeyLookup, error)

// Toucher is an optional callback invoked asynchronously after a successful
// lookup so the provider does not block the request on a write.
type Toucher func(ctx context.Context, keyID string) error

// Register installs a database-backed provider on the global access registry.
// Passing a nil lookup removes any previously registered instance.
func Register(name string, lookup Lookup, toucher Toucher) {
	if lookup == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeDBAPIKey)
		return
	}
	sdkaccess.RegisterProvider(
		sdkaccess.AccessProviderTypeDBAPIKey,
		newProvider(name, lookup, toucher),
	)
}

type provider struct {
	name    string
	lookup  Lookup
	toucher Toucher
}

func newProvider(name string, lookup Lookup, toucher Toucher) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultDBAccessProviderName
	}
	return &provider{name: providerName, lookup: lookup, toucher: toucher}
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return sdkaccess.DefaultDBAccessProviderName
	}
	return p.name
}

func (p *provider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || p.lookup == nil {
		return nil, sdkaccess.NewNotHandledError()
	}

	candidate, source := extractCandidate(r)
	if candidate == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}

	lookup, err := p.lookup(ctx, store.HashAPIKey(candidate))
	switch {
	case errors.Is(err, store.ErrAPIKeyNotFound):
		return nil, sdkaccess.NewInvalidCredentialError()
	case err != nil:
		return nil, sdkaccess.NewInternalAuthError("api key lookup failed", err)
	}

	if p.toucher != nil {
		keyID := lookup.ID
		go func() {
			if err := p.toucher(context.Background(), keyID); err != nil {
				log.WithError(err).Debugf("db-access: touch api key %s failed", keyID)
			}
		}()
	}

	return &sdkaccess.Result{
		Provider:  p.Identifier(),
		Principal: lookup.UserID,
		Metadata: map[string]string{
			"source":                    source,
			billing.MetadataKeyUserID:   lookup.UserID,
			billing.MetadataKeyAPIKeyID: lookup.ID,
		},
	}, nil
}

// extractCandidate returns the first non-empty API key supplied via any of the
// common header or query mechanisms accepted by the proxy.
func extractCandidate(r *http.Request) (value, source string) {
	if r == nil {
		return "", ""
	}
	if v := extractBearerToken(r.Header.Get("Authorization")); v != "" {
		return v, "authorization"
	}
	if v := strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")); v != "" {
		return v, "x-goog-api-key"
	}
	if v := strings.TrimSpace(r.Header.Get("X-Api-Key")); v != "" {
		return v, "x-api-key"
	}
	if r.URL != nil {
		if v := strings.TrimSpace(r.URL.Query().Get("key")); v != "" {
			return v, "query-key"
		}
		if v := strings.TrimSpace(r.URL.Query().Get("auth_token")); v != "" {
			return v, "query-auth-token"
		}
	}
	return "", ""
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(header)
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return strings.TrimSpace(header)
	}
	return strings.TrimSpace(parts[1])
}
