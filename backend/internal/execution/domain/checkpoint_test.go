package domain_test

import (
	"reflect"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func checkpointSteps(seqs ...int) []domain.StepResult {
	steps := make([]domain.StepResult, 0, len(seqs))
	for _, seq := range seqs {
		steps = append(steps, domain.StepResult{Seq: seq, Status: domain.StepStatusPassed})
	}
	return steps
}

func TestCheckpointMatrix(t *testing.T) {
	tests := []struct {
		name          string
		steps         []domain.StepResult
		wantCompleted []int
		wantNext      int
	}{
		{
			name: "no steps", steps: nil,
			wantCompleted: []int{}, wantNext: 1,
		},
		{
			name: "all contiguous completed", steps: checkpointSteps(1, 2, 3),
			wantCompleted: []int{1, 2, 3}, wantNext: 4,
		},
		{
			name: "interrupted step is not completed",
			steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusPassed},
				{Seq: 2, Status: domain.StepStatusInterrupted},
				{Seq: 3, Status: domain.StepStatusPassed},
			},
			wantCompleted: []int{1}, wantNext: 2,
		},
		{
			name: "gap stops the checkpoint",
			steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusPassed},
				{Seq: 3, Status: domain.StepStatusPassed},
			},
			wantCompleted: []int{1}, wantNext: 2,
		},
		{
			name: "failed step still counts as completed",
			steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusFailed},
				{Seq: 2, Status: domain.StepStatusPassed},
			},
			wantCompleted: []int{1, 2}, wantNext: 3,
		},
		{
			name: "pending step stops the checkpoint",
			steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusPassed},
				{Seq: 2, Status: domain.StepStatusPending},
				{Seq: 3, Status: domain.StepStatusPassed},
			},
			wantCompleted: []int{1}, wantNext: 2,
		},
		{
			name: "running step stops the checkpoint",
			steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusPassed},
				{Seq: 2, Status: domain.StepStatusRunning},
			},
			wantCompleted: []int{1}, wantNext: 2,
		},
		{
			name: "steps recorded out of order are sorted",
			steps: []domain.StepResult{
				{Seq: 2, Status: domain.StepStatusPassed},
				{Seq: 1, Status: domain.StepStatusPassed},
			},
			wantCompleted: []int{1, 2}, wantNext: 3,
		},
		{
			name: "first step interrupted", steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusInterrupted},
				{Seq: 2, Status: domain.StepStatusPassed},
			},
			wantCompleted: []int{}, wantNext: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completed, next := domain.Checkpoint(tt.steps)
			if !reflect.DeepEqual(completed, tt.wantCompleted) {
				t.Fatalf("Checkpoint completed = %v, want %v", completed, tt.wantCompleted)
			}
			if next != tt.wantNext {
				t.Fatalf("Checkpoint nextSeq = %d, want %d", next, tt.wantNext)
			}
		})
	}
}
