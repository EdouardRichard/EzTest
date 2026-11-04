package executionapp

import (
	"context"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestNoEmptySupersededRun reproduces the retry-7 S4->S5 evidence: a selected
// case version left the current+confirmed set between intent creation and wave
// planning (a late design revision de-confirmed the case). The planner must
// retire that intent before it inserts a case_run, so the wave never contains a
// never-attempted (attempts=0) superseded run whose case_id cannot be resolved.
func TestNoEmptySupersededRun(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	// task6Snapshot(300) belongs to case 1300 and task6Snapshot(301) to 1301.
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
	f.registry.executor = activeExecutor(f)

	if _, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{1300, 1301}); err != nil {
		t.Fatalf("EnsureInitialIntentsForCases: %v", err)
	}
	// The late design write de-confirms selected case 1301 before the wave is
	// planned: its selected case version is no longer current+confirmed.
	delete(f.cases.cases, 301)

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave == nil {
		t.Fatalf("active wave = %+v err=%v", wave, err)
	}
	runs, err := f.runs.ListAllByWave(ctx, wave.ID)
	if err != nil {
		t.Fatalf("ListAllByWave: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("wave runs = %d (%+v), want 1: the stale selected case must not insert a never-attempted run", len(runs), runs)
	}
	for _, run := range runs {
		if run.Status == execution.CaseRunSuperseded && run.Attempts == 0 {
			t.Fatalf("empty superseded run %+v: a selected case that never ran must not leave a terminal run", run)
		}
		if run.CaseVersionID == 301 {
			t.Fatalf("stale selected case version 301 entered the wave: %+v", run)
		}
	}
	// The S5 gate rule: every terminal run must record at least one attempt.
	terminal, attempted := 0, 0
	for _, run := range runs {
		if !run.Status.Terminal() {
			continue
		}
		terminal++
		if run.Attempts >= 1 {
			attempted++
		}
	}
	if attempted != terminal {
		t.Fatalf("terminal=%d withAttempts=%d, want every terminal run attempted", terminal, attempted)
	}
	// The intent is still retired (P3 §4.9) so no later tick re-plans it.
	intents, _, err := f.intents.List(ctx, 100, "", 10, nil)
	if err != nil {
		t.Fatalf("list intents: %v", err)
	}
	byCaseVersion := map[int64]execution.ExecutionIntent{}
	for _, intent := range intents {
		byCaseVersion[intent.CaseVersionID] = intent
	}
	if got := byCaseVersion[301].Status; got != execution.IntentSuperseded {
		t.Fatalf("stale intent status = %q, want superseded", got)
	}
	// A repeated tick must not create a second wave/intent/run.
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	activeWaves, err := f.waves.ListActive(ctx)
	if err != nil || len(activeWaves) != 1 {
		t.Fatalf("active waves = %d err=%v, want 1", len(activeWaves), err)
	}
	if again, _ := f.runs.ListAllByWave(ctx, wave.ID); len(again) != 1 {
		t.Fatalf("case runs after a second tick = %d, want 1", len(again))
	}
}

// TestSelectedCaseHasOneTerminalRun follows one execute.batch selection through
// a case-version replacement: the first wave loses one selected case to a late
// design revision, the case is re-confirmed as a new current version, and the
// second batch executes it. Exactly one terminal case_run may exist per
// selected case; the superseded, never-attempted placeholder must never count
// as the selected case's terminal result.
func TestSelectedCaseHasOneTerminalRun(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001) // selected case 1300
	f.cases.cases[301] = task6Snapshot(301, 5002) // selected case 1301
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
	f.registry.executor = activeExecutor(f)

	selection := []int64{1300, 1301}
	if _, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, selection); err != nil {
		t.Fatalf("EnsureInitialIntentsForCases: %v", err)
	}

	// Selected case 1301 is de-confirmed by a late design revision: its next
	// current version (302, same logical case 1301) appears as confirmed later.
	delete(f.cases.cases, 301)
	replacement := task6Snapshot(302, 5002)
	replacement.CaseID = 1301
	f.cases.cases[302] = replacement

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("first ReconcileVersion: %v", err)
	}
	firstWave, err := f.waves.GetActive(ctx, 100)
	if err != nil || firstWave == nil {
		t.Fatalf("first active wave = %+v err=%v", firstWave, err)
	}
	finishRunningRuns(t, f, firstWave.ID)
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("complete first wave: %v", err)
	}

	// The user re-confirms the modified case and dispatches the batch again.
	if _, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, selection); err != nil {
		t.Fatalf("second EnsureInitialIntentsForCases: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	secondWave, err := f.waves.GetActive(ctx, 100)
	if err != nil || secondWave == nil {
		t.Fatalf("second active wave = %+v err=%v", secondWave, err)
	}
	finishRunningRuns(t, f, secondWave.ID)
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("complete second wave: %v", err)
	}

	// Every terminal case_run of both waves, grouped by the selected case it
	// belongs to (case version 300 -> case 1300, 301/302 -> case 1301).
	caseByVersion := map[int64]int64{300: 1300, 301: 1301, 302: 1301}
	terminalByCase := map[int64][]execution.CaseRun{}
	waves, _, err := f.waves.List(ctx, 100, 20, nil)
	if err != nil {
		t.Fatalf("list waves: %v", err)
	}
	for _, wave := range waves {
		runs, err := f.runs.ListAllByWave(ctx, wave.ID)
		if err != nil {
			t.Fatalf("ListAllByWave(%d): %v", wave.ID, err)
		}
		for _, run := range runs {
			if run.Status == execution.CaseRunSuperseded && run.Attempts == 0 {
				t.Fatalf("empty superseded run %+v survived in wave %d", run, wave.ID)
			}
			if !run.Status.Terminal() {
				continue
			}
			caseID, ok := caseByVersion[run.CaseVersionID]
			if !ok {
				t.Fatalf("terminal run for unexpected case version %d: %+v", run.CaseVersionID, run)
			}
			terminalByCase[caseID] = append(terminalByCase[caseID], run)
		}
	}
	for _, caseID := range selection {
		runs := terminalByCase[caseID]
		if len(runs) != 1 {
			t.Fatalf("selected case %d terminal runs = %d (%+v), want exactly 1", caseID, len(runs), runs)
		}
		if runs[0].Attempts < 1 {
			t.Fatalf("selected case %d terminal run attempts = %d, want >= 1", caseID, runs[0].Attempts)
		}
	}
}

// activeExecutor gives the fixture one healthy endpoint so the scheduler can
// dispatch the executable sibling of a stale selected case.
func activeExecutor(f *task6Fixture) *execution.ExecutorInstance {
	now := f.clock.Now()
	return &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
}

// finishRunningRuns simulates the agent finishing every running case_run of a
// wave, which lets the scheduler complete it on the next tick.
func finishRunningRuns(t *testing.T, f *task6Fixture, waveID int64) {
	t.Helper()
	runs, err := f.runs.ListAllByWave(context.Background(), waveID)
	if err != nil {
		t.Fatalf("ListAllByWave(%d): %v", waveID, err)
	}
	for _, run := range runs {
		if run.Status != execution.CaseRunRunning {
			continue
		}
		run.Status = execution.CaseRunPassed
		run.FailureClass = ""
		run.Summary = execution.CaseRunSummary{VerdictReason: execution.VerdictReasonPassed}
		if ok, err := f.runs.Update(context.Background(), run); err != nil || !ok {
			t.Fatalf("finish run %d: ok=%v err=%v", run.ID, ok, err)
		}
	}
}
