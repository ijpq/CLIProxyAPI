package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

type billingStatsTestConnector struct {
	t *testing.T
}

func (c *billingStatsTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &billingStatsTestConn{t: c.t}, nil
}

func (c *billingStatsTestConnector) Driver() driver.Driver {
	return billingStatsTestDriver{}
}

type billingStatsTestDriver struct{}

func (billingStatsTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("billing stats test driver must use a connector")
}

type billingStatsTestConn struct {
	t *testing.T
}

func (c *billingStatsTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *billingStatsTestConn) Close() error { return nil }

func (c *billingStatsTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *billingStatsTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.t.Helper()
	if !strings.Contains(query, "make_interval(days => $1)") {
		c.t.Fatalf("query does not use a typed interval parameter: %s", query)
	}
	if strings.Contains(query, "$1||' days'") {
		c.t.Fatalf("query still coerces the integer parameter to text: %s", query)
	}
	if len(args) != 2 || args[0].Value != int64(30) || args[1].Value != "user-1" {
		c.t.Fatalf("query args = %#v, want [30 user-1]", args)
	}
	if strings.Contains(query, "generate_series") {
		return &billingStatsTestRows{
			columns: []string{"date", "cost", "requests"},
			values:  [][]driver.Value{{"2026-10-13", "1.250000", int64(2)}},
		}, nil
	}
	return &billingStatsTestRows{
		columns: []string{"model", "cost", "requests", "input_tokens", "output_tokens"},
		values:  [][]driver.Value{{"model-1", "2.500000", int64(3), int64(40), int64(20)}},
	}, nil
}

type billingStatsTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *billingStatsTestRows) Columns() []string { return r.columns }

func (r *billingStatsTestRows) Close() error { return nil }

func (r *billingStatsTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func TestAggregateUsageStatsUseTypedIntervalParameter(t *testing.T) {
	db := sql.OpenDB(&billingStatsTestConnector{t: t})
	t.Cleanup(func() { _ = db.Close() })
	store := &PostgresStore{db: db}

	daily, errDaily := store.AggregateUsageByDay(context.Background(), "user-1", 30)
	if errDaily != nil {
		t.Fatalf("AggregateUsageByDay() error = %v", errDaily)
	}
	if len(daily) != 1 || daily[0].Date != "2026-10-13" || daily[0].Requests != 2 {
		t.Fatalf("AggregateUsageByDay() = %#v", daily)
	}

	models, errModels := store.AggregateUsageByModel(context.Background(), "user-1", 30)
	if errModels != nil {
		t.Fatalf("AggregateUsageByModel() error = %v", errModels)
	}
	if len(models) != 1 || models[0].Model != "model-1" || models[0].InputTokens != 40 || models[0].OutputTokens != 20 {
		t.Fatalf("AggregateUsageByModel() = %#v", models)
	}
}
