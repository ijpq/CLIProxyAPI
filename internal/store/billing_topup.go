package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billingvalue"
)

// ErrTopupOrderNotFound is returned when a topup order id does not exist or
// does not belong to the calling user.
var ErrTopupOrderNotFound = errors.New("postgres store: topup order not found")

// ErrTopupOrderNotPending is returned when a state transition is attempted on
// an order that is already confirmed, cancelled, or otherwise terminal.
var ErrTopupOrderNotPending = errors.New("postgres store: topup order not in a transitionable state")

// ErrTopupTxHashTaken is returned when a tx hash submission collides with an
// existing order's tx hash (the unique index fires).
var ErrTopupTxHashTaken = errors.New("postgres store: topup tx hash already used")

// ErrTopupAmountCollision is returned when CreateTopupOrder encounters the
// active method+amount uniqueness constraint. The caller should retry with a
// different fractional suffix.
var ErrTopupAmountCollision = errors.New("postgres store: topup amount collision")

// TopupOrder mirrors a row in the topup_orders table.
type TopupOrder struct {
	ID            string
	UserID        string
	Method        string
	Amount        string // Original payment amount, not points.
	PointsAmount  string
	ExchangeRate  string
	Currency      string
	Network       string
	WalletAddress string
	TxHash        string
	Status        string
	Notes         string
	CreatedAt     time.Time
	SubmittedAt   sql.NullTime
	ConfirmedAt   sql.NullTime
	ExpiresAt     sql.NullTime
}

// CreateTopupOrder records a new pending order. The wallet address is snapshot
// at creation time so a later address rotation does not break in-flight orders.
func (s *PostgresStore) CreateTopupOrder(ctx context.Context, userID, method, amountStr, currency, network, walletAddress string, ttl time.Duration) (TopupOrder, error) {
	if s == nil || s.db == nil {
		return TopupOrder{}, fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(amountStr) == "" {
		return TopupOrder{}, fmt.Errorf("postgres store: user id and amount required")
	}
	if _, err := s.GetWalletBalance(ctx, userID); err != nil {
		return TopupOrder{}, err
	}
	cfg, err := s.GetPortalSettings(ctx)
	if err != nil {
		return TopupOrder{}, err
	}
	rate := ""
	switch strings.ToUpper(currency) {
	case "CNY":
		rate = cfg.CNYPointsPerUnit
	case "USDT":
		rate = cfg.USDTPointsPerUnit
	}
	rate, err = billingvalue.Normalize(rate, true)
	if err != nil {
		return TopupOrder{}, ErrPointsRateMissing
	}
	if _, err = billingvalue.Normalize(amountStr, true); err != nil {
		return TopupOrder{}, err
	}
	var expires sql.NullTime
	if ttl > 0 {
		expires = sql.NullTime{Time: time.Now().Add(ttl), Valid: true}
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (user_id, method, amount, currency, network, wallet_address, expires_at, exchange_rate, points_amount)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8::numeric, TRUNC($3::numeric * $8::numeric, 6))
		RETURNING id, user_id, method, amount::text, currency, network, wallet_address,
		          tx_hash, status, notes, created_at, submitted_at, confirmed_at, expires_at, COALESCE(points_amount::text,''), COALESCE(exchange_rate::text,'')
	`, s.fullTableName(BillingTopupOrdersTable))

	var o TopupOrder
	err = s.db.QueryRowContext(ctx, query, userID, method, amountStr, currency, network, walletAddress, expires, rate).Scan(
		&o.ID, &o.UserID, &o.Method, &o.Amount, &o.Currency, &o.Network, &o.WalletAddress,
		&o.TxHash, &o.Status, &o.Notes, &o.CreatedAt, &o.SubmittedAt, &o.ConfirmedAt, &o.ExpiresAt, &o.PointsAmount, &o.ExchangeRate,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return TopupOrder{}, ErrTopupAmountCollision
		}
		return TopupOrder{}, fmt.Errorf("postgres store: insert topup order: %w", err)
	}
	return o, nil
}

// GetTopupOrder loads an order by id. When userID is non-empty the order must
// belong to that user (used by the user portal); admin handlers pass an empty
// string to allow cross-user access.
func (s *PostgresStore) GetTopupOrder(ctx context.Context, id, userID string) (TopupOrder, error) {
	if s == nil || s.db == nil {
		return TopupOrder{}, fmt.Errorf("postgres store: not initialized")
	}
	if strings.TrimSpace(id) == "" {
		return TopupOrder{}, ErrTopupOrderNotFound
	}
	args := []any{id}
	where := "id = $1"
	if strings.TrimSpace(userID) != "" {
		where += " AND user_id = $2"
		args = append(args, userID)
	}
	query := fmt.Sprintf(`
		SELECT id, user_id, method, amount::text, currency, network, wallet_address,
		       tx_hash, status, notes, created_at, submitted_at, confirmed_at, expires_at, COALESCE(points_amount::text,''), COALESCE(exchange_rate::text,'')
		FROM %s WHERE %s
	`, s.fullTableName(BillingTopupOrdersTable), where)
	var o TopupOrder
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&o.ID, &o.UserID, &o.Method, &o.Amount, &o.Currency, &o.Network, &o.WalletAddress,
		&o.TxHash, &o.Status, &o.Notes, &o.CreatedAt, &o.SubmittedAt, &o.ConfirmedAt, &o.ExpiresAt, &o.PointsAmount, &o.ExchangeRate,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TopupOrder{}, ErrTopupOrderNotFound
	case err != nil:
		return TopupOrder{}, fmt.Errorf("postgres store: get topup order: %w", err)
	}
	return o, nil
}

// ListTopupOrders returns recent orders, scoped to userID when non-empty.
func (s *PostgresStore) ListTopupOrders(ctx context.Context, userID string, limit int) ([]TopupOrder, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []any{}
	where := ""
	if strings.TrimSpace(userID) != "" {
		where = "WHERE user_id = $1"
		args = append(args, userID)
	}
	args = append(args, limit)
	query := fmt.Sprintf(`
		SELECT id, user_id, method, amount::text, currency, network, wallet_address,
		       tx_hash, status, notes, created_at, submitted_at, confirmed_at, expires_at, COALESCE(points_amount::text,''), COALESCE(exchange_rate::text,'')
		FROM %s %s
		ORDER BY created_at DESC
		LIMIT $%d
	`, s.fullTableName(BillingTopupOrdersTable), where, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list topup orders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]TopupOrder, 0, limit)
	for rows.Next() {
		var o TopupOrder
		if err := rows.Scan(
			&o.ID, &o.UserID, &o.Method, &o.Amount, &o.Currency, &o.Network, &o.WalletAddress,
			&o.TxHash, &o.Status, &o.Notes, &o.CreatedAt, &o.SubmittedAt, &o.ConfirmedAt, &o.ExpiresAt, &o.PointsAmount, &o.ExchangeRate,
		); err != nil {
			return nil, fmt.Errorf("postgres store: scan topup order: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate topup orders: %w", err)
	}
	return out, nil
}

// SubmitTopupTxHash attaches a tx hash to a pending order and moves it to the
// submitted state, ready for admin confirmation.
func (s *PostgresStore) SubmitTopupTxHash(ctx context.Context, userID, orderID, txHash string) (TopupOrder, error) {
	if s == nil || s.db == nil {
		return TopupOrder{}, fmt.Errorf("postgres store: not initialized")
	}
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if txHash == "" {
		return TopupOrder{}, fmt.Errorf("postgres store: tx hash required")
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET tx_hash = $3, status = 'submitted', submitted_at = NOW()
		WHERE id = $1 AND user_id = $2
		  AND (status = 'pending' OR (status = 'submitted' AND LOWER(BTRIM(tx_hash)) = $3))
		  AND (expires_at IS NULL OR expires_at > clock_timestamp())
		RETURNING id, user_id, method, amount::text, currency, network, wallet_address,
		          tx_hash, status, notes, created_at, submitted_at, confirmed_at, expires_at, COALESCE(points_amount::text,''), COALESCE(exchange_rate::text,'')
	`, s.fullTableName(BillingTopupOrdersTable))
	var o TopupOrder
	err := s.db.QueryRowContext(ctx, query, orderID, userID, txHash).Scan(
		&o.ID, &o.UserID, &o.Method, &o.Amount, &o.Currency, &o.Network, &o.WalletAddress,
		&o.TxHash, &o.Status, &o.Notes, &o.CreatedAt, &o.SubmittedAt, &o.ConfirmedAt, &o.ExpiresAt, &o.PointsAmount, &o.ExchangeRate,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TopupOrder{}, ErrTopupOrderNotPending
	case isUniqueViolation(err):
		return TopupOrder{}, ErrTopupTxHashTaken
	case err != nil:
		return TopupOrder{}, fmt.Errorf("postgres store: submit topup tx hash: %w", err)
	}
	return o, nil
}

// ConfirmTopupOrder credits the wallet and records a ledger entry atomically.
// The transactions unique index on (reference) WHERE type='topup' prevents
// double-crediting the same order. Returns the updated order.
func (s *PostgresStore) ConfirmTopupOrder(ctx context.Context, orderID, adminNote string) (TopupOrder, error) {
	if s == nil || s.db == nil {
		return TopupOrder{}, fmt.Errorf("postgres store: not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TopupOrder{}, fmt.Errorf("postgres store: begin confirm tx: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				_ = rbErr
			}
		}
	}()

	// Lock the order row and grab the fields we need.
	lockQuery := fmt.Sprintf(`
		SELECT user_id, COALESCE(points_amount::text,''), status,
		       expires_at IS NOT NULL AND expires_at <= clock_timestamp()
		FROM %s WHERE id = $1 FOR UPDATE
	`, s.fullTableName(BillingTopupOrdersTable))
	var userID, amountStr, status string
	var expired bool
	err = tx.QueryRowContext(ctx, lockQuery, orderID).Scan(&userID, &amountStr, &status, &expired)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = ErrTopupOrderNotFound
		return TopupOrder{}, err
	case err != nil:
		err = fmt.Errorf("postgres store: lock topup order: %w", err)
		return TopupOrder{}, err
	}
	if amountStr == "" {
		err = ErrPointsRateMissing
		return TopupOrder{}, err
	}
	if (status != "pending" && status != "submitted") || expired {
		err = ErrTopupOrderNotPending
		return TopupOrder{}, err
	}

	// Credit wallet (upsert handles the zero-wallet case for newly-created
	// users who have not had any prior activity).
	upsertWallet := fmt.Sprintf(`
		INSERT INTO %s (user_id, balance, updated_at, unit)
		VALUES ($1, $2::numeric, NOW(), 'POINTS')
		ON CONFLICT (user_id)
		DO UPDATE SET balance = %s.balance + $2::numeric, updated_at = NOW()
		WHERE %s.unit='POINTS'
		RETURNING balance::text
	`, s.fullTableName(BillingWalletsTable), s.fullTableName(BillingWalletsTable), s.fullTableName(BillingWalletsTable))
	var newBalance string
	if err = tx.QueryRowContext(ctx, upsertWallet, userID, amountStr).Scan(&newBalance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrLegacyBalance
			return TopupOrder{}, err
		}
		err = fmt.Errorf("postgres store: credit wallet: %w", err)
		return TopupOrder{}, err
	}

	// Append ledger entry. The partial unique index on reference enforces
	// idempotency at the database layer even if this method is racing.
	insertTxn := fmt.Sprintf(`
		INSERT INTO %s (user_id, type, amount, balance_after, reference, metadata, unit)
		VALUES ($1, 'topup', $2::numeric, $3::numeric, $4, '{}'::jsonb, 'POINTS')
	`, s.fullTableName(BillingTransactionsTable))
	if _, err = tx.ExecContext(ctx, insertTxn, userID, amountStr, newBalance, orderID); err != nil {
		if isUniqueViolation(err) {
			err = ErrTopupOrderNotPending // already credited, treat as no-op error
			return TopupOrder{}, err
		}
		err = fmt.Errorf("postgres store: insert topup transaction: %w", err)
		return TopupOrder{}, err
	}

	// Mark the order confirmed and return its final state.
	updateOrder := fmt.Sprintf(`
		UPDATE %s
		SET status = 'confirmed', confirmed_at = NOW(),
		    notes = CASE WHEN $2 = '' THEN notes ELSE $2 END
		WHERE id = $1
		RETURNING id, user_id, method, amount::text, currency, network, wallet_address,
		          tx_hash, status, notes, created_at, submitted_at, confirmed_at, expires_at, COALESCE(points_amount::text,''), COALESCE(exchange_rate::text,'')
	`, s.fullTableName(BillingTopupOrdersTable))
	var o TopupOrder
	if err = tx.QueryRowContext(ctx, updateOrder, orderID, strings.TrimSpace(adminNote)).Scan(
		&o.ID, &o.UserID, &o.Method, &o.Amount, &o.Currency, &o.Network, &o.WalletAddress,
		&o.TxHash, &o.Status, &o.Notes, &o.CreatedAt, &o.SubmittedAt, &o.ConfirmedAt, &o.ExpiresAt, &o.PointsAmount, &o.ExchangeRate,
	); err != nil {
		err = fmt.Errorf("postgres store: update topup order: %w", err)
		return TopupOrder{}, err
	}

	if err = tx.Commit(); err != nil {
		return TopupOrder{}, fmt.Errorf("postgres store: commit confirm tx: %w", err)
	}
	return o, nil
}

// ListSubmittedTopupOrders returns top-up orders awaiting on-chain confirmation
// for the given method/network. Results are oldest-first so a polling worker
// processes them in submission order.
func (s *PostgresStore) ListSubmittedTopupOrders(ctx context.Context, method, network string, limit int) ([]TopupOrder, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := fmt.Sprintf(`
		SELECT id, user_id, method, amount::text, currency, network, wallet_address,
		       tx_hash, status, notes, created_at, submitted_at, confirmed_at, expires_at, COALESCE(points_amount::text,''), COALESCE(exchange_rate::text,'')
		FROM %s
		WHERE status = 'submitted' AND method = $1 AND network = $2 AND tx_hash <> ''
		ORDER BY submitted_at ASC NULLS LAST, created_at ASC
		LIMIT $3
	`, s.fullTableName(BillingTopupOrdersTable))

	rows, err := s.db.QueryContext(ctx, query, method, network, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list submitted topup orders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]TopupOrder, 0, limit)
	for rows.Next() {
		var o TopupOrder
		if err := rows.Scan(
			&o.ID, &o.UserID, &o.Method, &o.Amount, &o.Currency, &o.Network, &o.WalletAddress,
			&o.TxHash, &o.Status, &o.Notes, &o.CreatedAt, &o.SubmittedAt, &o.ConfirmedAt, &o.ExpiresAt, &o.PointsAmount, &o.ExchangeRate,
		); err != nil {
			return nil, fmt.Errorf("postgres store: scan submitted topup: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate submitted topups: %w", err)
	}
	return out, nil
}

// CancelTopupOrder marks a non-confirmed order as cancelled.
func (s *PostgresStore) CancelTopupOrder(ctx context.Context, userID, orderID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf(`
		UPDATE %s SET status = 'cancelled'
		WHERE id = $1 AND user_id = $2 AND status IN ('pending', 'submitted')
	`, s.fullTableName(BillingTopupOrdersTable))
	res, err := s.db.ExecContext(ctx, query, orderID, userID)
	if err != nil {
		return fmt.Errorf("postgres store: cancel topup order: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: cancel topup rows: %w", err)
	}
	if n == 0 {
		return ErrTopupOrderNotPending
	}
	return nil
}
