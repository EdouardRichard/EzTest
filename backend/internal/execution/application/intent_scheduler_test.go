package executionapp

import (
	"context"
	"errors"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func requireAPIError(t *testing.T, err error, code string) *platform.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", code)
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("err = %v, want code %q", err, code)
	}
	return apiErr
}

func TestEnvironmentFingerprintMatchesIntentBetweenScans(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	intents, err := f.intents.ListActive(ctx, 100)
	if err != nil || len(intents) != 1 {
		t.Fatalf("intents = %+v err=%v", intents, err)
	}
	created := intents[0]
	binding, _ := f.versions.Get(ctx, 100)
	fingerprint, err := EnvironmentFingerprint(binding)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if created.EnvironmentFingerprint != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", created.EnvironmentFingerprint, fingerprint)
	}
	if created.CodeBaseline != CodeBaseline(binding) {
		t.Fatal("intent code baseline must match the binding")
	}
	if len(created.VersionSUTIDs) != 1 || created.VersionSUTIDs[0] != 5001 {
		t.Fatalf("version_sut_ids = %v", created.VersionSUTIDs)
	}
}

func TestEnsureInitialIntentsIdempotent(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil || created != 2 {
		t.Fatalf("first scan: created=%d err=%v", created, err)
	}
	again, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil || again != 0 {
		t.Fatalf("second scan must be idempotent: created=%d err=%v", again, err)
	}
	active, _ := f.intents.ListActive(ctx, 100)
	if len(active) != 2 {
		t.Fatalf("active intents = %d, want 2", len(active))
	}
	if f.events.count(EventIntentCreated) != 2 {
		t.Fatalf("intent_created events = %d, want 2", f.events.count(EventIntentCreated))
	}
	for _, intent := range active {
		if intent.Purpose != execution.PurposeInitial || intent.Status != execution.IntentOpen {
			t.Fatalf("intent = %+v", intent)
		}
		if intent.DedupeKey == "" || intent.AttemptNo < 1 {
			t.Fatalf("intent dedupe/attempt = %+v", intent)
		}
	}
}

func TestEnsureInitialIntentsReopensCompletedVersion(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "completed", 5001)

	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if len(f.versions.reopened) != 1 || f.versions.reopened[0] != 100 {
		t.Fatalf("reopened = %v, want [100]", f.versions.reopened)
	}
	binding, _ := f.versions.Get(ctx, 100)
	if binding.Status != "running" {
		t.Fatalf("version status = %s, want running", binding.Status)
	}

	// A running version is not reopened again.
	f.cases.cases[301] = task6Snapshot(301, 5002)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("second EnsureInitialIntents: %v", err)
	}
	if len(f.versions.reopened) != 1 {
		t.Fatalf("reopened = %v, want still one call", f.versions.reopened)
	}
}

func TestManualIntentRejectsActiveDuplicate(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	first, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if first.Purpose != execution.PurposeRetest || first.Status != execution.IntentOpen {
		t.Fatalf("manual intent = %+v", first)
	}
	if f.events.count(EventIntentCreated) != 1 {
		t.Fatalf("intent_created = %d", f.events.count(EventIntentCreated))
	}
	if _, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42); err == nil {
		t.Fatal("duplicate active manual intent must conflict")
	} else {
		requireAPIError(t, err, "state_conflict")
	}

	// Once the previous intent leaves the active set a new one is allowed.
	if _, err := f.intents.Transition(ctx, first.ID, []execution.IntentStatus{execution.IntentOpen}, execution.IntentCancelled); err != nil {
		t.Fatalf("cancel intent: %v", err)
	}
	if _, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42); err != nil {
		t.Fatalf("retest after cancel: %v", err)
	}
}

func TestManualIntentRejectsInvalidInput(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)

	if _, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeInitial, 42); err == nil {
		t.Fatal("manual API must reject purpose=initial")
	} else {
		requireAPIError(t, err, "invalid_argument")
	}
	if _, err := f.intent.CreateManual(ctx, 100, 999, execution.PurposeManual, 42); err == nil {
		t.Fatal("missing case version must be rejected")
	}
}

func TestIntentCancelIdempotent(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	intent, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeManual, 42)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if err := f.intent.Cancel(ctx, intent.ID, 42); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := f.intent.Cancel(ctx, intent.ID, 42); err != nil {
		t.Fatalf("second Cancel must be idempotent: %v", err)
	}
	if f.events.count(EventIntentCancelled) != 1 {
		t.Fatalf("intent_cancelled events = %d, want 1", f.events.count(EventIntentCancelled))
	}
	stored, _ := f.intents.Get(ctx, intent.ID)
	if stored.Status != execution.IntentCancelled {
		t.Fatalf("status = %s", stored.Status)
	}
}

func TestCreateWaveAndClaimIntents(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)

	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	// No active executor: the planner creates the wave but cannot dispatch.
	f.registry.executor = nil
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave == nil {
		t.Fatalf("active wave = %+v err=%v", wave, err)
	}
	if wave.Status != execution.WavePlanned {
		t.Fatalf("wave status = %s, want planned", wave.Status)
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	if len(runs) != 2 || runs[0].Status != execution.CaseRunPending || runs[1].Status != execution.CaseRunPending {
		t.Fatalf("case runs = %+v", runs)
	}
	active, _ := f.intents.ListActive(ctx, 100)
	for _, intent := range active {
		if intent.Status != execution.IntentClaimed {
			t.Fatalf("intent status = %s, want claimed", intent.Status)
		}
	}
	if f.events.count(EventWaveCreated) != 1 {
		t.Fatalf("wave_created = %d", f.events.count(EventWaveCreated))
	}
	if f.queue.countType("execute.case") != 0 {
		t.Fatal("no execute.case work item may be created without an executor")
	}

	// A second tick must not create another wave or new case runs.
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	activeWaves, _ := f.waves.ListActive(ctx)
	if len(activeWaves) != 1 {
		t.Fatalf("active waves = %d, want 1", len(activeWaves))
	}
	runs, _ = f.runs.ListAllByWave(ctx, wave.ID)
	if len(runs) != 2 {
		t.Fatalf("case runs after second tick = %d, want 2", len(runs))
	}
}

func TestCreateWaveTriggerPriority(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
	if _, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeManual, 1); err != nil {
		t.Fatalf("manual intent: %v", err)
	}
	if _, err := f.intent.CreateManual(ctx, 100, 301, execution.PurposeRetest, 1); err != nil {
		t.Fatalf("retest intent: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil || wave.Trigger != execution.WaveTriggerRetest {
		t.Fatalf("wave = %+v, want trigger retest", wave)
	}
}

func TestSchedulerQuotaAndCrossSystem(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001, 5002) // cross-system
	f.cases.cases[301] = task6Snapshot(301, 5003)       // single SUT
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002, 5003)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	// Two runs already occupy both 5001 slots; the cross-system candidate would
	// need 5001 too, so it must be skipped as a whole.
	f.quota.running = []execution.RunningCase{
		{CaseRunID: 9001, VersionSUTIDs: []int64{5001}},
		{CaseRunID: 9002, VersionSUTIDs: []int64{5001}},
	}
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("wave not created")
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	if len(runs) != 2 {
		t.Fatalf("case runs = %d, want 2", len(runs))
	}
	byCaseVersion := map[int64]execution.CaseRun{}
	for _, run := range runs {
		byCaseVersion[run.CaseVersionID] = run
	}
	if byCaseVersion[300].Status != execution.CaseRunPending {
		t.Fatalf("cross-system run must be skipped, got %s", byCaseVersion[300].Status)
	}
	if byCaseVersion[301].Status != execution.CaseRunRunning {
		t.Fatalf("single-SUT run must dispatch, got %s", byCaseVersion[301].Status)
	}
	if f.queue.countType("execute.case") != 1 {
		t.Fatalf("execute.case work items = %d, want 1", f.queue.countType("execute.case"))
	}
}

func TestSchedulerCompletesWaveAndEnsuresAnalysis(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("wave not created")
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	if len(runs) != 1 {
		t.Fatalf("case runs = %d", len(runs))
	}
	// Simulate the CaseRunner finishing the run.
	run, _ := f.runs.Get(ctx, runs[0].ID)
	run.Status = execution.CaseRunPassed
	run.FailureClass = ""
	run.Summary = execution.CaseRunSummary{VerdictReason: execution.VerdictReasonPassed}
	if ok, err := f.runs.Update(ctx, run); err != nil || !ok {
		t.Fatalf("finish run: %v %v", ok, err)
	}

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("completion ReconcileVersion: %v", err)
	}
	finished, err := f.waves.Get(ctx, wave.ID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done", finished, err)
	}
	if finished.FinishedAt == nil {
		t.Fatal("finished_at not set")
	}
	if finished.Counts.Total != 1 || finished.Counts.Passed != 1 {
		t.Fatalf("counts = %+v", finished.Counts)
	}
	if f.events.count(EventWaveFinished) != 1 {
		t.Fatalf("wave_finished events = %d", f.events.count(EventWaveFinished))
	}
	if len(f.analysis.analysed) != 1 || f.analysis.analysed[0] != wave.ID {
		t.Fatalf("analysis calls = %v", f.analysis.analysed)
	}
	stored, _ := f.intents.Get(ctx, runs[0].IntentID)
	if stored.Status != execution.IntentDone {
		t.Fatalf("intent status = %s, want done", stored.Status)
	}
}
