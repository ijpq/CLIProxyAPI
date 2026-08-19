package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// APIKeyLookup is the subset of api_key columns needed to authenticate a request
// and attribute downstream usage to the owning user.
type APIKeyLookup struct {
	ID     string
	UserID string
	// Per-user access controls (from the users table), applied to every key
	// the user owns. Set only by the super admin.
	Unbilled       bool     // exempt from wallet balance check + debit
	AllowedModels  []string // restrict to these client-visible models (empty = all)
	AllowedAuthIDs []string // restrict to these upstream accounts (empty = all)
}

// EncodeAuthIDs joins upstream auth IDs into the comma-separated form stored in
// the api_keys.bound_auth_ids column. Blank entries are dropped.
func EncodeAuthIDs(ids []string) string {
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			cleaned = append(cleaned, id)
		}
	}
	return strings.Join(cleaned, ",")
}

// DecodeAuthIDs splits the comma-separated bound_auth_ids column back into a
// slice, dropping blanks.
func DecodeAuthIDs(raw string) []string {
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

// ErrAPIKeyNotFound is returned by LookupAPIKey when the supplied raw key does
// not match any active row (unknown or revoked).
var ErrAPIKeyNotFound = errors.New("postgres store: api key not found")

// HashAPIKey hashes a raw API key using SHA-256 and returns the lowercase hex
// digest. Callers store and compare keys exclusively through this digest so the
// plaintext key value is never persisted.
func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// LookupAPIKey resolves a hashed API key to its owning user. Revoked keys are
// treated as missing.
func (s *PostgresStore) LookupAPIKey(ctx context.Context, keyHash string) (APIKeyLookup, error) {
	keyHash = strings.TrimSpace(keyHash)
	if keyHash == "" {
		return APIKeyLookup{}, ErrAPIKeyNotFound
	}
	return s.lookupAPIKey(ctx, "k.key_hash", keyHash)
}

// LookupAPIKeyByID refreshes the current owner and access state for an active
// API-key row. It is used to revalidate delegated Realtime client secrets.
func (s *PostgresStore) LookupAPIKeyByID(ctx context.Context, keyID string) (APIKeyLookup, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return APIKeyLookup{}, ErrAPIKeyNotFound
	}
	return s.lookupAPIKey(ctx, "k.id", keyID)
}

func (s *PostgresStore) lookupAPIKey(ctx context.Context, column, value string) (APIKeyLookup, error) {
	if s == nil || s.db == nil {
		return APIKeyLookup{}, fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf(
		`SELECT k.id, k.user_id, u.unbilled, u.allowed_models, u.allowed_auth_ids
		 FROM %s k JOIN %s u ON u.id = k.user_id
		 WHERE %s = $1 AND k.revoked_at IS NULL`,
		s.fullTableName(BillingAPIKeysTable), s.fullTableName(BillingUsersTable), column,
	)
	var out APIKeyLookup
	var modelsRaw, authRaw string
	err := s.db.QueryRowContext(ctx, query, value).Scan(&out.ID, &out.UserID, &out.Unbilled, &modelsRaw, &authRaw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return APIKeyLookup{}, ErrAPIKeyNotFound
	case err != nil:
		return APIKeyLookup{}, fmt.Errorf("postgres store: lookup api key: %w", err)
	}
	out.AllowedModels = DecodeAuthIDs(modelsRaw)
	out.AllowedAuthIDs = DecodeAuthIDs(authRaw)
	return out, nil
}

// TouchAPIKeyLastUsed updates the last_used_at column for the given key id.
// Failures are returned to the caller but are typically best-effort.
func (s *PostgresStore) TouchAPIKeyLastUsed(ctx context.Context, keyID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(keyID) == "" {
		return nil
	}
	query := fmt.Sprintf(
		"UPDATE %s SET last_used_at = NOW() WHERE id = $1",
		s.fullTableName(BillingAPIKeysTable),
	)
	if _, err := s.db.ExecContext(ctx, query, keyID); err != nil {
		return fmt.Errorf("postgres store: touch api key: %w", err)
	}
	return nil
}
