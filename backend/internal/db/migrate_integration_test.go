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
	if len(applied) != 8 {
		t.Fatalf("applied %d migrations, want 8: %v", len(applied), applied)
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
}
