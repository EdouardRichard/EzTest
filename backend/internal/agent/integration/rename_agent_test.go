//go:build integration

// Package integration_test holds cross-cutting integration tests for the agent
// context. It is an external test package so the tests can only use exported
// repository APIs, mirroring the other context integration packages.
package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestP5RenameAgentTables pins the D34 rename of the agent context: every
// plural table has its singular replacement, the old name is gone, the
// BIGSERIAL sequence follows the table name, and the existing index names are
// deliberately left untouched (D34: fields/constraints/index names unchanged).
func TestP5RenameAgentTables(t *testing.T) {
	pool := testutil.OpenTestDB(t) // applies the embedded migrations, then truncates
	ctx := context.Background()

	pairs := [][2]string{
		{"agent_profile", "agent_profiles"},
		{"agent_session", "agent_sessions"},
		{"agent_run", "agent_runs"},
		{"agent_message", "agent_messages"},
	}
	for _, p := range pairs {
		newTable, oldTable := p[0], p[1]
		if got := countRelations(t, ctx, pool, "r", newTable); got != 1 {
			t.Errorf("new table %s exists %d times, want 1", newTable, got)
		}
		if got := countRelations(t, ctx, pool, "r", oldTable); got != 0 {
			t.Errorf("old table %s still exists", oldTable)
		}

		newSequence, oldSequence := newTable+"_id_seq", oldTable+"_id_seq"
		if got := countRelations(t, ctx, pool, "S", newSequence); got != 1 {
			t.Errorf("new sequence %s exists %d times, want 1", newSequence, got)
		}
		if got := countRelations(t, ctx, pool, "S", oldSequence); got != 0 {
			t.Errorf("old sequence %s still exists", oldSequence)
		}

		// The id column must still be backed by the renamed sequence.
		var serial string
		if err := pool.QueryRow(ctx, `SELECT pg_get_serial_sequence($1, 'id')`, newTable).Scan(&serial); err != nil {
			t.Fatalf("look up %s.id sequence: %v", newTable, err)
		}
		if !strings.HasSuffix(serial, "."+newSequence) {
			t.Errorf("%s.id uses sequence %q, want %q", newTable, serial, newSequence)
		}
	}

	// D34 only renames the tables. Every existing index keeps its name.
	for _, index := range []string{
		"idx_agent_sessions_created",
		"uq_agent_runs_running",
		"idx_agent_runs_session",
		"idx_agent_messages_session",
		"idx_agent_messages_run",
		"idx_agent_sessions_activity_channel",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1
			)`, index).Scan(&exists); err != nil {
			t.Fatalf("check index %s: %v", index, err)
		}
		if !exists {
			t.Errorf("index %s disappeared; D34 keeps existing index names", index)
		}
	}
}

// countRelations counts entries of the given pg_class kind in the public
// schema: "r" for ordinary tables and "S" for sequences.
func countRelations(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind, name string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = $1 AND c.relname = $2`,
		kind, name).Scan(&n); err != nil {
		t.Fatalf("count %s relation %s: %v", kind, name, err)
	}
	return n
}
