package executionapp

// W1-14A (spec R-EX-3/R-EX-5, R-EX-9): the scheduler triggers the version-context
// discovery delta exactly once when a wave reaches its terminal done state. The
// call is best effort: a failing evaluator (including ErrDeltaBusy mapped to nil
// by the production adapter) never rolls the wave back or fails the tick.

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

func TestSchedulerTriggersDiscoveryDeltaOnWaveTerminal(t *testing.T) {
	f := newTask6Fixture(t)
	versionID, waveID := finishTask6Wave(t, f)

	delta := &recordingDiscoveryDelta{}
	f.scheduler.delta = delta
	if err := f.scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
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
	t.Logf("wave %d terminal -> discovery delta calls=%v waves=%v timeout=%v", waveID, versions, waves, timeouts[0])
	if timeouts[0] <= 0 || timeouts[0] > 15*time.Second {
		t.Fatalf("delta timeout = %v, want a 15s best-effort deadline", timeouts[0])
	}

	// The terminal transition happens once; a later tick never re-evaluates.
	if err := f.scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	if versions, waves, _ := delta.calls(); len(versions) != 1 || len(waves) != 1 {
		t.Fatalf("duplicate delta calls = versions %v waves %v", versions, waves)
	}
}

func TestSchedulerDiscoveryDeltaFailureDoesNotFailWave(t *testing.T) {
	f := newTask6Fixture(t)
	versionID, waveID := finishTask6Wave(t, f)

	delta := &recordingDiscoveryDelta{err: errors.New("delta evaluation failed")}
	f.scheduler.delta = delta
	logs := captureSchedulerSlog(t)
	if err := f.scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("terminal ReconcileVersion must stay green, got %v", err)
	}
	finished, err := f.waves.Get(context.Background(), waveID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done despite a failed delta", finished, err)
	}
	if versions, _, _ := delta.calls(); len(versions) != 1 {
		t.Fatalf("delta calls = %v, want exactly one attempt", versions)
	}
	if logged := logs.String(); !strings.Contains(logged, "discovery delta") {
		t.Fatalf("failed delta evaluation was not logged: %q", logged)
	}
	t.Logf("delta failure left wave %d done; scheduler log: %s", waveID, strings.TrimSpace(logs.String()))
}
