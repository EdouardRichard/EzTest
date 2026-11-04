package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
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
	mu            sync.Mutex
	items         []agent.WorkItem
	contractItems []agent.WorkItem
	claimErr      error

	recoverCalls       int
	claimCalls         int
	legacyClaimCalls   int
	contractClaimCalls int
	claimTeams         [][]agent.Team
	contractClaimTeams [][]agent.Team
	markedRunning      []int64
	heartbeats         int
	heartbeatErr       error
	completes          []int64
	fails              []fakeFailRecord

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
	q.legacyClaimCalls++
	q.mu.Unlock()
	return q.claimItem()
}

func (q *fakeWorkerQueue) ClaimForTeams(_ context.Context, _ string, teams []agent.Team) (agent.WorkItem, bool, error) {
	q.mu.Lock()
	q.claimTeams = append(q.claimTeams, append([]agent.Team(nil), teams...))
	q.mu.Unlock()
	return q.claimItem()
}

// ClaimContract serves the 0.2.1 contract rows (kind != ""). The production
// worker must use this claim path for them: ClaimForTeams only ever selects
// legacy rows where
//
//	kind=''
func (q *fakeWorkerQueue) ClaimContract(_ context.Context, _ string, teams []agent.Team) (*agent.WorkItem, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.contractClaimTeams = append(q.contractClaimTeams, append([]agent.Team(nil), teams...))
	if q.claimErr != nil {
		return nil, false, q.claimErr
	}
	if len(q.contractItems) == 0 {
		return nil, false, nil
	}
	item := q.contractItems[0]
	q.contractItems = q.contractItems[1:]
	q.claimCalls++
	select {
	case q.claimed <- item.ID:
	default:
	}
	return &item, true, nil
}

func (q *fakeWorkerQueue) claimItem() (agent.WorkItem, bool, error) {
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

func (q *fakeWorkerQueue) Heartbeat(_ context.Context, _, _ int64, _ time.Duration) error {
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

// FailContract mirrors the 0.2.1 failure policy for contract rows.
func (q *fakeWorkerQueue) FailContract(ctx context.Context, id, epoch int64, cause error, retryable bool) error {
	return q.Fail(ctx, id, epoch, cause, retryable)
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

// TestWorkerDrainGateStopsNewClaims pins P7 §4.5 for the agent worker: once the
// replica starts draining, in-flight runs finish but no new item is claimed.
func TestWorkerDrainGateStopsNewClaims(t *testing.T) {
	first := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 20})
	second := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 21})
	second.ID = first.ID + 1
	queue := newFakeWorkerQueue(first, second)
	executor := &fakeRunExecutor{
		result:  agentapp.RunResult{AgentRunID: 77, Status: agent.AgentRunDone},
		started: make(chan int64, 4),
		release: make(chan struct{}),
	}
	var draining atomic.Bool
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerConcurrency(1),
		agentapp.WithWorkerDrainGate(draining.Load),
	)
	cancel, done := runWorker(t, worker)
	defer stopWorker(t, cancel, done)

	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started the first item")
	}
	draining.Store(true)
	close(executor.release)
	waitUntil(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.completes) == 1
	}, "in-flight item completion")

	time.Sleep(40 * time.Millisecond)
	claims, _, _ := queue.counts()
	if claims != 1 {
		t.Fatalf("claims while draining = %d, want only the in-flight 1", claims)
	}
}

// TestStageEntryWorkItemClaimed pins Task 65's stage-entry path: a 0.2.1
// design.cases item (created through the dispatcher defaults) is claimed by the
// agent worker through the contract claim, dispatched to the runner and
// completed. Before the fix the item carried Team=ops/Type=kind and the legacy
// team claim skipped it forever: rows with
//
//	kind <> ''
//
// are not claimable there.
func TestStageEntryWorkItemClaimed(t *testing.T) {
	params := agent.CreateWorkItemParams{
		VersionID:      7,
		Kind:           agent.WorkItemKindDesignCases,
		Inputs:         json.RawMessage(`{"requirement_ids":["501"]}`),
		CreatedBy:      agent.WorkItemCreatedByStateMachine,
		IdempotencyKey: "7:design.cases:entry",
	}.Normalize()
	item := agent.WorkItem{
		ID:          9001,
		VersionID:   7,
		Team:        params.Team,
		Type:        params.Type,
		Kind:        params.Kind,
		Inputs:      params.Inputs,
		Status:      agent.WorkItemOpen,
		DedupeKey:   "v021:design.cases:9001",
		MaxAttempts: 3,
	}
	queue := newFakeWorkerQueue()
	queue.contractItems = []agent.WorkItem{item}
	executor := &fakeRunExecutor{result: agentapp.RunResult{Status: agent.AgentRunDone}}
	worker := newTestWorker(queue, executor, &fakeSettings{}, agentapp.WithWorkerPollInterval(2*time.Millisecond))

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		executor.mu.Lock()
		ran := len(executor.calls)
		executor.mu.Unlock()
		queue.mu.Lock()
		completed := len(queue.completes)
		queue.mu.Unlock()
		return ran == 1 && completed == 1
	}, "stage-entry item claim and completion")
	stopWorker(t, cancel, done)

	executor.mu.Lock()
	claim := executor.calls[0]
	executor.mu.Unlock()
	if claim.Kind != agent.WorkItemKindDesignCases || claim.Type != agent.WorkItemKindDesignCases {
		t.Fatalf("dispatched item kind/type = %q/%q", claim.Kind, claim.Type)
	}
	if claim.Team != agent.TeamDesign {
		t.Fatalf("dispatched item team = %q, want design", claim.Team)
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completes) != 1 || queue.completes[0] != item.ID {
		t.Fatalf("completes = %v", queue.completes)
	}
	if len(queue.contractClaimTeams) == 0 {
		t.Fatal("worker never used the 0.2.1 contract claim")
	}
	for _, teams := range queue.contractClaimTeams {
		if !slices.Contains(teams, agent.TeamDesign) {
			t.Fatalf("contract claim teams = %v, want design", teams)
		}
	}
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

func TestWorkerClaimsOnlyAgentProductTeams(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeChatTurn, agentapp.WorkItemPayload{ConversationID: 20})
	queue := newFakeWorkerQueue(item)
	executor := &fakeRunExecutor{result: agentapp.RunResult{AgentRunID: 77, Status: agent.AgentRunDone}}
	worker := newTestWorker(queue, executor, &fakeSettings{}, agentapp.WithWorkerPollInterval(2*time.Millisecond))

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.completes) == 1
	}, "work item completion")
	stopWorker(t, cancel, done)

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.legacyClaimCalls != 0 {
		t.Fatalf("worker used the unfiltered Claim %d times", queue.legacyClaimCalls)
	}
	if len(queue.claimTeams) == 0 {
		t.Fatal("worker never called ClaimForTeams")
	}
	productTeams := []agent.Team{
		agent.TeamClarification,
		agent.TeamDesign,
		agent.TeamExecution,
		agent.TeamDelivery,
		agent.TeamSteward,
	}
	for _, teams := range queue.claimTeams {
		if !slices.Equal(teams, productTeams) {
			t.Fatalf("claim teams = %#v, want %#v", teams, productTeams)
		}
		for _, team := range teams {
			if team == agent.TeamCode || team == agent.TeamOps {
				t.Fatalf("agent worker claimed %s team: %#v", team, teams)
			}
		}
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

// TestDesignAuthorQueueDrainsWithinConcurrency pins the queue-scaling half of
// Task 79: a 67-requirement design round enqueues one authoring item per
// requirement, and the worker must drain all of them while never running more
// than agent.max_concurrent_runs at once. Combined with the bounded repair
// generations in the design planner, the queue is finite and drains instead of
// flooding the shared stack.
func TestDesignAuthorQueueDrainsWithinConcurrency(t *testing.T) {
	const (
		authorCount = 67
		limit       = 4
	)
	items := make([]agent.WorkItem, 0, authorCount)
	for index := 0; index < authorCount; index++ {
		item := fakeWorkItem(agentapp.WorkTypeDesignRequirement, agentapp.WorkItemPayload{
			ConversationID: 20, RequirementID: int64(5000 + index), RequirementVersionID: int64(7000 + index),
		})
		item.ID = int64(2000 + index)
		items = append(items, item)
	}
	queue := newFakeWorkerQueue(items...)
	executor := &fakeRunExecutor{
		result:  agentapp.RunResult{Status: agent.AgentRunDone},
		release: make(chan struct{}),
	}
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerConcurrency(limit))

	cancel, done := runWorker(t, worker)
	waitUntil(t, 3*time.Second, func() bool {
		executor.mu.Lock()
		defer executor.mu.Unlock()
		return executor.active == limit
	}, "the design author concurrency limit")
	time.Sleep(30 * time.Millisecond)
	if claims, _, _ := queue.counts(); claims > limit {
		t.Fatalf("claimed %d design authors, want the concurrency bound %d", claims, limit)
	}
	close(executor.release)
	waitUntil(t, 5*time.Second, func() bool {
		_, completes, _ := queue.counts()
		return completes == authorCount
	}, "all 67 design author items to drain")
	stopWorker(t, cancel, done)

	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.maxActive > limit {
		t.Fatalf("max concurrent design authors = %d, want <= %d", executor.maxActive, limit)
	}
	if len(executor.calls) != authorCount {
		t.Fatalf("executed design authors = %d, want %d", len(executor.calls), authorCount)
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
