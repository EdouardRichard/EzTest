package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// scriptedClient is a Client whose methods run a scripted function selected by
// the 1-based call number. It lets the retry tests count real calls and make
// each attempt return a different result.
type scriptedClient struct {
	chatCalls   int32
	streamCalls int32
	modelCalls  int32

	chat       func(ctx context.Context, call int) (ChatResponse, error)
	chatStream func(ctx context.Context, call int) (<-chan StreamEvent, error)
	listModels func(ctx context.Context, call int) ([]llm.ModelInfo, error)
}

func (c *scriptedClient) Chat(ctx context.Context, _ ChatRequest) (ChatResponse, error) {
	call := int(atomic.AddInt32(&c.chatCalls, 1))
	return c.chat(ctx, call)
}

func (c *scriptedClient) ChatStream(ctx context.Context, _ ChatRequest) (<-chan StreamEvent, error) {
	call := int(atomic.AddInt32(&c.streamCalls, 1))
	return c.chatStream(ctx, call)
}

func (c *scriptedClient) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	call := int(atomic.AddInt32(&c.modelCalls, 1))
	return c.listModels(ctx, call)
}

func (c *scriptedClient) calls() (chat, stream, models int32) {
	return atomic.LoadInt32(&c.chatCalls), atomic.LoadInt32(&c.streamCalls), atomic.LoadInt32(&c.modelCalls)
}

// llmErrorCode extracts the public API code from a typed upstream error.
func llmErrorCode(t *testing.T, err error) string {
	t.Helper()
	var llmErr *LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("err=%v (%T), want *LLMError", err, err)
	}
	return llmErr.Code
}

func rateLimitedError() error {
	return NewLLMError(platform.CodeLLMRateLimited, "llm upstream rate limited", true, 429, nil)
}

func upstreamError() error {
	return NewLLMError(platform.CodeLLMUpstreamError, "llm upstream error", true, 500, nil)
}

// TestRetryOn429ThenSuccess is the brief's retry scenario: the first upstream
// call is rate limited and the retry succeeds.
func TestRetryOn429ThenSuccess(t *testing.T) {
	firstRateLimited := true
	inner := &scriptedClient{chat: func(context.Context, int) (ChatResponse, error) {
		if firstRateLimited {
			firstRateLimited = false
			return ChatResponse{}, rateLimitedError()
		}
		return ChatResponse{Text: "ok"}, nil
	}}

	client := NewRetryClient(inner, 1, time.Millisecond, time.Second)
	res, err := client.Chat(context.Background(), ChatRequest{})
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if chat, _, _ := inner.calls(); chat != 2 {
		t.Fatalf("chat calls=%d want 2", chat)
	}
}

// TestRetryExhaustedReturnsLastError proves retries stop after the configured
// count and the final upstream error is returned.
func TestRetryExhaustedReturnsLastError(t *testing.T) {
	inner := &scriptedClient{chat: func(context.Context, int) (ChatResponse, error) {
		return ChatResponse{}, upstreamError()
	}}
	client := NewRetryClient(inner, 2, time.Millisecond, time.Second)
	_, err := client.Chat(context.Background(), ChatRequest{})
	if got := llmErrorCode(t, err); got != platform.CodeLLMUpstreamError {
		t.Fatalf("code=%q want %q", got, platform.CodeLLMUpstreamError)
	}
	if chat, _, _ := inner.calls(); chat != 3 {
		t.Fatalf("chat calls=%d want 3 (1 attempt + 2 retries)", chat)
	}
}

// TestRetryDoesNotRetryAuthFailure proves non-retryable errors are surfaced
// immediately.
func TestRetryDoesNotRetryAuthFailure(t *testing.T) {
	inner := &scriptedClient{chat: func(context.Context, int) (ChatResponse, error) {
		return ChatResponse{}, NewLLMError(platform.CodeLLMAuthFailed, "llm authentication failed", false, 401, nil)
	}}
	client := NewRetryClient(inner, 3, time.Millisecond, time.Second)
	_, err := client.Chat(context.Background(), ChatRequest{})
	if got := llmErrorCode(t, err); got != platform.CodeLLMAuthFailed {
		t.Fatalf("code=%q want %q", got, platform.CodeLLMAuthFailed)
	}
	if chat, _, _ := inner.calls(); chat != 1 {
		t.Fatalf("chat calls=%d want 1", chat)
	}
}

// TestRetryStreamOpensAfterRateLimit proves a stream that fails before it
// starts can be retried.
func TestRetryStreamOpensAfterRateLimit(t *testing.T) {
	inner := &scriptedClient{chatStream: func(_ context.Context, call int) (<-chan StreamEvent, error) {
		if call == 1 {
			return nil, rateLimitedError()
		}
		events := make(chan StreamEvent, 1)
		events <- StreamEvent{Done: true, Usage: Usage{TotalTokens: 3}}
		close(events)
		return events, nil
	}}
	client := NewRetryClient(inner, 1, time.Millisecond, time.Second)
	events, err := client.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	event := <-events
	if !event.Done || event.Usage.TotalTokens != 3 {
		t.Fatalf("event=%+v", event)
	}
	if _, stream, _ := inner.calls(); stream != 2 {
		t.Fatalf("stream calls=%d want 2", stream)
	}
}

// TestRetryAfterStreamStartedIsNotRetried proves once the stream has produced a
// byte, a later failure is reported through the channel instead of restarting
// the request.
func TestRetryAfterStreamStartedIsNotRetried(t *testing.T) {
	inner := &scriptedClient{chatStream: func(context.Context, int) (<-chan StreamEvent, error) {
		events := make(chan StreamEvent, 2)
		events <- StreamEvent{Delta: "partial"}
		events <- StreamEvent{Err: NewLLMError(platform.CodeLLMInvalidResponse, "llm stream ended unexpectedly", false, 0, nil)}
		close(events)
		return events, nil
	}}
	client := NewRetryClient(inner, 3, time.Millisecond, time.Second)
	events, err := client.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	var got []StreamEvent
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 2 || got[0].Delta != "partial" || got[1].Err == nil {
		t.Fatalf("events=%+v", got)
	}
	if _, stream, _ := inner.calls(); stream != 1 {
		t.Fatalf("stream calls=%d want 1 after the stream started", stream)
	}
}

// TestRetryTimeoutIsBounded proves the configured timeout caps the whole
// operation, including retries.
func TestRetryTimeoutIsBounded(t *testing.T) {
	inner := &scriptedClient{chat: func(ctx context.Context, _ int) (ChatResponse, error) {
		<-ctx.Done()
		return ChatResponse{}, NewLLMError(platform.CodeLLMTimeout, "llm request timed out", true, 0, ctx.Err())
	}}
	client := NewRetryClient(inner, 3, 10*time.Millisecond, 30*time.Millisecond)
	start := time.Now()
	_, err := client.Chat(context.Background(), ChatRequest{})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("retry operation took %v, timeout budget was 30ms", elapsed)
	}
	if got := llmErrorCode(t, err); got != platform.CodeLLMTimeout {
		t.Fatalf("code=%q want %q", got, platform.CodeLLMTimeout)
	}
	if chat, _, _ := inner.calls(); chat != 1 {
		t.Fatalf("chat calls=%d, want 1 after the timeout budget was exhausted", chat)
	}
}

// TestRetryListModels proves catalog refresh uses the same retry policy.
func TestRetryListModels(t *testing.T) {
	inner := &scriptedClient{listModels: func(_ context.Context, call int) ([]llm.ModelInfo, error) {
		if call == 1 {
			return nil, upstreamError()
		}
		return []llm.ModelInfo{{ID: "deepseek-flash"}}, nil
	}}
	client := NewRetryClient(inner, 1, time.Millisecond, time.Second)
	models, err := client.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "deepseek-flash" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if _, _, calls := inner.calls(); calls != 2 {
		t.Fatalf("model calls=%d want 2", calls)
	}
}
