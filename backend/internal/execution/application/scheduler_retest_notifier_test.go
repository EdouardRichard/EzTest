package executionapp

// v0.22 W2-5 (R-W2-2): every wave-terminal path notifies the delivery retest
// writeback exactly like the existing stage callback: done (completeWave),
// failed (failEmptyWave) and cancelled (PauseService.CancelWave). The call is
// nil-safe and best effort, bounded by a deadline, and a delivery failure never
// rolls the wave back.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

type recordingRetestNotifier struct {
	mu       sync.Mutex
	waves    []int64
	timeouts []time.Duration
	err      error
}

func (n *recordingRetestNotifier) OnWaveTerminalRetest(ctx context.Context, waveID int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.waves = append(n.waves, waveID)
	if deadline, ok := ctx.Deadline(); ok {
		n.timeouts = append(n.timeouts, time.Until(deadline))
	} else {
		n.timeouts = append(n.timeouts, -1)
	}
	return n.err
}

func (n *recordingRetestNotifier) calls() ([]int64, []time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]int64(nil), n.waves...), append([]time.Duration(nil), n.timeouts...)
}

func assertRetestNotifierTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	if timeout < 14*time.Second || timeout > 15*time.Second {
		t.Fatalf("retest notifier timeout = %v, want about 15s (14s..15s)", timeout)
	}
}

// schedulerWithRetest rebuilds the fixture scheduler through NewScheduler with
// the production RetestNotifier dependency, pinning the injection point.
func (f *task6Fixture) schedulerWithRetest(notifier WaveTerminalRetestNotifier) *Scheduler {
	return NewScheduler(SchedulerDeps{
		Intents:        f.intents,
		Waves:          f.waves,
		CaseRuns:       f.runs,
		Cases:          f.cases,
		Versions:       f.versions,
		Executors:      f.registry,
		WorkQueue:      f.queue,
		Quota:          domainQuota(3, 2, 1),
		QuotaReader:    f.quota,
		Analyzer:       f.analysis,
		Replanner:      f.intent,
		IDs:            f.ids,
		Clock:          f.clock,
		Events:         f.events,
		Instance:       "api-1",
		LeaseTTL:       30 * time.Second,
		RetestNotifier: notifier,
	})
}

func TestSchedulerNotifiesRetestOnWaveDone(t *testing.T) {
	f := newTask6Fixture(t)
	versionID, waveID := finishTask6Wave(t, f)

	notifier := &recordingRetestNotifier{}
	scheduler := f.schedulerWithRetest(notifier)
	if err := scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("terminal ReconcileVersion: %v", err)
	}
	finished, err := f.waves.Get(context.Background(), waveID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done", finished, err)
	}
	waves, timeouts := notifier.calls()
	if len(waves) != 1 || waves[0] != waveID {
		t.Fatalf("retest notifier waves = %v, want [%d]", waves, waveID)
	}
	assertRetestNotifierTimeout(t, timeouts[0])

	// A done wave is terminal once: the callback is never repeated.
	if err := scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	if waves, _ := notifier.calls(); len(waves) != 1 {
		t.Fatalf("duplicate retest notifications = %v, want one", waves)
	}
}

func TestSchedulerNotifiesRetestOnEmptyWaveFailure(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	now := f.clock.Now()
	empty := execution.RunWave{
		ID: 900, VersionID: 100, Status: execution.WavePlanned,
		Counts:    execution.WaveCounts{ByVersionSUT: map[string]execution.SUTCounts{}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.waves.CreateWithCaseRuns(ctx, empty, nil, nil); err != nil {
		t.Fatalf("seed empty wave: %v", err)
	}

	notifier := &recordingRetestNotifier{}
	scheduler := f.schedulerWithRetest(notifier)
	if err := scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("empty-wave ReconcileVersion: %v", err)
	}
	failed, err := f.waves.Get(ctx, empty.ID)
	if err != nil || failed.Status != execution.WaveFailed {
		t.Fatalf("wave = %+v err=%v, want failed", failed, err)
	}
	waves, timeouts := notifier.calls()
	if len(waves) != 1 || waves[0] != empty.ID {
		t.Fatalf("retest notifier waves = %v, want [%d]", waves, empty.ID)
	}
	assertRetestNotifierTimeout(t, timeouts[0])

	if err := scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second empty-wave ReconcileVersion: %v", err)
	}
	if waves, _ := notifier.calls(); len(waves) != 1 {
		t.Fatalf("duplicate retest notifications on empty wave = %v", waves)
	}
}

func TestSchedulerRetestNotifierFailureDoesNotFailWave(t *testing.T) {
	f := newTask6Fixture(t)
	versionID, waveID := finishTask6Wave(t, f)

	notifier := &recordingRetestNotifier{err: errors.New("delivery writeback unavailable")}
	scheduler := f.schedulerWithRetest(notifier)
	logs := captureSchedulerSlog(t)
	if err := scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("terminal ReconcileVersion must stay green, got %v", err)
	}
	finished, err := f.waves.Get(context.Background(), waveID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done despite a failed retest callback", finished, err)
	}
	if waves, _ := notifier.calls(); len(waves) != 1 {
		t.Fatalf("retest notifier attempts = %v, want one", waves)
	}
	if logged := logs.String(); !strings.Contains(logged, "retest") {
		t.Fatalf("failed retest writeback was not logged: %q", logged)
	}
}

func TestSchedulerRetestNotifierNilSafe(t *testing.T) {
	f := newTask6Fixture(t)
	versionID, waveID := finishTask6Wave(t, f)
	// f.scheduler has no RetestNotifier: finalization must still succeed.
	if err := f.scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("ReconcileVersion without a retest notifier: %v", err)
	}
	finished, err := f.waves.Get(context.Background(), waveID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done", finished, err)
	}
}

func TestPauseServiceNotifiesRetestOnCancel(t *testing.T) {
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	f.seedRun(500, 1, execution.CaseRunRunning)

	notifier := &recordingRetestNotifier{}
	pause := NewPauseService(PauseServiceDeps{
		Intents:        f.intents,
		Waves:          f.waves,
		CaseRuns:       f.runs,
		Humans:         f.humans,
		Contexts:       f.contexts,
		Canceller:      f.canceller,
		Executor:       f.control,
		Clock:          f.clock,
		Events:         f.events,
		RetestNotifier: notifier,
	})
	if _, err := pause.CancelWave(context.Background(), 100, 50, 42); err != nil {
		t.Fatalf("CancelWave: %v", err)
	}
	waves, timeouts := notifier.calls()
	if len(waves) != 1 || waves[0] != 50 {
		t.Fatalf("retest notifier waves = %v, want [50]", waves)
	}
	assertRetestNotifierTimeout(t, timeouts[0])

	// A repeated cancel re-runs the idempotent writeback so a lost delivery
	// callback can heal.
	if _, err := pause.CancelWave(context.Background(), 100, 50, 42); err != nil {
		t.Fatalf("duplicate CancelWave: %v", err)
	}
	if waves, _ := notifier.calls(); len(waves) != 2 {
		t.Fatalf("retest notifier waves after repeat cancel = %v, want two", waves)
	}
}

func TestPauseServiceRetestNotifierFailureDoesNotFailCancel(t *testing.T) {
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	f.seedRun(500, 1, execution.CaseRunRunning)

	notifier := &recordingRetestNotifier{err: errors.New("delivery writeback unavailable")}
	pause := NewPauseService(PauseServiceDeps{
		Intents:        f.intents,
		Waves:          f.waves,
		CaseRuns:       f.runs,
		Humans:         f.humans,
		Contexts:       f.contexts,
		Canceller:      f.canceller,
		Executor:       f.control,
		Clock:          f.clock,
		Events:         f.events,
		RetestNotifier: notifier,
	})
	cancelled, err := pause.CancelWave(context.Background(), 100, 50, 42)
	if err != nil {
		t.Fatalf("CancelWave with a failing retest notifier: %v", err)
	}
	if cancelled.Status != execution.WaveCancelled {
		t.Fatalf("wave = %s, want cancelled", cancelled.Status)
	}
	persisted, err := f.waves.Get(context.Background(), 50)
	if err != nil || persisted.Status != execution.WaveCancelled {
		t.Fatalf("persisted wave = %+v err=%v, want cancelled", persisted, err)
	}
}
