package application

import (
	"context"
	"errors"

	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Part is one piece of a message's content. Type is "text" for a text part or
// "image_url" for an image part; the adapters translate the type to the wire
// shape of their protocol (chat completions uses image_url, responses uses
// input_image).
type Part struct {
	Type     string
	Text     string
	ImageURL string
}

// Message is one conversation message. Content is always a list of parts so a
// user turn can mix text and an image.
type Message struct {
	Role    string
	Content []Part
}

// ChatRequest is one completion request. Model overrides the configuration's
// model when set. MaxTokens is optional.
type ChatRequest struct {
	Model     string
	Messages  []Message
	Stream    bool
	MaxTokens int
}

// Usage is the token accounting returned by the upstream.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is the parsed non-streaming response.
type ChatResponse struct {
	Text  string
	Usage Usage
}

// StreamEvent is one normalized streaming event: a delta, a terminal done (with
// usage) or a terminal error. A channel may emit deltas followed by exactly one
// Done or Err event.
type StreamEvent struct {
	Delta string
	Done  bool
	Err   error
	Usage Usage
}

// Client is the LLM gateway port. The DeepSeek adapter implements it for both
// upstream protocols; the retry wrapper decorates it without changing the
// contract.
type Client interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
	ListModels(ctx context.Context) ([]llm.ModelInfo, error)
}

// ClientFactory builds a client for a stored configuration, applying that
// configuration's protocol, retry, interval and timeout policy. The composition
// root wires the DeepSeek adapter and retry wrapper here.
type ClientFactory func(cfg llm.Config) Client

// LLMError is the typed upstream failure carried through the retry wrapper and
// mapped to the public llm_* API error envelope. It deliberately keeps the
// upstream body out of the message so a response cannot leak credentials.
type LLMError struct {
	// Code is one of the platform.CodeLLM* values.
	Code string
	// Message is the client-facing description.
	Message string
	// Retryable reports whether the failed call may be retried.
	Retryable bool
	// UpstreamStatus is the HTTP status returned by the upstream, or 0 for
	// transport and parsing failures.
	UpstreamStatus int
	// Err is the underlying error, kept for errors.Is/errors.As.
	Err error
}

// Error implements error.
func (e *LLMError) Error() string {
	if e == nil {
		return "llm: request failed"
	}
	if e.Message != "" {
		return e.Message
	}
	return "llm: request failed"
}

// Unwrap exposes the underlying cause.
func (e *LLMError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewLLMError builds a typed upstream error.
func NewLLMError(code, message string, retryable bool, upstreamStatus int, cause error) *LLMError {
	return &LLMError{
		Code:           code,
		Message:        message,
		Retryable:      retryable,
		UpstreamStatus: upstreamStatus,
		Err:            cause,
	}
}

// AsLLMError extracts the typed upstream error from err.
func AsLLMError(err error) (*LLMError, bool) {
	var llmErr *LLMError
	if err == nil || !errors.As(err, &llmErr) {
		return nil, false
	}
	return llmErr, true
}

// NotConfiguredError is the typed error used when a stored configuration has no
// usable API key. It is never retryable.
func NotConfiguredError() *LLMError {
	return NewLLMError(platform.CodeLLMNotConfigured, "llm is not configured", false, 0, nil)
}
