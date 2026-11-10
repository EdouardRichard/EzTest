//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestAgentProfileSeedVersionMigrationUpgradesLegacyRows replays a pre-v0.2.3
// database (migrations 1..64), inserts the five retired profiles the v0.22
// model dropped, then applies the 65th migration. It asserts the upgrade keeps
// the rows (audit/history), disables them, and leaves seed_version at 0 for the
// startup profile backfill.
func TestAgentProfileSeedVersionMigrationUpgradesLegacyRows(t *testing.T) {
	pool := testutil.NewEmptyDB(t)
	ctx := context.Background()

	// The fixture is an upgrade, not a fresh database: every migration before
	// agent_0009 must already be applied.
	const through = 64
	if err := db.ApplyMigrationsThroughForTest(ctx, pool, through); err != nil {
		t.Fatalf("ApplyMigrationsThroughForTest(%d): %v", through, err)
	}
	var seedVersionExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'agent_profile' AND column_name = 'seed_version'
		)
	`).Scan(&seedVersionExists); err != nil {
		t.Fatalf("check seed_version presence: %v", err)
	}
	if seedVersionExists {
		t.Fatalf("seed_version exists before migration %d; the fixture is not an upgrade path", through+1)
	}

	retired := []string{
		"requirement_curator",
		"context_scout",
		"design_planner",
		"case_author",
		"data_planner",
	}
	for i, key := range retired {
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_profile (id, key, name, system_prompt, allowed_tools, budgets, enabled)
			VALUES ($1, $2, $3, $4, '[]'::jsonb, '{}'::jsonb, TRUE)
		`, int64(920000+i), key, key, "[profile:"+key+"] legacy prompt"); err != nil {
			t.Fatalf("insert retired profile %q: %v", key, err)
		}
	}
	// A profile that survives v0.22 must keep its enabled flag.
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_profile (id, key, name, system_prompt, allowed_tools, budgets, enabled)
		VALUES (920010, 'clarification_host', '澄清 Host',
		        '[profile:clarification_host] legacy prompt', '[]'::jsonb, '{}'::jsonb, TRUE)
	`); err != nil {
		t.Fatalf("insert retained profile: %v", err)
	}

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 1 || applied[0] != "agent_0009_profile_seed_version.sql" {
		t.Fatalf("applied = %v, want only agent_0009_profile_seed_version.sql", applied)
	}

	for _, key := range retired {
		var enabled bool
		var seedVersion int
		if err := pool.QueryRow(ctx, `
			SELECT enabled, seed_version FROM agent_profile WHERE key = $1
		`, key).Scan(&enabled, &seedVersion); err != nil {
			t.Fatalf("read retired profile %q: %v", key, err)
		}
		if enabled {
			t.Errorf("retired profile %q is still enabled", key)
		}
		if seedVersion != 0 {
			t.Errorf("retired profile %q seed_version = %d, want 0", key, seedVersion)
		}
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_profile`).Scan(&rows); err != nil {
		t.Fatalf("count agent_profile: %v", err)
	}
	if rows != len(retired)+1 {
		t.Fatalf("agent_profile rows = %d, want %d (rows retained)", rows, len(retired)+1)
	}

	var retainedEnabled bool
	var retainedSeedVersion int
	if err := pool.QueryRow(ctx, `
		SELECT enabled, seed_version FROM agent_profile WHERE key = 'clarification_host'
	`).Scan(&retainedEnabled, &retainedSeedVersion); err != nil {
		t.Fatalf("read retained profile: %v", err)
	}
	if !retainedEnabled {
		t.Error("retained clarification_host profile was disabled")
	}
	if retainedSeedVersion != 0 {
		t.Errorf("retained profile seed_version = %d, want 0", retainedSeedVersion)
	}
}
