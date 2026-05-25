package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ListAllUsers returns all billing users, newest first.
func (s *PostgresStore) ListAllUsers(ctx context.Context, limit int) ([]User, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := fmt.Sprintf(`
		SELECT u.id, u.email, u.password_hash, u.display_name, u.status, u.is_admin,
		       u.created_at, u.updated_at
		FROM %s u
		ORDER BY u.created_at DESC
		LIMIT $1
	`, s.fullTableName(BillingUsersTable))
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]User, 0, limit)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Status, &u.IsAdmin, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AdminCreditWallet adds the given amount to a user's wallet and records a
// ledger entry with type 'adjust'. Returns the new balance.
func (s *PostgresStore) AdminCreditWallet(ctx context.Context, userID, amountStr, reference, note string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(amountStr) == "" {
		return "", fmt.Errorf("postgres store: user id and amount required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("postgres store: begin credit tx: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				_ = rbErr
			}
		}
	}()

	upsert := fmt.Sprintf(`
		INSERT INTO %s (user_id, balance, updated_at)
		VALUES ($1, $2::numeric, NOW())
		ON CONFLICT (user_id)
		DO UPDATE SET balance = %s.balance + $2::numeric, updated_at = NOW()
		RETURNING balance::text
	`, s.fullTableName(BillingWalletsTable), s.fullTableName(BillingWalletsTable))

	var newBalance string
	if err = tx.QueryRowContext(ctx, upsert, userID, amountStr).Scan(&newBalance); err != nil {
		err = fmt.Errorf("postgres store: credit wallet: %w", err)
		return "", err
	}

	if reference == "" {
		reference = "admin-adjust"
	}
	insertTxn := fmt.Sprintf(`
		INSERT INTO %s (user_id, type, amount, balance_after, reference, metadata)
		VALUES ($1, 'adjust', $2::numeric, $3::numeric, $4, $5::jsonb)
	`, s.fullTableName(BillingTransactionsTable))
	meta := "{}"
	if note != "" {
		meta = fmt.Sprintf(`{"note":%q}`, note)
	}
	if _, err = tx.ExecContext(ctx, insertTxn, userID, amountStr, newBalance, reference, meta); err != nil {
		err = fmt.Errorf("postgres store: insert adjust txn: %w", err)
		return "", err
	}

	if err = tx.Commit(); err != nil {
		return "", fmt.Errorf("postgres store: commit credit tx: %w", err)
	}
	return newBalance, nil
}
