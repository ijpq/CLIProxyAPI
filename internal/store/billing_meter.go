package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billingvalue"
)

// MeteredUsage captures the inputs for a single accounting event written by
// the billing plugin.
type MeteredUsage struct {
	EventID          string
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
	PriceVersion int64
	Status       string
	ErrorMessage string
	// AuthID and AuthLabel identify which upstream account (credential) served
	// the request, so audit logs can show which account's quota was consumed.
	AuthID    string
	AuthLabel string
	// SkipDebit records the usage row for audit but leaves the wallet and
	// transactions ledger untouched. Used for privileged (unbilled) accounts.
	SkipDebit bool
}

// RecordUsageAndDebit writes a usage_records row, debits the wallet, and
// appends a transactions row in a single Postgres transaction. The wallet row
// is locked FOR UPDATE while balance_after is computed so concurrent debits
// remain consistent.
//
// Completed work is accounted for even when its cost exceeds the balance.
// Request admission still requires a separate reservation policy to prevent debt.
func (s *PostgresStore) RecordUsageAndDebit(ctx context.Context, u MeteredUsage) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(u.UserID) == "" {
		return fmt.Errorf("postgres store: user id required")
	}
	if u.EventID == "" {
		u.EventID = uuid.NewString()
	}
	if u.Cost == "" {
		u.Cost = "0"
	}
	normalizedCost, err := billingvalue.Normalize(u.Cost, false)
	if err != nil {
		return err
	}
	u.Cost = normalizedCost
	payload, err := json.Marshal(u)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))

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
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s(event_id,digest) VALUES($1,$2) ON CONFLICT DO NOTHING`, s.fullTableName("billing_settled_events")), u.EventID, digest)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		var existing string
		if err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT digest FROM %s WHERE event_id=$1`, s.fullTableName("billing_settled_events")), u.EventID).Scan(&existing); err != nil {
			return err
		}
		if existing != digest {
			err = errors.New("billing event ID was reused with different accounting data")
			return err
		}
		return tx.Commit()
	}

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

	// The usage record and debit commit together; failures roll both back.
	insertUsage := fmt.Sprintf(`
		INSERT INTO %s (
			user_id, api_key_id, request_id, provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cost, status, error_message, auth_id, auth_label, price_version, unit
		) VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8, $9, $10::numeric, $11, $12, $13, $14, $15, 'POINTS')
	`, s.fullTableName(BillingUsageRecordsTable))

	status := u.Status
	if status == "" {
		status = "success"
	}

	if _, err = tx.ExecContext(ctx, insertUsage,
		u.UserID, u.APIKeyID, u.RequestID, u.Provider, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens,
		cost, status, u.ErrorMessage, u.AuthID, u.AuthLabel, u.PriceVersion,
	); err != nil {
		err = fmt.Errorf("postgres store: insert usage: %w", err)
		return err
	}

	// Privileged accounts are audited but not billed: the usage row above is
	// the full record; skip the wallet debit and ledger entry.
	if u.SkipDebit {
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("postgres store: commit meter tx: %w", err)
		}
		return nil
	}

	// Upsert the wallet by subtracting cost and update transactions ledger.
	upsertWallet := fmt.Sprintf(`
		INSERT INTO %s (user_id, balance, updated_at, unit)
		VALUES ($1, -$2::numeric, NOW(), 'POINTS')
		ON CONFLICT (user_id)
		DO UPDATE SET balance = %s.balance - $2::numeric, updated_at = NOW()
		WHERE %s.unit='POINTS'
		RETURNING balance::text
	`, s.fullTableName(BillingWalletsTable), s.fullTableName(BillingWalletsTable), s.fullTableName(BillingWalletsTable))

	var newBalance string
	if err = tx.QueryRowContext(ctx, upsertWallet, u.UserID, cost).Scan(&newBalance); err != nil {
		err = fmt.Errorf("postgres store: update wallet: %w", err)
		return err
	}

	insertTxn := fmt.Sprintf(`
		INSERT INTO %s (user_id, type, amount, balance_after, reference, metadata, unit)
		VALUES ($1, 'consume', -$2::numeric, $3::numeric, $4, $5::jsonb, 'POINTS')
	`, s.fullTableName(BillingTransactionsTable))

	meta, _ := json.Marshal(map[string]any{"request_id": u.RequestID, "price_version": u.PriceVersion})
	if _, err = tx.ExecContext(ctx, insertTxn, u.UserID, cost, newBalance, u.EventID, meta); err != nil {
		err = fmt.Errorf("postgres store: insert transaction: %w", err)
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit meter tx: %w", err)
	}
	return nil
}
