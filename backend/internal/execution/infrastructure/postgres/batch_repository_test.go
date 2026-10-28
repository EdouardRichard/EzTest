package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// gateCountQuerier captures the autonomy-gate SELECT and returns a fixed count.
type gateCountQuerier struct {
	sql   string
	count int64
}

// Exec implements querier; the gate only reads.
func (q *gateCountQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec in autonomy gate test")
}

// Query implements querier; the gate only reads one row.
func (q *gateCountQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query in autonomy gate test")
}

// QueryRow records the gate SQL and answers the count.
func (q *gateCountQuerier) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	q.sql = sql
	return gateCountRow{count: q.count}
}

// gateCountRow implements pgx.Row for one count column.
type gateCountRow struct{ count int64 }

// Scan implements pgx.Row.
func (r gateCountRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("autonomy gate row: want one destination")
	}
	target, ok := dest[0].(*int)
	if !ok {
		return errors.New("autonomy gate row: destination is not *int")
	}
	*target = int(r.count)
	return nil
}

// TestCountTerminalBatchesAwaitingAutonomy pins the creation gate query (spec
// §5.3): terminal batches of the version whose autonomy chain is not done.
func TestCountTerminalBatchesAwaitingAutonomy(t *testing.T) {
	q := &gateCountQuerier{count: 2}
	repo := &BatchRepository{q: q}

	got, err := repo.CountTerminalBatchesAwaitingAutonomy(context.Background(), 7)
	if err != nil {
		t.Fatalf("CountTerminalBatchesAwaitingAutonomy: %v", err)
	}
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	for _, needle := range []string{
		"FROM run_batch",
		"version_id = $1",
		"status IN ('done', 'failed', 'cancelled')",
		"autonomy_done = false",
	} {
		if !strings.Contains(q.sql, needle) {
			t.Fatalf("gate SQL %q does not contain %q", q.sql, needle)
		}
	}
}
