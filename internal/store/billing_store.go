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
	if s == nil || s.db == nil {
		return APIKeyLookup{}, fmt.Errorf("postgres store: not initialized")
	}
	keyHash = strings.TrimSpace(keyHash)
	if keyHash == "" {
		return APIKeyLookup{}, ErrAPIKeyNotFound
	}

	query := fmt.Sprintf(
		"SELECT id, user_id FROM %s WHERE key_hash = $1 AND revoked_at IS NULL",
		s.fullTableName(BillingAPIKeysTable),
	)
	var out APIKeyLookup
	err := s.db.QueryRowContext(ctx, query, keyHash).Scan(&out.ID, &out.UserID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return APIKeyLookup{}, ErrAPIKeyNotFound
	case err != nil:
		return APIKeyLookup{}, fmt.Errorf("postgres store: lookup api key: %w", err)
	}
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
