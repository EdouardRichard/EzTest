package executionapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
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
	if created.CodeBaseline != mustCodeBaseline(t, binding) {
		t.Fatal("intent code baseline must match the binding")
	}
	if len(created.SUTIDs) != 1 || created.SUTIDs[0] != 5001 {
		t.Fatalf("sut_ids = %v", created.SUTIDs)
	}
}

func TestIntentCodeBaselineUsesResolvedCommit(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)

	binding := task6Binding(100, "running", 5001)
	binding.SUTs[0].RepositoryBaselines = []RepositoryBaseline{
		{RepoID: 900, Commit: strings.Repeat("a", 40)},
	}
	f.versions.bindings[100] = binding

	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	intents, err := f.intents.ListActive(ctx, 100)
	if err != nil || len(intents) != 1 {
		t.Fatalf("intents = %+v err=%v", intents, err)
	}
	expected := mustCodeBaseline(t, binding)
	if len(expected) != 64 {
		t.Fatalf("expected a 64 hex commit baseline, got %q", expected)
	}
	if intents[0].CodeBaseline != expected {
		t.Fatalf("intent baseline = %q, want %q", intents[0].CodeBaseline, expected)
	}

	// V-0024: an unresolved commit is rejected typed, not branch-fallback. The
	// intent scan surfaces the rejection instead of creating a weakly bound intent.
	unresolved := binding
	unresolved.SUTs = append([]SUTBinding(nil), binding.SUTs...)
	unresolved.SUTs[0].RepositoryBaselines = []RepositoryBaseline{{RepoID: 900, Commit: ""}}
	f.versions.bindings[100] = unresolved
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err == nil {
		t.Fatal("an unresolved code baseline must reject intent creation, not fall back to a branch")
	}
}

// mustCodeBaseline computes the code baseline of a binding for test assertions,
// failing the test on a typed rejection.
func mustCodeBaseline(t *testing.T, binding VersionBinding) string {
	t.Helper()
	baseline, err := CodeBaseline(binding)
	if err != nil {
		t.Fatalf("CodeBaseline: %v", err)
	}
	return baseline
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

// TestSchedulerBoundSetDoesNotThrottleDispatch pins the v0.2.4-F P5 contract
// (design §3.3, V-0015) on the shape the retired per-SUT quota used to throttle.
//
// intent.sut_ids is still the version's WHOLE bound SUT set (controller ruling
// P2: the column stays -- it records the bound set for resolution/fingerprint/
// knowledge). What is gone is the quota COUPLING: with the per-SUT dimension
// removed, running load on one bound SUT can no longer hold back every run of
// the version (the head-of-line blocking the over-approximation caused under
// the retired 3/2/1 quota). The single global limit defaults to 0 = unlimited.
func TestSchedulerBoundSetDoesNotThrottleDispatch(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001, 5002)
	f.cases.cases[301] = task6Snapshot(301, 5003)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002, 5003)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	// Production-shaped settings: no admin cap, headroom for the 2 in-flight +
	// 2 new runs, so only a restored per-SUT-style coupling could hold them back.
	f.scheduler = f.newSchedulerWithSettings(f.quota, &fakeSettings{values: map[string]any{
		SettingMaxConcurrentCaseRuns:  int64(0),
		settingAgentMaxConcurrentRuns: int64(8),
	}})
	// Two runs already running. Under the retired per-SUT quota (default 2)
	// these saturated every bound SUT's slots and held back BOTH runs below --
	// including case 301, whose prose only ever mentioned 5003.
	f.quota.running = []execution.RunningCase{
		{CaseRunID: 9001},
		{CaseRunID: 9002},
	}
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}

	// The recorded bound set is unchanged: every intent of the version covers
	// the whole bound SUT set (ruling P2 -- recorded fact, not a quota input).
	intents, _, err := f.intents.List(ctx, 100, "", 0, nil)
	if err != nil {
		t.Fatalf("List intents: %v", err)
	}
	if len(intents) != 2 {
		t.Fatalf("intents = %d, want 2", len(intents))
	}
	for _, intent := range intents {
		if want := []int64{5001, 5002, 5003}; !reflect.DeepEqual(intent.SUTIDs, want) {
			t.Fatalf("intent %d sut_ids = %v, want the whole bound set %v", intent.ID, intent.SUTIDs, want)
		}
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
	for _, run := range runs {
		if run.Status != execution.CaseRunRunning {
			t.Fatalf("case run %d (case version %d) status = %s, want running: load on a bound "+
				"SUT must not throttle the version's runs after the per-SUT dimension was removed",
				run.ID, run.CaseVersionID, run.Status)
		}
	}
	if got := f.queue.countType("execute.case"); got != 2 {
		t.Fatalf("execute.case work items = %d, want 2", got)
	}
	if payloads := f.events.payloads(EventCaseRunBlocked); len(payloads) != 0 {
		t.Fatalf("blocked events = %d, want 0: nothing is quota-blocked", len(payloads))
	}
}

func TestSchedulerSelectsExecutorPerPendingRun(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
	now := f.clock.Now()
	// P7 multi-endpoint: each pending case_run re-selects the best executor so a
	// single tick can spread work over the registered endpoints.
	f.registry.sequence = []execution.ExecutorInstance{
		{ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8, Status: execution.ExecutorActive, LastHeartbeat: &now},
		{ID: 2, Name: "executor-2", Endpoint: "http://127.0.0.1:13337", Capacity: 8, Status: execution.ExecutorActive, LastHeartbeat: &now},
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
	owners := map[int64]bool{}
	for _, run := range runs {
		if run.Status != execution.CaseRunRunning {
			t.Fatalf("case run %d status = %s, want running", run.ID, run.Status)
		}
		if run.OwnerExecutorID == nil {
			t.Fatalf("case run %d has no owner executor", run.ID)
		}
		owners[*run.OwnerExecutorID] = true
	}
	if len(owners) != 2 || !owners[1] || !owners[2] {
		t.Fatalf("owner executors = %v, want 1 and 2", owners)
	}
	if got := f.queue.countType("execute.case"); got != 2 {
		t.Fatalf("execute.case work items = %d, want 2", got)
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
	// Simulate the CaseRunner finishing the run (fixture seeding: the
	// production Update primitive refuses terminal transitions, V-0022).
	run, _ := f.runs.Get(ctx, runs[0].ID)
	run.Status = execution.CaseRunPassed
	run.FailureClass = ""
	run.Summary = execution.CaseRunSummary{VerdictReason: execution.VerdictReasonPassed}
	f.runs.byID[run.ID] = run

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

// multiRoleSnapshot is a case whose steps ask for several distinct account
// roles. v0.2.4-B: a step carries no target SUT, so the account role is the only
// per-step dimension left, and "" counts as a role of its own.
func multiRoleSnapshot(caseVersionID int64, suts []int64, roles ...string) execution.CaseSnapshot {
	snapshot := task6Snapshot(caseVersionID, suts...)
	// A multi-SUT case has no single default: Prepare/executionSUTBindings only
	// resolves DefaultSUTID when the version binds exactly one SUT.
	snapshot.DefaultSUTID = 0
	snapshot.Steps = nil
	snapshot.Assertions = nil
	for index, role := range roles {
		snapshot.Steps = append(snapshot.Steps, execution.CaseStep{
			Seq: index + 1, Action: "step", TargetDescription: "target", AccountRole: role,
		})
		// Intent validation requires at least one assertion per case, and a ui
		// assertion carries no SUT id under the natural-language contract.
		snapshot.Assertions = append(snapshot.Assertions, execution.CaseAssertion{
			StepSeq: index + 1, AssertionSeq: 1, Kind: "ui", Expected: "ok",
		})
	}
	return snapshot
}

// TestRequiredContextCountReservesRolesNotTheCrossProduct pins review C1 at the
// unit level: the capacity reservation is the distinct account-role count, NOT
// roles x the intent's bound SUT set.
//
// The cross product was contexts the run never creates. ensureSUTContexts
// pre-warms one context per distinct role for a single-bound-SUT run and
// pre-warms nothing for a multi-SUT run (DefaultSUTID is 0), creating each
// context lazily in ensureBrowserContext -- and those are already counted by
// executor.ActiveContexts, the gate's other input. Reserving the product on top
// double-counted them and, above the default capacity of 8, deadlocked dispatch.
func TestRequiredContextCountReservesRolesNotTheCrossProduct(t *testing.T) {
	snapshot := multiRoleSnapshot(300, []int64{5001, 5002, 5003}, "admin", "author", "anonymous")
	if got := requiredContextCount(snapshot); got != 3 {
		t.Fatalf("requiredContextCount = %d, want 3 (one per distinct role, not 3 roles x 3 bound SUTs = 9)", got)
	}
	// A case with no steps still needs one context, and repeated roles collapse.
	if got := requiredContextCount(execution.CaseSnapshot{}); got != 1 {
		t.Fatalf("requiredContextCount(no steps) = %d, want 1", got)
	}
	repeated := multiRoleSnapshot(301, []int64{5001, 5002}, "admin", "admin", "author")
	if got := requiredContextCount(repeated); got != 2 {
		t.Fatalf("requiredContextCount(repeated roles) = %d, want 2 distinct roles", got)
	}
}

// TestSchedulerDispatchesWhenRolesTimesBoundSUTsExceedsCapacity is the review C1
// regression. Before the fix this run could NEVER dispatch: requiredContextCount
// returned 3 distinct roles x 3 bound SUTs = 9, the default executor capacity is
// 8, so CanDispatch returned false on every tick forever. The run stayed
// pending, the wave never completed, and because the reason was discarded at the
// call site nothing explained why. That is a livelock, and it hit exactly the
// multi-SUT version v0.2.4-B exists to support. Any roles x boundSUTs product
// over the capacity deadlocked the same way (2 SUTs x 5 roles, 9 x 1, ...).
func TestSchedulerDispatchesWhenRolesTimesBoundSUTsExceedsCapacity(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001, 5002, 5003}, "admin", "author", "anonymous")
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002, 5003)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
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
	if len(runs) != 1 {
		t.Fatalf("case runs = %d, want 1", len(runs))
	}
	if runs[0].Status != execution.CaseRunRunning {
		t.Fatalf("case run status = %s, want running: 3 roles x 3 bound SUTs must not be "+
			"reserved against a capacity of 8", runs[0].Status)
	}
	if got := f.queue.countType("execute.case"); got != 1 {
		t.Fatalf("execute.case work items = %d, want 1", got)
	}
	if got := f.events.count(EventCaseRunBlocked); got != 0 {
		t.Fatalf("blocked events = %d, want none: %v", got, f.events.payloads(EventCaseRunBlocked))
	}
}

// TestSchedulerTerminatesRunWhoseContextsExceedCapacity pins review I-new-1: a
// run whose context requirement exceeds the executor's ENTIRE capacity must not
// stay silently pending forever.
//
// The gate is execActive + contexts > execCapacity, so once contexts alone
// exceeds capacity it fails for every possible execActive, including an idle
// executor, and no future tick changes either number. Nothing else rescued it:
// run.Attempts is incremented only AFTER the gate, so a never-dispatched run
// stays at 0 and DefaultMaxDispatchAttempts never applies; LeaseUntil is likewise
// set after the gate, so lease-expiry recovery has nothing to reclaim; there is
// no pending-age watchdog; and completeWave needs every run finished, so the wave
// wedged with it. Reachable in production by registering a small executor
// capacity (max_contexts is the contract alias of capacity) against a case with
// more distinct account roles.
//
// It is blocked, not failed: nothing about the product under test was exercised
// or found wanting, so FailureClassBlocked plus a distinct TerminalCause is the
// honest record (TerminalCauseNeedsHuman classifies it as needing a human, and
// TestTerminatedRunPostsAHumanHandoff pins the handoff post that actually
// delivers it -- the predicate alone routes nothing),
// NextActionResolveBlocker says what to do, and blocked is terminal so the wave
// can now complete instead of hanging.
func TestSchedulerTerminatesRunWhoseContextsExceedsCapacity(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	// One bound SUT (so the per-SUT quota stays out of the way) and three
	// distinct account roles: requiredContextCount is 3, capacity is 1.
	f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin", "author", "auditor")
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 1,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	// Looked up by List, not GetActive: terminating the run lets the wave finish
	// in this same tick, which is the whole point of the escalation, so it is
	// usually no longer active by the time we assert on it.
	waves, _, listErr := f.waves.List(ctx, 100, 10, nil)
	if listErr != nil || len(waves) != 1 {
		t.Fatalf("waves = %d, %v; want exactly one", len(waves), listErr)
	}
	wave := waves[0]
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	if len(runs) != 1 {
		t.Fatalf("case runs = %d, want 1", len(runs))
	}
	run, err := f.runs.Get(ctx, runs[0].ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != execution.CaseRunBlocked {
		t.Fatalf("run status = %s, want blocked: a reservation above the whole "+
			"capacity can never be satisfied, so it must terminate rather than stay pending", run.Status)
	}
	if run.FailureClass != execution.FailureClassBlocked {
		t.Fatalf("failure class = %q, want %q", run.FailureClass, execution.FailureClassBlocked)
	}
	if run.Summary.TerminalCause != execution.ReasonContextRequirementExceedsCapacity {
		t.Fatalf("terminal cause = %q, want %q", run.Summary.TerminalCause,
			execution.ReasonContextRequirementExceedsCapacity)
	}
	if run.Summary.NextAction != execution.NextActionResolveBlocker {
		t.Fatalf("next action = %q, want %q", run.Summary.NextAction, execution.NextActionResolveBlocker)
	}
	if !execution.TerminalCauseNeedsHuman(run.Summary.TerminalCause) {
		t.Fatal("the cause must route to a human: only an operator can raise the capacity or cut the roles")
	}
	if run.Summary.LastError == "" {
		t.Fatal("last_error must carry the concrete numbers, not be empty")
	}
	if run.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0: the run was never dispatched", run.Attempts)
	}

	blocked := f.events.payloads(EventCaseRunBlocked)
	if len(blocked) != 1 {
		t.Fatalf("blocked events = %d, want 1: %v", len(blocked), f.events.types())
	}
	if got, _ := blocked[0]["reason"].(string); got != execution.ReasonContextRequirementExceedsCapacity {
		t.Fatalf("blocked reason = %q, want %q", got, execution.ReasonContextRequirementExceedsCapacity)
	}
	if terminal, _ := blocked[0]["terminal"].(bool); !terminal {
		t.Fatal("the blocked event must say it is terminal, so nobody waits for a retry")
	}
	if got, _ := blocked[0]["action"].(string); got != blockedAction(execution.ReasonContextRequirementExceedsCapacity) {
		t.Fatalf("blocked action = %q, want the raise-the-capacity remedy", got)
	}
	if got := f.events.count(EventCaseRunFinished); got != 1 {
		t.Fatalf("case_run.finished events = %d, want 1 (the wave must see a terminal run)", got)
	}

	// The wave completed instead of hanging on a run that will never start --
	// that is the whole point of escalating. Before the fix it stayed planned or
	// running forever, because completeWave needs every run finished.
	if wave.Status != execution.WaveDone && wave.Status != execution.WaveFailed {
		t.Fatalf("wave status = %s, want a terminal state now that its only run is blocked", wave.Status)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	// And the terminal run must not keep re-publishing on later ticks.
	if got := len(f.events.payloads(EventCaseRunBlocked)); got != 1 {
		t.Fatalf("blocked events after further ticks = %d, want still 1", got)
	}
}

// TestSchedulerPublishesCapacityBlockReason pins the TRANSIENT half of review C1:
// a run that merely has to wait for a busy executor stays pending, and says so.
//
// Distinct from TestSchedulerTerminatesRunWhoseContextsExceedCapacity above --
// here the requirement fits the executor (3 contexts, capacity 4) and only the
// currently active contexts push it over, so a later tick can dispatch it.
// Terminating this case would be wrong. The reason used to be discarded
// (ok, _ := plus a bare continue), which is what made the C1 livelock invisible;
// Constitution 七.6/七.7.
func TestSchedulerPublishesCapacityBlockReason(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin", "author", "auditor")
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	// Capacity 4 with 3 already active: 3 + 3 > 4, but 3 <= 4, so this is a
	// transient wait, not a structural impossibility.
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336",
		Capacity: 4, ActiveContexts: 3,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
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
	if runs[0].Status != execution.CaseRunPending {
		t.Fatalf("case run status = %s, want pending: a busy executor is a wait, not a termination", runs[0].Status)
	}
	payloads := f.events.payloads(EventCaseRunBlocked)
	if len(payloads) != 1 {
		t.Fatalf("blocked events = %d, want exactly 1: %v", len(payloads), f.events.types())
	}
	if got, _ := payloads[0]["reason"].(string); got != execution.ReasonExecutorCapacity {
		t.Fatalf("blocked reason = %q, want %q", got, execution.ReasonExecutorCapacity)
	}
	if got, _ := payloads[0]["action"].(string); got != "等待并发配额或执行器容量释放后重试" {
		t.Fatalf("blocked action = %q, want the wait-and-retry remedy, not the raise-capacity one", got)
	}
	if terminal, ok := payloads[0]["terminal"].(bool); ok && terminal {
		t.Fatal("a transient capacity wait must not be marked terminal")
	}
	// Deduplicated: a second tick must not re-publish the same block.
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	if got := len(f.events.payloads(EventCaseRunBlocked)); got != 1 {
		t.Fatalf("blocked events after a second tick = %d, want 1 (deduped by case_run+reason)", got)
	}

	// Once the executor drains, the same run dispatches -- proving it was a wait.
	f.registry.executor.ActiveContexts = 0
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("third ReconcileVersion: %v", err)
	}
	runs, _ = f.runs.ListAllByWave(ctx, wave.ID)
	if runs[0].Status != execution.CaseRunRunning {
		t.Fatalf("case run status after the executor drained = %s, want running", runs[0].Status)
	}
}

// TestMultiSUTConcurrencyNotCollapsed pins design P5 (v0.2.4-F, Review Focus 1):
// a multi-SUT version dispatches MORE THAN ONE case run concurrently, even with
// multi-SUT runs already in flight.
//
// This replaces TestSchedulerCrossSystemStaysInertWhileBoundSetIsWide, which
// documented the B-era trap: domain quota.go asserted "spans systems" via
// `candidate.CrossSystem || len(candidate.SUTIDs) > 1`, and since intent.sut_ids
// became the version's whole bound set, that OR was already true for every run
// of every multi-SUT version -- wiring any real usage-side signal collapsed
// concurrency to CrossSystem (default 1). P5 DISSOLVES the trap by removing the
// per-SUT and cross-system dimensions entirely: only the single global limit
// (default 0 = unlimited) remains, plus the structural/executor capacity gates.
//
// Mutation contract (narrowed to what this test actually proves, fix round 1
// Minor 1): restoring a FINITE GLOBAL DEFAULT (the retired global 3 rejects the
// 4th/5th run below) or a CROSS-SYSTEM-STYLE THROTTLE THAT COUNTS THE RUNNING
// SET (rejects the 2nd) must fail this test. A per-SUT throttle reintroduction
// is structurally blocked upstream: RunningCase no longer carries a SUT set, so
// there is no per-SUT input to restore without a type change this test cannot
// see.
func TestMultiSUTConcurrencyNotCollapsed(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	// Two cases, both naming prose targets across the version's two bound SUTs.
	f.cases.cases[300] = task6Snapshot(300, 5001, 5002)
	f.cases.cases[301] = task6Snapshot(301, 5001, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	// Production-shaped settings: no admin cap (0), generous claim headroom so
	// the 3 in-flight + 2 new runs fit and only a restored quota dimension could
	// refuse them.
	f.scheduler = f.newSchedulerWithSettings(f.quota, &fakeSettings{values: map[string]any{
		SettingMaxConcurrentCaseRuns:  int64(0),
		settingAgentMaxConcurrentRuns: int64(8),
	}})
	// Three multi-SUT runs already in flight. Under the retired defaults this
	// alone exhausted the global budget (3); under any restored cross-system
	// throttle counting the running set, a single one of them would collapse
	// the version to one run.
	f.quota.running = []execution.RunningCase{
		{CaseRunID: 9001},
		{CaseRunID: 9002},
		{CaseRunID: 9003},
	}
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	intents, _, err := f.intents.List(ctx, 100, "", 0, nil)
	if err != nil || len(intents) != 2 {
		t.Fatalf("intents = %d, %v", len(intents), err)
	}
	for _, intent := range intents {
		if len(intent.SUTIDs) != 2 {
			t.Fatalf("intent sut_ids = %v, want both bound SUTs (the wide bound set is preserved)", intent.SUTIDs)
		}
	}

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave == nil {
		t.Fatalf("active wave = %+v, %v", wave, err)
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	if len(runs) != 2 {
		t.Fatalf("case runs = %d, want 2", len(runs))
	}
	for _, run := range runs {
		if run.Status != execution.CaseRunRunning {
			t.Fatalf("case run %d status = %s, want running: multi-SUT concurrency collapsed. "+
				"If a finite global default or a cross-system-style count of the running set "+
				"was restored, this is the design-P5 regression the single global limit removed", run.ID, run.Status)
		}
	}
	if got := f.queue.countType("execute.case"); got != 2 {
		t.Fatalf("execute.case work items = %d, want 2 (both dispatched in one tick)", got)
	}
	if payloads := f.events.payloads(EventCaseRunBlocked); len(payloads) != 0 {
		t.Fatalf("blocked events = %d, want 0", len(payloads))
	}
}

// TestSchedulerReadsQuotaSettingLive pins the V-0015 fix at the scheduler entry
// point: the effective admission bound comes from the live settings on every
// dispatch pass (hot-reloadable, same source as the REST quotas projection),
// NOT from a construction-time hardcode. The admin limit (0 = unlimited) is
// composed with the provable claim headroom (ruling P13); read failures retain
// the last-known-good value and log (ruling P12, pinned separately by
// TestSchedulerQuotaReadFailureRetainsLastKnownGood).
func TestSchedulerReadsQuotaSettingLive(t *testing.T) {
	newFixture := func(t *testing.T, settings *fakeSettings) *task6Fixture {
		t.Helper()
		f := newTask6Fixture(t)
		f.cases.cases[300] = task6Snapshot(300, 5001)
		f.cases.cases[301] = task6Snapshot(301, 5002)
		f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
		now := f.clock.Now()
		f.registry.executor = &execution.ExecutorInstance{
			ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
			Status: execution.ExecutorActive, LastHeartbeat: &now,
		}
		f.scheduler = f.newSchedulerWithSettings(f.quota, settings)
		if _, err := f.intent.EnsureInitialIntents(context.Background(), 100); err != nil {
			t.Fatalf("EnsureInitialIntents: %v", err)
		}
		return f
	}
	runningCount := func(t *testing.T, f *task6Fixture) int {
		t.Helper()
		if err := f.scheduler.ReconcileVersion(context.Background(), 100); err != nil {
			t.Fatalf("ReconcileVersion: %v", err)
		}
		wave, _ := f.waves.GetActive(context.Background(), 100)
		if wave == nil {
			t.Fatal("wave not created")
		}
		runs, _ := f.runs.ListAllByWave(context.Background(), wave.ID)
		count := 0
		for _, run := range runs {
			if run.Status == execution.CaseRunRunning {
				count++
			}
		}
		return count
	}

	t.Run("setting 1 throttles to one run and the block is attributable", func(t *testing.T) {
		settings := &fakeSettings{values: map[string]any{
			SettingMaxConcurrentCaseRuns:  int64(1),
			settingAgentMaxConcurrentRuns: int64(4),
		}}
		f := newFixture(t, settings)
		if got := runningCount(t, f); got != 1 {
			t.Fatalf("running = %d, want 1 under a live global limit of 1", got)
		}
		payloads := f.events.payloads(EventCaseRunBlocked)
		if len(payloads) != 1 {
			t.Fatalf("blocked events = %d, want 1", len(payloads))
		}
		if got, _ := payloads[0]["reason"].(string); got != execution.ReasonGlobalConcurrency {
			t.Fatalf("blocked reason = %q, want %q", got, execution.ReasonGlobalConcurrency)
		}
		// Hot reload: raising the limit dispatches the waiting run on the next
		// tick without rebuilding the scheduler.
		settings.values[SettingMaxConcurrentCaseRuns] = int64(0)
		if got := runningCount(t, f); got != 2 {
			t.Fatalf("running after raising the live limit to 0 = %d, want 2 (no admin cap)", got)
		}
	})

	t.Run("setting 0 removes the admin cap; claim headroom still bounds", func(t *testing.T) {
		settings := &fakeSettings{values: map[string]any{
			SettingMaxConcurrentCaseRuns:  int64(0),
			settingAgentMaxConcurrentRuns: int64(4),
		}}
		f := newFixture(t, settings)
		if got := runningCount(t, f); got != 2 {
			t.Fatalf("running = %d, want 2: 0 means no admin cap and headroom 4 admits both", got)
		}
	})

	t.Run("claim headroom bounds admission when the admin limit is unlimited", func(t *testing.T) {
		settings := &fakeSettings{values: map[string]any{
			SettingMaxConcurrentCaseRuns:  int64(0),
			settingAgentMaxConcurrentRuns: int64(1),
		}}
		f := newFixture(t, settings)
		if got := runningCount(t, f); got != 1 {
			t.Fatalf("running = %d, want 1: dispatch must follow the provable claim headroom", got)
		}
	})

	t.Run("quota read failure before any good read logs and stays resource-bounded", func(t *testing.T) {
		// The quota key is absent from the fake -> Int returns ErrNotFound. No
		// last-known-good exists yet, so the fallback is the catalog default
		// (0 = unlimited) -- but admission is still bounded by claim headroom,
		// and the failure is logged, never silent.
		settings := &fakeSettings{values: map[string]any{
			settingAgentMaxConcurrentRuns: int64(1),
		}}
		f := newFixture(t, settings)
		logs := captureSlog(t)
		if got := runningCount(t, f); got != 1 {
			t.Fatalf("running = %d, want 1: even without a readable admin limit, claim "+
				"headroom bounds admission", got)
		}
		if logged := logs.String(); !strings.Contains(logged, "WARN") ||
			!strings.Contains(logged, SettingMaxConcurrentCaseRuns) {
			t.Fatalf("log output = %q, want a WARN naming %s", logged, SettingMaxConcurrentCaseRuns)
		}
	})
}

// TestSchedulerDoesNotTerminateWhenALargerExecutorIsMerelyBusy pins review round
// 4, Important 1: a PERMANENT verdict must not be inferred from the one executor
// Select happened to return.
//
// Registry.Select drops full and degraded rows before sorting the survivors by
// capacity DESC (and in endpoint-less single mode sorts by ID ascending, ignoring
// capacity), so with two configured endpoints -- a shipped P7 configuration -- a
// capacity-8 executor that is momentarily full or merely degraded is skipped in
// favour of an idle capacity-2 one. Escalating on that selected row terminally
// blocked the run, recorded a settled WRONG verdict, told the operator to raise a
// capacity that was already high enough, and let the wave complete and the stage
// machine advance, while the larger executor would have served the run seconds
// later. Worse in kind than the silent pending row the escalation was added to
// fix.
//
// So the escalation is guarded by the fleet maximum, and a probe that cannot prove
// impossibility fails closed to the transient path.
func TestSchedulerDoesNotTerminateWhenALargerExecutorIsMerelyBusy(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	// Three distinct roles -> requiredContextCount is 3.
	f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin", "author", "auditor")
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	// Select returns the small idle executor; the fleet also has a capacity-8 one
	// that is momentarily full (or degraded) and was therefore filtered out.
	f.registry.executor = &execution.ExecutorInstance{
		ID: 2, Name: "executor-small", Endpoint: "http://127.0.0.1:13337", Capacity: 2,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	f.registry.fleetCapacity = 8
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	waves, _, err := f.waves.List(ctx, 100, 10, nil)
	if err != nil || len(waves) != 1 {
		t.Fatalf("waves = %d, %v; want exactly one", len(waves), err)
	}
	runs, _ := f.runs.ListAllByWave(ctx, waves[0].ID)
	run, err := f.runs.Get(ctx, runs[0].ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != execution.CaseRunPending {
		t.Fatalf("run status = %s, want pending: a capacity-8 executor exists in the "+
			"fleet, so 3 contexts is a transient wait, not an impossibility", run.Status)
	}
	if run.Status.Terminal() || run.Summary.TerminalCause != "" {
		t.Fatalf("run must not carry a terminal verdict: status=%s cause=%q",
			run.Status, run.Summary.TerminalCause)
	}
	if got := f.teamRoom.count(); got != 0 {
		t.Fatalf("team-room handoff posts = %d, want none: nothing needs a human yet", got)
	}
	// And the operator must be told to WAIT, not to raise a capacity that is
	// already high enough -- reporting the structural reason here would
	// mis-attribute a transient condition as a permanent one.
	payloads := f.events.payloads(EventCaseRunBlocked)
	if len(payloads) != 1 {
		t.Fatalf("blocked events = %d, want 1: %v", len(payloads), f.events.types())
	}
	if got, _ := payloads[0]["reason"].(string); got != execution.ReasonExecutorCapacity {
		t.Fatalf("blocked reason = %q, want %q (transient), not %q", got,
			execution.ReasonExecutorCapacity, execution.ReasonContextRequirementExceedsCapacity)
	}
	if terminal, ok := payloads[0]["terminal"].(bool); ok && terminal {
		t.Fatal("a transient capacity wait must not be marked terminal")
	}

	// The same run DOES terminate once the fleet genuinely cannot serve it, which
	// is what proves the guard above is the deciding factor and not some other
	// accident of the fixture.
	f.registry.fleetCapacity = 2
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	run, err = f.runs.Get(ctx, runs[0].ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != execution.CaseRunBlocked {
		t.Fatalf("run status = %s, want blocked once no dispatchable executor can serve 3 contexts", run.Status)
	}
}

// TestTerminatedRunPostsAHumanHandoff pins review round 4, Important 2: a run the
// scheduler terminates must actually REACH a human, not merely be labelled as
// needing one.
//
// Both operator-facing promises the round-3 disclosure relied on were inert on
// this path. execution.TerminalCauseNeedsHuman has exactly one production
// consumer, CaseRunner.postTerminalHandoff (case_run.go), reached from the
// executor finalize path -- and a run the scheduler terminated was never
// dispatched, so it never enters CaseRunner and no handoff post was created.
// Asserting the predicate directly, as the earlier test did, proved its shape and
// nothing about routing. And there is no retest entry point either:
// FailureClass.IsIssueCandidate() is FailureClassApplication only (domain/case_run
// .go), so a blocked run emits no issue candidate, no issue exists, and
// StartRetest -- which requires an issueID -- cannot be called. The real recovery
// path is manual: POST /api/versions/{id}/issues with the case_run_id, then
// retest. Release note 8.4 and the T11 report were corrected to say so.
func TestTerminatedRunPostsAHumanHandoff(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin", "author", "auditor")
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	// Capacity 1 for both the selected executor and the whole fleet: genuinely
	// impossible, so the escalation path runs.
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 1,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	f.registry.fleetCapacity = 1
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	if got := f.teamRoom.count(); got != 1 {
		t.Fatalf("team-room handoff posts = %d, want exactly 1: a run that needs a "+
			"human must tell one, and postTerminalHandoff is unreachable for a run "+
			"the scheduler terminated before dispatch", got)
	}
	f.teamRoom.mu.Lock()
	post := f.teamRoom.posts[0]
	f.teamRoom.mu.Unlock()
	if post.Kind != "handoff" || post.Team != TeamExecution {
		t.Fatalf("post = kind %q team %q, want the same handoff post CaseRunner.postTerminalHandoff makes",
			post.Kind, post.Team)
	}
	if post.VersionID != 100 {
		t.Fatalf("post version = %d, want 100", post.VersionID)
	}
	if len(post.Refs) != 1 || post.Refs[0].Kind != "case_run" {
		t.Fatalf("post refs = %+v, want one case_run ref", post.Refs)
	}
	for _, want := range []string{
		execution.ReasonContextRequirementExceedsCapacity,
		execution.NextActionResolveBlocker,
	} {
		if !strings.Contains(post.Content, want) {
			t.Fatalf("handoff content %q does not mention %q", post.Content, want)
		}
	}
	// The content must carry the concrete numbers, or the operator cannot tell
	// which of the two remedies applies.
	if !strings.Contains(post.Content, "3") {
		t.Fatalf("handoff content %q does not state the required context count", post.Content)
	}

	// Best effort and once only: a further tick must not re-post for a run that
	// is already terminal.
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	if got := f.teamRoom.count(); got != 1 {
		t.Fatalf("handoff posts after a second tick = %d, want still 1", got)
	}
}

// TestSchedulerRunningUsageCountsEveryRunningRun pins the scheduler's own
// ListRunning fallback producer: with no injected quota reader, every running
// case_run counts exactly once towards the single global limit. The retired
// per-SUT/cross-system accounting (SUT sets, CrossSystem flags) is gone with the
// dimensions -- the running set is a plain count of runs.
func TestSchedulerRunningUsageCountsEveryRunningRun(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	// nil quota reader -> runningUsage falls through to the real producer.
	scheduler := f.newScheduler(nil)
	f.runs.byID[9001] = execution.CaseRun{
		ID: 9001, WaveID: 2001, IntentID: 1001, CaseVersionID: 300,
		Status: execution.CaseRunRunning,
	}
	f.runs.byID[9002] = execution.CaseRun{
		ID: 9002, WaveID: 2001, IntentID: 1002, CaseVersionID: 301,
		Status: execution.CaseRunRunning,
	}
	usage, err := scheduler.runningUsage(ctx)
	if err != nil {
		t.Fatalf("runningUsage: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("usage = %d entries, want one per running case", len(usage))
	}
	seen := map[int64]bool{}
	for _, item := range usage {
		seen[item.CaseRunID] = true
	}
	if !seen[9001] || !seen[9002] {
		t.Fatalf("usage = %+v, want both running runs counted", usage)
	}
}

// TestSchedulerFailsClosedWhenTheFleetCeilingCannotBeRead pins the fail-closed
// branch of review round 4's Important 1, which round 4 left untested: the
// fakeExecutorRegistry.fleetErr field existed but no test ever set it, so
// returning true on error -- permanently blocking a run whenever the registry
// probe hiccuped -- would have kept the whole suite green.
//
// The asymmetry is the point: a run that waits is recoverable on a later tick, a
// run that is wrongly terminally blocked is not. So an unreadable ceiling must
// degrade to "cannot prove impossibility", never to "impossible".
func TestSchedulerFailsClosedWhenTheFleetCeilingCannotBeRead(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	// 3 roles against capacity 1: CanDispatch returns the structural reason, so
	// the escalation path is reached and the ceiling probe decides the outcome.
	f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin", "author", "auditor")
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 1,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	f.registry.fleetErr = errors.New("registry probe unavailable")
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	waves, _, err := f.waves.List(ctx, 100, 10, nil)
	if err != nil || len(waves) != 1 {
		t.Fatalf("waves = %d, %v; want exactly one", len(waves), err)
	}
	runs, _ := f.runs.ListAllByWave(ctx, waves[0].ID)
	run, err := f.runs.Get(ctx, runs[0].ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != execution.CaseRunPending {
		t.Fatalf("run status = %s, want pending: an unreadable fleet ceiling must not be "+
			"read as proof of impossibility", run.Status)
	}
	if run.Status.Terminal() || run.Summary.TerminalCause != "" {
		t.Fatalf("run must carry no terminal verdict: status=%s cause=%q", run.Status, run.Summary.TerminalCause)
	}
	if got := f.teamRoom.count(); got != 0 {
		t.Fatalf("handoff posts = %d, want none: nothing was terminally blocked", got)
	}
	payloads := f.events.payloads(EventCaseRunBlocked)
	if len(payloads) != 1 {
		t.Fatalf("blocked events = %d, want 1: the wait must still be attributable", len(payloads))
	}
	if got, _ := payloads[0]["reason"].(string); got != execution.ReasonExecutorCapacity {
		t.Fatalf("blocked reason = %q, want the transient %q", got, execution.ReasonExecutorCapacity)
	}
	// And once the probe recovers with a genuinely too-small fleet, the same run
	// does terminate -- proving the pending state above was the fail-closed branch
	// and not the escalation being dead.
	f.registry.fleetErr = nil
	f.registry.fleetCapacity = 1
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	run, err = f.runs.Get(ctx, runs[0].ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != execution.CaseRunBlocked {
		t.Fatalf("run status = %s, want blocked once the ceiling is readable and genuinely too small", run.Status)
	}
}

// TestSchedulerProbesFleetCeilingOnlyOnStructuralRejection pins review round 7's
// Important: the fleet-ceiling probe must stay inside the structural-reason branch.
//
// Round 5's TOCTOU fix hoisted it above the `reason ==` test, which made it run on
// EVERY rejection -- global_concurrency and plain executor_capacity (plus the
// per-SUT/cross-system reasons retired by design P5), i.e. the common case in any
// busy wave. Each call costs an executorConfig (three settings reads) plus a full
// repo.List of the executor table, so a wave of N pending runs under a finite
// global quota went from roughly zero extra queries per tick to O(N). The
// observability half was worse: fleetCapacityCeiling logs slog.Warn on error, so
// during a registry outage every rejected run logged a warning every tick blaming
// "an oversized context requirement" for runs that have none -- noise aimed at
// whoever is reading logs during the incident.
func TestSchedulerProbesFleetCeilingOnlyOnStructuralRejection(t *testing.T) {
	ctx := context.Background()

	t.Run("a concurrency rejection does not probe the fleet", func(t *testing.T) {
		f := newTask6Fixture(t)
		// One role, capacity 8: the structural check cannot fire (1 <= 8), so the
		// rejection below must come from the global quota. The default is now
		// 0 = unlimited (design P5), so this test configures a finite limit of 3
		// explicitly -- the gate mechanics under a configured limit are unchanged.
		f.scheduler = f.newSchedulerWithQuota(execution.Quota{Global: 3}, f.quota)
		f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin")
		f.versions.bindings[100] = task6Binding(100, "running", 5001)
		now := f.clock.Now()
		f.registry.executor = &execution.ExecutorInstance{
			ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
			Status: execution.ExecutorActive, LastHeartbeat: &now,
		}
		f.quota.running = []execution.RunningCase{
			{CaseRunID: 9001},
			{CaseRunID: 9002},
			{CaseRunID: 9003},
		}
		if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
			t.Fatalf("EnsureInitialIntents: %v", err)
		}
		if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
			t.Fatalf("ReconcileVersion: %v", err)
		}
		waves, _, _ := f.waves.List(ctx, 100, 10, nil)
		runs, _ := f.runs.ListAllByWave(ctx, waves[0].ID)
		if runs[0].Status != execution.CaseRunPending {
			t.Fatalf("run status = %s, want pending (global quota exhausted)", runs[0].Status)
		}
		payloads := f.events.payloads(EventCaseRunBlocked)
		if len(payloads) != 1 {
			t.Fatalf("blocked events = %d, want 1", len(payloads))
		}
		if got, _ := payloads[0]["reason"].(string); got != execution.ReasonGlobalConcurrency {
			t.Fatalf("blocked reason = %q, want %q -- the fixture must reject on concurrency, "+
				"not capacity, or this case proves nothing", got, execution.ReasonGlobalConcurrency)
		}
		if f.registry.ceilingCalls != 0 {
			t.Fatalf("MaxDispatchableCapacity called %d time(s), want 0: a non-structural "+
				"rejection must not pay for an executorConfig plus a full executor-table "+
				"List, per pending run, per tick", f.registry.ceilingCalls)
		}
	})

	t.Run("a structural rejection does probe the fleet", func(t *testing.T) {
		f := newTask6Fixture(t)
		f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin", "author", "auditor")
		f.versions.bindings[100] = task6Binding(100, "running", 5001)
		now := f.clock.Now()
		f.registry.executor = &execution.ExecutorInstance{
			ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 1,
			Status: execution.ExecutorActive, LastHeartbeat: &now,
		}
		f.registry.fleetCapacity = 1
		if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
			t.Fatalf("EnsureInitialIntents: %v", err)
		}
		if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
			t.Fatalf("ReconcileVersion: %v", err)
		}
		if f.registry.ceilingCalls == 0 {
			t.Fatal("MaxDispatchableCapacity was never called, so the structural path did " +
				"not prove fleet-wide impossibility -- the escalation would be unsound")
		}
	})
}

// TestSchedulerTreatsAZeroFleetCeilingAsUnproven pins the zero branch of
// fleetCapacityCeiling's fail-closed rule (review round 7, Minor 1).
//
// The behaviour was documented in three places -- scheduler.go:363-368,
// domain/quota.go's capacity note and registry.go's MaxDispatchableCapacity -- and
// tested in NONE. The reviewer's mutation (`maxCapacity <= 0` returning true
// instead of false) left the whole suite green. It happens to be unreachable today,
// but that proof spans three facts in three files and nothing fails if one moves;
// a zero ceiling means "no dispatchable executor at all", which is not proof that
// the run's requirement is impossible, so it must degrade to a wait.
func TestSchedulerTreatsAZeroFleetCeilingAsUnproven(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	// 3 roles against a selected capacity of 1: CanDispatch returns the structural
	// reason, so the ceiling decides -- and the ceiling reports 0.
	f.cases.cases[300] = multiRoleSnapshot(300, []int64{5001}, "admin", "author", "auditor")
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 1,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	f.registry.fleetZero = true
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	waves, _, err := f.waves.List(ctx, 100, 10, nil)
	if err != nil || len(waves) != 1 {
		t.Fatalf("waves = %d, %v; want exactly one", len(waves), err)
	}
	runs, _ := f.runs.ListAllByWave(ctx, waves[0].ID)
	run, err := f.runs.Get(ctx, runs[0].ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != execution.CaseRunPending {
		t.Fatalf("run status = %s, want pending: a zero ceiling means nothing is "+
			"dispatchable, which is NOT proof the requirement is impossible", run.Status)
	}
	if run.Status.Terminal() || run.Summary.TerminalCause != "" {
		t.Fatalf("run must carry no terminal verdict: status=%s cause=%q", run.Status, run.Summary.TerminalCause)
	}
	if got := f.teamRoom.count(); got != 0 {
		t.Fatalf("handoff posts = %d, want none", got)
	}
	if f.registry.ceilingCalls == 0 {
		t.Fatal("the ceiling was never probed, so this test is not exercising the zero branch")
	}
	payloads := f.events.payloads(EventCaseRunBlocked)
	if len(payloads) != 1 {
		t.Fatalf("blocked events = %d, want 1: the wait must still be attributable", len(payloads))
	}
	if got, _ := payloads[0]["reason"].(string); got != execution.ReasonExecutorCapacity {
		t.Fatalf("blocked reason = %q, want the transient %q", got, execution.ReasonExecutorCapacity)
	}
}

// captureSlog redirects the process default logger into a buffer for the
// duration of one test, so WARN assertions read real log output.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestSchedulerQuotaReadFailureRetainsLastKnownGood pins controller ruling P12
// (fix round 1, constitution VII.6/VII.7): a failed read of
// execution.max_concurrent_case_runs must NOT silently widen admission to the
// zero value. The scheduler retains the last successfully-read limit and logs
// a WARN naming the setting and the cause.
func TestSchedulerQuotaReadFailureRetainsLastKnownGood(t *testing.T) {
	settings := &fakeSettings{values: map[string]any{
		SettingMaxConcurrentCaseRuns: int64(1),
		"agent.max_concurrent_runs":  int64(4),
	}}
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	// nil quota reader: runningUsage goes through the real ListRunning producer,
	// so the second tick sees the run the first tick dispatched.
	f.scheduler = f.newSchedulerWithSettings(nil, settings)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("first ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("wave not created")
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	running := 0
	for _, run := range runs {
		if run.Status == execution.CaseRunRunning {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("running under limit 1 = %d, want 1", running)
	}

	// The settings read now fails (key row missing / cache failure shape).
	delete(settings.values, SettingMaxConcurrentCaseRuns)
	logs := captureSlog(t)
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	runs, _ = f.runs.ListAllByWave(ctx, wave.ID)
	running = 0
	for _, run := range runs {
		if run.Status == execution.CaseRunRunning {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("running after a failed quota read = %d, want 1: the last-known-good "+
			"limit must be retained, not silently widened to unlimited", running)
	}
	logged := logs.String()
	if !strings.Contains(logged, "WARN") || !strings.Contains(logged, SettingMaxConcurrentCaseRuns) {
		t.Fatalf("log output = %q, want a WARN naming %s: a quota-read failure must "+
			"be attributable, never a silent degradation", logged, SettingMaxConcurrentCaseRuns)
	}
}

// TestSchedulerLargeWaveNoFalseDispatchTimeout pins controller ruling P13
// (fix round 1): under the DEFAULT unlimited quota, dispatch admission follows
// provable claim headroom (agent.max_concurrent_runs), so a large wave is NOT
// stamped running+enqueued wholesale.
//
// Why this matters: executor.ActiveContexts is only refreshed by the executor
// health poll, and nothing renews the dispatch lease of a queued run. Recovery
// (recoverLeases) interrupts a RUNNING run whose lease expired; an unclaimed
// one is classified FailureClassEnvironment / TerminalCauseDispatchTimeout and
// eventually fails on retry exhaustion. Dispatching a 50-100-case wave at once
// -- structurally impossible under the retired Global=3 -- would therefore
// mass-produce FALSE dispatch_timeout failures for legitimately queued runs.
// Admission bounded by claim headroom keeps every dispatched run claimable
// promptly; the rest stay PENDING (no lease, Attempts 0), which recovery never
// touches.
//
// Mutation contract: removing the claim-headroom bound dispatches all 12 runs
// in one tick and this test fails at the running-count assertion.
func TestSchedulerLargeWaveNoFalseDispatchTimeout(t *testing.T) {
	const claimCeiling = 4
	settings := &fakeSettings{values: map[string]any{
		SettingMaxConcurrentCaseRuns: int64(0), // default: unlimited
		"agent.max_concurrent_runs":  int64(claimCeiling),
	}}
	f := newTask6Fixture(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		f.cases.cases[int64(300+i)] = task6Snapshot(int64(300+i), 5001)
	}
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	// Capacity far above the wave so only claim headroom can bound admission.
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 64,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
	}
	// nil quota reader: runningUsage goes through the real ListRunning producer.
	f.scheduler = f.newSchedulerWithSettings(nil, settings)
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
	if len(runs) != 12 {
		t.Fatalf("case runs = %d, want the whole 12-case wave planned", len(runs))
	}
	running, pending := 0, 0
	for _, run := range runs {
		switch run.Status {
		case execution.CaseRunRunning:
			running++
			if run.LeaseUntil == nil {
				t.Fatalf("dispatched run %d has no dispatch lease", run.ID)
			}
		case execution.CaseRunPending:
			pending++
			// THE STRUCTURAL GUARANTEE: a waiting run carries no lease and no
			// attempt, so recoverLeases has nothing to reclaim and a false
			// dispatch_timeout is unreachable for it.
			if run.LeaseUntil != nil || run.Attempts != 0 {
				t.Fatalf("waiting run %d carries lease=%v attempts=%d, want none: a queued "+
					"run beyond claim headroom must stay pending, not running-with-a-lease",
					run.ID, run.LeaseUntil, run.Attempts)
			}
		default:
			t.Fatalf("run %d status = %s, want running or pending", run.ID, run.Status)
		}
	}
	if running != claimCeiling {
		t.Fatalf("dispatched = %d, want %d (claim headroom): admission must follow provable "+
			"claim capacity, not stamp the whole wave running+enqueued", running, claimCeiling)
	}
	if pending != 12-claimCeiling {
		t.Fatalf("pending = %d, want %d", pending, 12-claimCeiling)
	}
	if got := f.queue.countType("execute.case"); got != claimCeiling {
		t.Fatalf("execute.case work items = %d, want %d", got, claimCeiling)
	}
	// The wait is attributable, never silent (VII.6).
	payloads := f.events.payloads(EventCaseRunBlocked)
	if len(payloads) != 12-claimCeiling {
		t.Fatalf("blocked events = %d, want one per waiting run (%d)", len(payloads), 12-claimCeiling)
	}
	for _, payload := range payloads {
		if got, _ := payload["reason"].(string); got != execution.ReasonGlobalConcurrency {
			t.Fatalf("blocked reason = %q, want %q", got, execution.ReasonGlobalConcurrency)
		}
	}

	// Progress: two running runs finish; the next tick admits exactly two more
	// (headroom freed), still never exceeding the claim ceiling.
	finished := 0
	for _, run := range runs {
		if run.Status != execution.CaseRunRunning || finished == 2 {
			continue
		}
		run.Status = execution.CaseRunPassed
		run.Summary = execution.CaseRunSummary{VerdictReason: execution.VerdictReasonPassed}
		// Fixture seeding of a terminal state (V-0022: Update refuses terminal
		// transitions).
		f.runs.byID[run.ID] = run
		finished++
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	runs, _ = f.runs.ListAllByWave(ctx, wave.ID)
	running = 0
	for _, run := range runs {
		if run.Status == execution.CaseRunRunning {
			running++
		}
	}
	if running != claimCeiling {
		t.Fatalf("running after two completions = %d, want %d: freed headroom is readmitted",
			running, claimCeiling)
	}
	if got := f.queue.countType("execute.case"); got != claimCeiling+2 {
		t.Fatalf("execute.case work items = %d, want %d", got, claimCeiling+2)
	}
}

// TestSchedulerReservesExecutorContextsWithinTick pins the second half of the
// P13 admission bound: executor.ActiveContexts is only written by the health
// poll, so within one dispatch pass the scheduler must reserve the contexts it
// just admitted. Without the reservation a lagging ActiveContexts=0 lets one
// tick over-admit past the executor's whole capacity.
func TestSchedulerReservesExecutorContextsWithinTick(t *testing.T) {
	settings := &fakeSettings{values: map[string]any{
		SettingMaxConcurrentCaseRuns: int64(0),
		"agent.max_concurrent_runs":  int64(8),
	}}
	f := newTask6Fixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		f.cases.cases[int64(300+i)] = task6Snapshot(int64(300+i), 5001)
	}
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	now := f.clock.Now()
	f.registry.executor = &execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 2,
		Status: execution.ExecutorActive, LastHeartbeat: &now,
		// ActiveContexts stays 0 for the whole tick: the health poll has not
		// observed the contexts this tick is about to reserve.
	}
	f.scheduler = f.newSchedulerWithSettings(nil, settings)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	running := 0
	for _, run := range runs {
		if run.Status == execution.CaseRunRunning {
			running++
		}
	}
	if running != 2 {
		t.Fatalf("dispatched = %d, want 2 (executor capacity): in-tick reservations must "+
			"count against the lagging ActiveContexts", running)
	}
	payloads := f.events.payloads(EventCaseRunBlocked)
	if len(payloads) != 1 {
		t.Fatalf("blocked events = %d, want 1", len(payloads))
	}
	if got, _ := payloads[0]["reason"].(string); got != execution.ReasonExecutorCapacity {
		t.Fatalf("blocked reason = %q, want the transient %q", got, execution.ReasonExecutorCapacity)
	}
}
