//go:build integration

// Package integration_test holds the P5 mindmap integration tests. It uses the
// external test package so the migrations can be asserted against a real
// PostgreSQL database without importing production internals.
package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mindmapTables are the nine spec §5.2 tables created by mindmap_0001.
var mindmapTables = []string{
	"case_node",
	"case_step",
	"case_assertion",
	"case_source",
	"case_review",
	"case_review_finding",
	"case_baseline",
	"case_design_run",
	"case_export",
}

// TestP5MindmapTables applies the embedded migrations and asserts the P5
// schema: the nine case tables exist, every table has its named pk_<table>
// primary key, ids are application-generated, and the two widened CHECK
// constraints accept the new values while still rejecting unknown ones and
// accepting the legacy values.
func TestP5MindmapTables(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	for _, table := range mindmapTables {
		if !tableExists(t, ctx, pool, table) {
			t.Errorf("table %s does not exist", table)
			continue
		}
		assertPrimaryKeyName(t, ctx, pool, table, "pk_"+table)
		assertApplicationGeneratedID(t, ctx, pool, table)
	}

	assertAppModelNodeSource(t, ctx, pool)
	assertCodeAnchorSubjectKind(t, ctx, pool)
}

// assertAppModelNodeSource proves resource_0005 widened app_model_node.source
// to include 'explored' without dropping the legacy manual/import values.
func assertAppModelNodeSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	var constraintName string
	err := pool.QueryRow(ctx, `
		SELECT conname FROM pg_constraint
		WHERE conrelid = 'app_model_node'::regclass
		  AND contype = 'c'
		  AND conname = 'ck_app_model_node_source'`).Scan(&constraintName)
	if err != nil {
		t.Fatalf("ck_app_model_node_source missing: %v", err)
	}
	var definition string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'app_model_node'::regclass AND conname = 'ck_app_model_node_source'`).Scan(&definition); err != nil {
		t.Fatalf("read ck_app_model_node_source: %v", err)
	}
	for _, value := range []string{"manual", "import", "explored"} {
		if !strings.Contains(definition, "'"+value+"'") {
			t.Errorf("ck_app_model_node_source does not allow %q: %s", value, definition)
		}
	}

	var oldConstraint int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM pg_constraint
		WHERE conrelid = 'app_model_node'::regclass
		  AND conname = 'app_model_nodes_source_check'`).Scan(&oldConstraint); err != nil {
		t.Fatalf("count legacy source constraint: %v", err)
	}
	if oldConstraint != 0 {
		t.Errorf("legacy constraint app_model_nodes_source_check still exists")
	}

	var companyID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO company (key, name) VALUES ('p5-mindmap-test', 'P5 test')
		RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("insert test company: %v", err)
	}
	var systemID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO system (company_id, key, name, kind, base_url)
		VALUES ($1, 'p5-mindmap-test', 'P5 test', 'web', 'http://example.test')
		RETURNING id`, companyID).Scan(&systemID); err != nil {
		t.Fatalf("insert test system: %v", err)
	}

	for _, source := range []string{"manual", "import", "explored"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app_model_node (system_id, kind, key, name, source)
			VALUES ($1, 'page', $2, 'P5 test', $3)`,
			systemID, "p5-source-"+source, source); err != nil {
			t.Errorf("app_model_node rejected legacy/new source %q: %v", source, err)
		}
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO app_model_node (system_id, kind, key, name, source)
		VALUES ($1, 'page', 'p5-source-unknown', 'P5 test', 'crawled')`, systemID)
	assertCheckViolation(t, err, "app_model_node.source unknown value")
}

// assertCodeAnchorSubjectKind proves codeintel_0004 widened
// code_anchor.subject_kind to include 'case' without dropping the P4 values.
func assertCodeAnchorSubjectKind(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	var constraintName string
	err := pool.QueryRow(ctx, `
		SELECT conname FROM pg_constraint
		WHERE conrelid = 'code_anchor'::regclass
		  AND contype = 'c'
		  AND conname = 'ck_code_anchor_subject_kind'`).Scan(&constraintName)
	if err != nil {
		t.Fatalf("ck_code_anchor_subject_kind missing: %v", err)
	}
	var definition string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'code_anchor'::regclass AND conname = 'ck_code_anchor_subject_kind'`).Scan(&definition); err != nil {
		t.Fatalf("read ck_code_anchor_subject_kind: %v", err)
	}
	for _, value := range []string{"version", "requirement", "app_model", "case"} {
		if !strings.Contains(definition, "'"+value+"'") {
			t.Errorf("ck_code_anchor_subject_kind does not allow %q: %s", value, definition)
		}
	}

	var oldConstraint int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM pg_constraint
		WHERE conrelid = 'code_anchor'::regclass
		  AND conname = 'code_anchors_subject_kind_check'`).Scan(&oldConstraint); err != nil {
		t.Fatalf("count legacy subject_kind constraint: %v", err)
	}
	if oldConstraint != 0 {
		t.Errorf("legacy constraint code_anchors_subject_kind_check still exists")
	}

	for i, subjectKind := range []string{"version", "requirement", "app_model", "case"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO code_anchor (id, version_id, repo_id, subject_kind, subject_id, file_path, created_by)
			VALUES ($1, 1, 1, $2, $3, $4, 1)`,
			9000+int64(i), subjectKind, int64(100+i), "src/"+subjectKind+".ts"); err != nil {
			t.Errorf("code_anchor rejected subject_kind %q: %v", subjectKind, err)
		}
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO code_anchor (id, version_id, repo_id, subject_kind, subject_id, file_path, created_by)
		VALUES (9100, 1, 1, 'issue', 200, 'src/issue.ts', 1)`)
	assertCheckViolation(t, err, "code_anchor.subject_kind unknown value")
}

// tableExists reports whether a public table exists.
func tableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

// assertPrimaryKeyName proves the table's primary key carries the D34 pk_<table>
// name instead of the PostgreSQL default <table>_pkey.
func assertPrimaryKeyName(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, want string) {
	t.Helper()
	var name string
	err := pool.QueryRow(ctx, `
		SELECT conname FROM pg_constraint
		WHERE conrelid = to_regclass($1) AND contype = 'p'`, "public."+table).Scan(&name)
	if err != nil {
		t.Errorf("%s primary key: %v", table, err)
		return
	}
	if name != want {
		t.Errorf("%s primary key name = %q, want %q", table, name, want)
	}
}

// assertApplicationGeneratedID proves the id column is a BIGINT primary key with
// no sequence/default: P5 ids are Snowflake values generated in the application
// (D29).
func assertApplicationGeneratedID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) {
	t.Helper()
	var dataType string
	var hasDefault bool
	err := pool.QueryRow(ctx, `
		SELECT data_type, column_default IS NOT NULL
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'id'`,
		table).Scan(&dataType, &hasDefault)
	if err != nil {
		t.Errorf("%s.id: %v", table, err)
		return
	}
	if dataType != "bigint" {
		t.Errorf("%s.id type = %q, want bigint", table, dataType)
	}
	if hasDefault {
		t.Errorf("%s.id has a default; P5 ids are generated by the application", table)
	}
}

// assertCheckViolation requires err to be a PostgreSQL CHECK violation (23514).
func assertCheckViolation(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: insert succeeded, want a CHECK violation", what)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: error is not a PostgreSQL error: %v", what, err)
	}
	if pgErr.Code != "23514" {
		t.Fatalf("%s: SQLSTATE = %s (%s), want 23514", what, pgErr.Code, pgErr.Message)
	}
}
