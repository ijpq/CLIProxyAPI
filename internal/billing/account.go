package billing

import (
	"strings"
	"sync"
)

// Account describes an upstream credential (auth) that privileged users can
// bind their API keys to. It is a projection of the core auth record limited to
// the fields the portal needs.
type Account struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Status   string `json:"status"`
}

// SplitCSV splits a comma-separated metadata value into a slice, dropping
// blanks. Lets callers that only depend on the billing package decode the
// allowed-accounts / allowed-models metadata without importing the store.
func SplitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// AccountLister returns the currently available upstream accounts.
type AccountLister func() []Account

var (
	accountListerMu sync.RWMutex
	accountLister   AccountLister
)

// SetAccountLister registers the function used to enumerate upstream accounts.
// Passing nil clears it. Wired at server startup from the core auth manager.
func SetAccountLister(fn AccountLister) {
	accountListerMu.Lock()
	accountLister = fn
	accountListerMu.Unlock()
}

// Accounts returns the currently available upstream accounts, or nil when no
// lister is registered.
func Accounts() []Account {
	accountListerMu.RLock()
	fn := accountLister
	accountListerMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// AccountLabel resolves an account ID to a human-readable label (the account's
// own label, falling back to its provider). Returns "" when the ID is unknown.
func AccountLabel(authID string) string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ""
	}
	for _, a := range Accounts() {
		if a.ID == authID {
			if strings.TrimSpace(a.Label) != "" {
				return a.Label
			}
			return a.Provider
		}
	}
	return ""
}

// ValidAccountIDs filters ids down to those that currently exist, preserving
// order and dropping blanks/duplicates. When no lister is registered it returns
// the cleaned input unchanged (validation is best-effort).
func ValidAccountIDs(ids []string) []string {
	cleaned := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	accounts := Accounts()
	if len(accounts) == 0 {
		return cleaned
	}
	valid := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		valid[a.ID] = struct{}{}
	}
	out := make([]string, 0, len(cleaned))
	for _, id := range cleaned {
		if _, ok := valid[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// ModelLister returns the currently available client-visible model names.
type ModelLister func() []string

var (
	modelListerMu sync.RWMutex
	modelLister   ModelLister
)

// SetModelLister registers the function used to enumerate all client-visible
// model names, for the admin's per-user model whitelist picker. Wired at
// startup from the model registry.
func SetModelLister(fn ModelLister) {
	modelListerMu.Lock()
	modelLister = fn
	modelListerMu.Unlock()
}

// Models returns the currently available client-visible model names, or nil.
func Models() []string {
	modelListerMu.RLock()
	fn := modelLister
	modelListerMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// ValidModels filters names down to those that currently exist, preserving
// order and dropping blanks/duplicates. When no lister is registered it returns
// the cleaned input unchanged (best-effort validation).
func ValidModels(names []string) []string {
	cleaned := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		cleaned = append(cleaned, n)
	}
	all := Models()
	if len(all) == 0 {
		return cleaned
	}
	valid := make(map[string]struct{}, len(all))
	for _, n := range all {
		valid[n] = struct{}{}
	}
	out := make([]string, 0, len(cleaned))
	for _, n := range cleaned {
		if _, ok := valid[n]; ok {
			out = append(out, n)
		}
	}
	return out
}
