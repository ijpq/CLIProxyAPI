package store

import (
	"context"
	"fmt"
	"strings"
)

// DailyUsageStat aggregates cost and request count by date.
type DailyUsageStat struct {
	Date     string `json:"date"`
	Cost     string `json:"cost"`
	Requests int64  `json:"requests"`
}

// ModelUsageStat aggregates cost and token counts by model.
type ModelUsageStat struct {
	Model        string `json:"model"`
	Cost         string `json:"cost"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// AggregateUsageByDay returns per-day cost/request totals over the last N days.
func (s *PostgresStore) AggregateUsageByDay(ctx context.Context, userID string, days int) ([]DailyUsageStat, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	if days <= 0 || days > 365 {
		days = 30
	}
	args := []any{days}
	userFilter := ""
	if strings.TrimSpace(userID) != "" {
		userFilter = "AND user_id = $2"
		args = append(args, userID)
	}
	query := fmt.Sprintf(`
		SELECT d::date::text AS date,
		       COALESCE(SUM(u.cost),0)::text AS cost,
		       COUNT(u.id) AS requests
		FROM generate_series(NOW() - ($1||' days')::interval, NOW(), '1 day') d
		LEFT JOIN %s u ON u.created_at::date = d::date %s
		GROUP BY d::date
		ORDER BY d::date ASC
	`, s.fullTableName(BillingUsageRecordsTable), userFilter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: aggregate by day: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]DailyUsageStat, 0, days)
	for rows.Next() {
		var st DailyUsageStat
		if err := rows.Scan(&st.Date, &st.Cost, &st.Requests); err != nil {
			return nil, fmt.Errorf("postgres store: scan daily stat: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// AggregateUsageByModel returns per-model cost/request/token totals over the
// last N days.
func (s *PostgresStore) AggregateUsageByModel(ctx context.Context, userID string, days int) ([]ModelUsageStat, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	if days <= 0 || days > 365 {
		days = 30
	}
	args := []any{days}
	userFilter := ""
	if strings.TrimSpace(userID) != "" {
		userFilter = "AND user_id = $2"
		args = append(args, userID)
	}
	query := fmt.Sprintf(`
		SELECT model,
		       SUM(cost)::text AS cost,
		       COUNT(*) AS requests,
		       SUM(input_tokens) AS input_tokens,
		       SUM(output_tokens) AS output_tokens
		FROM %s
		WHERE created_at >= NOW() - ($1||' days')::interval %s
		GROUP BY model
		ORDER BY SUM(cost) DESC
		LIMIT 50
	`, s.fullTableName(BillingUsageRecordsTable), userFilter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: aggregate by model: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]ModelUsageStat, 0, 20)
	for rows.Next() {
		var st ModelUsageStat
		if err := rows.Scan(&st.Model, &st.Cost, &st.Requests, &st.InputTokens, &st.OutputTokens); err != nil {
			return nil, fmt.Errorf("postgres store: scan model stat: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
