package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application/agent_tool"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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

// TestAttachVisualAssertionsJoinsCaseAssertionsByPosition keeps the positional
// fallback for execution task assertions that do not carry the design
// case_assertion.id: the active configuration at the matching (step_seq,
// assertion_seq) supplies the visual_assertion_id, and archived configurations
// never leak in.
func TestAttachVisualAssertionsJoinsCaseAssertionsByPosition(t *testing.T) {
	task := agentapp.ExecutionTask{
		CaseVersionID: 41,
		Assertions: []agentapp.ExecutionAssertion{
			{StepSeq: 1, AssertionSeq: 1, Kind: "visual", Expected: "首页"},
			{StepSeq: 1, AssertionSeq: 2, Kind: "ui", Expected: "按钮"},
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

	if err := provider.attachVisualAssertions(context.Background(), 10, 41, &task); err != nil {
		t.Fatalf("attachVisualAssertions: %v", err)
	}

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
}

// recordingVisualAssertPort is the execution.visual_assert boundary: it records
// the assertion ID the real tool maps into the execution service.
type recordingVisualAssertPort struct {
	calls          int
	gotAssertionID int64
	gotScope       tools.Scope
	result         any
}

func (f *recordingVisualAssertPort) VisualAssert(_ context.Context, scope tools.Scope, assertionID int64) (any, error) {
	f.calls++
	f.gotScope = scope
	f.gotAssertionID = assertionID
	return f.result, nil
}

// TestAttachVisualAssertionsWithoutVisualAssertionsNeedsNoResolver keeps
// non-visual execution independent of the visual context: a case version with
// no kind=visual assertion must not fail and must not query the lister.
func TestAttachVisualAssertionsWithoutVisualAssertionsNeedsNoResolver(t *testing.T) {
	task := agentapp.ExecutionTask{Assertions: []agentapp.ExecutionAssertion{
		{StepSeq: 1, AssertionSeq: 1, Kind: "ui"},
	}}
	lister := &fakeVisualAssertionLister{}
	provider := NewTaskProvider(nil, lister)
	if err := provider.attachVisualAssertions(context.Background(), 10, 41, &task); err != nil {
		t.Fatalf("attachVisualAssertions: %v", err)
	}
	if lister.calls != 0 {
		t.Fatalf("visual List calls = %d, want 0 for a case without visual assertions", lister.calls)
	}
	if providerWithoutLister := NewTaskProvider(nil); providerWithoutLister.attachVisualAssertions(context.Background(), 10, 41, &task) != nil {
		t.Fatal("a non-visual case must not require a visual assertion resolver")
	}
}

// TestVisualAssertionResolvesAndExecutes pins the whole Task 115 mapping: the
// design case_assertion.id travels through the execution snapshot into the agent
// task context, the task provider attaches the resolved visual_assertion.id, and
// that exact ID becomes the execution.visual_assert tool input that runs the
// comparison and returns pass/fail.
func TestVisualAssertionResolvesAndExecutes(t *testing.T) {
	snapshot := execution.TaskSnapshot{
		CaseRun: execution.CaseRun{ID: 55, WaveID: 2, IntentID: 3},
		Case: execution.CaseSnapshot{
			CaseVersionID: 41, Title: "视觉用例",
			Assertions: []execution.CaseAssertion{
				{CaseAssertionID: 9001, StepSeq: 1, AssertionSeq: 1, Kind: "visual", Expected: "首页与基线一致", TargetVersionSUTID: 7},
				{CaseAssertionID: 9002, StepSeq: 1, AssertionSeq: 2, Kind: "ui", Expected: "按钮可见"},
			},
		},
	}
	task := mapTaskSnapshot(snapshot)
	if task.Assertions[0].CaseAssertionID != 9001 {
		t.Fatalf("mapTaskSnapshot dropped the design case_assertion.id: %+v", task.Assertions[0])
	}
	lister := &fakeVisualAssertionLister{configs: []visual.Assertion{{
		ID: 6001, VersionID: 10, CaseVersionID: 41, CaseAssertionID: 9001,
		StepSeq: 1, AssertionSeq: 1, Status: visual.AssertionActive,
	}}}
	provider := NewTaskProvider(nil, lister)

	if err := provider.attachVisualAssertions(context.Background(), 10, 41, &task); err != nil {
		t.Fatalf("attachVisualAssertions: %v", err)
	}
	resolved := task.Assertions[0]
	if resolved.VisualAssertionID != 6001 || resolved.CaseAssertionID != 9001 {
		t.Fatalf("resolved IDs = %d/%d, want visual=6001 case=9001",
			resolved.VisualAssertionID, resolved.CaseAssertionID)
	}
	if task.Assertions[1].VisualAssertionID != 0 {
		t.Fatalf("non-visual assertion got visual_assertion_id %d", task.Assertions[1].VisualAssertionID)
	}

	port := &recordingVisualAssertPort{result: map[string]any{
		"verdict": "passed", "classification": "passed", "assertion_result_id": "1001",
	}}
	tool := agenttool.NewVisualAssertTool(port)
	raw, err := json.Marshal(map[string]string{
		"assertionId": strconv.FormatInt(resolved.VisualAssertionID, 10),
	})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	out, err := tool.Execute(context.Background(), tools.Scope{VersionID: 10, CaseRunID: 55}, raw)
	if err != nil {
		t.Fatalf("execution.visual_assert: %v", err)
	}
	if port.calls != 1 || port.gotAssertionID != 6001 || port.gotScope.CaseRunID != 55 {
		t.Fatalf("tool call = %d assertion=%d scope=%+v", port.calls, port.gotAssertionID, port.gotScope)
	}
	result, ok := out.(map[string]any)
	if !ok || result["verdict"] != "passed" || result["classification"] != "passed" {
		t.Fatalf("visual assert result = %#v", out)
	}
}

// TestUnresolvableVisualAssertionFailsLoudly pins the Task 115 pre-execution
// rule: a visual assertion that cannot be mapped to an active visual_assertion
// configuration is a typed failure before the agent loop, not a silently empty
// ID that later aggregates the case to inconclusive.
func TestUnresolvableVisualAssertionFailsLoudly(t *testing.T) {
	newTask := func() agentapp.ExecutionTask {
		return agentapp.ExecutionTask{
			CaseVersionID: 41,
			Assertions: []agentapp.ExecutionAssertion{
				{StepSeq: 1, AssertionSeq: 1, Kind: "visual", CaseAssertionID: 9001, Expected: "首页"},
			},
		}
	}
	positioned := func(id, caseAssertionID int64, stepSeq, assertionSeq int, status visual.AssertionStatus) []visual.Assertion {
		return []visual.Assertion{{
			ID: id, VersionID: 10, CaseVersionID: 41, CaseAssertionID: caseAssertionID,
			StepSeq: stepSeq, AssertionSeq: assertionSeq, Status: status,
		}}
	}

	tests := []struct {
		name            string
		lister          *fakeVisualAssertionLister
		withoutResolver bool
		wantStatus      int
		wantCode        string
	}{
		{
			name:       "missing configuration",
			lister:     &fakeVisualAssertionLister{},
			wantStatus: http.StatusConflict,
			wantCode:   "visual_assertion_unresolved",
		},
		{
			name:       "archived configuration",
			lister:     &fakeVisualAssertionLister{configs: positioned(6001, 9001, 1, 1, visual.AssertionArchived)},
			wantStatus: http.StatusConflict,
			wantCode:   "visual_assertion_unresolved",
		},
		{
			name:       "position mismatch",
			lister:     &fakeVisualAssertionLister{configs: positioned(6001, 9001, 2, 1, visual.AssertionActive)},
			wantStatus: http.StatusConflict,
			wantCode:   "visual_assertion_unresolved",
		},
		{
			name:       "design assertion mismatch",
			lister:     &fakeVisualAssertionLister{configs: positioned(6001, 9999, 1, 1, visual.AssertionActive)},
			wantStatus: http.StatusConflict,
			wantCode:   "visual_assertion_unresolved",
		},
		{
			name:            "resolver not configured",
			withoutResolver: true,
			wantStatus:      http.StatusConflict,
			wantCode:        "visual_assertion_unresolved",
		},
		{
			name:       "resolution unavailable",
			lister:     &fakeVisualAssertionLister{err: platform.Unavailable("visual store down")},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var provider *TaskProviderAdapter
			if test.withoutResolver {
				provider = NewTaskProvider(nil)
			} else {
				provider = NewTaskProvider(nil, test.lister)
			}
			task := newTask()
			err := provider.attachVisualAssertions(context.Background(), 10, 41, &task)
			if err == nil {
				t.Fatalf("attachVisualAssertions accepted an unresolvable visual assertion: %+v", task.Assertions[0])
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want a typed *platform.Error", err)
			}
			if apiErr.Status != test.wantStatus || apiErr.Code != test.wantCode {
				t.Fatalf("error = %d/%s, want %d/%s", apiErr.Status, apiErr.Code, test.wantStatus, test.wantCode)
			}
			if task.Assertions[0].VisualAssertionID != 0 {
				t.Fatalf("fail-loud path fabricated a visual_assertion_id: %+v", task.Assertions[0])
			}
			if test.wantStatus == http.StatusConflict {
				if !strings.Contains(apiErr.Message, "step 1 assertion 1") {
					t.Fatalf("message = %q, want the unresolved assertion position", apiErr.Message)
				}
				if next, _ := apiErr.Details["next_action"].(string); next == "" {
					t.Fatalf("details = %#v, want a repair next_action", apiErr.Details)
				}
			}
		})
	}
}
