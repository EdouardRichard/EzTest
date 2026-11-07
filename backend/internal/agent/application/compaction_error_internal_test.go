package agentapp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestCompactionRunErrorClassification pins the fix-round-2 error taxonomy of a
// failed compaction transaction: only the R-A5-11 unrecoverable overflow is the
// terminal context_overflow_unrecoverable reason; a canceled run is aborted, a
// prefix-cache violation or unexpected transaction failure is retried as a tool
// failure, and an invalid policy/decision is a terminal payload error.
func TestCompactionRunErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		reason    string
		retryable bool
	}{
		{
			name:   "canceled run is an abort",
			err:    fmt.Errorf("finish: %w", context.Canceled),
			reason: "aborted",
		},
		{
			name:      "deadline is an interruption",
			err:       context.DeadlineExceeded,
			reason:    "interrupted",
			retryable: true,
		},
		{
			name:      "prefix-cache violation is retryable",
			err:       fmt.Errorf("assemble: %w", agentcontext.ErrPrefixChanged),
			reason:    "tool_failure",
			retryable: true,
		},
		{
			name:   "invalid compaction policy is a terminal payload error",
			err:    fmt.Errorf("start: %w", agentcontext.ErrInvalidCompactionPolicy),
			reason: "invalid_payload",
		},
		{
			name:   "invalid decision is a terminal payload error",
			err:    platform.Invalid("trigger", "must be one of pressure, overflow, stage_boundary"),
			reason: "invalid_payload",
		},
		{
			name: "unrecoverable overflow keeps its terminal reason",
			err: &agentcontext.ContextOverflowError{
				Policy:            agentcontext.NewCompactionPolicy(128000, 4096),
				PromptTokens:      200000,
				LargestUnitTokens: 200000,
			},
			reason: "context_overflow_unrecoverable",
		},
		{
			name:      "unexpected transaction failure is retryable",
			err:       errors.New("compaction manager unavailable"),
			reason:    "tool_failure",
			retryable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var runErr *RunError
			if err := compactionRunError(tc.err); !errors.As(err, &runErr) {
				t.Fatalf("compactionRunError(%v) = %T, want *RunError", tc.err, err)
			}
			if runErr.Reason != tc.reason || runErr.Retryable != tc.retryable {
				t.Fatalf("compactionRunError(%v) = %q retryable=%v, want %q retryable=%v",
					tc.err, runErr.Reason, runErr.Retryable, tc.reason, tc.retryable)
			}
			if !errors.Is(runErr, tc.err) {
				t.Fatalf("classification lost the cause: %v", runErr)
			}
		})
	}
	if compactionRunError(nil) != nil {
		t.Fatal("compactionRunError(nil) must be nil")
	}
}
