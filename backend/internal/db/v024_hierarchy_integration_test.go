//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// v0.2.4-B hierarchy inversion (SC-B1/SC-B2, GD-05). These assertions read the
// migrated schema directly so they pin the shape later contexts rely on:
// environment is top-level, sut belongs to an environment and owns base_url,
// the assets are owned by sut_id only, and the derived version binding chain is
// gone in favour of version.environment_id.

func v024ColumnExists(t *testing.T, pool *pgxpool.Pool, table, column string) bool {
	t.Helper()
	var found bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1 AND column_name = $2
		)`, table, column).Scan(&found)
	if err != nil {
		t.Fatalf("column %s.%s lookup: %v", table, column, err)
	}
	return found
}

func v024TableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var found bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND table_type = 'BASE TABLE' AND table_name = $1
		)`, table).Scan(&found)
	if err != nil {
		t.Fatalf("table %s lookup: %v", table, err)
	}
	return found
}

// v024UniqueIndexDef returns the definition of one index of the test schema.
func v024UniqueIndexDef(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var definition string
	err := pool.QueryRow(context.Background(), `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = $1`, name).Scan(&definition)
	if err != nil {
		t.Fatalf("read index %s: %v", name, err)
	}
	return definition
}

func TestV024EnvironmentIsTopLevel(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	if !v024TableExists(t, pool, "environment") {
		t.Fatal("environment table is missing")
	}
	for _, column := range []string{"id", "name", "description", "status", "created_at", "updated_at"} {
		if !v024ColumnExists(t, pool, "environment", column) {
			t.Errorf("environment.%s is missing", column)
		}
	}
	// The inverted hierarchy has no SUT above the environment.
	if v024ColumnExists(t, pool, "environment", "sut_id") {
		t.Error("environment still carries sut_id; it must be a top-level table")
	}
	if v024ColumnExists(t, pool, "environment", "base_url") {
		t.Error("environment still carries base_url; it moved down to sut")
	}
	if v024TableExists(t, pool, "sut_environment") {
		t.Error("sut_environment must be dropped by the v0.2.4-B inversion")
	}

	// status is the active/disabled enum of the shared lifecycle.
	tag, err := pool.Exec(ctx, `INSERT INTO environment (id, name, description, status)
		VALUES (1, 'staging', 'staging environment', 'disabled')`)
	if err != nil {
		t.Fatalf("insert environment: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("insert environment affected %d rows", tag.RowsAffected())
	}
	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (2, 'bad', 'nope')`); err == nil {
		t.Error("environment accepts an unknown status")
	}
	// The environment name is globally unique case-insensitively.
	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (3, 'STAGING', 'active')`); err == nil {
		t.Error("environment accepts a duplicate name")
	}
}

func TestV024SUTBelongsToEnvironmentAndOwnsBaseURL(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (10, 'prod', 'active')`); err != nil {
		t.Fatalf("insert environment: %v", err)
	}
	if !v024ColumnExists(t, pool, "sut", "environment_id") {
		t.Fatal("sut.environment_id is missing")
	}
	if !v024ColumnExists(t, pool, "sut", "base_url") {
		t.Fatal("sut.base_url is missing")
	}
	for _, column := range []string{"name", "intro", "family", "created_by"} {
		if !v024ColumnExists(t, pool, "sut", column) {
			t.Errorf("sut.%s is missing", column)
		}
	}

	// environment_id is a real foreign key inside the resource context.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (11, 999, 'ghost', 'https://ghost.example.com', 1)`); err == nil {
		t.Error("sut accepts an unknown environment_id; the FK must be enforced")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (11, 10, 'shop', 'https://shop.example.com', 1)`); err != nil {
		t.Fatalf("insert sut: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (12, 10, 'SHOP', 'https://shop2.example.com', 1)`); err == nil {
		t.Error("sut accepts a name that duplicates one inside the same environment")
	}
	// The same name is allowed in a different environment.
	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (20, 'dev', 'active')`); err != nil {
		t.Fatalf("insert second environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (13, 20, 'shop', 'https://dev.shop.example.com', 1)`); err != nil {
		t.Errorf("sut name must be unique per environment only: %v", err)
	}

	definition := v024UniqueIndexDef(t, pool, "uk_sut_name")
	if !strings.Contains(definition, "environment_id") || !strings.Contains(definition, "lower(name)") {
		t.Errorf("uk_sut_name is not scoped to (environment_id, lower(name)): %s", definition)
	}
}

func TestV024AssetsAreOwnedBySUTOnly(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (10, 'prod', 'active')`); err != nil {
		t.Fatalf("insert environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (11, 10, 'shop', 'https://shop.example.com', 1)`); err != nil {
		t.Fatalf("insert sut: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (12, 10, 'billing', 'https://billing.example.com', 1)`); err != nil {
		t.Fatalf("insert second sut: %v", err)
	}

	for _, table := range []string{"sut_page", "sut_api", "target_database"} {
		if v024ColumnExists(t, pool, table, "environment_id") {
			t.Errorf("%s still carries the environment_id double FK", table)
		}
		if !v024ColumnExists(t, pool, table, "sut_id") {
			t.Errorf("%s.sut_id is missing", table)
		}
	}

	// page: (sut_id, normalized_url)
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_page (id, sut_id, name, url, route_template)
		VALUES (21, 11, 'orders', 'https://shop.example.com/orders', '/orders')`); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_page (id, sut_id, name, url, route_template)
		VALUES (22, 11, 'orders again', 'https://shop.example.com/orders', '/orders')`); err == nil {
		t.Error("sut_page accepts a duplicate normalized_url inside one SUT")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_page (id, sut_id, name, url, route_template)
		VALUES (23, 12, 'orders', 'https://shop.example.com/orders', '/orders')`); err != nil {
		t.Errorf("sut_page normalized_url must be unique per SUT only: %v", err)
	}
	pageIndex := v024UniqueIndexDef(t, pool, "uk_sut_page_normalized_url")
	if !strings.Contains(pageIndex, "sut_id") || strings.Contains(pageIndex, "environment_id") {
		t.Errorf("uk_sut_page_normalized_url is not scoped to sut_id: %s", pageIndex)
	}

	// api: (sut_id, method, path_template)
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_api (id, sut_id, method, path_template, name)
		VALUES (31, 11, 'GET', '/api/orders', 'list orders')`); err != nil {
		t.Fatalf("insert api: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_api (id, sut_id, method, path_template, name)
		VALUES (32, 11, 'GET', '/api/orders', 'list orders again')`); err == nil {
		t.Error("sut_api accepts a duplicate (method, path_template) inside one SUT")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_api (id, sut_id, method, path_template, name)
		VALUES (33, 12, 'GET', '/api/orders', 'list orders')`); err != nil {
		t.Errorf("sut_api route must be unique per SUT only: %v", err)
	}
	apiIndex := v024UniqueIndexDef(t, pool, "uk_sut_api_route")
	if !strings.Contains(apiIndex, "sut_id") || strings.Contains(apiIndex, "environment_id") {
		t.Errorf("uk_sut_api_route is not scoped to sut_id: %s", apiIndex)
	}

	// database: (sut_id, lower(name)), credentials kept
	if _, err := pool.Exec(ctx, `
		INSERT INTO target_database (id, sut_id, name, kind, dsn, username, password, read_only)
		VALUES (41, 11, 'Shop DB', 'postgres', 'postgres://u:p@localhost:5432/shop', 'u', 'p', true)`); err != nil {
		t.Fatalf("insert database: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO target_database (id, sut_id, name, kind, dsn, username)
		VALUES (42, 11, 'shop db', 'postgres', 'postgres://u@localhost:5432/shop2', 'u')`); err == nil {
		t.Error("target_database accepts a duplicate name inside one SUT")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO target_database (id, sut_id, name, kind, dsn, username)
		VALUES (43, 12, 'Shop DB', 'postgres', 'postgres://u@localhost:5432/shop3', 'u')`); err != nil {
		t.Errorf("target_database name must be unique per SUT only: %v", err)
	}
	databaseIndex := v024UniqueIndexDef(t, pool, "uk_target_database_name")
	if !strings.Contains(databaseIndex, "sut_id") || strings.Contains(databaseIndex, "environment_id") {
		t.Errorf("uk_target_database_name is not scoped to sut_id: %s", databaseIndex)
	}
	for _, column := range []string{"dsn", "username", "password", "read_only", "status", "kind"} {
		if !v024ColumnExists(t, pool, "target_database", column) {
			t.Errorf("target_database.%s is missing", column)
		}
	}

	// The asset FKs cascade from the SUT.
	if _, err := pool.Exec(ctx, `DELETE FROM sut WHERE id = 11`); err != nil {
		t.Fatalf("delete sut: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM sut_page WHERE sut_id = 11)
		     + (SELECT count(*) FROM sut_api WHERE sut_id = 11)
		     + (SELECT count(*) FROM target_database WHERE sut_id = 11)`).Scan(&remaining); err != nil {
		t.Fatalf("count cascaded assets: %v", err)
	}
	if remaining != 0 {
		t.Errorf("deleting a SUT left %d asset rows behind", remaining)
	}
}

func TestV024TestAccountSUTAffinityColumn(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	if !v024ColumnExists(t, pool, "test_account", "sut_id") {
		t.Fatal("test_account.sut_id (affinity column) is missing")
	}
	// The pool stays global: sut_id is nullable and the username stays globally
	// unique.
	if _, err := pool.Exec(ctx, `
		INSERT INTO test_account (id, username, password, status) VALUES (51, 'admin', 'pw', 'active')`); err != nil {
		t.Fatalf("insert account without affinity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO test_account (id, username, password, status) VALUES (52, 'ADMIN', 'pw', 'active')`); err == nil {
		t.Error("test_account accepts a globally duplicated username")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO test_account (id, sut_id, username, password, status)
		VALUES (53, 999, 'shop-admin', 'pw', 'active')`); err == nil {
		t.Error("test_account.sut_id accepts an unknown SUT; the affinity FK must be enforced")
	}
}

func TestV024SUTRepositoryCarriesCodeBaseline(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	if !v024ColumnExists(t, pool, "sut_repository", "code_baseline_commit") {
		t.Fatal("sut_repository.code_baseline_commit is missing")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO code_repository (id, name, path, created_by) VALUES (61, 'shop-api', '/srv/shop', 1)`); err != nil {
		t.Fatalf("insert code_repository: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (10, 'prod', 'active')`); err != nil {
		t.Fatalf("insert environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (11, 10, 'shop', 'https://shop.example.com', 1)`); err != nil {
		t.Fatalf("insert sut: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_repository (id, sut_id, repo_id, code_baseline_commit)
		VALUES (71, 11, 61, 'not-a-sha')`); err == nil {
		t.Error("sut_repository accepts a malformed code_baseline_commit")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_repository (id, sut_id, repo_id, code_baseline_commit)
		VALUES (71, 11, 61, '0123456789abcdef0123456789abcdef01234567')`); err != nil {
		t.Fatalf("insert sut_repository baseline: %v", err)
	}
	// The (sut_id, repo_id) natural key of v0.22 DB-12 survives.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_repository (id, sut_id, repo_id) VALUES (72, 11, 61)`); err == nil {
		t.Error("sut_repository accepts a second row for the same (sut_id, repo_id) pair")
	}
	// An unresolved baseline stays NULL, so the column must be nullable.
	if _, err := pool.Exec(ctx, `
		INSERT INTO code_repository (id, name, path, created_by) VALUES (62, 'shop-web', '/srv/web', 1)`); err != nil {
		t.Fatalf("insert second code_repository: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_repository (id, sut_id, repo_id) VALUES (73, 11, 62)`); err != nil {
		t.Errorf("sut_repository must accept a NULL code_baseline_commit: %v", err)
	}
}

func TestV024VersionBindsEnvironmentDirectly(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	if !v024ColumnExists(t, pool, "version", "environment_id") {
		t.Fatal("version.environment_id is missing")
	}
	for _, table := range []string{"version_sut", "version_sut_resource", "version_repository_override"} {
		if v024TableExists(t, pool, table) {
			t.Errorf("%s must be dropped by the v0.2.4-B environment binding", table)
		}
	}
	// version_account survives: the version still pins its account subset.
	if !v024TableExists(t, pool, "version_account") {
		t.Error("version_account must be kept")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO environment (id, name, status) VALUES (10, 'prod', 'active')`); err != nil {
		t.Fatalf("insert environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, status, created_by, environment_id)
		VALUES (81, 'v1', 'initial', 1, 10)`); err != nil {
		t.Fatalf("insert version with environment_id: %v", err)
	}
	var environmentID int64
	if err := pool.QueryRow(ctx, `SELECT environment_id FROM version WHERE id = 81`).Scan(&environmentID); err != nil {
		t.Fatalf("read version.environment_id: %v", err)
	}
	if environmentID != 10 {
		t.Errorf("version.environment_id = %d, want 10", environmentID)
	}

	// The binding is mandatory at the DB level too, not only in the domain:
	// version_0016 sets NOT NULL after discarding the unbindable legacy rows
	// (GD-05). It stays a logical cross-context reference, so there must be no
	// physical foreign key into the resource context.
	var nullable string
	if err := pool.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'version' AND column_name = 'environment_id'`).
		Scan(&nullable); err != nil {
		t.Fatalf("read version.environment_id nullability: %v", err)
	}
	if nullable != "NO" {
		t.Errorf("version.environment_id is_nullable = %s, want NO", nullable)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, status, created_by) VALUES (82, 'unbound', 'initial', 1)`); err == nil {
		t.Error("version accepts a row without an environment_id")
	}
	var fkCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE contype = 'f' AND conrelid = 'version'::regclass
		  AND confrelid = 'environment'::regclass`).Scan(&fkCount); err != nil {
		t.Fatalf("count version->environment foreign keys: %v", err)
	}
	if fkCount != 0 {
		t.Errorf("version.environment_id has %d physical foreign key(s); cross-context references stay logical", fkCount)
	}
	// The lookup index is a plain one now that the column cannot be NULL.
	indexDef := v024UniqueIndexDef(t, pool, "idx_version_environment")
	if strings.Contains(indexDef, "WHERE") {
		t.Errorf("idx_version_environment is still partial on a NOT NULL column: %s", indexDef)
	}

	// scope_exclude is gone with version_sut, so no column may survive anywhere.
	var scopeExcludeColumns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND column_name = 'scope_exclude'`).
		Scan(&scopeExcludeColumns); err != nil {
		t.Fatalf("count scope_exclude columns: %v", err)
	}
	if scopeExcludeColumns != 0 {
		t.Errorf("scope_exclude still exists on %d column(s)", scopeExcludeColumns)
	}
}

// TestV024MigrationIsIdempotentOnMigratedDatabase proves the already-migrated
// startup path: a second Migrate on the same schema applies nothing.
func TestV024MigrationIsIdempotentOnMigratedDatabase(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	again, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second Migrate on an already migrated schema: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Migrate applied %v", again)
	}
}

// TestV024LegacyVersionDiscardClearsExactlyTheFKClosure pins the GD-05
// destructive step of version_0016. Three of the five foreign keys into version
// are NO ACTION rather than CASCADE, so a bare DELETE FROM version would raise a
// foreign_key_violation on a populated database; the migration therefore uses
// TRUNCATE version CASCADE, which follows the schema's own declared dependency
// graph. This test seeds both sides of the boundary and proves the closure is
// exactly the 13 FK-dependent tables, that version_account (logical, but owned
// by the version context) is cleared with them, and that other contexts' logical
// version_id rows are left alone rather than silently wiped.
func TestV024LegacyVersionDiscardClearsExactlyTheFKClosure(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	exec(`INSERT INTO environment (id, name, status) VALUES (10, 'prod', 'active')`)
	exec(`INSERT INTO version (id, title, status, created_by, environment_id)
		VALUES (81, 'legacy', 'initial', 1, 10)`)
	// The FK closure, one row per table.
	exec(`INSERT INTO version_plan_item (id, version_id, phase, title) VALUES (1, 81, 'design', 'p')`)
	exec(`INSERT INTO version_fact (id, version_id, kind, key, source, content_hash)
		VALUES (1, 81, 'page', 'k', 'execution', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`)
	exec(`INSERT INTO conversation (id, version_id, kind, title) VALUES (1, 81, 'clarification', 'c')`)
	exec(`INSERT INTO conversation_message (id, conversation_id, role) VALUES (1, 1, 'user')`)
	exec(`INSERT INTO conversation_message_part (id, message_id, seq, part_type) VALUES (1, 1, 1, 'text')`)
	exec(`INSERT INTO conversation_summary (id, conversation_id, summary, version) VALUES (1, 1, 's', 1)`)
	exec(`INSERT INTO requirement (id, version_id) VALUES (1, 81)`)
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, content_hash)
		VALUES (1, 1, 1, '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`)
	exec(`INSERT INTO requirement_confirmation (id, requirement_version_id, scope, actor_id) VALUES (1, 1, 'requirement', 1)`)
	exec(`INSERT INTO requirement_closure (id, requirement_id, requirement_version_id, close_round, check_result)
		VALUES (1, 1, 1, 1, '{}'::jsonb)`)
	// requirement_attachment hangs off the content ledger, and
	// requirement_extraction_run hangs off the attachment: both are inside the
	// closure (the run directly, the attachment transitively via the run's
	// NO ACTION edge being absent -- see the orphan assertion below).
	exec(`INSERT INTO content_object (sha256, size, relative_path) VALUES ('0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 3, 'a/b.png')`)
	exec(`INSERT INTO content_ref (id, object_id, owner_kind, owner_id)
		VALUES (1, '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 'requirement_attachment', 1)`)
	exec(`INSERT INTO requirement_attachment (id, version_id, content_ref_id, name, mime, size)
		VALUES (1, 81, 1, 'a.png', 'image/png', 3)`)
	exec(`INSERT INTO requirement_extraction_run (id, version_id, attachment_id, input_sha256, status)
		VALUES (1, 81, 1, '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 'done')`)
	exec(`INSERT INTO requirement_extraction_segment (id, run_id, seq, char_start, char_end, text_sha256, status)
		VALUES (1, 1, 1, 0, 1, '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 'done')`)
	// Version-context owned but LOGICAL (no FK): cleared explicitly by the
	// migration so it cannot outlive its version.
	exec(`INSERT INTO version_account (id, version_id, account_id, enabled) VALUES (1, 81, 5, true)`)
	// Other contexts' LOGICAL version_id rows: deliberately left orphaned.
	exec(`INSERT INTO clarification_handoff (id, version_id, revision) VALUES (1, 81, 1)`)

	// The statements version_0016 runs, in order.
	exec(`DELETE FROM version_account`)
	if _, err := pool.Exec(ctx, `TRUNCATE version CASCADE`); err != nil {
		t.Fatalf("TRUNCATE version CASCADE: %v", err)
	}

	cleared := []string{
		"version", "version_fact", "version_plan_item",
		"conversation", "conversation_message", "conversation_message_part", "conversation_summary",
		"requirement", "requirement_version", "requirement_confirmation", "requirement_closure",
		"requirement_extraction_run", "requirement_extraction_segment",
		"version_account",
	}
	for _, table := range cleared {
		var rows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if rows != 0 {
			t.Errorf("%s kept %d row(s); it is inside the discard closure", table, rows)
		}
	}

	// requirement_attachment has no FK into the closure (the edge runs the other
	// way, from requirement_extraction_run), so it survives as an orphan — which
	// is exactly what the migration documents rather than hides.
	var orphans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM requirement_attachment`).Scan(&orphans); err != nil {
		t.Fatalf("count requirement_attachment: %v", err)
	}
	if orphans != 1 {
		t.Errorf("requirement_attachment = %d rows, want the 1 documented orphan", orphans)
	}
	var handoffs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM clarification_handoff`).Scan(&handoffs); err != nil {
		t.Fatalf("count clarification_handoff: %v", err)
	}
	if handoffs != 1 {
		t.Errorf("clarification_handoff = %d rows, want the 1 documented orphan", handoffs)
	}

	// A bare DELETE FROM version would have failed on the NO ACTION edges; prove
	// the constraint shape the migration relies on is still what it documents.
	var noAction int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE contype = 'f' AND confrelid = 'version'::regclass AND confdeltype = 'a'`).
		Scan(&noAction); err != nil {
		t.Fatalf("count NO ACTION foreign keys into version: %v", err)
	}
	if noAction != 3 {
		t.Errorf("NO ACTION foreign keys into version = %d, want 3 (conversation, requirement, requirement_extraction_run)", noAction)
	}
}
