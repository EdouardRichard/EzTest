package adapters

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

type fakeVisualAssertionLister struct {
	configs       []visual.Assertion
	err           error
	calls         int
	versionID     int64
	caseVersionID int64
}

func (f *fakeVisualAssertionLister) List(_ context.Context, versionID int64, caseVersionID, _ *int64) ([]visual.Assertion, error) {
	f.calls++
	f.versionID = versionID
	if caseVersionID != nil {
		f.caseVersionID = *caseVersionID
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.configs, nil
}

func TestAttachVisualAssertionsJoinsCaseAssertionsByPosition(t *testing.T) {
	task := agentapp.ExecutionTask{
		CaseVersionID: 41,
		Assertions: []agentapp.ExecutionAssertion{
			{StepSeq: 1, AssertionSeq: 1, Kind: "visual", Expected: "首页"},
			{StepSeq: 1, AssertionSeq: 2, Kind: "ui", Expected: "按钮"},
			{StepSeq: 2, AssertionSeq: 1, Kind: "visual", Expected: "归档断言"},
		},
	}
	lister := &fakeVisualAssertionLister{configs: []visual.Assertion{
		{
			ID: 6001, VersionID: 10, CaseVersionID: 41, CaseAssertionID: 9001,
			StepSeq: 1, AssertionSeq: 1, Status: visual.AssertionActive,
		},
		{
			ID: 6002, VersionID: 10, CaseVersionID: 41, CaseAssertionID: 9002,
			StepSeq: 2, AssertionSeq: 1, Status: visual.AssertionArchived,
		},
	}}
	provider := NewTaskProvider(nil, lister)

	provider.attachVisualAssertions(context.Background(), 10, 41, &task)

	if lister.calls != 1 || lister.versionID != 10 || lister.caseVersionID != 41 {
		t.Fatalf("List call = %d version=%d caseVersion=%d", lister.calls, lister.versionID, lister.caseVersionID)
	}
	first := task.Assertions[0]
	if first.VisualAssertionID != 6001 || first.CaseAssertionID != 9001 {
		t.Fatalf("visual assertion IDs = %d/%d, want 6001/9001", first.VisualAssertionID, first.CaseAssertionID)
	}
	if second := task.Assertions[1]; second.VisualAssertionID != 0 || second.CaseAssertionID != 0 {
		t.Fatalf("non-visual assertion got IDs %d/%d", second.VisualAssertionID, second.CaseAssertionID)
	}
	if third := task.Assertions[2]; third.VisualAssertionID != 0 || third.CaseAssertionID != 0 {
		t.Fatalf("archived configuration leaked into the task context: %d/%d", third.VisualAssertionID, third.CaseAssertionID)
	}
}

func TestAttachVisualAssertionsWithoutListerKeepsIDsEmpty(t *testing.T) {
	task := agentapp.ExecutionTask{Assertions: []agentapp.ExecutionAssertion{
		{StepSeq: 1, AssertionSeq: 1, Kind: "visual"},
	}}
	provider := NewTaskProvider(nil)
	provider.attachVisualAssertions(context.Background(), 10, 41, &task)
	if task.Assertions[0].VisualAssertionID != 0 || task.Assertions[0].CaseAssertionID != 0 {
		t.Fatalf("task without a visual lister got IDs: %+v", task.Assertions[0])
	}
}

func TestAttachVisualAssertionsListErrorIsBestEffort(t *testing.T) {
	task := agentapp.ExecutionTask{Assertions: []agentapp.ExecutionAssertion{
		{StepSeq: 1, AssertionSeq: 1, Kind: "visual"},
	}}
	lister := &fakeVisualAssertionLister{err: errors.New("visual context unavailable")}
	provider := NewTaskProvider(nil, lister)
	provider.attachVisualAssertions(context.Background(), 10, 41, &task)
	if task.Assertions[0].VisualAssertionID != 0 || task.Assertions[0].CaseAssertionID != 0 {
		t.Fatalf("failed visual List must leave IDs empty: %+v", task.Assertions[0])
	}
}

func TestAttachVisualAssertionsSkipsListWithoutVisualAssertions(t *testing.T) {
	task := agentapp.ExecutionTask{Assertions: []agentapp.ExecutionAssertion{
		{StepSeq: 1, AssertionSeq: 1, Kind: "ui"},
	}}
	lister := &fakeVisualAssertionLister{}
	provider := NewTaskProvider(nil, lister)
	provider.attachVisualAssertions(context.Background(), 10, 41, &task)
	if lister.calls != 0 {
		t.Fatalf("visual List calls = %d, want 0 for a case without visual assertions", lister.calls)
	}
}
