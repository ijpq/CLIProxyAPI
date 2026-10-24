package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

const MaxActiveAPIKeys = 10

var ErrAPIKeyLimit = errors.New("active API key limit reached")
var ErrInvalidKeyPolicy = errors.New("invalid API key model policy")

// APIKeyPolicy distinguishes unrestricted access from an explicit denial.
// Account restrictions and the published model catalog always apply as well.
type APIKeyPolicy struct {
	ModelScope    string   `json:"model_scope"`
	AllowedModels []string `json:"allowed_models"`
}

func NormalizeKeyPolicy(p APIKeyPolicy) (APIKeyPolicy, error) {
	if p.ModelScope == "" && len(p.AllowedModels) == 0 {
		p.ModelScope = "all"
	}
	if p.ModelScope != "all" && p.ModelScope != "selected" && p.ModelScope != "none" {
		return APIKeyPolicy{}, ErrInvalidKeyPolicy
	}
	if len(p.AllowedModels) > 256 || (p.ModelScope != "selected" && len(p.AllowedModels) != 0) {
		return APIKeyPolicy{}, ErrInvalidKeyPolicy
	}
	models := make([]string, 0, len(p.AllowedModels))
	seen := make(map[string]bool)
	for _, model := range p.AllowedModels {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 256 || strings.Contains(model, ",") || strings.ContainsFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return APIKeyPolicy{}, ErrInvalidKeyPolicy
		}
		model = accessModelName(model)
		if model == "" {
			return APIKeyPolicy{}, ErrInvalidKeyPolicy
		}
		if !seen[model] {
			models = append(models, model)
			seen[model] = true
		}
	}
	if p.ModelScope == "selected" && len(models) == 0 {
		return APIKeyPolicy{}, ErrInvalidKeyPolicy
	}
	p.AllowedModels = models
	return p, nil
}

func accessModelName(model string) string {
	return strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName))
}

// A nil slice is unrestricted; an allocated empty slice denies all models.
func intersectModels(account, models []string) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		if account == nil {
			out = append(out, model)
			continue
		}
		for _, allowed := range account {
			if accessModelName(allowed) == accessModelName(model) {
				out = append(out, model)
				break
			}
		}
	}
	return out
}

func effectiveKeyModels(account []string, p APIKeyPolicy) []string {
	switch p.ModelScope {
	case "all":
		return account
	case "selected":
		return intersectModels(account, p.AllowedModels)
	default:
		return []string{}
	}
}

const apiKeyColumns = "id, user_id, key_prefix, name, last_used_at, revoked_at, created_at, model_scope, allowed_models"

func scanAPIKey(row rowScanner) (APIKeyRecord, error) {
	var rec APIKeyRecord
	var models string
	err := row.Scan(&rec.ID, &rec.UserID, &rec.KeyPrefix, &rec.Name, &rec.LastUsedAt, &rec.RevokedAt, &rec.CreatedAt, &rec.ModelScope, &models)
	rec.AllowedModels = DecodeAuthIDs(models)
	return rec, err
}

// CreateAPIKey serializes creation on the owner row so concurrent requests
// cannot exceed the active-key cap. Plaintext keys are never persisted.
func (s *PostgresStore) CreateAPIKey(ctx context.Context, userID, keyHash, keyPrefix, name string, policies ...APIKeyPolicy) (APIKeyRecord, error) {
	if s == nil || s.db == nil {
		return APIKeyRecord{}, fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(keyHash) == "" {
		return APIKeyRecord{}, fmt.Errorf("postgres store: user id and key hash required")
	}
	if len(policies) > 1 {
		return APIKeyRecord{}, ErrInvalidKeyPolicy
	}
	policy := APIKeyPolicy{}
	if len(policies) == 1 {
		policy = policies[0]
	}
	policy, err := NormalizeKeyPolicy(policy)
	if err != nil {
		return APIKeyRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKeyRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var owner string
	query := fmt.Sprintf("SELECT id FROM %s WHERE id=$1 AND status='active' FOR UPDATE", s.fullTableName(BillingUsersTable))
	if err = tx.QueryRowContext(ctx, query, userID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIKeyRecord{}, ErrUserNotFound
		}
		return APIKeyRecord{}, err
	}
	var count int
	query = fmt.Sprintf("SELECT count(*) FROM %s WHERE user_id=$1 AND revoked_at IS NULL", s.fullTableName(BillingAPIKeysTable))
	if err = tx.QueryRowContext(ctx, query, userID).Scan(&count); err != nil {
		return APIKeyRecord{}, err
	}
	if count >= MaxActiveAPIKeys {
		return APIKeyRecord{}, ErrAPIKeyLimit
	}
	query = fmt.Sprintf("INSERT INTO %s(user_id,key_hash,key_prefix,name,model_scope,allowed_models) VALUES($1,$2,$3,$4,$5,$6) RETURNING %s", s.fullTableName(BillingAPIKeysTable), apiKeyColumns)
	rec, err := scanAPIKey(tx.QueryRowContext(ctx, query, userID, keyHash, keyPrefix, strings.TrimSpace(name), policy.ModelScope, EncodeAuthIDs(policy.AllowedModels)))
	if err != nil {
		return APIKeyRecord{}, fmt.Errorf("postgres store: insert api key: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return APIKeyRecord{}, err
	}
	return rec, nil
}

// ListAPIKeys includes revoked keys for the owner's history, without secrets.
func (s *PostgresStore) ListAPIKeys(ctx context.Context, userID string) ([]APIKeyRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE user_id=$1 ORDER BY created_at DESC", apiKeyColumns, s.fullTableName(BillingAPIKeysTable))
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]APIKeyRecord, 0)
	for rows.Next() {
		rec, errScan := scanAPIKey(rows)
		if errScan != nil {
			return nil, errScan
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateAPIKeyPolicy(ctx context.Context, userID, keyID string, p APIKeyPolicy) (APIKeyRecord, error) {
	if s == nil || s.db == nil {
		return APIKeyRecord{}, fmt.Errorf("postgres store: not initialized")
	}
	p, err := NormalizeKeyPolicy(p)
	if err != nil {
		return APIKeyRecord{}, err
	}
	query := fmt.Sprintf("UPDATE %s SET model_scope=$3,allowed_models=$4 WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL RETURNING %s", s.fullTableName(BillingAPIKeysTable), apiKeyColumns)
	rec, err := scanAPIKey(s.db.QueryRowContext(ctx, query, keyID, userID, p.ModelScope, EncodeAuthIDs(p.AllowedModels)))
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	return rec, err
}

func (s *PostgresStore) RevokeAPIKey(ctx context.Context, userID, keyID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf("UPDATE %s SET revoked_at=NOW() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL", s.fullTableName(BillingAPIKeysTable))
	res, err := s.db.ExecContext(ctx, query, keyID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}
