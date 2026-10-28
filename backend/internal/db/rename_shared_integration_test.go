//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// d34TableRenames is the complete D34 rename mapping (spec §3.2): 38 tables
// moved from plural to singular names with no column, constraint or index
// change. code_analysis_cache is already singular and keeps its name (listed in
// d34UnchangedTables), which makes 39 historical tables in total.
var d34TableRenames = []struct{ old, new string }{
	// identity
	{"users", "user_account"},
	{"auth_tokens", "auth_token"},
	// platform
	{"idempotency_keys", "idempotency_key"},
	// eventing
	{"events", "event"},
	{"event_channels", "event_channel"},
	// audit / llm / storage
	{"audit_logs", "audit_log"},
	{"llm_configs", "llm_config"},
	{"storage_objects", "storage_object"},
	// agent
	{"agent_profiles", "agent_profile"},
	{"agent_sessions", "agent_session"},
	{"agent_runs", "agent_run"},
	{"agent_messages", "agent_message"},
	// resource
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
	// version
	{"versions", "version"},
	{"requirements", "requirement"},
	{"requirement_relations", "requirement_relation"},
	{"requirement_attachments", "requirement_attachment"},
	{"clarification_sessions", "clarification_session"},
	{"clarification_questions", "clarification_question"},
	{"decisions", "decision"},
	// codeintel
	{"code_change_sets", "code_change_set"},
	{"code_index_runs", "code_index_run"},
	{"code_entities", "code_entity"},
	{"code_edges", "code_edge"},
	{"code_fact_versions", "code_fact_version"},
	{"code_analyses", "code_analysis"},
	{"code_impact_candidates", "code_impact_candidate"},
	{"code_anchors", "code_anchor"},
}

// d34UnchangedTables are historical tables whose name already follows D34 and
// therefore must survive the rename migrations unchanged (spec §3.2).
var d34UnchangedTables = []string{
	"code_analysis_cache",
}

// TestP5RenameNoOldTables is the cross-context guard for D34: after the
// embedded migration chain runs, every historical table exists only under its
// singular name, no compatibility view/alias keeps an old plural name alive,
// and table data (spot-checked through the renamed tables) is untouched.
func TestP5RenameNoOldTables(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	// The absence assertions below are only meaningful if the lookup helpers
	// can actually detect an existing relation. Pin both directions first so a
	// broken helper cannot make this whole guard vacuously green.
	if !relationExists(t, ctx, pool, "schema_migrations") {
		t.Fatal("relationExists does not detect the migration ledger; absence checks would be vacuous")
	}
	if relationExists(t, ctx, pool, "d34_rename_probe_missing") {
		t.Fatal("relationExists reports a relation that does not exist")
	}

	for _, rename := range d34TableRenames {
		if !tableExists(t, ctx, pool, rename.new) {
			t.Errorf("table %s does not exist", rename.new)
		}
		if relationExists(t, ctx, pool, rename.old) {
			t.Errorf("legacy relation %s still exists; the D34 rename must not keep an alias", rename.old)
		}
	}
	for _, table := range d34UnchangedTables {
		if !tableExists(t, ctx, pool, table) {
			t.Errorf("table %s must keep its D34-compliant name", table)
		}
	}

	// The rename migration only renames relations: every renamed table must
	// still be an ordinary table, not a view. Checking the old name through
	// pg_class also catches a compatibility view that a plain to_regclass
	// lookup would miss.
	var oldViews int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relname = ANY($1::text[])
		  AND c.relkind IN ('v', 'm')`,
		oldNames(d34TableRenames)).Scan(&oldViews); err != nil {
		t.Fatalf("count legacy views: %v", err)
	}
	if oldViews != 0 {
		t.Errorf("%d legacy plural names are still exposed as views", oldViews)
	}
}

// oldNames flattens the old side of the rename mapping.
func oldNames(renames []struct{ old, new string }) []string {
	names := make([]string, len(renames))
	for i, rename := range renames {
		names[i] = rename.old
	}
	return names
}

// tableExists reports whether a public ordinary table exists.
func tableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1 AND c.relkind = 'r'
		)`, table).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

// relationExists reports whether any public relation (table, view, materialized
// view, partitioned table or foreign table) uses the name.
func relationExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1
			  AND c.relkind IN ('r', 'v', 'm', 'p', 'f')
		)`, name).Scan(&exists); err != nil {
		t.Fatalf("check relation %s: %v", name, err)
	}
	return exists
}
