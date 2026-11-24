//go:build integration

package designpg

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func designInt64Ptr(value int64) *int64 { return &value }

// readModelSeedDesignVersion inserts one version row. v0.2.4-B: version.environment_id
// is NOT NULL (version_0016), so the fixture creates the environment the version
// binds first. The environment id/name are derived from the version id so a test
// that seeds several versions gets distinct environments (uk_environment_name is
// unique on lower(name)).
func readModelSeedDesignVersion(t *testing.T, pool *pgxpool.Pool, id int64, title string) {
	t.Helper()
	ctx := context.Background()
	environmentID := id*10 + 1
	if _, err := pool.Exec(ctx, `
		INSERT INTO environment (id, name, status) VALUES ($1, $2, 'active')
		ON CONFLICT (id) DO NOTHING
	`, environmentID, fmt.Sprintf("p6-design-env-%d", environmentID)); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, status, created_by, environment_id)
		VALUES ($1, $2, 'initial', 1, $3)
	`, id, title, environmentID); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

func readModelSeedDesignScenario(t *testing.T, pool *pgxpool.Pool, versionID, scenarioID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES ($1, $2, 2001, 2002, $3)
	`, scenarioID, versionID, "scenario-"+time.Now().Format("150405.000000000")); err != nil {
		t.Fatalf("seed case scenario: %v", err)
	}
}

func readModelSeedDesignCase(t *testing.T, pool *pgxpool.Pool, caseID, caseVersionID, scenarioID int64, status, title string, createdAt, updatedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, caseID, scenarioID, caseVersionID, status, createdAt, updatedAt); err != nil {
		t.Fatalf("seed case entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (
			id, case_id, revision, title, content_hash, status, created_at, updated_at
		) VALUES ($1, $2, 1, $3, $4, $5, $6, $7)
	`, caseVersionID, caseID, title, strings.Repeat("a", 64), status, createdAt, updatedAt); err != nil {
		t.Fatalf("seed case version: %v", err)
	}
}

func readModelSeedDesignCaseVersion(t *testing.T, pool *pgxpool.Pool, caseID, caseVersionID int64, revision int, status, title string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_version (id, case_id, revision, title, content_hash, status)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, caseVersionID, caseID, revision, title, strings.Repeat("b", 64), status); err != nil {
		t.Fatalf("seed case version: %v", err)
	}
}

func readModelSeedDesignRequirement(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status)
		VALUES ($1, $2, $3, $4)
	`, requirementID, versionID, requirementVersionID, status); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES ($1, $2, 1, $3, $4)
	`, requirementVersionID, requirementID, "requirement", strings.Repeat("c", 64)); err != nil {
		t.Fatalf("seed requirement version: %v", err)
	}
}

func readModelSeedDesignScope(t *testing.T, pool *pgxpool.Pool, id, versionID, requirementID int64, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO design_scope (id, version_id, requirement_id, status)
		VALUES ($1, $2, $3, $4)
	`, id, versionID, requirementID, status); err != nil {
		t.Fatalf("seed design scope: %v", err)
	}
}

func TestDesignQueryReaderCountCaseConfirmation(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	readModelSeedDesignScenario(t, pool, 1001, 5001)
	readModelSeedDesignScenario(t, pool, 1002, 5002)
	readModelSeedDesignCase(t, pool, 7001, 8001, 5001, "draft", "draft case", base, base)
	readModelSeedDesignCase(t, pool, 7002, 8002, 5001, "confirmed", "confirmed case", base, base)
	readModelSeedDesignCase(t, pool, 7003, 8003, 5001, "stale", "stale case", base, base)
	readModelSeedDesignCase(t, pool, 7011, 8011, 5002, "confirmed", "other confirmed", base, base)
	// A case without a current version does not belong to the current-version
	// denominator.
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (7004, 5001, NULL, 'draft')
	`); err != nil {
		t.Fatalf("seed versionless case: %v", err)
	}

	num, denom, err := reader.CountCaseConfirmation(ctx, nil)
	if err != nil {
		t.Fatalf("CountCaseConfirmation global: %v", err)
	}
	if num != 2 || denom != 4 {
		t.Fatalf("global counts = %d/%d, want 2/4", num, denom)
	}

	num, denom, err = reader.CountCaseConfirmation(ctx, designInt64Ptr(1001))
	if err != nil {
		t.Fatalf("CountCaseConfirmation version: %v", err)
	}
	if num != 1 || denom != 3 {
		t.Fatalf("version counts = %d/%d, want 1/3", num, denom)
	}

	num, denom, err = reader.CountCaseConfirmation(ctx, designInt64Ptr(1003))
	if err != nil {
		t.Fatalf("CountCaseConfirmation empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestDesignQueryReaderSearchCases(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	readModelSeedDesignScenario(t, pool, 1001, 5001)

	readModelSeedDesignCase(t, pool, 7001, 8001, 5001, "draft", "Login success", base, base.Add(time.Hour))
	readModelSeedDesignCase(t, pool, 7002, 8002, 5001, "confirmed", "Login failure", base, base.Add(2*time.Hour))
	readModelSeedDesignCase(t, pool, 7003, 8003, 5001, "confirmed", "Checkout", base, base.Add(3*time.Hour))

	// 7004's historical revision matches q but its current revision does not.
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (7004, 5001, 8004, 'confirmed')
	`); err != nil {
		t.Fatalf("seed case entity: %v", err)
	}
	readModelSeedDesignCaseVersion(t, pool, 7004, 8104, 1, "superseded", "Login old")
	readModelSeedDesignCaseVersion(t, pool, 7004, 8004, 2, "confirmed", "Other")

	rows, err := reader.SearchCases(ctx, "login", 2)
	if err != nil {
		t.Fatalf("SearchCases: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (limit)", len(rows))
	}
	if rows[0].CaseID != 7002 || rows[1].CaseID != 7001 {
		t.Fatalf("case ids = %d,%d, want 7002,7001 (updated_at DESC)", rows[0].CaseID, rows[1].CaseID)
	}
	if rows[0].CaseVersionID != 8002 || rows[0].VersionID != 1001 || rows[0].Title != "Login failure" || rows[0].Status != "confirmed" {
		t.Fatalf("row = %#v", rows[0])
	}
	if rows[0].VersionTitle != "" {
		t.Fatalf("version_title = %q, want empty (owned by the version context)", rows[0].VersionTitle)
	}

	limited, err := reader.SearchCases(ctx, "login", 1)
	if err != nil {
		t.Fatalf("SearchCases limit: %v", err)
	}
	if len(limited) != 1 || limited[0].CaseID != 7002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestDesignQueryReaderListDraftCases(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	readModelSeedDesignScenario(t, pool, 1001, 5001)
	readModelSeedDesignCase(t, pool, 7001, 8001, 5001, "draft", "Older draft", base, base)
	readModelSeedDesignCase(t, pool, 7002, 8002, 5001, "draft", "Newer draft", base.Add(time.Hour), base.Add(time.Hour))
	readModelSeedDesignCase(t, pool, 7003, 8003, 5001, "confirmed", "Confirmed", base.Add(2*time.Hour), base.Add(2*time.Hour))

	rows, err := reader.ListDraftCases(ctx, 10)
	if err != nil {
		t.Fatalf("ListDraftCases: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (draft only)", len(rows))
	}
	if rows[0].CaseID != 7002 || rows[1].CaseID != 7001 {
		t.Fatalf("case ids = %d,%d, want 7002,7001 (created_at DESC)", rows[0].CaseID, rows[1].CaseID)
	}
	if rows[0].Title != "Newer draft" || rows[0].VersionID != 1001 || rows[0].VersionTitle != "" {
		t.Fatalf("row = %#v", rows[0])
	}

	limited, err := reader.ListDraftCases(ctx, 1)
	if err != nil {
		t.Fatalf("ListDraftCases limit: %v", err)
	}
	if len(limited) != 1 || limited[0].CaseID != 7002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestDesignQueryReaderPendingDesignCounts(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	readModelSeedDesignVersion(t, pool, 1001, "Login SUT")
	readModelSeedDesignVersion(t, pool, 1002, "Checkout SUT")

	// 1001: two clarified requirements without an active scope (2001 and 2003);
	// an active scope clears 2002. Draft/closed requirements are not pending.
	readModelSeedDesignRequirement(t, pool, 1001, 2001, 3001, "clarified")
	readModelSeedDesignRequirement(t, pool, 1001, 2002, 3002, "clarified")
	readModelSeedDesignRequirement(t, pool, 1001, 2003, 3003, "clarified")
	readModelSeedDesignRequirement(t, pool, 1001, 2004, 3004, "draft")
	readModelSeedDesignRequirement(t, pool, 1001, 2005, 3005, "closed")
	readModelSeedDesignScope(t, pool, 4001, 1001, 2002, "active")
	readModelSeedDesignScope(t, pool, 4002, 1001, 2003, "closed")
	// 1002 has one pending requirement.
	readModelSeedDesignRequirement(t, pool, 1002, 2011, 3011, "clarified")

	counts, err := reader.PendingDesignCounts(ctx, []int64{1001, 1002, 1003})
	if err != nil {
		t.Fatalf("PendingDesignCounts: %v", err)
	}
	if len(counts) != 2 || counts[1001] != 2 || counts[1002] != 1 {
		t.Fatalf("counts = %#v, want map[1001:2 1002:1]", counts)
	}
	if _, ok := counts[1003]; ok {
		t.Fatalf("counts = %#v, version 1003 must be omitted", counts)
	}
}

func TestDesignQueryReaderRequiresPool(t *testing.T) {
	if _, err := NewQueryReader(nil); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
}
