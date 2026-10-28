//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestP5RenameResourceTables asserts that the resource_0004 migration renamed
// all 11 resource tables and their serial sequences to the singular names from
// spec §3.2, without leaving the old names behind.
func TestP5RenameResourceTables(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	const (
		kindTable    = "r"
		kindSequence = "S"
	)
	renames := []struct{ old, new string }{
		{"companies", "company"},
		{"systems", "system"},
		{"environments", "environment"},
		{"app_model_nodes", "app_model_node"},
		{"accounts", "account"},
		{"account_system_accesses", "account_system_access"},
		{"sessions", "session"},
		{"session_snapshots", "session_snapshot"},
		{"session_leases", "session_lease"},
		{"manual_takeovers", "manual_takeover"},
		{"code_repositories", "code_repository"},
	}
	for _, rename := range renames {
		assertRelation(t, ctx, pool, rename.new, kindTable, true)
		assertRelation(t, ctx, pool, rename.old, kindTable, false)
		assertRelation(t, ctx, pool, rename.new+"_id_seq", kindSequence, true)
		assertRelation(t, ctx, pool, rename.old+"_id_seq", kindSequence, false)
	}
}

// assertRelation checks whether a public-schema relation with the given pg_class
// relkind ("r" for an ordinary table, "S" for a sequence) exists, and reports a
// test error when its presence differs from want.
func assertRelation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, kind string, want bool) {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'public' AND c.relname = $1 AND c.relkind = $2
		)`, name, kind).Scan(&exists)
	if err != nil {
		t.Fatalf("look up relation %q: %v", name, err)
	}
	if exists == want {
		return
	}
	if want {
		t.Errorf("relation %q (relkind %q) is missing after the rename migration", name, kind)
		return
	}
	t.Errorf("relation %q (relkind %q) still exists after the rename migration", name, kind)
}
