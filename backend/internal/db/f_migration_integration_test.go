//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFMigrationSchema pins the v0.2.4-F schema foundation (Task 2, SC-F2/F3/F5/F7):
// the three append-only execution fact tables (sut_resolution, asset_registration,
// session_fixture) and the DB-level immutability guard on
// sut_repository.code_baseline_commit. Assertions come from the F spec contract,
// not from observed output.
func TestFMigrationSchema(t *testing.T) {
	pool := integrationDB(t)
	ctx := context.Background()

	// SC-F2/F3/F5: the three append-only fact tables exist.
	for _, table := range []string{"sut_resolution", "asset_registration", "session_fixture"} {
		requireTable(t, pool, table)
		requirePKConstraint(t, pool, "pk_"+table)
	}

	// SC-F2 sut_resolution: case_run_id -> sut_ids + resolution_method + the run's
	// actually touched/verified page/API asset ids (the SC-F10 knowledge truth source).
	// ambiguous (T9 fix round 1, ruling P19) records a selection-from-ambiguity.
	for _, column := range []string{
		"id", "case_run_id", "sut_ids", "resolution_method",
		"touched_page_ids", "touched_api_ids", "ambiguous", "created_at", "created_by",
	} {
		requireColumn(t, pool, "sut_resolution", column)
	}
	requireColumnNotNull(t, pool, "sut_resolution", "case_run_id")
	requireColumnNotNull(t, pool, "sut_resolution", "ambiguous")
	requireIndex(t, pool, "idx_sut_resolution_case_run")

	// SC-F3 asset_registration: sut_id + asset_kind(page/api/db) + asset_id +
	// evidence_refs + source + gate_result.
	for _, column := range []string{
		"id", "sut_id", "asset_kind", "asset_id", "source",
		"evidence_refs", "gate_result", "created_at", "created_by",
	} {
		requireColumn(t, pool, "asset_registration", column)
	}
	requireColumnNotNull(t, pool, "asset_registration", "sut_id")
	requireColumnNotNull(t, pool, "asset_registration", "asset_id")
	requireCheckSet(t, pool, "chk_asset_registration_asset_kind",
		[]string{"page", "api", "db"}, []string{"bogus"})
	requireCheckSet(t, pool, "chk_asset_registration_source",
		[]string{"admin", "ai", "import"}, []string{"bogus"})
	requireIndex(t, pool, "idx_asset_registration_sut")
	requireIndex(t, pool, "idx_asset_registration_asset")

	// SC-F5 session_fixture: the (version,sut,environment,role) reuse key plus the
	// content-addressed storage-state reference.
	for _, column := range []string{
		"id", "version_id", "sut_id", "environment_id", "role",
		"storage_state_ref", "created_at", "created_by",
	} {
		requireColumn(t, pool, "session_fixture", column)
	}
	requireColumnNotNull(t, pool, "session_fixture", "version_id")
	requireColumnNotNull(t, pool, "session_fixture", "sut_id")
	requireColumnNotNull(t, pool, "session_fixture", "environment_id")
	requireIndex(t, pool, "idx_session_fixture_key")

	// Append-only signature (version_fact precedent): a fact carries created_at
	// but NEVER updated_at — rows are appended, never rewritten in place.
	for _, table := range []string{"sut_resolution", "asset_registration", "session_fixture"} {
		requireColumn(t, pool, table, "created_at")
		requireNoColumn(t, pool, table, "updated_at")
	}

	// Cross-context references are logical BIGINT only: asset_registration and
	// session_fixture carry no physical foreign key (constitution 五.7, facts
	// independent; every reference crosses into resource/version). sut_resolution's
	// cross-context arrays are logical too; its NOT NULL case_run_id is the one
	// within-context reference and carries a real FK to case_run (AGENTS §2.3,
	// evidence/step_result precedent).
	requireNoForeignKeys(t, pool, "asset_registration", "session_fixture")
	requireSUTResolutionCaseRunFK(t, pool)

	// Append-only log semantics: no UNIQUE owner key forces a single mutable row;
	// registering/resolving/saving the same owner twice appends a second row and
	// never rewrites the first (sut_registration precedent).
	requireNoUniqueConstraint(t, pool, "sut_resolution", "asset_registration", "session_fixture")
	requireAppendOnlyLog(t, pool, ctx)

	// CHECK constraints reject out-of-vocabulary values and a non-array evidence
	// payload (server-truth gate inputs).
	requireFactCheckEnforcement(t, pool, ctx)

	// SC-F7 / V-0024: a pinned code_baseline_commit is immutable at the DB level.
	requireCodeBaselineImmutability(t, pool, ctx)
}

// requireNoForeignKeys asserts the named tables carry no physical foreign key,
// pinning the logical cross-context BIGINT reference rule.
func requireNoForeignKeys(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND c.contype = 'f'
		  AND t.relname::text = ANY($1)
	`, tables).Scan(&count); err != nil {
		t.Fatalf("count foreign keys: %v", err)
	}
	if count != 0 {
		t.Errorf("fact tables carry %d physical foreign keys, want 0 (logical BIGINT references)", count)
	}
}

// requireSUTResolutionCaseRunFK asserts sut_resolution carries exactly one
// physical foreign key and it targets case_run — the NOT NULL within-context
// reference (AGENTS §2.3), while its resource-context arrays stay logical.
func requireSUTResolutionCaseRunFK(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT rt.relname
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_class rt ON rt.oid = c.confrelid
		WHERE n.nspname = current_schema() AND c.contype = 'f' AND t.relname = 'sut_resolution'
	`)
	if err != nil {
		t.Fatalf("query sut_resolution foreign keys: %v", err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			t.Fatalf("scan foreign key target: %v", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys: %v", err)
	}
	if len(targets) != 1 || targets[0] != "case_run" {
		t.Errorf("sut_resolution foreign keys = %v, want exactly [case_run]", targets)
	}
}

// requireNoUniqueConstraint asserts the named append-only tables carry no UNIQUE
// constraint (contype 'u'): an owner key would force a single mutable row and
// defeat the append-only fact log.
func requireNoUniqueConstraint(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND c.contype = 'u'
		  AND t.relname::text = ANY($1)
	`, tables).Scan(&count); err != nil {
		t.Fatalf("count unique constraints: %v", err)
	}
	if count != 0 {
		t.Errorf("append-only fact tables carry %d UNIQUE constraints, want 0", count)
	}
}

// requireAppendOnlyLog inserts two facts for the same logical owner in each table
// and asserts both survive — the schema is an append-only log, not a mutable
// single-row-per-owner table.
func requireAppendOnlyLog(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	// sut_resolution.case_run_id is a real within-context FK, so build a minimal
	// case_run first (execution_intent -> run_wave -> case_run; version_id and
	// case_version_id are logical references needing no parent rows).
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, environment_fingerprint, dedupe_key)
		VALUES (8100001, 8100000, 8100002, repeat('f', 64), 'f-sut-resolution-dedupe');
		INSERT INTO run_wave (id, version_id) VALUES (8100003, 8100000);
		INSERT INTO case_run (id, wave_id, intent_id, case_version_id) VALUES (8100, 8100003, 8100001, 8100002);
	`); err != nil {
		t.Fatalf("build case_run for sut_resolution FK: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_resolution (id, case_run_id, sut_ids, resolution_method, touched_page_ids, touched_api_ids, created_by)
		VALUES (8101, 8100, '{10}', 'catalog_hit', '{20}', '{30}', 1),
		       (8102, 8100, '{10,11}', 'exploration', '{20,21}', '{30,31}', 1)
	`); err != nil {
		t.Fatalf("append two sut_resolution facts for one case_run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO asset_registration (id, sut_id, asset_kind, asset_id, source, evidence_refs, gate_result, created_by)
		VALUES (8201, 8200, 'page', 8210, 'ai', '["evidence:sha256:aa"]', 'approved', 1),
		       (8202, 8200, 'page', 8210, 'ai', '["evidence:sha256:bb"]', 'approved', 1)
	`); err != nil {
		t.Fatalf("append two asset_registration facts for one asset: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_fixture (id, version_id, sut_id, environment_id, role, storage_state_ref, created_by)
		VALUES (8301, 8300, 8310, 8320, 'admin', 'state:sha256:aa', 1),
		       (8302, 8300, 8310, 8320, 'admin', 'state:sha256:bb', 1)
	`); err != nil {
		t.Fatalf("append two session_fixture facts for one reuse key: %v", err)
	}
	for _, tc := range []struct {
		table string
		where string
		args  []any
	}{
		{"sut_resolution", "case_run_id = 8100", nil},
		{"asset_registration", "sut_id = 8200 AND asset_kind = 'page' AND asset_id = 8210", nil},
		{"session_fixture", "version_id = 8300 AND sut_id = 8310 AND environment_id = 8320 AND role = 'admin'", nil},
	} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+tc.table+` WHERE `+tc.where, tc.args...).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", tc.table, err)
		}
		if count != 2 {
			t.Errorf("%s rows for one owner = %d, want 2 (append-only log keeps both facts)", tc.table, count)
		}
	}
}

// requireFactCheckEnforcement pins that the asset vocabulary CHECKs and the
// evidence_refs array CHECK actually reject bad values.
func requireFactCheckEnforcement(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO asset_registration (id, sut_id, asset_kind, asset_id, created_by)
		VALUES (8250, 8200, 'bogus', 8210, 1)
	`); err == nil {
		t.Error("asset_registration accepts asset_kind 'bogus'")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO asset_registration (id, sut_id, asset_kind, asset_id, source, created_by)
		VALUES (8251, 8200, 'page', 8210, 'robot', 1)
	`); err == nil {
		t.Error("asset_registration accepts source 'robot'")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO asset_registration (id, sut_id, asset_kind, asset_id, evidence_refs, created_by)
		VALUES (8252, 8200, 'page', 8210, '{"not":"array"}'::jsonb, 1)
	`); err == nil {
		t.Error("asset_registration accepts a non-array evidence_refs payload")
	}
	// The three asset kinds and the default source/evidence are accepted.
	var kind, source string
	var refs string
	if err := pool.QueryRow(ctx, `
		INSERT INTO asset_registration (id, sut_id, asset_kind, asset_id, created_by)
		VALUES (8253, 8200, 'db', 8211, 1)
		RETURNING asset_kind, source, evidence_refs::text
	`).Scan(&kind, &source, &refs); err != nil {
		t.Fatalf("insert db asset with defaults: %v", err)
	}
	if kind != "db" || source != "ai" || refs != "[]" {
		t.Errorf("asset defaults = (%q,%q,%q), want (db,ai,[])", kind, source, refs)
	}
}

// requireCodeBaselineImmutability pins the V-0024 DB guard: the first pin
// (NULL -> SHA) is allowed, but a pinned commit can be neither overwritten nor
// cleared, while the 40-hex CHECK still rejects a malformed SHA on an unpinned row.
func requireCodeBaselineImmutability(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)

	// The guard trigger and the original 40-hex CHECK both exist.
	var triggerExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()
			  AND c.relname = 'sut_repository'
			  AND t.tgname = 'trg_sut_repository_code_baseline_immutable'
			  AND NOT t.tgisinternal
		)
	`).Scan(&triggerExists); err != nil {
		t.Fatalf("check code_baseline trigger: %v", err)
	}
	if !triggerExists {
		t.Fatal("missing trigger trg_sut_repository_code_baseline_immutable on sut_repository")
	}
	var checkExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = 'chk_sut_repository_code_baseline_commit'
			  AND contype = 'c'
			  AND connamespace = current_schema()::regnamespace
		)
	`).Scan(&checkExists); err != nil {
		t.Fatalf("check 40-hex CHECK: %v", err)
	}
	if !checkExists {
		t.Error("the 40-hex CHECK chk_sut_repository_code_baseline_commit must be preserved")
	}

	// sut_repository.sut_id/repo_id are logical BIGINT (no FK), so a row needs no
	// parent catalog rows.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_repository (id, sut_id, repo_id) VALUES (8401, 8410, 8420)
	`); err != nil {
		t.Fatalf("insert unpinned sut_repository: %v", err)
	}

	// First pin NULL -> SHA is allowed.
	if _, err := pool.Exec(ctx, `UPDATE sut_repository SET code_baseline_commit = $1 WHERE id = 8401`, shaA); err != nil {
		t.Fatalf("first pin (NULL -> SHA) must be allowed: %v", err)
	}
	// Re-pinning the identical SHA is a no-op and stays allowed.
	if _, err := pool.Exec(ctx, `UPDATE sut_repository SET code_baseline_commit = $1 WHERE id = 8401`, shaA); err != nil {
		t.Errorf("re-pinning the identical SHA must be a no-op: %v", err)
	}
	// Overwriting a pinned commit with a different SHA is blocked.
	if _, err := pool.Exec(ctx, `UPDATE sut_repository SET code_baseline_commit = $1 WHERE id = 8401`, shaB); err == nil {
		t.Error("a pinned code_baseline_commit was overwritten (immutability guard missing)")
	}
	// Clearing a pinned commit back to NULL is blocked (the NULLIF($3,'') path).
	if _, err := pool.Exec(ctx, `UPDATE sut_repository SET code_baseline_commit = NULL WHERE id = 8401`); err == nil {
		t.Error("a pinned code_baseline_commit was cleared to NULL (immutability guard missing)")
	}
	// The pinned value survived both rejected writes.
	var pinned string
	if err := pool.QueryRow(ctx, `SELECT code_baseline_commit FROM sut_repository WHERE id = 8401`).Scan(&pinned); err != nil {
		t.Fatalf("read pinned commit: %v", err)
	}
	if pinned != shaA {
		t.Errorf("pinned commit = %q, want %q (guard must leave the pin intact)", pinned, shaA)
	}

	// The 40-hex CHECK still rejects a malformed SHA on an UNPINNED row (the guard
	// only blocks changes to an already-pinned value).
	if _, err := pool.Exec(ctx, `INSERT INTO sut_repository (id, sut_id, repo_id) VALUES (8402, 8411, 8421)`); err != nil {
		t.Fatalf("insert second unpinned sut_repository: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sut_repository SET code_baseline_commit = 'not-a-sha' WHERE id = 8402`); err == nil {
		t.Error("the 40-hex CHECK accepted a malformed commit SHA")
	}
	// An unpinned row can still be pinned for the first time.
	if _, err := pool.Exec(ctx, `UPDATE sut_repository SET code_baseline_commit = $1 WHERE id = 8402`, shaB); err != nil {
		t.Errorf("first pin of a second row must be allowed: %v", err)
	}
}
