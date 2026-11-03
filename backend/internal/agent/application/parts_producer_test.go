package agentapp_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// assistantParts returns the parts the runner wrote with its last conversation
// message.
func assistantParts(t *testing.T, fixture *runnerFixture) []agentapp.ConversationPart {
	t.Helper()
	if len(fixture.conv.appended) == 0 {
		t.Fatal("runner wrote no assistant conversation message")
	}
	return fixture.conv.appended[len(fixture.conv.appended)-1].Parts
}

func partOfType(parts []agentapp.ConversationPart, partType string) (agentapp.ConversationPart, bool) {
	for _, part := range parts {
		if part.Type == partType {
			return part, true
		}
	}
	return agentapp.ConversationPart{}, false
}

func partTypes(parts []agentapp.ConversationPart) []string {
	types := make([]string, 0, len(parts))
	for _, part := range parts {
		types = append(types, part.Type)
	}
	return types
}

// TestRunnerAppendsThinkingToolParts pins the producer: a model turn with
// reasoning content and a tool call appends a thinking part (with its duration)
// and a tool_call part (with call id, status, arguments, result and duration)
// before the final text part.
func TestRunnerAppendsThinkingToolParts(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		{
			ReasoningContent: "先读取任务包，确认当前版本的范围。",
			ToolCalls: []llmapp.ToolCall{{
				ID: "call_1", Name: tools.ToolContextTaskPack, Arguments: `{}`,
			}},
		},
		{Content: "任务范围已经确认。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	parts := assistantParts(t, fixture)
	types := partTypes(parts)
	if len(types) != 3 || types[0] != "thinking" || types[1] != "tool_call" || types[2] != "text" {
		t.Fatalf("part types = %v, want [thinking tool_call text]", types)
	}

	thinking := parts[0]
	if thinking.Text != "先读取任务包，确认当前版本的范围。" {
		t.Fatalf("thinking text = %q", thinking.Text)
	}
	if duration, ok := thinking.Data["duration_ms"].(int64); !ok || duration < 0 {
		t.Fatalf("thinking duration = %#v", thinking.Data["duration_ms"])
	}

	call := parts[1]
	if call.Data["call_id"] != "call_1" || call.Data["name"] != tools.ToolContextTaskPack {
		t.Fatalf("tool call identity = %#v", call.Data)
	}
	if call.Data["status"] != "done" {
		t.Fatalf("tool call status = %#v, want done", call.Data["status"])
	}
	if _, ok := call.Data["result"]; !ok {
		t.Fatalf("tool call result missing: %#v", call.Data)
	}
	if duration, ok := call.Data["duration_ms"].(int64); !ok || duration < 0 {
		t.Fatalf("tool call duration = %#v", call.Data["duration_ms"])
	}
	if arguments, ok := call.Data["arguments"].(json.RawMessage); !ok ||
		strings.TrimSpace(string(arguments)) != "{}" {
		t.Fatalf("tool call arguments = %#v", call.Data["arguments"])
	}
	if !strings.Contains(parts[2].Text, "任务范围已经确认") {
		t.Fatalf("final text = %q", parts[2].Text)
	}
}

// TestRunnerAppendsJobCardPart pins the job_card producer for a background job
// that writes into the conversation: job_id is the work item id, kind is the
// contract job kind, progress starts at the completed turns and total=0 means
// unknown (no fabricated percentage).
func TestRunnerAppendsJobCardPart(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.conv.kind = "design"
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "本轮设计范围：1 个待设计需求。"}}
	item := fakeWorkItem(agentapp.WorkTypeDesignRound, agentapp.WorkItemPayload{
		ConversationID: 20, RoundID: 9001,
	})
	item.Kind = "design.cases"

	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("Run: %v", err)
	}
	parts := assistantParts(t, fixture)
	card, ok := partOfType(parts, "job_card")
	if !ok {
		t.Fatalf("job card part missing: %v", partTypes(parts))
	}
	jobID := strconv.FormatInt(item.ID, 10)
	if card.Data["job_id"] != jobID {
		t.Fatalf("job_id = %#v, want %s", card.Data["job_id"], jobID)
	}
	if card.Data["kind"] != "design.cases" {
		t.Fatalf("kind = %#v", card.Data["kind"])
	}
	if card.Data["status"] != "running" {
		t.Fatalf("status = %#v, want running while the run is still in flight", card.Data["status"])
	}
	if current, ok := card.Data["current"].(int64); !ok || current != 1 {
		t.Fatalf("current = %#v, want the completed turn count", card.Data["current"])
	}
	if total, ok := card.Data["total"].(int64); !ok || total != 0 {
		t.Fatalf("total = %#v, want 0 (unknown)", card.Data["total"])
	}
	if cancelable, ok := card.Data["cancelable"].(bool); !ok || !cancelable {
		t.Fatalf("cancelable = %#v, want true for a running job", card.Data["cancelable"])
	}
}

// TestRunnerAppendsToolErrorPart pins the error producer: a structured tool
// failure becomes a failed tool_call part plus an error part with the tool error
// code/message and no raw internal detail.
func TestRunnerAppendsToolErrorPart(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		{ToolCalls: []llmapp.ToolCall{{
			ID: "call_err", Name: tools.ToolContextTeamRoomRead, Arguments: `{"max_posts":999}`,
		}}},
		{Content: "参数不合法，已继续澄清。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	parts := assistantParts(t, fixture)
	call, ok := partOfType(parts, "tool_call")
	if !ok {
		t.Fatalf("tool call part missing: %v", partTypes(parts))
	}
	if call.Data["status"] != "failed" || call.Data["error"] == "" {
		t.Fatalf("failed tool call = %#v", call.Data)
	}
	errorPart, ok := partOfType(parts, "error")
	if !ok {
		t.Fatalf("error part missing: %v", partTypes(parts))
	}
	if errorPart.Data["code"] != tools.CodeInvalidArgument {
		t.Fatalf("error code = %#v", errorPart.Data["code"])
	}
	if message, ok := errorPart.Data["message"].(string); !ok || message == "" {
		t.Fatalf("error message = %#v", errorPart.Data["message"])
	}
	if retryable, ok := errorPart.Data["retryable"].(bool); !ok || retryable {
		t.Fatalf("error retryable = %#v, want false for a fix-the-arguments failure", errorPart.Data["retryable"])
	}
}

// TestRunnerAppendsPartsNoSecrets is the secret/arguments guard: tool arguments,
// tool results and reasoning text can carry credentials, so the produced parts
// must redact them and bound the payload instead of persisting raw secrets.
func TestRunnerAppendsPartsNoSecrets(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		{
			ReasoningContent: "账号口令是 password=hunter2，不能写进对话。",
			ToolCalls: []llmapp.ToolCall{{
				ID:        "call_secret",
				Name:      tools.ToolContextSummaryUpdate,
				Arguments: `{"summary":"登录口令是 password=hunter2","pinned_facts":["token=sk-secret-123"]}`,
			}},
		},
		{Content: "摘要已更新。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	parts := assistantParts(t, fixture)
	encoded, err := json.Marshal(parts)
	if err != nil {
		t.Fatalf("encode parts: %v", err)
	}
	for _, secret := range []string{"hunter2", "sk-secret-123"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("produced parts leak credential %q: %s", secret, encoded)
		}
	}
	call, ok := partOfType(parts, "tool_call")
	if !ok {
		t.Fatalf("tool call part missing: %v", partTypes(parts))
	}
	if arguments, ok := call.Data["arguments"].(json.RawMessage); ok && strings.Contains(string(arguments), "hunter2") {
		t.Fatalf("tool call arguments leak credential: %s", arguments)
	}
	if result, ok := call.Data["result"].(json.RawMessage); ok && strings.Contains(string(result), "hunter2") {
		t.Fatalf("tool call result leak credential: %s", result)
	}
	thinking, ok := partOfType(parts, "thinking")
	if !ok {
		t.Fatalf("thinking part missing: %v", partTypes(parts))
	}
	if strings.Contains(thinking.Text, "hunter2") {
		t.Fatalf("thinking text leaks credential: %q", thinking.Text)
	}
}
