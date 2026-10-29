package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// seedSupplementExecutionCase stores one materialized ai_supplement case created
// after the baseline froze.
func seedSupplementExecutionCase(fixture *runFixture, id int64, status string) domain.CaseNode {
	node := domain.CaseNode{
		ID:            id,
		VersionID:     runTestVersionID,
		Kind:          domain.NodeKindCase,
		Title:         "supplement case",
		Priority:      domain.PriorityP1,
		Preconditions: "the user is signed in",
		TestData:      []byte(`{"account":"alice"}`),
		Source:        domain.NodeSourceAISupplement,
		Status:        status,
		SortOrder:     int(id),
		Revision:      1,
		CreatedAt:     fixture.now.Add(time.Hour),
		UpdatedAt:     fixture.now.Add(time.Hour),
	}
	fixture.nodes[id] = node
	step := domain.CaseStep{
		ID:        id*100 + 1,
		NodeID:    id,
		Seq:       1,
		Action:    "refund the order",
		Expected:  "the refund is accepted",
		Source:    domain.StepSourceAIGenerated,
		CreatedAt: fixture.now.Add(time.Hour),
		UpdatedAt: fixture.now.Add(time.Hour),
	}
	fixture.steps[step.ID] = step
	assertion := domain.CaseAssertion{
		ID:         id*100 + 2,
		NodeID:     id,
		StepID:     graphInt64Ptr(step.ID),
		Kind:       domain.AssertionKindUI,
		Expression: "the refund banner is visible",
		Source:     domain.StepSourceAIGenerated,
		CreatedAt:  fixture.now.Add(time.Hour),
		UpdatedAt:  fixture.now.Add(time.Hour),
	}
	fixture.assertions[assertion.ID] = assertion
	requirementID := int64(7)
	fixture.sources[id*100+3] = domain.CaseSource{
		ID:         id*100 + 3,
		VersionID:  runTestVersionID,
		NodeID:     id,
		RefKind:    domain.RefRequirement,
		RefID:      &requirementID,
		Source:     domain.NodeSourceAISupplement,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  fixture.now.Add(time.Hour),
		UpdatedAt:  fixture.now.Add(time.Hour),
	}
	return node
}

// TestExecutionSnapshotSelectsSupplementNodes covers the P7 selection rule: a
// materialized supplement node is created after the freeze but is selectable by
// the non-baseline batches, while a late user-authored node is still rejected.
func TestExecutionSnapshotSelectsSupplementNodes(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionCase(fixture, 2000, domain.NodeStatusReviewed)
	seedSupplementExecutionCase(fixture, 3000, domain.NodeStatusSupplemented)
	seedSupplementExecutionCase(fixture, 3100, domain.NodeStatusMerged)
	seedSupplementExecutionCase(fixture, 3200, domain.NodeStatusNeedsRevalidation)
	service := executionServiceFor(fixture, baselines)

	cases, err := service.SnapshotCases(ctx, runTestVersionID, []int64{2000, 3000, 3100, 3200})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(cases) != 4 {
		t.Fatalf("snapshots = %d, want 4", len(cases))
	}
	for index, id := range []int64{2000, 3000, 3100, 3200} {
		if cases[index].NodeID != id {
			t.Fatalf("snapshot node ids = %v", executionSnapshotIDs(cases))
		}
	}

	// The P6 baseline guard still rejects a late user-authored case: only
	// ai_supplement nodes are the P7 exception.
	late := seedExecutionCase(fixture, 5000, domain.NodeStatusReviewed)
	late.CreatedAt = fixture.baseline.FrozenAt.Add(time.Hour)
	fixture.nodes[late.ID] = late
	_, err = service.SnapshotCases(ctx, runTestVersionID, []int64{5000})
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_ids")
}

// TestMarkExecutedAcceptsMergedAndNeedsRevalidation covers the spec §5.2 matrix:
// a merged or needs_revalidation case becomes executed after its batch ran.
func TestMarkExecutedAcceptsMergedAndNeedsRevalidation(t *testing.T) {
	for _, status := range []string{domain.NodeStatusMerged, domain.NodeStatusNeedsRevalidation} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			fixture, baselines := newExecutionFixture()
			seedSupplementExecutionCase(fixture, 3000, status)
			service := executionServiceFor(fixture, baselines)

			if err := service.MarkExecuted(ctx, runTestVersionID, []int64{3000}); err != nil {
				t.Fatalf("mark executed: %v", err)
			}
			if got := fixture.nodes[3000].Status; got != domain.NodeStatusExecuted {
				t.Fatalf("status = %q, want executed", got)
			}
			if len(fixture.events) != 1 || fixture.events[0].Type != domain.EventCaseStatusChanged {
				t.Fatalf("events = %+v", fixture.events)
			}
			if fixture.events[0].Detail["from"] != status || fixture.events[0].Detail["to"] != domain.NodeStatusExecuted {
				t.Fatalf("event detail = %+v", fixture.events[0].Detail)
			}
		})
	}
}

// TestMarkMergedTransitionsSupplementedNodes covers the batch-adoption marker of
// spec §5.3: selecting a supplemented node marks it merged, once.
func TestMarkMergedTransitionsSupplementedNodes(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedSupplementExecutionCase(fixture, 3000, domain.NodeStatusSupplemented)
	seedExecutionCase(fixture, 42, domain.NodeStatusReviewed)
	service := executionServiceFor(fixture, baselines)

	if err := service.MarkMerged(ctx, runTestVersionID, []int64{3000}); err != nil {
		t.Fatalf("mark merged: %v", err)
	}
	if got := fixture.nodes[3000].Status; got != domain.NodeStatusMerged {
		t.Fatalf("status = %q, want merged", got)
	}
	if len(fixture.events) != 1 || fixture.events[0].Type != domain.EventCaseStatusChanged {
		t.Fatalf("events = %+v", fixture.events)
	}
	if fixture.events[0].Detail["from"] != domain.NodeStatusSupplemented ||
		fixture.events[0].Detail["to"] != domain.NodeStatusMerged {
		t.Fatalf("event detail = %+v", fixture.events[0].Detail)
	}

	// Idempotent: an already merged node is a no-op.
	revision := fixture.nodes[3000].Revision
	if err := service.MarkMerged(ctx, runTestVersionID, []int64{3000}); err != nil {
		t.Fatalf("second mark merged: %v", err)
	}
	if fixture.nodes[3000].Revision != revision || len(fixture.events) != 1 {
		t.Fatal("the second MarkMerged must be a no-op")
	}

	if err := service.MarkMerged(ctx, runTestVersionID, []int64{42}); err == nil {
		t.Fatal("marking a non-supplemented node merged must be rejected")
	}
}

// TestMarkMergedBySetsUpdatedByFromActor covers the actor attribution of the P7
// merge marker: the execution batch actor becomes the node's updated_by.
func TestMarkMergedBySetsUpdatedByFromActor(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedSupplementExecutionCase(fixture, 3000, domain.NodeStatusSupplemented)
	service := executionServiceFor(fixture, baselines)

	if err := service.MarkMergedBy(ctx, runTestVersionID, 77, []int64{3000}); err != nil {
		t.Fatalf("mark merged by actor: %v", err)
	}
	if got := fixture.nodes[3000].UpdatedBy; got != 77 {
		t.Fatalf("updated_by = %d, want the actor 77", got)
	}

	// The legacy port path (no actor) keeps the stored updated_by.
	seedSupplementExecutionCase(fixture, 3100, domain.NodeStatusSupplemented)
	if err := service.MarkMerged(ctx, runTestVersionID, []int64{3100}); err != nil {
		t.Fatalf("legacy mark merged: %v", err)
	}
	if got := fixture.nodes[3100].UpdatedBy; got != 0 {
		t.Fatalf("legacy updated_by = %d, want the stored value 0", got)
	}
}
