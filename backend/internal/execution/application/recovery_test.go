package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ---------------------------------------------------------------------------
// Fakes: recovery-specific ports
// ---------------------------------------------------------------------------

// recoveryCallLog records the ordered writes recovery performs on its ports so
// a test can pin the sequence of the ordered-CAS boot sequence.
type recoveryCallLog struct{ calls []string }

// record appends one write to the log.
func (l *recoveryCallLog) record(call string) {
	l.calls = append(l.calls, call)
}

// recoveryBatchRepository is the batch repository fake used by recovery. It
// embeds the shared batch-service fake and adds the scheduler's batch-queue
// scan, the optional residual-lease scan and the write log.
type recoveryBatchRepository struct {
	*fakeBatchRepository
	log       *recoveryCallLog
	activeErr error
}

// ActiveBatches returns the batches that still own an active slot (planned or
// running), mirroring the production BatchQueue adapter. Reading the stored map
// keeps a second recovery pass in sync with the first one (the batch is still
// running but paused).
func (f *recoveryBatchRepository) ActiveBatches(context.Context) ([]domain.RunBatch, error) {
	if f.activeErr != nil {
		return nil, f.activeErr
	}
	active := make([]domain.RunBatch, 0, len(f.batches))
	for _, batch := range f.batches {
		if batch.Status == domain.BatchStatusPlanned || batch.Status == domain.BatchStatusRunning {
			active = append(active, batch)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].ID < active[j].ID })
	return active, nil
}

// ResidualLeaseBatchIDs issues every stored batch, whatever its status, as the
// leftover-lease scan candidate: a terminal batch that lost its release write
// is not in the active set any more, so the sweep must reach it by id.
func (f *recoveryBatchRepository) ResidualLeaseBatchIDs(context.Context) ([]int64, error) {
	ids := make([]int64, 0, len(f.batches))
	for id := range f.batches {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// SetPaused records the attempted batch pause before delegating, so the test
// can prove the batch write precedes the version mirror.
func (f *recoveryBatchRepository) SetPaused(ctx context.Context, batchID int64, paused bool, mode domain.PauseMode, reason string) (domain.RunBatch, error) {
	if f.log != nil {
		f.log.record("batch.set_paused")
	}
	return f.fakeBatchRepository.SetPaused(ctx, batchID, paused, mode, reason)
}

// recoveryVersionPort records the pause mirror writes without changing the
// version status, which recovery must never do.
type recoveryVersionPort struct {
	status     string
	paused     bool
	getErr     error
	pauseErr   error
	pauseCalls []markPausedCall
	complete   int
	log        *recoveryCallLog
}

func (f *recoveryVersionPort) GetVersion(_ context.Context, versionID int64) (VersionRef, error) {
	if f.getErr != nil {
		return VersionRef{}, f.getErr
	}
	return VersionRef{ID: versionID, Status: f.status, Paused: f.paused}, nil
}

func (f *recoveryVersionPort) MarkPaused(_ context.Context, versionID int64, paused bool, mode, reason string, actor int64) error {
	if f.log != nil {
		f.log.record("version.mark_paused")
	}
	f.pauseCalls = append(f.pauseCalls, markPausedCall{versionID, paused, mode, reason, actor})
	if f.pauseErr != nil {
		return f.pauseErr
	}
	f.paused = paused
	return nil
}

func (f *recoveryVersionPort) SetBatchNo(context.Context, int64, int, int64) error { return nil }

func (f *recoveryVersionPort) CompleteExecution(context.Context, int64, int64) error {
	f.complete++
	return nil
}

// recoveryLease is one active lease held in the fake session port.
type recoveryLease struct {
	sessionID int64
	holder    string
}

// recoveryReleaseCall records one SessionPort.Release invocation.
type recoveryReleaseCall struct {
	sessionID int64
	holder    string
	reason    string
}

// recoverySessionPort models the session lease surface: recovery may only
// release leases, never acquire sessions or browsers.
type recoverySessionPort struct {
	leases       []recoveryLease
	releaseCalls []recoveryReleaseCall
	releaseErr   error
	acquireCalls int
}

func (f *recoverySessionPort) Acquire(context.Context, int64, int64, string, time.Duration) (SessionRef, error) {
	f.acquireCalls++
	return SessionRef{}, errors.New("recovery must not acquire sessions")
}

func (f *recoverySessionPort) Release(_ context.Context, sessionID int64, holder, reason string) error {
	f.releaseCalls = append(f.releaseCalls, recoveryReleaseCall{sessionID, holder, reason})
	if f.releaseErr != nil {
		return f.releaseErr
	}
	kept := f.leases[:0]
	for _, lease := range f.leases {
		if lease.sessionID == sessionID && lease.holder == holder {
			continue
		}
		kept = append(kept, lease)
	}
	f.leases = kept
	return nil
}

func (f *recoverySessionPort) MarkInvalid(context.Context, int64, string) error {
	return errors.New("recovery must not invalidate sessions")
}

func (f *recoverySessionPort) PendingTakeover(context.Context, int64) (*TakeoverRef, error) {
	return nil, nil
}

func (f *recoverySessionPort) Environment(_ context.Context, environmentID int64) (EnvironmentRef, error) {
	return EnvironmentRef{ID: environmentID}, nil
}

// recoveryStepRepository is the in-memory StepRepository used to locate the
// running step of a crashed case run.
type recoveryStepRepository struct {
	byCaseRun map[int64][]domain.StepResult
	listErr   error
}

func newRecoveryStepRepository() *recoveryStepRepository {
	return &recoveryStepRepository{byCaseRun: map[int64][]domain.StepResult{}}
}

func (f *recoveryStepRepository) ListByCaseRun(_ context.Context, caseRunID int64) ([]domain.StepResult, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]domain.StepResult(nil), f.byCaseRun[caseRunID]...), nil
}

func (f *recoveryStepRepository) Upsert(_ context.Context, step domain.StepResult) (domain.StepResult, error) {
	return step, nil
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// recoveryTestDeps is the fake world of one Recovery test: one running batch per
// version, an executing version, one session lease per seeded running run.
type recoveryTestDeps struct {
	batches  *recoveryBatchRepository
	runs     *fakeCaseRunRepository
	steps    *recoveryStepRepository
	versions *recoveryVersionPort
	sessions *recoverySessionPort
	events   *fakeEventPublisher
	clock    *fakeClock
	calls    *recoveryCallLog
}

// newRecoveryTest wires a Recovery to its fakes.
func newRecoveryTest(t *testing.T) (*Recovery, *recoveryTestDeps) {
	t.Helper()
	return newRecoveryTestWithLeaseScan(t, true)
}

// newRecoveryTestWithLeaseScan wires a Recovery with or without the optional
// residual-lease scan (the production composition root may leave it nil).
func newRecoveryTestWithLeaseScan(t *testing.T, withLeaseScan bool) (*Recovery, *recoveryTestDeps) {
	t.Helper()
	calls := &recoveryCallLog{}
	deps := &recoveryTestDeps{
		batches:  &recoveryBatchRepository{fakeBatchRepository: newFakeBatchRepository(), log: calls},
		runs:     newFakeCaseRunRepository(),
		steps:    newRecoveryStepRepository(),
		versions: &recoveryVersionPort{status: versionStatusExecuting, log: calls},
		sessions: &recoverySessionPort{},
		events:   &fakeEventPublisher{},
		clock:    &fakeClock{now: testNow},
		calls:    calls,
	}
	deps.batches.caseRuns = deps.runs
	recoveryDeps := RecoveryDeps{
		Queue:    deps.batches,
		Batches:  deps.batches,
		CaseRuns: deps.runs,
		Steps:    deps.steps,
		Versions: deps.versions,
		Sessions: deps.sessions,
		Events:   deps.events,
		Clock:    deps.clock,
	}
	if withLeaseScan {
		recoveryDeps.LeaseScan = deps.batches
	}
	recovery := NewRecovery(recoveryDeps)
	return recovery, deps
}

// seedBatch stores one batch and returns it.
func (d *recoveryTestDeps) seedBatch(batchID int64, status domain.BatchStatus, paused bool) domain.RunBatch {
	batch := domain.RunBatch{
		ID:           batchID,
		VersionID:    7,
		BatchNo:      int(batchID),
		Status:       status,
		Paused:       paused,
		PauseMode:    domain.PauseModeGraceful,
		BaselineHash: testBaselineHash,
	}
	d.batches.batches[batchID] = batch
	return batch
}

// seedRun stores one case run in the requested status.
func (d *recoveryTestDeps) seedRun(runID, batchID int64, status domain.CaseRunStatus) domain.CaseRun {
	run := domain.CaseRun{
		ID:         runID,
		BatchID:    batchID,
		VersionID:  7,
		CaseNodeID: runID * 10,
		Status:     status,
		Attempt:    1,
	}
	d.runs.runs[runID] = run
	return run
}

// seedRunning stores one running case run with an active session lease held by
// its case_run holder.
func (d *recoveryTestDeps) seedRunning(batchID, runID int64) {
	if _, ok := d.batches.batches[batchID]; !ok {
		d.seedBatch(batchID, domain.BatchStatusRunning, false)
	}
	started := testNow.Add(-time.Minute)
	run := d.seedRun(runID, batchID, domain.CaseRunStatusRunning)
	run.StartedAt = &started
	d.runs.runs[runID] = run
	d.seedLease(runID, 1000+runID)
}

// seedLease grants one active lease to a seeded run and records its session id
// on the run.
func (d *recoveryTestDeps) seedLease(runID, sessionID int64) {
	run := d.runs.runs[runID]
	run.SessionID = int64Ptr(sessionID)
	d.runs.runs[runID] = run
	d.sessions.leases = append(d.sessions.leases, recoveryLease{
		sessionID: sessionID,
		holder:    recoveryTestHolder(runID),
	})
}

// seedStep records one step_result of a case run.
func (d *recoveryTestDeps) seedStep(caseRunID int64, seq int, status domain.StepStatus) {
	d.steps.byCaseRun[caseRunID] = append(d.steps.byCaseRun[caseRunID], domain.StepResult{
		ID:        int64(seq),
		CaseRunID: caseRunID,
		Seq:       seq,
		Status:    status,
	})
}

// runStatus returns the stored status of one case run.
func (d *recoveryTestDeps) runStatus(runID int64) domain.CaseRunStatus {
	return d.runs.runs[runID].Status
}

// batchPaused reports the stored pause flag of one batch.
func (d *recoveryTestDeps) batchPaused(batchID int64) bool {
	return d.batches.batches[batchID].Paused
}

// activeLeases reports how many fake leases are still active.
func (d *recoveryTestDeps) activeLeases() int {
	return len(d.sessions.leases)
}

// recoveryTestHolder is the lease holder convention: the holder names the case
// run that owns the leased session and matches the scheduler's acquisition
// helper.
func recoveryTestHolder(caseRunID int64) string {
	return fmt.Sprintf("execution:case_run:%d", caseRunID)
}

// ---------------------------------------------------------------------------
// Recover
// ---------------------------------------------------------------------------

func TestRecoveryMarksRunningInterruptedAndPausesBatch(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)
	deps.seedRunning(1, 11)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, runID := range []int64{10, 11} {
		run := deps.runs.runs[runID]
		if run.Status != domain.CaseRunStatusInterrupted {
			t.Fatalf("run %d status = %s, want interrupted", runID, run.Status)
		}
		if run.FailureReason != recoveryTestRestart {
			t.Fatalf("run %d failure_reason = %q, want %q", runID, run.FailureReason, recoveryTestRestart)
		}
		if !run.CleanupRequired {
			t.Fatalf("run %d must require cleanup", runID)
		}
		if run.FinishedAt == nil || !run.FinishedAt.Equal(testNow) {
			t.Fatalf("run %d finished_at = %v, want %v", runID, run.FinishedAt, testNow)
		}
	}

	batch := deps.batches.batches[1]
	if !batch.Paused {
		t.Fatal("batch must be paused")
	}
	if batch.PauseMode != domain.PauseModeImmediate {
		t.Fatalf("batch pause_mode = %q, want immediate", batch.PauseMode)
	}
	if batch.PauseReason != recoveryTestRestart {
		t.Fatalf("batch pause_reason = %q, want %q", batch.PauseReason, recoveryTestRestart)
	}

	if len(deps.sessions.leases) != 0 {
		t.Fatalf("active leases = %d, want 0", len(deps.sessions.leases))
	}
	if len(deps.sessions.releaseCalls) != 2 {
		t.Fatalf("release calls = %d, want 2", len(deps.sessions.releaseCalls))
	}
	for i, wantRunID := range []int64{10, 11} {
		call := deps.sessions.releaseCalls[i]
		if call.sessionID != 1000+wantRunID {
			t.Fatalf("release %d session = %d, want %d", i, call.sessionID, 1000+wantRunID)
		}
		if call.holder != recoveryTestHolder(wantRunID) {
			t.Fatalf("release %d holder = %q, want %q", i, call.holder, recoveryTestHolder(wantRunID))
		}
		if call.reason != recoveryTestRestart {
			t.Fatalf("release %d reason = %q, want %q", i, call.reason, recoveryTestRestart)
		}
	}
	if deps.sessions.acquireCalls != 0 {
		t.Fatal("recovery must not acquire sessions")
	}

	if deps.versions.status != versionStatusExecuting {
		t.Fatalf("version status = %q, recovery must not change it", deps.versions.status)
	}
	if deps.versions.complete != 0 {
		t.Fatal("recovery must not complete the version")
	}
	if len(deps.versions.pauseCalls) != 1 {
		t.Fatalf("MarkPaused calls = %d, want 1", len(deps.versions.pauseCalls))
	}
	pause := deps.versions.pauseCalls[0]
	if pause.versionID != 7 || !pause.paused || pause.mode != string(domain.PauseModeImmediate) ||
		pause.reason != recoveryTestRestart {
		t.Fatalf("MarkPaused call = %+v", pause)
	}

	wantEvents := []string{"run.paused", "run.paused", "batch.paused"}
	if !reflect.DeepEqual(deps.events.types, wantEvents) {
		t.Fatalf("events = %v, want %v", deps.events.types, wantEvents)
	}
	for _, event := range deps.events.events {
		if event.VersionID != 7 || event.BatchID != 1 {
			t.Fatalf("event addressing = %+v", event)
		}
	}
	if got := deps.events.events[0].CaseID; got != 100 {
		t.Fatalf("run.paused case_id = %d, want 100", got)
	}
	if got, _ := deps.events.events[2].Detail["mode"]; got != "immediate" {
		t.Fatalf("batch.paused mode = %v", got)
	}
	if got, _ := deps.events.events[2].Detail["reason"]; got != recoveryTestRestart {
		t.Fatalf("batch.paused reason = %v", got)
	}
}

func TestRecoveryRecordsInterruptedStepSeq(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)
	deps.seedStep(10, 1, domain.StepStatusPassed)
	deps.seedStep(10, 2, domain.StepStatusRunning)
	deps.seedStep(10, 3, domain.StepStatusPending)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	run := deps.runs.runs[10]
	if run.InterruptedStepSeq == nil || *run.InterruptedStepSeq != 2 {
		t.Fatalf("interrupted_step_seq = %v, want 2", run.InterruptedStepSeq)
	}
}

func TestRecoveryLeavesInterruptedStepSeqNilWithoutRunningStep(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)
	deps.seedStep(10, 1, domain.StepStatusPassed)
	deps.seedStep(10, 2, domain.StepStatusPending)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := deps.runs.runs[10].InterruptedStepSeq; got != nil {
		t.Fatalf("interrupted_step_seq = %v, want nil", got)
	}
}

func TestRecoveryIsIdempotent(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventCount := len(deps.events.events)
	pauseCalls := len(deps.batches.pauseCalls)
	versionCalls := len(deps.versions.pauseCalls)
	releaseCalls := len(deps.sessions.releaseCalls)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatalf("second recovery: %v", err)
	}

	if got := deps.runStatus(10); got != domain.CaseRunStatusInterrupted {
		t.Fatalf("run status = %s, want interrupted", got)
	}
	if !deps.batchPaused(1) {
		t.Fatal("batch must stay paused")
	}
	if len(deps.events.events) != eventCount {
		t.Fatalf("events = %d after second run, want %d", len(deps.events.events), eventCount)
	}
	if len(deps.batches.pauseCalls) != pauseCalls {
		t.Fatalf("SetPaused calls = %d after second run, want %d", len(deps.batches.pauseCalls), pauseCalls)
	}
	if len(deps.versions.pauseCalls) != versionCalls {
		t.Fatalf("MarkPaused calls = %d after second run, want %d", len(deps.versions.pauseCalls), versionCalls)
	}
	if len(deps.sessions.releaseCalls) != releaseCalls {
		t.Fatalf("release calls = %d after second run, want %d", len(deps.sessions.releaseCalls), releaseCalls)
	}
}

func TestRecoveryLeavesNonRunningBatchesAndRunsUntouched(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedBatch(2, domain.BatchStatusDone, false)
	deps.seedRun(20, 2, domain.CaseRunStatusPassed)
	deps.seedRunning(1, 10)
	deps.seedRun(11, 1, domain.CaseRunStatusPending)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	if batch := deps.batches.batches[2]; batch.Paused || batch.Status != domain.BatchStatusDone {
		t.Fatalf("done batch changed: %+v", batch)
	}
	if got := deps.runStatus(20); got != domain.CaseRunStatusPassed {
		t.Fatalf("passed run status = %s, want passed", got)
	}
	if got := deps.runStatus(11); got != domain.CaseRunStatusPending {
		t.Fatalf("pending run status = %s, want pending", got)
	}
	if len(deps.batches.pauseCalls) != 1 || deps.batches.pauseCalls[0].batchID != 1 {
		t.Fatalf("pause calls = %+v, want only batch 1", deps.batches.pauseCalls)
	}
	if len(deps.versions.pauseCalls) != 1 {
		t.Fatalf("MarkPaused calls = %d, want 1", len(deps.versions.pauseCalls))
	}
}

func TestRecoveryReleasesOnlyTheRunsOwnLease(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)
	deps.sessions.leases = append(deps.sessions.leases, recoveryLease{
		sessionID: 2000,
		holder:    recoveryTestHolder(999),
	})

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := deps.activeLeases(); got != 1 {
		t.Fatalf("active leases = %d, want the unrelated lease to survive", got)
	}
	if deps.sessions.leases[0].holder != recoveryTestHolder(999) {
		t.Fatalf("surviving lease = %+v", deps.sessions.leases[0])
	}
}

// TestRecoveryMirrorsVersionPauseForAlreadyPausedBatch covers the crash window
// between BatchRepository.SetPaused and VersionPort.MarkPaused: the batch flag
// committed but the version mirror did not. The next boot must repair the
// mirror without re-pausing the batch or publishing a second batch.paused.
func TestRecoveryMirrorsVersionPauseForAlreadyPausedBatch(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedBatch(1, domain.BatchStatusRunning, true)
	deps.seedRunning(1, 10)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := deps.runStatus(10); got != domain.CaseRunStatusInterrupted {
		t.Fatalf("run status = %s, want interrupted", got)
	}
	if len(deps.batches.pauseCalls) != 0 {
		t.Fatalf("SetPaused calls = %d, want 0 for an already paused batch", len(deps.batches.pauseCalls))
	}
	if len(deps.versions.pauseCalls) != 1 {
		t.Fatalf("MarkPaused calls = %d, want the missing mirror repaired", len(deps.versions.pauseCalls))
	}
	mirror := deps.versions.pauseCalls[0]
	if mirror.versionID != 7 || !mirror.paused || mirror.reason != recoveryTestRestart ||
		mirror.actor != recoveryActorSystem {
		t.Fatalf("version mirror = %+v", mirror)
	}
	if mirror.mode != string(domain.PauseModeGraceful) {
		t.Fatalf("version mirror mode = %q, want the batch's stored mode", mirror.mode)
	}
	if !reflect.DeepEqual(deps.events.types, []string{"run.paused"}) {
		t.Fatalf("events = %v, want only run.paused", deps.events.types)
	}

	// The repaired mirror makes the next boot a no-op.
	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if len(deps.versions.pauseCalls) != 1 {
		t.Fatalf("MarkPaused calls after second run = %d, want 1", len(deps.versions.pauseCalls))
	}
	if !reflect.DeepEqual(deps.events.types, []string{"run.paused"}) {
		t.Fatalf("events after second run = %v", deps.events.types)
	}
}

// TestRecoveryPausesBatchBeforeMirroringVersion pins the write order required by
// the accepted ordered-CAS deviation: the batch pause is authoritative for
// dispatch, so it is committed before the version mirror.
func TestRecoveryPausesBatchBeforeMirroringVersion(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{"batch.set_paused", "version.mark_paused"}
	if !reflect.DeepEqual(deps.calls.calls, want) {
		t.Fatalf("writes = %v, want %v", deps.calls.calls, want)
	}
}

// TestRecoverySkipsVersionMirrorWhenBatchIsTerminal covers the race where the
// batch finishes between the active-batch scan and the pause write (finished or
// cancelled both answer ErrBatchFinished). The version must not be paused for a
// batch that is no longer running, and boot must not fail.
func TestRecoverySkipsVersionMirrorWhenBatchIsTerminal(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)
	deps.batches.pauseErr = domain.ErrBatchFinished

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatalf("a terminal batch must not fail recovery: %v", err)
	}

	if got := deps.runStatus(10); got != domain.CaseRunStatusInterrupted {
		t.Fatalf("run status = %s, want interrupted", got)
	}
	if len(deps.versions.pauseCalls) != 0 {
		t.Fatalf("MarkPaused calls = %d, want the version mirror skipped", len(deps.versions.pauseCalls))
	}
	if !reflect.DeepEqual(deps.events.types, []string{"run.paused"}) {
		t.Fatalf("events = %v, want only run.paused", deps.events.types)
	}
	if !reflect.DeepEqual(deps.calls.calls, []string{"batch.set_paused"}) {
		t.Fatalf("writes = %v, want only the attempted batch pause", deps.calls.calls)
	}
}

// TestRecoveryRecountsBatchAfterInterruptingRuns checks that the stored tally no
// longer claims the crashed runs are still running.
func TestRecoveryRecountsBatchAfterInterruptingRuns(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedRunning(1, 10)
	deps.seedRunning(1, 11)

	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	if deps.batches.countsCalls != 1 {
		t.Fatalf("UpdateCounts calls = %d, want 1", deps.batches.countsCalls)
	}
	if deps.batches.counts.CaseCount != 2 || deps.batches.counts.InterruptedCount != 2 {
		t.Fatalf("recounted batch = %+v, want 2 interrupted runs", deps.batches.counts)
	}

	// The next boot recounts again (the recount is idempotent) and keeps the
	// same tally, repairing a tally a previous pass failed to store.
	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if deps.batches.countsCalls != 2 {
		t.Fatalf("UpdateCounts calls after second run = %d, want 2", deps.batches.countsCalls)
	}
	if deps.batches.counts.InterruptedCount != 2 || deps.batches.counts.CaseCount != 2 {
		t.Fatalf("recounted batch after second run = %+v, want 2 interrupted runs", deps.batches.counts)
	}
}

func TestRecoveryRequiresABatchQueue(t *testing.T) {
	recovery := NewRecovery(RecoveryDeps{
		Batches:  newFakeBatchRepository(),
		CaseRuns: newFakeCaseRunRepository(),
		Steps:    newRecoveryStepRepository(),
		Versions: &recoveryVersionPort{status: versionStatusExecuting},
		Sessions: &recoverySessionPort{},
	})

	err := recovery.Recover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "batch queue") {
		t.Fatalf("err = %v, want a missing batch-queue error", err)
	}
}

// ---------------------------------------------------------------------------
// Sweep
// ---------------------------------------------------------------------------

func TestSweepReleasesLeasesOfNonRunningCaseRuns(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedBatch(1, domain.BatchStatusRunning, false)
	deps.seedRun(10, 1, domain.CaseRunStatusRunning)
	deps.seedLease(10, 1010)
	deps.seedRun(11, 1, domain.CaseRunStatusPassed)
	deps.seedLease(11, 1011)
	deps.seedRun(12, 1, domain.CaseRunStatusInterrupted)
	deps.seedLease(12, 1012)

	if err := recovery.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := deps.activeLeases(); got != 1 {
		t.Fatalf("active leases = %d, want only the running run's lease", got)
	}
	if deps.sessions.leases[0].holder != recoveryTestHolder(10) {
		t.Fatalf("surviving lease = %+v, want the running run's lease", deps.sessions.leases[0])
	}
	wantHolders := []string{recoveryTestHolder(11), recoveryTestHolder(12)}
	gotHolders := make([]string, 0, len(deps.sessions.releaseCalls))
	for _, call := range deps.sessions.releaseCalls {
		gotHolders = append(gotHolders, call.holder)
		if call.reason != recoveryTestRestart {
			t.Fatalf("sweep reason = %q, want %q", call.reason, recoveryTestRestart)
		}
	}
	if !reflect.DeepEqual(gotHolders, wantHolders) {
		t.Fatalf("swept holders = %v, want %v", gotHolders, wantHolders)
	}
	if len(deps.events.events) != 0 {
		t.Fatalf("sweep published %d events, want none", len(deps.events.events))
	}
}

// TestSweepReleasesTerminalBatchLeftovers covers the residual lease of a
// terminal batch: the finish that should have released it lost its write, and
// the batch already left the active set, so the active-batch scan cannot see it.
func TestSweepReleasesTerminalBatchLeftovers(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedBatch(2, domain.BatchStatusDone, false)
	deps.seedRun(20, 2, domain.CaseRunStatusPassed)
	deps.seedLease(20, 1020)
	deps.seedRun(21, 2, domain.CaseRunStatusInterrupted)
	deps.seedLease(21, 1021)
	deps.seedBatch(3, domain.BatchStatusCancelled, false)
	deps.seedRun(30, 3, domain.CaseRunStatusSkipped)
	deps.seedLease(30, 1030)

	if err := recovery.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := deps.activeLeases(); got != 0 {
		t.Fatalf("active leases = %d, want every terminal-batch leftover released", got)
	}
	wantHolders := []string{
		recoveryTestHolder(20),
		recoveryTestHolder(21),
		recoveryTestHolder(30),
	}
	gotHolders := make([]string, 0, len(deps.sessions.releaseCalls))
	for _, call := range deps.sessions.releaseCalls {
		gotHolders = append(gotHolders, call.holder)
		if call.reason != recoveryTestRestart {
			t.Fatalf("sweep reason = %q, want %q", call.reason, recoveryTestRestart)
		}
	}
	if !reflect.DeepEqual(gotHolders, wantHolders) {
		t.Fatalf("swept holders = %v, want %v", gotHolders, wantHolders)
	}
	if len(deps.events.events) != 0 {
		t.Fatalf("sweep published %d events, want none", len(deps.events.events))
	}
	if got := deps.runStatus(20); got != domain.CaseRunStatusPassed {
		t.Fatalf("sweep changed run 20 to %s", got)
	}
}

// TestSweepWithoutLeaseScanCoversActiveBatches pins the documented degradation:
// without the optional residual-lease scan the sweep still covers the active
// batches.
func TestSweepWithoutLeaseScanCoversActiveBatches(t *testing.T) {
	recovery, deps := newRecoveryTestWithLeaseScan(t, false)
	deps.seedBatch(1, domain.BatchStatusRunning, false)
	deps.seedRun(11, 1, domain.CaseRunStatusPassed)
	deps.seedLease(11, 1011)

	if err := recovery.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := deps.activeLeases(); got != 0 {
		t.Fatalf("active leases = %d, want 0", got)
	}
}

func TestSweepIsIdempotent(t *testing.T) {
	recovery, deps := newRecoveryTest(t)
	deps.seedBatch(1, domain.BatchStatusRunning, false)
	deps.seedRun(11, 1, domain.CaseRunStatusPassed)
	deps.seedLease(11, 1011)

	if err := recovery.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Sweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := deps.activeLeases(); got != 0 {
		t.Fatalf("active leases = %d, want 0", got)
	}
}

func TestSweepRequiresABatchQueue(t *testing.T) {
	recovery := NewRecovery(RecoveryDeps{
		Batches:  newFakeBatchRepository(),
		CaseRuns: newFakeCaseRunRepository(),
		Steps:    newRecoveryStepRepository(),
		Versions: &recoveryVersionPort{status: versionStatusExecuting},
		Sessions: &recoverySessionPort{},
	})

	err := recovery.Sweep(context.Background())
	if err == nil || !strings.Contains(err.Error(), "batch queue") {
		t.Fatalf("err = %v, want a missing batch-queue error", err)
	}
}

// recoveryTestRestart is the reason recovery stores on the interrupted run, the
// paused batch and the released lease.
const recoveryTestRestart = "restart"
