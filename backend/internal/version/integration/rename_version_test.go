//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// versionTableRenames is the D34 rename mapping of the version context (spec
// §3.2): seven tables go from plural to singular with no other change.
var versionTableRenames = []struct{ old, new string }{
	{"versions", "version"},
	{"requirements", "requirement"},
	{"requirement_relations", "requirement_relation"},
	{"requirement_attachments", "requirement_attachment"},
	{"clarification_sessions", "clarification_session"},
	{"clarification_questions", "clarification_question"},
	{"decisions", "decision"},
}

// versionIndexes are every index declared by the P3 version migrations. The
// rename migration must not rename or drop them (spec §3.3).
var versionIndexes = []string{
	"idx_versions_status_updated",
	"idx_versions_updated",
	"idx_requirements_version_status",
	"idx_requirements_version_created",
	"idx_relations_version",
	"idx_relations_from",
	"idx_relations_to",
	"uq_relations_directed",
	"uq_relations_undirected",
	"idx_attachments_requirement",
	"idx_attachments_version",
	"uq_clarification_active",
	"idx_clarification_version_started",
	"idx_questions_version_status",
	"idx_questions_requirement_status",
	"idx_decisions_version_status",
	"idx_decisions_requirement",
}

// versionConstraints are the constraint names the version repositories depend
// on: they classify PostgreSQL SQLSTATE + constraint name into domain errors.
// Because D34 renames tables only, every one of these names must survive the
// migration unchanged.
var versionConstraints = []string{
	"versions_pkey",
	"requirements_pkey",
	"requirement_relations_pkey",
	"requirement_attachments_pkey",
	"requirement_attachments_requirement_id_sha256_key",
	"clarification_sessions_pkey",
	"clarification_questions_pkey",
	"decisions_pkey",
	"requirements_version_id_fkey",
	"requirement_relations_version_id_fkey",
	"requirement_relations_from_requirement_id_fkey",
	"requirement_relations_to_requirement_id_fkey",
	"requirement_attachments_version_id_fkey",
	"requirement_attachments_requirement_id_fkey",
	"clarification_sessions_version_id_fkey",
	"clarification_questions_version_id_fkey",
	"clarification_questions_requirement_id_fkey",
	"clarification_questions_session_id_fkey",
	"decisions_version_id_fkey",
	"decisions_requirement_id_fkey",
	"decisions_question_id_fkey",
	"requirement_relations_relation_type_check",
	"clarification_questions_status_check",
	"clarification_questions_question_check",
	"clarification_questions_answered_via_check",
	"decisions_status_check",
	"decisions_topic_check",
}

// TestP5RenameVersionTables proves the version_0003 rename migration (D34):
// the seven version-context tables exist only under their singular names, the
// P3 indexes, constraints and columns are untouched, ids stay application
// generated (no sequences), and the renamed tables keep their cascading foreign
// keys.
func TestP5RenameVersionTables(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	t.Run("tables are renamed to singular names", func(t *testing.T) {
		for _, rename := range versionTableRenames {
			if !relationExists(t, ctx, pool, rename.new, "r") {
				t.Errorf("table %s does not exist", rename.new)
			}
			if relationExists(t, ctx, pool, rename.old, "r") {
				t.Errorf("legacy table %s still exists", rename.old)
			}
		}
	})

	t.Run("P3 indexes are preserved by name", func(t *testing.T) {
		for _, index := range versionIndexes {
			if !indexExists(t, ctx, pool, index) {
				t.Errorf("index %s does not exist", index)
			}
		}
	})

	t.Run("constraint names are preserved", func(t *testing.T) {
		for _, constraint := range versionConstraints {
			if !constraintExists(t, ctx, pool, constraint) {
				t.Errorf("constraint %s does not exist", constraint)
			}
		}
	})

	t.Run("columns and application generated ids are preserved", func(t *testing.T) {
		// Columns are not part of the rename mapping; spot-check the ones whose
		// loss would silently change behaviour.
		for _, column := range []struct{ table, name string }{
			{"version", "status"},
			{"requirement", "acceptance_criteria"},
			{"requirement_attachment", "storage_path"},
			{"clarification_question", "answered_via"},
			{"decision", "topic"},
		} {
			if !columnExists(t, ctx, pool, column.table, column.name) {
				t.Errorf("%s.%s does not exist", column.table, column.name)
			}
		}
		for _, rename := range versionTableRenames {
			assertApplicationGeneratedID(t, ctx, pool, rename.new)
		}
	})

	t.Run("no sequence keeps an old table name", func(t *testing.T) {
		// P3 ids are Snowflake values supplied by the application, so the rename
		// migration's ALTER SEQUENCE lines are defensive (spec §3.3). If a
		// deployment does have an owned sequence, it must be renamed with the
		// table, not left behind under the old name.
		rows, err := pool.Query(ctx, `
			SELECT c.relname
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relkind = 'S'`)
		if err != nil {
			t.Fatalf("list sequences: %v", err)
		}
		defer rows.Close()
		var stale []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan sequence: %v", err)
			}
			for _, rename := range versionTableRenames {
				if strings.HasPrefix(name, rename.old+"_") {
					stale = append(stale, name)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("list sequences: %v", err)
		}
		if len(stale) > 0 {
			t.Errorf("sequences keep old table names: %v", stale)
		}
	})

	t.Run("foreign keys still cascade through the renamed tables", func(t *testing.T) {
		// Build one full version tree through the singular names and rely on the
		// preserved ON DELETE CASCADE constraints to remove it.
		const (
			versionID     = int64(870001)
			requirementID = int64(870002)
			secondReqID   = int64(870003)
			relationID    = int64(870004)
			attachmentID  = int64(870005)
			sessionID     = int64(870006)
			questionID    = int64(870007)
			decisionID    = int64(870008)
		)
		mustExec(t, ctx, pool, `
			INSERT INTO version (id, name, created_by, updated_by)
			VALUES ($1, 'rename probe', 7, 7)`, versionID)
		mustExec(t, ctx, pool, `
			INSERT INTO requirement (id, version_id, title, created_by, updated_by)
			VALUES ($1, $3, 'first', 7, 7), ($2, $3, 'second', 7, 7)`,
			requirementID, secondReqID, versionID)
		mustExec(t, ctx, pool, `
			INSERT INTO requirement_relation
				(id, version_id, from_requirement_id, to_requirement_id, relation_type, created_by)
			VALUES ($1, $2, $3, $4, 'depends_on', 7)`,
			relationID, versionID, requirementID, secondReqID)
		mustExec(t, ctx, pool, `
			INSERT INTO requirement_attachment
				(id, version_id, requirement_id, kind, filename, mime_type, size_bytes,
				 sha256, storage_path, created_by)
			VALUES ($1, $2, $3, 'text', 'probe.txt', 'text/plain', 5,
				'`+strings.Repeat("a", 64)+`', 'requirement-attachments/870001/probe.txt', 7)`,
			attachmentID, versionID, requirementID)
		mustExec(t, ctx, pool, `
			INSERT INTO clarification_session (id, version_id, started_by)
			VALUES ($1, $2, 7)`, sessionID, versionID)
		mustExec(t, ctx, pool, `
			INSERT INTO clarification_question (id, version_id, requirement_id, question, created_by)
			VALUES ($1, $2, $3, 'What is the scope?', 7)`,
			questionID, versionID, requirementID)
		mustExec(t, ctx, pool, `
			INSERT INTO decision
				(id, version_id, requirement_id, question_id, topic, decision, status, source, proposed_by)
			VALUES ($1, $2, $3, $4, 'scope', 'checkout only', 'proposed', 'ai', 7)`,
			decisionID, versionID, requirementID, questionID)

		mustExec(t, ctx, pool, `DELETE FROM version WHERE id = $1`, versionID)

		for _, table := range []string{
			"version", "requirement", "requirement_relation", "requirement_attachment",
			"clarification_session", "clarification_question", "decision",
		} {
			if got := countRows(t, ctx, pool, table); got != 0 {
				t.Errorf("%s has %d rows after the version cascade, want 0", table, got)
			}
		}
	})
}

// relationExists reports whether a public relation of the given pg_class kind
// ("r" for table, "S" for sequence) exists.
func relationExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, kind string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1 AND c.relkind = $2
		)`, name, kind).Scan(&exists)
	if err != nil {
		t.Fatalf("check relation %s: %v", name, err)
	}
	return exists
}

// indexExists reports whether a public index exists.
func indexExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, index string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1
		)`, index).Scan(&exists)
	if err != nil {
		t.Fatalf("check index %s: %v", index, err)
	}
	return exists
}

// constraintExists reports whether a public constraint exists by name.
func constraintExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, constraint string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint con
			JOIN pg_class c ON c.oid = con.conrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND con.conname = $1
		)`, constraint).Scan(&exists)
	if err != nil {
		t.Fatalf("check constraint %s: %v", constraint, err)
	}
	return exists
}

// columnExists reports whether a public table has the column.
func columnExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, column string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
		)`, table, column).Scan(&exists)
	if err != nil {
		t.Fatalf("check column %s.%s: %v", table, column, err)
	}
	return exists
}

// assertApplicationGeneratedID proves the id column is a BIGINT primary key
// with no sequence/default: P3 ids are Snowflake values generated by the
// application.
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
		t.Errorf("%s.id has a default; ids are generated by the application", table)
	}
}

// mustExec runs one statement and fails the test on error.
func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", strings.SplitN(sql, "\n", 2)[0], err)
	}
}

// countRows returns the row count of one public table.
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM `+pgxIdentifier(table)).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// pgxIdentifier quotes one identifier for interpolation into a count query.
func pgxIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
