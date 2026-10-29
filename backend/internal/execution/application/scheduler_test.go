package application

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

const (
	schedBatchID   int64 = 55
	schedVersionID int64 = 7
	schedSystemID  int64 = 3
	schedEnvID     int64 = 4
)

// schedWorld is the fake world of one scheduler test.
type schedWorld struct {
	queue    *schedQueue
	batches  *schedBatchRepo
	runs     *schedCaseRunRepo
	steps    *schedStepRepo
	sessions *schedSessionPort
	runtime  *schedRuntime
	versions *schedVersionPort
	mindmap  *schedMindmap
	events   *schedEvents
	browser  *schedBrowser
	ids      *schedIDs
	clock    *schedClock
	autonomy *schedAutonomy
}

// newSchedulerTest wires a Scheduler and its CaseRunner to in-memory fakes. The
// batch has no pending run until the test seeds one.
func newSchedulerTest(t *testing.T, maxConcurrent int) (*Scheduler, *schedWorld) {
	t.Helper()
	world := newSchedWorld()
	runner := NewCaseRunner(CaseRunnerDeps{
		CaseRuns:     world.runs,
		Steps:        world.steps,
		Batches:      world.batches,
		Sessions:     world.sessions,
		Runtime:      world.runtime,
		Mindmap:      world.mindmap,
		Events:       world.events,
		Clock:        world.clock,
		Browser:      world.browser,
		Timeout:      2 * time.Second,
		PollInterval: time.Millisecond,
	})
	scheduler := NewScheduler(SchedulerDeps{
		Queue:         world.queue,
		Batches:       world.batches,
		CaseRuns:      world.runs,
		Versions:      world.versions,
		Sessions:      world.sessions,
		Mindmap:       world.mindmap,
		Events:        world.events,
		Clock:         world.clock,
		IDs:           world.ids,
		Runner:        runner,
		Autonomy:      world.autonomy,
		MaxConcurrent: maxConcurrent,
		Tick:          time.Millisecond,
		LeaseTTL:      time.Minute,
	})
	return scheduler, world
}

// newSchedWorld starts with one running, unpaused batch bound to an executing
// version.
func newSchedWorld() *schedWorld {
	world := &schedWorld{
		queue:    &schedQueue{},
		runs:     newSchedCaseRunRepo(),
		steps:    newSchedStepRepo(),
		sessions: newSchedSessionPort(),
		runtime:  newSchedRuntime(),
		versions: &schedVersionPort{status: "executing", systemID: int64Pointer(schedSystemID), environmentID: int64Pointer(schedEnvID)},
		mindmap:  &schedMindmap{},
		events:   &schedEvents{},
		browser:  newSchedBrowser(),
		ids:      &schedIDs{},
		clock:    &schedClock{now: testNow},
		autonomy: &schedAutonomy{},
	}
	world.batches = newSchedBatchRepo(world.runs)
	batch := domain.RunBatch{
		ID:           schedBatchID,
		VersionID:    schedVersionID,
		BatchNo:      1,
		Status:       domain.BatchStatusRunning,
		PauseMode:    domain.PauseModeGraceful,
		BaselineHash: testBaselineHash,
		CaseCount:    0,
		CreatedAt:    testNow,
		UpdatedAt:    testNow,
	}
	world.batches.seed(batch)
	world.queue.seed(batch)
	world.sessions.environment = EnvironmentRef{
		ID:           schedEnvID,
		SystemID:     schedSystemID,
		Name:         "local",
		BaseURL:      "http://127.0.0.1:13335",
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	}
	return world
}

// seedPending stores one pending case run with the given snapshot priority.
func (w *schedWorld) seedPending(caseRunID int64, priority string) domain.CaseRun {
	run := domain.CaseRun{
		ID:           caseRunID,
		BatchID:      schedBatchID,
		VersionID:    schedVersionID,
		CaseNodeID:   caseRunID,
		CaseRevision: 1,
		CaseHash:     "hash",
		Snapshot:     schedSnapshot(caseRunID, priority),
		Attempt:      1,
		Status:       domain.CaseRunStatusPending,
		CreatedAt:    testNow,
		UpdatedAt:    testNow,
	}
	w.runs.seed(run)
	w.batches.bumpCaseCount(schedBatchID, 1)
	return run
}

// seedRun stores an existing run.
func (w *schedWorld) seedRun(run domain.CaseRun) {
	w.runs.seed(run)
}

// blockRuns makes every StartCaseRun wait until releaseRuns is called.
func (w *schedWorld) blockRuns() {
	w.runtime.setStartGate(make(chan struct{}))
}

// releaseRuns unblocks every waiting StartCaseRun.
func (w *schedWorld) releaseRuns() {
	w.runtime.releaseStartGate()
}

// startedOrder returns the case run ids the fake runtime started, in call order.
func (w *schedWorld) startedOrder() []int64 {
	return w.runtime.startedIDs()
}

// waitStarted waits until n case runs reached StartCaseRun.
func (w *schedWorld) waitStarted(t *testing.T, n int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d case runs started", n), func() bool {
		return len(w.startedOrder()) >= n
	})
}

// run returns a stored case run.
func (w *schedWorld) run(t *testing.T, caseRunID int64) domain.CaseRun {
	t.Helper()
	run, err := w.runs.GetCaseRun(context.Background(), schedBatchID, caseRunID)
	if err != nil {
		t.Fatalf("get case run %d: %v", caseRunID, err)
	}
	return run
}

// listRuns returns every stored case run of the fixture batch, newest first.
func (w *schedWorld) listRuns(t *testing.T) []domain.CaseRun {
	t.Helper()
	runs, _, err := w.runs.ListCaseRuns(context.Background(), schedBatchID, "", 0, 100)
	if err != nil {
		t.Fatalf("list case runs: %v", err)
	}
	return runs
}

// seedBlockedSession stores one blocked(session) case run that holds a session.
func (w *schedWorld) seedBlockedSession(caseRunID, sessionID int64) domain.CaseRun {
	run := domain.CaseRun{
		ID:            caseRunID,
		BatchID:       schedBatchID,
		VersionID:     schedVersionID,
		CaseNodeID:    caseRunID,
		Attempt:       1,
		Snapshot:      schedSnapshot(caseRunID, "P0"),
		Status:        domain.CaseRunStatusBlocked,
		FailureClass:  failureClassPointer(domain.FailureClassSession),
		FailureReason: "takeover_required",
		SessionID:     &sessionID,
		CreatedAt:     testNow,
		UpdatedAt:     testNow,
	}
	w.seedRun(run)
	return run
}

// waitFor polls cond until it is true or a short deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// schedSnapshot is one immutable case snapshot with two steps and one assertion.
func schedSnapshot(caseNodeID int64, priority string) domain.CaseSnapshot {
	return domain.CaseSnapshot{
		Node: domain.CaseSnapshotNode{
			ID:       caseNodeID,
			Title:    fmt.Sprintf("case %d", caseNodeID),
			Priority: priority,
		},
		Steps: []domain.CaseSnapshotStep{
			{Seq: 1, Action: "open", Expected: "opened"},
			{Seq: 2, Action: "submit", Expected: "submitted"},
		},
		Assertions: []domain.CaseSnapshotAssertion{
			{ID: caseNodeID*10 + 1, StepSeq: 2, Kind: "ui", Target: "body", Expression: "exists", Expected: "visible"},
		},
		GraphHash:  testBaselineHash,
		CapturedAt: testNow,
	}
}

// ---------------------------------------------------------------------------
// Scheduler: concurrency / priority / session states
// ---------------------------------------------------------------------------

func TestSchedulerRespectsConcurrencyAndPriority(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 2)
	world.seedPending(1, "P2")
	world.seedPending(2, "P0")
	world.seedPending(3, "P1")
	world.blockRuns()

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	world.waitStarted(t, 2)

	got := append([]int64(nil), world.startedOrder()...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if !reflect.DeepEqual(got, []int64{2, 3}) {
		t.Fatalf("started case runs = %v, want priority order [2 3]", got)
	}
	if len(world.startedOrder()) != 2 {
		t.Fatalf("concurrency cap violated: %v", world.startedOrder())
	}

	world.runtime.setState(AgentRunState{Status: "done"})
	world.releaseRuns()
	scheduler.Wait()
}

func TestSchedulerQueuesWhenSessionIsBusy(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedPending(1, "P0")
	world.sessions.acquireErr = ErrSessionBusy

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := world.run(t, 1).Status; got != domain.CaseRunStatusPending {
		t.Fatalf("status = %s, want pending", got)
	}
	if len(world.startedOrder()) != 0 {
		t.Fatalf("a busy session must not start an agent run: %v", world.startedOrder())
	}
	if len(world.sessions.releases) != 0 {
		t.Fatalf("nothing was acquired, so nothing may be released: %v", world.sessions.releases)
	}
}

func TestSchedulerBlocksWhenNoSessionIsAvailable(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedPending(1, "P0")
	world.sessions.acquireErr = ErrSessionUnavailable

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := world.run(t, 1)
	if run.Status != domain.CaseRunStatusBlocked {
		t.Fatalf("status = %s, want blocked", run.Status)
	}
	if run.FailureClass == nil || *run.FailureClass != domain.FailureClassSession {
		t.Fatalf("failure class = %v, want session", run.FailureClass)
	}
	if run.FailureReason != "no_session" {
		t.Fatalf("failure reason = %q, want no_session", run.FailureReason)
	}
	if !world.events.has("run.finished") || !world.events.has("case.status_changed") {
		t.Fatalf("events = %v", world.events.types())
	}
	if got := world.mindmap.executed(); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("MarkExecuted = %v, want [1]", got)
	}
	if world.batches.countsCalls == 0 {
		t.Fatal("blocking a case must recount the batch")
	}
}

func TestSchedulerTakeoverGateAndRequeue(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	sessionID := int64(500)
	blocked := domain.CaseRun{
		ID:            10,
		BatchID:       schedBatchID,
		VersionID:     schedVersionID,
		CaseNodeID:    10,
		Attempt:       1,
		Snapshot:      schedSnapshot(10, "P0"),
		Source:        domain.CaseSourceAISupplement,
		Status:        domain.CaseRunStatusBlocked,
		FailureClass:  failureClassPointer(domain.FailureClassSession),
		FailureReason: "takeover_required",
		SessionID:     &sessionID,
		CreatedAt:     testNow,
		UpdatedAt:     testNow,
	}
	world.seedRun(blocked)
	world.sessions.takeovers[sessionID] = &TakeoverRef{ID: 99, SessionID: sessionID, Reason: "mfa"}

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := world.events.count("human.requested"); got != 1 {
		t.Fatalf("human.requested events = %d, want 1", got)
	}
	if got := world.events.count("gate.pending"); got != 1 {
		t.Fatalf("gate.pending events = %d, want 1", got)
	}
	detail := world.events.firstOf("gate.pending").Detail
	if detail["takeover_id"] != "99" || detail["case_run_id"] != "10" || detail["session_id"] != "500" {
		t.Fatalf("gate detail = %#v", detail)
	}
	batch, err := world.batches.GetBatch(context.Background(), schedVersionID, schedBatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != domain.BatchStatusRunning {
		t.Fatalf("a waiting takeover must keep the batch active, status = %s", batch.Status)
	}

	// A second tick must not duplicate the gate events while the takeover waits.
	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := world.events.count("human.requested"); got != 1 {
		t.Fatalf("human.requested was repeated: %d", got)
	}

	// The takeover completes: the next tick requeues a new attempt.
	delete(world.sessions.takeovers, sessionID)
	world.sessions.acquireResult = SessionRef{SessionID: 600, SnapshotID: 601}
	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := world.events.count("gate.resolved"); got != 1 {
		t.Fatalf("gate.resolved events = %d, want 1", got)
	}
	runs, _, err := world.runs.ListCaseRuns(context.Background(), schedBatchID, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var requeued *domain.CaseRun
	for i := range runs {
		if runs[i].ID != blocked.ID {
			requeued = &runs[i]
		}
	}
	if requeued == nil {
		t.Fatal("no requeued attempt was created")
	}
	if requeued.Attempt != 2 || requeued.RetryOfCaseRunID == nil || *requeued.RetryOfCaseRunID != blocked.ID {
		t.Fatalf("requeued attempt = %+v", requeued)
	}
	if requeued.Source != domain.CaseSourceAISupplement {
		t.Fatalf("requeued source = %q, want the original ai_supplement (T7 finding B)",
			requeued.Source)
	}
	if requeued.Status != domain.CaseRunStatusPending && requeued.Status != domain.CaseRunStatusRunning {
		t.Fatalf("requeued status = %s", requeued.Status)
	}
	if !world.sessions.released(600, "requeue") {
		t.Fatalf("the requeue probe lease was not released: %+v", world.sessions.releases)
	}

	world.runtime.setState(AgentRunState{Status: "done"})
	scheduler.Wait()
}

func TestSchedulerRequeuesTakeoverOnlyOnceAcrossTicks(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	sessionID := int64(500)
	blocked := world.seedBlockedSession(10, sessionID)
	world.sessions.takeovers[sessionID] = &TakeoverRef{ID: 99, SessionID: sessionID, Reason: "mfa"}

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := world.events.count("gate.pending"); got != 1 {
		t.Fatalf("gate.pending events = %d, want 1", got)
	}

	// The takeover completes. Hold the requeued attempt in StartCaseRun so the
	// two ticks observe the same in-flight attempt instead of racing its finish.
	world.blockRuns()
	world.runtime.setState(AgentRunState{Status: "done"})
	delete(world.sessions.takeovers, sessionID)
	world.clock.now = testNow.Add(time.Minute)

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs := world.listRuns(t)
	if len(runs) != 2 {
		t.Fatalf("case runs after the requeue tick = %d, want 2: %+v", len(runs), runs)
	}
	var requeued domain.CaseRun
	for _, run := range runs {
		if run.ID != blocked.ID {
			requeued = run
		}
	}
	if requeued.Attempt != blocked.Attempt+1 ||
		requeued.RetryOfCaseRunID == nil || *requeued.RetryOfCaseRunID != blocked.ID {
		t.Fatalf("requeued attempt = %+v", requeued)
	}
	if got := world.events.count("gate.resolved"); got != 1 {
		t.Fatalf("gate.resolved events = %d, want 1", got)
	}
	resolved := world.events.firstOf("gate.resolved")
	if resolved.Detail["case_run_id"] != "10" ||
		resolved.Detail["new_case_run_id"] != fmt.Sprintf("%d", requeued.ID) ||
		resolved.Detail["takeover_id"] != "99" {
		t.Fatalf("gate.resolved detail = %#v", resolved.Detail)
	}

	// A second tick with the same blocked row must not create another attempt nor
	// repeat the resolution: the blocked run is no longer the latest attempt.
	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runs := world.listRuns(t); len(runs) != 2 {
		t.Fatalf("a second tick created another attempt: %+v", runs)
	}
	if got := world.events.count("gate.resolved"); got != 1 {
		t.Fatalf("gate.resolved was repeated: %d", got)
	}

	world.releaseRuns()
	scheduler.Wait()
}

func TestSchedulerResolvesGateWhenTakeoverIsCancelled(t *testing.T) {
	t.Run("paused while the gate was pending", func(t *testing.T) {
		scheduler, world := newSchedulerTest(t, 1)
		sessionID := int64(500)
		world.seedBlockedSession(10, sessionID)
		world.sessions.takeovers[sessionID] = &TakeoverRef{ID: 99, SessionID: sessionID, Reason: "mfa"}

		if err := scheduler.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := world.events.count("gate.pending"); got != 1 {
			t.Fatalf("gate.pending events = %d, want 1", got)
		}

		delete(world.sessions.takeovers, sessionID)
		world.batches.setPaused(schedBatchID, true, domain.PauseModeGraceful)
		if err := scheduler.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := world.events.count("gate.resolved"); got != 1 {
			t.Fatalf("gate.resolved events = %d, want 1", got)
		}
		if got := world.events.count("human.requested"); got != 1 {
			t.Fatalf("human.requested was repeated: %d", got)
		}

		// Once resolved, later ticks must stay silent.
		if err := scheduler.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := world.events.count("gate.resolved"); got != 1 {
			t.Fatalf("gate.resolved was repeated: %d", got)
		}
	})

	t.Run("session still busy after the takeover disappeared", func(t *testing.T) {
		scheduler, world := newSchedulerTest(t, 1)
		sessionID := int64(500)
		world.seedBlockedSession(10, sessionID)
		// A live running run keeps the batch active so the tick can be repeated.
		world.seedRun(domain.CaseRun{
			ID: 11, BatchID: schedBatchID, VersionID: schedVersionID, CaseNodeID: 11,
			Attempt: 1, Snapshot: schedSnapshot(11, "P3"),
			Status: domain.CaseRunStatusRunning, SessionID: &sessionID,
			CreatedAt: testNow, UpdatedAt: testNow,
		})
		world.sessions.takeovers[sessionID] = &TakeoverRef{ID: 99, SessionID: sessionID, Reason: "mfa"}

		if err := scheduler.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		delete(world.sessions.takeovers, sessionID)
		world.sessions.acquireErr = ErrSessionBusy

		if err := scheduler.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := world.events.count("gate.resolved"); got != 1 {
			t.Fatalf("gate.resolved events = %d, want 1", got)
		}
		if runs := world.listRuns(t); len(runs) != 2 {
			t.Fatalf("a busy session must not create an attempt: %+v", runs)
		}

		if err := scheduler.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := world.events.count("gate.resolved"); got != 1 {
			t.Fatalf("gate.resolved was repeated: %d", got)
		}
	})
}

func TestSchedulerRequeuesInterruptedRunsAfterResume(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	// No environment binding keeps the tick to the requeue step only.
	world.versions.systemID = nil
	world.versions.environmentID = nil
	interruptedStep := 2
	run := domain.CaseRun{
		ID:                 10,
		BatchID:            schedBatchID,
		VersionID:          schedVersionID,
		CaseNodeID:         10,
		Attempt:            1,
		Snapshot:           schedSnapshot(10, "P1"),
		Status:             domain.CaseRunStatusInterrupted,
		FailureReason:      "paused",
		CleanupRequired:    true,
		InterruptedStepSeq: &interruptedStep,
		CreatedAt:          testNow,
		UpdatedAt:          testNow,
	}
	world.seedRun(run)

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored := world.run(t, 10)
	if stored.Status != domain.CaseRunStatusPending {
		t.Fatalf("status = %s, want pending", stored.Status)
	}
	if !stored.CleanupRequired || stored.InterruptedStepSeq == nil || *stored.InterruptedStepSeq != 2 {
		t.Fatalf("checkpoint lost: %+v", stored)
	}
	if !world.events.has("run.resumed") {
		t.Fatalf("events = %v", world.events.types())
	}
}

func TestSchedulerCompletesBatchWhenEveryCaseIsTerminal(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedRun(domain.CaseRun{
		ID:         10,
		BatchID:    schedBatchID,
		VersionID:  schedVersionID,
		CaseNodeID: 10,
		Attempt:    1,
		Snapshot:   schedSnapshot(10, "P0"),
		Status:     domain.CaseRunStatusPassed,
		CreatedAt:  testNow,
		UpdatedAt:  testNow,
	})

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch, err := world.batches.GetBatch(context.Background(), schedVersionID, schedBatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != domain.BatchStatusDone {
		t.Fatalf("batch status = %s, want done", batch.Status)
	}
	if batch.FinishedAt == nil || !batch.FinishedAt.Equal(testNow) {
		t.Fatalf("batch finished_at = %v", batch.FinishedAt)
	}
	// The P7 scheduler never completes the version itself: the autonomous chain
	// settles every terminal batch first and only then finalizes (spec §5.3).
	if world.versions.completeCalls != 0 {
		t.Fatalf("CompleteExecution calls = %d, want 0", world.versions.completeCalls)
	}
	if len(world.autonomy.batches) != 1 || world.autonomy.batches[0].ID != schedBatchID {
		t.Fatalf("autonomy hand-off = %+v, want one terminal batch %d",
			world.autonomy.batches, schedBatchID)
	}
	if world.batches.countsCalls == 0 {
		t.Fatal("finishing a batch must recount it")
	}
	finished := world.events.firstOf("batch.finished")
	if finished == nil {
		t.Fatalf("events = %v", world.events.types())
	}
	if finished.Detail["status"] != string(domain.BatchStatusDone) {
		t.Fatalf("batch.finished detail = %#v", finished.Detail)
	}
}

func TestSchedulerCompletesBatchOnlyWhenDoneCASWins(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedRun(domain.CaseRun{
		ID: 10, BatchID: schedBatchID, VersionID: schedVersionID, CaseNodeID: 10,
		Attempt: 1, Snapshot: schedSnapshot(10, "P0"),
		Status: domain.CaseRunStatusPassed, CreatedAt: testNow, UpdatedAt: testNow,
	})
	// A concurrent cancel commits after the recount but before the batch CAS.
	world.batches.afterCounts = func() {
		if _, err := world.batches.UpdateBatchStatus(context.Background(), schedBatchID,
			domain.BatchStatusRunning, domain.BatchStatusCancelled, nil); err != nil {
			t.Errorf("concurrent cancel: %v", err)
		}
	}

	err := scheduler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("the lost batch-status CAS must be reported")
	}
	batch, getErr := world.batches.GetBatch(context.Background(), schedVersionID, schedBatchID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if batch.Status != domain.BatchStatusCancelled {
		t.Fatalf("batch status = %s, want cancelled", batch.Status)
	}
	if world.versions.completeCalls != 0 {
		t.Fatalf("version completed %d times although the batch CAS lost", world.versions.completeCalls)
	}
	if len(world.batches.statusCalls) != 2 {
		t.Fatalf("batch status calls = %+v, want cancel + running->done attempt", world.batches.statusCalls)
	}
	last := world.batches.statusCalls[len(world.batches.statusCalls)-1]
	if last.from != domain.BatchStatusRunning || last.to != domain.BatchStatusDone {
		t.Fatalf("last batch status call = %+v, want running->done", last)
	}
}

// TestSchedulerDoesNotRedispatchActiveRun pins the guard that closes the
// graceful-park window: a pending row whose previous runner is still tearing
// down must not be dispatched a second time.
func TestSchedulerDoesNotRedispatchActiveRun(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 2)
	world.seedPending(1, "P0")
	scheduler.mu.Lock()
	scheduler.active[1] = struct{}{}
	scheduler.mu.Unlock()

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stored := world.run(t, 1); stored.Status != domain.CaseRunStatusPending {
		t.Fatalf("an active run was started again: status = %s", stored.Status)
	}
}

// ---------------------------------------------------------------------------
// CaseRunner: pause behaviour and classification
// ---------------------------------------------------------------------------

func TestGracefulPauseParksPending(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedPending(1, "P0")
	world.runtime.setState(AgentRunState{Status: "running"})
	releaseStatus := make(chan domain.CaseRunStatus, 1)
	world.sessions.onRelease = func() {
		run, err := world.runs.GetCaseRun(context.Background(), schedBatchID, 1)
		if err != nil {
			return
		}
		select {
		case releaseStatus <- run.Status:
		default:
		}
	}

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	world.waitStarted(t, 1)
	world.batches.setPaused(schedBatchID, true, domain.PauseModeGraceful)

	waitFor(t, "graceful pause to park the run", func() bool {
		return world.run(t, 1).Status == domain.CaseRunStatusPending
	})
	scheduler.Wait()

	select {
	case status := <-releaseStatus:
		if status != domain.CaseRunStatusRunning {
			t.Fatalf("lease released while the run was %s, want running (release before parking)", status)
		}
	default:
		t.Fatal("graceful pause did not release the session lease")
	}

	stored := world.run(t, 1)
	if stored.Status != domain.CaseRunStatusPending {
		t.Fatalf("status = %s, want pending", stored.Status)
	}
	if stored.InterruptedStepSeq != nil || stored.CleanupRequired {
		t.Fatalf("graceful pause must not interrupt: %+v", stored)
	}
	if len(world.runtime.canceled()) != 1 {
		t.Fatalf("CancelRun calls = %v, want 1", world.runtime.canceled())
	}
	if !world.sessions.released(500, "paused") {
		t.Fatalf("pause did not release the lease: %+v", world.sessions.releases)
	}
	if len(world.browser.closedIDs()) != 1 {
		t.Fatalf("pause did not close the context: %v", world.browser.closedIDs())
	}
	if !world.events.has("run.paused") {
		t.Fatalf("events = %v", world.events.types())
	}
}

func TestImmediatePauseInterruptsWithStepSeq(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	run := world.seedPending(1, "P0")
	// Step 1 completed, step 2 is in flight when the immediate pause lands.
	world.steps.seed(domain.StepResult{
		ID: 21, BatchID: schedBatchID, CaseRunID: run.ID, Seq: 1,
		Status: domain.StepStatusPassed,
	})
	world.steps.seed(domain.StepResult{
		ID: 22, BatchID: schedBatchID, CaseRunID: run.ID, Seq: 2,
		Status: domain.StepStatusRunning,
	})
	world.runtime.setState(AgentRunState{Status: "running"})

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	world.waitStarted(t, 1)
	world.batches.setPaused(schedBatchID, true, domain.PauseModeImmediate)

	waitFor(t, "immediate pause to interrupt the run", func() bool {
		return world.run(t, 1).Status == domain.CaseRunStatusInterrupted
	})
	scheduler.Wait()

	stored := world.run(t, 1)
	if !stored.CleanupRequired {
		t.Fatalf("immediate pause must require cleanup: %+v", stored)
	}
	if stored.InterruptedStepSeq == nil || *stored.InterruptedStepSeq != 2 {
		t.Fatalf("interrupted_step_seq = %v, want 2", stored.InterruptedStepSeq)
	}
	if len(world.runtime.canceled()) != 1 {
		t.Fatalf("CancelRun calls = %v, want 1", world.runtime.canceled())
	}
	if len(world.browser.canceledIDs()) != 1 {
		t.Fatalf("executor Cancel calls = %v, want 1", world.browser.canceledIDs())
	}
	paused := world.events.firstOf("run.paused")
	if paused == nil || paused.Detail["mode"] != string(domain.PauseModeImmediate) ||
		paused.Detail["case_run_id"] != "1" {
		t.Fatalf("run.paused = %#v", paused)
	}
}

func TestPauseAfterCompletionKeepsTerminalStatus(t *testing.T) {
	_, world := newSchedulerTest(t, 1)
	run := world.seedPending(1, "P0")
	// Simulate the case finish committing before the pause is observed.
	passed := run
	passed.Status = domain.CaseRunStatusPassed
	world.runs.seed(passed)
	world.runtime.setState(AgentRunState{Status: "done"})
	world.batches.setPaused(schedBatchID, true, domain.PauseModeImmediate)

	runner := NewCaseRunner(CaseRunnerDeps{
		CaseRuns: world.runs, Steps: world.steps, Batches: world.batches,
		Sessions: world.sessions, Runtime: world.runtime, Mindmap: world.mindmap,
		Events: world.events, Clock: world.clock, Browser: world.browser,
		Timeout: time.Second, PollInterval: time.Millisecond,
	})
	runner.Run(context.Background(), passed, SessionRef{SessionID: 500}, world.sessions.environment)

	stored := world.run(t, 1)
	if stored.Status != domain.CaseRunStatusPassed {
		t.Fatalf("status = %s, want passed", stored.Status)
	}
	if stored.CleanupRequired || stored.InterruptedStepSeq != nil {
		t.Fatalf("a completed run must not be rewound: %+v", stored)
	}
}

func TestCaseRunnerClassifiesWithoutBlamingTheApplication(t *testing.T) {
	cases := []struct {
		name        string
		agentState  AgentRunState
		wantStatus  domain.CaseRunStatus
		wantClass   domain.FailureClass
		mutateSteps func(*schedWorld, int64)
	}{
		{
			name:       "environment",
			agentState: AgentRunState{Status: "failed", Error: "host_not_allowed"},
			wantStatus: domain.CaseRunStatusBlocked,
			wantClass:  domain.FailureClassEnvironment,
		},
		{
			name:       "session",
			agentState: AgentRunState{Status: "failed", Error: "session_unauthorized"},
			wantStatus: domain.CaseRunStatusBlocked,
			wantClass:  domain.FailureClassSession,
		},
		{
			name:       "model budget",
			agentState: AgentRunState{Status: "failed", Error: "budget_exhausted"},
			wantStatus: domain.CaseRunStatusInconclusive,
			wantClass:  domain.FailureClassModel,
		},
		{
			name:       "model internal",
			agentState: AgentRunState{Status: "failed", Error: "internal"},
			wantStatus: domain.CaseRunStatusInconclusive,
			wantClass:  domain.FailureClassModel,
		},
		{
			name:       "agent ended without finish",
			agentState: AgentRunState{Status: "done"},
			wantStatus: domain.CaseRunStatusInconclusive,
			wantClass:  domain.FailureClassCase,
		},
		{
			name:       "application failure without evidence",
			agentState: AgentRunState{Status: "failed", Error: "target_not_found"},
			wantStatus: domain.CaseRunStatusInconclusive,
			wantClass:  domain.FailureClassCase,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheduler, world := newSchedulerTest(t, 1)
			world.seedPending(1, "P0")
			world.runtime.setState(tc.agentState)

			if err := scheduler.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the case run to become terminal", func() bool {
				return isTerminalCaseRun(world.run(t, 1).Status)
			})
			scheduler.Wait()

			stored := world.run(t, 1)
			if stored.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", stored.Status, tc.wantStatus)
			}
			if stored.FailureClass == nil || *stored.FailureClass != tc.wantClass {
				t.Fatalf("failure class = %v, want %s", stored.FailureClass, tc.wantClass)
			}
			if tc.wantStatus == domain.CaseRunStatusFailed {
				t.Fatalf("classification must not report failed for %s", tc.name)
			}
		})
	}
}

func TestSchedulerStartStopsWithContext(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedRun(domain.CaseRun{
		ID: 10, BatchID: schedBatchID, VersionID: schedVersionID, CaseNodeID: 10,
		Attempt: 1, Snapshot: schedSnapshot(10, "P0"),
		Status: domain.CaseRunStatusPassed, CreatedAt: testNow, UpdatedAt: testNow,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Start(ctx)
	}()
	waitFor(t, "the scheduler to finish the batch", func() bool {
		return world.events.has("batch.finished")
	})
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Fakes: queue, repositories and ports
// ---------------------------------------------------------------------------

type schedQueue struct {
	mu      sync.Mutex
	batches []domain.RunBatch
	err     error
}

func (q *schedQueue) seed(batch domain.RunBatch) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.batches = append(q.batches, batch)
}

func (q *schedQueue) ActiveBatches(ctx context.Context) ([]domain.RunBatch, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return nil, q.err
	}
	return append([]domain.RunBatch(nil), q.batches...), nil
}

type schedBatchStatusCall struct {
	batchID    int64
	from, to   domain.BatchStatus
	finishedAt *time.Time
}

type schedBatchRepo struct {
	mu          sync.Mutex
	batches     map[int64]domain.RunBatch
	runs        *schedCaseRunRepo
	countsCalls int
	statusCalls []schedBatchStatusCall
	pauseCalls  int
	// afterCounts runs once after a recount committed; a test uses it to model a
	// concurrent writer landing between the recount and the batch-status CAS.
	afterCounts func()
}

func newSchedBatchRepo(runs *schedCaseRunRepo) *schedBatchRepo {
	return &schedBatchRepo{batches: map[int64]domain.RunBatch{}, runs: runs}
}

func (b *schedBatchRepo) seed(batch domain.RunBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches[batch.ID] = batch
}

func (b *schedBatchRepo) bumpCaseCount(batchID int64, delta int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.batches[batchID]
	batch.CaseCount += delta
	b.batches[batchID] = batch
}

func (b *schedBatchRepo) CreateBatchWithRuns(ctx context.Context, batch domain.RunBatch, runs []domain.CaseRun) (domain.RunBatch, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch.CaseCount = len(runs)
	b.batches[batch.ID] = batch
	return batch, nil
}

func (b *schedBatchRepo) GetBatch(ctx context.Context, versionID, batchID int64) (domain.RunBatch, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch, ok := b.batches[batchID]
	if !ok || batch.VersionID != versionID {
		return domain.RunBatch{}, ErrBatchNotFound
	}
	return batch, nil
}

func (b *schedBatchRepo) ListBatches(ctx context.Context, versionID int64, beforeID int64, limit int) ([]domain.RunBatch, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]domain.RunBatch, 0)
	for _, batch := range b.batches {
		if batch.VersionID == versionID && (beforeID == 0 || batch.ID < beforeID) {
			out = append(out, batch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, false, nil
}

func (b *schedBatchRepo) UpdateBatchStatus(ctx context.Context, batchID int64, from, to domain.BatchStatus, finishedAt *time.Time) (domain.RunBatch, error) {
	b.mu.Lock()
	b.statusCalls = append(b.statusCalls, schedBatchStatusCall{batchID, from, to, finishedAt})
	batch, ok := b.batches[batchID]
	if !ok {
		b.mu.Unlock()
		return domain.RunBatch{}, ErrBatchNotFound
	}
	if batch.Status != from {
		stored := batch.Status
		b.mu.Unlock()
		if isTerminalBatchStatus(stored) {
			return domain.RunBatch{}, domain.ErrBatchFinished
		}
		return domain.RunBatch{}, fmt.Errorf("%w: batch %d is %s, not %s", ErrConflict, batchID, stored, from)
	}
	batch.Status = to
	batch.FinishedAt = finishedAt
	b.batches[batchID] = batch
	b.mu.Unlock()
	return batch, nil
}

// isTerminalBatchStatus mirrors the repository contract for the fixture.
func isTerminalBatchStatus(status domain.BatchStatus) bool {
	return status == domain.BatchStatusDone || status == domain.BatchStatusCancelled ||
		status == domain.BatchStatusFailed
}

func (b *schedBatchRepo) SetPaused(ctx context.Context, batchID int64, paused bool, mode domain.PauseMode, reason string) (domain.RunBatch, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pauseCalls++
	batch, ok := b.batches[batchID]
	if !ok {
		return domain.RunBatch{}, ErrBatchNotFound
	}
	batch.Paused = paused
	batch.PauseMode = mode
	batch.PauseReason = reason
	b.batches[batchID] = batch
	return batch, nil
}

func (b *schedBatchRepo) UpdateCounts(ctx context.Context, batchID int64) (domain.BatchCounts, error) {
	b.mu.Lock()
	b.countsCalls++
	b.mu.Unlock()
	runs := b.runs.forBatch(batchID)
	counts := domain.Aggregate(runs)
	b.mu.Lock()
	batch := b.batches[batchID]
	batch.PassedCount = counts.PassedCount
	batch.FailedCount = counts.FailedCount
	batch.BlockedCount = counts.BlockedCount
	batch.InconclusiveCount = counts.InconclusiveCount
	batch.SkippedCount = counts.SkippedCount
	batch.InterruptedCount = counts.InterruptedCount
	b.batches[batchID] = batch
	hook := b.afterCounts
	b.afterCounts = nil
	b.mu.Unlock()
	if hook != nil {
		hook()
	}
	return counts, nil
}

func (b *schedBatchRepo) UpdateBatchFailure(ctx context.Context, batchID int64, reason string) (domain.RunBatch, error) {
	return b.batches[batchID], nil
}

// CountTerminalBatchesAwaitingAutonomy implements the P7 creation gate; the
// scheduler never creates a batch, so the fake reports a settled chain.
func (b *schedBatchRepo) CountTerminalBatchesAwaitingAutonomy(context.Context, int64) (int, error) {
	return 0, nil
}

// setPaused flips the pause flag directly (test helper, not the API surface).
func (b *schedBatchRepo) setPaused(batchID int64, paused bool, mode domain.PauseMode) {
	if _, err := b.SetPaused(context.Background(), batchID, paused, mode, "user"); err != nil {
		panic(err)
	}
}

type schedCaseRunRepo struct {
	mu   sync.Mutex
	runs map[int64]domain.CaseRun
	next int64
}

func newSchedCaseRunRepo() *schedCaseRunRepo {
	return &schedCaseRunRepo{runs: map[int64]domain.CaseRun{}}
}

func (r *schedCaseRunRepo) seed(run domain.CaseRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[run.ID] = run
	if run.ID > r.next {
		r.next = run.ID
	}
}

func (r *schedCaseRunRepo) forBatch(batchID int64) []domain.CaseRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.CaseRun, 0)
	for _, run := range r.runs {
		if run.BatchID == batchID {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *schedCaseRunRepo) CreateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.runs[run.ID]; exists {
		return domain.CaseRun{}, ErrConflict
	}
	r.runs[run.ID] = run
	if run.ID > r.next {
		r.next = run.ID
	}
	return run, nil
}

func (r *schedCaseRunRepo) GetCaseRun(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[caseRunID]
	if !ok || run.BatchID != batchID {
		return domain.CaseRun{}, ErrCaseRunNotFound
	}
	return run, nil
}

func (r *schedCaseRunRepo) GetCaseRunForUpdate(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
	return r.GetCaseRun(ctx, batchID, caseRunID)
}

// GetCaseRunByID implements CaseRunLocator for the recorder tests.
func (r *schedCaseRunRepo) GetCaseRunByID(ctx context.Context, caseRunID int64) (domain.CaseRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[caseRunID]
	if !ok {
		return domain.CaseRun{}, ErrCaseRunNotFound
	}
	return run, nil
}

func (r *schedCaseRunRepo) ListCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]domain.CaseRun, 0)
	for _, run := range r.runs {
		if run.BatchID != batchID {
			continue
		}
		if status != "" && run.Status != status {
			continue
		}
		if beforeID > 0 && run.ID >= beforeID {
			continue
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

func (r *schedCaseRunRepo) NextPending(ctx context.Context, batchID int64) (domain.CaseRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var next *domain.CaseRun
	for _, run := range r.runs {
		if run.BatchID != batchID || run.Status != domain.CaseRunStatusPending {
			continue
		}
		candidate := run
		if next == nil || pendingBefore(candidate, *next) {
			next = &candidate
		}
	}
	if next == nil {
		return domain.CaseRun{}, ErrNoPendingCaseRun
	}
	return *next, nil
}

// pendingBefore orders pending runs by snapshot priority then case_node_id and
// id, matching the repository contract.
func pendingBefore(left, right domain.CaseRun) bool {
	leftRank, rightRank := priorityRank(left.Snapshot.Node.Priority), priorityRank(right.Snapshot.Node.Priority)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	if left.CaseNodeID != right.CaseNodeID {
		return left.CaseNodeID < right.CaseNodeID
	}
	return left.ID < right.ID
}

func priorityRank(priority string) int {
	switch priority {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	default:
		return 4
	}
}

func (r *schedCaseRunRepo) UpdateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[run.ID]; !ok {
		return domain.CaseRun{}, ErrCaseRunNotFound
	}
	r.runs[run.ID] = run
	return run, nil
}

func (r *schedCaseRunRepo) SetStatus(ctx context.Context, caseRunID int64, from, to domain.CaseRunStatus, at time.Time) (domain.CaseRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[caseRunID]
	if !ok {
		return domain.CaseRun{}, ErrCaseRunNotFound
	}
	if run.Status != from {
		if isTerminalCaseRun(run.Status) {
			return domain.CaseRun{}, domain.ErrCaseRunTerminal
		}
		return domain.CaseRun{}, fmt.Errorf("%w: case run %d is %s, not %s", ErrConflict, caseRunID, run.Status, from)
	}
	run.Status = to
	run.UpdatedAt = at
	if to == domain.CaseRunStatusRunning && run.StartedAt == nil {
		run.StartedAt = timePointer(at)
	}
	if isTerminalCaseRun(to) && run.FinishedAt == nil {
		run.FinishedAt = timePointer(at)
	}
	r.runs[caseRunID] = run
	return run, nil
}

func (r *schedCaseRunRepo) FinishCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.runs[run.ID]
	if !ok {
		return domain.CaseRun{}, ErrCaseRunNotFound
	}
	// Finish is a running -> terminal CAS only. An interrupted run is finished
	// by recovery as interrupted and must never be overwritten with a verdict.
	if current.Status != domain.CaseRunStatusRunning {
		if isTerminalCaseRun(current.Status) {
			return domain.CaseRun{}, domain.ErrCaseRunTerminal
		}
		return domain.CaseRun{}, fmt.Errorf("%w: case run %d is %s and cannot be finished",
			ErrConflict, run.ID, current.Status)
	}
	r.runs[run.ID] = run
	return run, nil
}

// schedStepRepo is an in-memory StepRepository.
type schedStepRepo struct {
	mu    sync.Mutex
	steps map[int64][]domain.StepResult
	// onList runs once after a read committed; a test uses it to model a
	// concurrent pause landing while a submission is being validated.
	onList func()
}

func newSchedStepRepo() *schedStepRepo {
	return &schedStepRepo{steps: map[int64][]domain.StepResult{}}
}

func (r *schedStepRepo) seed(step domain.StepResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps[step.CaseRunID] = append(r.steps[step.CaseRunID], step)
}

func (r *schedStepRepo) Upsert(ctx context.Context, step domain.StepResult) (domain.StepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := step
	for i := range r.steps[step.CaseRunID] {
		if r.steps[step.CaseRunID][i].Seq == step.Seq {
			stored.ID = r.steps[step.CaseRunID][i].ID
			r.steps[step.CaseRunID][i] = stored
			return stored, nil
		}
	}
	r.steps[step.CaseRunID] = append(r.steps[step.CaseRunID], stored)
	return stored, nil
}

func (r *schedStepRepo) ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.StepResult, error) {
	r.mu.Lock()
	out := append([]domain.StepResult(nil), r.steps[caseRunID]...)
	hook := r.onList
	r.onList = nil
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if hook != nil {
		hook()
	}
	return out, nil
}

// schedAssertionRepo is an in-memory AssertionRepository.
type schedAssertionRepo struct {
	mu   sync.Mutex
	rows map[int64][]domain.AssertionResult
}

func newSchedAssertionRepo() *schedAssertionRepo {
	return &schedAssertionRepo{rows: map[int64][]domain.AssertionResult{}}
}

func (r *schedAssertionRepo) seed(result domain.AssertionResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[result.CaseRunID] = append(r.rows[result.CaseRunID], result)
}

func (r *schedAssertionRepo) Upsert(ctx context.Context, result domain.AssertionResult) (domain.AssertionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rows[result.CaseRunID] {
		if r.rows[result.CaseRunID][i].AssertionID == result.AssertionID {
			r.rows[result.CaseRunID][i] = result
			return result, nil
		}
	}
	r.rows[result.CaseRunID] = append(r.rows[result.CaseRunID], result)
	return result, nil
}

func (r *schedAssertionRepo) ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.AssertionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.AssertionResult(nil), r.rows[caseRunID]...), nil
}

// schedAutonomy is the scheduler's autonomous-chain hand-off fake.
type schedAutonomy struct {
	batches []domain.RunBatch
}

func (a *schedAutonomy) OnBatchTerminal(ctx context.Context, batch domain.RunBatch) {
	a.batches = append(a.batches, batch)
}

// schedVersionPort is an in-memory VersionPort.
type schedVersionPort struct {
	status        string
	paused        bool
	batchNo       int
	systemID      *int64
	environmentID *int64
	completeCalls int
}

func (v *schedVersionPort) GetVersion(ctx context.Context, versionID int64) (VersionRef, error) {
	return VersionRef{
		ID: versionID, Status: v.status, Paused: v.paused, BatchNo: v.batchNo,
		SystemID: v.systemID, EnvironmentID: v.environmentID,
	}, nil
}

func (v *schedVersionPort) MarkPaused(ctx context.Context, versionID int64, paused bool, mode, reason string, actor int64) error {
	v.paused = paused
	return nil
}

func (v *schedVersionPort) AllocateBatchNo(ctx context.Context, versionID int64) (int, error) {
	v.batchNo++
	return v.batchNo, nil
}

func (v *schedVersionPort) ReleaseBatchNo(ctx context.Context, versionID int64, allocated int) error {
	if v.batchNo == allocated {
		v.batchNo = allocated - 1
	}
	return nil
}

func (v *schedVersionPort) SetBatchNo(ctx context.Context, versionID int64, batchNo int, actor int64) error {
	v.batchNo = batchNo
	return nil
}

func (v *schedVersionPort) CompleteExecution(ctx context.Context, versionID, actor int64) error {
	v.completeCalls++
	return nil
}

// schedSessionPort is an in-memory SessionPort.
type schedSessionPort struct {
	mu             sync.Mutex
	acquireResult  SessionRef
	acquireErr     error
	acquireCalls   []string
	releases       []schedRelease
	takeovers      map[int64]*TakeoverRef
	environment    EnvironmentRef
	environmentErr error
	// onRelease runs after one release was recorded; a test uses it to inspect
	// the run state at the moment the lease is freed.
	onRelease func()
}

type schedRelease struct {
	sessionID int64
	holder    string
	reason    string
}

func newSchedSessionPort() *schedSessionPort {
	return &schedSessionPort{
		acquireResult: SessionRef{SessionID: 500, SnapshotID: 501},
		takeovers:     map[int64]*TakeoverRef{},
	}
}

func (s *schedSessionPort) Acquire(ctx context.Context, environmentID, systemID int64, holder string, ttl time.Duration) (SessionRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls = append(s.acquireCalls, holder)
	if s.acquireErr != nil {
		return SessionRef{}, s.acquireErr
	}
	return s.acquireResult, nil
}

func (s *schedSessionPort) Release(ctx context.Context, sessionID int64, holder, reason string) error {
	s.mu.Lock()
	s.releases = append(s.releases, schedRelease{sessionID, holder, reason})
	hook := s.onRelease
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (s *schedSessionPort) MarkInvalid(ctx context.Context, sessionID int64, reason string) error {
	return nil
}

func (s *schedSessionPort) PendingTakeover(ctx context.Context, sessionID int64) (*TakeoverRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	takeover, ok := s.takeovers[sessionID]
	if !ok {
		return nil, nil
	}
	copied := *takeover
	return &copied, nil
}

func (s *schedSessionPort) Environment(ctx context.Context, environmentID int64) (EnvironmentRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.environment, s.environmentErr
}

func (s *schedSessionPort) released(sessionID int64, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, release := range s.releases {
		if release.sessionID == sessionID && release.reason == reason {
			return true
		}
	}
	return false
}

// schedRuntime is an in-memory AgentRuntimePort.
type schedRuntime struct {
	mu          sync.Mutex
	startGate   chan struct{}
	started     []int64
	startCalls  []CaseRunRequest
	startErr    error
	state       AgentRunState
	getErr      error
	cancelCalls []int64
}

func newSchedRuntime() *schedRuntime {
	return &schedRuntime{state: AgentRunState{Status: "running"}}
}

func (r *schedRuntime) EnsureProfile(ctx context.Context) error { return nil }

func (r *schedRuntime) StartCaseRun(ctx context.Context, req CaseRunRequest) (AgentRunRef, error) {
	r.mu.Lock()
	gate := r.startGate
	startErr := r.startErr
	if startErr == nil {
		r.started = append(r.started, req.CaseRunID)
		r.startCalls = append(r.startCalls, req)
	}
	r.mu.Unlock()

	if startErr != nil {
		return AgentRunRef{}, startErr
	}
	// The start is recorded before the gate so a test can observe dispatch
	// order while the run is intentionally held open.
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return AgentRunRef{}, ctx.Err()
		}
	}
	return AgentRunRef{SessionID: 1000 + req.CaseRunID, RunID: 2000 + req.CaseRunID}, nil
}

func (r *schedRuntime) GetRun(ctx context.Context, agentSessionID int64) (AgentRunState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.getErr
}

func (r *schedRuntime) CancelRun(ctx context.Context, agentSessionID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelCalls = append(r.cancelCalls, agentSessionID)
	return nil
}

func (r *schedRuntime) ListSessions(ctx context.Context, versionID int64) ([]AgentSessionRef, error) {
	return nil, nil
}

func (r *schedRuntime) setState(state AgentRunState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = state
}

func (r *schedRuntime) setStartGate(gate chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startGate = gate
}

func (r *schedRuntime) releaseStartGate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startGate != nil {
		close(r.startGate)
		r.startGate = nil
	}
}

func (r *schedRuntime) startedIDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.started...)
}

func (r *schedRuntime) canceled() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.cancelCalls...)
}

// schedBrowser is an in-memory BrowserExecutor.
type schedBrowser struct {
	mu       sync.Mutex
	next     int
	specs    []ContextSpec
	closed   []ContextRef
	canceled []ContextRef
	newErr   error
	// failures is the per-case-run terminal signal the execution tools report
	// (tool-call budget exhaustion, executor timeout).
	failures map[int64]string
}

func newSchedBrowser() *schedBrowser { return &schedBrowser{} }

func (b *schedBrowser) Health(ctx context.Context) error { return nil }

func (b *schedBrowser) NewContext(ctx context.Context, spec ContextSpec) (ContextRef, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.newErr != nil {
		return ContextRef{}, b.newErr
	}
	b.next++
	b.specs = append(b.specs, spec)
	return ContextRef{ID: fmt.Sprintf("ctx-%d", b.next)}, nil
}

func (b *schedBrowser) Action(ctx context.Context, ref ContextRef, action Action) (ActionResult, error) {
	return ActionResult{}, nil
}

// CaseRunFailure implements CaseRunFailureSignaler.
func (b *schedBrowser) CaseRunFailure(caseRunID int64) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	code, ok := b.failures[caseRunID]
	return code, ok
}

// setFailure records the terminal signal of one case run.
func (b *schedBrowser) setFailure(caseRunID int64, code string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures == nil {
		b.failures = make(map[int64]string)
	}
	b.failures[caseRunID] = code
}

func (b *schedBrowser) Cancel(ctx context.Context, ref ContextRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.canceled = append(b.canceled, ref)
	return nil
}

func (b *schedBrowser) Close(ctx context.Context, ref ContextRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = append(b.closed, ref)
	return nil
}

func (b *schedBrowser) contextSpecs() []ContextSpec {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]ContextSpec(nil), b.specs...)
}

func (b *schedBrowser) closedIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.closed))
	for _, ref := range b.closed {
		out = append(out, ref.ID)
	}
	return out
}

func (b *schedBrowser) canceledIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.canceled))
	for _, ref := range b.canceled {
		out = append(out, ref.ID)
	}
	return out
}

// schedEvents is an in-memory EventPublisher.
type schedEvents struct {
	mu     sync.Mutex
	events []Event
}

func (e *schedEvents) Publish(ctx context.Context, event Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return nil
}

func (e *schedEvents) types() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.events))
	for _, event := range e.events {
		out = append(out, event.Type)
	}
	return out
}

func (e *schedEvents) has(eventType string) bool { return e.count(eventType) > 0 }

func (e *schedEvents) count(eventType string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	count := 0
	for _, event := range e.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func (e *schedEvents) firstOf(eventType string) *Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.events {
		if e.events[i].Type == eventType {
			copied := e.events[i]
			return &copied
		}
	}
	return nil
}

// schedMindmap is an in-memory MindmapPort that records MarkExecuted.
type schedMindmap struct {
	mu          sync.Mutex
	executedIDs []int64
	err         error
}

func (m *schedMindmap) Baseline(ctx context.Context, versionID int64) (BaselineRef, error) {
	return BaselineRef{}, nil
}

func (m *schedMindmap) SnapshotCases(ctx context.Context, versionID int64, caseIDs []int64) ([]CaseSnapshotSourceView, error) {
	return nil, nil
}

func (m *schedMindmap) MarkExecuted(ctx context.Context, versionID int64, caseNodeIDs []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.executedIDs = append(m.executedIDs, caseNodeIDs...)
	return nil
}

func (m *schedMindmap) executed() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int64(nil), m.executedIDs...)
}

// schedIDs is a thread-safe IDGenerator.
type schedIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *schedIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

// schedClock is a fixed Clock.
type schedClock struct {
	now time.Time
}

func (c *schedClock) Now() time.Time { return c.now }
