package executionapp

// Task 95 (PO O8) pre-execution completeness gate.
//
// The PO trial confirmed and executed AC-FR-011-01 from a case_version carrying
// 1 step and 0 assertions (the design claimed 5 steps / 3 assertions). The
// executor ran the single step and the run settled inconclusive(incomplete),
// which looks like an execution-quality problem even though the version could
// never produce a pass. An incomplete version must instead be refused by a
// typed block before any intent/wave/case_run exists.

import (
	"context"
	"net/http"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestIncompleteCaseVersionIsBlockedBeforeExecution pins the gate: a selected
// batch containing an incomplete case version returns the typed
// incomplete_case_version block and creates no intent (not even for the
// complete sibling in the same selection), so no wave and no 0-assertion run can
// masquerade as an inconclusive execution result.
func TestIncompleteCaseVersionIsBlockedBeforeExecution(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	complete := task6Snapshot(300, 5001)
	incomplete := task6Snapshot(301, 5001)
	incomplete.Assertions = nil
	f.cases.cases[300] = complete
	f.cases.cases[301] = incomplete
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	_, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{complete.CaseID, incomplete.CaseID})
	apiErr := requireAPIError(t, err, "incomplete_case_version")
	if apiErr.Status != http.StatusConflict {
		t.Fatalf("incomplete_case_version status = %d, want 409", apiErr.Status)
	}
	if got, _ := apiErr.Details["case_version_id"].(string); got != "301" {
		t.Fatalf("details case_version_id = %v, want 301", apiErr.Details["case_version_id"])
	}
	if got, _ := apiErr.Details["step_count"].(int); got != 1 {
		t.Fatalf("details step_count = %v, want 1", apiErr.Details["step_count"])
	}
	if got, _ := apiErr.Details["assertion_count"].(int); got != 0 {
		t.Fatalf("details assertion_count = %v, want 0", apiErr.Details["assertion_count"])
	}
	if next, _ := apiErr.Details["next_action"].(string); next == "" {
		t.Fatalf("details = %#v, want repair next_action guidance", apiErr.Details)
	}

	active, err := f.intents.ListActive(ctx, 100)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("blocked batch created %d intent(s), want 0: %+v", len(active), active)
	}
	if err := f.scheduler.planVersion(ctx, 100); err != nil {
		t.Fatalf("planVersion after blocked batch: %v", err)
	}
	if wave, _ := f.waves.GetActive(ctx, 100); wave != nil {
		t.Fatalf("blocked batch created a wave: %+v", wave)
	}

	// A complete selection is still executable: the gate blocks only incomplete
	// versions and never weakens normal execution.
	created, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{complete.CaseID})
	if err != nil {
		t.Fatalf("complete selection: %v", err)
	}
	if created != 1 {
		t.Fatalf("complete selection created %d intent(s), want 1", created)
	}
	active, err = f.intents.ListActive(ctx, 100)
	if err != nil || len(active) != 1 || active[0].CaseVersionID != complete.CaseVersionID {
		t.Fatalf("complete selection intents = %+v err=%v, want only case version %d",
			active, err, complete.CaseVersionID)
	}
}

// TestIncompleteCaseVersionBlocksRetestIntent pins the second pre-execution
// entry point: the manual/retest intent API must not queue an incomplete case
// version either. Otherwise a user retrying the PO O8 case would still get a
// 0-assertion run even though the batch gate refuses it.
func TestIncompleteCaseVersionBlocksRetestIntent(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	incomplete := task6Snapshot(301, 5001)
	incomplete.Assertions = nil
	f.cases.cases[301] = incomplete
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	_, err := f.intent.CreateManual(ctx, 100, incomplete.CaseVersionID, execution.PurposeRetest, 7)
	apiErr := requireAPIError(t, err, "incomplete_case_version")
	if apiErr.Status != http.StatusConflict {
		t.Fatalf("incomplete_case_version status = %d, want 409", apiErr.Status)
	}
	active, err := f.intents.ListActive(ctx, 100)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("blocked retest created %d intent(s), want 0: %+v", len(active), active)
	}
}
