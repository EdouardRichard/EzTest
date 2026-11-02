package agentapp_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// TestRunnerCarriesReasoningContentOnToolTurns pins the runner half of spec
// §7.2: the reasoning text captured from a tool-calling assistant turn must be
// carried in the in-memory history, because the adapters replay it on the next
// request (DeepSeek-style reasoning models otherwise reject the turn).
func TestRunnerCarriesReasoningContentOnToolTurns(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		{
			ReasoningContent: "先读取版本上下文",
			ToolCalls: []llmapp.ToolCall{{
				ID:        "call_1",
				Name:      tools.ToolContextVersionPack,
				Arguments: `{}`,
			}},
			FinishReason: "tool_calls",
		},
		{Content: "完成"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fixture.chat.requestCount() != 2 {
		t.Fatalf("chat calls = %d, want tool turn + final turn", fixture.chat.requestCount())
	}

	second := fixture.chat.request(1)
	var assistant *llmapp.ChatMessage
	for i := range second.Messages {
		if second.Messages[i].Role == "assistant" {
			assistant = &second.Messages[i]
			break
		}
	}
	if assistant == nil {
		t.Fatalf("second request has no assistant history: %#v", second.Messages)
	}
	if assistant.ReasoningContent != "先读取版本上下文" {
		t.Fatalf("assistant reasoning_content = %q, want the first turn's reasoning", assistant.ReasoningContent)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool calls = %#v, want the tool call kept alongside the reasoning", assistant.ToolCalls)
	}
}
