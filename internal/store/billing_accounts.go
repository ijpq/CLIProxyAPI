package store

import (
	"context"
	"fmt"
	"strings"
)

// SetAccountAlias sets (or clears, when label is empty) the customer-facing
// display name for an upstream account identified by authID.
func (s *PostgresStore) SetAccountAlias(ctx context.Context, authID, label string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return fmt.Errorf("postgres store: auth id required")
	}
	label = strings.TrimSpace(label)
	table := s.fullTableName(BillingAccountAliasesTable)
	if label == "" {
		_, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE auth_id = $1", table), authID)
		if err != nil {
			return fmt.Errorf("postgres store: clear account alias: %w", err)
		}
		return nil
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (auth_id, label, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (auth_id) DO UPDATE SET label = EXCLUDED.label, updated_at = NOW()
	`, table)
	if _, err := s.db.ExecContext(ctx, query, authID, label); err != nil {
		return fmt.Errorf("postgres store: set account alias: %w", err)
	}
	return nil
}

// AccountAliases returns all configured account display names keyed by auth id.
func (s *PostgresStore) AccountAliases(ctx context.Context) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT auth_id, label FROM %s", s.fullTableName(BillingAccountAliasesTable)))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list account aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, fmt.Errorf("postgres store: scan account alias: %w", err)
		}
		out[id] = label
	}
	return out, rows.Err()
}
