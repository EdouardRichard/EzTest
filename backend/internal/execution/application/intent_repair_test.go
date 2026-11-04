package executionapp

// Repair-scan selection tests (Task 82). The startup/leader repair scan must not
// widen an execute.batch selection to every confirmed case after a restart or a
// leader failover: it may only refresh the case_ids frozen on the persisted
// execute.batch stage-entry work item (D23). A version with no persisted
// selection is a typed skip, never an unscoped backlog run.

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeBatchSelectionReader is the narrow work-item projection: the newest
// execute.batch item's case_ids, or found=false when the version never enqueued
// the stage-entry job.
type fakeBatchSelectionReader struct {
	mu      sync.Mutex
	caseIDs []int64
	found   bool
	err     error
	calls   int
}

func (f *fakeBatchSelectionReader) ExecuteBatchSelection(_ context.Context, _ int64) ([]int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return append([]int64(nil), f.caseIDs...), f.found, f.err
}

// TestRepairScanScopesToPersistedBatchSelection pins D23 for the repair path:
// with three current confirmed cases but a persisted selection of one, a restart
// scan creates exactly one initial intent and the planner only waves that case.
func TestRepairScanScopesToPersistedBatchSelection(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5001)
	f.cases.cases[302] = task6Snapshot(302, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	selectedCaseID := f.cases.cases[300].CaseID
	f.selections.caseIDs = []int64{selectedCaseID}

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if created != 1 {
		t.Fatalf("repair scan created %d intent(s), want 1 (persisted selection only)", created)
	}
	active, err := f.intents.ListActive(ctx, 100)
	if err != nil || len(active) != 1 {
		t.Fatalf("active intents = %+v err=%v, want exactly one", active, err)
	}
	if active[0].CaseVersionID != 300 {
		t.Fatalf("repair scan intent case version = %d, want 300", active[0].CaseVersionID)
	}
	for _, intent := range active {
		if intent.CaseVersionID == 301 || intent.CaseVersionID == 302 {
			t.Fatalf("unselected case version %d entered the repair scan: %+v", intent.CaseVersionID, intent)
		}
	}

	if err := f.scheduler.planVersion(ctx, 100); err != nil {
		t.Fatalf("planVersion: %v", err)
	}
	wave, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave == nil {
		t.Fatalf("active wave = %+v err=%v", wave, err)
	}
	runs := f.waves.runs[wave.ID]
	if len(runs) != 1 || runs[0].CaseVersionID != 300 {
		t.Fatalf("repair wave case runs = %+v, want only the selected case version", runs)
	}
}

// TestRepairScanSkipsVersionWithoutPersistedSelection pins the typed skip: a
// version with no persisted execute.batch selection creates nothing, so a lost
// stage-entry job can never turn confirmation history into an unscoped wave.
// R49 does not recreate the scope either: with no persisted selection it skips
// the execute.batch repair, so the typed skip stands instead of widening.
func TestRepairScanSkipsVersionWithoutPersistedSelection(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	f.selections.found = false

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if created != 0 {
		t.Fatalf("repair scan created %d intent(s) without a persisted selection", created)
	}
	if !errors.Is(err, ErrNoPersistedBatchSelection) {
		t.Fatalf("repair scan err = %v, want ErrNoPersistedBatchSelection", err)
	}
	active, err := f.intents.ListActive(ctx, 100)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("typed skip created intents: %+v", active)
	}
	if err := f.scheduler.planVersion(ctx, 100); err != nil {
		t.Fatalf("planVersion: %v", err)
	}
	if wave, _ := f.waves.GetActive(ctx, 100); wave != nil {
		t.Fatalf("typed skip created a wave: %+v", wave)
	}

	// A degraded assembly without the selection reader must skip the same way,
	// never fall back to the unscoped scan.
	f.intent.batches = nil
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); !errors.Is(err, ErrNoPersistedBatchSelection) {
		t.Fatalf("repair scan without a selection reader err = %v, want the typed skip", err)
	}
	if active, _ := f.intents.ListActive(ctx, 100); len(active) != 0 {
		t.Fatalf("degraded assembly created intents: %+v", active)
	}
}

// TestRepairScanScopesCompletedVersionReopen pins the same selection on the P3
// §5.9 completed-version reopen: only the persisted execute.batch selection is
// reopened, not every confirmed case of the version. The binding is status
// completed while the stage projection is still executing, which is the
// status-only projection the P4 close writes for a version that has not yet
// moved its stage; the stage=completed branch is covered separately below.
func TestRepairScanScopesCompletedVersionReopen(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5001)
	f.versions.bindings[100] = task6Binding(100, "completed", 5001)
	f.selections.caseIDs = []int64{f.cases.cases[301].CaseID}

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil {
		t.Fatalf("EnsureInitialIntents on completed version: %v", err)
	}
	if created != 1 {
		t.Fatalf("completed repair created %d intent(s), want only the persisted selection", created)
	}
	active, _ := f.intents.ListActive(ctx, 100)
	if len(active) != 1 || active[0].CaseVersionID != 301 {
		t.Fatalf("completed repair intents = %+v, want case version 301 only", active)
	}
	if len(f.versions.reopened) != 1 || f.versions.reopened[0] != 100 {
		t.Fatalf("reopened = %v, want [100]", f.versions.reopened)
	}
}

// TestRepairScanEmptyPersistedSelectionMeansAllCases pins the contract
// distinction between "no persisted selection" (typed skip) and an existing
// execute.batch row whose inputs object carries no narrowed case_ids ({}). The
// empty persisted selection keeps the D23 contract meaning "all current
// confirmed cases", so the scan may create intents for every executable case.
func TestRepairScanEmptyPersistedSelectionMeansAllCases(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	f.selections.caseIDs = nil
	f.selections.found = true

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil {
		t.Fatalf("EnsureInitialIntents with an empty persisted selection: %v", err)
	}
	if created != 2 {
		t.Fatalf("repair scan created %d intent(s), want all 2 confirmed cases", created)
	}
}

// TestRepairScanCompletedStageScopesToPersistedSelection pins the actual
// Stage=completed behavior: the startup/leader scan lists the newest versions
// regardless of status, so EnsureInitialIntents may run with the stage
// projection at completed. The scan refreshes only the persisted execute.batch
// selection and reopens the version while creating the first new initial
// intent. R49 itself scans running versions only and never repairs or reopens a
// completed version; this branch is reachable only through the version list
// scan.
func TestRepairScanCompletedStageScopesToPersistedSelection(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5001)
	binding := task6Binding(100, "completed", 5001)
	binding.Stage = intentCompletedStage
	f.versions.bindings[100] = binding
	f.selections.caseIDs = []int64{f.cases.cases[301].CaseID}

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil {
		t.Fatalf("EnsureInitialIntents on stage-completed version: %v", err)
	}
	if created != 1 {
		t.Fatalf("stage-completed repair created %d intent(s), want only the persisted selection", created)
	}
	active, _ := f.intents.ListActive(ctx, 100)
	if len(active) != 1 || active[0].CaseVersionID != 301 {
		t.Fatalf("stage-completed repair intents = %+v, want case version 301 only", active)
	}
	if len(f.versions.reopened) != 1 || f.versions.reopened[0] != 100 {
		t.Fatalf("reopened = %v, want [100]", f.versions.reopened)
	}
}

// TestRepairScanCompletedStageWithoutPersistedSelectionSkips pins the honest
// other half of the Stage=completed behavior: with no persisted execute.batch
// selection there is no safe scope, so the scan creates nothing, does not
// reopen the version, and returns the typed skip. R49 does not repair completed
// versions, so nothing else will re-enqueue the job.
func TestRepairScanCompletedStageWithoutPersistedSelectionSkips(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	binding := task6Binding(100, "completed", 5001)
	binding.Stage = intentCompletedStage
	f.versions.bindings[100] = binding
	f.selections.found = false

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if created != 0 {
		t.Fatalf("stage-completed repair created %d intent(s) without a persisted selection", created)
	}
	if !errors.Is(err, ErrNoPersistedBatchSelection) {
		t.Fatalf("stage-completed repair err = %v, want ErrNoPersistedBatchSelection", err)
	}
	if active, _ := f.intents.ListActive(ctx, 100); len(active) != 0 {
		t.Fatalf("typed skip created intents: %+v", active)
	}
	if len(f.versions.reopened) != 0 {
		t.Fatalf("reopened = %v, want none for a typed skip", f.versions.reopened)
	}
	if err := f.scheduler.planVersion(ctx, 100); err != nil {
		t.Fatalf("planVersion: %v", err)
	}
	if wave, _ := f.waves.GetActive(ctx, 100); wave != nil {
		t.Fatalf("typed skip created a wave: %+v", wave)
	}
}
