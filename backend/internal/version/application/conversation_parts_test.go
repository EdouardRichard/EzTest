package versionapp_test

import (
	"encoding/json"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- Task 53 helpers ----------

func newChatConversation(t *testing.T, env *conversationEnv) versionapp.ConversationView {
	t.Helper()
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Parts chat", 42)
	if err != nil {
		t.Fatalf("Create conversation: %v", err)
	}
	return conversation
}

func findConversationMessageEvent(t *testing.T, env *conversationEnv, messageID int64) eventingapp.Event {
	t.Helper()
	want := idString(messageID)
	for _, event := range env.events.events {
		if event.Type != "conversation.message" {
			continue
		}
		if event.Payload["id"] == want {
			return event
		}
	}
	t.Fatalf("conversation.message event for message %s missing: %#v", want, env.events.events)
	return eventingapp.Event{}
}

func decodedStreamParts(t *testing.T, rows []versionapp.MessagePartView) []map[string]any {
	t.Helper()
	parts := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var payload map[string]any
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			t.Fatalf("decode part payload %s: %v", row.Payload, err)
		}
		parts = append(parts, payload)
	}
	return parts
}

func eventParts(t *testing.T, event eventingapp.Event) []map[string]any {
	t.Helper()
	raw, ok := event.Payload["parts"].([]any)
	if !ok {
		t.Fatalf("event parts = %#v", event.Payload["parts"])
	}
	parts := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		part, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("event part = %#v", item)
		}
		parts = append(parts, part)
	}
	return parts
}

// TestConversationPartWriterSeq pins the conversation_message_part writer: the
// assistant message and its streamed rows are written as one aggregate, the row
// seq starts at 1 and increases monotonically in part order, the row payload is
// the contract part shape, and a duplicate (message_id, seq) is rejected
// atomically (the DB unique index uk_conversation_message_part_message_seq).
func TestConversationPartWriterSeq(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)

	parts := []version.MessagePart{
		{Type: "thinking", Text: "核对退款时效", DurationMS: 1200},
		{
			Type:       "tool_call",
			CallID:     "call_1",
			Name:       "context.task_pack",
			Status:     "done",
			Arguments:  json.RawMessage(`{"query":"退款"}`),
			Result:     json.RawMessage(`{"ok":true}`),
			DurationMS: 42,
		},
		{Type: "text", Text: "我已核对退款规则。"},
	}
	message, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID, parts, 555)
	if err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}

	rows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, message.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(rows) != len(parts) {
		t.Fatalf("stream rows = %#v, want %d rows", rows, len(parts))
	}
	for index, row := range rows {
		if row.Seq != index+1 {
			t.Fatalf("rows[%d].seq = %d, want %d (monotonic from 1)", index, row.Seq, index+1)
		}
		if row.Type != parts[index].Type {
			t.Fatalf("rows[%d].type = %q, want %q", index, row.Type, parts[index].Type)
		}
	}
	payloads := decodedStreamParts(t, rows)
	if payloads[0]["type"] != "thinking" || payloads[0]["text"] != "核对退款时效" || payloads[0]["duration_ms"] != float64(1200) {
		t.Fatalf("thinking payload = %#v", payloads[0])
	}
	if payloads[1]["call_id"] != "call_1" || payloads[1]["name"] != "context.task_pack" ||
		payloads[1]["status"] != "done" || payloads[1]["duration_ms"] != float64(42) {
		t.Fatalf("tool_call payload = %#v", payloads[1])
	}
	if arguments, ok := payloads[1]["arguments"].(map[string]any); !ok || arguments["query"] != "退款" {
		t.Fatalf("tool_call arguments = %#v", payloads[1]["arguments"])
	}
	if result, ok := payloads[1]["result"].(map[string]any); !ok || result["ok"] != true {
		t.Fatalf("tool_call result = %#v", payloads[1]["result"])
	}
	if payloads[2]["text"] != "我已核对退款规则。" {
		t.Fatalf("text payload = %#v", payloads[2])
	}

	// Every message owns its own 1-based seq sequence: a second assistant
	// message starts again at 1 and does not inherit the first one's counter.
	second, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID,
		[]version.MessagePart{{Type: "text", Text: "补充说明。"}}, 556)
	if err != nil {
		t.Fatalf("second AppendAssistantMessage: %v", err)
	}
	secondRows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, second.ID)
	if err != nil || len(secondRows) != 1 || secondRows[0].Seq != 1 {
		t.Fatalf("second message rows = %#v, %v", secondRows, err)
	}

	// The writer contract is unique (message_id, seq) inside one transaction:
	// a conflicting batch must leave neither the head nor any part row behind.
	conflicting := version.Message{
		ID:             990001,
		ConversationID: conversation.ID,
		Role:           version.MessageRoleAssistant,
		Parts:          []version.MessagePart{{Type: "text", Text: "conflict"}},
		StreamParts: []version.ConversationMessagePart{
			{Seq: 1, Type: "text", Payload: json.RawMessage(`{"type":"text","text":"a"}`), CreatedAt: testTime},
			{Seq: 1, Type: "text", Payload: json.RawMessage(`{"type":"text","text":"b"}`), CreatedAt: testTime},
		},
		Refs:       []version.MessageRef{},
		TokenUsage: map[string]any{},
		CreatedAt:  testTime,
	}
	if err := env.repo.AppendAssistantMessage(requestContext(), conversation.ID, conflicting); err == nil {
		t.Fatal("duplicate (message_id, seq) was accepted")
	}
	if _, ok := env.repo.messageParts[conflicting.ID]; ok {
		t.Fatalf("conflicting parts persisted: %#v", env.repo.messageParts[conflicting.ID])
	}
	for _, stored := range env.repo.messages[conversation.ID] {
		if stored.ID == conflicting.ID {
			t.Fatalf("conflicting message head persisted: %#v", stored)
		}
	}
}

// TestPartsSSE pins the SSE/history consistency rule: conversation.message
// carries the same parts that GET .../parts returns, annotated with the row's
// 1-based seq; parts that are not part of the streaming enum (rollback_proposal)
// stay snapshot-only and are never written to the part table.
func TestPartsSSE(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)

	parts := []version.MessagePart{
		{Type: "thinking", Text: "先确认约束", DurationMS: 900},
		{Type: "tool_call", CallID: "call_9", Name: "context.version_pack", Status: "failed",
			Error: "工具超时", DurationMS: 3000},
		{Type: "error", Code: "tool_failure", Message: "工具超时，已回退", Retryable: true, Action: "重试"},
		{Type: "text", Text: "已回退到只读上下文。"},
	}
	message, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID, parts, 700)
	if err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	rows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, message.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	history := decodedStreamParts(t, rows)

	event := findConversationMessageEvent(t, env, message.ID)
	if event.Channel != "version:"+idString(env.versionID) {
		t.Fatalf("event channel = %q", event.Channel)
	}
	if event.Payload["role"] != "assistant" || event.Payload["conversation_id"] != idString(conversation.ID) {
		t.Fatalf("event payload = %#v", event.Payload)
	}
	wire := eventParts(t, event)
	if len(wire) != len(history) {
		t.Fatalf("SSE parts = %#v, history = %#v", wire, history)
	}
	for index, part := range wire {
		seq, ok := part["seq"].(int)
		if !ok || seq != index+1 {
			t.Fatalf("SSE parts[%d].seq = %#v, want %d", index, part["seq"], index+1)
		}
		if part["type"] != history[index]["type"] {
			t.Fatalf("SSE parts[%d].type = %#v, history = %#v", index, part["type"], history[index]["type"])
		}
		for _, field := range []string{"text", "duration_ms", "call_id", "name", "status", "error", "code", "message", "retryable", "action"} {
			if history[index][field] != nil && part[field] != history[index][field] {
				t.Fatalf("SSE parts[%d][%q] = %#v, history = %#v", index, field, part[field], history[index][field])
			}
		}
	}

	// A snapshot-only part (not in the streaming enum) reaches the SSE message
	// but is not written to conversation_message_part.
	rollback, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID,
		[]version.MessagePart{{Type: "rollback_proposal", RequirementID: 3001, Reason: "范围变化"}}, 701)
	if err != nil {
		t.Fatalf("rollback AppendAssistantMessage: %v", err)
	}
	rollbackRows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, rollback.ID)
	if err != nil || len(rollbackRows) != 0 {
		t.Fatalf("rollback stream rows = %#v, %v", rollbackRows, err)
	}
	rollbackWire := eventParts(t, findConversationMessageEvent(t, env, rollback.ID))
	if len(rollbackWire) != 1 || rollbackWire[0]["type"] != "rollback_proposal" {
		t.Fatalf("rollback SSE parts = %#v", rollbackWire)
	}
	if _, ok := rollbackWire[0]["seq"]; ok {
		t.Fatalf("non-streaming part carried a seq: %#v", rollbackWire[0])
	}
}

// TestConversationPartValidatorContract pins the Go validator to the eight
// conversation_message_part contract types and their required payload fields.
func TestConversationPartValidatorContract(t *testing.T) {
	valid := []version.MessagePart{
		{Type: "text", Text: "hello"},
		{Type: "thinking", Text: "思考内容", DurationMS: 1000},
		{Type: "tool_call", CallID: "call_1", Name: "context.task_pack", Status: "running",
			Arguments: json.RawMessage(`{"a":1}`), Result: json.RawMessage(`null`)},
		{Type: "job_card", JobID: 501, JobKind: "design.cases", Status: "running", Current: 12, Total: 27},
		{Type: "attachment", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "a.md", MIME: "text/markdown", Size: 10},
		{Type: "requirement_card", RequirementID: 3001, Title: "Coupons", Status: "draft", Revision: 1},
		{Type: "case_card", CaseID: 4001, CaseVersionID: 4002, Title: "Login", Status: "confirmed"},
		{Type: "error", Code: "model_error", Message: "模型调用失败", Retryable: true, Action: "重试"},
	}
	for _, part := range valid {
		if err := version.ValidateAssistantMessageParts([]version.MessagePart{part}); err != nil {
			t.Fatalf("contract part %q rejected: %v", part.Type, err)
		}
	}

	cancelable := true
	invalid := []struct {
		name string
		part version.MessagePart
	}{
		{"text empty", version.MessagePart{Type: "text"}},
		{"thinking empty", version.MessagePart{Type: "thinking"}},
		{"thinking negative duration", version.MessagePart{Type: "thinking", Text: "x", DurationMS: -1}},
		{"tool call no call id", version.MessagePart{Type: "tool_call", Name: "x", Status: "done"}},
		{"tool call no name", version.MessagePart{Type: "tool_call", CallID: "c", Status: "done"}},
		{"tool call bad status", version.MessagePart{Type: "tool_call", CallID: "c", Name: "x", Status: "bogus"}},
		{"tool call negative duration", version.MessagePart{Type: "tool_call", CallID: "c", Name: "x", Status: "done", DurationMS: -1}},
		{"job card no job id", version.MessagePart{Type: "job_card", JobKind: "design.cases", Status: "running"}},
		{"job card bad kind", version.MessagePart{Type: "job_card", JobID: 1, JobKind: "chat.turn", Status: "running"}},
		{"job card bad status", version.MessagePart{Type: "job_card", JobID: 1, JobKind: "design.cases", Status: "bogus"}},
		{"job card progress overflow", version.MessagePart{Type: "job_card", JobID: 1, JobKind: "design.cases", Status: "running", Current: 5, Total: 2}},
		{"job card non-cancelable cancelable", version.MessagePart{Type: "job_card", JobID: 1, JobKind: "design.cases", Status: "done", Cancelable: &cancelable}},
		{"case card no case id", version.MessagePart{Type: "case_card", Title: "T", Status: "draft"}},
		{"case card bad status", version.MessagePart{Type: "case_card", CaseID: 1, Title: "T", Status: "bogus"}},
		{"error no code", version.MessagePart{Type: "error", Message: "m"}},
		{"error no message", version.MessagePart{Type: "error", Code: "model_error"}},
	}
	for _, testCase := range invalid {
		if err := version.ValidateAssistantMessageParts([]version.MessagePart{testCase.part}); err == nil {
			t.Fatalf("%s: accepted %#v", testCase.name, testCase.part)
		}
	}
}
