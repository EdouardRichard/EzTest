//go:build integration

package chain_test

// This file exercises the mindmap-side chain provider against the real
// PostgreSQL schema: the complete case layer (requirement sources aggregated,
// step/assertion counts in subqueries) and the requirement coverage matrix
// (spec §5.2 queries 3–5). It is skipped unless EZTEST_TEST_DATABASE_URL points
// at a dedicated *_test database (testutil.OpenTestDB enforces that).

import (
	"context"
	"reflect"
	"testing"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	mindmapchain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/chain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mindmapChainSeedVersion inserts one version row.
func mindmapChainSeedVersion(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, name, status, revision, created_by, updated_by)
		VALUES ($1, 'mindmap-chain-fixture', 'designing', 1, 1, 1)`, id)
	if err != nil {
		t.Fatalf("seed version %d: %v", id, err)
	}
}

// mindmapChainSeedRequirement inserts one requirement with an explicit status.
func mindmapChainSeedRequirement(t *testing.T, pool *pgxpool.Pool, id, versionID int64, status string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO requirement (id, version_id, title, priority, status, created_by, updated_by)
		VALUES ($1, $2, 'requirement', 'must', $3, 1, 1)`, id, versionID, status)
	if err != nil {
		t.Fatalf("seed requirement %d: %v", id, err)
	}
}

// mindmapChainSeedNode inserts one case_node row.
func mindmapChainSeedNode(t *testing.T, pool *pgxpool.Pool, id, versionID int64, kind, title, source, status string, sortOrder int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO case_node
			(id, version_id, kind, title, priority, source, status, sort_order, created_by, updated_by)
		VALUES ($1, $2, $3, $4, 'P1', $5, $6, $7, 1, 1)`,
		id, versionID, kind, title, source, status, sortOrder)
	if err != nil {
		t.Fatalf("seed case node %d: %v", id, err)
	}
}

// mindmapChainSeedSource inserts one case_source requirement reference.
func mindmapChainSeedSource(t *testing.T, pool *pgxpool.Pool, id, versionID, nodeID, requirementID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO case_source (id, version_id, node_id, ref_kind, ref_id, source, ref_key)
		VALUES ($1, $2, $3, 'requirement', $4, 'user_input', $5)`,
		id, versionID, nodeID, requirementID, "source-"+string(rune('a'+id%26)))
	if err != nil {
		t.Fatalf("seed case source %d: %v", id, err)
	}
}

// TestMindmapChainReadsCasesAndCoverage pins both mindmap provider methods
// against real rows: the case layer includes every case of the version (no
// status/source SQL filtering), scenario nodes are excluded, requirement refs
// are aggregated and de-duplicated, step/assertion counts are batched, and the
// coverage matrix mirrors mindmap domain.BuildCoverage (clarified requirements
// only, case nodes only).
func TestMindmapChainReadsCasesAndCoverage(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	const (
		versionID    = int64(301)
		otherVersion = int64(302)
		reqClarified = int64(3201)
		reqDraft     = int64(3202)
		reqForeign   = int64(3203)
	)
	mindmapChainSeedVersion(t, pool, versionID)
	mindmapChainSeedVersion(t, pool, otherVersion)
	mindmapChainSeedRequirement(t, pool, reqClarified, versionID, "clarified")
	mindmapChainSeedRequirement(t, pool, reqDraft, versionID, "draft")
	mindmapChainSeedRequirement(t, pool, reqForeign, otherVersion, "clarified")

	mindmapChainSeedNode(t, pool, 3301, versionID, "case", "first case", "user_input", "executed", 0)
	mindmapChainSeedNode(t, pool, 3302, versionID, "case", "supplement case", "ai_supplement", "needs_revalidation", 1)
	mindmapChainSeedNode(t, pool, 3303, versionID, "case", "draft case", "user_input", "draft", 2)
	mindmapChainSeedNode(t, pool, 3399, versionID, "scenario", "scenario", "user_input", "draft", 3)
	mindmapChainSeedNode(t, pool, 3401, otherVersion, "case", "foreign case", "user_input", "executed", 0)

	// Case 3301 references the clarified requirement twice (different ref keys)
	// and case 3302 references both requirements; the aggregate must de-duplicate.
	mindmapChainSeedSource(t, pool, 3501, versionID, 3301, reqClarified)
	mindmapChainSeedSource(t, pool, 3502, versionID, 3301, reqClarified)
	mindmapChainSeedSource(t, pool, 3503, versionID, 3302, reqClarified)
	mindmapChainSeedSource(t, pool, 3504, versionID, 3302, reqDraft)
	mindmapChainSeedSource(t, pool, 3505, versionID, 3303, reqDraft)
	// The scenario and the foreign-version case must never reach the chain case
	// layer or the coverage matrix.
	mindmapChainSeedSource(t, pool, 3506, versionID, 3399, reqClarified)
	mindmapChainSeedSource(t, pool, 3507, otherVersion, 3401, reqForeign)

	if _, err := pool.Exec(ctx, `
		INSERT INTO case_step (id, node_id, seq, action, source)
		VALUES (3601, 3301, 1, 'open', 'user_input'), (3602, 3301, 2, 'submit', 'user_input'),
		       (3603, 3303, 1, 'open', 'user_input')`); err != nil {
		t.Fatalf("seed steps: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_assertion (id, node_id, kind, expression, source)
		VALUES (3701, 3301, 'ui', 'visible', 'user_input')`); err != nil {
		t.Fatalf("seed assertions: %v", err)
	}

	reads := mindmapchain.New(pool)

	cases, err := reads.Cases(ctx, versionID)
	if err != nil {
		t.Fatalf("Cases: %v", err)
	}
	if len(cases) != 3 {
		t.Fatalf("cases = %d rows, want the complete case layer of 3 (scenario/foreign excluded)", len(cases))
	}
	want := []chainapp.CaseRow{
		{ID: 3301, Title: "first case", Status: "executed", Source: "user_input", Priority: "P1",
			RequirementIDs: []int64{reqClarified}, StepCount: 2, AssertionCount: 1, SortOrder: 0},
		{ID: 3302, Title: "supplement case", Status: "needs_revalidation", Source: "ai_supplement",
			Priority: "P1", RequirementIDs: []int64{reqClarified, reqDraft},
			StepCount: 0, AssertionCount: 0, SortOrder: 1},
		{ID: 3303, Title: "draft case", Status: "draft", Source: "user_input", Priority: "P1",
			RequirementIDs: []int64{reqDraft}, StepCount: 1, AssertionCount: 0, SortOrder: 2},
	}
	for index := range want {
		if !reflect.DeepEqual(cases[index], want[index]) {
			t.Fatalf("case %d = %+v, want %+v", index, cases[index], want[index])
		}
	}

	coverage, err := reads.Coverage(ctx, versionID)
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	wantCoverage := []chainapp.CoverageCell{{RequirementID: reqClarified, CaseIDs: []int64{3301, 3302}}}
	if !reflect.DeepEqual(coverage, wantCoverage) {
		t.Fatalf("coverage = %+v, want %+v (scenario/stale/foreign excluded)",
			coverage, wantCoverage)
	}
}
