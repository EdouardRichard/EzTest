package application_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// reviewGraphSnapshot rebuilds the canonical-hash input from the shared fixture.
func reviewGraphSnapshot(f *runFixture) domain.GraphSnapshot {
	nodes := make([]domain.CaseNode, 0, len(f.nodes))
	for _, node := range f.nodes {
		nodes = append(nodes, node)
	}
	steps := make(map[int64][]domain.CaseStep)
	for _, step := range f.steps {
		steps[step.NodeID] = append(steps[step.NodeID], step)
	}
	assertions := make(map[int64][]domain.CaseAssertion)
	for _, assertion := range f.assertions {
		assertions[assertion.NodeID] = append(assertions[assertion.NodeID], assertion)
	}
	sources := make([]domain.CaseSource, 0, len(f.sources))
	for _, source := range f.sources {
		sources = append(sources, source)
	}
	return domain.GraphSnapshot{
		VersionID:  runTestVersionID,
		Nodes:      nodes,
		Steps:      steps,
		Assertions: assertions,
		Sources:    sources,
	}
}

// TestFreezeRequiresNoBaseline covers the baseline uniqueness of spec §6.3/§6.4:
// freezing a version that already has a baseline is baseline_frozen and writes
// nothing.
func TestFreezeRequiresNoBaseline(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	seedReviewGraph(fixture)
	review := fixture.seedReview(9001, domain.ReviewApproved)
	fixture.baseline = &domain.CaseBaseline{
		ID:        1234,
		VersionID: runTestVersionID,
		ReviewID:  review.ID,
		NodeCount: 1,
		CaseCount: 1,
		GraphHash: "existing",
		FrozenBy:  7,
		FrozenAt:  fixture.now,
	}
	service := baselineServiceFor(fixture, nil)

	_, err := service.Freeze(ctx, runTestVersionID, 42)
	if err == nil {
		t.Fatal("freeze with an existing baseline must fail")
	}
	if reason := reviewDomainReason(t, err); reason != "baseline_frozen" {
		t.Fatalf("reason=%q want baseline_frozen", reason)
	}
	if fixture.baseline == nil || fixture.baseline.ID != 1234 || fixture.baseline.GraphHash != "existing" {
		t.Fatalf("baseline=%+v want the existing row untouched", fixture.baseline)
	}
	for id, node := range fixture.nodes {
		if node.Status != domain.NodeStatusDraft {
			t.Fatalf("node %d status=%s want draft", id, node.Status)
		}
	}
	if fixture.txCount != 0 {
		t.Fatalf("txCount=%d want 0", fixture.txCount)
	}
}

// TestFreezeHashAndCountsDeterministic covers the canonical graph hash and the
// node/case counts of spec §6.4: equal graphs freeze to the same hash, and the
// stored hash is the independently recomputed CanonicalGraphHash.
func TestFreezeHashAndCountsDeterministic(t *testing.T) {
	ctx := context.Background()

	freeze := func() (domain.CaseBaseline, *runFixture, domain.CaseReview, string) {
		fixture := newRunFixture()
		fixture.version.Status = "reviewing"
		seedReviewGraph(fixture)
		review := fixture.seedReview(9001, domain.ReviewApproved)
		expected := domain.CanonicalGraphHash(reviewGraphSnapshot(fixture))
		baseline, err := baselineServiceFor(fixture, nil).Freeze(ctx, runTestVersionID, 42)
		if err != nil {
			t.Fatalf("freeze: %v", err)
		}
		return baseline, fixture, review, expected
	}

	first, fixture, review, expected := freeze()
	if first.GraphHash != expected {
		t.Fatalf("graph hash=%q want recomputed %q", first.GraphHash, expected)
	}
	if len(first.GraphHash) != 64 {
		t.Fatalf("graph hash=%q want a lowercase hex sha256", first.GraphHash)
	}
	if first.NodeCount != 2 || first.CaseCount != 1 {
		t.Fatalf("counts node=%d case=%d want 2/1", first.NodeCount, first.CaseCount)
	}
	if first.ReviewID != review.ID {
		t.Fatalf("review id=%d want %d", first.ReviewID, review.ID)
	}
	if first.VersionID != runTestVersionID {
		t.Fatalf("version id=%d want %d", first.VersionID, runTestVersionID)
	}
	if first.FrozenBy != 42 {
		t.Fatalf("frozen by=%d want 42", first.FrozenBy)
	}
	if !first.FrozenAt.Equal(fixture.now) {
		t.Fatalf("frozen at=%v want %v", first.FrozenAt, fixture.now)
	}
	if first.CodeBaselineCommit == nil || *first.CodeBaselineCommit != *fixture.version.CodeBaselineCommit {
		t.Fatalf("code baseline commit=%v want %v", first.CodeBaselineCommit, fixture.version.CodeBaselineCommit)
	}
	if fixture.version.Status != "executing" {
		t.Fatalf("version status=%s want executing", fixture.version.Status)
	}
	for id, node := range fixture.nodes {
		if node.Status != domain.NodeStatusReviewed {
			t.Fatalf("node %d status=%s want reviewed", id, node.Status)
		}
	}
	if fixture.txCount != 1 {
		t.Fatalf("txCount=%d want 1", fixture.txCount)
	}

	second, _, _, _ := freeze()
	if second.GraphHash != first.GraphHash {
		t.Fatalf("equal graphs hashed differently: %q vs %q", second.GraphHash, first.GraphHash)
	}
	if second.NodeCount != first.NodeCount || second.CaseCount != first.CaseCount {
		t.Fatalf("equal graphs counted differently: %d/%d vs %d/%d",
			second.NodeCount, second.CaseCount, first.NodeCount, first.CaseCount)
	}
}
