package llminfra_test

// H-E5/H-E6/H-E7/H-E8/H-E12 (D 交接 §4.1i #3/#4/#5/#6 + 新 max_output): the
// streaming test gaps the D subproject handed to H. Each test pins a contract
// branch that had no regression gate. Expectations come from the contracts
// (R-D7 first-delta retry boundary, the usage telemetry parity rule, the
// transient failure taxonomy, the tool-call identity contract and the zero
// capability reduction rule SC-H8), never from observed output.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	llminfra "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
)

// TestStreamForwardsConfiguredMaxOutputWithoutClamp pins the SC-H8 zero
// capability reduction rule at the wire: a model configured for max_output above
// MaxRequestOutputTokens has that limit forwarded VERBATIM by both protocol
// adapters. Clamping the forwarded limit to 16000 would starve a long turn
// (constitution II.7) — the budget fix scales the reader budget instead (H-E12),
// it must never clamp the request. A future "fix" that clamps max_tokens here
// fails this test.
func TestStreamForwardsConfiguredMaxOutputWithoutClamp(t *testing.T) {
	const configuredMaxOutput = 110_000
	cases := []struct {
		name       string
		cfg        func(string) llm.Config
		limitKey   string
		sse        string
		writeSSE   func(*testing.T, http.ResponseWriter, string)
		pathSuffix string
	}{
		{
			name:       "chat-completions forwards max_tokens verbatim",
			cfg:        func(url string) llm.Config { return chatConfigFor(url, 0) },
			limitKey:   "max_tokens",
			sse:        chatTextSSE,
			writeSSE:   writeChatSSE,
			pathSuffix: "/v1/chat/completions",
		},
		{
			name:       "responses forwards max_output_tokens verbatim",
			cfg:        func(url string) llm.Config { return responsesConfig(url, 0) },
			limitKey:   "max_output_tokens",
			sse:        responsesTextSSE,
			writeSSE:   writeResponsesSSE,
			pathSuffix: "/v1/responses",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body = captureChatBody(t, r)
				tc.writeSSE(t, w, tc.sse)
			}))
			defer server.Close()

			adapter, err := llminfra.NewAdapter(tc.cfg(server.URL + "/v1"))
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
				Model:           "fake-llm",
				MaxOutputTokens: configuredMaxOutput,
				Messages:        []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			}, func(llmapp.Delta) {}); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if got := body[tc.limitKey]; got != float64(configuredMaxOutput) {
				t.Fatalf("%s = %#v, want the configured %d forwarded verbatim (no clamp to %d)",
					tc.limitKey, got, configuredMaxOutput, llmapp.MaxRequestOutputTokens)
			}
		})
	}
}

// responsesTextSSE is a minimal text-only Responses stream with a terminal
// response.completed carrying usage.
const responsesTextSSE = `event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"delta":" world"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16}}}

`

// TestStreamUsageAbsent pins H-E5 (usage telemetry parity, absent side): when the
// provider emits NO usage frame, the terminal delta carries a NIL usage map — the
// adapter must not fabricate a zero-valued usage snapshot. The "has usage" side is
// pinned by TestChatCompletionsStreamNormalizesPromptCacheUsage /
// TestResponsesStreamToolCall; this is the missing "absent" side. Both protocols.
func TestStreamUsageAbsent(t *testing.T) {
	const chatNoUsageSSE = `data: {"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	const responsesNoUsageSSE = `event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"delta":"Hello"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`
	cases := []struct {
		name     string
		cfg      func(string) llm.Config
		sse      string
		writeSSE func(*testing.T, http.ResponseWriter, string)
	}{
		{name: "chat-completions without a usage chunk", cfg: func(url string) llm.Config { return chatConfigFor(url, 0) }, sse: chatNoUsageSSE, writeSSE: writeChatSSE},
		{name: "responses without usage in response.completed", cfg: func(url string) llm.Config { return responsesConfig(url, 0) }, sse: responsesNoUsageSSE, writeSSE: writeResponsesSSE},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tc.writeSSE(t, w, tc.sse)
			}))
			defer server.Close()
			adapter, err := llminfra.NewAdapter(tc.cfg(server.URL + "/v1"))
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			var text, finishReason string
			var usage map[string]int
			var terminalUsageSeen bool
			if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
				Model:    "fake-llm",
				Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			}, func(delta llmapp.Delta) {
				text += delta.Content
				if delta.FinishReason != "" {
					finishReason = delta.FinishReason
					// The terminal delta is the one that carries finish_reason; its
					// usage must be nil when the provider reported none.
					usage = delta.Usage
					terminalUsageSeen = true
				}
			}); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if text != "Hello" {
				t.Fatalf("text = %q, want Hello", text)
			}
			if finishReason == "" {
				t.Fatal("no terminal finish_reason delta was emitted")
			}
			if !terminalUsageSeen {
				t.Fatal("the terminal delta was not observed")
			}
			if usage != nil {
				t.Fatalf("usage = %#v, want nil when the provider emits no usage frame (no fabricated zero snapshot)", usage)
			}
		})
	}
}

// TestStreamBrokenThenSuccessRetry pins H-E6 (R-D7 pre-first-delta retry): a
// stream that breaks BEFORE any delta reaches the caller (here an empty body, a
// transient fail-closed failure) is retried, and the next attempt's successful
// stream is delivered exactly once. The existing retry test covers HTTP 429/5xx
// then success; this covers the broken-STREAM then success path through the
// openai_chat.go Stream loop. No content is duplicated.
func TestStreamBrokenThenSuccessRetry(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch atomic.AddInt32(&attempts, 1) {
		case 1:
			// A 200 response whose stream is empty: consumeSSE fails it transient
			// ("empty stream") before any delta, so the retry boundary stays open.
			writeChatSSE(t, w, "")
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
	var deltaCount int
	if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}, func(delta llmapp.Delta) {
		text += delta.Content
		if delta.Content != "" {
			deltaCount++
		}
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "Hello world" {
		t.Fatalf("text = %q, want Hello world (the successful retry delivered once)", text)
	}
	if deltaCount != 2 {
		t.Fatalf("content deltas = %d, want 2 (no duplication across the retry)", deltaCount)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2 (broken stream retried once then success)", got)
	}
}

// TestStreamMidCancel pins H-E7 (adapter-level mid-stream cancel): cancelling the
// run context while the adapter is reading the stream body stops the read and
// returns a transient failure (the body read fails non-EOF), with NO retry (the
// cancelled context is never replayed) and the partial content already emitted
// preserved. The runner-level cancel is pinned separately; this is the missing
// adapter-level gate.
func TestStreamMidCancel(t *testing.T) {
	var attempts int32
	disconnected := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Hold the stream open so the client is mid-read when it cancels; exit when
		// the client disconnects (stopped reading) or the test releases.
		select {
		case <-r.Context().Done():
			select {
			case disconnected <- struct{}{}:
			default:
			}
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL, 2))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var text string
	err = adapter.Stream(ctx, llmapp.ChatRequest{
		Model:    "fake-llm",
		Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
	}, func(delta llmapp.Delta) {
		text += delta.Content
		// Cancel exactly where a real SSE reader would notice: after the first
		// delta reached the caller, mid-stream.
		cancel()
	})
	if err == nil {
		t.Fatal("a mid-stream cancel returned success")
	}
	var transient *llminfra.TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("error = %v (%T), want TransientError (the body read failed non-EOF)", err, err)
	}
	if text != "partial" {
		t.Fatalf("text = %q, want the partial content already emitted", text)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1 (a cancelled context is never retried)", got)
	}
	// The adapter stopped reading: the server observed the client disconnect.
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("the adapter did not stop reading after the cancel (server saw no disconnect)")
	}
}

// TestStreamUnannouncedIndex pins H-E8 (defensive branch): a tool-call argument
// fragment at an index whose id/name was NEVER announced must not cause the
// adapter to fabricate an identity. The emitted delta carries an EMPTY id/name,
// which is exactly the precondition the transcript seal validation rejects
// (agent/domain.AgentMessage.Validate: "tool_calls id and name are required").
// Both protocols. An announced index in the same stream keeps its identity, so
// the test discriminates fabrication from faithful forwarding.
func TestStreamUnannouncedIndex(t *testing.T) {
	t.Run("chat-completions unannounced index", func(t *testing.T) {
		// index 0 is announced (id/name); index 3 arrives only as an arguments
		// fragment with no prior announcement.
		const sse = `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_ok","type":"function","function":{"name":"requirement_draft_create","arguments":""}}]},"finish_reason":null}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":3,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":null}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
			"data: [DONE]\n\n"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeChatSSE(t, w, sse)
		}))
		defer server.Close()
		adapter, err := llminfra.NewAdapter(chatConfigFor(server.URL, 0))
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		identity := map[int][2]string{}
		args := map[int]string{}
		if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			Tools:    []llmapp.ToolDefinition{{Name: "requirement.draft_create", Parameters: []byte(`{"type":"object"}`)}},
		}, func(delta llmapp.Delta) {
			if delta.ToolCall == nil {
				return
			}
			idx := delta.ToolCall.Index
			ref := identity[idx]
			if delta.ToolCall.ID != "" {
				ref[0] = delta.ToolCall.ID
			}
			if delta.ToolCall.Name != "" {
				ref[1] = delta.ToolCall.Name
			}
			identity[idx] = ref
			args[idx] += delta.ToolCall.Arguments
		}); err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if identity[0] != [2]string{"call_ok", "requirement.draft_create"} {
			t.Fatalf("announced index 0 identity = %#v, want the announced id/name", identity[0])
		}
		if identity[3] != [2]string{"", ""} {
			t.Fatalf("unannounced index 3 identity = %#v, want empty id/name (no fabricated identity; the seal rejects it)", identity[3])
		}
		if args[3] != `{"x":1}` {
			t.Fatalf("unannounced index 3 arguments = %q, want the forwarded fragment", args[3])
		}
	})

	t.Run("responses unannounced output_index", func(t *testing.T) {
		// output_index 2 is announced; output_index 5 arrives only as an arguments
		// delta with no prior response.output_item.added.
		const sse = `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_ok","name":"requirement_draft_create","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":5,"delta":"{\"x\":1}"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeResponsesSSE(t, w, sse)
		}))
		defer server.Close()
		adapter, err := llminfra.NewAdapter(responsesConfig(server.URL, 0))
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		identity := map[int][2]string{}
		args := map[int]string{}
		if err := adapter.Stream(context.Background(), llmapp.ChatRequest{
			Model:    "fake-llm",
			Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}},
			Tools:    []llmapp.ToolDefinition{{Name: "requirement.draft_create", Parameters: []byte(`{"type":"object"}`)}},
		}, func(delta llmapp.Delta) {
			if delta.ToolCall == nil {
				return
			}
			idx := delta.ToolCall.Index
			ref := identity[idx]
			if delta.ToolCall.ID != "" {
				ref[0] = delta.ToolCall.ID
			}
			if delta.ToolCall.Name != "" {
				ref[1] = delta.ToolCall.Name
			}
			identity[idx] = ref
			args[idx] += delta.ToolCall.Arguments
		}); err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if identity[2] != [2]string{"call_ok", "requirement.draft_create"} {
			t.Fatalf("announced output_index 2 identity = %#v, want the announced id/name", identity[2])
		}
		if identity[5] != [2]string{"", ""} {
			t.Fatalf("unannounced output_index 5 identity = %#v, want empty id/name (no fabricated identity; the seal rejects it)", identity[5])
		}
		if args[5] != `{"x":1}` {
			t.Fatalf("unannounced output_index 5 arguments = %q, want the forwarded fragment", args[5])
		}
	})
}
