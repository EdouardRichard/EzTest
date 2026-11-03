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
// The R49 stage-entry recovery owns re-enqueueing the scoped job.
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
// reopened, not every confirmed case of the version.
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
