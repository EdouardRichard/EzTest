package agentapp

import (
	"context"
	"errors"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

type fakeRequirementFinalizer struct {
	calls []requirementFinalizeCall
	err   error
}

type requirementFinalizeCall struct {
	requirementID        int64
	requirementVersionID int64
}

func (f *fakeRequirementFinalizer) Finalize(_ context.Context, requirementID, requirementVersionID int64) error {
	f.calls = append(f.calls, requirementFinalizeCall{
		requirementID: requirementID, requirementVersionID: requirementVersionID,
	})
	return f.err
}

var _ RequirementFinalizer = (*fakeRequirementFinalizer)(nil)

func requirementFinalizerState(requirementID, requirementVersionID int64) *runState {
	return &runState{
		item: agent.WorkItem{Type: WorkTypeRequirementClose},
		payload: WorkItemPayload{
			RequirementID:        requirementID,
			RequirementVersionID: requirementVersionID,
		},
	}
}

func TestRequirementFinalizerInvokedForRequirementClose(t *testing.T) {
	finalizer := &fakeRequirementFinalizer{}
	runner := &Runner{requirementFinalizer: finalizer}
	if err := runner.finalizeRequirement(context.Background(), requirementFinalizerState(501, 700)); err != nil {
		t.Fatalf("finalizeRequirement: %v", err)
	}
	if len(finalizer.calls) != 1 || finalizer.calls[0].requirementID != 501 || finalizer.calls[0].requirementVersionID != 700 {
		t.Fatalf("calls = %+v", finalizer.calls)
	}
}

func TestRequirementFinalizerFailureIsRetryable(t *testing.T) {
	cause := errors.New("no closure yet")
	runner := &Runner{requirementFinalizer: &fakeRequirementFinalizer{err: cause}}
	err := runner.finalizeRequirement(context.Background(), requirementFinalizerState(501, 700))
	if err == nil {
		t.Fatal("finalizeRequirement = nil, want retryable error")
	}
	runErr, ok := err.(*RunError)
	if !ok || !runErr.Retryable || runErr.Reason != "requirement_finalize_failed" {
		t.Fatalf("error = %#v, want retryable requirement_finalize_failed", err)
	}
}

func TestRequirementFinalizerSkippedForOtherWorkTypes(t *testing.T) {
	finalizer := &fakeRequirementFinalizer{}
	runner := &Runner{requirementFinalizer: finalizer}
	state := requirementFinalizerState(501, 700)
	state.item.Type = WorkTypeIssueTriage
	if err := runner.finalizeRequirement(context.Background(), state); err != nil {
		t.Fatalf("finalizeRequirement: %v", err)
	}
	if len(finalizer.calls) != 0 {
		t.Fatalf("calls = %+v, want none", finalizer.calls)
	}
}

func TestRequirementFinalizerRequiresConfigurationAndPayload(t *testing.T) {
	runner := &Runner{}
	if err := runner.finalizeRequirement(context.Background(), requirementFinalizerState(501, 700)); err == nil {
		t.Fatal("nil finalizer = nil, want configuration error")
	}
	runner = &Runner{requirementFinalizer: &fakeRequirementFinalizer{}}
	if err := runner.finalizeRequirement(context.Background(), requirementFinalizerState(0, 700)); err == nil {
		t.Fatal("missing requirement ID = nil, want invalid payload")
	}
}
