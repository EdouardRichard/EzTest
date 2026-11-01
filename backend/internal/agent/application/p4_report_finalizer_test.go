package agentapp

import (
	"context"
	"errors"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

type fakeReportFinalizer struct {
	calls []finalizeCall
	err   error
}

type finalizeCall struct {
	reportID int64
	revision int32
}

func (f *fakeReportFinalizer) Finalize(_ context.Context, reportID int64, revision int32) error {
	f.calls = append(f.calls, finalizeCall{reportID: reportID, revision: revision})
	return f.err
}

var _ ReportFinalizer = (*fakeReportFinalizer)(nil)

func reportFinalizerState(workType string, reportID int64, revision int32) *runState {
	return &runState{
		item: agent.WorkItem{Type: workType},
		payload: WorkItemPayload{
			ReportID:       reportID,
			ReportRevision: revision,
		},
	}
}

func TestReportFinalizerInvokedForReportRuns(t *testing.T) {
	finalizer := &fakeReportFinalizer{}
	runner := &Runner{reportFinalizer: finalizer}

	for _, workType := range []string{WorkTypeReportWave, WorkTypeReportVersion} {
		finalizer.calls = nil
		state := reportFinalizerState(workType, 910, 2)
		if err := runner.finalizeReport(context.Background(), state); err != nil {
			t.Fatalf("finalizeReport(%s): %v", workType, err)
		}
		if len(finalizer.calls) != 1 || finalizer.calls[0] != (finalizeCall{reportID: 910, revision: 2}) {
			t.Fatalf("finalizeReport(%s) calls = %+v", workType, finalizer.calls)
		}
	}
}

func TestReportFinalizerFailureIsRetryable(t *testing.T) {
	cause := errors.New("report revision is not published")
	runner := &Runner{reportFinalizer: &fakeReportFinalizer{err: cause}}
	state := reportFinalizerState(WorkTypeReportWave, 910, 2)

	err := runner.finalizeReport(context.Background(), state)
	if err == nil {
		t.Fatal("unpublished report finalizer returned nil")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("finalizer error = %v, want *RunError", err)
	}
	if !runErr.Retryable {
		t.Fatalf("finalizer failure must be retryable: %+v", runErr)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("finalizer error lost its cause: %v", err)
	}
}

func TestReportFinalizerSkippedForOtherWorkTypes(t *testing.T) {
	finalizer := &fakeReportFinalizer{}
	runner := &Runner{reportFinalizer: finalizer}
	state := reportFinalizerState(WorkTypeChatTurn, 910, 2)

	if err := runner.finalizeReport(context.Background(), state); err != nil {
		t.Fatalf("finalizeReport(chat.turn): %v", err)
	}
	if len(finalizer.calls) != 0 {
		t.Fatalf("finalizer ran for a non-report task: %+v", finalizer.calls)
	}
}

func TestReportFinalizerRequiresConfigurationAndPayload(t *testing.T) {
	state := reportFinalizerState(WorkTypeReportWave, 910, 2)
	if err := (&Runner{}).finalizeReport(context.Background(), state); err == nil {
		t.Fatal("report run without a finalizer returned nil")
	}

	finalizer := &fakeReportFinalizer{}
	runner := &Runner{reportFinalizer: finalizer}
	if err := runner.finalizeReport(context.Background(), reportFinalizerState(WorkTypeReportWave, 0, 2)); err == nil {
		t.Fatal("finalizeReport without report_id returned nil")
	}
	if err := runner.finalizeReport(context.Background(), reportFinalizerState(WorkTypeReportVersion, 910, 0)); err == nil {
		t.Fatal("finalizeReport without revision returned nil")
	}
	if len(finalizer.calls) != 0 {
		t.Fatalf("finalizer ran with an empty payload: %+v", finalizer.calls)
	}
}

func TestP4AgentProfilesDoNotRequireChatFinalMessage(t *testing.T) {
	// The P4 issue/report run outputs are the analysis row and the published
	// report revision; an empty assistant message must not fail the run before
	// the finalizer hook can run.
	for _, profileKey := range []string{ProfileKeyIssueTriager, ProfileKeyReportWriter} {
		state := &runState{profile: agent.AgentProfile{Key: profileKey}}
		if err := (&Runner{}).writeOutputs(context.Background(), state, ""); err != nil {
			t.Fatalf("writeOutputs(%s) error = %v", profileKey, err)
		}
	}
}

func TestReportFinalizerClassifiesAsRetryableFailure(t *testing.T) {
	cause := errors.New("report revision is not published")
	runner := &Runner{reportFinalizer: &fakeReportFinalizer{err: cause}}
	_, retryable := classifyFailure(context.Background(), runner.finalizeReport(context.Background(), reportFinalizerState(WorkTypeReportWave, 910, 2)))
	if !retryable {
		t.Fatal("classifyFailure did not mark the report finalizer failure retryable")
	}
}
