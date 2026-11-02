package executionapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// maxWaitSecondsPerCall is the frozen execution.wait bound (spec §6.10).
const maxWaitSecondsPerCall = 300

// Wait implements execution.wait: an API-side, interruptible sleep that does not
// occupy an executor action slot. Page timers keep running in the browser. The
// wait counts against executable.case_timeout_seconds and is interrupted by
// pause/cancel/lease loss (the run context is canceled by CancelRun).
func (s *CaseRunner) Wait(ctx context.Context, scope tools.Scope, in tools.WaitInput) (any, error) {
	if in.Seconds < 1 || in.Seconds > maxWaitSecondsPerCall {
		return nil, platform.Invalid("seconds", "must be between 1 and 300")
	}
	if len([]rune(in.Reason)) > 200 {
		return nil, platform.Invalid("reason", "must be at most 200 characters")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	switch {
	case ctx.Err() != nil:
		return nil, waitInterruptedError(ctx)
	}

	caseTimeout := time.Duration(s.intSetting("execution.case_timeout_seconds", 900)) * time.Second
	now := s.now()
	s.mu.Lock()
	started, ok := s.waitStarted[caseRun.ID]
	if !ok || started.IsZero() {
		started = now
		s.waitStarted[caseRun.ID] = started
	}
	spent := s.waitTotals[caseRun.ID]
	requested := time.Duration(in.Seconds) * time.Second
	if spent+requested > caseTimeout || now.Sub(started) > caseTimeout {
		s.mu.Unlock()
		return nil, platform.StateConflict("execution.wait would exceed the case timeout")
	}
	s.waitTotals[caseRun.ID] = spent + requested
	s.mu.Unlock()

	deadline := now.Add(requested)
	timer := time.NewTimer(requested)
	defer timer.Stop()
	leaseTicker := time.NewTicker(s.leaseDuration() / 3)
	defer leaseTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, waitInterruptedError(ctx)
		case <-leaseTicker.C:
			s.renewLeaseQuietly(ctx, caseRun)
		case <-timer.C:
			s.renewLeaseQuietly(ctx, caseRun)
			return map[string]any{
				"waited_seconds": in.Seconds,
				"deadline_at":    deadline.UTC().Format(time.RFC3339),
			}, nil
		}
	}
}

// waitInterruptedError maps a canceled wait to the stable state_conflict code
// (pause/cancel/lease loss) or a timeout error when the run deadline expired.
func waitInterruptedError(ctx context.Context) error {
	if ctx.Err() == context.DeadlineExceeded {
		return platform.NewError(409, "timeout", "execution.wait was interrupted by the case timeout")
	}
	return platform.StateConflict("execution.wait was interrupted by pause, cancel or lease loss")
}
