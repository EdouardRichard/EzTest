//go:build integration

package db_test

import (
	"context"
	"errors"
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
	if len(applied) != 17 {
		t.Fatalf("applied %d migrations, want 17: %v", len(applied), applied)
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
