//go:build integration

package designpg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func p6DesignInt64Ptr(value int64) *int64 { return &value }

func p6SeedDesignVersion(t *testing.T, pool *pgxpool.Pool, id int64, title string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, title, status, created_by)
		VALUES ($1, $2, 'initial', 1)
	`, id, title); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

func p6SeedDesignScenario(t *testing.T, pool *pgxpool.Pool, versionID, scenarioID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES ($1, $2, 2001, 2002, $3)
	`, scenarioID, versionID, "scenario-"+time.Now().Format("150405.000000000")); err != nil {
		t.Fatalf("seed case scenario: %v", err)
	}
}

func p6SeedDesignCase(t *testing.T, pool *pgxpool.Pool, caseID, caseVersionID, scenarioID int64, status, title string, createdAt, updatedAt time.Time) {
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

func p6SeedDesignCaseVersion(t *testing.T, pool *pgxpool.Pool, caseID, caseVersionID int64, revision int, status, title string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_version (id, case_id, revision, title, content_hash, status)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, caseVersionID, caseID, revision, title, strings.Repeat("b", 64), status); err != nil {
		t.Fatalf("seed case version: %v", err)
	}
}

func p6SeedDesignRequirement(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status string) {
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

func p6SeedDesignScope(t *testing.T, pool *pgxpool.Pool, id, versionID, requirementID int64, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO design_scope (id, version_id, requirement_id, status)
		VALUES ($1, $2, $3, $4)
	`, id, versionID, requirementID, status); err != nil {
		t.Fatalf("seed design scope: %v", err)
	}
}

func TestP6DesignQueryReaderCountCaseConfirmation(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedDesignScenario(t, pool, 1001, 5001)
	p6SeedDesignScenario(t, pool, 1002, 5002)
	p6SeedDesignCase(t, pool, 7001, 8001, 5001, "draft", "draft case", base, base)
	p6SeedDesignCase(t, pool, 7002, 8002, 5001, "confirmed", "confirmed case", base, base)
	p6SeedDesignCase(t, pool, 7003, 8003, 5001, "stale", "stale case", base, base)
	p6SeedDesignCase(t, pool, 7011, 8011, 5002, "confirmed", "other confirmed", base, base)
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

	num, denom, err = reader.CountCaseConfirmation(ctx, p6DesignInt64Ptr(1001))
	if err != nil {
		t.Fatalf("CountCaseConfirmation version: %v", err)
	}
	if num != 1 || denom != 3 {
		t.Fatalf("version counts = %d/%d, want 1/3", num, denom)
	}

	num, denom, err = reader.CountCaseConfirmation(ctx, p6DesignInt64Ptr(1003))
	if err != nil {
		t.Fatalf("CountCaseConfirmation empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6DesignQueryReaderSearchCases(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedDesignScenario(t, pool, 1001, 5001)

	p6SeedDesignCase(t, pool, 7001, 8001, 5001, "draft", "Login success", base, base.Add(time.Hour))
	p6SeedDesignCase(t, pool, 7002, 8002, 5001, "confirmed", "Login failure", base, base.Add(2*time.Hour))
	p6SeedDesignCase(t, pool, 7003, 8003, 5001, "confirmed", "Checkout", base, base.Add(3*time.Hour))

	// 7004's historical revision matches q but its current revision does not.
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (7004, 5001, 8004, 'confirmed')
	`); err != nil {
		t.Fatalf("seed case entity: %v", err)
	}
	p6SeedDesignCaseVersion(t, pool, 7004, 8104, 1, "superseded", "Login old")
	p6SeedDesignCaseVersion(t, pool, 7004, 8004, 2, "confirmed", "Other")

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

func TestP6DesignQueryReaderListDraftCases(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedDesignScenario(t, pool, 1001, 5001)
	p6SeedDesignCase(t, pool, 7001, 8001, 5001, "draft", "Older draft", base, base)
	p6SeedDesignCase(t, pool, 7002, 8002, 5001, "draft", "Newer draft", base.Add(time.Hour), base.Add(time.Hour))
	p6SeedDesignCase(t, pool, 7003, 8003, 5001, "confirmed", "Confirmed", base.Add(2*time.Hour), base.Add(2*time.Hour))

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

func TestP6DesignQueryReaderPendingDesignCounts(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	p6SeedDesignVersion(t, pool, 1001, "Login SUT")
	p6SeedDesignVersion(t, pool, 1002, "Checkout SUT")

	// 1001: two clarified requirements without an active scope (2001 and 2003);
	// an active scope clears 2002. Draft/closed requirements are not pending.
	p6SeedDesignRequirement(t, pool, 1001, 2001, 3001, "clarified")
	p6SeedDesignRequirement(t, pool, 1001, 2002, 3002, "clarified")
	p6SeedDesignRequirement(t, pool, 1001, 2003, 3003, "clarified")
	p6SeedDesignRequirement(t, pool, 1001, 2004, 3004, "draft")
	p6SeedDesignRequirement(t, pool, 1001, 2005, 3005, "closed")
	p6SeedDesignScope(t, pool, 4001, 1001, 2002, "active")
	p6SeedDesignScope(t, pool, 4002, 1001, 2003, "closed")
	// 1002 has one pending requirement.
	p6SeedDesignRequirement(t, pool, 1002, 2011, 3011, "clarified")

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

func TestP6DesignQueryReaderRequiresPool(t *testing.T) {
	if _, err := NewQueryReader(nil); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
}
