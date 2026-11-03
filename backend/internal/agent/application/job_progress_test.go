package agentapp

// Task 47 (P2-T7): the job.progress/job.finished producer. A 0.2.1 work item is
// the UI's "job" (job_id = work_item.id): the worker emits a coalesced start /
// state progress event and exactly one terminal job.finished, while the runner
// reports its completed model turns through the same per-job coalescer. The
// tests pin the contract constraints from contracts/events.md §18.2 #4:
// one progress event per coalescing window, a monotonic current, exactly one
// terminal event, total=0 meaning "unknown", and the version channel.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
)

// jobEventRecorder is a thread-safe eventingapp.Publisher double: the emitter
// publishes from timer goroutines, so every read takes the lock.
type jobEventRecorder struct {
	mu     sync.Mutex
	events []eventingapp.Event
}

func (r *jobEventRecorder) Publish(_ context.Context, event eventingapp.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *jobEventRecorder) snapshot() []eventingapp.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]eventingapp.Event(nil), r.events...)
}

func (r *jobEventRecorder) byType(eventType string) []eventingapp.Event {
	events := r.snapshot()
	matched := make([]eventingapp.Event, 0, len(events))
	for _, event := range events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

func (r *jobEventRecorder) count(eventType string) int {
	return len(r.byType(eventType))
}

func newJobTestEmitter(recorder *jobEventRecorder, window time.Duration) *JobProgressEmitter {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewJobProgressEmitter(recorder, logger, window)
}

func waitForJobEvents(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", message)
}

// TestJobProgressCoalesced pins the 250-500ms per-job coalescing window: a
// burst inside one window produces exactly one event carrying the newest
// snapshot, and a stale (lower) update can never move progress backwards.
func TestJobProgressCoalesced(t *testing.T) {
	recorder := &jobEventRecorder{}
	emitter := newJobTestEmitter(recorder, 300*time.Millisecond)

	progress := func(current int) eventingapp.JobProgress {
		return eventingapp.JobProgress{
			JobID:   77,
			Kind:    "execute.batch",
			Current: current,
			Total:   27,
			Status:  eventingapp.JobStatusRunning,
		}
	}
	for current := 1; current <= 10; current++ {
		emitter.Progress(42, progress(current))
	}
	// A stale update inside the same window must not be published nor drag the
	// coalesced snapshot backwards.
	emitter.Progress(42, progress(4))

	if got := recorder.count(eventingapp.EventJobProgress); got != 0 {
		t.Fatalf("coalescing window published %d job.progress events before it closed", got)
	}
	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobProgress) == 1
	}, "one coalesced job.progress event")

	first := recorder.byType(eventingapp.EventJobProgress)[0]
	if first.Channel != "version:42" {
		t.Fatalf("coalesced channel = %q, want version:42", first.Channel)
	}
	want := map[string]any{
		"job_id":  "77",
		"kind":    "execute.batch",
		"current": 10,
		"total":   27,
		"status":  "running",
	}
	for field, value := range want {
		if first.Payload[field] != value {
			t.Fatalf("coalesced payload[%q] = %#v, want %#v (payload %#v)",
				field, first.Payload[field], value, first.Payload)
		}
	}

	// The next update starts a new window rather than postponing the first one:
	// at most one event per window, not one event per update.
	emitter.Progress(42, progress(11))
	if got := recorder.count(eventingapp.EventJobProgress); got != 1 {
		t.Fatalf("next window published %d events before closing", got)
	}
	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobProgress) == 2
	}, "second coalesced job.progress event")
	if got := recorder.byType(eventingapp.EventJobProgress)[1].Payload["current"]; got != 11 {
		t.Fatalf("second coalesced current = %#v, want 11", got)
	}
}

// TestJobFinishedOnce pins the terminal contract: repeated terminal writes and
// progress after the terminal state yield exactly one job.finished.
func TestJobFinishedOnce(t *testing.T) {
	recorder := &jobEventRecorder{}
	emitter := newJobTestEmitter(recorder, 150*time.Millisecond)

	emitter.Progress(42, eventingapp.JobProgress{
		JobID: 77, Kind: "execute.batch", Current: 3, Total: 9, Status: eventingapp.JobStatusRunning,
	})
	terminal := eventingapp.JobFinished{
		JobID:     77,
		Kind:      "execute.batch",
		Status:    eventingapp.JobStatusDone,
		ResultRef: "agent_run:9001",
	}
	emitter.Finished(42, terminal)
	emitter.Finished(42, terminal)
	// A conflicting duplicate terminal write is dropped, not re-published.
	emitter.Finished(42, eventingapp.JobFinished{
		JobID: 77, Kind: "execute.batch", Status: eventingapp.JobStatusFailed, Error: "late duplicate",
	})
	// Progress after the terminal state must not reach the stream.
	emitter.Progress(42, eventingapp.JobProgress{
		JobID: 77, Kind: "execute.batch", Current: 4, Total: 9, Status: eventingapp.JobStatusRunning,
	})

	finished := recorder.byType(eventingapp.EventJobFinished)
	if len(finished) != 1 {
		t.Fatalf("job.finished count = %d, want exactly 1 (%#v)", len(finished), finished)
	}
	if finished[0].Channel != "version:42" {
		t.Fatalf("job.finished channel = %q, want version:42", finished[0].Channel)
	}
	want := map[string]any{
		"job_id":     "77",
		"kind":       "execute.batch",
		"status":     "done",
		"result_ref": "agent_run:9001",
	}
	for field, value := range want {
		if finished[0].Payload[field] != value {
			t.Fatalf("job.finished payload[%q] = %#v, want %#v (payload %#v)",
				field, finished[0].Payload[field], value, finished[0].Payload)
		}
	}
	// The pending progress snapshot is flushed once before the terminal event so
	// a job that finishes inside one window still reports its last known state.
	progress := recorder.byType(eventingapp.EventJobProgress)
	if len(progress) != 1 {
		t.Fatalf("job.progress count = %d, want the single pre-terminal snapshot", len(progress))
	}
	events := recorder.snapshot()
	if len(events) != 2 || events[0].Type != eventingapp.EventJobProgress ||
		events[1].Type != eventingapp.EventJobFinished {
		t.Fatalf("event order = %#v, want job.progress then job.finished", events)
	}
	// Wait out another window: no duplicate terminal event may be published.
	time.Sleep(2 * 150 * time.Millisecond)
	if got := recorder.count(eventingapp.EventJobFinished); got != 1 {
		t.Fatalf("job.finished count after a second window = %d, want 1", got)
	}
	if got := recorder.count(eventingapp.EventJobProgress); got != 1 {
		t.Fatalf("job.progress count after a second window = %d, want 1", got)
	}
}

// TestJobProgressVersionScope pins the version channel/scope and prevents a job
// from leaking onto another version's stream.
func TestJobProgressVersionScope(t *testing.T) {
	recorder := &jobEventRecorder{}
	emitter := newJobTestEmitter(recorder, 120*time.Millisecond)

	emitter.Progress(42, eventingapp.JobProgress{
		JobID: 701, Kind: "design.cases", Current: 1, Total: 0, Status: eventingapp.JobStatusRunning,
	})
	emitter.Progress(43, eventingapp.JobProgress{
		JobID: 702, Kind: "execute.batch", Current: 2, Total: 5, Status: eventingapp.JobStatusRunning,
	})
	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobProgress) == 2
	}, "two version-scoped job.progress events")

	byJob := map[string]eventingapp.Event{}
	for _, event := range recorder.byType(eventingapp.EventJobProgress) {
		jobID, _ := event.Payload["job_id"].(string)
		byJob[jobID] = event
	}
	first, ok := byJob["701"]
	if !ok {
		t.Fatalf("missing job 701 event: %#v", byJob)
	}
	if first.Channel != "version:42" || first.Scope["version_id"] != "42" || first.Scope["job_id"] != "701" {
		t.Fatalf("job 701 event scope = channel %q scope %#v", first.Channel, first.Scope)
	}
	second, ok := byJob["702"]
	if !ok {
		t.Fatalf("missing job 702 event: %#v", byJob)
	}
	if second.Channel != "version:43" || second.Scope["version_id"] != "43" || second.Scope["job_id"] != "702" {
		t.Fatalf("job 702 event scope = channel %q scope %#v", second.Channel, second.Scope)
	}

	// A job already seen on one version is dropped when a caller claims another
	// version: a producer bug must not move the card to a foreign stream.
	emitter.Progress(43, eventingapp.JobProgress{
		JobID: 701, Kind: "design.cases", Current: 9, Total: 0, Status: eventingapp.JobStatusRunning,
	})
	time.Sleep(3 * 120 * time.Millisecond)
	for _, event := range recorder.byType(eventingapp.EventJobProgress) {
		if event.Payload["job_id"] == "701" && event.Channel == "version:43" {
			t.Fatalf("job 701 leaked onto version 43: %#v", event)
		}
	}
	if got := recorder.count(eventingapp.EventJobProgress); got != 2 {
		t.Fatalf("job.progress count after the foreign-version update = %d, want 2", got)
	}
}

// TestJobProgressRunnerTurn pins the runner side of the producer: each
// completed model turn advances the job's "processed steps" count with total=0
// (unknown), through the worker emitter carried on the run context.
func TestJobProgressRunnerTurn(t *testing.T) {
	recorder := &jobEventRecorder{}
	emitter := newJobTestEmitter(recorder, 50*time.Millisecond)
	runner := &Runner{}
	state := &runState{
		item:  agent.WorkItem{ID: 88, VersionID: 42, Kind: "design.cases"},
		turns: 3,
	}

	// Without the worker emitter the runner stays a no-op (P1-P8 call paths and
	// unit tests that build the runner directly keep working).
	runner.reportJobProgress(context.Background(), state)
	if got := recorder.count(eventingapp.EventJobProgress); got != 0 {
		t.Fatalf("runner without an emitter published %d events", got)
	}

	runner.reportJobProgress(withJobEvents(context.Background(), emitter), state)
	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobProgress) == 1
	}, "runner turn job.progress event")

	event := recorder.byType(eventingapp.EventJobProgress)[0]
	if event.Channel != "version:42" {
		t.Fatalf("runner event channel = %q, want version:42", event.Channel)
	}
	want := map[string]any{
		"job_id":  "88",
		"kind":    "design.cases",
		"current": 3,
		"total":   0,
		"status":  "running",
	}
	for field, value := range want {
		if event.Payload[field] != value {
			t.Fatalf("runner payload[%q] = %#v, want %#v (payload %#v)",
				field, event.Payload[field], value, event.Payload)
		}
	}
}

// jobTestRunner is a RunExecutor double. run, when set, mimics the real
// runner's use of the job event emitter carried on the run context.
type jobTestRunner struct {
	result RunResult
	err    error
	run    func(ctx context.Context, item agent.WorkItem) (RunResult, error)
}

func (r *jobTestRunner) Run(ctx context.Context, item agent.WorkItem) (RunResult, error) {
	if r.run != nil {
		return r.run(ctx, item)
	}
	return r.result, r.err
}

// jobTestQueue embeds WorkerQueue so only the lifecycle methods this producer
// touches need an implementation. The embedded nil interface panics on any
// other call, which keeps the double honest for these tests. Fail mirrors the
// repository predicate (`retryable AND attempts < max_attempts` reopens, else
// dead) instead of a caller-supplied verdict, so the double cannot disagree
// with the persisted outcome the worker must report.
type jobTestQueue struct {
	WorkerQueue

	mu           sync.Mutex
	item         agent.WorkItem
	claimed      bool
	completed    int
	failed       int
	completeErr  error
	heartbeats   int
	heartbeatErr error

	// itemsNewer models the repository's newest-page bound: the version holds
	// this many newer work items, so ListByVersion's page (limit rows,
	// ORDER BY id DESC) never contains the older failing item. A real version
	// can hold more than the 100-row page the worker used to read back.
	itemsNewer int
}

func (q *jobTestQueue) ClaimForTeams(context.Context, string, []agent.Team) (agent.WorkItem, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimed {
		return agent.WorkItem{}, false, nil
	}
	q.claimed = true
	return q.item, true, nil
}

func (q *jobTestQueue) MarkRunning(context.Context, int64, int64) error { return nil }

func (q *jobTestQueue) Heartbeat(context.Context, int64, int64, time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.heartbeats++
	return q.heartbeatErr
}

func (q *jobTestQueue) Complete(context.Context, int64, int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completed++
	return q.completeErr
}

func (q *jobTestQueue) Fail(_ context.Context, _ int64, _ int64, _ error, retryable bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed++
	// The repository's Fail SQL: `CASE WHEN retryable AND attempts <
	// max_attempts THEN 'open' ELSE 'dead' END`.
	if retryable && q.item.Attempts < q.item.MaxAttempts {
		q.item.Status = agent.WorkItemOpen
	} else {
		q.item.Status = agent.WorkItemDead
	}
	return nil
}

func (q *jobTestQueue) RecoverExpired(context.Context) (int64, error) { return 0, nil }

// ListByVersion returns one newest-first page. The synthetic items standing in
// for the version's newer work items fill the page first, exactly like the
// repository's `ORDER BY id DESC LIMIT n`; the queue's older item only appears
// when the page still has room.
func (q *jobTestQueue) ListByVersion(_ context.Context, versionID int64, limit int) ([]agent.WorkItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	items := make([]agent.WorkItem, 0, limit)
	for n := 0; n < q.itemsNewer && len(items) < limit; n++ {
		items = append(items, agent.WorkItem{
			ID:        q.item.ID + int64(q.itemsNewer-n),
			VersionID: versionID,
			Status:    agent.WorkItemDone,
		})
	}
	if q.item.VersionID == versionID && len(items) < limit {
		items = append(items, q.item)
	}
	return items, nil
}

func (q *jobTestQueue) Lease() time.Duration { return time.Minute }

func newJobTestWorker(queue WorkerQueue, runner RunExecutor, recorder *jobEventRecorder, options ...WorkerOption) *Worker {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := []WorkerOption{
		WithWorkerEvents(recorder),
		WithWorkerJobProgressWindow(50 * time.Millisecond),
		WithWorkerPollInterval(2 * time.Millisecond),
	}
	return NewWorker(queue, runner, nil, logger, "instance-1", append(base, options...)...)
}

func runJobTestWorker(t *testing.T, worker *Worker) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- worker.Run(ctx) }()
	return cancelFn, doneCh
}

func stopJobTestWorker(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

// TestJobProgressWorkerLifecycle pins the worker wiring: the claimed/running
// start snapshot reaches the version stream and the completed run produces one
// terminal job.finished carrying the work item id/kind.
func TestJobProgressWorkerLifecycle(t *testing.T) {
	item := agent.WorkItem{
		ID: 707, VersionID: 55, Team: agent.TeamDesign,
		Type: "design.cases", Kind: "design.cases",
		Status: agent.WorkItemRunning, LeaseEpoch: 4, Attempts: 1, MaxAttempts: 3,
	}
	queue := &jobTestQueue{item: item}
	runner := &jobTestRunner{run: func(ctx context.Context, item agent.WorkItem) (RunResult, error) {
		// Mimic the real runner: report one completed turn through the worker's
		// context emitter, then finish the run done.
		if emitter := jobEventsFromContext(ctx); emitter != nil {
			emitter.Progress(item.VersionID, eventingapp.JobProgress{
				JobID: item.ID, Kind: jobKind(item), Current: 2, Total: 0, Status: eventingapp.JobStatusRunning,
			})
		}
		return RunResult{AgentRunID: 9001, Status: agent.AgentRunDone}, nil
	}}
	recorder := &jobEventRecorder{}
	worker := newJobTestWorker(queue, runner, recorder)

	cancel, done := runJobTestWorker(t, worker)
	defer cancel()

	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobFinished) == 1
	}, "worker job.finished event")
	// Wait out another coalescing window: the terminal event must not duplicate.
	time.Sleep(120 * time.Millisecond)
	stopJobTestWorker(t, cancel, done)

	queue.mu.Lock()
	completed := queue.completed
	queue.mu.Unlock()
	if completed != 1 {
		t.Fatalf("completed work items = %d, want 1", completed)
	}

	finished := recorder.byType(eventingapp.EventJobFinished)
	if len(finished) != 1 {
		t.Fatalf("job.finished count = %d, want 1", len(finished))
	}
	if finished[0].Channel != "version:55" || finished[0].Scope["version_id"] != "55" ||
		finished[0].Scope["job_id"] != "707" {
		t.Fatalf("job.finished scope = channel %q %#v", finished[0].Channel, finished[0].Scope)
	}
	wantFinished := map[string]any{
		"job_id":     "707",
		"kind":       "design.cases",
		"status":     "done",
		"result_ref": "agent_run:9001",
	}
	for field, value := range wantFinished {
		if finished[0].Payload[field] != value {
			t.Fatalf("job.finished payload[%q] = %#v, want %#v (payload %#v)",
				field, finished[0].Payload[field], value, finished[0].Payload)
		}
	}

	progress := recorder.byType(eventingapp.EventJobProgress)
	if len(progress) != 1 {
		t.Fatalf("job.progress count = %d, want the run's turn snapshot", len(progress))
	}
	wantProgress := map[string]any{
		"job_id":  "707",
		"kind":    "design.cases",
		"current": 2,
		"total":   0,
		"status":  "running",
	}
	for field, value := range wantProgress {
		if progress[0].Payload[field] != value {
			t.Fatalf("job.progress payload[%q] = %#v, want %#v (payload %#v)",
				field, progress[0].Payload[field], value, progress[0].Payload)
		}
	}
	if progress[0].Channel != "version:55" {
		t.Fatalf("job.progress channel = %q, want version:55", progress[0].Channel)
	}
}

// TestJobProgressTotalClamped pins the denominator's monotonicity: once a job
// reports a known total, a later update carrying a smaller positive total (a
// retried run's re-estimate) must not shrink it, and total=0 stays "unknown"
// rather than replacing a known denominator.
func TestJobProgressTotalClamped(t *testing.T) {
	cases := []struct {
		name  string
		total int
	}{
		{"smaller positive total", 10},
		{"unknown zero total", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &jobEventRecorder{}
			emitter := newJobTestEmitter(recorder, 50*time.Millisecond)

			emitter.Progress(42, eventingapp.JobProgress{
				JobID: 99, Kind: "execute.batch", Current: 1, Total: 27, Status: eventingapp.JobStatusRunning,
			})
			waitForJobEvents(t, 2*time.Second, func() bool {
				return recorder.count(eventingapp.EventJobProgress) == 1
			}, "first job.progress with a known total")

			emitter.Progress(42, eventingapp.JobProgress{
				JobID: 99, Kind: "execute.batch", Current: 2, Total: tc.total, Status: eventingapp.JobStatusRunning,
			})
			waitForJobEvents(t, 2*time.Second, func() bool {
				return recorder.count(eventingapp.EventJobProgress) == 2
			}, "job.progress after a smaller/unknown total")

			events := recorder.byType(eventingapp.EventJobProgress)
			if got := events[1].Payload["total"]; got != 27 {
				t.Fatalf("total after a later update (total=%d) = %#v, want the monotonic max 27", tc.total, got)
			}
		})
	}
}

// TestJobTerminalFailurePredicate pins the predicate itself against the
// repository's Fail SQL (`retryable AND attempts < max_attempts` reopens, every
// other combination is dead), including the exhausted-retryable case a
// page-bounded read-back used to miss.
func TestJobTerminalFailurePredicate(t *testing.T) {
	cases := []struct {
		name      string
		attempts  int
		max       int
		retryable bool
		want      bool
	}{
		{"non-retryable below cap", 1, 3, false, true},
		{"non-retryable at cap", 3, 3, false, true},
		{"retryable below cap", 1, 3, true, false},
		{"retryable at cap", 3, 3, true, true},
		{"retryable over cap", 4, 3, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := agent.WorkItem{Attempts: tc.attempts, MaxAttempts: tc.max}
			if got := isTerminalFailure(item, tc.retryable); got != tc.want {
				t.Fatalf("isTerminalFailure(attempts=%d, max=%d, retryable=%v) = %v, want %v",
					tc.attempts, tc.max, tc.retryable, got, tc.want)
			}
		})
	}
}

// TestJobFinishedTerminalFailure pins the failed terminal state: a work item
// the queue marks dead (attempts exhausted) produces exactly one job.finished
// with status failed and the work item's last error.
func TestJobFinishedTerminalFailure(t *testing.T) {
	item := agent.WorkItem{
		ID: 808, VersionID: 56, Team: agent.TeamExecution,
		Type: "execute.batch", Kind: "execute.batch",
		Status: agent.WorkItemRunning, LeaseEpoch: 7, Attempts: 2, MaxAttempts: 3,
	}
	queue := &jobTestQueue{item: item}
	runner := &jobTestRunner{
		result: RunResult{AgentRunID: 9002, Status: agent.AgentRunFailed},
		err:    &RunError{Reason: "model_error", Retryable: false, Cause: errors.New("model down")},
	}
	recorder := &jobEventRecorder{}
	worker := newJobTestWorker(queue, runner, recorder)

	cancel, done := runJobTestWorker(t, worker)
	defer stopJobTestWorker(t, cancel, done)

	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobFinished) == 1
	}, "terminal job.finished event")
	time.Sleep(120 * time.Millisecond)

	queue.mu.Lock()
	failed := queue.failed
	queue.mu.Unlock()
	if failed != 1 {
		t.Fatalf("failed work items = %d, want 1", failed)
	}

	finished := recorder.byType(eventingapp.EventJobFinished)
	if len(finished) != 1 {
		t.Fatalf("job.finished count = %d, want 1", len(finished))
	}
	want := map[string]any{
		"job_id":  "808",
		"kind":    "execute.batch",
		"status":  "failed",
		"attempt": 2,
	}
	for field, value := range want {
		if finished[0].Payload[field] != value {
			t.Fatalf("job.finished payload[%q] = %#v, want %#v (payload %#v)",
				field, finished[0].Payload[field], value, finished[0].Payload)
		}
	}
	if finished[0].Payload["result_ref"] != "agent_run:9002" {
		t.Fatalf("job.finished result_ref = %#v", finished[0].Payload["result_ref"])
	}
	if errorText, _ := finished[0].Payload["error"].(string); errorText == "" {
		t.Fatalf("job.finished has no error message: %#v", finished[0].Payload)
	}
}

// TestJobFinishedTerminalFailureOutsideListPage pins the fix for the
// page-bounded terminal classification: a version can hold more work items than
// the newest ListByVersion page, so an older failing item must still produce the
// one terminal job.finished. The outcome comes from the repository's own Fail
// predicate (`retryable AND attempts < max_attempts` reopens, otherwise dead)
// for the claimed row, not from a page read-back that can omit it.
func TestJobFinishedTerminalFailureOutsideListPage(t *testing.T) {
	item := agent.WorkItem{
		ID: 909, VersionID: 57, Team: agent.TeamExecution,
		Type: "execute.batch", Kind: "execute.batch",
		Status: agent.WorkItemRunning, LeaseEpoch: 9, Attempts: 3, MaxAttempts: 3,
	}
	// The version has 150 newer items: a 100-row newest-first page never
	// contains item 909.
	queue := &jobTestQueue{item: item, itemsNewer: 150}
	runner := &jobTestRunner{
		result: RunResult{AgentRunID: 9003, Status: agent.AgentRunFailed},
		// Retryable, but the attempt budget is exhausted: the repository still
		// marks the row dead, so the job is terminal.
		err: &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("model down")},
	}
	recorder := &jobEventRecorder{}
	worker := newJobTestWorker(queue, runner, recorder)

	cancel, done := runJobTestWorker(t, worker)
	defer stopJobTestWorker(t, cancel, done)

	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobFinished) == 1
	}, "terminal job.finished for a failing item older than the newest page")
	time.Sleep(120 * time.Millisecond)

	queue.mu.Lock()
	failed := queue.failed
	queue.mu.Unlock()
	if failed != 1 {
		t.Fatalf("failed work items = %d, want 1", failed)
	}

	finished := recorder.byType(eventingapp.EventJobFinished)
	if len(finished) != 1 {
		t.Fatalf("job.finished count = %d, want exactly 1", len(finished))
	}
	want := map[string]any{
		"job_id":  "909",
		"kind":    "execute.batch",
		"status":  "failed",
		"attempt": 3,
	}
	for field, value := range want {
		if finished[0].Payload[field] != value {
			t.Fatalf("job.finished payload[%q] = %#v, want %#v (payload %#v)",
				field, finished[0].Payload[field], value, finished[0].Payload)
		}
	}
	if finished[0].Payload["result_ref"] != "agent_run:9003" {
		t.Fatalf("job.finished result_ref = %#v", finished[0].Payload["result_ref"])
	}
	// The work_item.dead projection must follow the same persisted outcome.
	if got := recorder.count("work_item.dead"); got != 1 {
		t.Fatalf("work_item.dead count = %d, want 1", got)
	}
	if finished[0].Channel != "version:57" {
		t.Fatalf("job.finished channel = %q, want version:57", finished[0].Channel)
	}
}

// TestJobFinishedRetryableFailureNotTerminal pins the other side of the
// predicate: a retryable failure below the attempt cap leaves the row open (the
// job will be retried), so it must not emit a terminal event; the progress
// update reflects the persisted open state, not a terminal failed.
func TestJobFinishedRetryableFailureNotTerminal(t *testing.T) {
	item := agent.WorkItem{
		ID: 811, VersionID: 58, Team: agent.TeamExecution,
		Type: "execute.batch", Kind: "execute.batch",
		Status: agent.WorkItemRunning, LeaseEpoch: 2, Attempts: 1, MaxAttempts: 3,
	}
	queue := &jobTestQueue{item: item}
	runner := &jobTestRunner{
		result: RunResult{Status: agent.AgentRunFailed},
		err:    &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("model busy")},
	}
	recorder := &jobEventRecorder{}
	worker := newJobTestWorker(queue, runner, recorder)

	cancel, done := runJobTestWorker(t, worker)
	defer stopJobTestWorker(t, cancel, done)

	waitForJobEvents(t, 2*time.Second, func() bool {
		for _, event := range recorder.byType(eventingapp.EventJobProgress) {
			if event.Payload["status"] == eventingapp.JobStatusOpen {
				return true
			}
		}
		return false
	}, "retryable failure job.progress with the persisted open status")
	time.Sleep(120 * time.Millisecond)

	if got := recorder.count(eventingapp.EventJobFinished); got != 0 {
		t.Fatalf("job.finished count = %d, want 0 for a retryable failure", got)
	}
	progress := recorder.byType(eventingapp.EventJobProgress)
	last := progress[len(progress)-1]
	if last.Payload["job_id"] != "811" {
		t.Fatalf("job.progress job_id = %#v, want 811", last.Payload["job_id"])
	}
	if message, _ := last.Payload["message"].(string); message == "" {
		t.Fatalf("retryable job.progress has no message: %#v", last.Payload)
	}
}

// TestJobProgressStartOnly pins the start event on its own: a run that reports
// no model turns still publishes exactly one running snapshot (current=0,
// total=0 meaning unknown) before the terminal done event.
func TestJobProgressStartOnly(t *testing.T) {
	item := agent.WorkItem{
		ID: 812, VersionID: 59, Team: agent.TeamClarification,
		Type: "chat.turn", Kind: "chat.turn",
		Status: agent.WorkItemRunning, LeaseEpoch: 3, Attempts: 1, MaxAttempts: 3,
	}
	queue := &jobTestQueue{item: item}
	runner := &jobTestRunner{result: RunResult{AgentRunID: 9005, Status: agent.AgentRunDone}}
	recorder := &jobEventRecorder{}
	worker := newJobTestWorker(queue, runner, recorder)

	cancel, done := runJobTestWorker(t, worker)
	defer stopJobTestWorker(t, cancel, done)

	waitForJobEvents(t, 2*time.Second, func() bool {
		return recorder.count(eventingapp.EventJobFinished) == 1
	}, "terminal job.finished without turn progress")
	time.Sleep(120 * time.Millisecond)

	progress := recorder.byType(eventingapp.EventJobProgress)
	if len(progress) != 1 {
		t.Fatalf("job.progress count = %d, want the one start snapshot", len(progress))
	}
	want := map[string]any{
		"job_id":  "812",
		"kind":    "chat.turn",
		"current": 0,
		"total":   0,
		"status":  "running",
	}
	for field, value := range want {
		if progress[0].Payload[field] != value {
			t.Fatalf("start job.progress payload[%q] = %#v, want %#v (payload %#v)",
				field, progress[0].Payload[field], value, progress[0].Payload)
		}
	}
	if got := recorder.count(eventingapp.EventJobFinished); got != 1 {
		t.Fatalf("job.finished count = %d, want 1", got)
	}
}

// TestJobFinishedSuppressedOnCompleteFailure pins that the DB stays the truth:
// when the terminal write-back fails, the worker has no persisted terminal state
// to report and must not publish job.finished.
func TestJobFinishedSuppressedOnCompleteFailure(t *testing.T) {
	item := agent.WorkItem{
		ID: 813, VersionID: 60, Team: agent.TeamDesign,
		Type: "design.cases", Kind: "design.cases",
		Status: agent.WorkItemRunning, LeaseEpoch: 4, Attempts: 1, MaxAttempts: 3,
	}
	queue := &jobTestQueue{item: item, completeErr: errors.New("write-back down")}
	runner := &jobTestRunner{result: RunResult{AgentRunID: 9006, Status: agent.AgentRunDone}}
	recorder := &jobEventRecorder{}
	worker := newJobTestWorker(queue, runner, recorder)

	cancel, done := runJobTestWorker(t, worker)
	defer stopJobTestWorker(t, cancel, done)

	waitForJobEvents(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return queue.completed == 1
	}, "attempted completion write-back")
	time.Sleep(120 * time.Millisecond)

	if got := recorder.count(eventingapp.EventJobFinished); got != 0 {
		t.Fatalf("job.finished count = %d, want 0 after a failed write-back", got)
	}
	queue.mu.Lock()
	failed := queue.failed
	queue.mu.Unlock()
	if failed != 0 {
		t.Fatalf("fails = %d, want 0", failed)
	}
}

// TestJobProgressLeaseLossSuppressed pins epoch fencing for the event stream:
// once the heartbeat reports lease_lost, the old owner owns no result and must
// publish no terminal event (the new owner owns the job's terminal event).
func TestJobProgressLeaseLossSuppressed(t *testing.T) {
	item := agent.WorkItem{
		ID: 814, VersionID: 61, Team: agent.TeamExecution,
		Type: "execute.batch", Kind: "execute.batch",
		Status: agent.WorkItemRunning, LeaseEpoch: 5, Attempts: 2, MaxAttempts: 3,
	}
	queue := &jobTestQueue{item: item, heartbeatErr: agent.ErrLeaseLost}
	runner := &jobTestRunner{run: func(ctx context.Context, _ agent.WorkItem) (RunResult, error) {
		<-ctx.Done()
		return RunResult{Status: agent.AgentRunInterrupted}, &RunError{
			Reason: "aborted", Retryable: false, Cause: ctx.Err(),
		}
	}}
	recorder := &jobEventRecorder{}
	worker := newJobTestWorker(queue, runner, recorder,
		WithWorkerHeartbeatInterval(5*time.Millisecond))

	cancel, done := runJobTestWorker(t, worker)
	defer stopJobTestWorker(t, cancel, done)

	waitForJobEvents(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return queue.heartbeats > 0
	}, "lease-lost heartbeat")
	time.Sleep(120 * time.Millisecond)

	if got := recorder.count(eventingapp.EventJobFinished); got != 0 {
		t.Fatalf("job.finished count = %d, want 0 after lease loss", got)
	}
	queue.mu.Lock()
	completed, failed := queue.completed, queue.failed
	queue.mu.Unlock()
	if completed != 0 {
		t.Fatalf("completed work items = %d after lease loss, want 0", completed)
	}
	if failed != 0 {
		t.Fatalf("failed work items = %d after lease loss, want 0", failed)
	}
}
