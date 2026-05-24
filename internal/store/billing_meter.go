package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// MeteredUsage captures the inputs for a single accounting event written by
// the billing plugin.
type MeteredUsage struct {
	UserID           string
	APIKeyID         string
	RequestID        string
	Provider         string
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	// Cost is the pre-computed charge expressed as a NUMERIC-compatible
	// decimal string (e.g. "0.012345"). The caller is responsible for
	// rounding to the schema's precision.
	Cost         string
	Status       string
	ErrorMessage string
}

// RecordUsageAndDebit writes a usage_records row, debits the wallet, and
// appends a transactions row in a single Postgres transaction. The wallet row
// is locked FOR UPDATE while balance_after is computed so concurrent debits
// remain consistent.
//
// The method does not refuse a debit that would push the balance below zero
// — pre-flight rate limiting is the layer responsible for that decision. A
// missing wallet is treated as zero so the user is still charged once a
// top-up happens.
func (s *PostgresStore) RecordUsageAndDebit(ctx context.Context, u MeteredUsage) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(u.UserID) == "" {
		return fmt.Errorf("postgres store: user id required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin meter tx: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				_ = rbErr
			}
		}
	}()

	// Lock the wallet row (or fall back to zero when none exists yet).
	lockQuery := fmt.Sprintf(
		"SELECT balance::text FROM %s WHERE user_id = $1 FOR UPDATE",
		s.fullTableName(BillingWalletsTable),
	)
	var balanceStr string
	err = tx.QueryRowContext(ctx, lockQuery, u.UserID).Scan(&balanceStr)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		balanceStr = "0"
		err = nil
	case err != nil:
		err = fmt.Errorf("postgres store: lock wallet: %w", err)
		return err
	}

	cost := strings.TrimSpace(u.Cost)
	if cost == "" {
		cost = "0"
	}

	// Insert the usage record first so usage history exists even when the
	// wallet update fails halfway through.
	insertUsage := fmt.Sprintf(`
		INSERT INTO %s (
			user_id, api_key_id, request_id, provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cost, status, error_message
		) VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8, $9, $10::numeric, $11, $12)
	`, s.fullTableName(BillingUsageRecordsTable))

	status := u.Status
	if status == "" {
		status = "success"
	}

	if _, err = tx.ExecContext(ctx, insertUsage,
		u.UserID, u.APIKeyID, u.RequestID, u.Provider, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens,
		cost, status, u.ErrorMessage,
	); err != nil {
		err = fmt.Errorf("postgres store: insert usage: %w", err)
		return err
	}

	// Upsert the wallet by subtracting cost and update transactions ledger.
	upsertWallet := fmt.Sprintf(`
		INSERT INTO %s (user_id, balance, updated_at)
		VALUES ($1, -$2::numeric, NOW())
		ON CONFLICT (user_id)
		DO UPDATE SET balance = %s.balance - $2::numeric, updated_at = NOW()
		RETURNING balance::text
	`, s.fullTableName(BillingWalletsTable), s.fullTableName(BillingWalletsTable))

	var newBalance string
	if err = tx.QueryRowContext(ctx, upsertWallet, u.UserID, cost).Scan(&newBalance); err != nil {
		err = fmt.Errorf("postgres store: update wallet: %w", err)
		return err
	}

	insertTxn := fmt.Sprintf(`
		INSERT INTO %s (user_id, type, amount, balance_after, reference, metadata)
		VALUES ($1, 'consume', -$2::numeric, $3::numeric, $4, '{}'::jsonb)
	`, s.fullTableName(BillingTransactionsTable))

	if _, err = tx.ExecContext(ctx, insertTxn, u.UserID, cost, newBalance, u.RequestID); err != nil {
		err = fmt.Errorf("postgres store: insert transaction: %w", err)
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit meter tx: %w", err)
	}
	return nil
}
