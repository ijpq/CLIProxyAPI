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

// SplitBoundAuthIDs splits the comma-separated bound-account metadata value
// into a slice, dropping blanks. Lets callers that only depend on the billing
// package decode MetadataKeyBoundAuthIDs without importing the store package.
func SplitBoundAuthIDs(raw string) []string {
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
