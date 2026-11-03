package versionadapter_test

import (
	"context"
	"encoding/json"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestConversationPortMapsStreamedParts pins the runtime -> version mapping of
// the 0.2.1 streamed part types produced by the runner (thinking/tool_call/
// job_card/error): the version validator must receive the contract fields, not
// just the generic type/text carrier.
func TestConversationPortMapsStreamedParts(t *testing.T) {
	ctx := context.Background()
	var appended []version.MessagePart
	fake := &fakeConversations{
		getFn: func(_ context.Context, versionID, conversationID int64) (versionapp.ConversationView, error) {
			return versionapp.ConversationView{ID: 20, VersionID: 10}, nil
		},
		appendFn: func(_ context.Context, conversationID int64, parts []version.MessagePart, runID int64, replyTo *int64) (versionapp.MessageView, error) {
			appended = parts
			return versionapp.MessageView{ID: 30, ConversationID: conversationID, Role: "assistant", Parts: parts, CreatedAt: testTime}, nil
		},
	}
	adapter := versionadapter.New(nil, fake, nil, nil, nil)

	_, err := adapter.AppendAssistantMessage(ctx, agentapp.AssistantMessageInput{
		VersionID: 10, ConversationID: 20, AgentRunID: 40,
		Parts: []agentapp.ConversationPart{
			{Type: "thinking", Text: "先读取任务包。", Data: map[string]any{"duration_ms": int64(1200)}},
			{Type: "tool_call", Data: map[string]any{
				"call_id":     "call_1",
				"name":        "context.task_pack",
				"status":      "done",
				"arguments":   json.RawMessage(`{"max_posts":5}`),
				"result":      json.RawMessage(`{"ok":true}`),
				"duration_ms": int64(42),
			}},
			{Type: "job_card", Data: map[string]any{
				"job_id":     "501",
				"kind":       "design.cases",
				"status":     "running",
				"current":    int64(12),
				"total":      int64(27),
				"cancelable": true,
			}},
			{Type: "error", Data: map[string]any{
				"code": "tool_failure", "message": "工具超时", "retryable": true, "action": "重试",
			}},
			{Type: "case_card", Data: map[string]any{
				"case_id": "4001", "case_version_id": "4002", "title": "登录", "status": "draft",
			}},
		},
	})
	if err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	if len(appended) != 5 {
		t.Fatalf("appended parts = %#v", appended)
	}
	if thinking := appended[0]; thinking.Type != "thinking" || thinking.Text != "先读取任务包。" || thinking.DurationMS != 1200 {
		t.Fatalf("thinking = %#v", thinking)
	}
	call := appended[1]
	if call.Type != "tool_call" || call.CallID != "call_1" || call.Name != "context.task_pack" ||
		call.Status != "done" || call.DurationMS != 42 {
		t.Fatalf("tool_call = %#v", call)
	}
	if string(call.Arguments) != `{"max_posts":5}` || string(call.Result) != `{"ok":true}` {
		t.Fatalf("tool_call arguments/result = %#v/%#v", call.Arguments, call.Result)
	}
	card := appended[2]
	if card.Type != "job_card" || card.JobID != 501 || card.JobKind != "design.cases" ||
		card.Status != "running" || card.Current != 12 || card.Total != 27 ||
		card.Cancelable == nil || !*card.Cancelable {
		t.Fatalf("job_card = %#v", card)
	}
	errorPart := appended[3]
	if errorPart.Type != "error" || errorPart.Code != "tool_failure" || errorPart.Message != "工具超时" ||
		!errorPart.Retryable || errorPart.Action != "重试" {
		t.Fatalf("error = %#v", errorPart)
	}
	caseCard := appended[4]
	if caseCard.Type != "case_card" || caseCard.CaseID != 4001 || caseCard.CaseVersionID != 4002 ||
		caseCard.Title != "登录" || caseCard.Status != "draft" {
		t.Fatalf("case_card = %#v", caseCard)
	}

	// The mapped aggregate must satisfy the version validator, which is the
	// contract gate the DB writer relies on.
	if err := version.ValidateAssistantMessageParts(appended); err != nil {
		t.Fatalf("mapped parts rejected by the version validator: %v", err)
	}
}
