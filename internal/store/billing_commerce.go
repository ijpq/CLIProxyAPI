package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrBillingConflict = errors.New("billing configuration was changed; reload and retry")
var ErrPointsRateMissing = errors.New("points exchange rate is not configured")
var ErrLegacyBalance = errors.New("legacy monetary balance requires reconciliation before points billing")

// PortalSettings contains only publishable site and business configuration.
// Credentials and upstream account identifiers must never be stored here.
type PortalSettings struct {
	Version                int64  `json:"version"`
	SiteName               string `json:"site_name"`
	PublicAPIBase          string `json:"public_api_base"`
	DocsURL                string `json:"docs_url"`
	CNYPointsPerUnit       string `json:"cny_points_per_unit"`
	USDTPointsPerUnit      string `json:"usdt_points_per_unit"`
	HelperEnabled          bool   `json:"helper_enabled"`
	HelperModel            string `json:"helper_model"`
	HelperDailyLimit       int    `json:"helper_daily_limit"`
	HelperGlobalDailyLimit int    `json:"helper_global_daily_limit"`
	HelperMaxOutput        int    `json:"helper_max_output_tokens"`
}

type PointPrice struct {
	Model           string           `json:"model"`
	Input           string           `json:"input_per_million"`
	Output          string           `json:"output_per_million"`
	CacheRead       string           `json:"cache_read_per_million"`
	CacheWrite      string           `json:"cache_write_per_million"`
	Enabled         bool             `json:"enabled"`
	Version         int64            `json:"version"`
	UpdatedAt       time.Time        `json:"updated_at"`
	DiscountPercent int              `json:"discount_percent"`
	StandardPrice   *PointPriceRates `json:"standard_price,omitempty"`
}

func commerceSchemaStatements(s *PostgresStore) []string {
	return []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY CHECK (id=1), version BIGINT NOT NULL DEFAULT 1, document JSONB NOT NULL)`, s.fullTableName("billing_portal_settings")),
		fmt.Sprintf(`INSERT INTO %s(id, document) VALUES (1, '{"site_name":"TrueCore AI","helper_daily_limit":20,"helper_global_daily_limit":500,"helper_max_output_tokens":768}'::jsonb) ON CONFLICT DO NOTHING`, s.fullTableName("billing_portal_settings")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id BIGSERIAL PRIMARY KEY, actor TEXT NOT NULL, kind TEXT NOT NULL, document JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`, s.fullTableName("billing_config_history")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (model TEXT PRIMARY KEY, input NUMERIC(20,6) NOT NULL CHECK(input>=0), output NUMERIC(20,6) NOT NULL CHECK(output>=0), cache_read NUMERIC(20,6) NOT NULL CHECK(cache_read>=0), cache_write NUMERIC(20,6) NOT NULL CHECK(cache_write>=0), enabled BOOLEAN NOT NULL, version BIGINT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`, s.fullTableName("billing_point_prices")),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS discount_percent SMALLINT NOT NULL DEFAULT 0 CHECK(discount_percent BETWEEN 0 AND 99), ADD COLUMN IF NOT EXISTS standard_price JSONB`, s.fullTableName("billing_point_prices")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (user_id UUID NOT NULL REFERENCES %s(id) ON DELETE CASCADE, day DATE NOT NULL, used INTEGER NOT NULL CHECK(used>0), PRIMARY KEY(user_id,day))`, s.fullTableName("billing_helper_usage"), s.fullTableName(BillingUsersTable)),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (day DATE PRIMARY KEY, used INTEGER NOT NULL CHECK(used>=0))`, s.fullTableName("billing_helper_global_usage")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (event_id TEXT PRIMARY KEY, digest TEXT NOT NULL)`, s.fullTableName("billing_settled_events")),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS points_amount NUMERIC(20,6), ADD COLUMN IF NOT EXISTS exchange_rate NUMERIC(20,6)`, s.fullTableName(BillingTopupOrdersTable)),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS unit TEXT NOT NULL DEFAULT 'LEGACY'`, s.fullTableName(BillingWalletsTable)),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS price_version BIGINT NOT NULL DEFAULT 0`, s.fullTableName(BillingUsageRecordsTable)),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS unit TEXT NOT NULL DEFAULT 'LEGACY'`, s.fullTableName(BillingUsageRecordsTable)),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS unit TEXT NOT NULL DEFAULT 'LEGACY'`, s.fullTableName(BillingTransactionsTable)),
		fmt.Sprintf(`UPDATE %s w SET unit='POINTS' WHERE unit='LEGACY' AND balance=0 AND NOT EXISTS (SELECT 1 FROM %s t WHERE t.user_id=w.user_id) AND NOT EXISTS (SELECT 1 FROM %s u WHERE u.user_id=w.user_id)`, s.fullTableName(BillingWalletsTable), s.fullTableName(BillingTransactionsTable), s.fullTableName(BillingUsageRecordsTable)),
	}
}

func (s *PostgresStore) GetPortalSettings(ctx context.Context) (PortalSettings, error) {
	var cfg PortalSettings
	var data []byte
	var version int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT version,document FROM %s WHERE id=1`, s.fullTableName("billing_portal_settings"))).Scan(&version, &data)
	if err != nil {
		return cfg, err
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	cfg.Version = version
	if cfg.HelperGlobalDailyLimit == 0 {
		cfg.HelperGlobalDailyLimit = 500
	}
	return cfg, nil
}

func (s *PostgresStore) SavePortalSettings(ctx context.Context, cfg PortalSettings, actor string) (PortalSettings, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cfg, err
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`UPDATE %s SET document=$1::jsonb,version=version+1 WHERE id=1 AND version=$2 RETURNING version`, s.fullTableName("billing_portal_settings")), data, cfg.Version).Scan(&cfg.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, ErrBillingConflict
	}
	if err != nil {
		return cfg, err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s(actor,kind,document) VALUES ($1,'settings',$2::jsonb)`, s.fullTableName("billing_config_history")), actor, data); err != nil {
		return cfg, err
	}
	return cfg, tx.Commit()
}

func (s *PostgresStore) ListPointPrices(ctx context.Context) ([]PointPrice, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT model,input::text,output::text,cache_read::text,cache_write::text,enabled,version,updated_at,discount_percent,standard_price FROM %s ORDER BY model`, s.fullTableName("billing_point_prices")))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]PointPrice, 0)
	for rows.Next() {
		var p PointPrice
		var standard []byte
		if err := rows.Scan(&p.Model, &p.Input, &p.Output, &p.CacheRead, &p.CacheWrite, &p.Enabled, &p.Version, &p.UpdatedAt, &p.DiscountPercent, &standard); err != nil {
			return nil, err
		}
		if len(standard) > 0 {
			if err := json.Unmarshal(standard, &p.StandardPrice); err != nil {
				return nil, err
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SavePointPrice(ctx context.Context, p PointPrice, actor string) (PointPrice, error) {
	p.Model = strings.ToLower(strings.TrimSpace(p.Model))
	if p.Model == "" {
		return p, fmt.Errorf("model is required")
	}
	var err error
	p, err = NormalizePointPrice(p)
	if err != nil {
		return p, err
	}
	standard, _ := json.Marshal(p.StandardPrice)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback() }()
	query := fmt.Sprintf(`INSERT INTO %s(model,input,output,cache_read,cache_write,enabled,discount_percent,standard_price) VALUES ($1,$2::numeric,$3::numeric,$4::numeric,$5::numeric,$6,$8,$9::jsonb)
		ON CONFLICT(model) DO UPDATE SET input=EXCLUDED.input,output=EXCLUDED.output,cache_read=EXCLUDED.cache_read,cache_write=EXCLUDED.cache_write,enabled=EXCLUDED.enabled,discount_percent=EXCLUDED.discount_percent,standard_price=EXCLUDED.standard_price,version=%s.version+1,updated_at=NOW() WHERE %s.version=$7
		RETURNING version,updated_at`, s.fullTableName("billing_point_prices"), s.fullTableName("billing_point_prices"), s.fullTableName("billing_point_prices"))
	err = tx.QueryRowContext(ctx, query, p.Model, p.Input, p.Output, p.CacheRead, p.CacheWrite, p.Enabled, p.Version, p.DiscountPercent, standard).Scan(&p.Version, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrBillingConflict
	}
	if err != nil {
		return p, err
	}
	data, _ := json.Marshal(p)
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s(actor,kind,document) VALUES($1,'price',$2::jsonb)`, s.fullTableName("billing_config_history")), actor, data); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// ConsumeHelperAllowance counts attempts, including provider failures, in UTC.
func (s *PostgresStore) ConsumeHelperAllowance(ctx context.Context, userID string, limit, globalLimit int) (bool, error) {
	if limit < 1 || globalLimit < 1 {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	global := s.fullTableName("billing_helper_global_usage")
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s(day,used) VALUES((NOW() AT TIME ZONE 'UTC')::date,0) ON CONFLICT DO NOTHING`, global)); err != nil {
		return false, err
	}
	var globalUsed int
	if err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT used FROM %s WHERE day=(NOW() AT TIME ZONE 'UTC')::date FOR UPDATE`, global)).Scan(&globalUsed); err != nil {
		return false, err
	}
	if globalUsed >= globalLimit {
		return false, nil
	}
	query := fmt.Sprintf(`INSERT INTO %s(user_id,day,used) VALUES($1,(NOW() AT TIME ZONE 'UTC')::date,1)
		ON CONFLICT(user_id,day) DO UPDATE SET used=%s.used+1 WHERE %s.used<$2 RETURNING used`, s.fullTableName("billing_helper_usage"), s.fullTableName("billing_helper_usage"), s.fullTableName("billing_helper_usage"))
	var used int
	err = tx.QueryRowContext(ctx, query, userID, limit).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET used=used+1 WHERE day=(NOW() AT TIME ZONE 'UTC')::date`, global)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
