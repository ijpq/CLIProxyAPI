package store

import (
	"context"
	"fmt"
	"strings"
)

// Billing-related table names. Kept as constants so callers can reference them
// without re-typing identifiers, but they are not configurable yet because the
// surrounding code (billing module) is built against these names.
const (
	BillingUsersTable        = "users"
	BillingAPIKeysTable      = "api_keys"
	BillingWalletsTable      = "wallets"
	BillingTransactionsTable = "transactions"
	BillingUsageRecordsTable = "usage_records"
	BillingTopupOrdersTable  = "topup_orders"
)

// EnsureBillingSchema creates the tables required for the paid-tier features:
// end-user accounts, per-user API keys, wallet balances, transaction ledger,
// and per-request usage records. All statements are idempotent so the method
// is safe to call on every startup.
//
// This is intentionally separate from EnsureSchema so deployments that do not
// enable the billing module pay no schema cost.
func (s *PostgresStore) EnsureBillingSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}

	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		query := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("postgres store: create schema: %w", err)
		}
	}

	for _, stmt := range billingSchemaStatements(s) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("postgres store: ensure billing schema: %w", err)
		}
	}
	return nil
}

// billingSchemaStatements returns the ordered DDL statements that build the
// billing schema. Ordering matters because of foreign keys.
func billingSchemaStatements(s *PostgresStore) []string {
	users := s.fullTableName(BillingUsersTable)
	apiKeys := s.fullTableName(BillingAPIKeysTable)
	wallets := s.fullTableName(BillingWalletsTable)
	transactions := s.fullTableName(BillingTransactionsTable)
	usageRecords := s.fullTableName(BillingUsageRecordsTable)

	return []string{
		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				email           TEXT NOT NULL UNIQUE,
				password_hash   TEXT NOT NULL,
				display_name    TEXT NOT NULL DEFAULT '',
				status          TEXT NOT NULL DEFAULT 'active',
				is_admin        BOOLEAN NOT NULL DEFAULT FALSE,
				created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)
		`, users),

		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				user_id         UUID PRIMARY KEY REFERENCES %s(id) ON DELETE CASCADE,
				balance         NUMERIC(20,6) NOT NULL DEFAULT 0,
				updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)
		`, wallets, users),

		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id         UUID NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
				key_hash        TEXT NOT NULL UNIQUE,
				key_prefix      TEXT NOT NULL,
				name            TEXT NOT NULL DEFAULT '',
				last_used_at    TIMESTAMPTZ,
				revoked_at      TIMESTAMPTZ,
				created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)
		`, apiKeys, users),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON %s(user_id)`, apiKeys),

		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id         UUID NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
				type            TEXT NOT NULL,
				amount          NUMERIC(20,6) NOT NULL,
				balance_after   NUMERIC(20,6) NOT NULL,
				reference       TEXT NOT NULL DEFAULT '',
				metadata        JSONB NOT NULL DEFAULT '{}'::JSONB,
				created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)
		`, transactions, users),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_transactions_user_created ON %s(user_id, created_at DESC)`, transactions),
		fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_topup_ref ON %s(reference) WHERE type = 'topup' AND reference <> ''`, transactions),

		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id             UUID NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
				api_key_id          UUID REFERENCES %s(id) ON DELETE SET NULL,
				request_id          TEXT NOT NULL DEFAULT '',
				provider            TEXT NOT NULL DEFAULT '',
				model               TEXT NOT NULL DEFAULT '',
				input_tokens        BIGINT NOT NULL DEFAULT 0,
				output_tokens       BIGINT NOT NULL DEFAULT 0,
				cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
				cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
				cost                NUMERIC(20,6) NOT NULL DEFAULT 0,
				status              TEXT NOT NULL DEFAULT 'success',
				error_message       TEXT NOT NULL DEFAULT '',
				created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)
		`, usageRecords, users, apiKeys),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_usage_user_created ON %s(user_id, created_at DESC)`, usageRecords),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_usage_key_created ON %s(api_key_id, created_at DESC)`, usageRecords),

		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id         UUID NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
				method          TEXT NOT NULL DEFAULT 'usdt',
				amount          NUMERIC(20,6) NOT NULL,
				currency        TEXT NOT NULL DEFAULT 'USDT',
				network         TEXT NOT NULL DEFAULT '',
				wallet_address  TEXT NOT NULL DEFAULT '',
				tx_hash         TEXT NOT NULL DEFAULT '',
				status          TEXT NOT NULL DEFAULT 'pending',
				notes           TEXT NOT NULL DEFAULT '',
				created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				submitted_at    TIMESTAMPTZ,
				confirmed_at    TIMESTAMPTZ,
				expires_at      TIMESTAMPTZ
			)
		`, s.fullTableName(BillingTopupOrdersTable), users),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_topup_user_created ON %s(user_id, created_at DESC)`, s.fullTableName(BillingTopupOrdersTable)),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_topup_status ON %s(status)`, s.fullTableName(BillingTopupOrdersTable)),
		fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS idx_topup_tx_hash ON %s(tx_hash) WHERE tx_hash <> ''`, s.fullTableName(BillingTopupOrdersTable)),
		// Active orders on the same method must have unique amounts so the
		// operator can attribute incoming payments to a specific order when
		// the channel (WeChat / Alipay personal QR) provides no other
		// reference data.
		fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS idx_topup_active_method_amount ON %s(method, amount) WHERE status IN ('pending','submitted')`, s.fullTableName(BillingTopupOrdersTable)),
	}
}
