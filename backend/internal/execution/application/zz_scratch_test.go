package executionapp_test

import (
	"context"
	"encoding/json"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestZZScratchPrepareFailureSummary(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	snapshot := caseRunSnapshot()
	snapshot.Steps[0].TargetVersionSUTID = 0
	snapshot.Steps[0].AccountRole = "anonymous"
	snapshot.Assertions[0].TargetVersionSUTID = 0
	fixture.cases.snapshot = snapshot

	_, err := fixture.runner.Prepare(fixture.runCtx, p3WorkItem())
	t.Logf("Prepare err = %v", err)

	// Mimic the worker run sink: a tool_failure before the task snapshot.
	runErr := &agentapp.RunError{Reason: "tool_failure", Retryable: false, Cause: err}
	finErr := fixture.runner.Finalize(context.Background(), 3001, 3, runErr, 1, 3)
	t.Logf("Finalize err = %v", finErr)
	stored := fixture.caseRuns.get(t, 3001)
	raw, _ := json.MarshalIndent(stored, "", "  ")
	t.Logf("stored = %s", raw)
	if stored.Status != execution.CaseRunFailed {
		t.Fatalf("status = %q", stored.Status)
	}
}
