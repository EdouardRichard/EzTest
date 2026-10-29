package application

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// jitterWindow is the maximum random addition to the fixed retry interval, from
// the design's "fixed interval + 0-100ms jitter".
const jitterWindow = 100 * time.Millisecond

// retryClient retries rate-limited, 5xx and timeout failures with a fixed
// interval plus jitter. The timeout bounds the whole operation, retries
// included. A streaming call is retried only while it has not produced any
// event; once the stream is open, later failures are delivered through the
// channel unchanged.
type retryClient struct {
	client     Client
	retryCount int
	interval   time.Duration
	timeout    time.Duration
}

// NewRetryClient wraps client with the configured retry policy. retryCount is
// the number of retries after the first attempt; interval is the fixed wait
// between attempts; timeout bounds the whole operation. Non-positive values mean
// no timeout and zero interval.
func NewRetryClient(client Client, retryCount int, interval, timeout time.Duration) Client {
	if client == nil {
		return nil
	}
	if retryCount < 0 {
		retryCount = 0
	}
	if interval < 0 {
		interval = 0
	}
	return &retryClient{
		client:     client,
		retryCount: retryCount,
		interval:   interval,
		timeout:    timeout,
	}
}

// Chat runs a non-streaming completion, retrying retryable failures.
func (c *retryClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	opCtx, cancel := c.operationContext(ctx)
	defer cancel()

	for attempt := 0; ; attempt++ {
		res, err := c.client.Chat(opCtx, req)
		if err == nil {
			return res, nil
		}
		if attempt >= c.retryCount || !retryable(err) {
			return ChatResponse{}, err
		}
		if err := c.wait(opCtx); err != nil {
			return ChatResponse{}, timeoutError(err)
		}
	}
}

// ChatStream opens a streaming completion. Retries stop as soon as the
// underlying client returns a stream: once bytes may have reached the caller,
// restarting would duplicate output.
func (c *retryClient) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	opCtx, cancel := c.operationContext(ctx)

	for attempt := 0; ; attempt++ {
		events, err := c.client.ChatStream(opCtx, req)
		if err == nil {
			return c.wrapStream(opCtx, cancel, events), nil
		}
		if attempt >= c.retryCount || !retryable(err) {
			cancel()
			return nil, err
		}
		if err := c.wait(opCtx); err != nil {
			cancel()
			return nil, timeoutError(err)
		}
	}
}

// ListModels refreshes the model catalog with the same retry policy.
func (c *retryClient) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	opCtx, cancel := c.operationContext(ctx)
	defer cancel()

	for attempt := 0; ; attempt++ {
		models, err := c.client.ListModels(opCtx)
		if err == nil {
			return models, nil
		}
		if attempt >= c.retryCount || !retryable(err) {
			return nil, err
		}
		if err := c.wait(opCtx); err != nil {
			return nil, timeoutError(err)
		}
	}
}

// operationContext applies the configured timeout to the whole operation. The
// returned cancel must be called by the caller, except for a successfully
// opened stream where wrapStream owns it.
func (c *retryClient) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

// wait sleeps for the fixed interval plus jitter while respecting the operation
// deadline. It returns the context error when the deadline expires first.
func (c *retryClient) wait(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	delay := c.interval + jitter()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// jitter returns a random duration in [0, jitterWindow).
func jitter() time.Duration {
	if jitterWindow <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(jitterWindow)))
}

// retryable reports whether err is a retryable LLM failure.
func retryable(err error) bool {
	llmErr, ok := AsLLMError(err)
	return ok && llmErr.Retryable
}

// timeoutError turns a context expiry during the retry wait into the public
// timeout error. The last attempt's error is preferred when it is already typed.
func timeoutError(err error) error {
	if llmErr, ok := AsLLMError(err); ok {
		return llmErr
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return NewLLMError(platform.CodeLLMTimeout, "llm request timed out", true, 0, err)
	}
	return err
}

// wrapStream forwards the normalized stream and releases the operation context
// when the source channel closes. It never retries: the stream is live.
func (c *retryClient) wrapStream(ctx context.Context, cancel context.CancelFunc, source <-chan StreamEvent) <-chan StreamEvent {
	out := make(chan StreamEvent)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-source:
				if !ok {
					return
				}
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
