package agentapp_test

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestRunnerP3UnrunnableCaseIsBlocked pins the typed-blocker contract: when the
// execution task provider rejects a case because its scope cannot be executed
// (for example a step target that is not bound to the version), the run must be
// classified as a case blocker instead of an automation/tool failure. The
// execution context then persists blocked/blocked with the reason and evidence
// instead of a silent zero-result agent_failure.
func TestRunnerP3UnrunnableCaseIsBlocked(t *testing.T) {
	provider := &fakeTaskProvider{err: platform.StateConflict("step 1 targets unbound version_sut 0")}
	fixture := newRunnerFixtureWithTaskProvider(t, provider)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "unused"}}

	_, err := fixture.runner.Run(context.Background(), p3CaseItem())
	var runErr *agentapp.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("error %v is not a *RunError", err)
	}
	if runErr.Reason != "case_blocked" {
		t.Fatalf("run reason = %q, want case_blocked", runErr.Reason)
	}
	if runErr.Retryable {
		t.Fatalf("a terminal case binding conflict must not be retried")
	}
	if fixture.chat.requestCount() != 0 {
		t.Fatalf("model must not be called for an unrunnable case")
	}
	if failure := agentapp.Classify(err); failure.Retryable || !failure.UserVisible ||
		failure.Action != agentapp.FailureActionFixCaseBinding {
		t.Fatalf("classified failure = %+v, want a non-retryable actionable case blocker", failure)
	}
}
