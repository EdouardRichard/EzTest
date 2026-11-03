package agentapp

// Task 61 fix round 1: the stored job_card must finalize with the same outcome
// the worker gives the work item. A hard-timeout run is classified
// RunError{Reason:"interrupted", Retryable:true}: below the attempt cap the
// worker re-opens the item and publishes job.progress{open}; at the cap the item
// is dead and it publishes job.finished{failed}. The card mapping must match, or
// a timed-out job keeps a persisted "running"/cancelable card after a reload
// while the live UI already shows open/failed. Only success was covered before.

import (
	"context"
	"fmt"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestJobCardTerminalStatus(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		attempts    int
		maxAttempts int
		wantStatus  string
		wantOK      bool
	}{
		{
			name:        "success is terminal done",
			attempts:    1,
			maxAttempts: 3,
			wantStatus:  "done",
			wantOK:      true,
		},
		{
			name:        "retryable failure below the cap reopens",
			err:         &RunError{Reason: "model_error", Retryable: true},
			attempts:    1,
			maxAttempts: 3,
			wantStatus:  "open",
			wantOK:      true,
		},
		{
			name:        "non-retryable failure is terminal failed",
			err:         &RunError{Reason: "model_error", Retryable: false},
			attempts:    1,
			maxAttempts: 3,
			wantStatus:  "failed",
			wantOK:      true,
		},
		{
			name:        "retryable failure at the cap is terminal failed",
			err:         &RunError{Reason: "tool_failure", Retryable: true},
			attempts:    3,
			maxAttempts: 3,
			wantStatus:  "failed",
			wantOK:      true,
		},
		{
			name:        "hard-timeout interruption below the cap reopens",
			err:         contextRunError(context.DeadlineExceeded),
			attempts:    1,
			maxAttempts: 3,
			wantStatus:  "open",
			wantOK:      true,
		},
		{
			name:        "interrupted at the cap is terminal failed",
			err:         &RunError{Reason: "interrupted", Retryable: true},
			attempts:    3,
			maxAttempts: 3,
			wantStatus:  "failed",
			wantOK:      true,
		},
		{
			name:        "aborted run owns no terminal card",
			err:         &RunError{Reason: "aborted", Retryable: false},
			attempts:    1,
			maxAttempts: 3,
			wantStatus:  "",
			wantOK:      false,
		},
		{
			name:        "lease-lost run owns no terminal card",
			err:         &RunError{Reason: "lease_lost", Retryable: false},
			attempts:    1,
			maxAttempts: 3,
			wantStatus:  "",
			wantOK:      false,
		},
		{
			name:        "fenced lease loss owns no terminal card",
			err:         fmt.Errorf("write case_run: %w", platform.LeaseLost()),
			attempts:    1,
			maxAttempts: 3,
			wantStatus:  "",
			wantOK:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := agent.WorkItem{Attempts: tc.attempts, MaxAttempts: tc.maxAttempts}
			status, ok := jobCardTerminalStatus(item, tc.err)
			if status != tc.wantStatus || ok != tc.wantOK {
				t.Fatalf("jobCardTerminalStatus(attempts=%d, max=%d, err=%v) = (%q, %v), want (%q, %v)",
					tc.attempts, tc.maxAttempts, tc.err, status, ok, tc.wantStatus, tc.wantOK)
			}
		})
	}
}
