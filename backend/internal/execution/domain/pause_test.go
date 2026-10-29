package domain_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestCanPause(t *testing.T) {
	tests := []struct {
		status domain.BatchStatus
		want   bool
	}{
		{domain.BatchStatusPlanned, true},
		{domain.BatchStatusRunning, true},
		{domain.BatchStatusDone, false},
		{domain.BatchStatusFailed, false},
		{domain.BatchStatusCancelled, false},
		{domain.BatchStatus("unknown"), false},
	}
	for _, tt := range tests {
		if got := domain.CanPause(tt.status); got != tt.want {
			t.Fatalf("CanPause(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestPauseCaseOutcomeMatrix(t *testing.T) {
	tests := []struct {
		name         string
		mode         domain.PauseMode
		stepInFlight bool
		wantStatus   domain.CaseRunStatus
		wantCleanup  bool
	}{
		{
			name: "graceful mid-step parks the run", mode: domain.PauseModeGraceful, stepInFlight: true,
			wantStatus: domain.CaseRunStatusPending, wantCleanup: false,
		},
		{
			name: "graceful between steps parks the run", mode: domain.PauseModeGraceful, stepInFlight: false,
			wantStatus: domain.CaseRunStatusPending, wantCleanup: false,
		},
		{
			name: "immediate mid-step interrupts and cleans up", mode: domain.PauseModeImmediate, stepInFlight: true,
			wantStatus: domain.CaseRunStatusInterrupted, wantCleanup: true,
		},
		{
			name: "immediate between steps still interrupts", mode: domain.PauseModeImmediate, stepInFlight: false,
			wantStatus: domain.CaseRunStatusInterrupted, wantCleanup: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, cleanup := domain.PauseCaseOutcome(tt.mode, tt.stepInFlight)
			if status != tt.wantStatus || cleanup != tt.wantCleanup {
				t.Fatalf("PauseCaseOutcome(%q, %v) = %s/%v, want %s/%v",
					tt.mode, tt.stepInFlight, status, cleanup, tt.wantStatus, tt.wantCleanup)
			}
			if tt.mode == domain.PauseModeGraceful && status == domain.CaseRunStatusInterrupted {
				t.Fatal("graceful pause must never interrupt a case run")
			}
		})
	}
}
