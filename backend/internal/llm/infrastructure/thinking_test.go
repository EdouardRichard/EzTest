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
