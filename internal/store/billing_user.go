package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUserNotFound is returned by user lookup helpers when no row matches.
var ErrUserNotFound = errors.New("postgres store: user not found")

// ErrEmailTaken is returned by CreateUser when the email is already registered.
var ErrEmailTaken = errors.New("postgres store: email already registered")

// User mirrors a row in the billing users table.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	DisplayName  string
	Status       string
	IsAdmin      bool
	// Per-user access controls, set only by the super admin.
	Unbilled       bool     // exempt from wallet balance check + debit
	AllowedModels  []string // restrict to these client-visible models (empty = all)
	AllowedAuthIDs []string // restrict to these upstream accounts (empty = all)
	AllowReset     bool     // may consume provider rate-limit reset credits
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// APIKeyRecord describes an api_keys row for portal listings (no plaintext).
type APIKeyRecord struct {
	ID         string
	UserID     string
	KeyPrefix  string
	Name       string
	LastUsedAt sql.NullTime
	RevokedAt  sql.NullTime
	CreatedAt  time.Time
}

// UsageRecord mirrors a row in the usage_records table for portal listings.
type UsageRecord struct {
	ID               string
	APIKeyID         sql.NullString
	RequestID        string
	Provider         string
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Cost             string
	Status           string
	ErrorMessage     string
	AuthID           string
	AuthLabel        string
	CreatedAt        time.Time
	// UserEmail is populated only by admin-wide listings (ListAllUsage); it is
	// empty for a single user's own ListUsage.
	UserEmail string
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows so scanUser can serve
// single-row and multi-row queries.
type rowScanner interface {
	Scan(dest ...any) error
}

// userSelectColumns is the canonical column list for loading a User; kept in
// sync with scanUser so every user query decodes the same shape.
const userSelectColumns = "id, email, password_hash, display_name, status, is_admin, unbilled, allowed_models, allowed_auth_ids, allow_reset, created_at, updated_at"

func scanUser(row rowScanner) (User, error) {
	var u User
	var modelsRaw, authRaw string
	if err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Status, &u.IsAdmin,
		&u.Unbilled, &modelsRaw, &authRaw, &u.AllowReset, &u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return User{}, err
	}
	u.AllowedModels = DecodeAuthIDs(modelsRaw)
	u.AllowedAuthIDs = DecodeAuthIDs(authRaw)
	return u, nil
}

// CreateUser inserts a new user and a zero-balance wallet in a single
// transaction. Returns ErrEmailTaken when the email is already present.
func (s *PostgresStore) CreateUser(ctx context.Context, email, passwordHash, displayName string) (User, error) {
	if s == nil || s.db == nil {
		return User{}, fmt.Errorf("postgres store: not initialized")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || passwordHash == "" {
		return User{}, fmt.Errorf("postgres store: email and password required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("postgres store: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				// Rollback failure is logged-only context; keep the original error.
				_ = rbErr
			}
		}
	}()

	insertUser := fmt.Sprintf(`
		INSERT INTO %s (email, password_hash, display_name)
		VALUES ($1, $2, $3)
		RETURNING %s
	`, s.fullTableName(BillingUsersTable), userSelectColumns)

	var u User
	u, err = scanUser(tx.QueryRowContext(ctx, insertUser, email, passwordHash, strings.TrimSpace(displayName)))
	if err != nil {
		if isUniqueViolation(err) {
			err = ErrEmailTaken
			return User{}, err
		}
		err = fmt.Errorf("postgres store: insert user: %w", err)
		return User{}, err
	}

	insertWallet := fmt.Sprintf(
		"INSERT INTO %s (user_id, balance) VALUES ($1, 0)",
		s.fullTableName(BillingWalletsTable),
	)
	if _, err = tx.ExecContext(ctx, insertWallet, u.ID); err != nil {
		err = fmt.Errorf("postgres store: insert wallet: %w", err)
		return User{}, err
	}

	if err = tx.Commit(); err != nil {
		return User{}, fmt.Errorf("postgres store: commit user: %w", err)
	}
	return u, nil
}

// GetUserByEmail loads a user row by lower-cased email.
func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	if s == nil || s.db == nil {
		return User{}, fmt.Errorf("postgres store: not initialized")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return User{}, ErrUserNotFound
	}

	query := fmt.Sprintf("SELECT %s FROM %s WHERE email = $1", userSelectColumns, s.fullTableName(BillingUsersTable))
	u, err := scanUser(s.db.QueryRowContext(ctx, query, email))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return User{}, ErrUserNotFound
	case err != nil:
		return User{}, fmt.Errorf("postgres store: get user: %w", err)
	}
	return u, nil
}

// GetUserByID loads a user row by id.
func (s *PostgresStore) GetUserByID(ctx context.Context, id string) (User, error) {
	if s == nil || s.db == nil {
		return User{}, fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(id) == "" {
		return User{}, ErrUserNotFound
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", userSelectColumns, s.fullTableName(BillingUsersTable))
	u, err := scanUser(s.db.QueryRowContext(ctx, query, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return User{}, ErrUserNotFound
	case err != nil:
		return User{}, fmt.Errorf("postgres store: get user: %w", err)
	}
	return u, nil
}

// CreateAPIKey persists a new api_key row owned by userID. The hash and prefix
// are computed by the caller so the plaintext key is never seen here. Per-user
// access controls live on the users table, so keys carry no per-key policy.
func (s *PostgresStore) CreateAPIKey(ctx context.Context, userID, keyHash, keyPrefix, name string) (APIKeyRecord, error) {
	if s == nil || s.db == nil {
		return APIKeyRecord{}, fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(keyHash) == "" {
		return APIKeyRecord{}, fmt.Errorf("postgres store: user id and key hash required")
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (user_id, key_hash, key_prefix, name)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, key_prefix, name, last_used_at, revoked_at, created_at
	`, s.fullTableName(BillingAPIKeysTable))
	var rec APIKeyRecord
	err := s.db.QueryRowContext(ctx, query, userID, keyHash, keyPrefix, strings.TrimSpace(name)).Scan(
		&rec.ID, &rec.UserID, &rec.KeyPrefix, &rec.Name, &rec.LastUsedAt, &rec.RevokedAt, &rec.CreatedAt,
	)
	if err != nil {
		return APIKeyRecord{}, fmt.Errorf("postgres store: insert api key: %w", err)
	}
	return rec, nil
}

// ListAPIKeys returns all keys owned by userID, newest first. Revoked keys are
// included so the user can audit them.
func (s *PostgresStore) ListAPIKeys(ctx context.Context, userID string) ([]APIKeyRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf(`
		SELECT id, user_id, key_prefix, name, last_used_at, revoked_at, created_at
		FROM %s WHERE user_id = $1
		ORDER BY created_at DESC
	`, s.fullTableName(BillingAPIKeysTable))
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list api keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]APIKeyRecord, 0, 8)
	for rows.Next() {
		var rec APIKeyRecord
		if err := rows.Scan(&rec.ID, &rec.UserID, &rec.KeyPrefix, &rec.Name, &rec.LastUsedAt, &rec.RevokedAt, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan api key: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate api keys: %w", err)
	}
	return out, nil
}

// RevokeAPIKey marks a key as revoked. Returns ErrAPIKeyNotFound when the key
// does not belong to userID or does not exist.
func (s *PostgresStore) RevokeAPIKey(ctx context.Context, userID, keyID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf(`
		UPDATE %s SET revoked_at = NOW()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
	`, s.fullTableName(BillingAPIKeysTable))
	res, err := s.db.ExecContext(ctx, query, keyID, userID)
	if err != nil {
		return fmt.Errorf("postgres store: revoke api key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: revoke api key rows: %w", err)
	}
	if n == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// GetWalletBalance returns the current balance for the user as a string
// (preserving NUMERIC precision). Missing wallets return "0".
func (s *PostgresStore) GetWalletBalance(ctx context.Context, userID string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf("SELECT balance::text FROM %s WHERE user_id = $1", s.fullTableName(BillingWalletsTable))
	var balance string
	err := s.db.QueryRowContext(ctx, query, userID).Scan(&balance)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "0", nil
	case err != nil:
		return "", fmt.Errorf("postgres store: get wallet: %w", err)
	}
	return balance, nil
}

// ListUsage returns up to limit usage records for the user, newest first.
// before is an exclusive upper bound on created_at for keyset pagination; pass
// the zero time to start from the most recent row.
// UsageFilter narrows a usage listing. Empty fields are ignored. UserID applies
// only to admin-wide listings (ListAllUsage); ListUsage is already scoped to one
// user.
type UsageFilter struct {
	Model  string
	AuthID string
	UserID string
}

// UserRef is a minimal user identity used to populate filter pickers.
type UserRef struct {
	ID    string
	Email string
}

func (s *PostgresStore) ListUsage(ctx context.Context, userID string, before time.Time, limit int, filter UsageFilter) ([]UsageRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var args []any
	var conds []string
	add := func(clause string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(clause, len(args)))
	}
	add("user_id = $%d", userID)
	if !before.IsZero() {
		add("created_at < $%d", before)
	}
	if m := strings.TrimSpace(filter.Model); m != "" {
		add("model = $%d", m)
	}
	if a := strings.TrimSpace(filter.AuthID); a != "" {
		add("auth_id = $%d", a)
	}
	args = append(args, limit)
	limitIdx := len(args)

	query := fmt.Sprintf(`
		SELECT id, api_key_id, request_id, provider, model,
		       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       cost::text, status, error_message, auth_id, auth_label, created_at
		FROM %s
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d
	`, s.fullTableName(BillingUsageRecordsTable), strings.Join(conds, " AND "), limitIdx)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]UsageRecord, 0, limit)
	for rows.Next() {
		var rec UsageRecord
		if err := rows.Scan(
			&rec.ID, &rec.APIKeyID, &rec.RequestID, &rec.Provider, &rec.Model,
			&rec.InputTokens, &rec.OutputTokens, &rec.CacheReadTokens, &rec.CacheWriteTokens,
			&rec.Cost, &rec.Status, &rec.ErrorMessage, &rec.AuthID, &rec.AuthLabel, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres store: scan usage: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate usage: %w", err)
	}
	return out, nil
}

// ListAllUsage returns usage across all users (admin view), each row annotated
// with the owning user's email. Ordering and pagination match ListUsage.
func (s *PostgresStore) ListAllUsage(ctx context.Context, before time.Time, limit int, filter UsageFilter) ([]UsageRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var args []any
	var conds []string
	add := func(clause string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(clause, len(args)))
	}
	if !before.IsZero() {
		add("ur.created_at < $%d", before)
	}
	if m := strings.TrimSpace(filter.Model); m != "" {
		add("ur.model = $%d", m)
	}
	if a := strings.TrimSpace(filter.AuthID); a != "" {
		add("ur.auth_id = $%d", a)
	}
	if u := strings.TrimSpace(filter.UserID); u != "" {
		add("ur.user_id = $%d", u)
	}
	args = append(args, limit)
	limitIdx := len(args)
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT ur.id, ur.api_key_id, ur.request_id, ur.provider, ur.model,
		       ur.input_tokens, ur.output_tokens, ur.cache_read_tokens, ur.cache_write_tokens,
		       ur.cost::text, ur.status, ur.error_message, ur.auth_id, ur.auth_label, ur.created_at,
		       COALESCE(u.email, '')
		FROM %s ur
		LEFT JOIN %s u ON u.id = ur.user_id%s
		ORDER BY ur.created_at DESC
		LIMIT $%d
	`, s.fullTableName(BillingUsageRecordsTable), s.fullTableName(BillingUsersTable), where, limitIdx)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list all usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]UsageRecord, 0, limit)
	for rows.Next() {
		var rec UsageRecord
		if err := rows.Scan(
			&rec.ID, &rec.APIKeyID, &rec.RequestID, &rec.Provider, &rec.Model,
			&rec.InputTokens, &rec.OutputTokens, &rec.CacheReadTokens, &rec.CacheWriteTokens,
			&rec.Cost, &rec.Status, &rec.ErrorMessage, &rec.AuthID, &rec.AuthLabel, &rec.CreatedAt,
			&rec.UserEmail,
		); err != nil {
			return nil, fmt.Errorf("postgres store: scan all usage: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate all usage: %w", err)
	}
	return out, nil
}

// UsageFilterOptions returns the distinct filter values that actually appear in
// usage rows, so the UI only offers meaningful choices. When userID is non-empty
// the scope is that user's own rows; empty userID = all users (admin) and also
// returns the distinct owning users.
func (s *PostgresStore) UsageFilterOptions(ctx context.Context, userID string) (models []string, authIDs []string, users []UserRef, err error) {
	if s == nil || s.db == nil {
		return nil, nil, nil, fmt.Errorf("postgres store: not initialized")
	}
	usage := s.fullTableName(BillingUsageRecordsTable)
	usersTbl := s.fullTableName(BillingUsersTable)

	scope := ""
	var scopeArgs []any
	if uid := strings.TrimSpace(userID); uid != "" {
		scope = " AND user_id = $1"
		scopeArgs = []any{uid}
	}

	distinct := func(col string) ([]string, error) {
		q := fmt.Sprintf("SELECT DISTINCT %s FROM %s WHERE %s <> ''%s ORDER BY %s", col, usage, col, scope, col)
		rows, errQ := s.db.QueryContext(ctx, q, scopeArgs...)
		if errQ != nil {
			return nil, errQ
		}
		defer func() { _ = rows.Close() }()
		var vals []string
		for rows.Next() {
			var v string
			if errS := rows.Scan(&v); errS != nil {
				return nil, errS
			}
			vals = append(vals, v)
		}
		return vals, rows.Err()
	}

	if models, err = distinct("model"); err != nil {
		return nil, nil, nil, fmt.Errorf("postgres store: usage models: %w", err)
	}
	if authIDs, err = distinct("auth_id"); err != nil {
		return nil, nil, nil, fmt.Errorf("postgres store: usage accounts: %w", err)
	}

	// The owning-user picker only makes sense in the admin-wide scope.
	if strings.TrimSpace(userID) == "" {
		q := fmt.Sprintf(`SELECT DISTINCT ur.user_id, COALESCE(u.email, '')
			FROM %s ur LEFT JOIN %s u ON u.id = ur.user_id
			WHERE ur.user_id IS NOT NULL ORDER BY 2`, usage, usersTbl)
		rows, errQ := s.db.QueryContext(ctx, q)
		if errQ != nil {
			return nil, nil, nil, fmt.Errorf("postgres store: usage users: %w", errQ)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var ref UserRef
			if errS := rows.Scan(&ref.ID, &ref.Email); errS != nil {
				return nil, nil, nil, fmt.Errorf("postgres store: scan usage user: %w", errS)
			}
			users = append(users, ref)
		}
		if errR := rows.Err(); errR != nil {
			return nil, nil, nil, fmt.Errorf("postgres store: iterate usage users: %w", errR)
		}
	}
	return models, authIDs, users, nil
}

// UpdateUserPassword replaces the password hash for the given user id.
func (s *PostgresStore) UpdateUserPassword(ctx context.Context, userID, newPasswordHash string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(userID) == "" || newPasswordHash == "" {
		return fmt.Errorf("postgres store: user id and password required")
	}
	query := fmt.Sprintf(
		"UPDATE %s SET password_hash = $2, updated_at = NOW() WHERE id = $1",
		s.fullTableName(BillingUsersTable),
	)
	res, err := s.db.ExecContext(ctx, query, userID, newPasswordHash)
	if err != nil {
		return fmt.Errorf("postgres store: update password: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: update password rows: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// PromoteUserToAdmin sets is_admin=true for the user with the given email.
// Returns ErrUserNotFound when no row matches so callers can decide whether
// to retry later (e.g. after the operator registers).
func (s *PostgresStore) PromoteUserToAdmin(ctx context.Context, email string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ErrUserNotFound
	}
	query := fmt.Sprintf(
		"UPDATE %s SET is_admin = TRUE, updated_at = NOW() WHERE email = $1",
		s.fullTableName(BillingUsersTable),
	)
	res, err := s.db.ExecContext(ctx, query, email)
	if err != nil {
		return fmt.Errorf("postgres store: promote admin: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: promote admin rows: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetUserLimits replaces the per-user access controls (unbilled + allowed
// models + allowed upstream accounts + reset permission) for the given user.
// Empty slices clear the corresponding restriction. Returns ErrUserNotFound
// when no row matches.
func (s *PostgresStore) SetUserLimits(ctx context.Context, userID string, unbilled bool, allowedModels, allowedAuthIDs []string, allowReset bool) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(userID) == "" {
		return ErrUserNotFound
	}
	query := fmt.Sprintf(
		"UPDATE %s SET unbilled = $2, allowed_models = $3, allowed_auth_ids = $4, allow_reset = $5, updated_at = NOW() WHERE id = $1",
		s.fullTableName(BillingUsersTable),
	)
	res, err := s.db.ExecContext(ctx, query, userID, unbilled, EncodeAuthIDs(allowedModels), EncodeAuthIDs(allowedAuthIDs), allowReset)
	if err != nil {
		return fmt.Errorf("postgres store: set user limits: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: set user limits rows: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// isUniqueViolation reports whether err looks like a Postgres unique_violation
// (SQLSTATE 23505). We avoid importing pgconn to keep the dependency surface
// small; the textual check is sufficient for the stdlib database/sql path.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505") || strings.Contains(msg, "unique constraint")
}
