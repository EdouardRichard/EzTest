package application_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// newIncrementalPlanFixture builds a continuing version that already has one
// materialized incremental_design run and two affected candidate nodes.
func newIncrementalPlanFixture(t *testing.T) (*runFixture, domain.CaseDesignRun) {
	t.Helper()
	fixture := newRunFixture()
	fixture.version.Status = "continuing"
	fixture.baseline = &domain.CaseBaseline{
		ID:        9000,
		VersionID: runTestVersionID,
		GraphHash: supplementGraphHash,
		FrozenAt:  fixture.now,
	}
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: runTestVersionID, Title: "Refund", Status: "clarified"},
	}
	fixture.steps[7001] = domain.CaseStep{
		ID: 7001, NodeID: 9001, Seq: 1, Action: "open the order", Source: domain.StepSourceAIGenerated,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.assertions[7002] = domain.CaseAssertion{
		ID: 7002, NodeID: 9001, Kind: domain.AssertionKindUI, Expression: "the refund is visible",
		Source: domain.StepSourceAIGenerated, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.nodes[9001] = domain.CaseNode{
		ID:        9001,
		VersionID: runTestVersionID,
		Kind:      domain.NodeKindCase,
		Title:     "Refund a paid order",
		Priority:  domain.PriorityP1,
		Source:    domain.NodeSourceAISupplement,
		Status:    domain.NodeStatusSupplemented,
		SortOrder: 9001,
		Revision:  1,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	ref := int64(7)
	fixture.sources[7101] = domain.CaseSource{
		ID: 7101, VersionID: runTestVersionID, NodeID: 9001, RefKind: domain.RefRequirement,
		RefID: &ref, Source: domain.NodeSourceAISupplement, Confidence: domain.ConfidenceHigh,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	run := domain.CaseDesignRun{
		ID:        7700,
		VersionID: runTestVersionID,
		Kind:      application.RunKindIncrementalDesign,
		Status:    domain.RunDone,
		Input:     []byte(`{"kind":"incremental_design","batchId":"77"}`),
		Result: []byte(`{
			"materialized": true,
			"newCaseIds": ["9001"],
			"duplicateSkipped": 0,
			"affectedCases": [
				{"nodeId":"42","confidence":"high","reason":"the refund API changed","source":"code","requirementIds":["7"]},
				{"nodeId":"43","confidence":"medium","reason":"the order page changed","source":"requirement"},
				{"nodeId":"44","confidence":"low","reason":"maybe related","source":"code"}
			],
			"unmappedChanges": [{"path":"src/refund.ts","summary":"new refund endpoint"}],
			"warnings": [{"code":"history_rewritten","message":"base fell back to the version baseline"}],
			"degraded": false
		}`),
		CreatedBy: 42,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	fixture.runs[run.ID] = run
	fixture.nodes[42] = domain.CaseNode{
		ID: 42, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "Pay order",
		Priority: domain.PriorityP1, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusExecuted,
		SortOrder: 42, Revision: 2, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.nodes[43] = domain.CaseNode{
		ID: 43, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "Cancel order",
		Priority: domain.PriorityP2, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusReviewed,
		SortOrder: 43, Revision: 2, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.nodes[44] = domain.CaseNode{
		ID: 44, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "List orders",
		Priority: domain.PriorityP2, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusReviewed,
		SortOrder: 44, Revision: 2, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	return fixture, run
}

// planService wires the production GraphService to the shared run fixture. The
// run rows are read through the graph transaction's Runs() repository.
func planService(fixture *runFixture) *application.GraphService {
	return planServiceWithVersions(fixture, runVersionLifecycle{fixture})
}

func planServiceWithVersions(fixture *runFixture, versions application.VersionLifecycle) *application.GraphService {
	return application.NewGraphService(application.GraphDeps{
		Graph:        fixture,
		Nodes:        runNodeRepo{f: fixture},
		Steps:        runStepRepo{f: fixture},
		Assertions:   runAssertionRepo{f: fixture},
		Sources:      runSourceRepo{f: fixture},
		Baselines:    runBaselineRepo{f: fixture},
		Versions:     versions,
		Batches:      fixture.batches,
		Requirements: runRequirementReader{f: fixture},
		Events:       runEventSink{f: fixture},
		IDs:          runIDGenerator{f: fixture},
		Clock:        runClock{f: fixture},
	})
}

// TestIncrementalPlanView covers spec §7.6: the latest incremental_design run,
// the supplemented new cases, the affected proposals, the unmapped changes, the
// deterministic defaultCaseIds and the warnings are projected from DB state.
func TestIncrementalPlanView(t *testing.T) {
	fixture, run := newIncrementalPlanFixture(t)
	view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
	if err != nil {
		t.Fatalf("incremental plan: %v", err)
	}

	if view.VersionID != runTestVersionID || view.Phase != "designing" {
		t.Fatalf("version/phase = %d/%q", view.VersionID, view.Phase)
	}
	if view.ParentBatchID == nil || *view.ParentBatchID != 77 {
		t.Fatalf("parent batch = %v, want 77", view.ParentBatchID)
	}
	if view.Run == nil || view.Run.ID != run.ID || view.Run.Status != domain.RunDone ||
		view.Run.Kind != application.RunKindIncrementalDesign {
		t.Fatalf("run = %+v", view.Run)
	}
	if len(view.NewCases) != 1 {
		t.Fatalf("new cases = %+v", view.NewCases)
	}
	newCase := view.NewCases[0]
	if newCase.NodeID != 9001 || newCase.Title != "Refund a paid order" ||
		newCase.Priority != domain.PriorityP1 || newCase.Status != domain.NodeStatusSupplemented ||
		newCase.Confidence != domain.ConfidenceHigh {
		t.Fatalf("new case = %+v", newCase)
	}
	if len(newCase.RequirementIDs) != 1 || newCase.RequirementIDs[0] != 7 {
		t.Fatalf("new case requirements = %v", newCase.RequirementIDs)
	}

	if len(view.AffectedCases) != 3 {
		t.Fatalf("affected cases = %+v", view.AffectedCases)
	}
	want := map[int64]struct {
		title      string
		confidence string
		selected   bool
	}{
		42: {title: "Pay order", confidence: domain.ConfidenceHigh, selected: true},
		43: {title: "Cancel order", confidence: domain.ConfidenceMedium, selected: true},
		44: {title: "List orders", confidence: domain.ConfidenceLow, selected: false},
	}
	for _, affected := range view.AffectedCases {
		expected, ok := want[affected.NodeID]
		if !ok {
			t.Fatalf("unexpected affected case %+v", affected)
		}
		if affected.Title != expected.title || affected.Confidence != expected.confidence ||
			affected.Selected != expected.selected {
			t.Fatalf("affected case %+v, want %+v", affected, expected)
		}
	}
	if len(view.UnmappedChanges) != 1 || view.UnmappedChanges[0].Path != "src/refund.ts" {
		t.Fatalf("unmapped changes = %+v", view.UnmappedChanges)
	}
	wantDefaults := []int64{42, 43, 9001}
	if len(view.DefaultCaseIDs) != len(wantDefaults) {
		t.Fatalf("default case ids = %v, want %v", view.DefaultCaseIDs, wantDefaults)
	}
	for index, id := range wantDefaults {
		if view.DefaultCaseIDs[index] != id {
			t.Fatalf("default case ids = %v, want %v", view.DefaultCaseIDs, wantDefaults)
		}
	}
	if !containsPlanWarning(view.Warnings, "history_rewritten") {
		t.Fatalf("warnings = %+v, want history_rewritten", view.Warnings)
	}
	if containsPlanWarning(view.Warnings, "impact_unavailable") {
		t.Fatalf("warnings = %+v, impact data was provided", view.Warnings)
	}
	if view.Degraded {
		t.Fatal("degraded = true, want false")
	}
}

// TestIncrementalPlanEmptyWithoutRun covers spec §7.6: a version without an
// incremental_design run returns an empty plan, not an error.
func TestIncrementalPlanEmptyWithoutRun(t *testing.T) {
	fixture := newRunFixture()
	fixture.version.Status = "continuing"
	view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
	if err != nil {
		t.Fatalf("incremental plan: %v", err)
	}
	if view.VersionID != runTestVersionID || view.Run != nil {
		t.Fatalf("view = %+v, want an empty plan", view)
	}
	if len(view.NewCases) != 0 || len(view.AffectedCases) != 0 || len(view.DefaultCaseIDs) != 0 {
		t.Fatalf("view = %+v, want empty collections", view)
	}
	if len(view.Warnings) != 0 || view.Degraded {
		t.Fatalf("warnings/degraded = %+v/%v", view.Warnings, view.Degraded)
	}
}

// TestIncrementalPlanDegradedAndImpactUnavailable covers the degraded flag and
// the synthesized impact_unavailable warning when the run carries no P4 data.
func TestIncrementalPlanDegradedAndImpactUnavailable(t *testing.T) {
	fixture := newRunFixture()
	fixture.version.Status = "continuing"
	fixture.runs[8800] = domain.CaseDesignRun{
		ID:        8800,
		VersionID: runTestVersionID,
		Kind:      application.RunKindIncrementalDesign,
		Status:    domain.RunDegraded,
		Input:     []byte(`{"kind":"incremental_design","batchId":"78"}`),
		Result:    []byte(`{"degraded":true,"affectedCases":[]}`),
		CreatedBy: 42,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
	if err != nil {
		t.Fatalf("incremental plan: %v", err)
	}
	if view.Run == nil || view.Run.Status != domain.RunDegraded || !view.Degraded {
		t.Fatalf("run/degraded = %+v/%v", view.Run, view.Degraded)
	}
	if !containsPlanWarning(view.Warnings, "impact_unavailable") {
		t.Fatalf("warnings = %+v, want impact_unavailable", view.Warnings)
	}
}

// TestIncrementalPlanUsesContinuePhaseReader covers the optional P7 phase port:
// the plan reports the version's continue_phase when the version adapter exposes
// it.
func TestIncrementalPlanUsesContinuePhaseReader(t *testing.T) {
	fixture := newRunFixture()
	fixture.version.Status = "continuing"
	view, err := planServiceWithVersions(fixture, phasedVersionLifecycle{runVersionLifecycle{f: fixture}, "executing"}).
		IncrementalPlan(context.Background(), runTestVersionID)
	if err != nil {
		t.Fatalf("incremental plan: %v", err)
	}
	if view.Phase != "executing" {
		t.Fatalf("phase = %q, want executing", view.Phase)
	}
}

// TestIncrementalPlanNewCasesScopedToRun covers the newCases attribution rule:
// only nodes declared by the run result (or traced to the run through ai_run
// sources) are listed; an untagged manual ai_supplement node is never mixed in,
// and a run with no attribution lists no new cases at all.
func TestIncrementalPlanNewCasesScopedToRun(t *testing.T) {
	t.Run("untagged manual supplement is excluded", func(t *testing.T) {
		fixture, _ := newIncrementalPlanFixture(t)
		fixture.nodes[9002] = domain.CaseNode{
			ID: 9002, VersionID: runTestVersionID, Kind: domain.NodeKindCase,
			Title: "Manually added supplement", Priority: domain.PriorityP2,
			Source: domain.NodeSourceAISupplement, Status: domain.NodeStatusSupplemented,
			SortOrder: 9002, Revision: 1, CreatedAt: fixture.now, UpdatedAt: fixture.now,
		}
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		for _, newCase := range view.NewCases {
			if newCase.NodeID == 9002 {
				t.Fatalf("new cases = %+v, the untagged manual supplement leaked into the run plan", view.NewCases)
			}
		}
		if len(view.NewCases) != 1 || view.NewCases[0].NodeID != 9001 {
			t.Fatalf("new cases = %+v, want only the declared node 9001", view.NewCases)
		}
	})

	t.Run("run without attribution lists no new cases", func(t *testing.T) {
		fixture, run := newIncrementalPlanFixture(t)
		run.Result = []byte(`{"materialized":true}`)
		fixture.runs[run.ID] = run
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if len(view.NewCases) != 0 {
			t.Fatalf("new cases = %+v, want none without run attribution", view.NewCases)
		}
	})
}

func containsPlanWarning(warnings []application.IncrementalPlanWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
