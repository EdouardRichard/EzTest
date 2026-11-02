//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestMigrations021Schema pins the additive 0.2.1 schema: the three new tables,
// the new columns on existing tables, and the sut_page uniqueness swap from
// route_template to the normalized URL. It also exercises the generated
// normalized_url column so the normalization rule stays enforceable in the DB.
func TestMigrations021Schema(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	tables := []string{"version_plan_item", "version_fact", "agent_message_part"}
	for _, name := range tables {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatalf("to_regclass(%s): %v", name, err)
		}
		if !exists {
			t.Errorf("missing 0.2.1 table %s", name)
		}
	}

	columns := []struct {
		table      string
		column     string
		dataType   string
		udtName    string
		nullable   string
		generated  string
		defaultSQL string
	}{
		// version: stage machine state.
		{"version", "stage", "text", "text", "NO", "NEVER", "'created'::text"},
		{"version", "stage_entered_at", "timestamp with time zone", "timestamptz", "NO", "NEVER", "now()"},
		{"version", "blocker", "text", "text", "YES", "NEVER", ""},
		// work_item: the new 0.2.1 task contract.
		{"work_item", "kind", "text", "text", "NO", "NEVER", "''::text"},
		{"work_item", "goal", "text", "text", "NO", "NEVER", "''::text"},
		{"work_item", "target", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"work_item", "inputs", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"work_item", "expected", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"work_item", "budget", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"work_item", "depends_on", "ARRAY", "_int8", "YES", "NEVER", ""},
		{"work_item", "created_by", "text", "text", "NO", "NEVER", "'state_machine'::text"},
		{"work_item", "idempotency_key", "text", "text", "YES", "NEVER", ""},
		{"work_item", "result", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"work_item", "lease_epoch", "bigint", "int8", "NO", "NEVER", "0"},
		// agent_run: actual model, config, fallback and token/cache accounting.
		{"agent_run", "model", "text", "text", "NO", "NEVER", "''::text"},
		{"agent_run", "config_id", "bigint", "int8", "YES", "NEVER", ""},
		{"agent_run", "fallback_from", "text", "text", "YES", "NEVER", ""},
		{"agent_run", "tokens", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"agent_run", "cache", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		// llm: thinking mode, reasoning effort, default model, capabilities.
		{"llm_config", "thinking_type", "text", "text", "NO", "NEVER", "'auto'::text"},
		{"llm_config", "reasoning_effort", "text", "text", "NO", "NEVER", "'none'::text"},
		{"llm_config", "default_model_id", "bigint", "int8", "YES", "NEVER", ""},
		{"llm_model", "capabilities", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"llm_model", "deprecated", "boolean", "bool", "NO", "NEVER", "false"},
		// knowledge: structured bindings + verification lifecycle.
		{"knowledge_item", "bindings", "jsonb", "jsonb", "NO", "NEVER", "'{}'::jsonb"},
		{"knowledge_item", "last_verified_at", "timestamp with time zone", "timestamptz", "YES", "NEVER", ""},
		{"knowledge_item", "status", "text", "text", "NO", "NEVER", "'draft'::text"},
		// attachment: parser routing and structured parse errors.
		{"requirement_attachment", "kind", "text", "text", "NO", "NEVER", "'document'::text"},
		{"requirement_attachment", "parse_status", "text", "text", "NO", "NEVER", "'pending'::text"},
		{"requirement_attachment", "parse_error", "text", "text", "YES", "NEVER", ""},
		{"requirement_attachment", "page_refs", "jsonb", "jsonb", "NO", "NEVER", "'[]'::jsonb"},
		// conversation: per-conversation model override (spec §12.2).
		{"conversation", "model_override", "text", "text", "YES", "NEVER", ""},
		// sut_page: route_template is now optional and normalized_url is generated.
		{"sut_page", "route_template", "text", "text", "YES", "NEVER", ""},
		{"sut_page", "normalized_url", "text", "text", "NO", "ALWAYS", ""},
	}
	for _, c := range columns {
		var dataType, udtName, nullable, generated string
		var defaultSQL *string
		if err := pool.QueryRow(ctx, `
			SELECT data_type, udt_name, is_nullable, is_generated, column_default
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
		`, c.table, c.column).Scan(&dataType, &udtName, &nullable, &generated, &defaultSQL); err != nil {
			t.Errorf("missing 0.2.1 column %s.%s: %v", c.table, c.column, err)
			continue
		}
		if dataType != c.dataType || udtName != c.udtName {
			t.Errorf("%s.%s type = %s/%s, want %s/%s", c.table, c.column, dataType, udtName, c.dataType, c.udtName)
		}
		if nullable != c.nullable {
			t.Errorf("%s.%s nullable = %s, want %s", c.table, c.column, nullable, c.nullable)
		}
		if generated != c.generated {
			t.Errorf("%s.%s generated = %s, want %s", c.table, c.column, generated, c.generated)
		}
		if c.defaultSQL != "" && (defaultSQL == nil || *defaultSQL != c.defaultSQL) {
			t.Errorf("%s.%s default = %v, want %s", c.table, c.column, defaultSQL, c.defaultSQL)
		}
	}

	if len(applied) != 47 {
		t.Fatalf("applied %d migrations, want 47: %v", len(applied), applied)
	}
	again, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Migrate applied %v", again)
	}

	// New unique/ordering indexes.
	indexes := []struct {
		name     string
		contains []string
	}{
		{"uk_version_fact_version_kind_key_hash", []string{"UNIQUE", "(version_id, kind, key, content_hash)"}},
		{"idx_version_plan_item_version_status_order", []string{"(version_id, status, sort_order)"}},
		{"uk_work_item_idempotency", []string{"UNIQUE", "(version_id, kind, idempotency_key)"}},
		{"uk_agent_message_part_message_seq", []string{"UNIQUE", "(message_id, seq)"}},
		{"uk_sut_page_normalized_url", []string{"UNIQUE", "(environment_id, normalized_url)"}},
	}
	for _, index := range indexes {
		var definition string
		if err := pool.QueryRow(ctx, `
			SELECT indexdef FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = $1
		`, index.name).Scan(&definition); err != nil {
			t.Errorf("missing 0.2.1 index %s: %v", index.name, err)
			continue
		}
		for _, want := range index.contains {
			if !strings.Contains(definition, want) {
				t.Errorf("index %s = %s, want %q", index.name, definition, want)
			}
		}
	}

	var oldIndexExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('uk_sut_page_template') IS NOT NULL`).Scan(&oldIndexExists); err != nil {
		t.Fatalf("check old sut_page index: %v", err)
	}
	if oldIndexExists {
		t.Error("uk_sut_page_template still exists; uniqueness must move to normalized_url")
	}

	// New constraints (including the widened knowledge status enum).
	constraints := []string{
		"chk_version_stage", "chk_work_item_created_by", "chk_agent_message_part_type",
		"chk_agent_message_part_payload", "chk_knowledge_item_status",
		"fk_agent_message_part_message", "fk_llm_config_default_model",
	}
	for _, name := range constraints {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1)`, name).Scan(&exists); err != nil {
			t.Fatalf("check constraint %s: %v", name, err)
		}
		if !exists {
			t.Errorf("missing 0.2.1 constraint %s", name)
		}
	}

	var statusCheck string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'knowledge_item'::regclass AND conname = 'chk_knowledge_item_status'
	`).Scan(&statusCheck); err != nil {
		t.Fatalf("read knowledge status check: %v", err)
	}
	for _, status := range []string{"draft", "approved", "rejected", "revoked", "suspect", "stale"} {
		if !strings.Contains(statusCheck, "'"+status+"'") {
			t.Errorf("knowledge status check rejects %q: %s", status, statusCheck)
		}
	}
	for i, status := range []string{"suspect", "stale"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO knowledge_item (id, scope, title, content, status)
			VALUES ($1, 'global', 'migration test', 'body', $2)
		`, 210200+i, status); err != nil {
			t.Errorf("knowledge_item status %q rejected: %v", status, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, created_by) VALUES (210001, '0.2.1 migration test', 1)
	`); err != nil {
		t.Fatalf("insert version: %v", err)
	}

	// version_fact is append-only and conflict-unique per content hash.
	if _, err := pool.Exec(ctx, `
		INSERT INTO version_fact (id, version_id, kind, key, payload, confidence, source, content_hash)
		VALUES (210010, 210001, 'page', '/login', '{}'::jsonb, 0.9, 'explorer', repeat('a', 64))
	`); err != nil {
		t.Fatalf("insert version_fact: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version_fact (id, version_id, kind, key, payload, confidence, source, content_hash)
		VALUES (210011, 210001, 'page', '/login', '{}'::jsonb, 0.5, 'explorer', repeat('a', 64))
	`); err == nil {
		t.Error("duplicate version_fact (version_id, kind, key, content_hash) accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version_fact (id, version_id, kind, key, payload, confidence, source, content_hash)
		VALUES (210012, 210001, 'page', '/login', '{}'::jsonb, 0.5, 'explorer', repeat('b', 64))
	`); err != nil {
		t.Errorf("conflicting fact with a different content hash rejected: %v", err)
	}

	// work_item idempotency is version+kind+key; NULL keys stay exempt.
	workItemInsert := `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key, kind, idempotency_key)
		VALUES ($1, 210001, 'design', 'design.cases', $2, 'design.cases', $3)
	`
	if _, err := pool.Exec(ctx, workItemInsert, 210020, "dedupe-a", "idem-a"); err != nil {
		t.Fatalf("insert work_item: %v", err)
	}
	if _, err := pool.Exec(ctx, workItemInsert, 210021, "dedupe-b", "idem-a"); err == nil {
		t.Error("duplicate work_item idempotency key accepted")
	}
	if _, err := pool.Exec(ctx, workItemInsert, 210022, "dedupe-c", nil); err != nil {
		t.Errorf("work_item with NULL idempotency key rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, workItemInsert, 210023, "dedupe-d", nil); err != nil {
		t.Errorf("second work_item with NULL idempotency key rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE work_item SET depends_on = ARRAY[210020]::BIGINT[] WHERE id = 210020
	`); err != nil {
		t.Errorf("store BIGINT[] depends_on: %v", err)
	}

	// sut_page: normalized_url is generated and replaces route_template uniqueness.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, name, created_by) VALUES (210100, '0.2.1 pages', 1)
	`); err != nil {
		t.Fatalf("insert sut: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_environment (id, sut_id, name, base_url) VALUES (210101, 210100, 'prod', 'https://example.test')
	`); err != nil {
		t.Fatalf("insert sut_environment: %v", err)
	}
	var normalizedURL string
	var routeTemplate *string
	if err := pool.QueryRow(ctx, `
		INSERT INTO sut_page (id, sut_id, environment_id, name, url)
		VALUES (210110, 210100, 210101, 'login', 'https://Example.TEST:443/Login/?next=%2Fhome#/signin')
		RETURNING normalized_url, route_template
	`).Scan(&normalizedURL, &routeTemplate); err != nil {
		t.Fatalf("insert sut_page: %v", err)
	}
	if want := "https://example.test/Login#/signin"; normalizedURL != want {
		t.Errorf("normalized_url = %q, want %q", normalizedURL, want)
	}
	if routeTemplate != nil {
		t.Errorf("route_template = %q, want NULL", *routeTemplate)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_page (id, sut_id, environment_id, name, url)
		VALUES (210111, 210100, 210101, 'login duplicate', 'https://example.test/Login#/signin')
	`); err == nil {
		t.Error("duplicate normalized sut_page URL accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_page (id, sut_id, environment_id, name, url)
		VALUES (210112, 210100, 210101, 'orders', 'http://Example.test:80/Orders/')
		RETURNING normalized_url
	`); err != nil {
		t.Errorf("insert second distinct normalized sut_page: %v", err)
	}
}
