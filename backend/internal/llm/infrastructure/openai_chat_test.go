package llminfra_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	llminfra "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
)

func chatConfigFor(baseURL string, maxRetries int) llm.Config {
	cfg := openAITestConfig(baseURL)
	cfg.MaxRetries = maxRetries
	return cfg
}

// captureChatBody decodes one request body into a generic map for assertions.
func captureChatBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request body: %v\n%s", err, raw)
	}
	return body
}

func writeChatCompletion(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{
		"id":"chatcmpl-test",
		"object":"chat.completion",
		"choices":[{"index":0,"message":{"role":"assistant","content":"received"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}
	}`)
}

func TestOpenAIChatSendsToolsAndParsesToolCalls(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("authorization = %q", got)
		}
		body = captureChatBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_fake_1","type":"function","function":{"name":"requirement.draft_create","arguments":"{\"title\":\"login\"}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":21,"completion_tokens":7,"total_tokens":28}
		}`)
	}))
	defer server.Close()

	client := llminfra.NewClient()
	response, err := client.Chat(context.Background(), chatConfigFor(server.URL+"/v1", 0), llmapp.ChatRequest{
		Model:           "fake-llm",
		MaxOutputTokens: 8192,
		Messages: []llmapp.ChatMessage{
			{Role: "system", Content: "[profile:requirement_curator] system prompt"},
			{Role: "user", Content: "add a login feature"},
		},
		Tools: []llmapp.ToolDefinition{{
			Name:        "requirement.draft_create",
			Description: "create a requirement draft",
			Parameters:  []byte(`{"type":"object","properties":{"title":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if response.Content != "" {
		t.Fatalf("content = %q, want empty for a tool call", response.Content)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
	call := response.ToolCalls[0]
	if call.ID != "call_fake_1" || call.Name != "requirement.draft_create" || call.Arguments != `{"title":"login"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if response.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage["prompt_tokens"] != 21 || response.Usage["completion_tokens"] != 7 || response.Usage["total_tokens"] != 28 {
		t.Fatalf("usage = %#v", response.Usage)
	}

	if body["model"] != "fake-llm" {
		t.Fatalf("model = %#v", body["model"])
	}
	if body["temperature"] != float64(0) {
		t.Fatalf("temperature = %#v", body["temperature"])
	}
	if body["max_tokens"] != float64(8192) {
		t.Fatalf("max_tokens = %#v", body["max_tokens"])
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", body["tool_choice"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	function := tool["function"].(map[string]any)
	if tool["type"] != "function" || function["name"] != "requirement.draft_create" {
		t.Fatalf("tool = %#v", tool)
	}
	if _, ok := function["parameters"].(map[string]any); !ok {
		t.Fatalf("parameters = %#v", function["parameters"])
	}
}

func TestOpenAIChatSendsToolContinuationMessages(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = captureChatBody(t, r)
		writeChatCompletion(w)
	}))
	defer server.Close()

	client := llminfra.NewClient()
	response, err := client.Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
		Model: "fake-llm",
		Messages: []llmapp.ChatMessage{
			{Role: "assistant", Content: "", ToolCalls: []llmapp.ToolCall{{ID: "call_1", Name: "requirement.draft_create", Arguments: `{"title":"login"}`}}},
			{Role: "tool", Content: `{"requirement_id":"3001","status":"draft"}`, ToolCallID: "call_1"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if response.Content != "received" || response.Usage["total_tokens"] != 16 {
		t.Fatalf("response = %#v", response)
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	assistant := messages[0].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("assistant message = %#v", assistant)
	}
	toolCalls, ok := assistant["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", assistant["tool_calls"])
	}
	toolCall := toolCalls[0].(map[string]any)
	if toolCall["id"] != "call_1" || toolCall["type"] != "function" {
		t.Fatalf("tool call = %#v", toolCall)
	}
	toolMessage := messages[1].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_1" {
		t.Fatalf("tool message = %#v", toolMessage)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("tools should be omitted when none were supplied: %#v", body["tools"])
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("tool_choice should be omitted when there are no tools: %#v", body["tool_choice"])
	}
}

func TestOpenAIChatRetriesTransientHTTPFailures(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		switch attempt {
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		default:
			writeChatCompletion(w)
		}
	}))
	defer server.Close()

	response, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 2), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if response.Content != "received" {
		t.Fatalf("response = %#v", response)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestOpenAIChatDoesNotRetryClientErrors(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid parameter"}}`)
	}))
	defer server.Close()

	_, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 3), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on HTTP 400")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("error = %q", err)
	}
}

func TestOpenAIChatClassifiesContextOverflow(t *testing.T) {
	responses := []string{
		`{"error":{"message":"This model's maximum context length is 64000 tokens","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		`{"error":{"message":"request exceeds the context window of this model"}}`,
	}
	for _, payload := range responses {
		t.Run(payload, func(t *testing.T) {
			var attempts int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&attempts, 1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()

			_, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 2), llmapp.ChatRequest{
				Model:    "fake-llm",
				Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			})
			if err == nil {
				t.Fatal("Chat succeeded on a context overflow response")
			}
			if !llmapp.IsContextOverflow(err) {
				t.Fatalf("IsContextOverflow = false for %q", err)
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want no retry for a 4xx overflow", attempts)
			}
		})
	}
}

func TestOpenAIChatRedactsAPIKeyFromErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key `+testAPIKey+`"}}`)
	}))
	defer server.Close()

	_, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on HTTP 401")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("api key leaked in error %q", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error did not mark the redacted secret: %q", err)
	}
}

func TestOpenAIChatTimesOutPerRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the server observes the client disconnecting when
		// the per-request deadline expires.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()

	cfg := chatConfigFor(server.URL, 0)
	cfg.TimeoutSeconds = 1
	start := time.Now()
	_, err := llminfra.NewClient().Chat(context.Background(), cfg, llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded despite the per-request timeout")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timed out") {
		t.Fatalf("error = %q, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestOpenAIChatDefaultsMaxTokens(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = captureChatBody(t, r)
		writeChatCompletion(w)
	}))
	defer server.Close()

	if _, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if body["max_tokens"] != float64(llmapp.DefaultChatMaxOutputTokens) {
		t.Fatalf("max_tokens = %#v, want default", body["max_tokens"])
	}
}

func TestOpenAIChatRejectsInvalidSuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer server.Close()

	_, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat accepted a response without choices")
	}
	if !strings.Contains(err.Error(), "choices") {
		t.Fatalf("error = %q", err)
	}
}

var _ llmapp.ChatClient = (*llminfra.Client)(nil)
