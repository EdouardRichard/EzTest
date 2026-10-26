// Package testutil provides helpers shared by integration tests. It is test
// support code and must never be imported by production packages.
package testutil

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// testDBLockID serialises the shared test database across test binaries.
	// Go runs different packages' test binaries in parallel by default, so
	// without a cross-process lock one package's schema reset or truncate could
	// destroy another package's tables mid-test. It is deliberately distinct
	// from db.Migrate's startup lock (4242).
	testDBLockID = 4243

	lockTestDBSQL   = "SELECT pg_advisory_lock($1)"
	unlockTestDBSQL = "SELECT pg_advisory_unlock($1)"
)

// OpenTestDB opens the database named by EZTEST_TEST_DATABASE_URL on an empty
// public schema, applies the embedded migrations and truncates every public
// table except schema_migrations. The pool is closed, and the tables are
// emptied again, when the test finishes. The test is skipped when the variable
// is unset. Like OpenRawTestDB it holds advisory lock 4243 for the whole test,
// so tests using it must not call t.Parallel().
func OpenTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := OpenRawTestDB(t)

	ctx := context.Background()
	if err := db.MigrateEmbedded(ctx, pool); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	// Clean leftovers from a previous run before handing the pool to the test.
	truncateAll(t, ctx, pool)
	t.Cleanup(func() {
		truncateAll(t, ctx, pool)
	})
	return pool
}

// OpenRawTestDB opens the database named by EZTEST_TEST_DATABASE_URL with an
// empty public schema: it drops and recreates the schema (the URL points at
// the test database) and does not apply any migrations. Tests that supply
// their own migration set start from a clean ledger instead of inheriting the
// embedded one. The pool is closed when the test finishes. The test is skipped
// when the variable is unset.
//
// The helper takes advisory lock 4243 for the lifetime of the test so that
// parallel test binaries in other packages cannot reset or truncate the shared
// database while this test runs. Because the lock is held for the whole test,
// a test must not call this helper (or OpenTestDB) a second time while the
// first pool is still in use, and tests must not call t.Parallel(). Cleanup
// order is pool close registered first, then unlock/release, so the lock is
// always released before the pool closes and even a failing test unlocks.
func OpenRawTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	database, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)

	// Take the cross-package lock before touching the schema.
	lockTestDB(t, ctx, database.Pool)

	if _, err := database.Pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset test database schema: %v", err)
	}
	return database.Pool
}

// lockTestDB acquires advisory lock testDBLockID on a dedicated pooled
// connection and registers a cleanup that unlocks it and returns the
// connection. Session-level advisory locks belong to a single session, so the
// connection stays acquired for the whole test rather than only while the reset
// runs. Cleanups run LIFO: callers register their pool close first, then this
// lock cleanup, then any later work (truncateAll); therefore later work runs
// while the lock is still held and the lock is released before the pool closes.
func lockTestDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire test database lock connection: %v", err)
	}
	if _, err := conn.Exec(ctx, lockTestDBSQL, testDBLockID); err != nil {
		conn.Release()
		t.Fatalf("lock test database (advisory lock %d): %v", testDBLockID, err)
	}
	t.Cleanup(func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, unlockTestDBSQL, testDBLockID); err != nil {
			// Closing the connection releases the session lock server-side and
			// keeps a still-locked session out of the pool.
			_ = conn.Conn().Close(unlockCtx)
			conn.Release()
			return
		}
		conn.Release()
	})
}

// truncateAll empties every table in the public schema except the migration
// ledger, resetting owned sequences so tests get deterministic IDs.
func truncateAll(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
		ORDER BY tablename`)
	if err != nil {
		t.Fatalf("list test tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan test table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("list test tables: %v", err)
	}
	rows.Close()
	if len(tables) == 0 {
		return
	}

	names := make([]string, len(tables))
	for i, name := range tables {
		names[i] = pgx.Identifier{name}.Sanitize()
	}
	statement := "TRUNCATE TABLE " + strings.Join(names, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatalf("truncate test tables: %v", err)
	}
}
