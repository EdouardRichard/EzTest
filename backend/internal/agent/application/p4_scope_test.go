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

const p4TestFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// p4ScopeCaptureTool records the runtime-injected Scope for one tool call.
type p4ScopeCaptureTool struct {
	scope tools.Scope
	calls int
}

func (t *p4ScopeCaptureTool) Name() string        { return "p4.test.scope_capture" }
func (t *p4ScopeCaptureTool) Description() string { return "P4 scope injection test tool" }
func (t *p4ScopeCaptureTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *p4ScopeCaptureTool) Execute(_ context.Context, scope tools.Scope, _ json.RawMessage) (any, error) {
	t.scope = scope
	t.calls++
	return map[string]any{"ok": true}, nil
}

// p4ScopeCaptureRepo implements just enough of agent.RuntimeRepository for
// executeTools: it records the tool transcript row.
type p4ScopeCaptureRepo struct {
	agent.RuntimeRepository
	appended int
}

func (r *p4ScopeCaptureRepo) AppendMessage(_ context.Context, message agent.AgentMessage) (agent.AgentMessage, error) {
	r.appended++
	return message, nil
}

func captureExecuteToolsScope(t *testing.T, payload WorkItemPayload) tools.Scope {
	t.Helper()
	tool := &p4ScopeCaptureTool{}
	repo := &p4ScopeCaptureRepo{}
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
	return tool.scope
}

func TestExecuteToolsInjectsP4PayloadIntoScope(t *testing.T) {
	scope := captureExecuteToolsScope(t, WorkItemPayload{
		IssueID:              9201,
		ReportID:             9301,
		ReportRevision:       3,
		RequirementID:        3001,
		RequirementVersionID: 3101,
		CloseRound:           2,
		ReportRevisionID:     9401,
		PredicateFingerprint: p4TestFingerprint,
	})
	want := tools.Scope{
		VersionID:            42,
		AgentRunID:           4001,
		IssueID:              9201,
		ReportID:             9301,
		ReportRevision:       3,
		RequirementID:        3001,
		RequirementVersionID: 3101,
		CloseRound:           2,
		ReportRevisionID:     9401,
		PredicateFingerprint: p4TestFingerprint,
	}
	if !reflect.DeepEqual(scope, want) {
		t.Fatalf("scope = %#v, want %#v", scope, want)
	}
}

func TestExecuteToolsKeepsP3Scope(t *testing.T) {
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

func TestParseWorkItemPayloadP4Fields(t *testing.T) {
	raw := json.RawMessage(`{
		"issue_id": "9201",
		"report_id": 9301,
		"report_revision": "3",
		"requirement_id": "3001",
		"requirement_version_id": "3101",
		"close_round": 2,
		"report_revision_id": "9401",
		"predicate_fingerprint": "` + p4TestFingerprint + `"
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
		CloseRound:           2,
		ReportRevisionID:     9401,
		PredicateFingerprint: p4TestFingerprint,
	}
	if payload != want {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
}

// The frozen report task payloads spell the revision "revision"
// (contracts/task-types.md §6); the runtime keeps it as a compatibility alias
// for the canonical "report_revision" key.
func TestParseWorkItemPayloadP4RevisionAlias(t *testing.T) {
	payload, err := ParseWorkItemPayload(json.RawMessage(`{"report_id":"9301","revision":"4"}`))
	if err != nil {
		t.Fatalf("ParseWorkItemPayload: %v", err)
	}
	if payload.ReportID != 9301 || payload.ReportRevision != 4 {
		t.Fatalf("payload = %#v, want report_id=9301 revision=4", payload)
	}
}
