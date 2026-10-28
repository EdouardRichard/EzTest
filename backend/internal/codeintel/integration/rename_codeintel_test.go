//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// codeintelTableRenames is the D34 codeintel rename mapping of spec §3.2: the
// old plural table name and the singular name the rename migration must
// produce. Field, constraint and index names are intentionally left alone.
var codeintelTableRenames = []struct {
	oldName string
	newName string
}{
	{"code_change_sets", "code_change_set"},
	{"code_index_runs", "code_index_run"},
	{"code_entities", "code_entity"},
	{"code_edges", "code_edge"},
	{"code_fact_versions", "code_fact_version"},
	{"code_analyses", "code_analysis"},
	{"code_impact_candidates", "code_impact_candidate"},
	{"code_anchors", "code_anchor"},
}

// TestP5RenameCodeintelTables applies the embedded migrations and asserts the
// codeintel tables were renamed to their D34 singular names while
// code_analysis_cache kept its name.
func TestP5RenameCodeintelTables(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	for _, rename := range codeintelTableRenames {
		if renameTableExists(t, ctx, pool, rename.oldName) {
			t.Errorf("old table %s still exists, want it renamed to %s", rename.oldName, rename.newName)
		}
		if !renameTableExists(t, ctx, pool, rename.newName) {
			t.Errorf("renamed table %s does not exist", rename.newName)
		}
	}

	// code_analysis_cache is the one codeintel table D34 leaves untouched.
	if !renameTableExists(t, ctx, pool, "code_analysis_cache") {
		t.Errorf("table code_analysis_cache does not exist; it must keep its name")
	}
	if renameTableExists(t, ctx, pool, "code_analysis_caches") {
		t.Errorf("table code_analysis_caches exists; code_analysis_cache must keep its name")
	}
}

// renameTableExists reports whether a public table (or view) exists.
func renameTableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}
