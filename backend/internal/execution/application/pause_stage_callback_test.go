package executionapp

// v0.22 Task 25 fix round 1 (review finding 2): B §7.1 counts a cancelled wave as
// terminal, so PauseService.CancelWave must invoke the same wave-terminal stage
// callback as the scheduler. Without it a cancellation leaves the version in
// executing until a manual stage advance.

import (
	"context"
	"errors"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestCancelWaveNotifiesStageCallback pins the production call site: cancelling a
// wave notifies the stage callback after the cancel is committed, and a repeated
// cancel re-notifies so a stage advance lost after the first callback can heal.
func TestCancelWaveNotifiesStageCallback(t *testing.T) {
	f := newTask9Fixture()
	f.seedIntent(70, 300)
	wave := f.seedWave(execution.WaveRunning)
	f.seedRun(80, 70, execution.CaseRunRunning)
	notifier := &recordingWaveStageNotifier{}
	f.pause.stageNotifier = notifier

	cancelled, err := f.pause.CancelWave(context.Background(), 100, wave.ID, 7)
	if err != nil {
		t.Fatalf("CancelWave: %v", err)
	}
	if cancelled.Status != execution.WaveCancelled {
		t.Fatalf("wave status = %q, want cancelled", cancelled.Status)
	}
	calls := notifier.calls()
	if len(calls) != 1 || calls[0] != wave.ID {
		t.Fatalf("stage callback waves = %v, want [%d]", calls, wave.ID)
	}

	// Cancellation is idempotent; the duplicate call re-notifies (the stage
	// service is idempotent) so a lost stage advance can heal.
	if _, err := f.pause.CancelWave(context.Background(), 100, wave.ID, 7); err != nil {
		t.Fatalf("duplicate CancelWave: %v", err)
	}
	if calls := notifier.calls(); len(calls) != 2 {
		t.Fatalf("stage callback waves after duplicate cancel = %v, want two calls", calls)
	}
}

// TestCancelWaveCallbackFailureDoesNotRollBackCancellation pins the best-effort
// contract: the wave cancellation is the source of truth, so a failing stage
// callback is logged and never returned to the caller.
func TestCancelWaveCallbackFailureDoesNotRollBackCancellation(t *testing.T) {
	f := newTask9Fixture()
	f.seedIntent(70, 300)
	wave := f.seedWave(execution.WaveRunning)
	f.seedRun(80, 70, execution.CaseRunRunning)
	notifier := &recordingWaveStageNotifier{err: errors.New("stage service unavailable")}
	f.pause.stageNotifier = notifier

	cancelled, err := f.pause.CancelWave(context.Background(), 100, wave.ID, 7)
	if err != nil {
		t.Fatalf("CancelWave with failing stage callback: %v", err)
	}
	if cancelled.Status != execution.WaveCancelled {
		t.Fatalf("wave status = %q, want cancelled", cancelled.Status)
	}
	if len(notifier.calls()) != 1 {
		t.Fatalf("stage callback attempts = %v, want one", notifier.calls())
	}
	persisted, err := f.waves.Get(context.Background(), wave.ID)
	if err != nil || persisted.Status != execution.WaveCancelled {
		t.Fatalf("persisted wave = %+v err=%v, want cancelled", persisted, err)
	}
}
