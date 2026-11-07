package executionapp

// W1-14A fix round 1: a cancelled wave is a terminal wave (B §7.1), so
// PauseService.CancelWave must drive the same discovery-delta trigger as the
// scheduler's done/failed paths. Exactly one pass per terminal wave: a repeated
// cancel of an already-cancelled wave only re-notifies the (idempotent) stage
// callback and never re-evaluates the now-stale observations.

import (
	"context"
	"errors"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// newTask9PauseWithDelta rebuilds the fixture pause service through the
// production constructor so the new Delta dependency is injected, not poked.
func newTask9PauseWithDelta(f *task9Fixture, delta DiscoveryDeltaEvaluator) *PauseService {
	return NewPauseService(PauseServiceDeps{
		Intents:   f.intents,
		Waves:     f.waves,
		CaseRuns:  f.runs,
		Humans:    f.humans,
		Contexts:  f.contexts,
		Canceller: f.canceller,
		Executor:  f.control,
		Clock:     f.clock,
		Events:    f.events,
		Delta:     delta,
	})
}

// TestCancelWaveTriggersDiscoveryDeltaOnce pins the cancelled terminal path: the
// evaluator runs once after the cancel committed, with the shared 15s deadline,
// and a repeated cancel does not trigger a second pass.
func TestCancelWaveTriggersDiscoveryDeltaOnce(t *testing.T) {
	f := newTask9Fixture()
	f.seedIntent(70, 300)
	wave := f.seedWave(execution.WaveRunning)
	f.seedRun(80, 70, execution.CaseRunRunning)
	delta := &recordingDiscoveryDelta{}
	pause := newTask9PauseWithDelta(f, delta)

	cancelled, err := pause.CancelWave(context.Background(), 100, wave.ID, 7)
	if err != nil {
		t.Fatalf("CancelWave: %v", err)
	}
	if cancelled.Status != execution.WaveCancelled {
		t.Fatalf("wave status = %q, want cancelled", cancelled.Status)
	}
	versions, waves, timeouts := delta.calls()
	if len(versions) != 1 || versions[0] != 100 || waves[0] != wave.ID {
		t.Fatalf("delta calls = versions %v waves %v, want one (100,%d)", versions, waves, wave.ID)
	}
	assertDeltaTimeout(t, timeouts[0])
	t.Logf("wave %d cancelled -> discovery delta calls=%v waves=%v timeout=%v",
		wave.ID, versions, waves, timeouts[0])

	// A repeated cancel converges the rows but the wave was already terminal,
	// so it must not re-run the delta pass.
	if _, err := pause.CancelWave(context.Background(), 100, wave.ID, 7); err != nil {
		t.Fatalf("duplicate CancelWave: %v", err)
	}
	if versions, waves, _ := delta.calls(); len(versions) != 1 || len(waves) != 1 {
		t.Fatalf("duplicate delta calls after repeated cancel = versions %v waves %v", versions, waves)
	}
}

// TestCancelWaveDeltaFailureDoesNotRollBackCancellation pins the best-effort
// contract: a failing evaluator is logged, the cancellation is still returned as
// success and the persisted wave stays cancelled.
func TestCancelWaveDeltaFailureDoesNotRollBackCancellation(t *testing.T) {
	f := newTask9Fixture()
	f.seedIntent(70, 300)
	wave := f.seedWave(execution.WaveRunning)
	f.seedRun(80, 70, execution.CaseRunRunning)
	delta := &recordingDiscoveryDelta{err: errors.New("delta evaluation failed")}
	pause := newTask9PauseWithDelta(f, delta)
	logs := captureSchedulerSlog(t)

	cancelled, err := pause.CancelWave(context.Background(), 100, wave.ID, 7)
	if err != nil {
		t.Fatalf("CancelWave with failing delta: %v", err)
	}
	if cancelled.Status != execution.WaveCancelled {
		t.Fatalf("wave status = %q, want cancelled", cancelled.Status)
	}
	if versions, _, _ := delta.calls(); len(versions) != 1 {
		t.Fatalf("delta calls = %v, want exactly one attempt", versions)
	}
	persisted, err := f.waves.Get(context.Background(), wave.ID)
	if err != nil || persisted.Status != execution.WaveCancelled {
		t.Fatalf("persisted wave = %+v err=%v, want cancelled", persisted, err)
	}
	if logged := logs.String(); !strings.Contains(logged, "discovery delta") {
		t.Fatalf("failed delta evaluation was not logged: %q", logged)
	}
}

// TestCancelWaveTerminalDoneDoesNotTriggerDiscoveryDelta pins the guard: a
// finished (non-cancelled) wave is a conflict, so the cancel never happens and
// the delta trigger is never invoked.
func TestCancelWaveTerminalDoneDoesNotTriggerDiscoveryDelta(t *testing.T) {
	f := newTask9Fixture()
	f.seedIntent(70, 300)
	wave := f.seedWave(execution.WaveDone)
	delta := &recordingDiscoveryDelta{}
	pause := newTask9PauseWithDelta(f, delta)

	if _, err := pause.CancelWave(context.Background(), 100, wave.ID, 7); err == nil {
		t.Fatal("CancelWave of a done wave must be a state conflict")
	}
	if versions, waves, _ := delta.calls(); len(versions) != 0 || len(waves) != 0 {
		t.Fatalf("delta calls = versions %v waves %v, want none", versions, waves)
	}
}
