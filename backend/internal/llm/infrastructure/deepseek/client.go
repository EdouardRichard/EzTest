// Package deepseek is the LLM gateway's HTTP adapter for DeepSeek-compatible
// upstreams. It implements both supported protocols — chat completions and
// responses — normalizes their streaming events and maps every upstream failure
// to the typed llm_* application error.
package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// maxResponseBytes caps a non-streaming upstream body.
	maxResponseBytes = 4 << 20
	// maxSSELineBytes caps one streamed SSE line.
	maxSSELineBytes = 1 << 20
)

// client is one protocol adapter bound to a stored configuration. It is
// immutable after construction and safe for concurrent use.
type client struct {
	cfg     llm.Config
	http    *http.Client
	baseURL string
}

// NewDeepSeekClient builds the LLM client for cfg. hc may be nil, in which case
// http.DefaultClient is used. The returned client speaks the protocol selected
// by cfg.Protocol.
func NewDeepSeekClient(cfg llm.Config, hc *http.Client) llmapp.Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &client{
		cfg:     cfg,
		http:    hc,
		baseURL: normalizeBaseURL(cfg.BaseURL),
	}
}

// normalizeBaseURL implements the design's rule: a base already ending in /v1
// is used as-is, otherwise /v1 is appended.
func normalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

// endpoint joins the normalized base and a protocol path.
func (c *client) endpoint(path string) string {
	return c.baseURL + path
}

// Chat dispatches to the adapter selected by the configuration protocol.
func (c *client) Chat(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	if c.cfg.Protocol == llm.ProtocolResponses {
		return c.responsesChat(ctx, req)
	}
	return c.chatCompletions(ctx, req)
}

// ChatStream dispatches to the streaming adapter selected by the configuration
// protocol.
func (c *client) ChatStream(ctx context.Context, req llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	if c.cfg.Protocol == llm.ProtocolResponses {
		return c.responsesChatStream(ctx, req)
	}
	return c.chatCompletionsStream(ctx, req)
}

// newRequest builds an upstream request with the configured key, custom headers
// and JSON body. Custom Authorization/Content-Type headers are ignored: the
// save path already rejects them, and the adapter must never override its own
// transport headers.
func (c *client) newRequest(ctx context.Context, method, path string, payload any) (*http.Request, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, llmapp.NewLLMError(platform.CodeLLMInvalidResponse, "llm request could not be encoded", false, 0, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return nil, llmapp.NewLLMError(platform.CodeLLMBadRequest, "llm request could not be built", false, 0, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for name, value := range c.cfg.Headers {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "", "authorization", "content-type":
			continue
		}
		req.Header.Set(strings.TrimSpace(name), value)
	}
	return req, nil
}

// do executes one upstream request and maps transport and status failures. A
// non-2xx response is not returned to the caller.
func (c *client) do(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Drain a bounded prefix so the connection can be reused, then drop it:
		// the upstream body is never exposed to the client.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		return nil, statusError(resp.StatusCode)
	}
	return resp, nil
}

// decodeJSON decodes one JSON response body. Malformed upstream payloads are
// reported as llm_invalid_response.
func decodeJSON(resp *http.Response, dst any) error {
	defer func() { _ = resp.Body.Close() }()
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	if err := decoder.Decode(dst); err != nil {
		return invalidResponseError(err)
	}
	return nil
}

// statusError maps an upstream HTTP status to the public llm_* error.
func statusError(status int) *llmapp.LLMError {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return llmapp.NewLLMError(platform.CodeLLMAuthFailed, "llm authentication failed", false, status, nil)
	case status == http.StatusTooManyRequests:
		return llmapp.NewLLMError(platform.CodeLLMRateLimited, "llm rate limited", true, status, nil)
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return llmapp.NewLLMError(platform.CodeLLMBadRequest, "llm request was rejected by the upstream", false, status, nil)
	case status >= http.StatusInternalServerError:
		return llmapp.NewLLMError(platform.CodeLLMUpstreamError, "llm upstream error", true, status, nil)
	default:
		return llmapp.NewLLMError(platform.CodeLLMUpstreamError, "llm upstream error", false, status, nil)
	}
}

// transportError maps a network failure. Deadlines become llm_timeout; other
// transport failures are retryable upstream errors except a caller cancellation.
func transportError(ctx context.Context, err error) *llmapp.LLMError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return llmapp.NewLLMError(platform.CodeLLMUpstreamError, "llm request was canceled", false, 0, err)
		}
		return llmapp.NewLLMError(platform.CodeLLMTimeout, "llm request timed out", true, 0, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return llmapp.NewLLMError(platform.CodeLLMTimeout, "llm request timed out", true, 0, err)
	}
	return llmapp.NewLLMError(platform.CodeLLMUpstreamError, "llm upstream request failed", true, 0, err)
}

// invalidResponseError is the typed parse failure.
func invalidResponseError(cause error) *llmapp.LLMError {
	return llmapp.NewLLMError(platform.CodeLLMInvalidResponse, "llm returned an invalid response", false, 0, cause)
}

// upstreamStreamError is the typed error for an upstream error event inside an
// already-started stream. It is not retryable: bytes have already been sent.
func upstreamStreamError() *llmapp.LLMError {
	return llmapp.NewLLMError(platform.CodeLLMUpstreamError, "llm upstream stream failed", false, 0, nil)
}

// streamEndedError is reported when a stream closes without its terminal frame.
func streamEndedError() *llmapp.LLMError {
	return llmapp.NewLLMError(platform.CodeLLMInvalidResponse, "llm stream ended unexpectedly", false, 0, nil)
}

// invalidStreamPayloadError is reported for malformed stream frames.
func invalidStreamPayloadError(cause error) *llmapp.LLMError {
	return llmapp.NewLLMError(platform.CodeLLMInvalidResponse, "llm returned an invalid stream event", false, 0, cause)
}

// usageFromTokens converts wire token counts to the normalized usage.
func usageFromTokens(prompt, completion, total int) llmapp.Usage {
	if total == 0 {
		total = prompt + completion
	}
	return llmapp.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
}

// emit sends one normalized event unless the caller has gone away.
func emit(ctx context.Context, out chan<- llmapp.StreamEvent, event llmapp.StreamEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// upstreamErrorBody is the error object OpenAI-compatible upstreams return.
type upstreamErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// sseFrame is one parsed Server-Sent Events frame.
type sseFrame struct {
	event string
	data  string
}

// sseReader parses SSE lines into frames. It tolerates comments and multiline
// data fields and returns the final pending frame before io.EOF.
type sseReader struct {
	scanner *bufio.Scanner
	eof     bool
}

// newSSEReader returns a parser over r.
func newSSEReader(r io.Reader) *sseReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)
	return &sseReader{scanner: scanner}
}

// next returns the next frame or io.EOF.
func (r *sseReader) next() (sseFrame, error) {
	frame := sseFrame{}
	hasData := false
	for r.scanner.Scan() {
		line := strings.TrimSuffix(r.scanner.Text(), "\r")
		if line == "" {
			if hasData || frame.event != "" {
				return frame, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			frame.event = value
		case "data":
			if hasData {
				frame.data += "\n" + value
			} else {
				frame.data = value
			}
			hasData = true
		}
	}
	if err := r.scanner.Err(); err != nil {
		return sseFrame{}, err
	}
	if hasData || frame.event != "" {
		return frame, nil
	}
	if r.eof {
		return sseFrame{}, io.EOF
	}
	r.eof = true
	return sseFrame{}, io.EOF
}
