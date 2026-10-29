package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestChatCompletionsSendsToolsThinkingAndReasoning pins the chat completions
// request contract used by the agent runtime (design §7.2): tool specs,
// thinking/reasoning_effort/max_tokens switches, and the assistant/tool
// history fields that must be echoed back when tools are enabled.
func TestChatCompletionsSendsToolsThinkingAndReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		body := decodeBody(t, r)

		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("tools=%v want one tool spec", body["tools"])
		}
		tool, _ := tools[0].(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if tool["type"] != "function" || function["name"] != "file_read" {
			t.Fatalf("tools[0]=%v", tools[0])
		}
		parameters, _ := function["parameters"].(map[string]any)
		if parameters["type"] != "object" {
			t.Fatalf("tools[0].function.parameters=%v want a JSON schema object", function["parameters"])
		}

		thinking, _ := body["thinking"].(map[string]any)
		if thinking["type"] != "enabled" {
			t.Fatalf("thinking=%v want {\"type\":\"enabled\"}", body["thinking"])
		}
		if body["reasoning_effort"] != "max" {
			t.Fatalf("reasoning_effort=%v want max", body["reasoning_effort"])
		}
		if body["max_tokens"] != float64(2048) {
			t.Fatalf("max_tokens=%v want 2048", body["max_tokens"])
		}

		messages, ok := body["messages"].([]any)
		if !ok || len(messages) != 3 {
			t.Fatalf("messages=%v want user, assistant and tool", body["messages"])
		}
		user, _ := messages[0].(map[string]any)
		if _, exists := user["reasoning_content"]; exists {
			t.Errorf("user message must not carry reasoning_content: %v", user)
		}

		assistant, _ := messages[1].(map[string]any)
		if assistant["role"] != "assistant" {
			t.Fatalf("messages[1]=%v want assistant", messages[1])
		}
		if assistant["reasoning_content"] != "think-1" {
			t.Fatalf("assistant reasoning_content=%v want think-1", assistant["reasoning_content"])
		}
		if content, exists := assistant["content"]; exists && content != nil {
			t.Fatalf("assistant content=%v want omitted/null for an empty content turn", content)
		}
		calls, ok := assistant["tool_calls"].([]any)
		if !ok || len(calls) != 1 {
			t.Fatalf("assistant tool_calls=%v", assistant["tool_calls"])
		}
		call, _ := calls[0].(map[string]any)
		callFunction, _ := call["function"].(map[string]any)
		if call["id"] != "call_1" || call["type"] != "function" {
			t.Fatalf("tool_calls[0]=%v", calls[0])
		}
		if callFunction["name"] != "file_read" || callFunction["arguments"] != `{"path":"README.md"}` {
			t.Fatalf("tool_calls[0].function=%v", call["function"])
		}

		toolMessage, _ := messages[2].(map[string]any)
		if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_1" {
			t.Fatalf("tool message=%v want tool_call_id call_1", messages[2])
		}
		content, ok := toolMessage["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("tool content=%v want one text part", toolMessage["content"])
		}
		part, _ := content[0].(map[string]any)
		if part["type"] != "text" || part["text"] != "README contents" {
			t.Fatalf("tool content[0]=%v", content[0])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	res, err := c.Chat(context.Background(), llmapp.ChatRequest{
		Model: "deepseek-flash",
		Messages: []llmapp.Message{
			{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "read README"}}},
			{
				Role:             "assistant",
				ReasoningContent: "think-1",
				ToolCalls: []llmapp.ToolCall{
					{ID: "call_1", Name: "file_read", Arguments: `{"path":"README.md"}`},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    []llmapp.Part{{Type: "text", Text: "README contents"}},
			},
		},
		Tools: []llmapp.ToolSpec{{
			Name:        "file_read",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
		MaxTokens:       2048,
		Thinking:        "enabled",
		ReasoningEffort: "max",
	})
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// TestChatCompletionsParsesToolCallsWithEmptyContent pins design §7.2's rule
// that an empty content with tool calls is a valid response, and that reasoning
// content, finish_reason and reasoning_tokens are parsed.
func TestChatCompletionsParsesToolCallsWithEmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":null,"reasoning_content":"r","tool_calls":[{"id":"call_1","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"completion_tokens_details":{"reasoning_tokens":5}}}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	res, err := c.Chat(context.Background(), llmapp.ChatRequest{
		Model:    "deepseek-flash",
		Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "read README"}}}},
		Tools:    []llmapp.ToolSpec{{Name: "file_read"}},
	})
	if err != nil {
		t.Fatalf("empty content with tool calls must be valid: %v", err)
	}
	if res.Text != "" {
		t.Fatalf("text=%q want empty", res.Text)
	}
	if res.ReasoningContent != "r" {
		t.Fatalf("reasoning_content=%q want r", res.ReasoningContent)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", res.FinishReason)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool_calls=%+v", res.ToolCalls)
	}
	call := res.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "file_read" || call.Arguments != `{"path":"README.md"}` {
		t.Fatalf("tool_call=%+v", call)
	}
	if res.Usage.ReasoningTokens != 5 {
		t.Fatalf("usage.reasoning_tokens=%d want 5", res.Usage.ReasoningTokens)
	}
	if res.Usage.TotalTokens != 14 {
		t.Fatalf("usage.total_tokens=%d want 14", res.Usage.TotalTokens)
	}
}

// TestChatCompletionsStreamAssemblesReasoningAndToolCalls pins the streaming
// contract: reasoning_content deltas are forwarded, tool call fragments are
// assembled by index and only carried on Done, together with finish_reason and
// usage.
func TestChatCompletionsStreamAssemblesReasoningAndToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "", `{"choices":[{"delta":{"reasoning_content":"think-"}}]}`)
		writeSSE(t, w, "", `{"choices":[{"delta":{"reasoning_content":"1"}}]}`)
		writeSSE(t, w, "", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"file_read","arguments":"{\"pa"}}]}}]}`)
		writeSSE(t, w, "", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"README.md\"}"}}]}}]}`)
		writeSSE(t, w, "", `{"choices":[{"delta":{"content":""},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"completion_tokens_details":{"reasoning_tokens":7}}}`)
		writeSSE(t, w, "", `[DONE]`)
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	events, err := c.ChatStream(context.Background(), llmapp.ChatRequest{
		Model:    "deepseek-flash",
		Stream:   true,
		Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "read README"}}}},
		Tools:    []llmapp.ToolSpec{{Name: "file_read"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}

	var reasoning []string
	var done []llmapp.StreamEvent
	for _, event := range collectStream(t, events) {
		if event.Err != nil {
			t.Fatalf("unexpected stream error: %v", event.Err)
		}
		if event.ReasoningDelta != "" {
			reasoning = append(reasoning, event.ReasoningDelta)
		}
		if !event.Done && len(event.ToolCalls) > 0 {
			t.Fatalf("tool call fragments must only be carried on Done: %+v", event)
		}
		if event.Done {
			done = append(done, event)
		}
	}
	if len(reasoning) != 2 || reasoning[0] != "think-" || reasoning[1] != "1" {
		t.Fatalf("reasoning deltas=%v want [think- 1]", reasoning)
	}
	if len(done) != 1 {
		t.Fatalf("done events=%+v want exactly one", done)
	}
	terminal := done[0]
	if terminal.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", terminal.FinishReason)
	}
	if terminal.Usage.ReasoningTokens != 7 {
		t.Fatalf("done usage=%+v want reasoning_tokens 7", terminal.Usage)
	}
	if terminal.Usage.TotalTokens != 14 {
		t.Fatalf("done usage=%+v want total_tokens 14", terminal.Usage)
	}
	if len(terminal.ToolCalls) != 1 {
		t.Fatalf("done tool_calls=%+v", terminal.ToolCalls)
	}
	call := terminal.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "file_read" || call.Arguments != `{"path":"README.md"}` {
		t.Fatalf("assembled tool call=%+v", call)
	}
}

// TestResponsesRejectsTools pins design §7.3: the responses adapter rejects
// tools with llm_bad_request before any upstream request is made.
func TestResponsesRejectsTools(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"unexpected"}]}]}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{Protocol: llm.ProtocolResponses, APIKey: "sk-test"}, srv.Client())
	base := llmapp.ChatRequest{
		Model:    "deepseek-flash",
		Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "read README"}}}},
		Tools:    []llmapp.ToolSpec{{Name: "file_read", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}

	_, err := c.Chat(context.Background(), base)
	var llmErr *llmapp.LLMError
	if !errors.As(err, &llmErr) || llmErr.Code != platform.CodeLLMBadRequest {
		t.Fatalf("Chat err=%v want %q", err, platform.CodeLLMBadRequest)
	}
	if llmErr.Retryable {
		t.Fatalf("tools rejection must not be retryable: %+v", llmErr)
	}

	streamRequest := base
	streamRequest.Stream = true
	if _, err := c.ChatStream(context.Background(), streamRequest); err != nil {
		var streamErr *llmapp.LLMError
		if !errors.As(err, &streamErr) || streamErr.Code != platform.CodeLLMBadRequest {
			t.Fatalf("ChatStream err=%v want %q", err, platform.CodeLLMBadRequest)
		}
	} else {
		t.Fatal("ChatStream accepted tools on the responses protocol")
	}

	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("upstream requests=%d want 0: tools must be rejected before the request", got)
	}
}
