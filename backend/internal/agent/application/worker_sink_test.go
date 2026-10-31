package agentapp_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// fakeRunSink records the worker run-finished callbacks.
type fakeRunSink struct {
	mu      sync.Mutex
	calls   int
	item    agent.WorkItem
	result  agentapp.RunResult
	runErr  error
	sinkErr error
}

func (s *fakeRunSink) OnRunFinished(_ context.Context, item agent.WorkItem, result agentapp.RunResult, runErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.item = item
	s.result = result
	s.runErr = runErr
	return s.sinkErr
}

func (s *fakeRunSink) snapshot() (int, agentapp.RunResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.result, s.runErr
}

func TestWorkerCallsRunSinkOnSuccess(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeExecuteCase, agentapp.WorkItemPayload{
		IntentID: 1001, WaveID: 2001, CaseRunID: 3001, DispatchEpoch: 2, DispatchToken: "tok",
	})
	queue := newFakeWorkerQueue(item)
	executor := &fakeRunExecutor{
		result: agentapp.RunResult{AgentRunID: 77, Status: agent.AgentRunDone},
	}
	sink := &fakeRunSink{sinkErr: errors.New("sink write failed")}
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerRunSink(sink),
	)

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		calls, _, _ := sink.snapshot()
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.completes) == 1 && calls == 1
	}, "run sink success callback")
	stopWorker(t, cancel, done)

	calls, result, runErr := sink.snapshot()
	if calls != 1 {
		t.Fatalf("sink calls = %d", calls)
	}
	if result.Status != agent.AgentRunDone || result.AgentRunID != 77 || runErr != nil {
		t.Fatalf("sink result = %+v, err = %v", result, runErr)
	}
	// A sink error must not prevent the work item from completing.
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completes) != 1 || len(queue.fails) != 0 {
		t.Fatalf("completes = %v, fails = %#v", queue.completes, queue.fails)
	}
}

func TestWorkerCallsRunSinkOnFailure(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeExecuteCase, agentapp.WorkItemPayload{CaseRunID: 3001})
	queue := newFakeWorkerQueue(item)
	runErr := &agentapp.RunError{Reason: "model_error", Retryable: true, Cause: errors.New("model down")}
	executor := &fakeRunExecutor{
		result: agentapp.RunResult{AgentRunID: 78, Status: agent.AgentRunFailed},
		err:    runErr,
	}
	sink := &fakeRunSink{}
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerRunSink(sink),
	)

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		calls, _, _ := sink.snapshot()
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.fails) == 1 && calls == 1
	}, "run sink failure callback")
	stopWorker(t, cancel, done)

	calls, result, gotErr := sink.snapshot()
	if calls != 1 || result.Status != agent.AgentRunFailed || gotErr == nil {
		t.Fatalf("sink calls = %d, result = %+v, err = %v", calls, result, gotErr)
	}
}

func TestWorkerSkipsRunSinkOnLeaseLost(t *testing.T) {
	item := fakeWorkItem(agentapp.WorkTypeExecuteCase, agentapp.WorkItemPayload{CaseRunID: 3001})
	queue := newFakeWorkerQueue(item)
	executor := &fakeRunExecutor{
		result: agentapp.RunResult{AgentRunID: 79, Status: agent.AgentRunInterrupted},
		err:    &agentapp.RunError{Reason: "lease_lost", Retryable: false},
	}
	sink := &fakeRunSink{}
	worker := newTestWorker(queue, executor, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerRunSink(sink),
	)

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.fails) == 1
	}, "lease-lost failure write-back")
	stopWorker(t, cancel, done)

	if calls, _, _ := sink.snapshot(); calls != 0 {
		t.Fatalf("sink must not be called after lease loss, calls = %d", calls)
	}
}
