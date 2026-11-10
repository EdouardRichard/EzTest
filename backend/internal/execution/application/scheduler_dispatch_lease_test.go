package executionapp

// R2-NEW-01 dispatch hardening: the lease the scheduler stamps at dispatch time
// covers the queue wait (an agent worker claims the item minutes later), and a
// dispatch that cannot be queued is released instead of stranding the run until
// that lease expires.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// newQueuedDispatchFixture wires one version whose single case_run is ready to
// dispatch against a healthy executor.
func newQueuedDispatchFixture(t *testing.T) *task6Fixture {
	t.Helper()
	f := newTask6Fixture(t)
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	f.registry.executor = &execution.ExecutorInstance{
		ID: 7, Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive,
	}
	if _, err := f.intent.EnsureInitialIntents(context.Background(), 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	return f
}

func singleWaveRun(t *testing.T, f *task6Fixture) execution.CaseRun {
	t.Helper()
	wave, err := f.waves.GetActive(context.Background(), 100)
	if err != nil || wave == nil {
		t.Fatalf("active wave = %+v err=%v, want one", wave, err)
	}
	runs, err := f.runs.ListAllByWave(context.Background(), wave.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("case runs = %+v err=%v, want exactly one", runs, err)
	}
	return runs[0]
}

// TestSchedulerUsesDispatchLeaseForQueuedRuns: the dispatch lease is the
// queue-wait lease (DefaultDispatchLease), not the 90s agent execution lease. A
// work item that waits behind a busy worker must not be judged lease-expired
// before an agent can claim it.
func TestSchedulerUsesDispatchLeaseForQueuedRuns(t *testing.T) {
	ctx := context.Background()
	f := newQueuedDispatchFixture(t)

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	dispatched := singleWaveRun(t, f)
	if dispatched.Status != execution.CaseRunRunning {
		t.Fatalf("dispatched status = %q, want running", dispatched.Status)
	}
	if dispatched.LeaseUntil == nil {
		t.Fatal("dispatch did not stamp a case_run lease")
	}
	if lease := dispatched.LeaseUntil.Sub(f.clock.Now()); lease < DefaultDispatchLease {
		t.Fatalf("dispatch lease = %v, want at least %v (the queue-wait lease)", lease, DefaultDispatchLease)
	}
	if dispatched.Summary.Dispatch == nil || dispatched.Summary.Dispatch.Attempt != 1 {
		t.Fatalf("dispatch snapshot = %+v, want attempt 1", dispatched.Summary.Dispatch)
	}

	// The long dispatch lease must not leak into execution accounting: the item
	// now sits in the queue, so the run survives a 5 minute wait untouched by
	// Recovery (well past the 90s agent lease).
	f.clock.now = f.clock.now.Add(5 * time.Minute)
	recovery := NewRecovery(RecoveryDeps{
		Intents:     f.intents,
		Waves:       f.waves,
		CaseRuns:    f.runs,
		Clock:       f.clock,
		Events:      f.events,
		MaxAttempts: 3,
	})
	if err := recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Recovery.Reconcile: %v", err)
	}
	waiting := singleWaveRun(t, f)
	if waiting.Status != execution.CaseRunRunning || waiting.Attempts != 1 {
		t.Fatalf("queued run after a 5m wait = %s attempt %d, want running attempt 1",
			waiting.Status, waiting.Attempts)
	}
	if waiting.DispatchEpoch != dispatched.DispatchEpoch {
		t.Fatalf("queue wait changed the dispatch epoch: %d -> %d",
			dispatched.DispatchEpoch, waiting.DispatchEpoch)
	}
}

// TestSchedulerReleasesRunWhenWorkItemEnsureFails: a run that cannot be queued
// must be returned to pending under a new epoch (token cleared, lease/owner
// cleared, the concrete error persisted) so the next tick can redispatch it
// instead of Recovery discovering a stranded "running" row after 10 minutes.
func TestSchedulerReleasesRunWhenWorkItemEnsureFails(t *testing.T) {
	ctx := context.Background()
	f := newQueuedDispatchFixture(t)
	f.queue.err = errors.New("queue write failed")

	err := f.scheduler.ReconcileVersion(ctx, 100)
	if err == nil {
		t.Fatal("ReconcileVersion succeeded despite a failing work queue")
	}
	if !strings.Contains(err.Error(), "queue write failed") {
		t.Fatalf("ReconcileVersion error = %q, want the queue failure", err)
	}
	released := singleWaveRun(t, f)
	if released.Status != execution.CaseRunPending {
		t.Fatalf("released status = %q, want pending", released.Status)
	}
	if released.LeaseUntil != nil {
		t.Fatalf("released dispatch kept lease until %v", released.LeaseUntil)
	}
	if released.OwnerExecutorID != nil {
		t.Fatalf("released dispatch kept owner executor %d", *released.OwnerExecutorID)
	}
	if released.OwnerInstance != "" {
		t.Fatalf("released dispatch kept owner instance %q", released.OwnerInstance)
	}
	if released.DispatchToken != "" {
		t.Fatalf("released dispatch kept token %q", released.DispatchToken)
	}
	if released.StartedAt != nil {
		t.Fatalf("released dispatch kept started_at %v", released.StartedAt)
	}
	if !strings.Contains(released.Summary.LastError, "queue write failed") {
		t.Fatalf("last error = %q, want the queue failure", released.Summary.LastError)
	}
	if released.Summary.Dispatch == nil {
		t.Fatal("released dispatch dropped the dispatch snapshot")
	}
	if released.Attempts != 1 {
		t.Fatalf("released attempts = %d, want the failed dispatch attempt kept", released.Attempts)
	}
	releasedEpoch := released.DispatchEpoch
	if got := f.queue.countType(WorkTypeExecuteCase); got != 0 {
		t.Fatalf("queued execute items after a failed ensure = %d, want 0", got)
	}

	// The next tick redispatch succeeds: a new attempt and a new epoch, so the
	// never-queued item can never be claimed by a stale token.
	f.queue.err = nil
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("redispatch ReconcileVersion: %v", err)
	}
	redispatched := singleWaveRun(t, f)
	if redispatched.Status != execution.CaseRunRunning {
		t.Fatalf("redispatch status = %q, want running", redispatched.Status)
	}
	if redispatched.Attempts != 2 {
		t.Fatalf("redispatch attempts = %d, want 2", redispatched.Attempts)
	}
	if redispatched.DispatchEpoch != releasedEpoch+1 {
		t.Fatalf("redispatch epoch = %d, want %d", redispatched.DispatchEpoch, releasedEpoch+1)
	}
	if redispatched.LeaseUntil == nil || redispatched.LeaseUntil.Sub(f.clock.Now()) < DefaultDispatchLease {
		t.Fatalf("redispatch lease = %v, want at least %v", redispatched.LeaseUntil, DefaultDispatchLease)
	}
	if got := f.queue.countType(WorkTypeExecuteCase); got != 1 {
		t.Fatalf("queued execute items = %d, want 1", got)
	}
}

// TestSchedulerReleasesRunWhenIntentTransitionFails: when the dispatch ledger
// write that follows the work-item Ensure fails, the item is already queued, so
// the run must still be released (same invariants as the Ensure-failure path)
// and the original transition error must be returned. The next tick redispatch
// must consume the next attempt.
func TestSchedulerReleasesRunWhenIntentTransitionFails(t *testing.T) {
	ctx := context.Background()
	f := newQueuedDispatchFixture(t)
	f.intents.transitionErr = errors.New("intent ledger write failed")

	err := f.scheduler.ReconcileVersion(ctx, 100)
	if err == nil {
		t.Fatal("ReconcileVersion succeeded despite a failing intent transition")
	}
	if !strings.Contains(err.Error(), "intent ledger write failed") {
		t.Fatalf("ReconcileVersion error = %q, want the intent transition failure", err)
	}
	released := singleWaveRun(t, f)
	if released.Status != execution.CaseRunPending {
		t.Fatalf("released status = %q, want pending", released.Status)
	}
	if released.LeaseUntil != nil {
		t.Fatalf("released dispatch kept lease until %v", released.LeaseUntil)
	}
	if released.OwnerExecutorID != nil {
		t.Fatalf("released dispatch kept owner executor %d", *released.OwnerExecutorID)
	}
	if released.OwnerInstance != "" {
		t.Fatalf("released dispatch kept owner instance %q", released.OwnerInstance)
	}
	if released.StartedAt != nil {
		t.Fatalf("released dispatch kept started_at %v", released.StartedAt)
	}
	if released.DispatchToken != "" {
		t.Fatalf("released dispatch kept token %q", released.DispatchToken)
	}
	// The dispatch took epoch 0 -> 1, the release 1 -> 2.
	if released.DispatchEpoch != 2 {
		t.Fatalf("released epoch = %d, want 2 (dispatch + release)", released.DispatchEpoch)
	}
	if !strings.Contains(released.Summary.LastError, "intent ledger write failed") {
		t.Fatalf("last error = %q, want the transition failure", released.Summary.LastError)
	}
	if released.Summary.RetryReason != "dispatch_failed" {
		t.Fatalf("retry reason = %q, want dispatch_failed", released.Summary.RetryReason)
	}
	if released.Summary.Dispatch == nil || released.Summary.Dispatch.Attempt != 1 {
		t.Fatalf("released dispatch snapshot = %+v, want attempt 1 kept", released.Summary.Dispatch)
	}
	if released.Attempts != 1 {
		t.Fatalf("released attempts = %d, want the failed dispatch attempt kept", released.Attempts)
	}
	// The work item was queued before the transition failed: it survives, fenced
	// by the release epoch bump.
	if got := f.queue.countType(WorkTypeExecuteCase); got != 1 {
		t.Fatalf("queued execute items after the transition failure = %d, want 1", got)
	}
	releasedEpoch := released.DispatchEpoch

	// The next tick redispatch succeeds with the next attempt and a new epoch,
	// so the stale queued item can never be claimed by its old token.
	f.intents.transitionErr = nil
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("redispatch ReconcileVersion: %v", err)
	}
	redispatched := singleWaveRun(t, f)
	if redispatched.Status != execution.CaseRunRunning {
		t.Fatalf("redispatch status = %q, want running", redispatched.Status)
	}
	if redispatched.Attempts != 2 {
		t.Fatalf("redispatch attempts = %d, want 2", redispatched.Attempts)
	}
	if redispatched.DispatchEpoch != releasedEpoch+1 {
		t.Fatalf("redispatch epoch = %d, want %d", redispatched.DispatchEpoch, releasedEpoch+1)
	}
	if redispatched.DispatchToken == "" {
		t.Fatal("redispatch did not stamp a dispatch token")
	}
	if redispatched.LeaseUntil == nil || redispatched.LeaseUntil.Sub(f.clock.Now()) < DefaultDispatchLease {
		t.Fatalf("redispatch lease = %v, want at least %v", redispatched.LeaseUntil, DefaultDispatchLease)
	}
	if redispatched.Summary.Dispatch == nil || redispatched.Summary.Dispatch.Attempt != 2 {
		t.Fatalf("redispatch snapshot = %+v, want attempt 2", redispatched.Summary.Dispatch)
	}
	// The stale (fenced) item stays queued; the working item is a new epoch.
	if got := f.queue.countType(WorkTypeExecuteCase); got != 2 {
		t.Fatalf("queued execute items after redispatch = %d, want 2 (stale + live)", got)
	}
}

// TestSchedulerReleaseDispatchKeepsClaimedRun: a worker can claim (Prepare) the
// already-queued run in the window between Ensure and the failed intent
// Transition. The release must then be a CAS miss: the claimed/running row is
// left untouched, a structured WARN is logged, and no duplicate dispatch of the
// case the worker is executing can happen.
func TestSchedulerReleaseDispatchKeepsClaimedRun(t *testing.T) {
	ctx := context.Background()
	f := newQueuedDispatchFixture(t)
	logs := captureSchedulerSlog(t)

	claimLease := f.clock.Now().Add(DefaultCaseLease)
	claimedAt := f.clock.Now().UTC().Format(time.RFC3339Nano)
	f.intents.transitionErr = errors.New("intent ledger write failed")
	f.intents.onTransitionErr = func() {
		inflight := singleWaveRun(t, f)
		ok, err := f.runs.UpdateCAS(ctx, inflight.ID, inflight.DispatchEpoch, inflight.DispatchToken,
			func(run *execution.CaseRun) error {
				// The same stored-ledger state CaseRunner.Prepare marks on claim.
				run.Status = execution.CaseRunRunning
				run.OwnerInstance = "agent-1"
				run.OwnerExecutorID = int64PtrValue(7)
				run.LeaseUntil = &claimLease
				if run.Summary.Dispatch == nil {
					run.Summary.Dispatch = &execution.CaseRunDispatchDiagnostic{Attempt: run.Attempts}
				}
				run.Summary.Dispatch.ClaimedAt = claimedAt
				return nil
			})
		if err != nil || !ok {
			t.Fatalf("simulated worker claim: ok=%v err=%v", ok, err)
		}
	}

	err := f.scheduler.ReconcileVersion(ctx, 100)
	if err == nil || !strings.Contains(err.Error(), "intent ledger write failed") {
		t.Fatalf("ReconcileVersion error = %v, want the original transition failure", err)
	}

	claimed := singleWaveRun(t, f)
	if claimed.Status != execution.CaseRunRunning {
		t.Fatalf("claimed run status = %q, want running (the release overwrote it)", claimed.Status)
	}
	if claimed.DispatchEpoch != 1 {
		t.Fatalf("claimed run epoch = %d, want the claimed epoch 1 kept", claimed.DispatchEpoch)
	}
	if claimed.DispatchToken == "" {
		t.Fatal("claimed run lost its dispatch token")
	}
	if claimed.OwnerInstance != "agent-1" || claimed.OwnerExecutorID == nil || *claimed.OwnerExecutorID != 7 {
		t.Fatalf("claimed owner = %q/%v, want agent-1/7", claimed.OwnerInstance, claimed.OwnerExecutorID)
	}
	if claimed.LeaseUntil == nil || !claimed.LeaseUntil.Equal(claimLease) {
		t.Fatalf("claimed lease = %v, want the agent lease %v", claimed.LeaseUntil, claimLease)
	}
	if claimed.Summary.Dispatch == nil || claimed.Summary.Dispatch.ClaimedAt != claimedAt {
		t.Fatalf("claimed marker = %+v, want %q", claimed.Summary.Dispatch, claimedAt)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("claimed attempts = %d, want 1", claimed.Attempts)
	}
	logged := logs.String()
	if !strings.Contains(logged, "already claimed") {
		t.Fatalf("release CAS miss was not logged: %q", logged)
	}

	// The next tick must not dispatch a duplicate: the claimed row is not
	// returned to pending, so no second execute item is queued.
	f.intents.transitionErr = nil
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("post-claim ReconcileVersion: %v", err)
	}
	after := singleWaveRun(t, f)
	if after.Status != execution.CaseRunRunning || after.Attempts != 1 ||
		after.DispatchEpoch != claimed.DispatchEpoch || after.DispatchToken != claimed.DispatchToken {
		t.Fatalf("post-claim run = %s attempt %d epoch %d token %q, want the claimed row untouched",
			after.Status, after.Attempts, after.DispatchEpoch, after.DispatchToken)
	}
	if got := f.queue.countType(WorkTypeExecuteCase); got != 1 {
		t.Fatalf("queued execute items after the claimed tick = %d, want 1 (no duplicate)", got)
	}
}
