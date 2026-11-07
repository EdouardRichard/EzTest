package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeQuestionAsker implements the question.ask port. It records the injected
// scope and the validated input so the test can prove the model never supplies
// the conversation/version/agent-run identity.
type fakeQuestionAsker struct {
	calls  int
	scope  Scope
	input  QuestionAskInput
	result QuestionAskResult
	err    error
}

func (f *fakeQuestionAsker) AskQuestions(_ context.Context, scope Scope, input QuestionAskInput) (QuestionAskResult, error) {
	f.calls++
	f.scope = scope
	f.input = input
	if f.err != nil {
		return QuestionAskResult{}, f.err
	}
	if f.result.ID != 0 {
		return f.result, nil
	}
	return QuestionAskResult{
		ID:        7001,
		MessageID: 8001,
		BatchID:   input.BatchID,
		Status:    "pending",
		ExpiresAt: time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC),
	}, nil
}

var _ QuestionAsker = (*fakeQuestionAsker)(nil)

func questionAskScope() Scope {
	return Scope{VersionID: 7, ConversationID: 11, AgentRunID: 9001}
}

func questionAskPayload() string {
	return `{"questions":[` +
		`{"id":"q1","text":"退款到账时间按 T+1 还是 T+3 验收？","options":[{"id":"a","label":"T+1"},{"id":"b","label":"T+3"}],"multiple":false,"allow_free_text":true,"required":true,"source_refs":["attachment:spec","requirement:FR-012"]},` +
		`{"id":"q2","text":"是否需要覆盖退款失败场景？","options":[{"id":"yes","label":"需要"},{"id":"no","label":"不需要"}],"multiple":false,"allow_free_text":false,"required":false,"source_refs":[]}` +
		`],"batch_id":"qb-1","expires_at":"2026-11-06T06:00:00Z"}`
}

func questionItemsJSON(n int) string {
	items := make([]string, 0, n)
	for index := 0; index < n; index++ {
		items = append(items, fmt.Sprintf(
			`{"id":"q%d","text":"问题 %d","options":[{"id":"a","label":"A"}],"allow_free_text":true}`, index, index))
	}
	return "[" + strings.Join(items, ",") + "]"
}

// TestQuestionAskToolCreatesPending is the task's tool contract test: the model
// supplies only the question payload, the tool forwards it with the injected
// scope and returns the pending human_request id.
func TestQuestionAskToolCreatesPending(t *testing.T) {
	asker := &fakeQuestionAsker{}
	tool := NewQuestionAskTool(asker)
	value, err := tool.Execute(t.Context(), questionAskScope(), json.RawMessage(questionAskPayload()))
	if err != nil {
		t.Fatal(err)
	}
	if asker.calls != 1 {
		t.Fatalf("asker calls = %d, want 1", asker.calls)
	}
	if asker.scope.VersionID != 7 || asker.scope.ConversationID != 11 || asker.scope.AgentRunID != 9001 {
		t.Fatalf("scope = %+v, want the injected host run scope", asker.scope)
	}
	if asker.input.BatchID != "qb-1" || len(asker.input.Questions) != 2 {
		t.Fatalf("input = %+v", asker.input)
	}
	first := asker.input.Questions[0]
	if first.ID != "q1" || !first.Required || !first.AllowFreeText || first.Multiple {
		t.Fatalf("first question = %+v", first)
	}
	if len(first.Options) != 2 || first.Options[0].ID != "a" || first.Options[0].Label != "T+1" {
		t.Fatalf("first options = %+v", first.Options)
	}
	if len(first.SourceRefs) != 2 || first.SourceRefs[1] != "requirement:FR-012" {
		t.Fatalf("source refs = %+v", first.SourceRefs)
	}
	if asker.input.ExpiresAt == nil || !asker.input.ExpiresAt.Equal(time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("expires_at = %v", asker.input.ExpiresAt)
	}

	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", value)
	}
	if result["question_id"] != "7001" || result["message_id"] != "8001" {
		t.Fatalf("result ids = %#v", result)
	}
	if result["batch_id"] != "qb-1" || result["status"] != "pending" || result["expires_at"] == "" {
		t.Fatalf("result = %#v", result)
	}
}

// TestQuestionAskToolValidatesPayload keeps malformed model input a typed,
// retryable invalid_argument before any state is created.
func TestQuestionAskToolValidatesPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		field   string
	}{
		{"missing questions", `{}`, "questions"},
		{"zero questions", `{"questions":[]}`, "questions"},
		{"six questions", `{"questions":` + questionItemsJSON(6) + `}`, "questions"},
		{"duplicate ids", `{"questions":[{"id":"q1","text":"one","options":[{"id":"a","label":"A"}],"allow_free_text":true},{"id":"q1","text":"two","options":[{"id":"a","label":"A"}],"allow_free_text":true}]}`, "questions[1].id"},
		{"empty text", `{"questions":[{"id":"q1","text":"  ","options":[{"id":"a","label":"A"}],"allow_free_text":true}]}`, "questions[0].text"},
		{"empty option label", `{"questions":[{"id":"q1","text":"one","options":[{"id":"a","label":""}],"allow_free_text":true}]}`, "questions[0].options[0].label"},
		{"duplicate option id", `{"questions":[{"id":"q1","text":"one","options":[{"id":"a","label":"A"},{"id":"a","label":"B"}],"allow_free_text":true}]}`, "questions[0].options[1].id"},
		{"unanswerable required", `{"questions":[{"id":"q1","text":"one","options":[],"allow_free_text":false,"required":true}]}`, "questions[0]"},
		{"unknown scope field", `{"questions":` + questionItemsJSON(1) + `,"version_id":"7"}`, "parameters"},
		{"invalid expires_at", `{"questions":` + questionItemsJSON(1) + `,"expires_at":"tomorrow"}`, "expires_at"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			asker := &fakeQuestionAsker{}
			_, err := NewQuestionAskTool(asker).Execute(t.Context(), questionAskScope(), json.RawMessage(test.payload))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("tool error = %+v", toolErr)
			}
			if got := toolErr.Details["field"]; got != test.field {
				t.Fatalf("details.field = %#v, want %q (details %#v)", got, test.field, toolErr.Details)
			}
			if asker.calls != 0 {
				t.Fatalf("invalid payload reached the port (%d calls)", asker.calls)
			}
		})
	}
}

// TestQuestionAskToolRequiresHostRunScope pins that version, conversation and
// the owning agent run all come from the runtime-injected scope.
func TestQuestionAskToolRequiresHostRunScope(t *testing.T) {
	scopes := []Scope{
		{ConversationID: 11, AgentRunID: 9001},
		{VersionID: 7, AgentRunID: 9001},
		{VersionID: 7, ConversationID: 11},
	}
	for _, scope := range scopes {
		asker := &fakeQuestionAsker{}
		_, err := NewQuestionAskTool(asker).Execute(t.Context(), scope, json.RawMessage(questionAskPayload()))
		toolErr := asToolError(t, err)
		if toolErr.Code != CodeInvalidArgument || toolErr.Details["field"] != "scope" || !toolErr.Retryable {
			t.Fatalf("scope %+v tool error = %+v", scope, toolErr)
		}
		if asker.calls != 0 {
			t.Fatalf("scope %+v reached the port", scope)
		}
	}
}

// TestQuestionAskToolMapsPortErrors pins the port error contract.
func TestQuestionAskToolMapsPortErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"not found", platform.NotFound("conversation"), CodeNotFound},
		{"state conflict", platform.StateConflict("question is not pending"), CodeStateConflict},
		{"invalid", platform.Invalid("answers", "required"), CodeInvalidArgument},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			asker := &fakeQuestionAsker{err: test.err}
			_, err := NewQuestionAskTool(asker).Execute(t.Context(), questionAskScope(), json.RawMessage(questionAskPayload()))
			toolErr := asToolError(t, err)
			if toolErr.Code != test.code {
				t.Fatalf("code = %q, want %q", toolErr.Code, test.code)
			}
		})
	}
	t.Run("internal failure stays internal", func(t *testing.T) {
		asker := &fakeQuestionAsker{err: context.DeadlineExceeded}
		_, err := NewQuestionAskTool(asker).Execute(t.Context(), questionAskScope(), json.RawMessage(questionAskPayload()))
		var toolErr *Error
		if err == nil || errorsAs(err, &toolErr) {
			t.Fatalf("internal failure must not become a tool error: %v", err)
		}
	})
	t.Run("missing port", func(t *testing.T) {
		_, err := NewQuestionAskTool(nil).Execute(t.Context(), questionAskScope(), json.RawMessage(questionAskPayload()))
		var toolErr *Error
		if err == nil || errorsAs(err, &toolErr) {
			t.Fatalf("missing port must be an internal configuration error: %v", err)
		}
	})
}

// TestQuestionAskToolDescriptionDoesNotPromiseSkipWakeUp pins the wake
// contract: only an answer writes the kind=answer message that wakes the Host;
// skipping cancels the question without an answer message.
func TestQuestionAskToolDescriptionDoesNotPromiseSkipWakeUp(t *testing.T) {
	description := NewQuestionAskTool(nil).Description()
	for _, forbidden := range []string{"回答或跳过后会唤醒", "回答或跳过后唤醒"} {
		if strings.Contains(description, forbidden) {
			t.Fatalf("description promises a skip wake-up: %q", description)
		}
	}
	if !strings.Contains(description, "回答后会唤醒") {
		t.Fatalf("description must state that an answer wakes the conversation: %q", description)
	}
}

// TestQuestionAskToolSchemaHidesScope pins the shared scope-hiding contract.
func TestQuestionAskToolSchemaHidesScope(t *testing.T) {
	tool := NewQuestionAskTool(nil)
	if tool.Name() != ToolQuestionAsk {
		t.Fatalf("name = %q, want %q", tool.Name(), ToolQuestionAsk)
	}
	raw := string(tool.Parameters())
	for _, forbidden := range []string{"version_id", "conversation_id", "agent_run_id"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("schema leaks the injected scope field %q: %s", forbidden, raw)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema shape = %#v", schema)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "questions" {
		t.Fatalf("required = %#v, want [questions]", required)
	}
	if tool.Description() == "" {
		t.Fatal("tool has no description")
	}
}
