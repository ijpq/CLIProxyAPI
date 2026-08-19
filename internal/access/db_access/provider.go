// Package dbaccess implements an access.Provider backed by the Postgres
// billing store. It validates inbound API keys against the api_keys table and
// surfaces the owning user id so downstream metering can attribute usage.
package dbaccess

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

// Lookup resolves a SHA-256 hashed API key to its owning row. Implementations
// must return store.ErrAPIKeyNotFound when no active key matches; any other
// error is treated as an internal failure.
type Lookup func(ctx context.Context, keyHash string) (store.APIKeyLookup, error)

// LookupByID refreshes the current authorization state for an active API-key
// row without requiring the original plaintext key.
type LookupByID func(ctx context.Context, keyID string) (store.APIKeyLookup, error)

// Toucher is an optional callback invoked asynchronously after a successful
// lookup so the provider does not block the request on a write.
type Toucher func(ctx context.Context, keyID string) error

// Register installs a database-backed provider on the global access registry.
// Passing a nil lookup removes any previously registered instance.
func Register(name string, lookup Lookup, toucher Toucher) {
	register(name, lookup, nil, toucher)
}

// RegisterWithRevalidation also enables delegated credentials to refresh the
// current key status and per-user access controls by API-key ID.
func RegisterWithRevalidation(name string, lookup Lookup, lookupByID LookupByID, toucher Toucher) {
	register(name, lookup, lookupByID, toucher)
}

func register(name string, lookup Lookup, lookupByID LookupByID, toucher Toucher) {
	if lookup == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeDBAPIKey)
		return
	}
	sdkaccess.RegisterProvider(
		sdkaccess.AccessProviderTypeDBAPIKey,
		newProviderWithRevalidation(name, lookup, lookupByID, toucher),
	)
}

type provider struct {
	name       string
	lookup     Lookup
	lookupByID LookupByID
	toucher    Toucher

	keyHashesMu sync.RWMutex
	keyHashes   map[string]string
}

func newProvider(name string, lookup Lookup, toucher Toucher) *provider {
	return newProviderWithRevalidation(name, lookup, nil, toucher)
}

func newProviderWithRevalidation(name string, lookup Lookup, lookupByID LookupByID, toucher Toucher) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultDBAccessProviderName
	}
	return &provider{
		name:       providerName,
		lookup:     lookup,
		lookupByID: lookupByID,
		toucher:    toucher,
		keyHashes:  make(map[string]string),
	}
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

	keyHash := store.HashAPIKey(candidate)
	lookup, err := p.lookup(ctx, keyHash)
	switch {
	case errors.Is(err, store.ErrAPIKeyNotFound):
		return nil, sdkaccess.NewInvalidCredentialError()
	case err != nil:
		return nil, sdkaccess.NewInternalAuthError("api key lookup failed", err)
	}

	p.rememberKeyHash(lookup.ID, keyHash)
	p.touch(lookup.ID)
	return p.result(lookup, source), nil
}

// Revalidate refreshes a delegated credential from its active API-key row.
func (p *provider) Revalidate(ctx context.Context, previous *sdkaccess.Result) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || (p.lookupByID == nil && p.lookup == nil) {
		return nil, sdkaccess.NewNotHandledError()
	}
	if previous == nil {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	keyID := strings.TrimSpace(previous.Metadata[billing.MetadataKeyAPIKeyID])
	if keyID == "" {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	var (
		lookup store.APIKeyLookup
		err    error
	)
	if p.lookupByID != nil {
		lookup, err = p.lookupByID(ctx, keyID)
	} else if keyHash, ok := p.keyHash(keyID); ok {
		lookup, err = p.lookup(ctx, keyHash)
	} else {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	switch {
	case errors.Is(err, store.ErrAPIKeyNotFound):
		p.forgetKeyHash(keyID)
		return nil, sdkaccess.NewInvalidCredentialError()
	case err != nil:
		return nil, sdkaccess.NewInternalAuthError("api key revalidation failed", err)
	}
	if lookup.ID != keyID || strings.TrimSpace(lookup.UserID) == "" || strings.TrimSpace(previous.Principal) != strings.TrimSpace(lookup.UserID) {
		p.forgetKeyHash(keyID)
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	p.touch(lookup.ID)
	return p.result(lookup, "delegated"), nil
}

func (p *provider) rememberKeyHash(keyID, keyHash string) {
	if p == nil || p.lookupByID != nil {
		return
	}
	keyID = strings.TrimSpace(keyID)
	keyHash = strings.TrimSpace(keyHash)
	if keyID == "" || keyHash == "" {
		return
	}
	p.keyHashesMu.Lock()
	if p.keyHashes == nil {
		p.keyHashes = make(map[string]string)
	}
	p.keyHashes[keyID] = keyHash
	p.keyHashesMu.Unlock()
}

func (p *provider) keyHash(keyID string) (string, bool) {
	if p == nil {
		return "", false
	}
	p.keyHashesMu.RLock()
	keyHash, ok := p.keyHashes[strings.TrimSpace(keyID)]
	p.keyHashesMu.RUnlock()
	return keyHash, ok && keyHash != ""
}

func (p *provider) forgetKeyHash(keyID string) {
	if p == nil {
		return
	}
	p.keyHashesMu.Lock()
	delete(p.keyHashes, strings.TrimSpace(keyID))
	p.keyHashesMu.Unlock()
}

func (p *provider) result(lookup store.APIKeyLookup, source string) *sdkaccess.Result {
	meta := map[string]string{
		"source":                    source,
		billing.MetadataKeyUserID:   lookup.UserID,
		billing.MetadataKeyAPIKeyID: lookup.ID,
	}
	if lookup.Unbilled {
		meta[billing.MetadataKeyUnbilled] = "1"
	}
	if len(lookup.AllowedAuthIDs) > 0 {
		meta[billing.MetadataKeyAllowedAuthIDs] = store.EncodeAuthIDs(lookup.AllowedAuthIDs)
	}
	if len(lookup.AllowedModels) > 0 {
		meta[billing.MetadataKeyAllowedModels] = store.EncodeAuthIDs(lookup.AllowedModels)
	}
	return &sdkaccess.Result{Provider: p.Identifier(), Principal: lookup.UserID, Metadata: meta}
}

func (p *provider) touch(keyID string) {
	if p == nil || p.toucher == nil {
		return
	}
	go func() {
		if err := p.toucher(context.Background(), keyID); err != nil {
			log.WithError(err).Debugf("db-access: touch api key %s failed", keyID)
		}
	}()
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
