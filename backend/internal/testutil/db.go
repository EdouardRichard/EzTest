// Package testutil provides isolated PostgreSQL schemas for integration tests.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewDB creates a random schema, applies every migration and returns a pool
// whose connections use that schema. The schema is dropped at test cleanup.
func NewDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := NewEmptyDB(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("testutil: migrate test schema: %v", err)
	}
	return pool
}

// NewEmptyDB creates a migrated-less random schema. Used by migration tests.
func NewEmptyDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("EZTEST_TEST_DATABASE_URL")
	if strings.TrimSpace(databaseURL) == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}
	if !isTestDatabase(databaseURL) {
		t.Fatalf("refusing to use a non-test database: %s", databaseURL)
	}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("testutil: open admin pool: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("testutil: ping test database: %v", err)
	}

	schema := "test_" + randomSuffix()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("testutil: create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("testutil: parse test url: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	cfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatalf("testutil: open schema pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatalf("testutil: ping schema pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

// Truncate empties the named tables. With no tables it truncates every table in
// the current test schema.
func Truncate(t testing.TB, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	ctx := context.Background()
	if len(tables) == 0 {
		rows, err := pool.Query(ctx, `
			SELECT table_name FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
			ORDER BY table_name
		`)
		if err != nil {
			t.Fatalf("testutil: list tables: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				t.Fatalf("testutil: scan table: %v", err)
			}
			tables = append(tables, table)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("testutil: list tables: %v", err)
		}
	}
	for _, table := range tables {
		if _, err := pool.Exec(ctx, "TRUNCATE TABLE "+quoteIdentifier(table)+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("testutil: truncate %s: %v", table, err)
		}
	}
}

func isTestDatabase(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	return strings.HasSuffix(name, "_test") || strings.HasSuffix(name, "_e2e")
}

func randomSuffix() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
