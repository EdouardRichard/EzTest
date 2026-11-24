package agentapp_test

// v0.2.4-H T12 (H-E2): the crash sweep is WIRED into the worker's recovery pass.
//
// RecoverExpired settles work items; without a run-level sweep behind it the runs
// those items abandoned stay 'running' with streaming heads forever. These tests
// pin the wiring itself (startup pass + every periodic recovery pass), because a
// sweeper that exists but is never called is exactly the "code in place but
// unusable" state constitution 七.5 forbids counting as implemented.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

type recordingCrashSweeper struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *recordingCrashSweeper) Sweep(_ context.Context) (agentapp.CrashSweepReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return agentapp.CrashSweepReport{}, s.err
	}
	return agentapp.CrashSweepReport{Runs: 1, TranscriptHeads: 1}, nil
}

func (s *recordingCrashSweeper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestWorkerSweepsCrashedRunsAfterRecovery(t *testing.T) {
	queue := newFakeWorkerQueue()
	sweeper := &recordingCrashSweeper{}
	worker := newTestWorker(queue, &fakeRunExecutor{}, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerRecoveryInterval(3*time.Millisecond),
		agentapp.WithWorkerCrashSweeper(sweeper),
	)

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool { return sweeper.count() >= 2 },
		"the crash sweep to run at startup and on a recovery tick")
	stopWorker(t, cancel, done)

	queue.mu.Lock()
	recoverCalls := queue.recoverCalls
	queue.mu.Unlock()
	if recoverCalls < 2 {
		t.Fatalf("lease recovery passes = %d, want the sweep to ride them", recoverCalls)
	}
}

// TestWorkerCrashSweepFailureDoesNotStopTheWorker pins best-effort semantics: a
// failing sweep is logged and the worker keeps claiming and recovering.
func TestWorkerCrashSweepFailureDoesNotStopTheWorker(t *testing.T) {
	queue := newFakeWorkerQueue()
	sweeper := &recordingCrashSweeper{err: errors.New("store unavailable")}
	worker := newTestWorker(queue, &fakeRunExecutor{}, &fakeSettings{},
		agentapp.WithWorkerPollInterval(2*time.Millisecond),
		agentapp.WithWorkerRecoveryInterval(3*time.Millisecond),
		agentapp.WithWorkerCrashSweeper(sweeper),
	)

	cancel, done := runWorker(t, worker)
	waitUntil(t, 2*time.Second, func() bool { return sweeper.count() >= 2 },
		"the worker to keep sweeping after a failed pass")
	stopWorker(t, cancel, done)
}
