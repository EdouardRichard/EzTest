package adapters

import (
	"context"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// RunSinkAdapter implements agentapp.RunSink: after an execute.case agent run it
// calls CaseRunner.Finalize so a retryable failure redispatches and a terminal
// run gets the deterministic aggregated verdict. Non-execution work items are
// ignored.
type RunSinkAdapter struct {
	runner *executionapp.CaseRunner
}

// NewRunSink wires the worker run sink.
func NewRunSink(runner *executionapp.CaseRunner) *RunSinkAdapter {
	return &RunSinkAdapter{runner: runner}
}

var _ agentapp.RunSink = (*RunSinkAdapter)(nil)

// OnRunFinished finalizes the case_run of one finished agent run.
func (s *RunSinkAdapter) OnRunFinished(ctx context.Context, item agent.WorkItem, result agentapp.RunResult, runErr error) error {
	if s.runner == nil {
		return errNotConfigured("case runner")
	}
	payload, err := agentapp.ParseWorkItemPayload(item.Payload)
	if err != nil {
		// Not an execution work item we can finalize; the worker already
		// classified the run failure itself.
		return nil
	}
	if payload.CaseRunID <= 0 {
		return nil
	}
	attempt := item.Attempts
	if attempt < 1 {
		attempt = 1
	}
	maxAttempts := item.MaxAttempts
	return s.runner.Finalize(ctx, payload.CaseRunID, runErr, attempt, maxAttempts)
}
