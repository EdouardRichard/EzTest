package llminfra_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	llminfra "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
)

// writeThinkingTestResponse answers the minimal successful reply of one
// protocol so the adapter only exercises the request payload.
func writeThinkingTestResponse(t *testing.T, w http.ResponseWriter, protocol llm.Protocol) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	switch protocol {
	case llm.ProtocolOpenAIResponses:
		_, _ = io.WriteString(w, `{
			"id":"resp_test","object":"response","status":"completed",
			"output":[{"type":"message","id":"msg_1","role":"assistant","content":[
				{"type":"output_text","text":"ok","annotations":[]}
			]}]
		}`)
	default:
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test","object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}
}

// TestThinkingPayloadMapping pins the per-protocol thinking/reasoning mapping:
// chat completions carries reasoning_effort plus the provider-specific thinking
// object, Responses carries reasoning{effort,summary}. The auto/none defaults
// must leave the wire payload byte-for-byte compatible with the pre-0.2.1
// payload (no new keys).
func TestThinkingPayloadMapping(t *testing.T) {
	ctx := context.Background()

	t.Run("chat completions enabled high", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body = captureChatBody(t, r)
			writeThinkingTestResponse(t, w, llm.ProtocolOpenAIChatCompletions)
		}))
		defer server.Close()

		cfg := chatConfigFor(server.URL+"/v1", 0)
		cfg.ThinkingType = llm.ThinkingTypeEnabled
		cfg.ReasoningEffort = llm.ReasoningEffortHigh
		if _, err := llminfra.NewClient().Chat(ctx, cfg, llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if got := body["reasoning_effort"]; got != "high" {
			t.Fatalf("reasoning_effort = %#v, want high", got)
		}
		thinking, ok := body["thinking"].(map[string]any)
		if !ok || thinking["type"] != "enabled" {
			t.Fatalf("thinking = %#v, want {type:enabled}", body["thinking"])
		}
	})

	t.Run("chat completions defaults omit", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body = captureChatBody(t, r)
			writeThinkingTestResponse(t, w, llm.ProtocolOpenAIChatCompletions)
		}))
		defer server.Close()

		cfg := chatConfigFor(server.URL+"/v1", 0)
		cfg.ThinkingType = llm.ThinkingTypeAuto
		cfg.ReasoningEffort = llm.ReasoningEffortNone
		if _, err := llminfra.NewClient().Chat(ctx, cfg, llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if _, ok := body["reasoning_effort"]; ok {
			t.Fatalf("default payload unexpectedly carries reasoning_effort: %#v", body["reasoning_effort"])
		}
		if _, ok := body["thinking"]; ok {
			t.Fatalf("default payload unexpectedly carries thinking: %#v", body["thinking"])
		}
	})

	t.Run("responses enabled high", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				t.Errorf("path = %q, want /v1/responses", r.URL.Path)
			}
			body = captureChatBody(t, r)
			writeThinkingTestResponse(t, w, llm.ProtocolOpenAIResponses)
		}))
		defer server.Close()

		cfg := responsesConfig(server.URL+"/v1", 0)
		cfg.ThinkingType = llm.ThinkingTypeEnabled
		cfg.ReasoningEffort = llm.ReasoningEffortHigh
		if _, err := llminfra.NewClient().Chat(ctx, cfg, llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("reasoning = %#v, want an object", body["reasoning"])
		}
		if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
			t.Fatalf("reasoning = %#v, want effort=high summary=auto", reasoning)
		}
	})

	t.Run("responses disabled maps effort none", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body = captureChatBody(t, r)
			writeThinkingTestResponse(t, w, llm.ProtocolOpenAIResponses)
		}))
		defer server.Close()

		cfg := responsesConfig(server.URL+"/v1", 0)
		cfg.ThinkingType = llm.ThinkingTypeDisabled
		cfg.ReasoningEffort = llm.ReasoningEffortNone
		if _, err := llminfra.NewClient().Chat(ctx, cfg, llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("reasoning = %#v, want {effort:none}", body["reasoning"])
		}
		if reasoning["effort"] != "none" {
			t.Fatalf("reasoning = %#v, want effort=none", reasoning)
		}
	})

	t.Run("responses defaults omit", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body = captureChatBody(t, r)
			writeThinkingTestResponse(t, w, llm.ProtocolOpenAIResponses)
		}))
		defer server.Close()

		cfg := responsesConfig(server.URL+"/v1", 0)
		cfg.ThinkingType = llm.ThinkingTypeAuto
		cfg.ReasoningEffort = llm.ReasoningEffortNone
		if _, err := llminfra.NewClient().Chat(ctx, cfg, llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if _, ok := body["reasoning"]; ok {
			t.Fatalf("default payload unexpectedly carries reasoning: %#v", body["reasoning"])
		}
	})
}

// TestReasoningContentPassthrough pins the spec §7.2 history contract over a
// real HTTP exchange: the adapter captures choices[].message.reasoning_content
// from the first assistant turn and the application can replay it on the next
// tool turn. DeepSeek-style reasoning models reject a second tool turn whose
// assistant history dropped the reasoning content (HTTP 400), so both halves
// of the round trip are asserted on the wire.
func TestReasoningContentPassthrough(t *testing.T) {
	ctx := context.Background()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, captureChatBody(t, r))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-reasoning","object":"chat.completion",
				"choices":[{"index":0,"message":{
					"role":"assistant","content":null,
					"reasoning_content":"I need to read the file first.",
					"tool_calls":[{"id":"call_1","type":"function","function":{
						"name":"read_file","arguments":"{\"path\":\"README.md\"}"}}]},
					"finish_reason":"tool_calls"}]
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-done","object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":"the file says ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	client := llminfra.NewClient()
	cfg := chatConfigFor(server.URL+"/v1", 0)
	tools := []llmapp.ToolDefinition{{
		Name:        "read_file",
		Description: "read a file",
		Parameters:  []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}
	user := llmapp.ChatMessage{Role: "user", Content: "read the README"}

	first, err := client.Chat(ctx, cfg, llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{user},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if first.ReasoningContent != "I need to read the file first." {
		t.Fatalf("reasoning_content = %q, want the upstream reasoning text", first.ReasoningContent)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %#v", first.ToolCalls)
	}

	history := []llmapp.ChatMessage{
		user,
		{
			Role:             "assistant",
			ReasoningContent: first.ReasoningContent,
			ToolCalls:        first.ToolCalls,
		},
		{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Content: `{"status":"ok"}`},
	}
	second, err := client.Chat(ctx, cfg, llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: history,
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if second.Content != "the file says ok" {
		t.Fatalf("second content = %q", second.Content)
	}

	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	messages, ok := bodies[1]["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("second request messages = %#v", bodies[1]["messages"])
	}
	assistant, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("assistant message = %#v", messages[1])
	}
	if assistant["reasoning_content"] != "I need to read the file first." {
		t.Fatalf("second request reasoning_content = %#v, want the first turn's reasoning", assistant["reasoning_content"])
	}
	calls, ok := assistant["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", assistant["tool_calls"])
	}
	if call, _ := calls[0].(map[string]any); call["id"] != "call_1" {
		t.Fatalf("assistant tool call = %#v", calls[0])
	}
	tool, ok := messages[2].(map[string]any)
	if !ok || tool["tool_call_id"] != "call_1" {
		t.Fatalf("tool message = %#v", messages[2])
	}
}

// TestResponsesReasoningItemPreserved pins the Responses half of spec §7.2: a
// reasoning output item is normalized to reasoning text and its provider-issued
// item (id + summary/encrypted content) is kept so the next tool turn can
// replay it verbatim instead of silently dropping it.
func TestResponsesReasoningItemPreserved(t *testing.T) {
	ctx := context.Background()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, captureChatBody(t, r))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"resp_1","object":"response","status":"completed",
				"output":[
					{"type":"reasoning","id":"rs_1","summary":[
						{"type":"summary_text","text":"I need the file."}
					],"encrypted_content":"opaque-reasoning"},
					{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}
				]
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"resp_2","object":"response","status":"completed",
			"output":[{"type":"message","id":"msg_2","role":"assistant","content":[
				{"type":"output_text","text":"the file says ok"}
			]}]
		}`)
	}))
	defer server.Close()

	client := llminfra.NewClient()
	cfg := responsesConfig(server.URL+"/v1", 0)
	tools := []llmapp.ToolDefinition{{
		Name:        "read_file",
		Description: "read a file",
		Parameters:  []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}
	user := llmapp.ChatMessage{Role: "user", Content: "read the README"}

	first, err := client.Chat(ctx, cfg, llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{user},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if first.ReasoningContent != "I need the file." {
		t.Fatalf("reasoning_content = %q, want the reasoning summary text", first.ReasoningContent)
	}
	if len(first.ReasoningItems) != 1 {
		t.Fatalf("reasoning items = %#v, want the provider item preserved", first.ReasoningItems)
	}

	history := []llmapp.ChatMessage{
		user,
		{
			Role:             "assistant",
			ReasoningContent: first.ReasoningContent,
			ReasoningItems:   first.ReasoningItems,
			ToolCalls:        first.ToolCalls,
		},
		{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Content: `{"status":"ok"}`},
	}
	second, err := client.Chat(ctx, cfg, llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: history,
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if second.Content != "the file says ok" {
		t.Fatalf("second content = %q", second.Content)
	}

	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	input, ok := bodies[1]["input"].([]any)
	if !ok || len(input) != 4 {
		t.Fatalf("second request input = %#v", bodies[1]["input"])
	}
	reasoning, ok := input[1].(map[string]any)
	if !ok || reasoning["type"] != "reasoning" {
		t.Fatalf("replayed reasoning item = %#v", input[1])
	}
	if reasoning["id"] != "rs_1" || reasoning["encrypted_content"] != "opaque-reasoning" {
		t.Fatalf("replayed reasoning item = %#v, want the provider item verbatim", reasoning)
	}
	call, ok := input[2].(map[string]any)
	if !ok || call["type"] != "function_call" || call["call_id"] != "call_1" {
		t.Fatalf("function_call item = %#v", input[2])
	}
	toolOutput, ok := input[3].(map[string]any)
	if !ok || toolOutput["type"] != "function_call_output" {
		t.Fatalf("function_call_output item = %#v", input[3])
	}
}

// TestRemoteModelCapabilities pins the 0.2.1 catalog capability mapping: the
// upstream capabilities object exposes tools/reasoning/prompt-cache so a
// refresh can persist them on llm_model.capabilities.
func TestRemoteModelCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[
			{"id":"reasoning-model","context_window":200000,"capabilities":{
				"function_calling":true,"reasoning":true,"prompt_cache":true
			}},
			{"id":"plain-model"}
		]}`)
	}))
	defer server.Close()

	models, err := llminfra.NewClient().ListModels(context.Background(), chatConfigFor(server.URL+"/v1", 0))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	if !models[0].SupportsTools || !models[0].SupportsReasoning || !models[0].SupportsPromptCache {
		t.Fatalf("reasoning model capabilities = %#v", models[0])
	}
	if models[0].ContextWindow != 200000 {
		t.Fatalf("context window = %d", models[0].ContextWindow)
	}
	if models[1].SupportsTools || models[1].SupportsReasoning || models[1].SupportsPromptCache {
		t.Fatalf("plain model capabilities = %#v", models[1])
	}
}
