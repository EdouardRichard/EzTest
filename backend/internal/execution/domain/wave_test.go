package execution

import "testing"

func TestWaveTransitions(t *testing.T) {
	valid := []struct{ from, to WaveStatus }{
		{WavePlanned, WaveRunning},
		{WavePlanned, WaveFailed},
		{WavePlanned, WaveCancelled},
		{WaveRunning, WavePaused},
		{WaveRunning, WaveDone},
		{WaveRunning, WaveFailed},
		{WaveRunning, WaveCancelled},
		{WavePaused, WaveRunning},
		{WavePaused, WaveCancelled},
	}
	for _, tc := range valid {
		if !tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be allowed", tc.from, tc.to)
		}
	}
	invalid := []struct{ from, to WaveStatus }{
		{WavePlanned, WavePaused},
		{WavePlanned, WaveDone},
		{WaveDone, WaveRunning},
		{WaveDone, WaveDone},
		{WaveFailed, WaveRunning},
		{WaveCancelled, WaveRunning},
		{WavePaused, WaveDone},
		{WavePaused, WavePaused},
	}
	for _, tc := range invalid {
		if tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be rejected", tc.from, tc.to)
		}
	}
}

func TestWaveActiveStatuses(t *testing.T) {
	for _, status := range []WaveStatus{WavePlanned, WaveRunning, WavePaused} {
		if !status.Active() {
			t.Errorf("%s must occupy the single active wave slot", status)
		}
		if status.Terminal() {
			t.Errorf("%s must not be terminal", status)
		}
	}
	for _, status := range []WaveStatus{WaveDone, WaveFailed, WaveCancelled} {
		if status.Active() {
			t.Errorf("%s must not be active", status)
		}
		if !status.Terminal() {
			t.Errorf("%s must be terminal", status)
		}
	}
	if WaveStatus("bogus").Valid() {
		t.Fatal("unknown wave status must be invalid")
	}
}

func TestWavePauseModeValid(t *testing.T) {
	if !ValidPauseMode(PauseModeGraceful) || !ValidPauseMode(PauseModeImmediate) {
		t.Fatal("graceful and immediate must be valid pause modes")
	}
	if !ValidPauseMode("") {
		t.Fatal("empty pause mode is the default and must be valid")
	}
	if ValidPauseMode("force") {
		t.Fatal("unknown pause mode must be invalid")
	}
}
