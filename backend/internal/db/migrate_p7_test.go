//go:build integration

package db_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// p7MigrationNames is the append-only P7 tail of migrations/order.txt (the
// spec §4.1 migration table, positions 33-40). The order is part of the P7
// contract: later migrations extend the same tables and later tasks read these
// names.
var p7MigrationNames = []string{
	"delivery_0001_issue.sql",
	"delivery_0002_report.sql",
	"delivery_0003_analysis.sql",
	"resource_0007_db_target.sql",
	"version_0005_continue.sql",
	"execution_0002_autonomy.sql",
	"mindmap_0002_supplement.sql",
	"codeintel_0005_issue_anchor.sql",
}

// TestP7Schema pins the P7 data-model contract (spec §4.2) against a real
// PostgreSQL database: the new tables and columns exist with their declared
// types, the widened CHECK constraints accept the P7 values in addition to the
// legacy ones (verified by real inserts, not only by DDL text), the embedded
// migration set is idempotent across two startup runs, and order.txt appends the
// eight P7 migrations in the documented order.
func TestP7Schema(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	ctx := context.Background()

	// Two startup runs: the second one must verify checksums and apply nothing
	// (Agent.md §6.3 idempotency).
	for attempt := 1; attempt <= 2; attempt++ {
		if err := db.MigrateEmbedded(ctx, pool); err != nil {
			t.Fatalf("migrate embedded (run %d): %v", attempt, err)
		}
	}

	for _, table := range []string{
		"issue", "issue_ref", "report", "report_ref", "analysis_run", "db_target",
	} {
		if !tableExists(t, ctx, pool, table) {
			t.Errorf("P7 table %s does not exist", table)
		}
	}

	for _, want := range []struct {
		table    string
		column   string
		dataType string
		notNull  bool
	}{
		{"version", "continue_phase", "text", false},
		{"version", "continued_at", "timestamp with time zone", false},
		{"run_batch", "kind", "text", true},
		{"run_batch", "parent_batch_id", "bigint", false},
		{"run_batch", "base_fallback_reason", "text", true},
		{"run_batch", "graph_hash", "text", true},
		{"run_batch", "supplement_attempted", "boolean", true},
		{"run_batch", "autonomy_done", "boolean", true},
		{"case_run", "source", "text", true},
	} {
		assertP7Column(t, ctx, pool, want.table, want.column, want.dataType, want.notNull)
	}

	assertP7CheckWidening(t, ctx, pool)
	assertP7OrderTail(t)
}

// assertP7Column proves a P7 column exists with the spec type and nullability.
func assertP7Column(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	table, column, wantType string, wantNotNull bool) {
	t.Helper()
	var dataType, isNullable string
	err := pool.QueryRow(ctx, `
		SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
		table, column).Scan(&dataType, &isNullable)
	if err != nil {
		t.Errorf("%s.%s does not exist: %v", table, column, err)
		return
	}
	if dataType != wantType {
		t.Errorf("%s.%s type = %q, want %q", table, column, dataType, wantType)
	}
	if notNull := isNullable == "NO"; notNull != wantNotNull {
		t.Errorf("%s.%s nullable = %t, want %t", table, column, !notNull, !wantNotNull)
	}
}

// assertP7CheckWidening proves the three widened CHECK constraints keep every
// legacy value and accept the new P7 value. Both the constraint definition and
// a real insert are checked so the test cannot pass on stale DDL text alone;
// the legacy auto-named constraints must be replaced, not duplicated.
func assertP7CheckWidening(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	for _, want := range []struct {
		table, constraint string
		values            []string
	}{
		{"evidence", "ck_evidence_kind",
			[]string{"screenshot", "http_exchange", "console_log", "network_log", "download", "db_result"}},
		{"case_design_run", "ck_case_design_run_kind",
			[]string{"explore", "design", "review", "data_plan", "supplement", "incremental_design"}},
		{"code_anchor", "ck_code_anchor_subject_kind",
			[]string{"version", "requirement", "app_model", "case", "issue"}},
	} {
		var definition string
		err := pool.QueryRow(ctx, `
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conrelid = to_regclass($1) AND conname = $2`,
			"public."+want.table, want.constraint).Scan(&definition)
		if err != nil {
			t.Errorf("%s constraint %s is missing: %v", want.table, want.constraint, err)
			continue
		}
		for _, value := range want.values {
			if !strings.Contains(definition, "'"+value+"'") {
				t.Errorf("%s constraint %s does not allow %q: %s",
					want.table, want.constraint, value, definition)
			}
		}
	}

	for _, legacy := range []struct{ table, constraint string }{
		{"evidence", "evidence_kind_check"},
		{"case_design_run", "case_design_run_kind_check"},
	} {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)::int
			FROM pg_constraint
			WHERE conrelid = to_regclass($1) AND conname = $2`,
			"public."+legacy.table, legacy.constraint).Scan(&count); err != nil {
			t.Fatalf("count legacy %s constraint: %v", legacy.table, err)
		}
		if count != 0 {
			t.Errorf("legacy constraint %s.%s still exists; the widening must replace it",
				legacy.table, legacy.constraint)
		}
	}

	// A run_batch row supplies the evidence FK parent and exercises the new
	// graph_hash NOT NULL column.
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_batch (id, version_id, batch_no, baseline_hash, graph_hash, created_by)
		VALUES (1, 1, 1, repeat('a', 64), repeat('a', 64), 1)`); err != nil {
		t.Fatalf("insert P7 run_batch probe: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO evidence (id, batch_id, kind, path, sha256, size_bytes)
		VALUES (1, 1, 'db_result', 'p7/db-result.json', repeat('b', 64), 1)`); err != nil {
		t.Errorf("evidence rejects kind 'db_result': %v", err)
	}
	for i, kind := range []string{"supplement", "incremental_design"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO case_design_run (id, version_id, kind, batch_id, created_by)
			VALUES ($1, 1, $2, 1, 1)`, int64(2+i), kind); err != nil {
			t.Errorf("case_design_run rejects kind %q: %v", kind, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO code_anchor (id, version_id, repo_id, subject_kind, subject_id, file_path, created_by)
		VALUES (1, 1, 1, 'issue', 1, 'src/p7/issue.go', 1)`); err != nil {
		t.Errorf("code_anchor rejects subject_kind 'issue': %v", err)
	}
}

// assertP7OrderTail proves migrations/order.txt ends with the eight P7
// migrations in the spec §4.1 order. The integration test runs with the package
// directory (backend/internal/db) as its working directory.
func assertP7OrderTail(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile("migrations/order.txt")
	if err != nil {
		t.Fatalf("read migrations/order.txt: %v", err)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	if len(names) < len(p7MigrationNames) {
		t.Fatalf("order.txt has %d migrations, want at least %d", len(names), len(p7MigrationNames))
	}
	got := names[len(names)-len(p7MigrationNames):]
	if strings.Join(got, "|") != strings.Join(p7MigrationNames, "|") {
		t.Fatalf("order.txt P7 tail = %v, want %v", got, p7MigrationNames)
	}
}

// TestP7MigrationBackfill upgrades a P6 database that already holds data. A
// fresh-schema test cannot catch a missing backfill: the P6 tables are empty,
// so SET NOT NULL would succeed anyway. This test pins the execution_0002
// ordering the spec requires (add columns -> backfill -> SET NOT NULL -> add
// CHECK) and the two documented data rules: graph_hash is backfilled from
// baseline_hash, autonomy_done is set for P6 terminal batches only, case_run
// source defaults to baseline, and existing evidence kinds survive the
// evidence.kind constraint replacement.
func TestP7MigrationBackfill(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	ctx := context.Background()

	if err := db.Migrate(ctx, pool, p6MigrationFS(t)); err != nil {
		t.Fatalf("migrate P6 schema: %v", err)
	}

	terminalHash := strings.Repeat("a", 64)
	activeHash := strings.Repeat("d", 64)
	for _, batch := range []struct {
		id           int64
		batchNo      int64
		status       string
		baselineHash string
	}{
		{1, 1, "done", terminalHash},
		{2, 2, "running", activeHash},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, created_by)
			VALUES ($1, 1, $2, $3, $4, 1)`,
			batch.id, batch.batchNo, batch.status, batch.baselineHash); err != nil {
			t.Fatalf("insert P6 run_batch %d: %v", batch.id, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_run (id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot)
		VALUES (1, 1, 1, 1, 1, $1, '{}'::jsonb)`, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("insert P6 case_run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO evidence (id, batch_id, kind, path, sha256, size_bytes)
		VALUES (1, 1, 'screenshot', 'p6/shot.png', $1, 1)`, strings.Repeat("c", 64)); err != nil {
		t.Fatalf("insert P6 evidence: %v", err)
	}

	if err := db.MigrateEmbedded(ctx, pool); err != nil {
		t.Fatalf("migrate P7 schema over P6 data: %v", err)
	}

	for _, want := range []struct {
		id           int64
		kind         string
		baselineHash string
		autonomyDone bool
	}{
		{1, "baseline", terminalHash, true},
		{2, "baseline", activeHash, false},
	} {
		var kind, graphHash string
		var autonomyDone bool
		if err := pool.QueryRow(ctx, `
			SELECT kind, graph_hash, autonomy_done FROM run_batch WHERE id = $1`, want.id).
			Scan(&kind, &graphHash, &autonomyDone); err != nil {
			t.Fatalf("read P7 run_batch %d: %v", want.id, err)
		}
		if kind != want.kind {
			t.Errorf("run_batch %d kind = %q, want %q", want.id, kind, want.kind)
		}
		if graphHash != want.baselineHash {
			t.Errorf("run_batch %d graph_hash = %q, want baseline_hash %q",
				want.id, graphHash, want.baselineHash)
		}
		if autonomyDone != want.autonomyDone {
			t.Errorf("run_batch %d autonomy_done = %t, want %t",
				want.id, autonomyDone, want.autonomyDone)
		}
	}

	var source, evidenceKind string
	if err := pool.QueryRow(ctx, `SELECT source FROM case_run WHERE id = 1`).Scan(&source); err != nil {
		t.Fatalf("read P7 case_run source: %v", err)
	}
	if source != "baseline" {
		t.Errorf("case_run.source = %q, want baseline", source)
	}
	if err := pool.QueryRow(ctx, `SELECT kind FROM evidence WHERE id = 1`).Scan(&evidenceKind); err != nil {
		t.Fatalf("read P7 evidence kind: %v", err)
	}
	if evidenceKind != "screenshot" {
		t.Errorf("legacy evidence kind = %q, want screenshot", evidenceKind)
	}
}

// p6MigrationFS builds the on-disk P6 migration set (everything before the P7
// tail) so an upgrade can be exercised without hard-coding the 32 file names.
func p6MigrationFS(t *testing.T) fstest.MapFS {
	t.Helper()
	raw, err := os.ReadFile("migrations/order.txt")
	if err != nil {
		t.Fatalf("read migrations/order.txt: %v", err)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	if len(names) <= len(p7MigrationNames) {
		t.Fatalf("order.txt has %d migrations, want more than the %d P7 migrations",
			len(names), len(p7MigrationNames))
	}
	fsys := fstest.MapFS{}
	var order strings.Builder
	for _, name := range names[:len(names)-len(p7MigrationNames)] {
		content, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatalf("read P6 migration %s: %v", name, err)
		}
		fsys["migrations/"+name] = &fstest.MapFile{Data: content}
		order.WriteString(name + "\n")
	}
	fsys["migrations/order.txt"] = &fstest.MapFile{Data: []byte(order.String())}
	return fsys
}
