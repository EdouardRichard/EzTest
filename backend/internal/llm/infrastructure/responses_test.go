package llminfra_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	llminfra "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
)

// responsesConfig returns the OpenAI test fixture bound to the Responses
// protocol. It keeps the same httptest base URL and secret assertions as the
// chat-completions tests.
func responsesConfig(baseURL string, maxRetries int) llm.Config {
	cfg := openAITestConfig(baseURL)
	cfg.Protocol = string(llm.ProtocolOpenAIResponses)
	cfg.MaxRetries = maxRetries
	return cfg
}

// writeResponsesSSE writes one fixed text/event-stream body.
func writeResponsesSSE(t *testing.T, w http.ResponseWriter, sse string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, sse); err != nil {
		t.Errorf("write SSE body: %v", err)
	}
}

// TestResponsesNonStream pins the /responses wire format: input items (message,
// function_call, function_call_output), flat function tools, output item
// parsing, finish reason and token usage.
func TestResponsesNonStream(t *testing.T) {
	var body map[string]any
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("authorization = %q", got)
		}
		body = captureChatBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp_test",
			"object":"response",
			"status":"completed",
			"output":[
				{"type":"reasoning","id":"rs_1","summary":[]},
				{"type":"message","id":"msg_1","role":"assistant","content":[
					{"type":"output_text","text":"hello ","annotations":[]},
					{"type":"output_text","text":"world","annotations":[]}
				]},
				{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"requirement_draft_create","arguments":"{\"title\":\"login\"}"}
			],
			"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16}
		}`)
	}))
	defer server.Close()

	response, err := llminfra.NewClient().Chat(context.Background(), responsesConfig(server.URL+"/v1", 0), llmapp.ChatRequest{
		Model:           "fake-llm",
		MaxOutputTokens: 8192,
		Messages: []llmapp.ChatMessage{
			{Role: "system", Content: "[profile:requirement_curator] system prompt"},
			{Role: "user", Content: "add a login feature"},
			{Role: "assistant", ToolCalls: []llmapp.ToolCall{{
				ID:        "call_prev",
				Name:      "requirement.draft_create",
				Arguments: `{"title":"login"}`,
			}}},
			{Role: "tool", ToolCallID: "call_prev", Content: `{"ok":true}`},
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
	if requestPath != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requestPath)
	}

	if response.Content != "hello world" {
		t.Fatalf("content = %q, want both output_text parts concatenated", response.Content)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
	call := response.ToolCalls[0]
	if call.ID != "call_abc" || call.Name != "requirement.draft_create" || call.Arguments != `{"title":"login"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if response.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", response.FinishReason)
	}
	if response.Usage["prompt_tokens"] != 11 || response.Usage["completion_tokens"] != 5 || response.Usage["total_tokens"] != 16 {
		t.Fatalf("usage = %#v", response.Usage)
	}

	if body["model"] != "fake-llm" {
		t.Fatalf("model = %#v", body["model"])
	}
	if body["max_output_tokens"] != float64(8192) {
		t.Fatalf("max_output_tokens = %#v", body["max_output_tokens"])
	}
	if stream, ok := body["stream"]; ok {
		t.Fatalf("non-streaming request sent stream = %#v", stream)
	}

	input, ok := body["input"].([]any)
	if !ok || len(input) != 4 {
		t.Fatalf("input = %#v", body["input"])
	}
	system := input[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "[profile:requirement_curator] system prompt" {
		t.Fatalf("system input item = %#v", system)
	}
	user := input[1].(map[string]any)
	if user["role"] != "user" || user["content"] != "add a login feature" {
		t.Fatalf("user input item = %#v", user)
	}
	previousCall := input[2].(map[string]any)
	if previousCall["type"] != "function_call" || previousCall["call_id"] != "call_prev" {
		t.Fatalf("function_call input item = %#v", previousCall)
	}
	if previousCall["name"] != "requirement_draft_create" || previousCall["arguments"] != `{"title":"login"}` {
		t.Fatalf("function_call input item = %#v", previousCall)
	}
	toolOutput := input[3].(map[string]any)
	if toolOutput["type"] != "function_call_output" || toolOutput["call_id"] != "call_prev" || toolOutput["output"] != `{"ok":true}` {
		t.Fatalf("function_call_output input item = %#v", toolOutput)
	}

	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", body["tool_choice"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "requirement_draft_create" {
		t.Fatalf("tool = %#v", tool)
	}
	if _, ok := tool["parameters"].(map[string]any); !ok {
		t.Fatalf("tool parameters = %#v", tool["parameters"])
	}
}

// responsesToolCallSSE is a fixed Responses stream with text, a reasoning
// summary and one function call whose arguments arrive in two fragments.
const responsesToolCallSSE = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Let me "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"check."}

event: response.reasoning_summary.delta
data: {"type":"response.reasoning_summary.delta","item_id":"rs_1","output_index":1,"summary_index":0,"delta":"need a tool"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"requirement_draft_create","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"title\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"\"login\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"requirement_draft_create","arguments":"{\"title\":\"login\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":21,"output_tokens":9,"total_tokens":30}}}

`

// TestResponsesStreamToolCall pins streaming aggregation: text deltas,
// reasoning-summary deltas, fragmented function-call arguments and the terminal
// finish/usage chunk.
func TestResponsesStreamToolCall(t *testing.T) {
	var body map[string]any
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		body = captureChatBody(t, r)
		writeResponsesSSE(t, w, responsesToolCallSSE)
	}))
	defer server.Close()

	adapter, err := llminfra.NewAdapter(responsesConfig(server.URL+"/v1", 0))
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
	if requestPath != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requestPath)
	}
	if stream, _ := body["stream"].(bool); !stream {
		t.Fatalf("stream request body = %#v, want stream=true", body)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	if tool := tools[0].(map[string]any); tool["name"] != "requirement_draft_create" {
		t.Fatalf("tool = %#v", tool)
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
		t.Fatalf("finish reason = %q", finishReason)
	}
	if usage["prompt_tokens"] != 21 || usage["completion_tokens"] != 9 || usage["total_tokens"] != 30 {
		t.Fatalf("usage = %#v", usage)
	}
}

// TestResponsesStreamEmptyOrBrokenIsTransient pins the fail-closed stream
// behavior: an empty body, a malformed event and a stream that ends before a
// terminal event are all retryable upstream failures, not empty successes.
func TestResponsesStreamEmptyOrBrokenIsTransient(t *testing.T) {
	cases := []struct {
		name          string
		sse           string
		wantSubstring string
	}{
		{name: "empty", sse: "", wantSubstring: "empty"},
		{name: "malformed", sse: "data: {not json}\n\n", wantSubstring: "decode"},
		{
			name:          "truncated",
			sse:           "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
			wantSubstring: "ended",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeResponsesSSE(t, w, tc.sse)
			}))
			defer server.Close()

			adapter, err := llminfra.NewAdapter(responsesConfig(server.URL+"/v1", 0))
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

// writeResponsesCompletion writes a minimal successful non-streaming reply.
func writeResponsesCompletion(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{
		"id":"resp_retry",
		"object":"response",
		"status":"completed",
		"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"`+content+`"}]}],
		"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
	}`)
}

// TestResponsesClientErrorIsProtocolError pins the 4xx classification: a
// parameter/capability rejection is a typed ProtocolError, is not retried, and
// never leaks the configured API key.
func TestResponsesClientErrorIsProtocolError(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid parameter for `+testAPIKey+`"}}`)
	}))
	defer server.Close()

	_, err := llminfra.NewClient().Chat(context.Background(), responsesConfig(server.URL, 3), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on HTTP 400")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1 (4xx must not be retried)", got)
	}
	var protocolErr *llminfra.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %v (%T), want ProtocolError", err, err)
	}
	if protocolErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("protocol error status = %d", protocolErr.StatusCode)
	}
	message := err.Error()
	if !strings.Contains(message, "HTTP 400") || !strings.Contains(message, "invalid parameter") {
		t.Fatalf("error = %q", message)
	}
	if strings.Contains(message, testAPIKey) {
		t.Fatalf("api key leaked in error %q", message)
	}
}

// TestResponsesRetriesTransientFailures pins the retryable class: 429 and 5xx
// are retried up to cfg.MaxRetries before the successful attempt.
func TestResponsesRetriesTransientFailures(t *testing.T) {
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
			writeResponsesCompletion(w, "received")
		}
	}))
	defer server.Close()

	response, err := llminfra.NewClient().Chat(context.Background(), responsesConfig(server.URL, 2), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if response.Content != "received" {
		t.Fatalf("response = %#v", response)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

// TestResponsesEmptyResponseIsTransient pins the empty-response rule: a 200
// reply without output items is retried like a transient upstream failure.
func TestResponsesEmptyResponseIsTransient(t *testing.T) {
	t.Run("retried", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&attempts, 1) < 3 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"status":"completed","output":[]}`)
				return
			}
			writeResponsesCompletion(w, "recovered")
		}))
		defer server.Close()

		response, err := llminfra.NewClient().Chat(context.Background(), responsesConfig(server.URL, 2), llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if response.Content != "recovered" {
			t.Fatalf("response = %#v", response)
		}
		if got := atomic.LoadInt32(&attempts); got != 3 {
			t.Fatalf("attempts = %d, want 3", got)
		}
	})

	t.Run("classified", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"completed","output":[]}`)
		}))
		defer server.Close()

		_, err := llminfra.NewClient().Chat(context.Background(), responsesConfig(server.URL, 0), llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
		})
		if err == nil {
			t.Fatal("Chat accepted an empty response")
		}
		var transient *llminfra.TransientError
		if !errors.As(err, &transient) {
			t.Fatalf("error = %v (%T), want TransientError", err, err)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "empty") {
			t.Fatalf("error = %q, want it to mention the empty response", err)
		}
	})
}

// TestResponsesContextOverflowIsClassified keeps the compaction contract the
// chat-completions adapter already honours.
func TestResponsesContextOverflowIsClassified(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"This model's maximum context length is 64000 tokens","code":"context_length_exceeded"}}`)
	}))
	defer server.Close()

	_, err := llminfra.NewClient().Chat(context.Background(), responsesConfig(server.URL, 2), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat succeeded on a context overflow response")
	}
	if !llmapp.IsContextOverflow(err) {
		t.Fatalf("IsContextOverflow = false for %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want no retry", got)
	}
}

// TestResponsesStreamHTTPErrorsAreClassified pins the stream error classes: 4xx
// is a ProtocolError, 429/5xx are TransientError.
func TestResponsesStreamHTTPErrorsAreClassified(t *testing.T) {
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

			adapter, err := llminfra.NewAdapter(responsesConfig(server.URL, 0))
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
