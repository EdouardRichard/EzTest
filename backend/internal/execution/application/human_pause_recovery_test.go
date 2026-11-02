package executionapp

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestPauseWaveGracefulAndImmediate(t *testing.T) {
	ctx := context.Background()

	t.Run("graceful keeps in-flight runs", func(t *testing.T) {
		f := newTask9Fixture()
		f.seedWave(execution.WaveRunning)
		f.seedRun(500, 1, execution.CaseRunRunning)
		f.seedRun(501, 2, execution.CaseRunPending)

		wave, err := f.pause.PauseWave(ctx, 100, 50, 42, "", "maintenance")
		if err != nil {
			t.Fatalf("PauseWave: %v", err)
		}
		if wave.Status != execution.WavePaused || wave.PauseMode != execution.PauseModeGraceful {
			t.Fatalf("wave = %+v", wave)
		}
		running, _ := f.runs.Get(ctx, 500)
		if running.Status != execution.CaseRunRunning {
			t.Fatalf("graceful pause cancelled the in-flight run: %s", running.Status)
		}
		pending, _ := f.runs.Get(ctx, 501)
		if pending.Status != execution.CaseRunPending {
			t.Fatalf("pending run = %s", pending.Status)
		}
		if f.canceller.cancelled(500) {
			t.Fatal("graceful pause must not cancel runs")
		}
		if f.events.count(EventWavePaused) != 1 {
			t.Fatalf("wave_paused events = %d", f.events.count(EventWavePaused))
		}

		// Idempotent: paused stays paused and emits no second event.
		again, err := f.pause.PauseWave(ctx, 100, 50, 42, "", "again")
		if err != nil || again.Status != execution.WavePaused {
			t.Fatalf("second pause = %+v err=%v", again, err)
		}
		if f.events.count(EventWavePaused) != 1 {
			t.Fatalf("idempotent pause emitted %d events", f.events.count(EventWavePaused))
		}
	})

	t.Run("immediate cancels in-flight runs", func(t *testing.T) {
		f := newTask9Fixture()
		f.seedWave(execution.WaveRunning)
		f.seedRun(500, 1, execution.CaseRunRunning)
		f.seedRun(501, 2, execution.CaseRunPending)
		if err := f.contexts.Create(ctx, execution.CaseRunSUTContext{
			ID: 900, CaseRunID: 500, VersionSUTID: 5001, BrowserContextID: "ctx-1",
			Status: execution.SUTContextActive, CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now(),
		}); err != nil {
			t.Fatalf("seed context: %v", err)
		}

		wave, err := f.pause.PauseWave(ctx, 100, 50, 42, execution.PauseModeImmediate, "stop now")
		if err != nil {
			t.Fatalf("PauseWave immediate: %v", err)
		}
		if wave.Status != execution.WavePaused || wave.PauseMode != execution.PauseModeImmediate {
			t.Fatalf("wave = %+v", wave)
		}
		running, _ := f.runs.Get(ctx, 500)
		if running.Status != execution.CaseRunPaused || running.PauseReason != "stop now" {
			t.Fatalf("immediate paused run = %+v", running)
		}
		if !f.canceller.cancelled(500) {
			t.Fatal("immediate pause must cancel the runner")
		}
		if !f.control.deletedContext("ctx-1") {
			t.Fatalf("executor context not deleted: %v", f.control.deleted)
		}
		pending, _ := f.runs.Get(ctx, 501)
		if pending.Status != execution.CaseRunPending {
			t.Fatalf("pending run must stay pending, got %s", pending.Status)
		}
	})
}

func TestResumeWaveRejectsOpenHuman(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	now := f.clock.Now()
	f.waves.byID[50] = execution.RunWave{
		ID: 50, VersionID: 100, Status: execution.WavePaused, Trigger: execution.WaveTriggerAuto,
		PauseReason: execution.PauseReasonWaitingHuman, PauseMode: execution.PauseModeGraceful,
		CreatedAt: now, UpdatedAt: now,
	}
	f.seedRun(500, 1, execution.CaseRunPaused)
	run := f.runs.byID[500]
	run.PauseReason = execution.PauseReasonWaitingHuman
	f.runs.byID[500] = run
	human := execution.HumanRequest{
		ID: 700, VersionID: 100, CaseRunID: int64PtrValue(500), Kind: execution.HumanLoginTakeover,
		Status: execution.HumanOpen, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := f.humans.Create(ctx, human); err != nil {
		t.Fatalf("create human request: %v", err)
	}

	if _, err := f.pause.ResumeWave(ctx, 100, 50, 42); err == nil {
		t.Fatal("resume with an open human request must conflict")
	} else {
		requireAPIError(t, err, "state_conflict")
	}

	if _, err := f.human.Resolve(ctx, human.ID, 42, map[string]any{"code": "246810"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wave, err := f.pause.ResumeWave(ctx, 100, 50, 42)
	if err != nil {
		t.Fatalf("ResumeWave after resolve: %v", err)
	}
	if wave.Status != execution.WaveRunning {
		t.Fatalf("wave status = %s, want running", wave.Status)
	}
}

func TestHumanResolveResumesCase(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	run := f.seedRun(500, 1, execution.CaseRunRunning)
	f.seedIntent(1, 300)
	scope := tools.Scope{
		VersionID: 100, WaveID: 50, CaseRunID: 500, IntentID: 1,
		DispatchEpoch: run.DispatchEpoch, DispatchToken: run.DispatchToken,
	}

	requestID, err := f.human.CreateFromTool(ctx, scope, tools.HumanRequestInput{
		Kind: "login_takeover", Reason: "2FA required", AccountRole: "admin", ChallengeKind: "totp",
	})
	if err != nil {
		t.Fatalf("CreateFromTool: %v", err)
	}
	paused, _ := f.runs.Get(ctx, 500)
	if paused.Status != execution.CaseRunPaused || paused.Summary.HumanRequestID != requestID {
		t.Fatalf("paused run = %+v", paused)
	}
	wave, _ := f.waves.Get(ctx, 50)
	if wave.Status != execution.WavePaused || wave.PauseReason != execution.PauseReasonWaitingHuman {
		t.Fatalf("wave = %+v", wave)
	}
	if f.events.count(EventHumanCreated) != 1 || f.events.count(EventCaseRunPaused) != 1 {
		t.Fatalf("events = %v", f.events.types())
	}
	if !f.canceller.cancelled(500) {
		t.Fatal("human request must cancel the in-flight run")
	}
	// The response code is never written into events.
	for _, event := range f.events.events {
		if event.Payload["code"] == "246810" || event.Payload["response"] != nil {
			t.Fatalf("response leaked into event: %+v", event.Payload)
		}
	}

	resolved, err := f.human.Resolve(ctx, requestID, 42, map[string]any{"code": "246810", "note": "done"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != execution.HumanResolved {
		t.Fatalf("request status = %s", resolved.Status)
	}
	resumed, _ := f.runs.Get(ctx, 500)
	if resumed.Status != execution.CaseRunPending {
		t.Fatalf("case run = %s, want pending", resumed.Status)
	}
	if resumed.DispatchEpoch != run.DispatchEpoch+1 || resumed.DispatchToken != "" {
		t.Fatalf("epoch/token = %d/%q", resumed.DispatchEpoch, resumed.DispatchToken)
	}
	wave, _ = f.waves.Get(ctx, 50)
	if wave.Status != execution.WaveRunning {
		t.Fatalf("wave = %s, want running", wave.Status)
	}
	if f.events.count(EventHumanResolved) != 1 {
		t.Fatalf("human_resolved events = %d", f.events.count(EventHumanResolved))
	}
	for _, event := range f.events.events {
		if event.Type == EventCaseRunResumed && event.Channel != "version:100" {
			t.Fatalf("case_run.resumed channel = %q, want version:100", event.Channel)
		}
	}
}

func TestHumanTimeoutFailsCase(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	run := f.seedRun(500, 1, execution.CaseRunRunning)
	f.seedIntent(1, 300)
	scope := tools.Scope{VersionID: 100, WaveID: 50, CaseRunID: 500, DispatchEpoch: run.DispatchEpoch, DispatchToken: run.DispatchToken}
	requestID, err := f.human.CreateFromTool(ctx, scope, tools.HumanRequestInput{Kind: "login_takeover", Reason: "2FA"})
	if err != nil {
		t.Fatalf("CreateFromTool: %v", err)
	}

	f.clock.now = f.clock.Now().Add(2 * time.Hour)
	count, err := f.human.Expire(ctx, f.clock.Now())
	if err != nil || count != 1 {
		t.Fatalf("Expire = %d err=%v", count, err)
	}
	failed, _ := f.runs.Get(ctx, 500)
	if failed.Status != execution.CaseRunFailed || failed.FailureClass != execution.FailureClassSession {
		t.Fatalf("run = %s/%s, want failed/session", failed.Status, failed.FailureClass)
	}
	for _, event := range f.events.events {
		if event.Type == EventCaseRunFinished && event.Channel != "version:100" {
			t.Fatalf("case_run.finished channel = %q, want version:100", event.Channel)
		}
	}
	if failed.Summary.VerdictReason != execution.VerdictReasonHumanTimeout {
		t.Fatalf("verdict reason = %q", failed.Summary.VerdictReason)
	}
	request, _ := f.humans.Get(ctx, requestID)
	if request.Status != execution.HumanExpired {
		t.Fatalf("request = %s, want expired", request.Status)
	}
	wave, _ := f.waves.Get(ctx, 50)
	if wave.Status != execution.WaveRunning {
		t.Fatalf("wave = %s, want running (continued)", wave.Status)
	}
	if f.events.count(EventHumanExpired) != 1 {
		t.Fatalf("human_expired events = %d", f.events.count(EventHumanExpired))
	}
}

func TestCaseRunPauseResume(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedRun(500, 1, execution.CaseRunPending)

	paused, err := f.pause.PauseCaseRun(ctx, 100, 500, 42, "", "user")
	if err != nil {
		t.Fatalf("PauseCaseRun pending: %v", err)
	}
	if paused.Status != execution.CaseRunPaused {
		t.Fatalf("status = %s", paused.Status)
	}
	resumed, err := f.pause.ResumeCaseRun(ctx, 100, 500, 42)
	if err != nil {
		t.Fatalf("ResumeCaseRun: %v", err)
	}
	if resumed.Status != execution.CaseRunPending || resumed.DispatchEpoch != paused.DispatchEpoch+1 {
		t.Fatalf("resumed = %+v", resumed)
	}

	// Running case: immediate cancel keeps finished results and pauses.
	running := f.seedRun(501, 2, execution.CaseRunRunning)
	paused, err = f.pause.PauseCaseRun(ctx, 100, 501, 42, execution.PauseModeImmediate, "cancel")
	if err != nil {
		t.Fatalf("PauseCaseRun running: %v", err)
	}
	if paused.Status != execution.CaseRunPaused || !f.canceller.cancelled(501) {
		t.Fatalf("running pause = %+v", paused)
	}
	if paused.PausedBy == nil || *paused.PausedBy != 42 {
		t.Fatalf("paused_by = %v", paused.PausedBy)
	}
	_ = running
}

func TestExecutorOfflineRedispatch(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	intent := f.seedIntent(1, 300)
	run := f.seedRun(500, 1, execution.CaseRunRunning)
	lease := f.clock.Now().Add(time.Minute)
	run.LeaseUntil = &lease
	run.OwnerExecutorID = int64PtrValue(1)
	f.runs.byID[500] = run
	offline := execution.ExecutorInstance{ID: 1, Name: "executor", Endpoint: "http://x", Status: execution.ExecutorOffline}
	f.executors.byID[1] = offline
	f.cases.cases[300] = task6Snapshot(300, 5001)

	if err := f.recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	redispatch, _ := f.runs.Get(ctx, 500)
	if redispatch.Status != execution.CaseRunPending {
		t.Fatalf("status = %s, want pending", redispatch.Status)
	}
	if redispatch.DispatchEpoch != run.DispatchEpoch+1 || redispatch.DispatchToken != "" {
		t.Fatalf("epoch/token = %d/%q", redispatch.DispatchEpoch, redispatch.DispatchToken)
	}
	// The old epoch/token cannot write anymore.
	if ok, _ := f.runs.UpdateCAS(ctx, 500, run.DispatchEpoch, run.DispatchToken, func(*execution.CaseRun) error { return nil }); ok {
		t.Fatal("stale epoch write must be rejected")
	}
	stored, _ := f.intents.Get(ctx, intent.ID)
	if stored.Status != execution.IntentRunning {
		t.Fatalf("intent = %s, want running for redispatch", stored.Status)
	}

	// Exhausted attempts fail as environment.
	f2 := newTask9Fixture()
	f2.seedWave(execution.WaveRunning)
	f2.seedIntent(1, 300)
	exhausted := f2.seedRun(500, 1, execution.CaseRunRunning)
	exhausted.Attempts = 3
	exhausted.OwnerExecutorID = int64PtrValue(1)
	f2.runs.byID[500] = exhausted
	f2.executors.byID[1] = offline
	f2.cases.cases[300] = task6Snapshot(300, 5001)
	if err := f2.recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile exhausted: %v", err)
	}
	failed, _ := f2.runs.Get(ctx, 500)
	if failed.Status != execution.CaseRunFailed || failed.FailureClass != execution.FailureClassEnvironment {
		t.Fatalf("exhausted run = %s/%s", failed.Status, failed.FailureClass)
	}
}

func TestLeaseExpiredRecovery(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	f.seedIntent(2, 301)
	expired := f.seedRun(500, 1, execution.CaseRunRunning)
	past := f.clock.Now().Add(-time.Minute)
	expired.LeaseUntil = &past
	f.runs.byID[500] = expired
	fresh := f.seedRun(501, 2, execution.CaseRunRunning)
	future := f.clock.Now().Add(time.Minute)
	fresh.LeaseUntil = &future
	f.runs.byID[501] = fresh
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5001)

	if err := f.recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	recovered, _ := f.runs.Get(ctx, 500)
	if recovered.Status != execution.CaseRunPending || recovered.DispatchEpoch != expired.DispatchEpoch+1 {
		t.Fatalf("lease-expired run = %+v", recovered)
	}
	untouched, _ := f.runs.Get(ctx, 501)
	if untouched.Status != execution.CaseRunRunning {
		t.Fatal("recovery must not interrupt a live lease (no bulk interrupt on restart)")
	}
}

func TestSupersededCancelsActive(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	intent := f.seedIntent(1, 300)
	f.seedRun(500, 1, execution.CaseRunRunning)
	// No current+confirmed case 300 in the reader: the case went stale.

	if err := f.recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	stored, _ := f.intents.Get(ctx, intent.ID)
	if stored.Status != execution.IntentSuperseded {
		t.Fatalf("intent = %s, want superseded", stored.Status)
	}
	run, _ := f.runs.Get(ctx, 500)
	if run.Status != execution.CaseRunSuperseded {
		t.Fatalf("case run = %s, want superseded", run.Status)
	}
	if f.events.count(EventIntentSuperseded) != 1 {
		t.Fatalf("intent_superseded events = %d", f.events.count(EventIntentSuperseded))
	}
}

func TestCancelWaveAndIntent(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	f.seedRun(500, 1, execution.CaseRunRunning)
	f.seedRun(501, 2, execution.CaseRunPending)
	if err := f.intents.Create(ctx, execution.ExecutionIntent{
		ID: 2, VersionID: 100, CaseVersionID: 301, VersionSUTIDs: []int64{5001},
		EnvironmentFingerprint: testFingerprintForTask9, Purpose: execution.PurposeManual, Status: execution.IntentRunning,
	}); err != nil {
		t.Fatalf("seed intent 2: %v", err)
	}

	wave, err := f.pause.CancelWave(ctx, 100, 50, 42)
	if err != nil {
		t.Fatalf("CancelWave: %v", err)
	}
	if wave.Status != execution.WaveCancelled || wave.FinishedAt == nil {
		t.Fatalf("wave = %+v", wave)
	}
	run, _ := f.runs.Get(ctx, 500)
	if run.Status != execution.CaseRunSuperseded {
		t.Fatalf("run = %s, want superseded", run.Status)
	}
	if f.events.count(EventWaveFinished) != 1 {
		t.Fatalf("wave_finished events = %d", f.events.count(EventWaveFinished))
	}
}

func TestCancelIntentSupersedesRuns(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	f.seedRun(500, 1, execution.CaseRunPending)
	if err := f.pause.CancelIntent(ctx, 100, 1, 42); err != nil {
		t.Fatalf("CancelIntent: %v", err)
	}
	stored, _ := f.intents.Get(ctx, 1)
	if stored.Status != execution.IntentCancelled {
		t.Fatalf("intent = %s, want cancelled", stored.Status)
	}
	run, _ := f.runs.Get(ctx, 500)
	if run.Status != execution.CaseRunSuperseded {
		t.Fatalf("run = %s, want superseded", run.Status)
	}
}

func TestRecoveryCleansStaleAndOrphanContexts(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	f.seedRun(500, 1, execution.CaseRunPending)
	f.cases.cases[300] = task6Snapshot(300, 5001)
	old := f.clock.Now().Add(-10 * time.Minute)
	if err := f.contexts.Create(ctx, execution.CaseRunSUTContext{
		ID: 900, CaseRunID: 500, VersionSUTID: 5001, BrowserContextID: "ctx-old",
		Status: execution.SUTContextActive, CreatedAt: old, UpdatedAt: old,
	}); err != nil {
		t.Fatalf("seed stale context: %v", err)
	}
	if err := f.contexts.Create(ctx, execution.CaseRunSUTContext{
		ID: 901, CaseRunID: 500, VersionSUTID: 5002, BrowserContextID: "ctx-known",
		Status: execution.SUTContextActive, CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now(),
	}); err != nil {
		t.Fatalf("seed known context: %v", err)
	}
	f.control.live = []ExecutorContext{
		{ID: "ctx-known", Key: "run:500:vs:5002:role:"},
		{ID: "ctx-orphan", Key: "run:999:vs:5001:role:"},
		{ID: "foreign-ctx", Key: "other:1"},
	}

	if err := f.recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	stale, _ := f.contexts.ListByCaseRun(ctx, 500)
	for _, item := range stale {
		if item.ID == 900 && item.Status != execution.SUTContextClosed {
			t.Fatalf("stale context not closed: %+v", item)
		}
		if item.ID == 901 && item.Status == execution.SUTContextClosed {
			t.Fatalf("fresh context must stay open: %+v", item)
		}
	}
	if !f.control.deletedContext("ctx-old") {
		t.Fatalf("stale executor context not deleted: %v", f.control.deleted)
	}
	if !f.control.deletedContext("ctx-orphan") {
		t.Fatalf("orphan executor context not deleted: %v", f.control.deleted)
	}
	if f.control.deletedContext("ctx-known") {
		t.Fatal("known executor context must not be deleted")
	}
	if f.control.deletedContext("foreign-ctx") {
		t.Fatal("non-execution context must not be deleted")
	}
}
