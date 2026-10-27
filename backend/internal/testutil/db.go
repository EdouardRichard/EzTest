// Package testutil provides helpers shared by integration tests. It is test
// support code and must never be imported by production packages.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
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
// embedded one. Before connecting, the URL is checked by
// validateTestDatabaseURL: it must name a dedicated `_test`/`_e2e` PostgreSQL
// database and must not be the development database. The pool is closed when
// the test finishes. The test is skipped when the variable is unset.
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
	// Refuse to touch anything that is not clearly a dedicated test database
	// before opening a connection. The helper drops the public schema, so a
	// mis-set variable must never be able to wipe the development database.
	if err := validateTestDatabaseURL(url, strings.TrimSpace(os.Getenv("EZTEST_DATABASE_URL"))); err != nil {
		t.Fatalf("refusing to reset test database: %v", err)
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

// validateTestDatabaseURL guards the destructive test helpers. OpenRawTestDB
// drops and recreates the public schema, so the URL must be a real PostgreSQL
// URL, must name a dedicated test/e2e database and must not resolve to the
// development database. The checks are pure so they can be unit-tested without
// a database and run before any connection or DROP.
//
// Validation uses the effective pgx connection target, not just the URL text:
// pgx applies query parameters on top of the path and host, so
// ...?dbname=eztest, ...?database=eztest, ...?host=..., ...?port=... can point
// the connection at a different database than the path suggests. The same
// pgxpool.ParseConfig parse that pgxpool.New performs is used here, so the
// checked target is the one that would be dialled. Query parameters that do not
// change the target (for example sslmode) remain allowed.
//
// The comparison against the development URL is skipped when devURL is empty
// (for example a plain `go test` invocation that only exports the test URL);
// the scheme, host, database-name and suffix checks still fail closed. When a
// development URL is present it must itself be a valid PostgreSQL URL.
func validateTestDatabaseURL(testURL, devURL string) error {
	testURL = strings.TrimSpace(testURL)
	if testURL == "" {
		return errors.New("EZTEST_TEST_DATABASE_URL is not set")
	}
	test, err := url.Parse(testURL)
	if err != nil {
		// Do not wrap the parse error: it quotes the raw URL, including the
		// password. The message is still actionable without it.
		return errors.New("EZTEST_TEST_DATABASE_URL is not a valid URL")
	}
	if !isPostgresURL(test) {
		return fmt.Errorf("EZTEST_TEST_DATABASE_URL must use the postgres scheme, got %q", test.Scheme)
	}
	if test.Hostname() == "" {
		return errors.New("EZTEST_TEST_DATABASE_URL must include a host")
	}
	testName := databaseName(test)
	if testName == "" {
		return errors.New("EZTEST_TEST_DATABASE_URL must include a database name")
	}
	testConfig, err := pgxpool.ParseConfig(testURL)
	if err != nil {
		// pgx errors quote the connection string, password included; never
		// wrap them. The effective target cannot be verified, so fail closed.
		return errors.New("EZTEST_TEST_DATABASE_URL is not a valid URL for a PostgreSQL connection")
	}
	testTarget := effectiveTarget(testConfig.ConnConfig)
	if testConfig.ConnConfig.Host == "" {
		return errors.New("EZTEST_TEST_DATABASE_URL must include a host")
	}
	if testConfig.ConnConfig.Database == "" {
		return errors.New("EZTEST_TEST_DATABASE_URL must include a database name")
	}
	if !isTestDatabaseName(testConfig.ConnConfig.Database) {
		return fmt.Errorf("EZTEST_TEST_DATABASE_URL database %q must end with _test or _e2e", testConfig.ConnConfig.Database)
	}

	devURL = strings.TrimSpace(devURL)
	if devURL == "" {
		return nil
	}
	dev, err := url.Parse(devURL)
	if err != nil || !isPostgresURL(dev) || dev.Hostname() == "" || databaseName(dev) == "" {
		return errors.New("EZTEST_DATABASE_URL is not a valid PostgreSQL URL; cannot verify that the test database is separate")
	}
	devConfig, err := pgxpool.ParseConfig(devURL)
	if err != nil || devConfig.ConnConfig.Host == "" || devConfig.ConnConfig.Database == "" {
		return errors.New("EZTEST_DATABASE_URL is not a valid PostgreSQL URL; cannot verify that the test database is separate")
	}
	if testTarget == effectiveTarget(devConfig.ConnConfig) {
		return fmt.Errorf("EZTEST_TEST_DATABASE_URL points at the same database as EZTEST_DATABASE_URL (%s); refusing to drop its schema", testTarget)
	}
	return nil
}

// isTestDatabaseName reports whether the effective database name is dedicated
// to tests. A suffix check on the URL path alone is not enough because pgx
// query parameters can replace the database name.
func isTestDatabaseName(name string) bool {
	return strings.HasSuffix(name, "_test") || strings.HasSuffix(name, "_e2e")
}

// isPostgresURL reports whether u uses a scheme the pgx driver accepts.
func isPostgresURL(u *url.URL) bool {
	scheme := strings.ToLower(u.Scheme)
	return scheme == "postgres" || scheme == "postgresql"
}

// databaseName returns the URL's database path without the leading slash.
func databaseName(u *url.URL) string {
	return strings.TrimPrefix(u.Path, "/")
}

// effectiveTarget renders the host, port and database pgx would actually
// connect to. Credentials are deliberately excluded: two URLs can reach the
// same database with different users or TLS modes, and the schema reset must be
// refused in every such case.
func effectiveTarget(cfg *pgx.ConnConfig) string {
	return strings.ToLower(cfg.Host) + ":" + strconv.Itoa(int(cfg.Port)) + "/" + cfg.Database
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
