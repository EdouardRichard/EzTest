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

// Chat performs one OpenAI-compatible chat completion with tools. Transient
// failures (network, timeout, HTTP 429 and 5xx) are retried up to
// cfg.MaxRetries; other 4xx responses are returned immediately. Context-window
// overflows are wrapped with llmapp.ErrContextOverflow so the runtime can
// compact and retry. The configured API key is never included in errors.
func (a *chatCompletionsAdapter) Chat(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	payload, names, err := buildChatPayload(a.cfg, req)
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

// Stream is not implemented for the chat-completions adapter yet: the P1
// gateway only requires non-streaming completions. It fails closed with
// ErrNotImplemented instead of pretending to stream; the Responses adapter in
// responses.go implements streaming on its own wire protocol.
func (a *chatCompletionsAdapter) Stream(context.Context, llmapp.ChatRequest, func(llmapp.Delta)) error {
	return fmt.Errorf("llm chat completions streaming is not implemented yet: %w", llmapp.ErrNotImplemented)
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
		return llmapp.ChatResponse{}, classifyChatFailure(cfg, response.StatusCode, raw)
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

// classifyChatFailure turns a non-2xx upstream response into a safe typed error.
// 429/5xx are transient; other 4xx are non-retryable protocol rejections.
func classifyChatFailure(cfg llm.Config, status int, raw []byte) error {
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
	for _, value := range cfg.Headers {
		if strings.TrimSpace(value) != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	message = sanitizeUpstreamMessage(message, cfg.APIKey, 300)
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
func buildChatPayload(cfg llm.Config, req llmapp.ChatRequest) (chatRequestPayload, *toolNameMapping, error) {
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
		Stream:      false,
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

// sanitizeUpstreamMessage redacts the configured key and bounds the text that
// may be echoed into errors/events.
func sanitizeUpstreamMessage(message, secret string, limit int) string {
	message = strings.TrimSpace(message)
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
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
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	Thinking        *chatThinkingPayload `json:"thinking,omitempty"`
}

// chatThinkingPayload is the provider-specific thinking switch used by
// OpenAI-compatible gateways that accept a thinking object (for example GLM).
// Qwen uses enable_thinking instead and is not covered by this switch.
type chatThinkingPayload struct {
	Type string `json:"type"`
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
