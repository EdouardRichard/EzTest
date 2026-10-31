package agentapp_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeFailRecord struct {
	id        int64
	epoch     int64
	retryable bool
}

type fakeWorkerQueue struct {
	mu       sync.Mutex
	items    []agent.WorkItem
	claimErr error

	recoverCalls  int
	claimCalls    int
	markedRunning []int64
	heartbeats    int
	heartbeatErr  error
	completes     []int64
	fails         []fakeFailRecord

	claimed   chan int64
	completed chan int64
	failed    chan fakeFailRecord
}

func newFakeWorkerQueue(items ...agent.WorkItem) *fakeWorkerQueue {
	return &fakeWorkerQueue{
		items:     items,
		claimed:   make(chan int64, 16),
		completed: make(chan int64, 16),
		failed:    make(chan fakeFailRecord, 16),
	}
}

func (q *fakeWorkerQueue) Claim(_ context.Context, _ string) (agent.WorkItem, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimErr != nil {
		return agent.WorkItem{}, false, q.claimErr
	}
	if len(q.items) == 0 {
		return agent.WorkItem{}, false, nil
	}
	item := q.items[0]
	q.items = q.items[1:]
	q.claimCalls++
	select {
	case q.claimed <- item.ID:
	default:
	}
	return item, true, nil
}

func (q *fakeWorkerQueue) MarkRunning(_ context.Context, id, _ int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.markedRunning = append(q.markedRunning, id)
	return nil
}

func (q *fakeWorkerQueue) Heartbeat(_ context.Context, _, _ int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.heartbeats++
	return q.heartbeatErr
}

func (q *fakeWorkerQueue) Complete(_ context.Context, id, _ int64) error {
	q.mu.Lock()
	q.completes = append(q.completes, id)
	q.mu.Unlock()
	select {
	case q.completed <- id:
	default:
	}
	return nil
}

func (q *fakeWorkerQueue) Fail(_ context.Context, id, epoch int64, _ error, retryable bool) error {
	record := fakeFailRecord{id: id, epoch: epoch, retryable: retryable}
	q.mu.Lock()
	q.fails = append(q.fails, record)
	q.mu.Unlock()
	select {
	case q.failed <- record:
	default:
	}
	return nil
}

func (q *fakeWorkerQueue) RecoverExpired(context.Context) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.recoverCalls++
	return 0, nil
}

func (q *fakeWorkerQueue) ListByVersion(_ context.Context, versionID int64, _ int) ([]agent.WorkItem, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	list := make([]agent.WorkItem, 0)
	for _, item := range q.items {
		if item.VersionID == versionID {
			list = append(list, item)
		}
	}
	return list, nil
}

func (q *fakeWorkerQueue) Lease() time.Duration { return 30 * time.Second }

func (q *fakeWorkerQueue) counts() (claims, completes, fails int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.claimCalls, len(q.completes), len(q.fails)
}

type fakeRunExecutor struct {
	mu        sync.Mutex
	calls     []agent.WorkItem
	result    agentapp.RunResult
	err       error
	run       func(ctx context.Context, item agent.WorkItem) (agentapp.RunResult, error)
	active    int
	maxActive int
	started   chan int64
	release   chan struct{}
}

func (f *fakeRunExecutor) Run(ctx context.Context, item agent.WorkItem) (agentapp.RunResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, item)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.started != nil {
		select {
		case f.started <- item.ID:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
		}
	}
	if f.run != nil {
		return f.run(ctx, item)
	}
	return f.result, f.err
}

func newTestWorker(queue agentapp.WorkerQueue, runner agentapp.RunExecutor, settings *fakeSettings, options ...agentapp.WorkerOption) *agentapp.Worker {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return agentapp.NewWorker(queue, runner, settings, logger, "instance-1", options...)
}

func runWorker(t *testing.T, worker *agentapp.Worker) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- worker.Run(ctx) }()
	return cancelFn, doneCh
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool, message string) {
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

func stopWorker(t *testing.T, cancel context.CancelFunc, done <-chan error) {
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

func TestWorkerRecoversExpiredLeasesPeriodically(t *testing.T) {
	queue := newFakeWorkerQueue()
	executor := &fakeRunExecutor{}
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerRecoveryInterval(3*time.Millisecond),
	)

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return queue.recoverCalls >= 2
	}, "periodic lease recovery")
	stopWorker(t, cancel, done)
}

func TestWorkerClaimsRunsAndCompletes(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 20, MessageID: 501})
	queue := newFakeWorkerQueue(item)
	executor := &fakeRunExecutor{result: agentapp.RunResult{AgentRunID: 77, Status: agent.AgentRunDone}}
	worker := newTestWorker(queue, executor, &fakeSettings{}, agentapp.WithWorkerPollInterval(2*time.Millisecond))

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		c, _, _ := queue.counts()
		return c >= 1 && len(queue.completes) == 1
	}, "work item completion")
	stopWorker(t, cancel, done)

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.markedRunning) != 1 || queue.markedRunning[0] != item.ID {
		t.Fatalf("mark running = %v", queue.markedRunning)
	}
	if queue.completes[0] != item.ID {
		t.Fatalf("completed = %v", queue.completes)
	}
	if len(queue.fails) != 0 {
		t.Fatalf("fails = %#v", queue.fails)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("runner calls = %d", len(executor.calls))
	}
}

func TestWorkerFailsRetryableRunnerError(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 20})
	queue := newFakeWorkerQueue(item)
	executor := &fakeRunExecutor{
		result: agentapp.RunResult{Status: agent.AgentRunFailed},
		err:    &agentapp.RunError{Reason: "model_error", Retryable: true, Cause: errors.New("model down")},
	}
	worker := newTestWorker(queue, executor, &fakeSettings{}, agentapp.WithWorkerPollInterval(2*time.Millisecond))

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.fails) == 1
	}, "work item failure")
	stopWorker(t, cancel, done)

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completes) != 0 {
		t.Fatalf("completed = %v", queue.completes)
	}
	record := queue.fails[0]
	if record.id != item.ID || !record.retryable {
		t.Fatalf("fail record = %#v", record)
	}
}

func TestWorkerHeartbeatLeaseLostDoesNotOverwriteResult(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 20})
	queue := newFakeWorkerQueue(item)
	queue.heartbeatErr = platform.NewError(409, "lease_lost", "lease taken over")
	executor := &fakeRunExecutor{
		run: func(ctx context.Context, _ agent.WorkItem) (agentapp.RunResult, error) {
			<-ctx.Done()
			return agentapp.RunResult{Status: agent.AgentRunInterrupted}, &agentapp.RunError{
				Reason: "aborted", Retryable: false, Cause: ctx.Err(),
			}
		},
		started: make(chan int64, 1),
	}
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerHeartbeatInterval(5*time.Millisecond))

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return queue.heartbeats > 0 && len(executor.calls) == 1
	}, "heartbeat lease loss")
	// Give the worker time to (incorrectly) write back after the runner returns.
	time.Sleep(50 * time.Millisecond)
	stopWorker(t, cancel, done)

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completes) != 0 {
		t.Fatalf("completed after lease loss: %v", queue.completes)
	}
	if len(queue.fails) != 0 {
		t.Fatalf("failed after lease loss: %#v", queue.fails)
	}
}

func TestWorkerRespectsConcurrencyLimit(t *testing.T) {
	items := []agent.WorkItem{
		fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 20}),
		fakeWorkItem(agentapp.WorkTypeRequirementCurate, agentapp.WorkItemPayload{ConversationID: 20}),
		fakeWorkItem(agentapp.WorkTypeAttachmentIngest, agentapp.WorkItemPayload{AttachmentID: 9001}),
	}
	for i := range items {
		items[i].ID = int64(1000 + i)
	}
	queue := newFakeWorkerQueue(items...)
	executor := &fakeRunExecutor{
		result:  agentapp.RunResult{Status: agent.AgentRunDone},
		started: make(chan int64, 8),
		release: make(chan struct{}),
	}
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerConcurrency(2))

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		executor.mu.Lock()
		defer executor.mu.Unlock()
		return executor.active == 2
	}, "two concurrent runs")
	time.Sleep(30 * time.Millisecond)
	claims, _, _ := queue.counts()
	if claims != 2 {
		t.Fatalf("claims = %d, want concurrency limit 2", claims)
	}
	close(executor.release)
	waitUntil(t, 2*time.Second, func() bool {
		c, completes, _ := queue.counts()
		return c == 3 && completes == 3
	}, "all items processed")
	stopWorker(t, cancel, done)

	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.maxActive != 2 {
		t.Fatalf("max active runs = %d, want 2", executor.maxActive)
	}
}

func TestWorkerStopsBeforeClaimWhenCanceled(t *testing.T) {
	queue := newFakeWorkerQueue()
	worker := newTestWorker(queue, &fakeRunExecutor{result: agentapp.RunResult{Status: agent.AgentRunDone}}, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond))

	cancel, done := runWorker(t, worker)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	claims, _, _ := queue.counts()
	if claims != 0 {
		t.Fatalf("claims after cancellation = %d", claims)
	}
}

var _ agentapp.WorkerQueue = (*fakeWorkerQueue)(nil)
var _ agentapp.RunExecutor = (*fakeRunExecutor)(nil)
var _ auditapp.Recorder = (*fakeAuditRecorder)(nil)
