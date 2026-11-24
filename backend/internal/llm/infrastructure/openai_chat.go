package llminfra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// chatRetryBaseDelay is the deterministic first backoff between retryable chat
// attempts. It is intentionally short: the runtime's own soft/hard timeouts
// bound the overall run, and tests must stay fast.
const chatRetryBaseDelay = 25 * time.Millisecond

var _ llmapp.ChatClient = (*Client)(nil)

// newAdapter selects the wire-protocol adapter for cfg. It lets Client.Chat
// reuse its injected HTTP client without exposing transport placement details.
func newAdapter(cfg llm.Config, client *Client) (llmapp.Adapter, error) {
	protocol, ok := llm.NormalizeProtocol(cfg.Protocol)
	if !ok {
		return nil, fmt.Errorf("unknown llm protocol %q", cfg.Protocol)
	}
	switch protocol {
	case llm.ProtocolOpenAIChatCompletions:
		return &chatCompletionsAdapter{client: client, cfg: cfg}, nil
	case llm.ProtocolOpenAIResponses:
		return &responsesAdapter{client: client, cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown llm protocol %q", cfg.Protocol)
	}
}

// chatCompletionsAdapter binds one configuration to the
// POST /chat/completions wire protocol.
type chatCompletionsAdapter struct {
	client *Client
	cfg    llm.Config
}

var _ llmapp.Adapter = (*chatCompletionsAdapter)(nil)

// toolNameMapping maps upstream-safe tool names back to the frozen internal
// names. DeepSeek rejects function names outside ^[a-zA-Z0-9_-]+$, so dotted
// names such as requirement.draft_create are sent as requirement_draft_create.
type toolNameMapping struct {
	toOriginal map[string]string
}

// originalName returns the internal tool name for an upstream name; unknown
// names are returned unchanged.
func (m *toolNameMapping) originalName(sanitized string) string {
	if m == nil {
		return sanitized
	}
	if original, ok := m.toOriginal[sanitized]; ok {
		return original
	}
	return sanitized
}

// sanitizeToolName replaces every rune outside [A-Za-z0-9_-] with '_' so
// OpenAI-compatible upstreams with strict name patterns accept the request.
func sanitizeToolName(name string) string {
	var builder strings.Builder
	builder.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

// Chat performs one completion using the protocol adapter selected from
// cfg.Protocol. The chat-completions behavior (retries, timeout and error
// classification) is unchanged.
func (c *Client) Chat(ctx context.Context, cfg llm.Config, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	adapter, err := newAdapter(cfg, c)
	if err != nil {
		return llmapp.ChatResponse{}, err
	}
	return adapter.Chat(ctx, req)
}

// Stream performs one streaming completion using the protocol adapter selected
// from cfg.Protocol, forwarding normalized deltas. Both protocols implement
// Stream; a streaming failure is returned to the caller, never silently
// retried through the non-streaming Chat path.
func (c *Client) Stream(ctx context.Context, cfg llm.Config, req llmapp.ChatRequest, onDelta func(llmapp.Delta)) error {
	adapter, err := newAdapter(cfg, c)
	if err != nil {
		return err
	}
	return adapter.Stream(ctx, req, onDelta)
}

// Chat performs one OpenAI-compatible chat completion with tools. Transient
// failures (network, timeout, HTTP 429 and 5xx) are retried up to
// cfg.MaxRetries; other 4xx responses are returned immediately. Context-window
// overflows are wrapped with llmapp.ErrContextOverflow so the runtime can
// compact and retry. The configured API key is never included in errors.
func (a *chatCompletionsAdapter) Chat(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	payload, names, err := buildChatPayload(a.cfg, req, false)
	if err != nil {
		return llmapp.ChatResponse{}, err
	}
	attempts := a.cfg.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := waitRetry(ctx, attempt-2); err != nil {
				return llmapp.ChatResponse{}, contextErr(ctx, err)
			}
		}
		response, err := a.doChat(ctx, payload, names)
		if err == nil {
			return response, nil
		}
		lastErr = err
		// Only errors classified as transient are retried: a 4xx
		// parameter/capability rejection is never replayed, and no retry ever
		// switches to a different model (D24).
		if !errors.Is(err, llmapp.ErrTransient) || ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("llm chat request failed")
	}
	return llmapp.ChatResponse{}, lastErr
}

// Stream performs one streaming POST /chat/completions call and forwards
// normalized deltas. Transient failures (network, timeout, HTTP 429/5xx, an
// empty or broken stream) are retried up to cfg.MaxRetries ONLY until the first
// delta reaches the caller (R-D7): once content was emitted a retry would
// duplicate it, so any later failure is returned directly. Protocol rejections
// and context overflows are never retried, and no retry switches model (D24).
// The configured API key is never included in errors.
func (a *chatCompletionsAdapter) Stream(ctx context.Context, req llmapp.ChatRequest, onDelta func(llmapp.Delta)) error {
	payload, names, err := buildChatPayload(a.cfg, req, true)
	if err != nil {
		return err
	}
	attempts := a.cfg.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	firstDeltaSeen := false
	tracked := func(delta llmapp.Delta) {
		firstDeltaSeen = true
		onDelta(delta)
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := waitRetry(ctx, attempt-2); err != nil {
				return contextErr(ctx, err)
			}
		}
		err := a.doStream(ctx, payload, names, tracked)
		if err == nil {
			return nil
		}
		lastErr = err
		// R-D7: after the first delta a retry would duplicate content, so stop.
		// Only transient failures before the first delta are replayed; a 4xx
		// rejection or a cancelled context is never retried (parity with Chat).
		if firstDeltaSeen || !errors.Is(err, llmapp.ErrTransient) || ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("llm chat stream request failed")
	}
	return lastErr
}

// doStream performs one streaming attempt with the per-config request timeout.
// Non-2xx replies are classified exactly like the non-streaming path
// (classifyChatFailure); a 2xx reply is consumed as server-sent events.
func (a *chatCompletionsAdapter) doStream(ctx context.Context, payload chatRequestPayload, names *toolNameMapping, onDelta func(llmapp.Delta)) error {
	timeout := time.Duration(a.cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := a.client.newRequest(attemptCtx, a.cfg, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := a.client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return contextErr(ctx, err)
		}
		if isTimeout(err) {
			return &TransientError{Err: errors.New("llm provider request timed out")}
		}
		return &TransientError{Err: errors.New("llm provider request failed")}
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
		return classifyChatFailure(response.StatusCode, raw)
	}
	return consumeChatStream(response.Body, names, onDelta, payload.MaxTokens)
}

// chatStreamState tracks function-call metadata announced by the first fragment
// of each tool call so later argument fragments carry the call id/name, plus the
// finish_reason and usage captured from the trailing chunks (last wins: each
// usage chunk is a cumulative provider snapshot, not an additive fragment).
type chatStreamState struct {
	names        *toolNameMapping
	calls        map[int]streamCallRef
	finishReason string
	usage        map[string]int
	sawChoice    bool
}

// consumeChatStream reads chat-completions SSE chunks, emits normalized deltas
// and classifies empty/truncated/malformed streams as transient failures instead
// of silently returning success. The terminal delta (finish_reason + usage) is
// emitted when the [DONE] sentinel arrives. The SSE framing is the shared
// consumeSSE scanner; only the chunk semantics live here. maxOutputTokens is the
// request's forwarded output limit, used to scale the streaming byte budget
// (H-E12) so a configured long turn is not starved.
func consumeChatStream(body io.Reader, names *toolNameMapping, onDelta func(llmapp.Delta), maxOutputTokens int) error {
	state := chatStreamState{names: names, calls: make(map[int]streamCallRef)}
	return consumeSSE(body, func(data string) (bool, error) {
		return state.handleChunk(data, onDelta)
	}, "read llm chat stream failed", maxOutputTokens)
}

// handleChunk maps one SSE data payload to zero or more normalized deltas and
// reports whether it was the terminal [DONE] sentinel. Malformed payloads are
// transient stream failures; an empty completion is transient too. Tool names
// are reverse-mapped to the internal names (parity with parseChatCompletion).
func (s *chatStreamState) handleChunk(data string, onDelta func(llmapp.Delta)) (bool, error) {
	if strings.TrimSpace(data) == "[DONE]" {
		if !s.sawChoice {
			return false, &TransientError{Err: errors.New("llm provider returned an empty stream")}
		}
		onDelta(llmapp.Delta{FinishReason: s.finishReason, Usage: s.usage})
		return true, nil
	}
	var chunk chatStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, &TransientError{Err: fmt.Errorf("decode llm chat stream event failed: %w", err)}
	}
	// usage may arrive in a trailing chunk whose choices array is empty
	// (stream_options.include_usage), or alongside the finish_reason chunk.
	if chunk.Usage != nil {
		s.usage = chatUsageMap(chunk.Usage)
	}
	if len(chunk.Choices) == 0 {
		return false, nil
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != "" {
		s.finishReason = choice.FinishReason
		s.sawChoice = true
	}
	if choice.Delta.Content != "" {
		s.sawChoice = true
		onDelta(llmapp.Delta{Content: choice.Delta.Content})
	}
	if choice.Delta.ReasoningContent != "" {
		s.sawChoice = true
		onDelta(llmapp.Delta{ReasoningContent: choice.Delta.ReasoningContent})
	}
	for _, call := range choice.Delta.ToolCalls {
		s.sawChoice = true
		ref := s.calls[call.Index]
		if call.ID != "" {
			ref.id = call.ID
		}
		if call.Function.Name != "" {
			ref.name = s.names.originalName(call.Function.Name)
		}
		s.calls[call.Index] = ref
		onDelta(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{
			Index:     call.Index,
			ID:        ref.id,
			Name:      ref.name,
			Arguments: call.Function.Arguments,
		}})
	}
	return false, nil
}

// doChat performs one attempt with the per-config request timeout. Failures are
// typed with the application classification: ErrTransient marks a retryable
// transport/429/5xx/empty response, ProtocolError marks a non-retryable
// parameter/capability rejection.
func (a *chatCompletionsAdapter) doChat(ctx context.Context, payload chatRequestPayload, names *toolNameMapping) (llmapp.ChatResponse, error) {
	cfg := a.cfg
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := a.client.newRequest(attemptCtx, cfg, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return llmapp.ChatResponse{}, err
	}
	response, err := a.client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return llmapp.ChatResponse{}, contextErr(ctx, err)
		}
		if isTimeout(err) {
			return llmapp.ChatResponse{}, &TransientError{Err: errors.New("llm provider request timed out")}
		}
		return llmapp.ChatResponse{}, &TransientError{Err: errors.New("llm provider request failed")}
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
		return llmapp.ChatResponse{}, classifyChatFailure(response.StatusCode, raw)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return llmapp.ChatResponse{}, &TransientError{Err: fmt.Errorf("read llm chat response failed: %w", err)}
	}
	parsed, err := parseChatCompletion(raw, names)
	if err != nil {
		return llmapp.ChatResponse{}, err
	}
	return parsed, nil
}

// classifyChatFailure turns a non-2xx upstream response into a typed error.
// 429/5xx are transient; other 4xx are non-retryable protocol rejections. The
// upstream message is kept verbatim (GD-01: credentials are plaintext).
func classifyChatFailure(status int, raw []byte) error {
	var payload chatErrorPayload
	message := ""
	code := ""
	if err := json.Unmarshal(raw, &payload); err == nil {
		message = payload.Error.Message
		code = payload.Error.Code
	}
	if strings.TrimSpace(message) == "" {
		message = string(raw)
	}
	// GD-01 (Q05=B): the upstream message is kept verbatim (credentials are
	// plaintext by design); only a size bound is applied before it is echoed.
	message = boundUpstreamMessage(message, 300)
	if isChatContextOverflow(status, code, message) {
		return fmt.Errorf("%w: provider returned HTTP %d", llmapp.ErrContextOverflow, status)
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return &TransientError{StatusCode: status, Err: fmt.Errorf("llm provider returned HTTP %d: %s", status, message)}
	}
	return &ProtocolError{StatusCode: status, Message: message}
}

// buildChatPayload maps the application request to the OpenAI wire format.
// Function names are sanitized for strict upstreams and the reverse mapping is
// returned so response tool calls can be restored to the internal names. The
// chat-completions thinking mapping adds reasoning_effort plus the
// provider-specific thinking object; the auto/none defaults add no keys so the
// legacy payload stays byte-compatible. Assistant history replays
// reasoning_content only when the application captured it (spec §7.2).
func buildChatPayload(cfg llm.Config, req llmapp.ChatRequest, stream bool) (chatRequestPayload, *toolNameMapping, error) {
	messages := make([]chatMessagePayload, 0, len(req.Messages))
	for _, message := range req.Messages {
		item := chatMessagePayload{
			Role:             message.Role,
			Content:          message.Content,
			ToolCallID:       message.ToolCallID,
			ReasoningContent: message.ReasoningContent,
		}
		for _, image := range message.Images {
			if len(image.Data) == 0 {
				continue
			}
			item.ImageParts = append(item.ImageParts, chatContentPart{
				Type: "image_url",
				ImageURL: &chatContentImageURL{
					URL: "data:" + image.MIME + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
				},
			})
		}
		for _, call := range message.ToolCalls {
			item.ToolCalls = append(item.ToolCalls, chatToolCallPayload{
				ID:   call.ID,
				Type: "function",
				Function: chatFunctionCallPayload{
					// Assistant history names must match the sanitized tool
					// definitions sent in the same request.
					Name:      sanitizeToolName(call.Name),
					Arguments: call.Arguments,
				},
			})
		}
		messages = append(messages, item)
	}

	payload := chatRequestPayload{
		Model:       req.Model,
		Messages:    messages,
		Temperature: 0,
		MaxTokens:   req.MaxOutputTokens,
		Stream:      stream,
	}
	if stream {
		// OpenAI chat-completions only emits the usage object in the stream when
		// stream_options.include_usage is set; request it so the streaming path
		// keeps usage/cache telemetry parity with the non-streaming path.
		payload.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	}
	applyChatThinking(&payload, cfg)
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = llmapp.DefaultChatMaxOutputTokens
	}
	if len(req.Tools) > 0 {
		names := &toolNameMapping{toOriginal: make(map[string]string, len(req.Tools))}
		tools := make([]chatToolPayload, 0, len(req.Tools))
		for _, tool := range req.Tools {
			sanitized := sanitizeToolName(tool.Name)
			if existing, ok := names.toOriginal[sanitized]; ok && existing != tool.Name {
				return chatRequestPayload{}, nil, fmt.Errorf(
					"llm tool names %q and %q collide after sanitization to %q", existing, tool.Name, sanitized,
				)
			}
			names.toOriginal[sanitized] = tool.Name

			parameters := json.RawMessage(tool.Parameters)
			if len(parameters) == 0 {
				parameters = json.RawMessage(`{}`)
			}
			tools = append(tools, chatToolPayload{
				Type: "function",
				Function: chatFunctionDefinition{
					Name:        sanitized,
					Description: tool.Description,
					Parameters:  parameters,
				},
			})
		}
		payload.Tools = tools
		payload.ToolChoice = "auto"
		return payload, names, nil
	}
	return payload, nil, nil
}

// parseChatCompletion decodes a successful OpenAI-compatible completion and
// maps sanitized function names back to the internal names. reasoning_content
// is captured so the application can replay it on the next tool turn (spec
// §7.2); providers that omit it leave the field empty and the history payload
// omits the key.
func parseChatCompletion(raw []byte, names *toolNameMapping) (llmapp.ChatResponse, error) {
	var payload chatCompletionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		// A malformed success body is a protocol failure, not a transient one.
		return llmapp.ChatResponse{}, &ProtocolError{StatusCode: http.StatusOK, Message: "decode llm chat response failed"}
	}
	if len(payload.Choices) == 0 {
		// An empty completion is retryable (spec §5.6: 空响应按退避重试).
		return llmapp.ChatResponse{}, &TransientError{Err: errors.New("llm provider returned no choices")}
	}
	choice := payload.Choices[0]
	response := llmapp.ChatResponse{
		FinishReason:     choice.FinishReason,
		ReasoningContent: choice.Message.ReasoningContent,
	}
	if choice.Message.Content != nil {
		response.Content = *choice.Message.Content
	}
	for _, call := range choice.Message.ToolCalls {
		response.ToolCalls = append(response.ToolCalls, llmapp.ToolCall{
			ID:        call.ID,
			Name:      names.originalName(call.Function.Name),
			Arguments: call.Function.Arguments,
		})
	}
	if payload.Usage != nil {
		response.Usage = chatUsageMap(payload.Usage)
	}
	return response, nil
}

// chatUsageMap converts the chat-completions usage object into the normalized
// key map the application consumes. The protocol-specific prompt-cache counters
// are optional; a reported zero stays authoritative (a cache miss) while an
// absent counter leaves the cache keys out of the map.
func chatUsageMap(usage *chatCompletionUsage) map[string]int {
	result := map[string]int{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}
	if result["total_tokens"] == 0 {
		// Some providers emit total_tokens=0 alongside a real component
		// breakdown. Synthesize the total so the budget aggregation uses it
		// instead of summing every usage key (which includes the prompt-cache
		// bookkeeping that overlaps prompt_tokens).
		result["total_tokens"] = usage.PromptTokens + usage.CompletionTokens
	}
	var cachedTokens *int
	switch {
	case usage.PromptTokensDetails != nil:
		// OpenAI: usage.prompt_tokens_details.cached_tokens.
		cachedTokens = &usage.PromptTokensDetails.CachedTokens
	case usage.PromptCacheHitTokens != nil:
		// DeepSeek: usage.prompt_cache_hit_tokens.
		cachedTokens = usage.PromptCacheHitTokens
	default:
		// OpenAI-compatible gateways fronting Anthropic-style usage.
		cachedTokens = usage.CacheReadInputTokens
	}
	setPromptCacheUsage(result, cachedTokens)
	return result
}

// setPromptCacheUsage normalizes one reported prompt-cache read count into the
// keys the application consumes (cached_tokens/cache_hit). A nil count means the
// provider reported no cache statistics at all, so nothing is added and the
// caller's local estimate stays authoritative. A reported zero is authoritative
// too: it is a cache miss and overrides a local cache-hit estimate.
func setPromptCacheUsage(usage map[string]int, cachedTokens *int) {
	if cachedTokens == nil {
		return
	}
	value := *cachedTokens
	if value < 0 {
		value = 0
	}
	usage["cached_tokens"] = value
	usage["cache_hit"] = boolToInt(value > 0)
}

// boolToInt renders a boolean usage flag as the 0/1 int the usage map carries.
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// waitRetry sleeps with a deterministic exponential backoff bounded at 250ms.
func waitRetry(ctx context.Context, attempt int) error {
	delay := chatRetryBaseDelay << attempt
	if delay > 250*time.Millisecond {
		delay = 250 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// contextErr normalizes a canceled/deadline-exceeded parent context.
func contextErr(ctx context.Context, fallback error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return errors.New("llm provider request canceled")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errors.New("llm provider request timed out")
	default:
		return fallback
	}
}

// isChatContextOverflow reports whether a non-2xx response means the request
// exceeded the model context window.
func isChatContextOverflow(status int, code, message string) bool {
	if strings.EqualFold(strings.TrimSpace(code), "context_length_exceeded") {
		return true
	}
	return status == http.StatusBadRequest && strings.Contains(strings.ToLower(message), "context")
}

// boundUpstreamMessage trims and bounds the upstream text that may be echoed
// into errors/events. GD-01 (Q05=B): credentials are plaintext by design, so the
// message is never redacted — only a size bound is applied.
func boundUpstreamMessage(message string, limit int) string {
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if limit > 0 && len(runes) > limit {
		message = string(runes[:limit]) + "..."
	}
	return message
}

// applyChatThinking maps the config's thinking settings onto the
// chat-completions payload (spec §7.2):
//   - thinking_type auto adds nothing (provider default);
//   - thinking_type enabled/disabled adds the provider-specific thinking object;
//   - reasoning_effort none adds nothing, every other effort is forwarded.
func applyChatThinking(payload *chatRequestPayload, cfg llm.Config) {
	if effort := cfg.EffectiveReasoningEffort(); effort != llm.ReasoningEffortNone {
		payload.ReasoningEffort = effort
	}
	switch cfg.EffectiveThinkingType() {
	case llm.ThinkingTypeEnabled:
		payload.Thinking = &chatThinkingPayload{Type: llm.ThinkingTypeEnabled}
	case llm.ThinkingTypeDisabled:
		payload.Thinking = &chatThinkingPayload{Type: llm.ThinkingTypeDisabled}
	}
}

type chatRequestPayload struct {
	Model           string               `json:"model"`
	Messages        []chatMessagePayload `json:"messages"`
	Tools           []chatToolPayload    `json:"tools,omitempty"`
	ToolChoice      string               `json:"tool_choice,omitempty"`
	Temperature     float64              `json:"temperature"`
	MaxTokens       int                  `json:"max_tokens"`
	Stream          bool                 `json:"stream"`
	StreamOptions   *chatStreamOptions   `json:"stream_options,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	Thinking        *chatThinkingPayload `json:"thinking,omitempty"`
}

// chatThinkingPayload is the provider-specific thinking switch used by
// OpenAI-compatible gateways that accept a thinking object (for example GLM).
// Qwen uses enable_thinking instead and is not covered by this switch.
type chatThinkingPayload struct {
	Type string `json:"type"`
}

// chatStreamOptions is the OpenAI stream_options object. include_usage asks the
// provider to emit the usage object in a trailing stream chunk so the streaming
// path keeps usage telemetry parity with the non-streaming path.
type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessagePayload struct {
	Role             string                `json:"role"`
	Content          string                `json:"content,omitempty"`
	ToolCalls        []chatToolCallPayload `json:"tool_calls,omitempty"`
	ToolCallID       string                `json:"tool_call_id,omitempty"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	// ImageParts switches the content field to the OpenAI content-parts array.
	// It is never serialized as its own key.
	ImageParts []chatContentPart `json:"-"`
}

// chatContentPart is one chat-completions content part: text or an inline image
// data URL.
type chatContentPart struct {
	Type     string               `json:"type"`
	Text     string               `json:"text,omitempty"`
	ImageURL *chatContentImageURL `json:"image_url,omitempty"`
}

// chatContentImageURL is the image_url payload of one content part.
type chatContentImageURL struct {
	URL string `json:"url"`
}

// MarshalJSON keeps text-only messages on the byte-compatible string content
// wire and switches to the content-parts array only when images are present.
func (m chatMessagePayload) MarshalJSON() ([]byte, error) {
	type wireMessage struct {
		Role             string                `json:"role"`
		Content          any                   `json:"content"`
		ToolCalls        []chatToolCallPayload `json:"tool_calls,omitempty"`
		ToolCallID       string                `json:"tool_call_id,omitempty"`
		ReasoningContent string                `json:"reasoning_content,omitempty"`
	}
	var content any
	switch {
	case len(m.ImageParts) > 0:
		parts := make([]chatContentPart, 0, len(m.ImageParts)+1)
		if m.Content != "" {
			parts = append(parts, chatContentPart{Type: "text", Text: m.Content})
		}
		parts = append(parts, m.ImageParts...)
		content = parts
	case m.Content != "":
		content = m.Content
	default:
		// Always serialize content, as the empty string for a pure tool call:
		// strict OpenAI-compatible upstreams (DeepSeek) answer 422 "missing
		// field `content`" when the key is omitted.
		content = ""
	}
	return json.Marshal(wireMessage{
		Role:             m.Role,
		Content:          content,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		ReasoningContent: m.ReasoningContent,
	})
}

type chatToolCallPayload struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function chatFunctionCallPayload `json:"function"`
}

type chatFunctionCallPayload struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatToolPayload struct {
	Type     string                 `json:"type"`
	Function chatFunctionDefinition `json:"function"`
}

type chatFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatCompletionPayload struct {
	Choices []struct {
		Message struct {
			Role             string  `json:"role"`
			Content          *string `json:"content"`
			ReasoningContent string  `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatCompletionUsage `json:"usage"`
}

// chatCompletionUsage is one OpenAI-compatible usage object. The prompt-cache
// counters are optional and protocol-specific; pointers keep "reported zero"
// distinct from "not reported".
type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// PromptTokensDetails is the OpenAI nested prompt-cache detail.
	PromptTokensDetails *chatPromptTokensDetails `json:"prompt_tokens_details"`
	// PromptCacheHitTokens is the DeepSeek prompt-cache read counter.
	PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
	// CacheReadInputTokens is the Anthropic-style alias used by some gateways.
	CacheReadInputTokens *int `json:"cache_read_input_tokens"`
}

// chatPromptTokensDetails is the OpenAI prompt_tokens_details object.
type chatPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type chatErrorPayload struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// chatStreamChunk is one OpenAI chat-completions streaming chunk
// (object "chat.completion.chunk"). Usage may arrive in a final chunk whose
// choices array is empty when stream_options.include_usage is set.
type chatStreamChunk struct {
	Choices []chatStreamChoice   `json:"choices"`
	Usage   *chatCompletionUsage `json:"usage"`
}

// chatStreamChoice is one streamed choice: the incremental delta plus the
// finish_reason that stays null until the final content chunk.
type chatStreamChoice struct {
	Index        int             `json:"index"`
	Delta        chatStreamDelta `json:"delta"`
	FinishReason string          `json:"finish_reason"`
}

// chatStreamDelta is the incremental message fragment of a streamed choice.
type chatStreamDelta struct {
	Role             string               `json:"role"`
	Content          string               `json:"content"`
	ReasoningContent string               `json:"reasoning_content"`
	ToolCalls        []chatStreamToolCall `json:"tool_calls"`
}

// chatStreamToolCall is one streamed function-call fragment. The first fragment
// carries id/name; later fragments carry only the arguments fragment, keyed by
// index so parallel calls stay distinct.
type chatStreamToolCall struct {
	Index    int                        `json:"index"`
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function chatStreamToolCallFunction `json:"function"`
}

// chatStreamToolCallFunction is the function fragment of a streamed tool call.
type chatStreamToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
