// Package testutil provides helpers shared by integration tests. It is test
// support code and must never be imported by production packages.
package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenTestDB opens the database named by EZTEST_TEST_DATABASE_URL on an empty
// public schema, applies the embedded migrations and truncates every public
// table except schema_migrations. The pool is closed, and the tables are
// emptied again, when the test finishes. The test is skipped when the variable
// is unset.
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

	if _, err := database.Pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset test database schema: %v", err)
	}
	return database.Pool
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
