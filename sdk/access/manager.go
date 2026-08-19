package access

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Manager coordinates authentication providers.
type Manager struct {
	mu        sync.RWMutex
	providers []Provider
}

// NewManager constructs an empty manager.
func NewManager() *Manager {
	return &Manager{}
}

// SetProviders replaces the active provider list.
func (m *Manager) SetProviders(providers []Provider) {
	if m == nil {
		return
	}
	cloned := make([]Provider, len(providers))
	copy(cloned, providers)
	m.mu.Lock()
	m.providers = cloned
	m.mu.Unlock()
}

// Providers returns a snapshot of the active providers.
func (m *Manager) Providers() []Provider {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot := make([]Provider, len(m.providers))
	copy(snapshot, m.providers)
	return snapshot
}

// Authenticate evaluates providers until one succeeds.
func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError) {
	if m == nil {
		return nil, nil
	}
	providers := m.Providers()
	if len(providers) == 0 {
		return nil, nil
	}

	var (
		missing bool
		invalid bool
	)

	for _, provider := range providers {
		if provider == nil {
			continue
		}
		res, authErr := provider.Authenticate(ctx, r)
		if authErr == nil {
			return res, nil
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNotHandled) {
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNoCredentials) {
			missing = true
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeInvalidCredential) {
			invalid = true
			continue
		}
		return nil, authErr
	}

	if invalid {
		return nil, NewInvalidCredentialError()
	}
	if missing {
		return nil, NewNoCredentialsError()
	}
	return nil, NewNoCredentialsError()
}

// Revalidate refreshes a prior authentication result through the provider that
// issued it. handled is false when that provider does not support revalidation.
func (m *Manager) Revalidate(ctx context.Context, previous *Result) (result *Result, handled bool, authErr *AuthError) {
	if m == nil || previous == nil {
		return nil, false, nil
	}
	providerID := strings.TrimSpace(previous.Provider)
	if providerID == "" {
		return nil, false, nil
	}
	for _, provider := range m.Providers() {
		if provider == nil || !strings.EqualFold(strings.TrimSpace(provider.Identifier()), providerID) {
			continue
		}
		revalidator, ok := provider.(Revalidator)
		if !ok {
			continue
		}
		result, authErr = revalidator.Revalidate(ctx, previous)
		if IsAuthErrorCode(authErr, AuthErrorCodeNotHandled) {
			continue
		}
		if authErr == nil && result == nil {
			return nil, true, NewInternalAuthError("authentication revalidation returned no result", nil)
		}
		return result, true, authErr
	}
	return nil, false, nil
}
