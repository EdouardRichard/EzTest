package application_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// textPart builds the single text part most tests need.
func textPart(text string) []domain.Part {
	return []domain.Part{{Type: "text", Text: text}}
}

// messagesContain reports whether any text part of any message equals want.
func messagesContain(messages []application.LLMMessage, want string) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Text == want {
				return true
			}
		}
	}
	return false
}

func TestBuildConvertsHistoryWithReasoningAndTools(t *testing.T) {
	const systemPrompt = "你是运行时验证助手。"
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart("检查运行时健康")},
		{
			ID:        2,
			Role:      "assistant",
			Parts:     textPart("我来读取文件。"),
			Reasoning: "先读取 README",
			ToolCalls: []domain.ToolCall{
				{ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`},
			},
		},
		{
			ID:         3,
			Role:       "tool",
			Parts:      textPart("构建步骤：make build"),
			ToolCallID: "call_1",
			ToolName:   "file.read",
		},
		{ID: 4, Role: "assistant", Parts: textPart("构建说明是 make build。")},
	}

	result, err := application.NewContextBuilder().Build(systemPrompt, history, 100000)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Messages) != 5 {
		t.Fatalf("len(Messages)=%d want 5 (system + 4 history): %+v", len(result.Messages), result.Messages)
	}
	if result.DroppedMessages != 0 {
		t.Fatalf("DroppedMessages=%d want 0 with a generous budget", result.DroppedMessages)
	}

	system := result.Messages[0]
	if system.Role != "system" {
		t.Fatalf("first message role=%q want system", system.Role)
	}
	if len(system.Parts) != 1 || system.Parts[0].Text != systemPrompt {
		t.Fatalf("system message parts=%+v want the profile prompt", system.Parts)
	}

	user := result.Messages[1]
	if user.Role != "user" || len(user.Parts) != 1 || user.Parts[0].Text != "检查运行时健康" {
		t.Fatalf("user message=%+v", user)
	}

	assistantWithTool := result.Messages[2]
	if assistantWithTool.Role != "assistant" {
		t.Fatalf("third message role=%q want assistant", assistantWithTool.Role)
	}
	if assistantWithTool.ReasoningContent != "先读取 README" {
		t.Fatalf("ReasoningContent=%q want %q", assistantWithTool.ReasoningContent, "先读取 README")
	}
	if len(assistantWithTool.Parts) != 1 || assistantWithTool.Parts[0].Text != "我来读取文件。" {
		t.Fatalf("assistant parts=%+v", assistantWithTool.Parts)
	}
	if len(assistantWithTool.ToolCalls) != 1 {
		t.Fatalf("ToolCalls=%+v want one call", assistantWithTool.ToolCalls)
	}
	call := assistantWithTool.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "file.read" || call.Arguments != `{"path":"README.md"}` {
		t.Fatalf("ToolCall=%+v", call)
	}

	toolMessage := result.Messages[3]
	if toolMessage.Role != "tool" {
		t.Fatalf("fourth message role=%q want tool", toolMessage.Role)
	}
	if toolMessage.ToolCallID != "call_1" {
		t.Fatalf("ToolCallID=%q want call_1", toolMessage.ToolCallID)
	}
	if len(toolMessage.ToolCalls) != 0 {
		t.Fatalf("tool message carries tool calls: %+v", toolMessage.ToolCalls)
	}
	if len(toolMessage.Parts) != 1 || toolMessage.Parts[0].Text != "构建步骤：make build" {
		t.Fatalf("tool parts=%+v", toolMessage.Parts)
	}

	final := result.Messages[4]
	if final.Role != "assistant" {
		t.Fatalf("fifth message role=%q want assistant", final.Role)
	}
	if final.ReasoningContent != "" {
		t.Fatalf("empty history reasoning was echoed as %q", final.ReasoningContent)
	}
	if len(final.Parts) != 1 || final.Parts[0].Text != "构建说明是 make build。" {
		t.Fatalf("final assistant parts=%+v", final.Parts)
	}
}

func TestBuildDropsOldestGroupsAndKeepsLatestUser(t *testing.T) {
	const systemPrompt = "system"
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart("old q")},
		{ID: 2, Role: "assistant", Parts: textPart("old a")},
		{ID: 3, Role: "user", Parts: textPart("mid q")},
		{ID: 4, Role: "assistant", Parts: textPart("mid a")},
		{ID: 5, Role: "user", Parts: textPart("new q")},
		{ID: 6, Role: "assistant", Parts: textPart("new a")},
	}
	// 16 chars overhead per message: system 6+16=22, a 5-char group is
	// 2*(5+16)=42. 22+42 fits the latest group exactly; older groups must go.
	const budgetChars = 64

	result, err := application.NewContextBuilder().Build(systemPrompt, history, budgetChars)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if result.DroppedMessages != 4 {
		t.Fatalf("DroppedMessages=%d want 4 (the two oldest complete groups)", result.DroppedMessages)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("len(Messages)=%d want 3 (system + latest group): %+v", len(result.Messages), result.Messages)
	}
	if result.Messages[0].Role != "system" {
		t.Fatalf("first message role=%q want system", result.Messages[0].Role)
	}
	if result.Messages[1].Role != "user" || result.Messages[1].Parts[0].Text != "new q" {
		t.Fatalf("latest user message was not kept: %+v", result.Messages[1])
	}
	if !messagesContain(result.Messages, "new a") {
		t.Fatal("latest assistant message was dropped with its group")
	}
	if messagesContain(result.Messages, "old q") || messagesContain(result.Messages, "mid q") {
		t.Fatalf("older user messages survived truncation: %+v", result.Messages)
	}
	if got, want := application.EstimateChars(result.Messages), budgetChars; got > want {
		t.Fatalf("EstimateChars=%d want <= budget %d", got, want)
	}
}

func TestBuildKeepsToolCallPairing(t *testing.T) {
	const systemPrompt = "sys"
	history := []domain.Message{
		// Old group: the assistant call and its tool result must be dropped
		// together; keeping the tool message alone would be an orphan result.
		{ID: 1, Role: "user", Parts: textPart("q1")},
		{
			ID:   2,
			Role: "assistant",
			ToolCalls: []domain.ToolCall{
				{ID: "call_old", Name: "file.read", Arguments: `{"path":"old"}`},
			},
		},
		{ID: 3, Role: "tool", Parts: textPart("old result"), ToolCallID: "call_old"},
		{ID: 4, Role: "user", Parts: textPart("q2")},
		{ID: 5, Role: "assistant", Parts: textPart("a2")},
		{ID: 6, Role: "user", Parts: textPart("q3")},
		{ID: 7, Role: "assistant", Parts: textPart("a3")},
	}
	// system 3+16=19, latest group 2*(2+16)=36 => 55. Older groups do not fit.
	const budgetChars = 55

	result, err := application.NewContextBuilder().Build(systemPrompt, history, budgetChars)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if result.DroppedMessages != 5 {
		t.Fatalf("DroppedMessages=%d want 5 (the two older groups)", result.DroppedMessages)
	}
	for _, message := range result.Messages {
		if message.Role == "tool" {
			t.Fatalf("an orphan tool message survived truncation: %+v", message)
		}
		if message.ToolCallID == "call_old" {
			t.Fatalf("tool_call_id of a dropped call survived: %+v", message)
		}
		for _, call := range message.ToolCalls {
			if call.ID == "call_old" {
				t.Fatalf("tool call of a dropped turn survived: %+v", call)
			}
		}
	}
	if !messagesContain(result.Messages, "q3") || !messagesContain(result.Messages, "a3") {
		t.Fatalf("latest group was not kept: %+v", result.Messages)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("len(Messages)=%d want 3 (system + latest group)", len(result.Messages))
	}
}

func TestBuildTreatsLeadingNonUserMessagesAsTheirOwnGroup(t *testing.T) {
	const systemPrompt = "sys"
	history := []domain.Message{
		// Abnormal prefix without a user message: it forms its own group.
		{
			ID:   1,
			Role: "assistant",
			ToolCalls: []domain.ToolCall{
				{ID: "call_prefix", Name: "file.read", Arguments: `{"path":"x"}`},
			},
		},
		{ID: 2, Role: "tool", Parts: textPart("prefix"), ToolCallID: "call_prefix"},
		{ID: 3, Role: "user", Parts: textPart("hi")},
		{ID: 4, Role: "assistant", Parts: textPart("hello")},
	}
	// system 19 + latest group (2+16)+(5+16)=39 => 58; the prefix does not fit.
	const budgetChars = 58

	result, err := application.NewContextBuilder().Build(systemPrompt, history, budgetChars)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if result.DroppedMessages != 2 {
		t.Fatalf("DroppedMessages=%d want 2 (the whole leading prefix)", result.DroppedMessages)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("len(Messages)=%d want 3 (system + latest group)", len(result.Messages))
	}
	if result.Messages[1].Role != "user" || result.Messages[1].Parts[0].Text != "hi" {
		t.Fatalf("latest user message was not kept: %+v", result.Messages[1])
	}
}

func TestBuildHandlesEmptyHistory(t *testing.T) {
	builder := application.NewContextBuilder()

	result, err := builder.Build("sys", nil, 100)
	if err != nil {
		t.Fatalf("Build with empty history returned error: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Role != "system" {
		t.Fatalf("Messages=%+v want only the system message", result.Messages)
	}
	if result.DroppedMessages != 0 {
		t.Fatalf("DroppedMessages=%d want 0", result.DroppedMessages)
	}

	// With no history the system prompt is the entire context; if it alone
	// cannot fit there is nothing left to truncate.
	if _, err := builder.Build("sys", nil, 1); !errors.Is(err, domain.ErrContextOverflow) {
		t.Fatalf("err=%v want domain.ErrContextOverflow when only the system prompt exceeds the budget", err)
	}
}

func TestBuildFailsWhenLatestUserAloneExceedsBudget(t *testing.T) {
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart(strings.Repeat("x", 200))},
		{ID: 2, Role: "assistant", Parts: textPart("answer")},
	}

	_, err := application.NewContextBuilder().Build("system", history, 50)
	if !errors.Is(err, domain.ErrContextOverflow) {
		t.Fatalf("err=%v want domain.ErrContextOverflow", err)
	}
}

// assistantHasVisibleContent reports whether one LLM message is a wire-valid
// assistant turn: it must carry a non-empty text part or at least one tool call.
func assistantHasVisibleContent(message application.LLMMessage) bool {
	if len(message.ToolCalls) > 0 {
		return true
	}
	for _, part := range message.Parts {
		if part.Type == "text" && part.Text != "" {
			return true
		}
	}
	return false
}

// TestBuildDropsEmptyAssistantTurns pins the C1 contract: an assistant message
// with neither content nor tool_calls is rejected by the upstream with HTTP 400
// "Invalid assistant message", so the builder must never replay one — including
// reasoning-only turns left by a thought-only abort and placeholders completed
// with empty content.
func TestBuildDropsEmptyAssistantTurns(t *testing.T) {
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart("q1")},
		{ID: 2, Role: "assistant", Parts: []domain.Part{}},                                   // immediate abort: completed empty
		{ID: 3, Role: "assistant", Reasoning: "只有思考"},                                        // thought-only abort
		{ID: 4, Role: "assistant", Parts: textPart("")},                                      // empty successful answer
		{ID: 5, Role: "assistant", Parts: []domain.Part{{Type: "image_url", ImageURL: "x"}}}, // no text, no calls
		{ID: 6, Role: "user", Parts: textPart("q2")},
		{ID: 7, Role: "assistant", Parts: textPart("a2")},
	}

	result, err := application.NewContextBuilder().Build("sys", history, 100000)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if result.DroppedMessages != 0 {
		t.Fatalf("DroppedMessages=%d want 0 (dropping invalid turns is sanitation, not truncation)", result.DroppedMessages)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("Messages=%+v want system + user q1 + user q2 + assistant a2", result.Messages)
	}
	if result.Messages[0].Role != "system" {
		t.Fatalf("first message role=%q want system", result.Messages[0].Role)
	}
	if !messagesContain(result.Messages, "q1") || !messagesContain(result.Messages, "q2") || !messagesContain(result.Messages, "a2") {
		t.Fatalf("valid messages were dropped: %+v", result.Messages)
	}
	for _, message := range result.Messages {
		if message.Role != "assistant" {
			continue
		}
		if !assistantHasVisibleContent(message) {
			t.Fatalf("an empty assistant message survived: %+v", message)
		}
	}
}

// TestBuildDropsUnpairedToolCalls pins the I2 contract: crash history may hold
// an assistant tool_calls message whose tool results were never persisted; the
// upstream answers such a replay with HTTP 400 "insufficient tool messages".
func TestBuildDropsUnpairedToolCalls(t *testing.T) {
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart("q1")},
		{
			ID:   2,
			Role: "assistant",
			ToolCalls: []domain.ToolCall{
				{ID: "call_a", Name: "file.read", Arguments: `{"path":"a"}`},
				{ID: "call_b", Name: "file.read", Arguments: `{"path":"b"}`},
			},
		},
		{ID: 3, Role: "tool", Parts: textPart("partial"), ToolCallID: "call_a"},
		{ID: 4, Role: "user", Parts: textPart("q2")},
		{ID: 5, Role: "assistant", Parts: textPart("a2")},
	}

	result, err := application.NewContextBuilder().Build("sys", history, 100000)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("Messages=%+v want system + user q1 + user q2 + assistant a2", result.Messages)
	}
	for _, message := range result.Messages {
		if len(message.ToolCalls) > 0 {
			t.Fatalf("an assistant with unpaired tool_calls survived: %+v", message)
		}
		if message.Role == "tool" {
			t.Fatalf("a partial tool result survived its dropped assistant: %+v", message)
		}
	}
	if !messagesContain(result.Messages, "q1") || !messagesContain(result.Messages, "q2") || !messagesContain(result.Messages, "a2") {
		t.Fatalf("the user turn or a valid answer was dropped: %+v", result.Messages)
	}
}

func TestBuildKeepsCompleteToolCallPairing(t *testing.T) {
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart("q1")},
		{
			ID:   2,
			Role: "assistant",
			ToolCalls: []domain.ToolCall{
				{ID: "call_a", Name: "file.read", Arguments: `{"path":"a"}`},
				{ID: "call_b", Name: "file.read", Arguments: `{"path":"b"}`},
			},
		},
		{ID: 3, Role: "tool", Parts: textPart("result a"), ToolCallID: "call_a"},
		{ID: 4, Role: "tool", Parts: textPart("result b"), ToolCallID: "call_b"},
		{ID: 5, Role: "assistant", Parts: textPart("done")},
	}

	result, err := application.NewContextBuilder().Build("sys", history, 100000)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Messages) != 6 {
		t.Fatalf("Messages=%+v want system + the complete user turn", result.Messages)
	}
	assistant := result.Messages[2]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 2 {
		t.Fatalf("assistant tool calls=%+v want both calls", assistant)
	}
	if len(assistant.Parts) != 0 {
		t.Fatalf("assistant parts=%+v want none (only tool calls)", assistant.Parts)
	}
	// A reasoning-only assistant turn is wire-valid when it carries tool calls.
	if got := result.Messages[3]; got.Role != "tool" || got.ToolCallID != "call_a" {
		t.Fatalf("first tool message=%+v", got)
	}
	if got := result.Messages[4]; got.Role != "tool" || got.ToolCallID != "call_b" {
		t.Fatalf("second tool message=%+v", got)
	}
}

func TestBuildDropsOrphanToolMessages(t *testing.T) {
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart("q1")},
		// Orphan tool result without any assistant tool_calls message.
		{ID: 2, Role: "tool", Parts: textPart("orphan"), ToolCallID: "call_missing"},
		{ID: 3, Role: "user", Parts: textPart("q2")},
		// A complete pair followed by an extra tool message that answers nothing.
		{
			ID:   4,
			Role: "assistant",
			ToolCalls: []domain.ToolCall{
				{ID: "call_a", Name: "file.read", Arguments: `{}`},
			},
		},
		{ID: 5, Role: "tool", Parts: textPart("result a"), ToolCallID: "call_a"},
		{ID: 6, Role: "tool", Parts: textPart("stray"), ToolCallID: "call_stray"},
		{ID: 7, Role: "assistant", Parts: textPart("a2")},
	}

	result, err := application.NewContextBuilder().Build("sys", history, 100000)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Messages) != 6 {
		t.Fatalf("Messages=%+v want system + user q1 + user q2 + assistant/tool pair + a2", result.Messages)
	}
	for _, message := range result.Messages {
		if message.ToolCallID == "call_missing" || message.ToolCallID == "call_stray" {
			t.Fatalf("an orphan tool message survived: %+v", message)
		}
	}
	if got := result.Messages[4]; got.Role != "tool" || got.ToolCallID != "call_a" {
		t.Fatalf("the paired tool message was dropped: %+v", got)
	}
}

func TestBuildFiltersEmptyTextParts(t *testing.T) {
	history := []domain.Message{
		{ID: 1, Role: "user", Parts: textPart("q1")},
		{
			ID:   2,
			Role: "assistant",
			Parts: []domain.Part{
				{Type: "text", Text: ""},
				{Type: "text", Text: "可见回答"},
			},
		},
		{ID: 3, Role: "user", Parts: []domain.Part{{Type: "text", Text: ""}}},
	}

	result, err := application.NewContextBuilder().Build("sys", history, 100000)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("Messages=%+v want system + q1 + assistant + empty user turn kept", result.Messages)
	}
	assistant := result.Messages[2]
	if len(assistant.Parts) != 1 || assistant.Parts[0].Text != "可见回答" {
		t.Fatalf("assistant parts=%+v want only the non-empty part", assistant.Parts)
	}
	// The latest user turn is never dropped, even when its content is empty.
	latestUser := result.Messages[3]
	if latestUser.Role != "user" {
		t.Fatalf("latest message role=%q want user", latestUser.Role)
	}
	if len(latestUser.Parts) != 0 {
		t.Fatalf("empty user text parts were not filtered: %+v", latestUser.Parts)
	}
}

func TestEstimateCharsCountsTextReasoningAndArguments(t *testing.T) {
	messages := []application.LLMMessage{
		{Role: "system", Parts: []domain.Part{{Type: "text", Text: "sys"}}}, // 3 + 16
		{
			Role:             "assistant",
			Parts:            []domain.Part{{Type: "text", Text: "ok"}},                                       // 2
			ReasoningContent: "think",                                                                         // 5
			ToolCalls:        []domain.ToolCall{{ID: "call_1", Name: "file.read", Arguments: `{"path":"a"}`}}, // 12 args
		}, // 2+5+12 + 16
		{Role: "tool", Parts: []domain.Part{{Type: "text", Text: "result"}}, ToolCallID: "call_1"}, // 6 + 16
	}
	// 19 + 35 + 22 = 76; tool call id/name and message roles are not counted.
	if got, want := application.EstimateChars(messages), 76; got != want {
		t.Fatalf("EstimateChars=%d want %d", got, want)
	}
}
