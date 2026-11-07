package executionapp

// W1-14A (spec R-EX-3/R-EX-5, R-EX-9): the scheduler triggers the version-context
// discovery delta exactly once when a wave reaches a terminal state. Every
// terminal path must notify: done (completeWave), an abnormal planned wave with
// no case_runs (failEmptyWave) and a cancelled wave (PauseService.CancelWave).
// The call is best effort: a failing evaluator (including ErrDeltaBusy mapped to
// nil by the production adapter) never rolls the wave back or fails the tick.
//
// Fix round 1: the scheduler is rebuilt through NewScheduler(SchedulerDeps{Delta})
// instead of mutating the private field, so the test also pins the production
// dependency injection.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

type recordingDiscoveryDelta struct {
	mu      sync.Mutex
	version []int64
	wave    []int64
	timeout []time.Duration
	err     error
}

func (d *recordingDiscoveryDelta) Evaluate(ctx context.Context, versionID, waveID int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.version = append(d.version, versionID)
	d.wave = append(d.wave, waveID)
	if deadline, ok := ctx.Deadline(); ok {
		d.timeout = append(d.timeout, time.Until(deadline))
	} else {
		d.timeout = append(d.timeout, -1)
	}
	return d.err
}

func (d *recordingDiscoveryDelta) calls() ([]int64, []int64, []time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int64(nil), d.version...), append([]int64(nil), d.wave...), append([]time.Duration(nil), d.timeout...)
}

// schedulerWithDelta rebuilds the fixture scheduler through NewScheduler with the
// production Delta dependency. The fakes are shared, so dispatching through
// f.scheduler first and finalizing through this scheduler exercises the same
// stores.
func (f *task6Fixture) schedulerWithDelta(delta DiscoveryDeltaEvaluator) *Scheduler {
	return NewScheduler(SchedulerDeps{
		Intents:     f.intents,
		Waves:       f.waves,
		CaseRuns:    f.runs,
		Cases:       f.cases,
		Versions:    f.versions,
		Executors:   f.registry,
		WorkQueue:   f.queue,
		Quota:       domainQuota(3, 2, 1),
		QuotaReader: f.quota,
		Analyzer:    f.analysis,
		Replanner:   f.intent,
		IDs:         f.ids,
		Clock:       f.clock,
		Events:      f.events,
		Instance:    "api-1",
		LeaseTTL:    30 * time.Second,
		Delta:       delta,
	})
}

// assertDeltaTimeout pins the single 15s best-effort deadline with a tolerance
// range (the remaining duration is measured after the context is built).
func assertDeltaTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	if timeout < 14*time.Second || timeout > 15*time.Second {
		t.Fatalf("delta timeout = %v, want about 15s (14s..15s)", timeout)
	}
}

// finishTask6Wave drives the fixture through dispatch and the terminal case_run
// state so the next reconcile finalizes the wave.
func finishTask6Wave(t *testing.T, f *task6Fixture) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("dispatch ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("wave not created")
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	run, _ := f.runs.Get(ctx, runs[0].ID)
	run.Status = execution.CaseRunPassed
	run.FailureClass = ""
	if ok, err := f.runs.Update(ctx, run); err != nil || !ok {
		t.Fatalf("finish run: %v %v", ok, err)
	}
	return 100, wave.ID
}

// TestSchedulerTriggersDiscoveryDeltaOnWaveTerminal pins the done path: the
// terminal transition calls the evaluator exactly once with the 15s deadline,
// and a later tick never repeats it.
func TestSchedulerTriggersDiscoveryDeltaOnWaveTerminal(t *testing.T) {
	f := newTask6Fixture(t)
	versionID, waveID := finishTask6Wave(t, f)

	delta := &recordingDiscoveryDelta{}
	scheduler := f.schedulerWithDelta(delta)
	if err := scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("terminal ReconcileVersion: %v", err)
	}
	finished, err := f.waves.Get(context.Background(), waveID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done", finished, err)
	}

	versions, waves, timeouts := delta.calls()
	if len(versions) != 1 || versions[0] != versionID || waves[0] != waveID {
		t.Fatalf("delta calls = versions %v waves %v, want one (%d,%d)", versions, waves, versionID, waveID)
	}
	assertDeltaTimeout(t, timeouts[0])
	t.Logf("wave %d done -> discovery delta calls=%v waves=%v timeout=%v", waveID, versions, waves, timeouts[0])

	// The terminal transition happens once; a later tick never re-evaluates.
	if err := scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	if versions, waves, _ := delta.calls(); len(versions) != 1 || len(waves) != 1 {
		t.Fatalf("duplicate delta calls = versions %v waves %v", versions, waves)
	}
}

// TestSchedulerTriggersDiscoveryDeltaOnEmptyWaveFailure pins the abnormal
// planned-wave path: failEmptyWave is a terminal state too, so the evaluator is
// called once even though no case_run exists (the version evaluator no-ops when
// the wave produced no observation).
func TestSchedulerTriggersDiscoveryDeltaOnEmptyWaveFailure(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	now := f.clock.Now()
	// An active planned wave without case_runs: ReconcileVersion reaches
	// failEmptyWave without planning another wave (there are no open intents).
	empty := execution.RunWave{
		ID: 900, VersionID: 100, Status: execution.WavePlanned,
		Counts:    execution.WaveCounts{ByVersionSUT: map[string]execution.SUTCounts{}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.waves.CreateWithCaseRuns(ctx, empty, nil, nil); err != nil {
		t.Fatalf("seed empty wave: %v", err)
	}

	delta := &recordingDiscoveryDelta{}
	scheduler := f.schedulerWithDelta(delta)
	if err := scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("empty-wave ReconcileVersion: %v", err)
	}
	failed, err := f.waves.Get(ctx, empty.ID)
	if err != nil || failed.Status != execution.WaveFailed {
		t.Fatalf("wave = %+v err=%v, want failed", failed, err)
	}
	versions, waves, timeouts := delta.calls()
	if len(versions) != 1 || versions[0] != 100 || waves[0] != empty.ID {
		t.Fatalf("delta calls = versions %v waves %v, want one (100,%d)", versions, waves, empty.ID)
	}
	assertDeltaTimeout(t, timeouts[0])
	t.Logf("empty wave %d failed -> discovery delta calls=%v waves=%v timeout=%v",
		empty.ID, versions, waves, timeouts[0])

	// A failed wave is no longer active, so a later tick has nothing to fail.
	if err := scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second empty-wave ReconcileVersion: %v", err)
	}
	if versions, waves, _ := delta.calls(); len(versions) != 1 || len(waves) != 1 {
		t.Fatalf("duplicate delta calls on empty wave = versions %v waves %v", versions, waves)
	}
}

// TestSchedulerDiscoveryDeltaFailureDoesNotFailWave pins the best-effort
// contract on the done path: the failure is logged, the wave stays done and the
// tick stays green.
func TestSchedulerDiscoveryDeltaFailureDoesNotFailWave(t *testing.T) {
	f := newTask6Fixture(t)
	versionID, waveID := finishTask6Wave(t, f)

	delta := &recordingDiscoveryDelta{err: errors.New("delta evaluation failed")}
	scheduler := f.schedulerWithDelta(delta)
	logs := captureSchedulerSlog(t)
	if err := scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("terminal ReconcileVersion must stay green, got %v", err)
	}
	finished, err := f.waves.Get(context.Background(), waveID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done despite a failed delta", finished, err)
	}
	versions, _, timeouts := delta.calls()
	if len(versions) != 1 {
		t.Fatalf("delta calls = %v, want exactly one attempt", versions)
	}
	assertDeltaTimeout(t, timeouts[0])
	if logged := logs.String(); !strings.Contains(logged, "discovery delta") {
		t.Fatalf("failed delta evaluation was not logged: %q", logged)
	}
	t.Logf("delta failure left wave %d done; scheduler log: %s", waveID, strings.TrimSpace(logs.String()))
}
