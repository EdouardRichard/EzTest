package storageapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

type gcFakeRunner struct {
	mu      sync.Mutex
	modes   []storage.GCMode
	actors  []int64
	err     error
	started chan struct{}
}

func (r *gcFakeRunner) Run(_ context.Context, mode storage.GCMode, actorID int64) (storage.GCRun, error) {
	r.mu.Lock()
	r.modes = append(r.modes, mode)
	r.actors = append(r.actors, actorID)
	r.mu.Unlock()
	if r.started != nil {
		select {
		case r.started <- struct{}{}:
		default:
		}
	}
	if r.err != nil {
		return storage.GCRun{}, r.err
	}
	return storage.GCRun{ID: 7, Mode: mode, Status: storage.GCRunCompleted}, nil
}

func (r *gcFakeRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.modes)
}

func (r *gcFakeRunner) lastMode() storage.GCMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.modes) == 0 {
		return ""
	}
	return r.modes[len(r.modes)-1]
}

func TestGCWorkerRunOnceUsesDryRunSetting(t *testing.T) {
	settings := newGCFakeSettings(t.TempDir())
	runner := &gcFakeRunner{}
	worker := NewGCWorker(runner, settings, nil)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if runner.lastMode() != storage.GCModeDryRun {
		t.Fatalf("mode = %q, want dry_run", runner.lastMode())
	}

	settings.bools[GCSettingDryRun] = false
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce apply: %v", err)
	}
	if runner.lastMode() != storage.GCModeApply {
		t.Fatalf("mode = %q, want apply", runner.lastMode())
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, actorID := range runner.actors {
		if actorID != 0 {
			t.Fatalf("worker actor = %d, want system (0)", actorID)
		}
	}
}

func TestGCWorkerRunOnceDefaultsToDryRunOnSettingsError(t *testing.T) {
	settings := newGCFakeSettings(t.TempDir())
	settings.err = errors.New("settings down")
	runner := &gcFakeRunner{}
	worker := NewGCWorker(runner, settings, nil)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if runner.lastMode() != storage.GCModeDryRun {
		t.Fatalf("mode = %q, want dry_run fallback", runner.lastMode())
	}
}

func TestGCWorkerRunLoopsUntilCancelled(t *testing.T) {
	settings := newGCFakeSettings(t.TempDir())
	runner := &gcFakeRunner{started: make(chan struct{}, 4)}
	worker := NewGCWorker(runner, settings, nil, WithGCWorkerInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("worker never started a run")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	if runner.calls() != 1 {
		t.Fatalf("runs = %d, want 1", runner.calls())
	}
}

func TestGCWorkerRunWaitsForInterval(t *testing.T) {
	settings := newGCFakeSettings(t.TempDir())
	runner := &gcFakeRunner{}
	worker := NewGCWorker(runner, settings, nil, WithGCWorkerInterval(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if runner.calls() != 0 {
		t.Fatalf("worker ran before the interval: %d", runner.calls())
	}
}
