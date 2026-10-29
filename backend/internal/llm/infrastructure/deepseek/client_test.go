package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/deepseek"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// testClient builds the adapter under test against a mock upstream.
func testClient(baseURL string, cfg llm.Config, hc *http.Client) llmapp.Client {
	cfg.BaseURL = baseURL
	if cfg.Model == "" {
		cfg.Model = "deepseek-flash"
	}
	if cfg.Protocol == "" {
		cfg.Protocol = llm.ProtocolChat
	}
	return deepseek.NewDeepSeekClient(cfg, hc)
}

// decodeBody reads one JSON request body.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

// writeSSE writes one SSE frame and flushes it.
func writeSSE(t *testing.T, w http.ResponseWriter, event, data string) {
	t.Helper()
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("test server ResponseWriter is not a Flusher")
	}
	flusher.Flush()
}

// collectStream drains a normalized stream channel with a watchdog so a broken
// implementation fails the test instead of hanging it.
func collectStream(t *testing.T, events <-chan llmapp.StreamEvent) []llmapp.StreamEvent {
	t.Helper()
	var got []llmapp.StreamEvent
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return got
			}
			got = append(got, event)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for stream events, got %+v", got)
		}
	}
}

func TestChatCompletionsNonStreamAndImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer sk-test"; got != want {
			t.Errorf("Authorization=%q want %q", got, want)
		}
		body := decodeBody(t, r)
		if body["model"] != "deepseek-flash" {
			t.Errorf("model=%v", body["model"])
		}
		if body["stream"] != false {
			t.Errorf("stream=%v want false", body["stream"])
		}
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 1 {
			t.Fatalf("messages=%v", body["messages"])
		}
		content, ok := msgs[0].(map[string]any)["content"].([]any)
		if !ok || len(content) != 2 {
			t.Fatalf("content=%v", msgs[0])
		}
		if content[0].(map[string]any)["type"] != "text" || content[0].(map[string]any)["text"] != "describe" {
			t.Errorf("text part=%v", content[0])
		}
		image, ok := content[1].(map[string]any)
		if !ok || image["type"] != "image_url" {
			t.Fatalf("no image_url part: %v", content[1])
		}
		imageURL, ok := image["image_url"].(map[string]any)
		if !ok || imageURL["url"] != "data:image/png;base64,AA==" {
			t.Errorf("image_url=%v", image["image_url"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	res, err := c.Chat(context.Background(), llmapp.ChatRequest{Model: "deepseek-flash", Messages: []llmapp.Message{{
		Role: "user",
		Content: []llmapp.Part{
			{Type: "text", Text: "describe"},
			{Type: "image_url", ImageURL: "data:image/png;base64,AA=="},
		},
	}}})
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if res.Usage.PromptTokens != 1 || res.Usage.CompletionTokens != 2 || res.Usage.TotalTokens != 3 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestResponsesNonStreamAndImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		body := decodeBody(t, r)
		if body["model"] != "deepseek-flash" || body["stream"] != false {
			t.Errorf("body=%v", body)
		}
		input, ok := body["input"].([]any)
		if !ok || len(input) != 1 {
			t.Fatalf("input=%v", body["input"])
		}
		content, ok := input[0].(map[string]any)["content"].([]any)
		if !ok || len(content) != 2 {
			t.Fatalf("content=%v", input[0])
		}
		text, ok := content[0].(map[string]any)
		if !ok || text["type"] != "input_text" || text["text"] != "describe" {
			t.Errorf("text part=%v", content[0])
		}
		image, ok := content[1].(map[string]any)
		if !ok || image["type"] != "input_image" {
			t.Fatalf("no input_image part: %v", content[1])
		}
		if image["image_url"] != "data:image/png;base64,AA==" {
			t.Errorf("image_url=%v want a plain string", image["image_url"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{Protocol: llm.ProtocolResponses, APIKey: "sk-test"}, srv.Client())
	res, err := c.Chat(context.Background(), llmapp.ChatRequest{Model: "deepseek-flash", Messages: []llmapp.Message{{
		Role: "user",
		Content: []llmapp.Part{
			{Type: "text", Text: "describe"},
			{Type: "image_url", ImageURL: "data:image/png;base64,AA=="},
		},
	}}})
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if res.Usage.PromptTokens != 1 || res.Usage.CompletionTokens != 2 || res.Usage.TotalTokens != 3 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestChatStreamNormalizesDeltasAndDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if body := decodeBody(t, r); body["stream"] != true {
			t.Errorf("stream=%v want true", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "", `{"choices":[{"delta":{"content":"he"}}]}`)
		writeSSE(t, w, "", `{"choices":[{"delta":{"content":"llo"}}]}`)
		writeSSE(t, w, "", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
		writeSSE(t, w, "", `[DONE]`)
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	events, err := c.ChatStream(context.Background(), llmapp.ChatRequest{
		Model:    "deepseek-flash",
		Stream:   true,
		Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	got := collectStream(t, events)
	var deltas []string
	var done []llmapp.StreamEvent
	for _, event := range got {
		if event.Err != nil {
			t.Fatalf("unexpected stream error: %v", event.Err)
		}
		if event.Delta != "" {
			deltas = append(deltas, event.Delta)
		}
		if event.Done {
			done = append(done, event)
		}
	}
	if strings.Join(deltas, "") != "hello" {
		t.Fatalf("deltas=%v want hello", deltas)
	}
	if len(done) != 1 || done[0].Usage.TotalTokens != 3 {
		t.Fatalf("done events=%+v", done)
	}
}

func TestResponsesStreamNormalizesDeltasAndDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hi"}`)
		writeSSE(t, w, "response.completed", `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{Protocol: llm.ProtocolResponses, APIKey: "sk-test"}, srv.Client())
	events, err := c.ChatStream(context.Background(), llmapp.ChatRequest{
		Model:    "deepseek-flash",
		Stream:   true,
		Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	got := collectStream(t, events)
	var deltas []string
	sawDone := false
	for _, event := range got {
		if event.Err != nil {
			t.Fatalf("unexpected stream error: %v", event.Err)
		}
		if event.Delta != "" {
			deltas = append(deltas, event.Delta)
		}
		if event.Done {
			sawDone = true
			if event.Usage.TotalTokens != 3 {
				t.Fatalf("done usage=%+v", event.Usage)
			}
		}
	}
	if strings.Join(deltas, "") != "Hi" || !sawDone {
		t.Fatalf("deltas=%v done=%v", deltas, sawDone)
	}
}

// TestStreamErrorAfterFirstByteIsNotRetried reproduces the brief's scenario:
// the upstream disconnects after one delta. The adapter must not restart the
// request; the error is delivered through the normalized stream.
func TestStreamErrorAfterFirstByteIsNotRetried(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "", `{"choices":[{"delta":{"content":"partial"}}]}`)
		// Returning closes the connection without [DONE].
	}))
	defer srv.Close()

	inner := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	c := llmapp.NewRetryClient(inner, 3, time.Millisecond, 2*time.Second)
	events, err := c.ChatStream(context.Background(), llmapp.ChatRequest{
		Model:    "deepseek-flash",
		Stream:   true,
		Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	got := collectStream(t, events)
	if len(got) != 2 || got[0].Delta != "partial" || got[1].Err == nil {
		t.Fatalf("events=%+v", got)
	}
	var llmErr *llmapp.LLMError
	if !errors.As(got[1].Err, &llmErr) || llmErr.Code != platform.CodeLLMInvalidResponse {
		t.Fatalf("stream error=%v want %q", got[1].Err, platform.CodeLLMInvalidResponse)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("upstream requests=%d, want 1: the stream must not be retried after its first byte", got)
	}
}

func TestModelsContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-flash","context_window":1048576,"max_output_tokens":8192,"input_modalities":["text","image"]}]}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	models, err := c.ListModels(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	model := models[0]
	if model.ID != "deepseek-flash" {
		t.Fatalf("id=%q", model.ID)
	}
	if model.ContextWindow == nil || *model.ContextWindow != 1048576 {
		t.Fatalf("context_window=%v want 1048576", model.ContextWindow)
	}
	if model.MaxOutputTokens == nil || *model.MaxOutputTokens != 8192 {
		t.Fatalf("max_output_tokens=%v want 8192", model.MaxOutputTokens)
	}
	if len(model.InputModalities) != 2 || model.InputModalities[0] != "text" || model.InputModalities[1] != "image" {
		t.Fatalf("input_modalities=%v", model.InputModalities)
	}
}

// TestBaseURLNormalization pins the design's rule: a base already ending in
// /v1 is used as-is, otherwise /v1 is inserted.
func TestBaseURLNormalization(t *testing.T) {
	cases := []struct {
		name     string
		basePath string
		wantPath string
	}{
		{name: "no version", basePath: "", wantPath: "/v1/models"},
		{name: "with version", basePath: "/v1", wantPath: "/v1/models"},
		{name: "with trailing slash", basePath: "/v1/", wantPath: "/v1/models"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-flash"}]}`))
			}))
			defer srv.Close()
			c := testClient(srv.URL+tc.basePath, llm.Config{APIKey: "sk-test"}, srv.Client())
			if _, err := c.ListModels(context.Background()); err != nil {
				t.Fatalf("err=%v", err)
			}
			if path != tc.wantPath {
				t.Fatalf("path=%q want %q", path, tc.wantPath)
			}
		})
	}
}

// TestUpstreamErrorMapping pins the llm_* error codes, retryability and
// upstream_status detail from the design.
func TestUpstreamErrorMapping(t *testing.T) {
	cases := []struct {
		status    int
		wantCode  string
		retryable bool
	}{
		{status: http.StatusUnauthorized, wantCode: platform.CodeLLMAuthFailed},
		{status: http.StatusForbidden, wantCode: platform.CodeLLMAuthFailed},
		{status: http.StatusTooManyRequests, wantCode: platform.CodeLLMRateLimited, retryable: true},
		{status: http.StatusBadRequest, wantCode: platform.CodeLLMBadRequest},
		{status: http.StatusUnprocessableEntity, wantCode: platform.CodeLLMBadRequest},
		{status: http.StatusInternalServerError, wantCode: platform.CodeLLMUpstreamError, retryable: true},
		{status: http.StatusServiceUnavailable, wantCode: platform.CodeLLMUpstreamError, retryable: true},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
			}))
			defer srv.Close()
			c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
			_, err := c.Chat(context.Background(), llmapp.ChatRequest{
				Model:    "deepseek-flash",
				Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "hi"}}}},
			})
			var llmErr *llmapp.LLMError
			if !errors.As(err, &llmErr) {
				t.Fatalf("err=%v (%T), want *LLMError", err, err)
			}
			if llmErr.Code != tc.wantCode {
				t.Fatalf("code=%q want %q", llmErr.Code, tc.wantCode)
			}
			if llmErr.Retryable != tc.retryable {
				t.Fatalf("retryable=%v want %v", llmErr.Retryable, tc.retryable)
			}
			if llmErr.UpstreamStatus != tc.status {
				t.Fatalf("upstream_status=%d want %d", llmErr.UpstreamStatus, tc.status)
			}
		})
	}
}

func TestInvalidJSONResponseMapping(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "not json", body: `nope`},
		{name: "no choices", body: `{"choices":[]}`},
		{name: "no content", body: `{"choices":[{"message":{}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
			_, err := c.Chat(context.Background(), llmapp.ChatRequest{
				Model:    "deepseek-flash",
				Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "hi"}}}},
			})
			var llmErr *llmapp.LLMError
			if !errors.As(err, &llmErr) || llmErr.Code != platform.CodeLLMInvalidResponse {
				t.Fatalf("err=%v want %q", err, platform.CodeLLMInvalidResponse)
			}
		})
	}
}

func TestTimeoutMapping(t *testing.T) {
	// The handler deliberately answers only after the caller's deadline: the
	// adapter must map the expired context to llm_timeout. It must not wait on
	// r.Context().Done() because an unresponsive test handler would then block
	// httptest.Server.Close.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test"}, srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Chat(ctx, llmapp.ChatRequest{
		Model:    "deepseek-flash",
		Messages: []llmapp.Message{{Role: "user", Content: []llmapp.Part{{Type: "text", Text: "hi"}}}},
	})
	var llmErr *llmapp.LLMError
	if !errors.As(err, &llmErr) || llmErr.Code != platform.CodeLLMTimeout {
		t.Fatalf("err=%v want %q", err, platform.CodeLLMTimeout)
	}
	if !llmErr.Retryable {
		t.Fatal("timeout must be retryable")
	}
}

func TestCustomHeadersAreApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Custom"); got != "yes" {
			t.Errorf("X-Custom=%q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-flash"}]}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL, llm.Config{APIKey: "sk-test", Headers: map[string]string{"X-Custom": "yes"}}, srv.Client())
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
}
