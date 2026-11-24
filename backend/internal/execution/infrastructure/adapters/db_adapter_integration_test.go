//go:build integration

package adapters

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// v0.2.4-C T12 (P8 lift): the database adapter runs general read-write SQL
// (the old READ ONLY transaction is removed), returns more than 200 rows when
// unbounded, and honours a configurable statement timeout.

func targetDSN(t *testing.T) (string, func()) {
	t.Helper()
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()
	var schema string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatalf("current_schema: %v", err)
	}
	base := os.Getenv("EZTEST_TEST_DATABASE_URL")
	dsn := base
	if strings.Contains(base, "?") {
		dsn += "&search_path=" + schema
	} else {
		dsn += "?search_path=" + schema
	}
	return dsn, func() { pool.Close() }
}

func TestDatabaseAdapterWriteSQLAndUnlimitedRows(t *testing.T) {
	dsn, cleanup := targetDSN(t)
	defer cleanup()
	adapter := NewDatabaseAdapter()
	ctx := context.Background()

	if _, err := adapter.Query(ctx, dsn, "", "", "CREATE TABLE eztest_query_sample (id integer, name text)", executionapp.DBQueryOptions{}); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adapter.Query(context.Background(), dsn, "", "", "DROP TABLE IF EXISTS eztest_query_sample", executionapp.DBQueryOptions{})
	})

	// INSERT (write) succeeds — the old READ ONLY transaction rejected it.
	inserted, err := adapter.Query(ctx, dsn, "", "", "INSERT INTO eztest_query_sample (id, name) VALUES (1, 'alice')", executionapp.DBQueryOptions{})
	if err != nil {
		t.Fatalf("INSERT rejected (read-only transaction not lifted?): %v", err)
	}
	if inserted.RowsAffected != 1 {
		t.Fatalf("INSERT RowsAffected = %d, want 1", inserted.RowsAffected)
	}

	// UPDATE (write) succeeds.
	updated, err := adapter.Query(ctx, dsn, "", "", "UPDATE eztest_query_sample SET name = 'bob' WHERE id = 1", executionapp.DBQueryOptions{})
	if err != nil {
		t.Fatalf("UPDATE rejected: %v", err)
	}
	if updated.RowsAffected != 1 {
		t.Fatalf("UPDATE RowsAffected = %d, want 1", updated.RowsAffected)
	}

	// Seed 500 rows and read them back unbounded (> the old 200-row cap).
	if _, err := adapter.Query(ctx, dsn, "", "", "INSERT INTO eztest_query_sample (id, name) SELECT g, 'n' FROM generate_series(2, 501) g", executionapp.DBQueryOptions{}); err != nil {
		t.Fatalf("bulk INSERT: %v", err)
	}
	result, err := adapter.Query(ctx, dsn, "", "", "SELECT id FROM eztest_query_sample ORDER BY id", executionapp.DBQueryOptions{})
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if len(result.Rows) != 501 {
		t.Fatalf("rows = %d, want 501 (the 200-row cap is lifted)", len(result.Rows))
	}
	if result.Truncated {
		t.Fatalf("Truncated = true, want false for an unbounded read")
	}

	// An explicit max_rows still bounds the result.
	bounded, err := adapter.Query(ctx, dsn, "", "", "SELECT id FROM eztest_query_sample ORDER BY id", executionapp.DBQueryOptions{MaxRows: 10})
	if err != nil {
		t.Fatalf("bounded SELECT: %v", err)
	}
	if len(bounded.Rows) != 10 || !bounded.Truncated {
		t.Fatalf("bounded rows = %d truncated = %v, want 10 / true", len(bounded.Rows), bounded.Truncated)
	}
}

func TestDatabaseAdapterStatementTimeoutConfigurable(t *testing.T) {
	dsn, cleanup := targetDSN(t)
	defer cleanup()
	adapter := NewDatabaseAdapter()
	ctx := context.Background()
	// A 1ms statement timeout against a 200ms sleep must fail (configurable,
	// default 5s when omitted).
	_, err := adapter.Query(ctx, dsn, "", "", "SELECT pg_sleep(0.2)", executionapp.DBQueryOptions{StatementTimeoutMS: 1})
	if err == nil {
		t.Fatalf("expected a statement-timeout error for a 1ms budget against pg_sleep(0.2)")
	}
}

// TestDatabaseAdapterStatementTimeoutAbove10sHonored (T12 fix round 1): the
// advertised statement_timeout_ms range is 1..900000, so a configured timeout
// ABOVE the old 10s outer-context floor must genuinely govern. An 11s statement
// under a 12s statement timeout must SUCCEED — the outer context must not
// pre-empt it at 10s. RED before the derived-deadline fix (10s ctx kills it).
func TestDatabaseAdapterStatementTimeoutAbove10sHonored(t *testing.T) {
	dsn, cleanup := targetDSN(t)
	defer cleanup()
	adapter := NewDatabaseAdapter()
	ctx := context.Background()
	result, err := adapter.Query(ctx, dsn, "", "", "SELECT pg_sleep(11)", executionapp.DBQueryOptions{StatementTimeoutMS: 12000})
	if err != nil {
		t.Fatalf("an 11s statement under a 12s statement_timeout must succeed (the 10s outer ctx must not pre-empt it): %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("pg_sleep result rows = %d, want 1", result.RowCount)
	}
}

// TestDatabaseAdapterStatementTimeoutAbove10sFires (T12 fix round 1): a configured
// statement timeout above 10s must be the limit that fires (not the old 10s ctx).
// An 11s statement timeout against a 20s sleep must error AFTER ~11s (>10s),
// proving the configured timeout governs.
func TestDatabaseAdapterStatementTimeoutAbove10sFires(t *testing.T) {
	dsn, cleanup := targetDSN(t)
	defer cleanup()
	adapter := NewDatabaseAdapter()
	ctx := context.Background()
	started := time.Now()
	_, err := adapter.Query(ctx, dsn, "", "", "SELECT pg_sleep(20)", executionapp.DBQueryOptions{StatementTimeoutMS: 11000})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatalf("a 20s sleep under an 11s statement_timeout must fail")
	}
	// The configured 11s statement timeout must be the limit that fires (~11s),
	// not the old 10s outer ctx (~10.1s). 10.5s discriminates the two.
	if elapsed <= 10500*time.Millisecond {
		t.Fatalf("statement failed after %v (<=10.5s): the old 10s outer ctx fired, not the configured 11s statement_timeout", elapsed)
	}
}
