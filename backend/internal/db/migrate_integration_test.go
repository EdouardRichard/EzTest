//go:build integration

package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func TestMigrateAppliesAllTablesAndIsIdempotent(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 33 {
		t.Fatalf("applied %d migrations, want 33: %v", len(applied), applied)
	}

	again, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Migrate applied %v", again)
	}

	expected := []string{
		"app_user", "auth_token", "audit_log", "code_repository", "content_object",
		"content_ref", "event", "event_channel", "llm_config", "llm_model",
		"schema_migrations", "sut", "sut_api", "sut_environment", "sut_page",
		"system_setting", "system_setting_revision", "target_database", "test_account",
		"version", "version_sut", "version_sut_resource",
		"work_item", "work_item_attempt", "team_room_post", "team_room_digest",
		"agent_profile", "agent_run", "agent_message", "context_packet",
		"requirement", "requirement_version", "requirement_confirmation", "requirement_relation",
		"conversation", "conversation_message", "conversation_summary", "requirement_attachment",
		"case_scenario", "case_entity", "case_version", "case_step", "case_assertion",
		"case_source", "case_confirmation", "design_round", "design_scope", "export_artifact",
		"execution_intent", "run_wave", "case_run", "case_run_sut_context",
		"step_result", "assertion_result", "evidence", "evidence_ref",
		"executor_instance", "human_request",
		"delivery_issue", "delivery_issue_ref", "delivery_issue_status_history",
		"delivery_report", "delivery_report_revision", "delivery_report_ref",
		"delivery_analysis_run", "requirement_closure",
		"code_index_run", "code_entity", "code_edge", "code_fact", "code_analysis",
		"code_impact_candidate", "code_anchor", "code_fact_review",
		"visual_assertion", "visual_baseline", "visual_diff",
		"eval_run", "eval_metric", "agent_profile_prompt_revision",
	}
	found := map[string]bool{}
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
	`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	for _, name := range expected {
		if !found[name] {
			t.Fatalf("missing table %s (found %v)", name, found)
		}
	}
}

func TestP2SchemaNames(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	constraints := []string{
		"pk_case_scenario", "pk_case_entity", "pk_case_version", "pk_case_step",
		"pk_case_assertion", "pk_case_source", "pk_case_confirmation",
		"pk_design_round", "pk_design_scope", "pk_export_artifact",
		"uk_case_scenario_requirement_version_name",
		"uk_case_version_case_revision",
		"uk_case_step_version_seq",
		"uk_case_confirmation_version",
		"uk_design_round_conversation",
		"uk_design_scope_version_requirement",
		"uk_export_artifact_version_owner_file_format_revision",
	}
	rows, err := pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conname = ANY($1)`, constraints)
	if err != nil {
		t.Fatalf("query P2 constraints: %v", err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints: %v", err)
	}
	for _, name := range constraints {
		if !found[name] {
			t.Errorf("missing P2 constraint %s", name)
		}
	}

	indexes := []string{
		"idx_case_scenario_requirement", "idx_case_scenario_requirement_version",
		"idx_case_entity_scenario", "idx_case_entity_current", "idx_case_entity_status",
		"idx_case_version_case", "idx_case_version_status",
		"idx_case_step_version", "idx_case_assertion_version", "idx_case_source_version",
		"idx_case_confirmation_batch", "idx_case_confirmation_actor",
		"idx_design_round_version", "idx_design_round_status", "idx_design_scope_requirement",
		"idx_export_artifact_version", "idx_export_artifact_created_at",
	}
	rows, err = pool.Query(ctx, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = ANY($1)
	`, indexes)
	if err != nil {
		t.Fatalf("query P2 indexes: %v", err)
	}
	found = map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	for _, name := range indexes {
		if !found[name] {
			t.Errorf("missing P2 index %s", name)
		}
	}

	var oldNames int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name IN ('scenario', 'export')
	`).Scan(&oldNames); err != nil {
		t.Fatalf("count legacy table names: %v", err)
	}
	if oldNames != 0 {
		t.Fatalf("legacy table names present: %d", oldNames)
	}
}

func TestP3ExecutionTables(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()
	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 33 {
		t.Fatalf("applied %d migrations, want 33: %v", len(applied), applied)
	}

	tables := []string{
		"execution_intent", "run_wave", "case_run", "case_run_sut_context",
		"step_result", "assertion_result", "evidence", "evidence_ref",
		"executor_instance", "human_request",
	}
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		  AND table_name = ANY($1)
	`, tables)
	if err != nil {
		t.Fatalf("query P3 tables: %v", err)
	}
	foundTables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		foundTables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	for _, name := range tables {
		if !foundTables[name] {
			t.Errorf("missing P3 table %s", name)
		}
	}

	constraints := []string{
		"pk_execution_intent", "pk_run_wave", "pk_case_run", "pk_case_run_sut_context",
		"pk_step_result", "pk_assertion_result", "pk_evidence", "pk_evidence_ref",
		"pk_executor_instance", "pk_human_request",
		"uk_case_run_wave_intent", "uk_case_run_sut_context", "uk_step_result_run_seq",
		"uk_assertion_result_run_step_assertion", "uk_evidence_ref_owner",
		"uk_executor_instance_endpoint",
	}
	rows, err = pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conname = ANY($1)`, constraints)
	if err != nil {
		t.Fatalf("query P3 constraints: %v", err)
	}
	foundConstraints := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		foundConstraints[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints: %v", err)
	}
	for _, name := range constraints {
		if !foundConstraints[name] {
			t.Errorf("missing P3 constraint %s", name)
		}
	}

	// Partial unique indexes are indexes, not constraints (execution intent
	// active key and single-active-wave invariant).
	partialIndexes := []string{"uk_execution_intent_active", "uk_run_wave_active"}
	rows, err = pool.Query(ctx, `
		SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = ANY($1)
	`, partialIndexes)
	if err != nil {
		t.Fatalf("query P3 partial unique indexes: %v", err)
	}
	defs := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		defs[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate partial indexes: %v", err)
	}
	for _, name := range partialIndexes {
		definition, ok := defs[name]
		if !ok {
			t.Errorf("missing P3 partial unique index %s", name)
			continue
		}
		if !strings.Contains(definition, "UNIQUE INDEX") {
			t.Errorf("%s is not a unique index: %s", name, definition)
		}
		if !strings.Contains(definition, "WHERE") {
			t.Errorf("%s is not partial: %s", name, definition)
		}
	}

	indexes := []string{
		"idx_execution_intent_version_status", "idx_execution_intent_case_version",
		"idx_execution_intent_dedupe", "idx_run_wave_version", "idx_run_wave_status",
		"idx_case_run_wave", "idx_case_run_intent", "idx_case_run_case_version",
		"idx_case_run_lease", "idx_case_run_sut_context_run", "idx_case_run_sut_context_context",
		"idx_step_result_run", "idx_assertion_result_run",
		"idx_evidence_case_run", "idx_evidence_version_sut", "idx_evidence_content_ref",
		"idx_evidence_kind", "idx_evidence_ref_owner", "idx_executor_instance_status",
		"idx_human_request_version_status", "idx_human_request_case_run", "idx_human_request_expiry",
	}
	rows, err = pool.Query(ctx, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = ANY($1)
	`, indexes)
	if err != nil {
		t.Fatalf("query P3 indexes: %v", err)
	}
	foundIndexes := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		foundIndexes[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	for _, name := range indexes {
		if !foundIndexes[name] {
			t.Errorf("missing P3 index %s", name)
		}
	}
}

func TestP4DeliverySchema(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 33 {
		t.Fatalf("applied %d migrations, want 33: %v", len(applied), applied)
	}
	again, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Migrate applied %v", again)
	}

	tables := []string{
		"delivery_issue", "delivery_issue_ref", "delivery_issue_status_history",
		"delivery_report", "delivery_report_revision", "delivery_report_ref",
		"delivery_analysis_run", "requirement_closure",
	}
	for _, name := range tables {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatalf("to_regclass(%s): %v", name, err)
		}
		if !exists {
			t.Errorf("missing P4 table %s", name)
		}
	}

	// D34: every P4 table carries both audit columns.
	for _, name := range tables {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1
			  AND column_name IN ('created_at', 'updated_at')
			  AND data_type = 'timestamp with time zone'
			  AND is_nullable = 'NO'
			  AND column_default LIKE 'now()'
		`, name).Scan(&count); err != nil {
			t.Fatalf("audit columns for %s: %v", name, err)
		}
		if count != 2 {
			t.Errorf("%s audit columns = %d, want created_at/updated_at", name, count)
		}
	}

	constraints := []string{
		"pk_delivery_issue", "pk_delivery_issue_ref", "pk_delivery_issue_status_history",
		"pk_delivery_report", "pk_delivery_report_revision", "pk_delivery_report_ref",
		"pk_delivery_analysis_run", "pk_requirement_closure",
		"uk_delivery_issue_version_id_dedupe_key",
		"uk_delivery_issue_ref_issue_id_kind_ref_id_role",
		"uk_delivery_report_revision_report_id_revision",
		"uk_delivery_report_ref_report_revision_id_kind_ref_id",
		"uk_requirement_closure_requirement_id_close_round",
	}
	rows, err := pool.Query(ctx, `
		SELECT c.conname
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND c.conname = ANY($1)
	`, constraints)
	if err != nil {
		t.Fatalf("query P4 constraints: %v", err)
	}
	foundConstraints := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		foundConstraints[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints: %v", err)
	}
	for _, name := range constraints {
		if !foundConstraints[name] {
			t.Errorf("missing P4 constraint %s", name)
		}
	}

	indexes := []string{
		"idx_delivery_issue_version_id_status_id", "idx_delivery_issue_merged_into_id",
		"idx_delivery_issue_created_at", "idx_delivery_issue_ref_kind_ref_id",
		"idx_delivery_issue_ref_issue_id_id", "idx_delivery_issue_status_history_issue_id_id",
		"idx_delivery_report_version_id_kind_id", "idx_delivery_report_current_revision_id",
		"idx_delivery_report_revision_report_id_revision", "idx_delivery_report_revision_html_ref_id",
		"idx_delivery_report_ref_kind_ref_id", "idx_delivery_report_ref_report_id_id",
		"idx_delivery_analysis_run_version_id_kind_id",
		"idx_requirement_closure_requirement_id_id", "idx_requirement_closure_requirement_version_id",
	}
	// Partial unique indexes are indexes, not constraints.
	partialUnique := []string{
		"uk_delivery_report_version_id_wave_id", "uk_delivery_report_version_id_kind",
		"uk_delivery_analysis_run_version_id_kind_dedupe_key",
		"uk_delivery_analysis_run_agent_run_id",
	}
	rows, err = pool.Query(ctx, `
		SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = ANY($1)
	`, append(append([]string{}, indexes...), partialUnique...))
	if err != nil {
		t.Fatalf("query P4 indexes: %v", err)
	}
	defs := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		defs[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	for _, name := range indexes {
		if _, ok := defs[name]; !ok {
			t.Errorf("missing P4 index %s", name)
		}
	}
	for _, name := range partialUnique {
		definition, ok := defs[name]
		if !ok {
			t.Errorf("missing P4 partial unique index %s", name)
			continue
		}
		if !strings.Contains(definition, "UNIQUE INDEX") {
			t.Errorf("%s is not a unique index: %s", name, definition)
		}
		if !strings.Contains(definition, "WHERE") {
			t.Errorf("%s is not partial: %s", name, definition)
		}
	}

	// Cross-context references are logical BIGINT only: the only foreign keys on
	// P4 tables stay inside delivery or point at requirement/requirement_version.
	allowedTargets := map[string]bool{
		"delivery_issue": true, "delivery_issue_ref": true,
		"delivery_issue_status_history": true, "delivery_report": true,
		"delivery_report_revision": true, "delivery_report_ref": true,
		"delivery_analysis_run": true, "requirement_closure": true,
		"requirement": true, "requirement_version": true,
	}
	rows, err = pool.Query(ctx, `
		SELECT c.conname, t.relname, rt.relname
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_class rt ON rt.oid = c.confrelid
		WHERE n.nspname = current_schema()
		  AND c.contype = 'f'
		  AND t.relname::text = ANY($1)
		ORDER BY c.conname
	`, tables)
	if err != nil {
		t.Fatalf("query P4 foreign keys: %v", err)
	}
	foreignKeys := 0
	for rows.Next() {
		var name, source, target string
		if err := rows.Scan(&name, &source, &target); err != nil {
			t.Fatalf("scan foreign key: %v", err)
		}
		foreignKeys++
		if !allowedTargets[target] {
			t.Errorf("foreign key %s on %s targets cross-context table %s", name, source, target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys: %v", err)
	}
	if foreignKeys != 8 {
		t.Errorf("P4 foreign keys = %d, want 8", foreignKeys)
	}

	// Same-context cascades: refs/history follow their issue, revisions and refs
	// follow their report, closure follows its requirement version.
	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_issue (id, version_id, title, category, severity, dedupe_key)
		VALUES (1, 1, 'issue', 'ui', 'major', repeat('a', 32))
	`); err != nil {
		t.Fatalf("insert delivery issue: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_issue_ref (id, issue_id, kind, ref_id)
		VALUES (2, 1, 'case_run', 9)
	`); err != nil {
		t.Fatalf("insert delivery issue ref: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_issue_status_history (id, issue_id, to_status, actor_kind)
		VALUES (3, 1, 'open', 'system')
	`); err != nil {
		t.Fatalf("insert delivery issue status history: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM delivery_issue WHERE id = 1`); err != nil {
		t.Fatalf("delete delivery issue: %v", err)
	}
	for _, name := range []string{"delivery_issue_ref", "delivery_issue_status_history"} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+name).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Errorf("%s rows after issue delete = %d, want 0", name, count)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_report (id, version_id, kind) VALUES (10, 1, 'version')
	`); err != nil {
		t.Fatalf("insert delivery report: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_report_revision (id, report_id, revision, input_fingerprint, as_of)
		VALUES (11, 10, 1, repeat('b', 64), now())
	`); err != nil {
		t.Fatalf("insert delivery report revision: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_report_ref (id, report_id, report_revision_id, kind, ref_id)
		VALUES (12, 10, 11, 'issue', 5)
	`); err != nil {
		t.Fatalf("insert delivery report ref: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM delivery_report WHERE id = 10`); err != nil {
		t.Fatalf("delete delivery report: %v", err)
	}
	for _, name := range []string{"delivery_report_revision", "delivery_report_ref"} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+name).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Errorf("%s rows after report delete = %d, want 0", name, count)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, created_by) VALUES (20, 'v', 1)
	`); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, status) VALUES (21, 20, 'draft')
	`); err != nil {
		t.Fatalf("insert requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES (22, 21, 1, 'r', repeat('c', 64))
	`); err != nil {
		t.Fatalf("insert requirement version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_closure (id, requirement_id, requirement_version_id, close_round, check_result)
		VALUES (23, 21, 22, 1, '{}'::jsonb)
	`); err != nil {
		t.Fatalf("insert requirement closure: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM requirement_version WHERE id = 22`); err != nil {
		t.Fatalf("delete requirement version: %v", err)
	}
	var closureRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM requirement_closure`).Scan(&closureRows); err != nil {
		t.Fatalf("count requirement closure: %v", err)
	}
	if closureRows != 0 {
		t.Errorf("requirement_closure rows after requirement version delete = %d, want 0", closureRows)
	}
}

// TestP6QualityPromptSchema pins the two P6 migrations: eval tables (#32) and
// append-only prompt revisions (#33).
func TestP6QualityPromptSchema(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	tables := []string{"eval_run", "eval_metric", "agent_profile_prompt_revision"}
	for _, name := range tables {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatalf("to_regclass(%s): %v", name, err)
		}
		if !exists {
			t.Errorf("missing P6 table %s", name)
		}
	}

	constraints := []string{
		"pk_eval_run", "pk_eval_metric", "fk_eval_metric_run",
		"uk_agent_profile_prompt_revision",
	}
	rows, err := pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conname = ANY($1)`, constraints)
	if err != nil {
		t.Fatalf("query P6 constraints: %v", err)
	}
	foundConstraints := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		foundConstraints[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints: %v", err)
	}
	for _, name := range constraints {
		if !foundConstraints[name] {
			t.Errorf("missing P6 constraint %s", name)
		}
	}

	indexes := []string{
		"idx_eval_run_created_at", "idx_eval_metric_run",
		"idx_agent_profile_prompt_revision_key",
	}
	rows, err = pool.Query(ctx, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = ANY($1)
	`, indexes)
	if err != nil {
		t.Fatalf("query P6 indexes: %v", err)
	}
	foundIndexes := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		foundIndexes[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	for _, name := range indexes {
		if !foundIndexes[name] {
			t.Errorf("missing P6 index %s", name)
		}
	}
}

func TestMigrateRejectsChangedChecksum(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE schema_migrations SET checksum = 'changed' WHERE version = 'settings_0001_settings.sql'`,
	); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	_, err := db.Migrate(ctx, pool)
	if !errors.Is(err, db.ErrMigrationChecksum) {
		t.Fatalf("Migrate error = %v, want ErrMigrationChecksum", err)
	}
}

func TestSchemaConstraints(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	insertUser := func(id int64, username, role string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO app_user (id, username, display_name, password_hash, role)
			VALUES ($1, $2, $2, 'hash', $3)
		`, id, username, role)
		return err
	}
	if err := insertUser(1, "admin", "admin"); err != nil {
		t.Fatalf("insert admin: %v", err)
	}
	if err := insertUser(2, "second-admin", "admin"); err == nil {
		t.Fatal("second admin accepted (single-admin index missing)")
	}
	if err := insertUser(3, "Admin", "user"); err == nil {
		t.Fatal("case-insensitive username uniqueness missing")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO sut (id, name, created_by) VALUES (10, 'Order', 1)`); err != nil {
		t.Fatalf("insert sut: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sut (id, name, created_by) VALUES (11, 'order', 1)`); err == nil {
		t.Fatal("case-insensitive sut name uniqueness missing")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_environment (id, sut_id, name, base_url, is_default)
		VALUES (20, 10, 'dev', 'http://localhost:8080', true)
	`); err != nil {
		t.Fatalf("insert environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_environment (id, sut_id, name, base_url, is_default)
		VALUES (21, 10, 'dev2', 'http://localhost:8081', true)
	`); err == nil {
		t.Fatal("second default environment accepted")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, created_by) VALUES (30, 'v1', 1)
	`); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version_sut (id, version_id, sut_id, environment_id)
		VALUES (40, 30, 10, 20)
	`); err != nil {
		t.Fatalf("insert version_sut: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version_sut (id, version_id, sut_id, environment_id)
		VALUES (41, 30, 10, 20)
	`); err == nil {
		t.Fatal("duplicate version+sut accepted")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO event_channel (channel) VALUES ('system')
	`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event (channel, seq, type) VALUES ('system', 1, 'test')
	`); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event (channel, seq, type) VALUES ('system', 1, 'duplicate')
	`); err == nil {
		t.Fatal("duplicate (channel, seq) accepted")
	}

	// P1 constraints: task dedupe, requirement revisions, one confirmation per
	// requirement version, and conversation client-message idempotency.
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key)
		VALUES (50, 30, 'clarification', 'chat.turn', 'message:1')
	`); err != nil {
		t.Fatalf("insert work item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key)
		VALUES (51, 30, 'clarification', 'chat.turn', 'message:1')
	`); err == nil {
		t.Fatal("duplicate work item dedupe key accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, status) VALUES (60, 30, 'draft')
	`); err != nil {
		t.Fatalf("insert requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES (70, 60, 1, 'r1', repeat('a', 64))
	`); err != nil {
		t.Fatalf("insert requirement version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES (71, 60, 1, 'r1-dup', repeat('b', 64))
	`); err == nil {
		t.Fatal("duplicate (requirement_id, revision) accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_confirmation (id, requirement_version_id, scope, actor_id)
		VALUES (80, 70, 'requirement', 1)
	`); err != nil {
		t.Fatalf("insert requirement confirmation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_confirmation (id, requirement_version_id, scope, actor_id)
		VALUES (81, 70, 'requirement', 1)
	`); err == nil {
		t.Fatal("duplicate confirmation for a requirement version accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversation (id, version_id, kind, title) VALUES (90, 30, 'clarification', 'c1')
	`); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversation_message (id, conversation_id, role, client_message_id)
		VALUES (100, 90, 'user', 'client-1')
	`); err != nil {
		t.Fatalf("insert conversation message: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversation_message (id, conversation_id, role, client_message_id)
		VALUES (101, 90, 'user', 'client-1')
	`); err == nil {
		t.Fatal("duplicate conversation client_message_id accepted")
	}

	// P2 constraints: scenario natural key, case version revision, step seq,
	// confirmation uniqueness, design scope/round uniqueness, export revision.
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES (110, 30, 60, 70, 'happy path')
	`); err != nil {
		t.Fatalf("insert case scenario: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES (111, 30, 60, 70, 'happy path')
	`); err == nil {
		t.Fatal("duplicate case scenario natural key accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, status) VALUES (120, 110, 'draft')
	`); err != nil {
		t.Fatalf("insert case entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, content_hash)
		VALUES (130, 120, 1, 'case one', repeat('c', 64))
	`); err != nil {
		t.Fatalf("insert case version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, content_hash)
		VALUES (131, 120, 1, 'case one dup', repeat('e', 64))
	`); err == nil {
		t.Fatal("duplicate case version revision accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_step (id, case_version_id, seq, action) VALUES (140, 130, 1, 'open')
	`); err != nil {
		t.Fatalf("insert case step: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_step (id, case_version_id, seq, action) VALUES (141, 130, 1, 'open again')
	`); err == nil {
		t.Fatal("duplicate case step seq accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_confirmation (id, case_version_id, scope, target_scope_id, batch_id, confirmed_by)
		VALUES (150, 130, 'case', 120, 'batch-1', 1)
	`); err != nil {
		t.Fatalf("insert case confirmation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_confirmation (id, case_version_id, scope, target_scope_id, batch_id, confirmed_by)
		VALUES (151, 130, 'case', 120, 'batch-2', 1)
	`); err == nil {
		t.Fatal("duplicate case confirmation accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO design_round (id, version_id, conversation_id, activation)
		VALUES (160, 30, 90, 'manual')
	`); err != nil {
		t.Fatalf("insert design round: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO design_round (id, version_id, conversation_id, activation)
		VALUES (161, 30, 90, 'auto_new')
	`); err == nil {
		t.Fatal("duplicate design round conversation accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO design_scope (id, version_id, requirement_id, activated_by_round_id)
		VALUES (170, 30, 60, 160)
	`); err != nil {
		t.Fatalf("insert design scope: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO design_scope (id, version_id, requirement_id, activated_by_round_id)
		VALUES (171, 30, 60, 160)
	`); err == nil {
		t.Fatal("duplicate design scope accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO export_artifact (
			id, version_id, owner_kind, owner_id, revision, file_format,
			content_object_id, size_bytes, mime, filename
		) VALUES (180, 30, 'case', 30, 1, 'markdown', repeat('d', 64), 1, 'text/markdown', 'case.md')
	`); err != nil {
		t.Fatalf("insert export artifact: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO export_artifact (
			id, version_id, owner_kind, owner_id, revision, file_format,
			content_object_id, size_bytes, mime, filename
		) VALUES (181, 30, 'case', 30, 1, 'markdown', repeat('f', 64), 1, 'text/markdown', 'case2.md')
	`); err == nil {
		t.Fatal("duplicate export artifact revision accepted")
	}
}
