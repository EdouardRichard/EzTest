//go:build integration

package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateAppliesAllTablesAndIsIdempotent(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 62 {
		t.Fatalf("applied %d migrations, want 62: %v", len(applied), applied)
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
		"sut_repository", "version_repository_override", "version_account",
		"clarification_handoff",
		"work_item", "work_item_attempt", "team_room_post", "team_room_digest",
		"agent_profile", "agent_run", "agent_message", "context_packet",
		"agent_activity",
		"requirement", "requirement_version", "requirement_confirmation", "requirement_relation",
		"requirement_extraction_run", "requirement_extraction_segment",
		"conversation", "conversation_message", "conversation_summary", "requirement_attachment",
		"attachment_image",
		"case_scenario", "case_entity", "case_version", "case_step", "case_assertion",
		"case_source", "case_confirmation", "design_round", "design_scope", "export_artifact",
		"execution_intent", "run_wave", "case_run", "case_run_sut_context",
		"execution_discovery_observation",
		"step_result", "assertion_result", "evidence", "evidence_ref",
		"executor_instance", "human_request",
		"delivery_issue", "delivery_issue_ref", "delivery_issue_status_history",
		"delivery_report", "delivery_report_revision", "delivery_report_ref",
		"delivery_analysis_run", "requirement_closure",
		"code_index_run", "code_entity", "code_edge", "code_fact", "code_analysis",
		"code_impact_candidate", "code_anchor", "code_fact_review",
		"visual_assertion", "visual_baseline", "visual_diff",
		"eval_run", "eval_metric", "agent_profile_prompt_revision",
		"knowledge_item", "knowledge_revision", "knowledge_usage",
		"gc_run", "gc_tombstone", "api_instance",
		"version_plan_item", "version_fact", "conversation_message_part",
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
	if len(applied) != 62 {
		t.Fatalf("applied %d migrations, want 62: %v", len(applied), applied)
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
	if len(applied) != 62 {
		t.Fatalf("applied %d migrations, want 62: %v", len(applied), applied)
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
		"ck_delivery_issue_source",
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

// TestP7P9Schema pins the seven P7 migrations (#34-#40): knowledge, GC ledger,
// report PDF metadata, API instances, SUT family, issue recurrence and the
// deterministic ops work-item team.
func TestP7P9Schema(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	tables := []string{
		"knowledge_item", "knowledge_revision", "knowledge_usage",
		"gc_run", "gc_tombstone", "api_instance",
	}
	for _, name := range tables {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatalf("to_regclass(%s): %v", name, err)
		}
		if !exists {
			t.Errorf("missing P7 table %s", name)
		}
	}

	// Column extensions on pre-existing tables: report PDF metadata, issue
	// recurrence counters, the v0.22 retest model and the optional SUT family.
	columns := []struct {
		table      string
		column     string
		dataType   string
		nullable   string
		defaultSQL string
	}{
		{"delivery_report_revision", "pdf_status", "text", "NO", "'pending'::text"},
		{"delivery_report_revision", "pdf_error", "text", "YES", ""},
		{"delivery_report_revision", "pdf_attempts", "integer", "NO", "0"},
		{"delivery_report_revision", "pdf_rendered_at", "timestamp with time zone", "YES", ""},
		{"delivery_issue", "recurrence_count", "integer", "NO", "0"},
		{"delivery_issue", "last_recurred_at", "timestamp with time zone", "YES", ""},
		{"delivery_issue", "retest_status", "text", "NO", "'not_retested'::text"},
		{"delivery_issue", "last_retest_wave_id", "bigint", "YES", ""},
		{"delivery_issue", "last_retest_case_run_id", "bigint", "YES", ""},
		{"sut", "family", "text", "YES", ""},
	}
	for _, column := range columns {
		var dataType, nullable string
		var defaultValue *string
		if err := pool.QueryRow(ctx, `
			SELECT data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1 AND column_name = $2
		`, column.table, column.column).Scan(&dataType, &nullable, &defaultValue); err != nil {
			t.Errorf("missing P7 column %s.%s: %v", column.table, column.column, err)
			continue
		}
		if dataType != column.dataType {
			t.Errorf("%s.%s type = %s, want %s", column.table, column.column, dataType, column.dataType)
		}
		if nullable != column.nullable {
			t.Errorf("%s.%s nullable = %s, want %s", column.table, column.column, nullable, column.nullable)
		}
		if column.defaultSQL != "" && (defaultValue == nil || *defaultValue != column.defaultSQL) {
			t.Errorf("%s.%s default = %v, want %s", column.table, column.column, defaultValue, column.defaultSQL)
		}
	}

	constraints := []string{
		"pk_knowledge_item", "ck_knowledge_item_scope",
		"pk_knowledge_revision", "uk_knowledge_revision_knowledge_id_revision",
		"pk_knowledge_usage", "pk_gc_run", "pk_gc_tombstone", "pk_api_instance",
		"uk_api_instance_instance_key", "uk_api_instance_node_id",
	}
	rows, err := pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conname = ANY($1)`, constraints)
	if err != nil {
		t.Fatalf("query P7 constraints: %v", err)
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
			t.Errorf("missing P7 constraint %s", name)
		}
	}

	indexes := []string{
		"idx_knowledge_item_scope_status_id", "idx_knowledge_item_status_id",
		"idx_knowledge_item_source_version", "idx_knowledge_item_search",
		"idx_knowledge_revision_knowledge_id_id", "idx_knowledge_usage_knowledge_id_id",
		"idx_content_ref_live_object", "idx_content_object_created_at",
		"idx_gc_tombstone_run_id", "idx_delivery_report_revision_pdf_pending",
		"idx_sut_family", "idx_api_instance_status_heartbeat",
		"uk_gc_run_running",
	}
	rows, err = pool.Query(ctx, `
		SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = ANY($1)
	`, indexes)
	if err != nil {
		t.Fatalf("query P7 indexes: %v", err)
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
			t.Errorf("missing P7 index %s", name)
		}
	}
	// One active GC run at a time: partial unique index on a constant expression.
	if definition := defs["uk_gc_run_running"]; !strings.Contains(definition, "UNIQUE INDEX") ||
		!strings.Contains(definition, "WHERE (status = 'running'::text)") {
		t.Errorf("uk_gc_run_running is not a partial unique index: %s", definition)
	}

	// #40: deterministic worker tasks (report.render_pdf) own the ops team so the
	// generic agent worker (five product teams) can never claim them.
	var teamCheck string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'work_item'::regclass AND conname = 'chk_work_item_team'
	`).Scan(&teamCheck); err != nil {
		t.Fatalf("read chk_work_item_team: %v", err)
	}
	for _, team := range []string{"clarification", "design", "execution", "delivery", "steward", "code", "ops"} {
		if !strings.Contains(teamCheck, "'"+team+"'") {
			t.Errorf("chk_work_item_team does not allow %q: %s", team, teamCheck)
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

func TestP8HardeningSchema(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 62 {
		t.Fatalf("applied %d migrations, want 62: %v", len(applied), applied)
	}

	columns := []struct {
		table  string
		column string
	}{
		{"api_instance", "active_requests"},
		{"api_instance", "active_work"},
		{"case_run_sut_context", "executor_id"},
	}
	for _, tc := range columns {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
			)
		`, tc.table, tc.column).Scan(&exists); err != nil {
			t.Fatalf("query %s.%s: %v", tc.table, tc.column, err)
		}
		if !exists {
			t.Errorf("missing P8 column %s.%s", tc.table, tc.column)
		}
	}

	constraints := []string{
		"ck_api_instance_active_requests",
		"ck_api_instance_active_work",
		"ck_case_run_sut_context_executor",
	}
	for _, name := range constraints {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint WHERE conname = $1
			)
		`, name).Scan(&exists); err != nil {
			t.Fatalf("query constraint %s: %v", name, err)
		}
		if !exists {
			t.Errorf("missing P8 constraint %s", name)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO api_instance (id, instance_key, hostname, pid, api_port, node_id, status, version, started_at, heartbeat_at)
		VALUES (900001, 'p8:1', 'p8', 1, 13333, 1, 'active', '0.2.0-p8', now(), now())
	`); err != nil {
		t.Fatalf("insert api_instance: %v", err)
	}
	var requests, work int
	if err := pool.QueryRow(ctx, `SELECT active_requests, active_work FROM api_instance WHERE id = 900001`).Scan(&requests, &work); err != nil {
		t.Fatalf("read defaults: %v", err)
	}
	if requests != 0 || work != 0 {
		t.Fatalf("api_instance inflight defaults = %d/%d, want 0/0", requests, work)
	}
}

// TestV022ABSchema pins the A/B core schema of v0.22: model parameter
// provenance, the single-writer conversation lease, message kinds, summary
// watermarks, run telemetry, the per-run activity trace and requirement
// extraction run/segment bookkeeping.
// See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-1..DB-9,
// docs/version-V0.22/B-clarification-messaging.md §5.1 and
// docs/version-V0.22/A-model-token-budget.md §4.2, §5.2, §6.4, §7.3.
func TestV022ABSchema(t *testing.T) {
	pool := integrationDB(t)

	for _, table := range []string{
		"agent_activity",
		"requirement_extraction_run",
		"requirement_extraction_segment",
	} {
		requireTable(t, pool, table)
	}

	for _, c := range []struct{ table, column string }{
		// DB-1: catalog/manual provenance per model parameter.
		{"llm_model", "window_source"},
		{"llm_model", "output_source"},
		// DB-2: single active turn and its lease.
		{"conversation", "active_turn_id"},
		{"conversation", "active_agent"},
		{"conversation", "lease_until"},
		// DB-3: message author and kind.
		{"conversation_message", "author_agent"},
		{"conversation_message", "kind"},
		// DB-4: compaction watermark and metadata.
		{"conversation_summary", "covered_message_id"},
		{"conversation_summary", "compaction_id"},
		{"conversation_summary", "summary_meta"},
		// DB-5: run telemetry (model/config_id exist since 0.2.1).
		{"agent_run", "cached_tokens"},
		{"agent_run", "duration_ms"},
		// DB-6: run activity trace.
		{"agent_activity", "id"},
		{"agent_activity", "run_id"},
		{"agent_activity", "type"},
		{"agent_activity", "title"},
		{"agent_activity", "started_at"},
		{"agent_activity", "ended_at"},
		{"agent_activity", "payload"},
		{"agent_activity", "error"},
		// DB-7: requirement extraction runs.
		{"requirement_extraction_run", "version_id"},
		{"requirement_extraction_run", "attachment_id"},
		{"requirement_extraction_run", "input_sha256"},
		{"requirement_extraction_run", "status"},
		{"requirement_extraction_run", "total_segments"},
		{"requirement_extraction_run", "done_segments"},
		{"requirement_extraction_run", "failed_segments"},
		{"requirement_extraction_run", "expected_count"},
		{"requirement_extraction_run", "extracted_count"},
		{"requirement_extraction_run", "error"},
		// R-A7-8 / Lead ruling C12 (version_0013): the full reconciliation
		// report object persisted for the progress API (58/58 current plus the
		// separate V2 planned partition).
		{"requirement_extraction_run", "report"},
		// DB-8: extraction segments / cursor.
		{"requirement_extraction_segment", "run_id"},
		{"requirement_extraction_segment", "seq"},
		{"requirement_extraction_segment", "heading_path"},
		{"requirement_extraction_segment", "char_start"},
		{"requirement_extraction_segment", "char_end"},
		{"requirement_extraction_segment", "text_sha256"},
		{"requirement_extraction_segment", "status"},
		{"requirement_extraction_segment", "attempts"},
		{"requirement_extraction_segment", "extracted_count"},
		{"requirement_extraction_segment", "renewed_at"},
		// DB-9: requirement source and reconciliation.
		{"requirement", "extraction_run_id"},
		{"requirement", "source_quote"},
		{"requirement", "external_id"},
		{"requirement", "confidence"},
		// DB-20 / R-A8-2: optimistic-lock revision of the plan write path
		// (Lead-approved additive column, version_0012).
		{"version_plan_item", "revision"},
		// Task 20 follow-up (version_0014): pre-registered message attachments
		// carry their owning conversation, so an unbound draft stays scoped to
		// that conversation and never leaks into another conversation of the
		// same version. NULL stays valid for requirement-scoped and legacy rows.
		{"requirement_attachment", "conversation_id"},
	} {
		requireColumn(t, pool, c.table, c.column)
	}

	// The plan revision backs the R-A8-2 optimistic lock: every stored row has
	// one, and rows written without a revision (the pre-v0.22 write path)
	// start at 1.
	requireColumnNotNull(t, pool, "version_plan_item", "revision")
	requireColumnDefault(t, pool, "version_plan_item", "revision", "1")

	// R-A7-8 / Lead ruling C12: report is a NOT NULL JSON object with an empty
	// object default, so pre-v0.22 rows and a run that never reconciled read
	// back as "no report yet" instead of NULL.
	requireColumnNotNull(t, pool, "requirement_extraction_run", "report")
	requireColumnDefault(t, pool, "requirement_extraction_run", "report", "'{}'::jsonb")
	var reportCheck string
	if err := pool.QueryRow(context.Background(), `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'chk_requirement_extraction_run_report'
		  AND contype = 'c'
		  AND connamespace = current_schema()::regnamespace
	`).Scan(&reportCheck); err != nil {
		t.Errorf("read chk_requirement_extraction_run_report: %v", err)
	} else if !strings.Contains(reportCheck, "jsonb_typeof(report) = 'object'") {
		t.Errorf("requirement_extraction_run.report CHECK is not an object check: %s", reportCheck)
	}

	// D34 / Agent.md §6.9.5: the three new tables name their primary-key
	// constraints pk_<table> instead of PostgreSQL's implicit <table>_pkey.
	for _, constraint := range []string{
		"pk_agent_activity",
		"pk_requirement_extraction_run",
		"pk_requirement_extraction_segment",
	} {
		requirePKConstraint(t, pool, constraint)
	}

	requireIndex(t, pool, "uk_conversation_active_turn")
	// Task 20 follow-up: the unbound-draft lookup by conversation before the
	// message binding exists.
	requireIndex(t, pool, "idx_requirement_attachment_conversation")

	// The one-active-turn invariant is a partial UNIQUE index on
	// conversation(id): rows with a NULL active_turn_id stay outside it.
	var (
		table      string
		column     string
		definition string
	)
	if err := pool.QueryRow(context.Background(), `
		SELECT
			t.relname,
			pg_get_indexdef(i.indexrelid, 1, true),
			pg_get_indexdef(i.indexrelid)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = 'uk_conversation_active_turn'
	`).Scan(&table, &column, &definition); err != nil {
		t.Fatalf("read uk_conversation_active_turn: %v", err)
	}
	if table != "conversation" {
		t.Errorf("uk_conversation_active_turn is on table %s, want conversation", table)
	}
	if column != "id" {
		t.Errorf("uk_conversation_active_turn covers %s, want id", column)
	}
	if !strings.Contains(definition, "UNIQUE INDEX") {
		t.Errorf("uk_conversation_active_turn is not unique: %s", definition)
	}
	if !strings.Contains(definition, "WHERE (active_turn_id IS NOT NULL)") {
		t.Errorf("uk_conversation_active_turn is not partial: %s", definition)
	}
}

// TestV022CSchema pins the C-domain v0.22 schema: version description/goal, the
// version_sut single-repository column removal and exclusion-document rename,
// the SUT/version repository bindings, the global test-account pool with
// per-version enablement, and the clarification handoff artifact.
// See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-10..DB-15,
// DB-22 and docs/version-V0.22/C-version-sut-resources.md §3.
func TestV022CSchema(t *testing.T) {
	pool := integrationDB(t)

	// DB-10: version information and the blocked exit. version.blocker already
	// exists as TEXT and stays unchanged (lead ruling C6); only presence is
	// pinned here. DB-11: scope is renamed in place to scope_exclude.
	for _, c := range []struct{ table, column string }{
		{"version", "description"},
		{"version", "goal"},
		{"version", "blocker"},
		{"version_sut", "scope_exclude"},
	} {
		requireColumn(t, pool, c.table, c.column)
	}
	// DB-11: the single-repository column moves to sut_repository.
	requireNoColumn(t, pool, "version_sut", "code_repo_id")

	for _, table := range []string{
		"sut_repository",
		"version_repository_override",
		"version_account",
		"clarification_handoff",
	} {
		requireTable(t, pool, table)
	}

	// DB-22 / Agent.md §6.9.4 (audit fields): the handoff is generated and then
	// refreshed in place, so like the other mutable v0.22 tables it carries both
	// created_at and updated_at.
	requireColumn(t, pool, "clarification_handoff", "updated_at")

	// D34 / Agent.md §6.9.5: the new tables name their primary-key constraints
	// pk_<table> instead of PostgreSQL's implicit <table>_pkey.
	for _, constraint := range []string{
		"pk_sut_repository",
		"pk_version_repository_override",
		"pk_version_account",
		"pk_clarification_handoff",
	} {
		requirePKConstraint(t, pool, constraint)
	}

	// DB-12..DB-14, DB-22 natural keys plus the lookup indexes on the logical
	// repo/account references.
	for _, index := range []string{
		"uk_sut_repository_sut_repo",
		"uk_version_repository_override_version_repo",
		"uk_version_account_version_account",
		"uk_clarification_handoff_version_revision",
		"idx_sut_repository_repo",
		"idx_version_account_account",
	} {
		requireIndex(t, pool, index)
	}

	// DB-15: test accounts are a global, case-insensitively unique pool. The old
	// uniqueness was scoped to environment_id, which is gone.
	requireNoColumn(t, pool, "test_account", "sut_id")
	requireNoColumn(t, pool, "test_account", "environment_id")
	var testAccountIndexDef string
	if err := pool.QueryRow(context.Background(), `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'uk_test_account_username'
	`).Scan(&testAccountIndexDef); err != nil {
		t.Fatalf("read uk_test_account_username: %v", err)
	}
	if !strings.Contains(testAccountIndexDef, "UNIQUE INDEX") {
		t.Errorf("uk_test_account_username is not unique: %s", testAccountIndexDef)
	}
	if !strings.Contains(testAccountIndexDef, "lower(") || !strings.Contains(testAccountIndexDef, "username") {
		t.Errorf("uk_test_account_username does not cover lower(username): %s", testAccountIndexDef)
	}
	if strings.Contains(testAccountIndexDef, "environment_id") {
		t.Errorf("uk_test_account_username is still environment-scoped: %s", testAccountIndexDef)
	}
}

// TestV022EEXSchema pins the E/EX v0.22 schema: document-image extraction rows
// and the extraction coverage report, the frozen wave scope snapshot,
// execution-time discovery observations, structured human questions and the
// discovery-delta case auto-confirmation audit trail.
// See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-16..DB-19,
// DB-29, DB-30; docs/version-V0.22/E-tools-attachments.md §2.3;
// docs/version-V0.22/B-clarification-messaging.md §6.1 and
// docs/version-V0.22/A-model-token-budget.md §7.3.
func TestV022EEXSchema(t *testing.T) {
	pool := integrationDB(t)
	ctx := context.Background()

	requireTable(t, pool, "attachment_image")
	requireTable(t, pool, "execution_discovery_observation")
	requireColumn(t, pool, "requirement_attachment", "extraction_report")
	requireNoColumn(t, pool, "requirement_attachment", "parse_status")
	requireColumn(t, pool, "run_wave", "scope_snapshot")
	requireColumn(t, pool, "human_request", "question")
	requireColumn(t, pool, "case_version", "source_observation_id")
	requireColumn(t, pool, "case_confirmation", "auto_confirmed_by")
	requireUnique(t, pool, "uk_execution_discovery_observation")

	// DB-17: one row per image extracted from a document attachment. The
	// attachment reference is a same-context FK that cascades on delete; every
	// image must point at a content object and declare where it came from
	// (E §2.3). Image statuses are independent from the attachment extraction
	// status and (attachment_id, seq) is the natural key.
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_attachment (id, version_id, content_ref_id, name, mime, size)
		VALUES (9200, 920, 920, 'doc.pdf', 'application/pdf', 10)
	`); err != nil {
		t.Fatalf("insert requirement attachment: %v", err)
	}
	for _, column := range []string{
		"id", "attachment_id", "seq", "content_ref_id", "mime", "width", "height",
		"source_kind", "status", "caption", "error", "created_at", "updated_at",
	} {
		requireColumn(t, pool, "attachment_image", column)
	}
	requireColumnNotNull(t, pool, "attachment_image", "content_ref_id")
	requireColumnNotNull(t, pool, "attachment_image", "source_kind")
	requireCheckSet(t, pool, "chk_attachment_image_source_kind",
		[]string{"embedded", "page_render", "standalone"}, []string{"bogus"})
	requireCheckSet(t, pool, "chk_attachment_image_status",
		[]string{"extracted", "analyzing", "done", "failed", "skipped"}, []string{"bogus"})
	requirePKConstraint(t, pool, "pk_attachment_image")
	requireUnique(t, pool, "uk_attachment_image_attachment_seq")
	requireIndex(t, pool, "idx_attachment_image_attachment_status")

	// Image rows default to extracted, require seq > 0, declare a known
	// source kind and keep the (attachment_id, seq) identity unique.
	var imageStatus string
	if err := pool.QueryRow(ctx, `
		INSERT INTO attachment_image (id, attachment_id, seq, content_ref_id, source_kind)
		VALUES (9201, 9200, 1, 920, 'embedded')
		RETURNING status
	`).Scan(&imageStatus); err != nil {
		t.Fatalf("insert attachment image: %v", err)
	}
	if imageStatus != "extracted" {
		t.Errorf("attachment_image.status default = %q, want extracted", imageStatus)
	}
	for _, sourceKind := range []string{"page_render", "standalone"} {
		if _, err := pool.Exec(ctx, `
			UPDATE attachment_image SET source_kind = $1 WHERE id = 9201
		`, sourceKind); err != nil {
			t.Errorf("attachment_image rejects source_kind %q: %v", sourceKind, err)
		}
	}
	for _, sourceKind := range []string{"", "bogus"} {
		if _, err := pool.Exec(ctx, `
			UPDATE attachment_image SET source_kind = $1 WHERE id = 9201
		`, sourceKind); err == nil {
			t.Errorf("attachment_image accepts source_kind %q", sourceKind)
		}
	}
	for _, status := range []string{"analyzing", "done", "failed", "skipped"} {
		if _, err := pool.Exec(ctx, `
			UPDATE attachment_image SET status = $1 WHERE id = 9201
		`, status); err != nil {
			t.Errorf("attachment_image rejects status %q: %v", status, err)
		}
	}
	for _, status := range []string{"", "bogus"} {
		if _, err := pool.Exec(ctx, `
			UPDATE attachment_image SET status = $1 WHERE id = 9201
		`, status); err == nil {
			t.Errorf("attachment_image accepts status %q", status)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_image (id, attachment_id, seq, content_ref_id)
		VALUES (9202, 9200, 2, 920)
	`); err == nil {
		t.Error("attachment_image accepts a missing source_kind")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_image (id, attachment_id, seq, content_ref_id, source_kind)
		VALUES (9203, 9200, 3, NULL, 'embedded')
	`); err == nil {
		t.Error("attachment_image accepts a NULL content_ref_id")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_image (id, attachment_id, seq, content_ref_id, source_kind)
		VALUES (9204, 999999, 4, 920, 'embedded')
	`); err == nil {
		t.Error("attachment_image accepts an unknown attachment_id")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_image (id, attachment_id, seq, content_ref_id, source_kind)
		VALUES (9205, 9200, 1, 920, 'embedded')
	`); err == nil {
		t.Error("duplicate attachment_image (attachment_id, seq) accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_image (id, attachment_id, seq, content_ref_id, source_kind)
		VALUES (9206, 9200, 0, 920, 'embedded')
	`); err == nil {
		t.Error("attachment_image seq = 0 accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM requirement_attachment WHERE id = 9200`); err != nil {
		t.Fatalf("delete requirement attachment: %v", err)
	}
	var remainingImages int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM attachment_image WHERE attachment_id = 9200
	`).Scan(&remainingImages); err != nil {
		t.Fatalf("count attachment images after cascade: %v", err)
	}
	if remainingImages != 0 {
		t.Errorf("attachment_image rows after attachment delete = %d, want 0 (ON DELETE CASCADE)", remainingImages)
	}

	// DB-16: the structured question state machine. The 0.21 open/resolved
	// statuses are gone and new rows default to pending.
	for _, column := range []string{"conversation_id", "message_id"} {
		requireColumn(t, pool, "human_request", column)
	}
	requireCheckSet(t, pool, "chk_human_request_kind",
		[]string{"clarification_question", "approval", "blocked_recovery", "login_takeover", "conflict", "other"},
		[]string{"escalation"})
	requireCheckSet(t, pool, "chk_human_request_status",
		[]string{"pending", "answered", "expired", "cancelled"}, []string{"open", "resolved"})
	requireIndex(t, pool, "idx_human_request_conversation")
	var humanStatus string
	if err := pool.QueryRow(ctx, `
		INSERT INTO human_request (id, version_id, kind, expires_at)
		VALUES (9301, 9300, 'approval', now() + interval '1 hour')
		RETURNING status
	`).Scan(&humanStatus); err != nil {
		t.Fatalf("insert human request: %v", err)
	}
	if humanStatus != "pending" {
		t.Errorf("human_request.status default = %q, want pending", humanStatus)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE human_request SET status = 'open' WHERE id = 9301
	`); err == nil {
		t.Error("human_request still accepts legacy status 'open'")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE human_request SET status = 'resolved' WHERE id = 9301
	`); err == nil {
		t.Error("human_request still accepts legacy status 'resolved'")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE human_request SET kind = 'clarification_question' WHERE id = 9301
	`); err != nil {
		t.Errorf("human_request rejects v0.22 kind clarification_question: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE human_request SET kind = 'escalation' WHERE id = 9301
	`); err == nil {
		t.Error("human_request accepts unknown kind 'escalation'")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE human_request SET status = 'answered' WHERE id = 9301
	`); err != nil {
		t.Errorf("human_request rejects v0.22 status answered: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE human_request SET status = 'bogus' WHERE id = 9301
	`); err == nil {
		t.Error("human_request accepts unknown status 'bogus'")
	}

	// DB-29: executions keep only normalized resource templates. The unique key
	// is the observation identity; sensitive request payload columns must not
	// exist (N-16/T8).
	for _, column := range []string{
		"id", "version_id", "wave_id", "case_run_id", "version_sut_id",
		"environment_id", "kind", "method", "host", "resource_template",
		"status_code", "mime", "source_kind", "fingerprint", "occurrences",
		"first_seen_at", "last_seen_at", "decision", "resource_id", "fact_id",
		"created_at", "updated_at",
	} {
		requireColumn(t, pool, "execution_discovery_observation", column)
	}
	for _, column := range []string{
		"query", "query_values", "query_string", "headers", "header",
		"cookie", "cookies", "body", "request_body", "response_body", "raw",
	} {
		requireNoColumn(t, pool, "execution_discovery_observation", column)
	}
	requirePKConstraint(t, pool, "pk_execution_discovery_observation")
	// version_sut_id is part of the observation identity, so it must be
	// non-null or NULLS DISTINCT would defeat the unique key.
	requireColumnNotNull(t, pool, "execution_discovery_observation", "version_sut_id")
	requireCheckSet(t, pool, "chk_execution_discovery_observation_kind",
		[]string{"page", "api"}, []string{"bogus"})
	requireCheckSet(t, pool, "chk_execution_discovery_observation_decision",
		[]string{"new", "proposed", "approved", "rejected", "out_of_scope"}, []string{"bogus"})
	requireCheckSet(t, pool, "chk_execution_discovery_observation_source_kind",
		[]string{"execution", "p9"}, []string{"bogus"})
	for _, index := range []string{
		"idx_execution_discovery_observation_version_decision",
		"idx_execution_discovery_observation_wave",
		"idx_execution_discovery_observation_resource",
	} {
		requireIndex(t, pool, index)
	}
	var uniqueDef string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname = 'uk_execution_discovery_observation'
		  AND connamespace = current_schema()::regnamespace
	`).Scan(&uniqueDef); err != nil {
		t.Fatalf("read uk_execution_discovery_observation: %v", err)
	}
	wantUniqueDef := "UNIQUE (version_id, version_sut_id, kind, method, resource_template)"
	if uniqueDef != wantUniqueDef {
		t.Errorf("uk_execution_discovery_observation = %q, want %q", uniqueDef, wantUniqueDef)
	}
	var decision, sourceKind string
	if err := pool.QueryRow(ctx, `
		INSERT INTO execution_discovery_observation
			(id, version_id, version_sut_id, kind, resource_template)
		VALUES (9401, 9400, 9410, 'page', '/orders/42')
		RETURNING decision, source_kind
	`).Scan(&decision, &sourceKind); err != nil {
		t.Fatalf("insert discovery observation: %v", err)
	}
	if decision != "new" || sourceKind != "execution" {
		t.Errorf("observation defaults = (%q, %q), want (new, execution)", decision, sourceKind)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_discovery_observation
			(id, version_id, version_sut_id, kind, resource_template)
		VALUES (9402, 9400, 9410, 'page', '/orders/42')
	`); err == nil {
		t.Error("duplicate execution_discovery_observation identity accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_discovery_observation
			(id, version_id, version_sut_id, kind, resource_template)
		VALUES (9403, 9400, 9410, 'api', '/orders/42')
	`); err != nil {
		t.Errorf("distinct discovery observation kind rejected: %v", err)
	}
	for _, change := range []struct{ column, value string }{
		{"decision", "proposed"},
		{"source_kind", "p9"},
	} {
		if _, err := pool.Exec(ctx, `
			UPDATE execution_discovery_observation SET `+change.column+` = $1 WHERE id = 9401
		`, change.value); err != nil {
			t.Errorf("execution_discovery_observation rejects %s %q: %v", change.column, change.value, err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE execution_discovery_observation SET `+change.column+` = 'bogus' WHERE id = 9401
		`); err == nil {
			t.Errorf("execution_discovery_observation accepts %s 'bogus'", change.column)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_discovery_observation
			(id, version_id, version_sut_id, kind, resource_template)
		VALUES (9404, 9400, 9410, 'bogus', '/orders/43')
	`); err == nil {
		t.Error("execution_discovery_observation accepts kind 'bogus'")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_discovery_observation
			(id, version_id, kind, resource_template)
		VALUES (9405, 9400, 'page', '/orders/44')
	`); err == nil {
		t.Error("execution_discovery_observation accepts a NULL version_sut_id")
	}

	// DB-30: discovery-delta case versions and the auto-confirmation audit.
	for _, column := range []string{
		"trigger", "source_observation_id", "auto_confirmed_by", "rollback_of",
	} {
		requireColumn(t, pool, "case_version", column)
	}
	requireIndex(t, pool, "idx_case_version_source_observation")
	for _, column := range []string{
		"auto_confirmed", "auto_confirmed_by", "trigger", "source_observation_id",
	} {
		requireColumn(t, pool, "case_confirmation", column)
	}
	requireColumnDefault(t, pool, "case_version", "trigger", "'normal'::text")
	requireColumnDefault(t, pool, "case_confirmation", "trigger", "'manual'::text")
	requireCheckSet(t, pool, "chk_case_version_trigger",
		[]string{"normal", "discovery_delta"}, []string{"bogus"})
	requireCheckSet(t, pool, "chk_case_confirmation_trigger",
		[]string{"manual", "discovery_delta"}, []string{"bogus"})
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES (9500, 950, 950, 951, 'scenario')
	`); err != nil {
		t.Fatalf("insert case scenario: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id) VALUES (9501, 9500)
	`); err != nil {
		t.Fatalf("insert case entity: %v", err)
	}
	var caseVersionTrigger string
	if err := pool.QueryRow(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, content_hash)
		VALUES (9502, 9501, 1, 'case', repeat('a', 64))
		RETURNING trigger
	`).Scan(&caseVersionTrigger); err != nil {
		t.Fatalf("insert case version: %v", err)
	}
	if caseVersionTrigger != "normal" {
		t.Errorf("case_version.trigger default = %q, want normal", caseVersionTrigger)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE case_version SET trigger = 'discovery_delta' WHERE id = 9502
	`); err != nil {
		t.Errorf("case_version rejects trigger discovery_delta: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE case_version SET trigger = 'bogus' WHERE id = 9502
	`); err == nil {
		t.Error("case_version accepts trigger 'bogus'")
	}
	var confirmationTrigger string
	if err := pool.QueryRow(ctx, `
		INSERT INTO case_confirmation
			(id, case_version_id, scope, target_scope_id, batch_id, confirmed_by)
		VALUES (9503, 9502, 'case', 9502, 'batch-1', 950)
		RETURNING trigger
	`).Scan(&confirmationTrigger); err != nil {
		t.Fatalf("insert case confirmation: %v", err)
	}
	if confirmationTrigger != "manual" {
		t.Errorf("case_confirmation.trigger default = %q, want manual", confirmationTrigger)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE case_confirmation SET trigger = 'discovery_delta' WHERE id = 9503
	`); err != nil {
		t.Errorf("case_confirmation rejects trigger discovery_delta: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE case_confirmation SET trigger = 'bogus' WHERE id = 9503
	`); err == nil {
		t.Error("case_confirmation accepts trigger 'bogus'")
	}
}

// integrationDB returns a fresh, fully migrated test schema.
func integrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testutil.NewEmptyDB(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pool
}

func requireTable(t *testing.T, pool *pgxpool.Pool, table string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Errorf("check table %s: %v", table, err)
		return
	}
	if !exists {
		t.Errorf("missing table %s", table)
	}
}

func requireColumn(t *testing.T, pool *pgxpool.Pool, table, column string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
		)
	`, table, column).Scan(&exists); err != nil {
		t.Errorf("check column %s.%s: %v", table, column, err)
		return
	}
	if !exists {
		t.Errorf("missing column %s.%s", table, column)
	}
}

func requireNoColumn(t *testing.T, pool *pgxpool.Pool, table, column string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
		)
	`, table, column).Scan(&exists); err != nil {
		t.Errorf("check column %s.%s: %v", table, column, err)
		return
	}
	if exists {
		t.Errorf("column %s.%s still exists", table, column)
	}
}

func requireIndex(t *testing.T, pool *pgxpool.Pool, index string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
		t.Errorf("check index %s: %v", index, err)
		return
	}
	if !exists {
		t.Errorf("missing index %s", index)
	}
}

// requireUnique asserts a UNIQUE constraint (contype 'u') with exactly this
// name exists in the test schema, pinning the D34 uk_<table>_<columns> naming.
func requireUnique(t *testing.T, pool *pgxpool.Pool, constraint string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = $1
			  AND contype = 'u'
			  AND connamespace = current_schema()::regnamespace
		)
	`, constraint).Scan(&exists); err != nil {
		t.Errorf("check unique constraint %s: %v", constraint, err)
		return
	}
	if !exists {
		t.Errorf("missing unique constraint %s", constraint)
	}
}

// requirePKConstraint asserts a primary-key constraint with exactly this name
// exists in the test schema, pinning the D34 pk_<table> constraint naming.
func requirePKConstraint(t *testing.T, pool *pgxpool.Pool, constraint string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = $1
			  AND contype = 'p'
			  AND connamespace = current_schema()::regnamespace
		)
	`, constraint).Scan(&exists); err != nil {
		t.Errorf("check constraint %s: %v", constraint, err)
		return
	}
	if !exists {
		t.Errorf("missing primary-key constraint %s", constraint)
	}
}

// requireColumnNotNull asserts information_schema reports the column as NOT
// NULL, so a silently reintroduced NULL slips no further than this test.
func requireColumnNotNull(t *testing.T, pool *pgxpool.Pool, table, column string) {
	t.Helper()
	var nullable string
	if err := pool.QueryRow(context.Background(), `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
	`, table, column).Scan(&nullable); err != nil {
		t.Errorf("read %s.%s nullability: %v", table, column, err)
		return
	}
	if nullable != "NO" {
		t.Errorf("%s.%s is nullable, want NOT NULL", table, column)
	}
}

// requireColumnDefault asserts the exact column default, pinning enum-like
// defaults (normal/manual) against a silent drop.
func requireColumnDefault(t *testing.T, pool *pgxpool.Pool, table, column, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(), `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
	`, table, column).Scan(&got); err != nil {
		t.Errorf("read %s.%s default: %v", table, column, err)
		return
	}
	if got != want {
		t.Errorf("%s.%s default = %q, want %q", table, column, got, want)
	}
}

// requireCheckSet pins a named CHECK constraint's value set via
// pg_get_constraintdef: every allowed value must still be listed and no
// forbidden (legacy/draft) value may survive.
func requireCheckSet(t *testing.T, pool *pgxpool.Pool, constraint string, allowed, forbidden []string) {
	t.Helper()
	var def string
	if err := pool.QueryRow(context.Background(), `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = $1
		  AND contype = 'c'
		  AND connamespace = current_schema()::regnamespace
	`, constraint).Scan(&def); err != nil {
		t.Errorf("read check constraint %s: %v", constraint, err)
		return
	}
	for _, value := range allowed {
		if !strings.Contains(def, "'"+value+"'") {
			t.Errorf("check constraint %s misses %q: %s", constraint, value, def)
		}
	}
	for _, value := range forbidden {
		if strings.Contains(def, "'"+value+"'") {
			t.Errorf("check constraint %s still allows %q: %s", constraint, value, def)
		}
	}
}
