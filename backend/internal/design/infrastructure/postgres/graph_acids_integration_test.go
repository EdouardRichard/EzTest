//go:build integration

package designpg_test

import (
	"context"
	"testing"

	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestReadGraphPopulatesRequirementACIDsIntegration guards the T19 fix: the
// design graph reader must carry the requirement's current AC id set (and
// priority/sut_description), identical to RequirementContext. Before the fix
// readGraph selected only id/version/revision/title/status, so ACIDs was always
// empty — which made the judge scheduler's coverage precheck (NeedsACRepair) and
// the shared review gate (MissingACs) trivially pass and silently disabled the
// SC-E2 coverage gate / SC-E4 AC-gap repair. The application unit tests
// hand-populated ACIDs on their fakes and never exercised this real reader.
func TestReadGraphPopulatesRequirementACIDsIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	seedVersion(t, pool, 1001, false)
	// defaultSeedCriteria carries two ACs (AC-1 and the two-digit AC-10).
	seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "登录", 1)
	seedScenario(t, pool, 1001, 3001, 3002, 5001, "正常登录", 0)

	repo := designpg.New(pool)
	graph, err := repo.ReadGraph(ctx, 1001)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	if len(graph.Requirements) != 1 {
		t.Fatalf("requirements = %d, want 1", len(graph.Requirements))
	}
	ref := graph.Requirements[0].Requirement
	want := []string{"AC-1", "AC-10"}
	if len(ref.ACIDs) != len(want) {
		t.Fatalf("ReadGraph ACIDs = %v, want %v (empty set disables the coverage gate)", ref.ACIDs, want)
	}
	for i, id := range want {
		if ref.ACIDs[i] != id {
			t.Fatalf("ReadGraph ACIDs = %v, want %v (id order)", ref.ACIDs, want)
		}
	}
	if ref.Priority != "P2" {
		t.Fatalf("ReadGraph priority = %q, want P2", ref.Priority)
	}
	if ref.SutDescription != seedSutDescription {
		t.Fatalf("ReadGraph sut_description = %q, want %q", ref.SutDescription, seedSutDescription)
	}

	// Single truth: the graph ref and RequirementContext must agree on the AC set.
	contextRef, err := repo.RequirementContext(ctx, 1001, 3001)
	if err != nil {
		t.Fatalf("RequirementContext: %v", err)
	}
	if len(contextRef.ACIDs) != len(ref.ACIDs) {
		t.Fatalf("RequirementContext ACIDs = %v, ReadGraph ACIDs = %v; the two readers diverge", contextRef.ACIDs, ref.ACIDs)
	}
	for i := range ref.ACIDs {
		if contextRef.ACIDs[i] != ref.ACIDs[i] {
			t.Fatalf("RequirementContext ACIDs = %v, ReadGraph ACIDs = %v; the two readers diverge", contextRef.ACIDs, ref.ACIDs)
		}
	}
}
