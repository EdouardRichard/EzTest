package llminfra_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
				{"id":"call_fake_1","type":"function","function":{"name":"requirement_draft_create","arguments":"{\"title\":\"login\"}"}}
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
	if tool["type"] != "function" || function["name"] != "requirement_draft_create" {
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
	// Strict OpenAI-compatible upstreams (DeepSeek) reject a tool-call message
	// that omits the content field ("missing field `content`", HTTP 422). The
	// wire must always carry it, as the empty string for a pure tool call.
	if content, ok := assistant["content"]; !ok || content != "" {
		t.Fatalf("assistant content = %#v (present=%v), want explicit empty content", content, ok)
	}
	toolCalls, ok := assistant["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", assistant["tool_calls"])
	}
	toolCall := toolCalls[0].(map[string]any)
	if toolCall["id"] != "call_1" || toolCall["type"] != "function" {
		t.Fatalf("tool call = %#v", toolCall)
	}
	function := toolCall["function"].(map[string]any)
	if function["name"] != "requirement_draft_create" {
		t.Fatalf("assistant history tool name was not sanitized: %#v", function["name"])
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

func TestOpenAIChatKeepsAPIKeyPlaintextInErrors(t *testing.T) {
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
	// GD-01 (Q05=B): credentials are plaintext by design and may appear verbatim
	// in error messages; the legacy API-key redaction is removed.
	if !strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("api key must appear verbatim in error (GD-01 plaintext): %q", err)
	}
	if strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error still carries the legacy redaction marker: %q", err)
	}
}

func TestOpenAIChatKeepsCustomHeaderValuesPlaintextInErrors(t *testing.T) {
	const headerSecret = "header-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad header `+headerSecret+`"}}`)
	}))
	defer server.Close()

	cfg := chatConfigFor(server.URL, 0)
	cfg.Headers = map[string]string{"X-Provider-Token": headerSecret}
	_, err := llminfra.NewClient().Chat(context.Background(), cfg, llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on HTTP 400")
	}
	// GD-01 (Q05=B): custom header values are plaintext by design in errors.
	if !strings.Contains(err.Error(), headerSecret) {
		t.Fatalf("custom header value must appear verbatim in error (GD-01 plaintext): %q", err)
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

func TestOpenAIChatSanitizesAllToolNamesAndKeepsUnknownResponses(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = captureChatBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"context_team_room-read","arguments":"{}"}},
				{"id":"call_2","type":"function","function":{"name":"unknown_tool","arguments":"{}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	response, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		Tools: []llmapp.ToolDefinition{
			{Name: "context.team_room-read", Description: "read", Parameters: []byte(`{"type":"object"}`)},
			{Name: "sut.page_list", Description: "pages", Parameters: []byte(`{"type":"object"}`)},
			{Name: "tool:with space", Description: "weird", Parameters: []byte(`{"type":"object"}`)},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(response.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
	if response.ToolCalls[0].Name != "context.team_room-read" {
		t.Fatalf("sanitized name was not mapped back: %#v", response.ToolCalls[0])
	}
	if response.ToolCalls[1].Name != "unknown_tool" {
		t.Fatalf("unknown name must stay as returned: %#v", response.ToolCalls[1])
	}

	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 3 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	wantNames := []string{"context_team_room-read", "sut_page_list", "tool_with_space"}
	for i, want := range wantNames {
		function := tools[i].(map[string]any)["function"].(map[string]any)
		if got := function["name"]; got != want {
			t.Fatalf("tool[%d] name = %#v, want %q", i, got, want)
		}
	}
}

func TestOpenAIChatLeavesPlainToolNamesUnchanged(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = captureChatBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"sut_list","arguments":"{}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	response, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		Tools:    []llmapp.ToolDefinition{{Name: "sut_list", Description: "suts", Parameters: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	tools := body["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "sut_list" {
		t.Fatalf("plain tool name changed: %#v", function["name"])
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "sut_list" {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
}

func TestOpenAIChatRejectsCollidingSanitizedToolNames(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeChatCompletion(w)
	}))
	defer server.Close()

	_, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		Tools: []llmapp.ToolDefinition{
			{Name: "requirement.draft_create", Description: "dotted", Parameters: []byte(`{"type":"object"}`)},
			{Name: "requirement_draft_create", Description: "plain", Parameters: []byte(`{"type":"object"}`)},
		},
	})
	if err == nil {
		t.Fatal("Chat accepted colliding sanitized tool names")
	}
	if !strings.Contains(err.Error(), "collide") {
		t.Fatalf("error = %q", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("upstream was called despite the tool-name collision")
	}
}

// TestChatCompletionsImagePayload pins the chat-completions vision wire shape:
// a user message with images serializes content as [{type:text},{type:image_url}]
// with a base64 data URL, while text-only messages keep the plain string form.
func TestChatCompletionsImagePayload(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = captureChatBody(t, r)
		writeChatCompletion(w)
	}))
	defer server.Close()

	imageData := []byte("png-bytes")
	_, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL+"/v1", 0), llmapp.ChatRequest{
		Model:           "fake-llm",
		MaxOutputTokens: 8192,
		Messages: []llmapp.ChatMessage{
			{Role: "user", Content: "describe this screenshot", Images: []llmapp.ChatImage{
				{MIME: "image/png", Data: imageData},
			}},
			{Role: "user", Content: "plain text stays a string"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	imageMessage := messages[0].(map[string]any)
	parts, ok := imageMessage["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("image content = %#v, want a two-part array", imageMessage["content"])
	}
	textPart := parts[0].(map[string]any)
	if textPart["type"] != "text" || textPart["text"] != "describe this screenshot" {
		t.Fatalf("text part = %#v", textPart)
	}
	imagePart := parts[1].(map[string]any)
	if imagePart["type"] != "image_url" {
		t.Fatalf("image part = %#v", imagePart)
	}
	imageURL := imagePart["image_url"].(map[string]any)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageData)
	if imageURL["url"] != want {
		t.Fatalf("image url = %#v, want %q", imageURL["url"], want)
	}

	plain := messages[1].(map[string]any)
	if _, ok := plain["content"].(string); !ok {
		t.Fatalf("text-only content = %#v, want the byte-compatible string form", plain["content"])
	}
}

// TestOpenAIChatNormalizesPromptCacheUsage pins the provider-usage normalization
// (contract §18.2.6): the nested/aliased prompt-cache counters of the supported
// chat-completions providers are flattened into the keys the agent runner
// consumes (cached_tokens/cache_hit) without disturbing the token totals or
// inventing cache statistics a provider did not report.
func TestOpenAIChatNormalizesPromptCacheUsage(t *testing.T) {
	cases := []struct {
		name       string
		usage      string
		wantCached int
		wantCache  bool
		wantHit    int
	}{
		{
			name:       "openai prompt_tokens_details.cached_tokens",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":64}}`,
			wantCached: 64, wantCache: true, wantHit: 1,
		},
		{
			name:       "deepseek prompt_cache_hit_tokens",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_cache_hit_tokens":32}`,
			wantCached: 32, wantCache: true, wantHit: 1,
		},
		{
			name:       "cache_read_input_tokens alias",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"cache_read_input_tokens":48}`,
			wantCached: 48, wantCache: true, wantHit: 1,
		},
		{
			name:       "explicit zero cached tokens",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":0}}`,
			wantCached: 0, wantCache: true, wantHit: 0,
		},
		{
			// Some providers emit total_tokens=0 next to the component
			// breakdown; the total must stay the sum of the components so the
			// budget does not also add the overlapping cache counters.
			name:       "zero total with cache statistics",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":64}}`,
			wantCached: 64, wantCache: true, wantHit: 1,
		},
		{
			name:      "no cache statistics",
			usage:     `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}`,
			wantCache: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{
					"id":"chatcmpl-cache",
					"object":"chat.completion",
					"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
					"usage":`+tc.usage+`
				}`)
			}))
			defer server.Close()

			response, err := llminfra.NewClient().Chat(context.Background(), chatConfigFor(server.URL, 0), llmapp.ChatRequest{
				Model:    "fake-llm",
				Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if response.Usage["prompt_tokens"] != 100 ||
				response.Usage["completion_tokens"] != 5 ||
				response.Usage["total_tokens"] != 105 {
				t.Fatalf("usage totals = %#v, want the provider totals preserved", response.Usage)
			}
			cached, hasCache := response.Usage["cached_tokens"]
			if hasCache != tc.wantCache {
				t.Fatalf("usage cached_tokens presence = %v (%#v), want %v", hasCache, response.Usage, tc.wantCache)
			}
			if hasCache {
				if cached != tc.wantCached {
					t.Fatalf("usage cached_tokens = %d, want %d", cached, tc.wantCached)
				}
				if hit := response.Usage["cache_hit"]; hit != tc.wantHit {
					t.Fatalf("usage cache_hit = %d, want %d", hit, tc.wantHit)
				}
			} else if _, hasHit := response.Usage["cache_hit"]; hasHit {
				t.Fatalf("usage fabricated cache_hit: %#v", response.Usage)
			}
		})
	}
}

// writeChatSSE writes one fixed text/event-stream chat-completions body.
func writeChatSSE(t *testing.T, w http.ResponseWriter, sse string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, sse); err != nil {
		t.Errorf("write SSE body: %v", err)
	}
}

// chatToolCallSSE is a fixed OpenAI chat-completions stream with text, a
// reasoning_content fragment and one function call whose arguments arrive in
// two fragments (first fragment carries id/name, later fragments carry only the
// arguments fragment keyed by index). finish_reason and usage arrive in separate
// trailing chunks before [DONE] (stream_options.include_usage contract).
const chatToolCallSSE = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me "},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"check."},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"need a tool"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"requirement_draft_create","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"title\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"login\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":21,"completion_tokens":9,"total_tokens":30}}

data: [DONE]

`

// chatTextSSE is a fixed text-only chat-completions stream: two content
// fragments, a finish_reason chunk and a trailing usage chunk before [DONE].
const chatTextSSE = `data: {"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}

data: [DONE]

`

// TestChatCompletionsStreamToolCall pins streaming aggregation for the
// chat-completions protocol: text deltas, reasoning_content deltas, fragmented
// function-call arguments keyed by index, the reverse-mapped tool name and the
// terminal finish_reason/usage chunk. It also pins the streaming request wire
// (stream=true + stream_options.include_usage so OpenAI reports usage).
func TestChatCompletionsStreamToolCall(t *testing.T) {
	var body map[string]any
	var requestPath, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		accept = r.Header.Get("Accept")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("authorization = %q", got)
		}
		body = captureChatBody(t, r)
		writeChatSSE(t, w, chatToolCallSSE)
	}))
	defer server.Close()

	adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL+"/v1", 0))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}

	var text, reasoning, arguments strings.Builder
	var callID, callName, finishReason string
	var usage map[string]int
	streamErr := adapter.Stream(context.Background(), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "add a login feature"}},
		Tools: []llmapp.ToolDefinition{{
			Name:        "requirement.draft_create",
			Description: "create a requirement draft",
			Parameters:  []byte(`{"type":"object"}`),
		}},
	}, func(delta llmapp.Delta) {
		text.WriteString(delta.Content)
		reasoning.WriteString(delta.ReasoningContent)
		if delta.ToolCall != nil {
			if delta.ToolCall.ID != "" {
				callID = delta.ToolCall.ID
			}
			if delta.ToolCall.Name != "" {
				callName = delta.ToolCall.Name
			}
			arguments.WriteString(delta.ToolCall.Arguments)
		}
		if delta.FinishReason != "" {
			finishReason = delta.FinishReason
		}
		if delta.Usage != nil {
			usage = delta.Usage
		}
	})
	if streamErr != nil {
		t.Fatalf("Stream: %v", streamErr)
	}
	if requestPath != "/v1/chat/completions" {
		t.Fatalf("request path = %q, want /v1/chat/completions", requestPath)
	}
	if accept != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", accept)
	}
	if stream, _ := body["stream"].(bool); !stream {
		t.Fatalf("stream request body = %#v, want stream=true", body)
	}
	// OpenAI chat-completions only streams usage when include_usage is set.
	if opts, ok := body["stream_options"].(map[string]any); !ok || opts["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage=true", body["stream_options"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	if function := tools[0].(map[string]any)["function"].(map[string]any); function["name"] != "requirement_draft_create" {
		t.Fatalf("tool = %#v", tools[0])
	}

	if text.String() != "Let me check." {
		t.Fatalf("text = %q", text.String())
	}
	if reasoning.String() != "need a tool" {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
	if callID != "call_abc" || callName != "requirement.draft_create" || arguments.String() != `{"title":"login"}` {
		t.Fatalf("tool call = id %q name %q arguments %q", callID, callName, arguments.String())
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", finishReason)
	}
	if usage["prompt_tokens"] != 21 || usage["completion_tokens"] != 9 || usage["total_tokens"] != 30 {
		t.Fatalf("usage = %#v", usage)
	}
}

// TestChatCompletionsStreamTextOnly pins the no-tools text stream: content
// fragments concatenate, finish_reason and usage reach the terminal delta.
func TestChatCompletionsStreamTextOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, chatTextSSE)
	}))
	defer server.Close()

	adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL+"/v1", 0))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	var text, finishReason string
	var toolSeen bool
	var usage map[string]int
	if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}, func(delta llmapp.Delta) {
		text += delta.Content
		if delta.ToolCall != nil {
			toolSeen = true
		}
		if delta.FinishReason != "" {
			finishReason = delta.FinishReason
		}
		if delta.Usage != nil {
			usage = delta.Usage
		}
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "Hello world" {
		t.Fatalf("text = %q, want Hello world", text)
	}
	if toolSeen {
		t.Fatal("text-only stream emitted a tool call")
	}
	if finishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", finishReason)
	}
	if usage["prompt_tokens"] != 11 || usage["completion_tokens"] != 5 || usage["total_tokens"] != 16 {
		t.Fatalf("usage = %#v", usage)
	}
}

// TestChatCompletionsStreamEmptyOrBrokenIsTransient pins the fail-closed stream
// behavior: an empty body, a malformed event and a stream that ends before
// [DONE] are all retryable upstream failures, not empty successes.
func TestChatCompletionsStreamEmptyOrBrokenIsTransient(t *testing.T) {
	cases := []struct {
		name          string
		sse           string
		wantSubstring string
	}{
		{name: "empty", sse: "", wantSubstring: "empty"},
		{name: "malformed", sse: "data: {not json}\n\n", wantSubstring: "decode"},
		{
			name:          "truncated",
			sse:           `data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n",
			wantSubstring: "ended",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeChatSSE(t, w, tc.sse)
			}))
			defer server.Close()

			adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL+"/v1", 0))
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			err = adapter.Stream(context.Background(), llmapp.ChatRequest{
				Model:    "fake-llm",
				Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			}, func(llmapp.Delta) {})
			if err == nil {
				t.Fatal("Stream returned no error for an empty/broken stream")
			}
			var transient *llminfra.TransientError
			if !errors.As(err, &transient) {
				t.Fatalf("error = %v (%T), want TransientError", err, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantSubstring) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantSubstring)
			}
		})
	}
}

// TestChatCompletionsStreamHTTPErrorsAreClassified pins the stream error classes
// (classifyChatFailure parity): 4xx is a ProtocolError, 429/5xx are TransientError.
func TestChatCompletionsStreamHTTPErrorsAreClassified(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		wantProtocol  bool
		wantSubstring string
	}{
		{name: "bad request", status: http.StatusBadRequest, wantProtocol: true, wantSubstring: "HTTP 400"},
		{name: "unauthorized", status: http.StatusUnauthorized, wantProtocol: true, wantSubstring: "HTTP 401"},
		{name: "rate limited", status: http.StatusTooManyRequests, wantSubstring: "HTTP 429"},
		{name: "server error", status: http.StatusBadGateway, wantSubstring: "HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":{"message":"upstream rejected the request"}}`)
			}))
			defer server.Close()

			adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL, 0))
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			err = adapter.Stream(context.Background(), llmapp.ChatRequest{
				Model:    "fake-llm",
				Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			}, func(llmapp.Delta) {})
			if err == nil {
				t.Fatal("Stream succeeded on a non-2xx response")
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Fatalf("error = %q, want %q", err, tc.wantSubstring)
			}
			if tc.wantProtocol {
				var protocolErr *llminfra.ProtocolError
				if !errors.As(err, &protocolErr) {
					t.Fatalf("error = %v (%T), want ProtocolError", err, err)
				}
			} else {
				var transient *llminfra.TransientError
				if !errors.As(err, &transient) {
					t.Fatalf("error = %v (%T), want TransientError", err, err)
				}
			}
		})
	}
}

// TestChatCompletionsStreamContextOverflowIsClassified keeps the compaction
// contract: a context-window overflow is recognizable and is not retried.
func TestChatCompletionsStreamContextOverflowIsClassified(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"This model's maximum context length is 64000 tokens","code":"context_length_exceeded"}}`)
	}))
	defer server.Close()

	adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL, 2))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	err = adapter.Stream(context.Background(), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}, func(llmapp.Delta) {})
	if err == nil {
		t.Fatal("Stream succeeded on a context overflow response")
	}
	if !llmapp.IsContextOverflow(err) {
		t.Fatalf("IsContextOverflow = false for %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want no retry", got)
	}
}

// TestChatCompletionsStreamRetriesTransientBeforeFirstDelta pins the R-D7
// pre-first-delta boundary: transient HTTP failures before any delta are retried
// up to cfg.MaxRetries (parity with the Chat attempts loop).
func TestChatCompletionsStreamRetriesTransientBeforeFirstDelta(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&attempts, 1) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
		default:
			writeChatSSE(t, w, chatTextSSE)
		}
	}))
	defer server.Close()

	adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL, 2))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	var text string
	if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}, func(delta llmapp.Delta) {
		text += delta.Content
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "Hello world" {
		t.Fatalf("text = %q, want Hello world", text)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

// TestChatCompletionsStreamNoRetryAfterFirstDelta pins the R-D7 post-first-delta
// boundary: once a delta reached the caller, a later transient stream failure is
// returned directly (no retry), and the partial content already emitted stands.
func TestChatCompletionsStreamNoRetryAfterFirstDelta(t *testing.T) {
	// A stream that emits one content fragment then ends before [DONE].
	const partialSSE = `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n"
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		writeChatSSE(t, w, partialSSE)
	}))
	defer server.Close()

	adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL, 2))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	var text string
	err = adapter.Stream(context.Background(), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}, func(delta llmapp.Delta) {
		text += delta.Content
	})
	if err == nil {
		t.Fatal("Stream succeeded on a stream that ended before completion")
	}
	var transient *llminfra.TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("error = %v (%T), want TransientError", err, err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry after first delta)", got)
	}
	if text != "partial" {
		t.Fatalf("text = %q, want the partial content already emitted", text)
	}
}

// TestChatCompletionsStreamNormalizesPromptCacheUsage pins usage/cache parity
// with the non-streaming path: the terminal delta's usage is normalized through
// chatUsageMap (nested/aliased prompt-cache counters -> cached_tokens/cache_hit).
func TestChatCompletionsStreamNormalizesPromptCacheUsage(t *testing.T) {
	cases := []struct {
		name       string
		usage      string
		wantCached int
		wantCache  bool
		wantHit    int
	}{
		{
			name:       "openai prompt_tokens_details.cached_tokens",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":64}}`,
			wantCached: 64, wantCache: true, wantHit: 1,
		},
		{
			name:       "deepseek prompt_cache_hit_tokens",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_cache_hit_tokens":32}`,
			wantCached: 32, wantCache: true, wantHit: 1,
		},
		{
			name:       "cache_read_input_tokens alias",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"cache_read_input_tokens":48}`,
			wantCached: 48, wantCache: true, wantHit: 1,
		},
		{
			name:       "explicit zero cached tokens",
			usage:      `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":0}}`,
			wantCached: 0, wantCache: true, wantHit: 0,
		},
		{
			name:      "no cache statistics",
			usage:     `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}`,
			wantCache: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sse := `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n" +
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				`data: {"choices":[],"usage":` + tc.usage + `}` + "\n\n" +
				"data: [DONE]\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeChatSSE(t, w, sse)
			}))
			defer server.Close()

			adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL, 0))
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			var usage map[string]int
			if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
				Model:    "fake-llm",
				Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			}, func(delta llmapp.Delta) {
				if delta.Usage != nil {
					usage = delta.Usage
				}
			}); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if usage["prompt_tokens"] != 100 || usage["completion_tokens"] != 5 || usage["total_tokens"] != 105 {
				t.Fatalf("usage totals = %#v, want the provider totals preserved", usage)
			}
			cached, hasCache := usage["cached_tokens"]
			if hasCache != tc.wantCache {
				t.Fatalf("usage cached_tokens presence = %v (%#v), want %v", hasCache, usage, tc.wantCache)
			}
			if hasCache {
				if cached != tc.wantCached {
					t.Fatalf("usage cached_tokens = %d, want %d", cached, tc.wantCached)
				}
				if hit := usage["cache_hit"]; hit != tc.wantHit {
					t.Fatalf("usage cache_hit = %d, want %d", hit, tc.wantHit)
				}
			} else if _, hasHit := usage["cache_hit"]; hasHit {
				t.Fatalf("usage fabricated cache_hit: %#v", usage)
			}
		})
	}
}

var _ llmapp.ChatClient = (*llminfra.Client)(nil)

// chatInterleavedToolCallSSE is a fixed chat-completions stream with TWO
// parallel function calls whose fragments interleave out of order across
// indices: index 1 is announced first, and argument fragments alternate
// between the two calls. Per-index assembly must keep the calls distinct.
const chatInterleavedToolCallSSE = `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_second","type":"function","function":{"name":"tool_b","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_first","type":"function","function":{"name":"tool_a","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"b\":"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":13,"completion_tokens":8,"total_tokens":21}}

data: [DONE]

`

// TestChatCompletionsStreamInterleavedToolCalls pins per-index assembly against
// a hardcode-index-0 mutant: two parallel calls announced out of order, with
// interleaved argument fragments, must assemble into two distinct calls keyed
// by Delta.ToolCall.Index (not merged into one).
func TestChatCompletionsStreamInterleavedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeChatSSE(t, w, chatInterleavedToolCallSSE)
	}))
	defer server.Close()

	adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL+"/v1", 0))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}

	type assembled struct {
		id        string
		name      string
		arguments strings.Builder
	}
	calls := map[int]*assembled{}
	var order []int
	var finishReason string
	streamErr := adapter.Stream(context.Background(), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "run both tools"}},
		Tools: []llmapp.ToolDefinition{
			{Name: "tool.a", Description: "first", Parameters: []byte(`{"type":"object"}`)},
			{Name: "tool.b", Description: "second", Parameters: []byte(`{"type":"object"}`)},
		},
	}, func(delta llmapp.Delta) {
		if delta.FinishReason != "" {
			finishReason = delta.FinishReason
		}
		if delta.ToolCall == nil {
			return
		}
		index := delta.ToolCall.Index
		call, ok := calls[index]
		if !ok {
			call = &assembled{}
			calls[index] = call
			order = append(order, index)
		}
		if delta.ToolCall.ID != "" {
			call.id = delta.ToolCall.ID
		}
		if delta.ToolCall.Name != "" {
			call.name = delta.ToolCall.Name
		}
		call.arguments.WriteString(delta.ToolCall.Arguments)
	})
	if streamErr != nil {
		t.Fatalf("Stream: %v", streamErr)
	}
	if len(calls) != 2 {
		t.Fatalf("assembled calls = %d, want 2 distinct indices (index-0 hardcode merges them)", len(calls))
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 0 {
		t.Fatalf("announcement order = %v, want [1 0] (fixture is out of order)", order)
	}
	first, second := calls[0], calls[1]
	if first == nil || first.id != "call_first" || first.name != "tool.a" || first.arguments.String() != `{"a":1}` {
		t.Fatalf("index 0 = %#v, want call_first/tool.a/{\"a\":1}", first)
	}
	if second == nil || second.id != "call_second" || second.name != "tool.b" || second.arguments.String() != `{"b":2}` {
		t.Fatalf("index 1 = %#v, want call_second/tool.b/{\"b\":2}", second)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", finishReason)
	}
}
