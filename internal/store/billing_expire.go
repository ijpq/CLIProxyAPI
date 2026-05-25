package store

import (
	"context"
	"fmt"
)

// ExpirePendingTopupOrders cancels all orders past their expires_at that are
// still pending or submitted. Returns the number of rows affected.
func (s *PostgresStore) ExpirePendingTopupOrders(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: not initialized")
	}
	query := fmt.Sprintf(`
		UPDATE %s SET status = 'expired'
		WHERE status IN ('pending','submitted')
		  AND expires_at IS NOT NULL
		  AND expires_at < NOW()
	`, s.fullTableName(BillingTopupOrdersTable))
	res, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("postgres store: expire topup orders: %w", err)
	}
	return res.RowsAffected()
}
