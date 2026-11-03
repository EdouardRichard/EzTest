package executionapp

// Stage/selection gate tests (Task 78). A single case confirmation must not
// create an execution intent (and therefore no wave) while the version is still
// at the case gate: only the explicit case hard gate / execute.batch trigger may
// refresh initial intents, and it must only do so for the selected cases.

import (
	"context"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestSingleConfirmDoesNotAutoRunWave pins the QA-F2 fix: a case version that is
// merely confirmed at the case gate (stage=case_review) does not become an
// execution intent/wave. The same case becomes executable only after the
// explicit batch execute (stage=executing) triggers the scoped refresh.
func TestSingleConfirmDoesNotAutoRunWave(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)

	binding := task6Binding(100, "running", 5001)
	binding.Stage = "case_review"
	f.versions.bindings[100] = binding

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil {
		t.Fatalf("EnsureInitialIntents at case_review: %v", err)
	}
	if created != 0 {
		t.Fatalf("single confirmation created %d initial intent(s) at case_review, want 0", created)
	}
	active, err := f.intents.ListActive(ctx, 100)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("single confirmation created active intents at case_review: %+v", active)
	}
	if err := f.scheduler.planVersion(ctx, 100); err != nil {
		t.Fatalf("planVersion at case_review: %v", err)
	}
	if wave, _ := f.waves.GetActive(ctx, 100); wave != nil {
		t.Fatalf("single confirmation auto-created a wave at case_review: %+v", wave)
	}

	// Explicit batch execute: the version is now executing and the execute.batch
	// stage job refreshes intents for the selected case only.
	binding.Stage = "executing"
	f.versions.bindings[100] = binding
	created, err = f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{300 + 1000})
	if err != nil {
		t.Fatalf("EnsureInitialIntentsForCases: %v", err)
	}
	if created != 1 {
		t.Fatalf("explicit batch execute created %d intent(s), want 1", created)
	}
	active, _ = f.intents.ListActive(ctx, 100)
	if len(active) != 1 || active[0].CaseVersionID != 300 {
		t.Fatalf("active intents = %+v, want only case version 300", active)
	}
	if err := f.scheduler.planVersion(ctx, 100); err != nil {
		t.Fatalf("planVersion after batch execute: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("explicit batch execute did not create a wave")
	}
	runs := f.waves.runs[wave.ID]
	if len(runs) != 1 || runs[0].CaseVersionID != 300 {
		t.Fatalf("wave case runs = %+v, want only case version 300", runs)
	}
}

// TestSelectedCaseNotExecutableCarriesNextAction pins the typed guidance of the
// D23 selection guard: a stale/unconfirmed selected case must tell the caller
// how to recover instead of only failing with a bare 409 conflict.
func TestSelectedCaseNotExecutableCarriesNextAction(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	_, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{999999})
	apiErr := requireAPIError(t, err, "selected_case_not_executable")
	if next, _ := apiErr.Details["next_action"].(string); next == "" {
		t.Fatalf("selected_case_not_executable details = %#v, want next_action guidance", apiErr.Details)
	}
}

// TestBatchExecuteSelectedOnly pins the D23 selected-only contract end to end:
// the execute.batch scope must not widen to every confirmed case of the version.
func TestBatchExecuteSelectedOnly(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5001)
	f.cases.cases[302] = task6Snapshot(302, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	selectedCaseID := f.cases.cases[300].CaseID
	created, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{selectedCaseID})
	if err != nil {
		t.Fatalf("EnsureInitialIntentsForCases: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1 (only the selected case)", created)
	}
	active, err := f.intents.ListActive(ctx, 100)
	if err != nil || len(active) != 1 {
		t.Fatalf("active intents = %+v err=%v, want 1", active, err)
	}
	if active[0].CaseVersionID != 300 {
		t.Fatalf("intent case version = %d, want 300", active[0].CaseVersionID)
	}
	for _, intent := range active {
		if intent.CaseVersionID == 301 || intent.CaseVersionID == 302 {
			t.Fatalf("unselected case version %d was enqueued: %+v", intent.CaseVersionID, intent)
		}
	}

	// Repeated execute.batch dispatch is idempotent for the same selection.
	again, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{selectedCaseID})
	if err != nil {
		t.Fatalf("second EnsureInitialIntentsForCases: %v", err)
	}
	if again != 0 {
		t.Fatalf("second selected refresh created %d intent(s), want 0", again)
	}

	if err := f.scheduler.planVersion(ctx, 100); err != nil {
		t.Fatalf("planVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("selected batch did not create a wave")
	}
	runs := f.waves.runs[wave.ID]
	if len(runs) != 1 || runs[0].CaseVersionID != 300 {
		t.Fatalf("wave case runs = %+v, want only the selected case version", runs)
	}
	if wave.Trigger != execution.WaveTriggerAuto {
		t.Fatalf("wave trigger = %q, want auto", wave.Trigger)
	}
}
