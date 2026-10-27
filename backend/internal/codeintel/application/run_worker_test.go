package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Worker test doubles.
// ---------------------------------------------------------------------------

type runnerCall struct {
	kind string
	id   int64
}

type stubRunner struct {
	mu         sync.Mutex
	calls      []runnerCall
	indexFn    func(ctx context.Context, id int64) error
	analysisFn func(ctx context.Context, id int64) error
}

func (r *stubRunner) RunIndex(ctx context.Context, id int64) error {
	r.mu.Lock()
	r.calls = append(r.calls, runnerCall{kind: JobKindIndex, id: id})
	r.mu.Unlock()
	if r.indexFn != nil {
		return r.indexFn(ctx, id)
	}
	return nil
}

func (r *stubRunner) RunAnalysis(ctx context.Context, id int64) error {
	r.mu.Lock()
	r.calls = append(r.calls, runnerCall{kind: JobKindAnalysis, id: id})
	r.mu.Unlock()
	if r.analysisFn != nil {
		return r.analysisFn(ctx, id)
	}
	return nil
}

func (r *stubRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *stubRunner) recorded() []runnerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runnerCall(nil), r.calls...)
}

type stubRecoverer struct {
	mu     sync.Mutex
	calls  []time.Time
	err    error
	called chan time.Time
}

func (r *stubRecoverer) RecoverStale(_ context.Context, before time.Time) error {
	r.mu.Lock()
	r.calls = append(r.calls, before)
	r.mu.Unlock()
	if r.called != nil {
		select {
		case r.called <- before:
		default:
		}
	}
	return r.err
}

func (r *stubRecoverer) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func workerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitClosed waits for ch to be closed or fails the test after the deadline.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestWorkerQueueFull pins the bounded queue contract: a slot stays reserved
// while a job runs, so the next enqueue is rejected with ErrQueueFull instead
// of blocking or growing without bound.
func TestWorkerQueueFull(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &stubRunner{indexFn: func(context.Context, int64) error {
		close(started)
		<-release
		return nil
	}}
	w := NewWorker(WorkerConfig{QueueSize: 1, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRunner(runner)
	w.Start()

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 1}); err != nil {
		t.Fatalf("first Enqueue error = %v", err)
	}
	waitClosed(t, started, "first job to start")

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 2}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second Enqueue error = %v, want ErrQueueFull", err)
	}

	close(release)
	w.Stop(context.Background())
	if got := runner.callCount(); got != 1 {
		t.Fatalf("runner calls = %d, want 1 (rejected job must not run)", got)
	}
}

// TestWorkerRecovery pins the startup recovery hook: it is called exactly once
// with a threshold ten minutes before Now.
func TestWorkerRecovery(t *testing.T) {
	rec := &stubRecoverer{called: make(chan time.Time, 1)}
	w := NewWorker(WorkerConfig{QueueSize: 2, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRecoverer(rec)

	beforeStart := time.Now()
	w.Start()
	var got time.Time
	select {
	case got = <-rec.called:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RecoverStale")
	}
	w.Stop(context.Background())

	wantLow := beforeStart.Add(-10*time.Minute - 2*time.Second)
	wantHigh := time.Now().Add(-10*time.Minute + 2*time.Second)
	if got.Before(wantLow) || got.After(wantHigh) {
		t.Fatalf("RecoverStale before = %v, want about Now-10m", got)
	}
	if count := rec.callCount(); count != 1 {
		t.Fatalf("RecoverStale calls = %d, want 1", count)
	}
}

// TestWorkerRunsBothJobKinds pins the worker dispatch and draining Stop.
func TestWorkerRunsBothJobKinds(t *testing.T) {
	runner := &stubRunner{}
	w := NewWorker(WorkerConfig{QueueSize: 4, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRunner(runner)
	w.Start()

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 11}); err != nil {
		t.Fatalf("Enqueue(index) error = %v", err)
	}
	if err := w.Enqueue(Job{Kind: JobKindAnalysis, ID: 12}); err != nil {
		t.Fatalf("Enqueue(analysis) error = %v", err)
	}
	w.Stop(context.Background())

	got := runner.recorded()
	if len(got) != 2 {
		t.Fatalf("runner calls = %+v, want index+analysis", got)
	}
	if got[0] != (runnerCall{kind: JobKindIndex, id: 11}) || got[1] != (runnerCall{kind: JobKindAnalysis, id: 12}) {
		t.Fatalf("runner calls = %+v, want index 11 then analysis 12", got)
	}
}

// TestWorkerContinuesAfterRunnerError pins that a failed job is terminal for the
// worker: the goroutine keeps consuming the queue.
func TestWorkerContinuesAfterRunnerError(t *testing.T) {
	processed := make(chan int64, 1)
	runner := &stubRunner{indexFn: func(_ context.Context, id int64) error {
		if id == 1 {
			return errors.New("run failed")
		}
		processed <- id
		return nil
	}}
	w := NewWorker(WorkerConfig{QueueSize: 2, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRunner(runner)
	w.Start()
	defer w.Stop(context.Background())

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 1}); err != nil {
		t.Fatalf("Enqueue(1) error = %v", err)
	}
	// The first slot is released only once the failing job returns; retry the
	// enqueue briefly instead of racing the goroutine.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := w.Enqueue(Job{Kind: JobKindIndex, ID: 2})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrQueueFull) || time.Now().After(deadline) {
			t.Fatalf("Enqueue(2) error = %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case id := <-processed:
		if id != 2 {
			t.Fatalf("processed id = %d, want 2", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not process the job after a failed one")
	}
}

// TestWorkerTimeout pins the per-job context deadline.
func TestWorkerTimeout(t *testing.T) {
	timeoutSeen := make(chan error, 1)
	runner := &stubRunner{indexFn: func(ctx context.Context, _ int64) error {
		<-ctx.Done()
		timeoutSeen <- ctx.Err()
		return ctx.Err()
	}}
	w := NewWorker(WorkerConfig{QueueSize: 1, Concurrency: 1, Timeout: 20 * time.Millisecond, Logger: workerTestLogger()})
	w.SetRunner(runner)
	w.Start()
	defer w.Stop(context.Background())

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 1}); err != nil {
		t.Fatalf("Enqueue error = %v", err)
	}
	select {
	case err := <-timeoutSeen:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("job context error = %v, want DeadlineExceeded", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job context was never cancelled by the worker timeout")
	}
}

// TestWorkerRecoversPanic pins panic isolation: a panic in one job is caught
// and the worker keeps serving later jobs.
func TestWorkerRecoversPanic(t *testing.T) {
	panicked := make(chan struct{})
	processed := make(chan struct{})
	var once sync.Once
	var processedOnce sync.Once
	runner := &stubRunner{indexFn: func(_ context.Context, id int64) error {
		if id == 1 {
			once.Do(func() { close(panicked) })
			panic("runner exploded")
		}
		processedOnce.Do(func() { close(processed) })
		return nil
	}}
	w := NewWorker(WorkerConfig{QueueSize: 2, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRunner(runner)
	w.Start()
	defer w.Stop(context.Background())

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 1}); err != nil {
		t.Fatalf("Enqueue(1) error = %v", err)
	}
	waitClosed(t, panicked, "panicking job")
	// The panicking job's slot may still be reserved for a moment; retry until
	// the worker has recovered it.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := w.Enqueue(Job{Kind: JobKindIndex, ID: 2})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrQueueFull) || time.Now().After(deadline) {
			t.Fatalf("Enqueue(2) error = %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	waitClosed(t, processed, "job after the panic")
}

// TestWorkerStopRejectsEnqueue pins that a stopped worker accepts no new work.
func TestWorkerStopRejectsEnqueue(t *testing.T) {
	w := NewWorker(WorkerConfig{QueueSize: 1, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRunner(&stubRunner{})
	w.Start()
	w.Stop(context.Background())

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 1}); !errors.Is(err, ErrWorkerStopped) {
		t.Fatalf("Enqueue after Stop error = %v, want ErrWorkerStopped", err)
	}
}

// TestWorkerStopDrainsQueuedJobs pins the graceful-stop contract: Stop does not
// return before the jobs already accepted have run.
func TestWorkerStopDrainsQueuedJobs(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	processed := make(chan int64, 3)
	var once sync.Once
	runner := &stubRunner{indexFn: func(_ context.Context, id int64) error {
		if id == 1 {
			once.Do(func() { close(started) })
			<-release
		}
		processed <- id
		return nil
	}}
	w := NewWorker(WorkerConfig{QueueSize: 4, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRunner(runner)
	w.Start()

	for id := int64(1); id <= 3; id++ {
		if err := w.Enqueue(Job{Kind: JobKindIndex, ID: id}); err != nil {
			t.Fatalf("Enqueue(%d) error = %v", id, err)
		}
	}
	waitClosed(t, started, "first job to start")
	close(release)

	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w.Stop(stopCtx)

	if got := runner.callCount(); got != 3 {
		t.Fatalf("runner calls = %d, want 3 drained jobs", got)
	}
	for want := int64(1); want <= 3; want++ {
		select {
		case id := <-processed:
			if id != want {
				t.Fatalf("processed id = %d, want %d", id, want)
			}
		default:
			t.Fatalf("job %d was not processed before Stop returned", want)
		}
	}
}

// TestWorkerRecoveryFailureDoesNotBlockJobs pins that a failing startup
// recovery is logged and does not prevent the worker from serving jobs.
func TestWorkerRecoveryFailureDoesNotBlockJobs(t *testing.T) {
	processed := make(chan struct{}, 1)
	runner := &stubRunner{indexFn: func(context.Context, int64) error {
		processed <- struct{}{}
		return nil
	}}
	w := NewWorker(WorkerConfig{QueueSize: 1, Concurrency: 1, Timeout: time.Second, Logger: workerTestLogger()})
	w.SetRecoverer(&stubRecoverer{err: errors.New("db unavailable")})
	w.SetRunner(runner)
	w.Start()
	defer w.Stop(context.Background())

	if err := w.Enqueue(Job{Kind: JobKindIndex, ID: 1}); err != nil {
		t.Fatalf("Enqueue error = %v", err)
	}
	select {
	case <-processed:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not run a job after a failed recovery")
	}
}
