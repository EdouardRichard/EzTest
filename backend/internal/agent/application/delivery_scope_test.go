package agentapp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// deliveryScopeCaptureTool records the runtime-injected Scope for one tool call.
type deliveryScopeCaptureTool struct {
	scope tools.Scope
	calls int
}

func (t *deliveryScopeCaptureTool) Name() string        { return "p4.test.scope_capture" }
func (t *deliveryScopeCaptureTool) Description() string { return "P4 scope injection test tool" }
func (t *deliveryScopeCaptureTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *deliveryScopeCaptureTool) Execute(_ context.Context, scope tools.Scope, _ json.RawMessage) (any, error) {
	t.scope = scope
	t.calls++
	return map[string]any{"ok": true}, nil
}

// deliveryScopeCaptureRepo implements just enough of agent.RuntimeRepository for
// executeTools: it records the tool transcript row and accepts the activity fact
// writes of the same loop (v0.2.4-D T10). Every other port method still panics
// through the embedded nil interface, so an unexpected read stays loud.
type deliveryScopeCaptureRepo struct {
	agent.RuntimeRepository
	appended   int
	activities int
}

func (r *deliveryScopeCaptureRepo) AppendMessage(_ context.Context, message agent.AgentMessage) (agent.AgentMessage, error) {
	r.appended++
	return message, nil
}

func (r *deliveryScopeCaptureRepo) InsertActivity(_ context.Context, activity agent.Activity) error {
	if err := activity.Validate(); err != nil {
		return err
	}
	r.activities++
	return nil
}

func captureExecuteToolsScope(t *testing.T, payload WorkItemPayload) tools.Scope {
	t.Helper()
	tool := &deliveryScopeCaptureTool{}
	repo := &deliveryScopeCaptureRepo{}
	runner := &Runner{repo: repo}
	state := &runState{
		item:     agent.WorkItem{ID: 1001, VersionID: 42},
		payload:  payload,
		profile:  agent.AgentProfile{Key: "p4.test", AllowedTools: []string{tool.Name()}},
		run:      agent.AgentRun{ID: 4001},
		registry: tools.NewRegistry(tool),
	}
	calls := []llmapp.ToolCall{{ID: "call_1", Name: tool.Name(), Arguments: `{}`}}
	if err := runner.executeTools(context.Background(), state, calls); err != nil {
		t.Fatalf("executeTools: %v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	if repo.appended != 1 {
		t.Fatalf("appended transcripts = %d, want 1", repo.appended)
	}
	// Q04 (v0.2.4-D T10): the same loop records the call's fact twice — start
	// and outcome — under one (run_id, seq).
	if repo.activities != 2 {
		t.Fatalf("activity fact writes = %d, want start+outcome (2)", repo.activities)
	}
	return tool.scope
}

func TestExecuteToolsInjectsDeliveryPayloadIntoScope(t *testing.T) {
	scope := captureExecuteToolsScope(t, WorkItemPayload{
		IssueID:              9201,
		ReportID:             9301,
		ReportRevision:       3,
		RequirementID:        3001,
		RequirementVersionID: 3101,
	})
	want := tools.Scope{
		VersionID:            42,
		AgentRunID:           4001,
		IssueID:              9201,
		ReportID:             9301,
		ReportRevision:       3,
		RequirementID:        3001,
		RequirementVersionID: 3101,
	}
	if !reflect.DeepEqual(scope, want) {
		t.Fatalf("scope = %#v, want %#v", scope, want)
	}
}

func TestExecuteToolsKeepsExecutionScope(t *testing.T) {
	scope := captureExecuteToolsScope(t, WorkItemPayload{
		ConversationID:       20,
		MessageID:            501,
		AttachmentID:         9001,
		RequirementID:        3001,
		RequirementVersionID: 3101,
		CaseID:               7001,
		CaseVersionID:        7101,
		RoundID:              8001,
		Trigger:              "user_message",
		IntentID:             8101,
		WaveID:               8201,
		CaseRunID:            8301,
		DispatchEpoch:        7,
		DispatchToken:        "token-1",
	})
	want := tools.Scope{
		VersionID:            42,
		AgentRunID:           4001,
		ConversationID:       20,
		RequirementID:        3001,
		RequirementVersionID: 3101,
		CaseID:               7001,
		CaseVersionID:        7101,
		RoundID:              8001,
		WaveID:               8201,
		CaseRunID:            8301,
		IntentID:             8101,
		DispatchEpoch:        7,
		DispatchToken:        "token-1",
	}
	if !reflect.DeepEqual(scope, want) {
		t.Fatalf("P3 scope = %#v, want %#v", scope, want)
	}
}

func TestParseWorkItemPayloadDeliveryFields(t *testing.T) {
	raw := json.RawMessage(`{
		"issue_id": "9201",
		"report_id": 9301,
		"report_revision": "3",
		"requirement_id": "3001",
		"requirement_version_id": "3101"
	}`)
	payload, err := ParseWorkItemPayload(raw)
	if err != nil {
		t.Fatalf("ParseWorkItemPayload: %v", err)
	}
	want := WorkItemPayload{
		IssueID:              9201,
		ReportID:             9301,
		ReportRevision:       3,
		RequirementID:        3001,
		RequirementVersionID: 3101,
	}
	// WorkItemPayload carries a []JudgeCaseRef slice (v0.2.4-E T14), so it is
	// no longer ==-comparable; reflect.DeepEqual keeps the same whole-struct
	// assertion (both JudgeCases stay nil for this non-judge payload).
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
}

// The frozen report task payloads spell the revision "revision"
// (contracts/task-types.md §6); the runtime keeps it as a compatibility alias
// for the canonical "report_revision" key.
func TestParseWorkItemPayloadDeliveryRevisionAlias(t *testing.T) {
	payload, err := ParseWorkItemPayload(json.RawMessage(`{"report_id":"9301","revision":"4"}`))
	if err != nil {
		t.Fatalf("ParseWorkItemPayload: %v", err)
	}
	if payload.ReportID != 9301 || payload.ReportRevision != 4 {
		t.Fatalf("payload = %#v, want report_id=9301 revision=4", payload)
	}
}
