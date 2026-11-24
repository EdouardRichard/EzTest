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

// responsesAdapter binds one configuration to the OpenAI Responses wire
// protocol: POST /responses with input/output items, flat function tools and
// server-sent events. It is a distinct wire implementation from
// chatCompletionsAdapter; it never falls back to /chat/completions.
type responsesAdapter struct {
	client *Client
	cfg    llm.Config
}

var _ llmapp.Adapter = (*responsesAdapter)(nil)

// ProtocolError is a non-retryable upstream protocol/capability rejection: the
// request shape, model capability or credentials are wrong, so a retry cannot
// help. It carries the upstream HTTP status and the bounded provider message
// (GD-01: the message is plaintext, never redacted).
// It implements llmapp.ClassifiedError and classifies as llmapp.ErrProtocol so
// the application can map it without importing infrastructure (R25).
type ProtocolError struct {
	StatusCode int
	Message    string
}

// Error renders the typed protocol failure with the bounded upstream provider
// message verbatim (GD-01: plaintext, never redacted — the message may echo
// request content; credentials are plaintext by design, Q05=B).
func (e *ProtocolError) Error() string {
	if e == nil {
		return "llm provider protocol error"
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("llm provider protocol error (HTTP %d)", e.StatusCode)
	}
	return fmt.Sprintf("llm provider protocol error (HTTP %d): %s", e.StatusCode, e.Message)
}

// LLMErrorKind classifies the failure as a non-retryable protocol rejection.
func (e *ProtocolError) LLMErrorKind() llmapp.LLMErrorKind { return llmapp.LLMKindProtocol }

// HTTPStatusCode returns the upstream HTTP status that caused the rejection.
func (e *ProtocolError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

// UpstreamMessage returns the bounded provider message (GD-01: plaintext).
func (e *ProtocolError) UpstreamMessage() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Is maps the typed error onto the application sentinel.
func (e *ProtocolError) Is(target error) bool { return target == llmapp.ErrProtocol }

// TransientError marks an upstream/transport failure that a later attempt may
// recover from: HTTP 429/5xx, an empty response or a broken stream. It
// implements llmapp.ClassifiedError and classifies as llmapp.ErrTransient.
type TransientError struct {
	StatusCode int
	Err        error
}

// Error delegates to the wrapped failure.
func (e *TransientError) Error() string {
	if e == nil || e.Err == nil {
		return "llm provider transient error"
	}
	return e.Err.Error()
}

// Unwrap exposes the wrapped failure to errors.Is/errors.As.
func (e *TransientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// LLMErrorKind classifies the failure as retryable transient.
func (e *TransientError) LLMErrorKind() llmapp.LLMErrorKind { return llmapp.LLMKindTransient }

// HTTPStatusCode returns the upstream HTTP status (0 for transport failures).
func (e *TransientError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

// UpstreamMessage returns the bounded transport/provider message (GD-01: plaintext).
func (e *TransientError) UpstreamMessage() string { return e.Error() }

// Is maps the typed error onto the application sentinel.
func (e *TransientError) Is(target error) bool { return target == llmapp.ErrTransient }

// Chat performs one non-streaming POST /responses completion and normalizes the
// output items back into the application response. Transient failures are
// retried up to cfg.MaxRetries; protocol errors are returned immediately.
func (a *responsesAdapter) Chat(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	payload, names, err := buildResponsesPayload(a.cfg, req, false)
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
		var transient *TransientError
		if !errors.As(err, &transient) || ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("llm responses request failed")
	}
	return llmapp.ChatResponse{}, lastErr
}

// doChat performs one POST /responses attempt with the per-config request
// timeout.
func (a *responsesAdapter) doChat(ctx context.Context, payload responsesRequestPayload, names *toolNameMapping) (llmapp.ChatResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, a.requestTimeout())
	defer cancel()

	request, err := a.client.newRequest(attemptCtx, a.cfg, http.MethodPost, "/responses", payload)
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
		return llmapp.ChatResponse{}, responsesHTTPError(response.StatusCode, raw)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return llmapp.ChatResponse{}, &TransientError{Err: errors.New("read llm responses response failed")}
	}
	parsed, err := parseResponsesResponse(raw, names)
	if err != nil {
		return llmapp.ChatResponse{}, err
	}
	return parsed, nil
}

// Stream performs one streaming POST /responses call and forwards normalized
// deltas. The stream is not replayed on failure: once deltas were emitted a
// retry would duplicate content, so transport/stream failures are returned to
// the caller.
func (a *responsesAdapter) Stream(ctx context.Context, req llmapp.ChatRequest, onDelta func(llmapp.Delta)) error {
	payload, names, err := buildResponsesPayload(a.cfg, req, true)
	if err != nil {
		return err
	}
	attemptCtx, cancel := context.WithTimeout(ctx, a.requestTimeout())
	defer cancel()

	request, err := a.client.newRequest(attemptCtx, a.cfg, http.MethodPost, "/responses", payload)
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
		return responsesHTTPError(response.StatusCode, raw)
	}
	return consumeResponsesStream(response.Body, names, onDelta, payload.MaxOutputTokens)
}

// requestTimeout resolves the per-config request bound, falling back to the
// client default when the stored value is absent.
func (a *responsesAdapter) requestTimeout() time.Duration {
	timeout := time.Duration(a.cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return timeout
}

// responsesHTTPError maps a non-2xx upstream reply to a typed error. 429
// and 5xx are transient; other 4xx are non-retryable protocol/capability
// errors; context overflows stay recognizable as ErrContextOverflow. The
// upstream body is parsed for a message and bounded like the chat adapter
// (GD-01: the message is plaintext, never redacted).
func responsesHTTPError(status int, raw []byte) error {
	var payload responsesErrorEnvelope
	message := ""
	code := ""
	if err := json.Unmarshal(raw, &payload); err == nil {
		message = payload.Error.Message
		code = payload.Error.Code
	}
	if strings.TrimSpace(message) == "" {
		message = string(raw)
	}
	message = boundUpstreamMessage(message, 300)
	if isChatContextOverflow(status, code, message) {
		return fmt.Errorf("%w: provider returned HTTP %d", llmapp.ErrContextOverflow, status)
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return &TransientError{StatusCode: status, Err: fmt.Errorf("llm provider returned HTTP %d: %s", status, message)}
	}
	return &ProtocolError{StatusCode: status, Message: message}
}

// buildResponsesPayload maps the application request onto the Responses wire
// format: role messages become input message items, assistant tool calls become
// function_call items, tool results become function_call_output items and tools
// are flat function definitions. Function names are sanitized for strict
// upstreams and the reverse mapping is returned for response parsing. The
// thinking mapping adds reasoning{effort,summary}; auto/none adds no key.
func buildResponsesPayload(cfg llm.Config, req llmapp.ChatRequest, stream bool) (responsesRequestPayload, *toolNameMapping, error) {
	payload := responsesRequestPayload{
		Model:           req.Model,
		Input:           make([]any, 0, len(req.Messages)),
		MaxOutputTokens: req.MaxOutputTokens,
		Stream:          stream,
	}
	applyResponsesReasoning(&payload, cfg)
	if payload.MaxOutputTokens <= 0 {
		payload.MaxOutputTokens = llmapp.DefaultChatMaxOutputTokens
	}

	for _, message := range req.Messages {
		switch message.Role {
		case "tool":
			payload.Input = append(payload.Input, responsesFunctionCallOutputItem{
				Type:   "function_call_output",
				CallID: message.ToolCallID,
				Output: message.Content,
			})
		case "assistant":
			// Provider-issued reasoning items must be replayed verbatim before
			// the matching function calls when the caller manually manages the
			// context (spec §7.2); the wire item carries the provider id and
			// encrypted content that cannot be reconstructed from text.
			for _, item := range message.ReasoningItems {
				if len(item) == 0 {
					continue
				}
				payload.Input = append(payload.Input, item)
			}
			if message.Content != "" {
				payload.Input = append(payload.Input, responsesMessageItem{Role: "assistant", Content: message.Content})
			}
			for _, call := range message.ToolCalls {
				payload.Input = append(payload.Input, responsesFunctionCallItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      sanitizeToolName(call.Name),
					Arguments: call.Arguments,
				})
			}
		default:
			payload.Input = append(payload.Input, responsesMessageItem{
				Role:    message.Role,
				Content: message.Content,
				Images:  message.Images,
			})
		}
	}

	if len(req.Tools) == 0 {
		return payload, nil, nil
	}
	names := &toolNameMapping{toOriginal: make(map[string]string, len(req.Tools))}
	tools := make([]responsesTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		sanitized := sanitizeToolName(tool.Name)
		if existing, ok := names.toOriginal[sanitized]; ok && existing != tool.Name {
			return responsesRequestPayload{}, nil, fmt.Errorf(
				"llm tool names %q and %q collide after sanitization to %q", existing, tool.Name, sanitized,
			)
		}
		names.toOriginal[sanitized] = tool.Name

		parameters := json.RawMessage(tool.Parameters)
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{}`)
		}
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        sanitized,
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	payload.Tools = tools
	payload.ToolChoice = "auto"
	return payload, names, nil
}

// parseResponsesResponse decodes a successful non-streaming reply into the
// normalized application response. An empty turn is transient (retried); a
// malformed body is a non-retryable protocol failure.
func parseResponsesResponse(raw []byte, names *toolNameMapping) (llmapp.ChatResponse, error) {
	var payload responsesResponsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		// A malformed success body is a protocol failure, not a transient one.
		return llmapp.ChatResponse{}, &ProtocolError{StatusCode: http.StatusOK, Message: "decode llm responses response failed"}
	}
	response, empty := normalizeResponsesResponse(payload, names)
	if payload.Status == "failed" {
		message := "llm provider response failed"
		if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
			message += ": " + boundUpstreamMessage(payload.Error.Message, 300)
		}
		return llmapp.ChatResponse{}, &TransientError{Err: errors.New(message)}
	}
	if empty {
		return llmapp.ChatResponse{}, &TransientError{Err: errors.New("llm provider returned an empty response")}
	}
	return response, nil
}

// normalizeResponsesResponse flattens output items into text, reasoning text
// and tool calls, and reports whether the payload carried any usable assistant
// turn. Reasoning items are preserved twice: their readable summary/text is
// normalized into ReasoningContent and the opaque provider item is kept in
// ReasoningItems so buildResponsesPayload can replay it verbatim (spec §7.2).
func normalizeResponsesResponse(payload responsesResponsePayload, names *toolNameMapping) (llmapp.ChatResponse, bool) {
	response := llmapp.ChatResponse{}
	empty := true
	for _, rawItem := range payload.Output {
		var item responsesOutputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			continue
		}
		switch item.Type {
		case "reasoning":
			if text := responsesReasoningText(item); text != "" {
				if response.ReasoningContent != "" {
					response.ReasoningContent += "\n"
				}
				response.ReasoningContent += text
			}
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					response.Content += part.Text
					empty = false
				}
			}
		case "function_call":
			call := llmapp.ToolCall{
				ID:        item.CallID,
				Name:      names.originalName(item.Name),
				Arguments: item.Arguments,
			}
			if call.ID == "" {
				call.ID = item.ID
			}
			response.ToolCalls = append(response.ToolCalls, call)
			empty = false
		}
	}
	response.ReasoningItems = responsesReasoningItems(payload.Output)
	if payload.Usage != nil {
		response.Usage = responsesUsageMap(payload.Usage)
	}
	response.FinishReason = responsesFinishReason(payload, len(response.ToolCalls) > 0)
	return response, empty
}

// responsesReasoningItems copies the provider-issued reasoning output items
// verbatim (id/summary/encrypted content) so buildResponsesPayload can replay
// them on the next turn (spec §7.2). It is the single extraction shared by the
// non-streaming normalizer and the streaming terminal capture, so both paths
// preserve exactly the same items in output order.
func responsesReasoningItems(output []json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	for _, rawItem := range output {
		var item responsesOutputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			continue
		}
		if item.Type != "reasoning" {
			continue
		}
		items = append(items, append(json.RawMessage(nil), rawItem...))
	}
	return items
}

// responsesReasoningText extracts the readable reasoning text of one Responses
// reasoning item: the summary parts when present, otherwise the raw
// reasoning_text content parts.
func responsesReasoningText(item responsesOutputItem) string {
	var builder strings.Builder
	appendPart := func(text string) {
		if text == "" {
			return
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(text)
	}
	for _, part := range item.Summary {
		appendPart(part.Text)
	}
	if builder.Len() > 0 {
		return builder.String()
	}
	for _, part := range item.Content {
		if part.Type == "reasoning_text" || part.Type == "text" {
			appendPart(part.Text)
		}
	}
	return builder.String()
}

// responsesFinishReason maps the Responses status to the chat-completions
// finish_reason vocabulary the application already understands.
func responsesFinishReason(payload responsesResponsePayload, hasToolCalls bool) string {
	switch payload.Status {
	case "incomplete":
		reason := ""
		if payload.IncompleteDetails != nil {
			reason = payload.IncompleteDetails.Reason
		}
		switch reason {
		case "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		default:
			return "incomplete"
		}
	case "failed":
		return "error"
	default:
		// "completed", or an upstream that omits the status field.
		if hasToolCalls {
			return "tool_calls"
		}
		return "stop"
	}
}

// responsesUsageMap converts Responses token counts to the chat-completions keys
// used by ChatResponse.Usage and normalizes the protocol-specific prompt-cache
// counters (input_tokens_details.cached_tokens and the flattened aliases) into
// the cached_tokens/cache_hit keys the application consumes.
func responsesUsageMap(usage *responsesUsage) map[string]int {
	result := map[string]int{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.TotalTokens,
	}
	if result["total_tokens"] == 0 {
		result["total_tokens"] = usage.InputTokens + usage.OutputTokens
	}
	var cachedTokens *int
	switch {
	case usage.InputTokensDetails != nil:
		// OpenAI: usage.input_tokens_details.cached_tokens.
		cachedTokens = &usage.InputTokensDetails.CachedTokens
	case usage.CacheReadInputTokens != nil:
		// Anthropic-style alias used by some compatible gateways.
		cachedTokens = usage.CacheReadInputTokens
	default:
		cachedTokens = usage.PromptCacheHitTokens
	}
	setPromptCacheUsage(result, cachedTokens)
	return result
}

// responsesStreamState tracks function-call metadata announced by
// response.output_item.added so argument fragments can carry the call id/name.
type responsesStreamState struct {
	names *toolNameMapping
	calls map[int]streamCallRef
}

// consumeResponsesStream reads SSE events, emits normalized deltas and
// classifies empty/truncated/malformed streams as transient failures instead of
// silently returning success. The SSE framing is the shared consumeSSE scanner;
// only the event semantics live here. maxOutputTokens is the request's forwarded
// output limit, used to scale the streaming byte budget (H-E12) so a configured
// long turn is not starved.
func consumeResponsesStream(body io.Reader, names *toolNameMapping, onDelta func(llmapp.Delta), maxOutputTokens int) error {
	state := responsesStreamState{names: names, calls: make(map[int]streamCallRef)}
	return consumeSSE(body, func(data string) (bool, error) {
		return state.handleEvent(data, onDelta)
	}, "read llm responses stream failed", maxOutputTokens)
}

// handleEvent maps one SSE data payload to zero or more normalized deltas and
// reports whether it was the terminal event. Malformed payloads and upstream
// error events are transient stream failures.
func (s *responsesStreamState) handleEvent(data string, onDelta func(llmapp.Delta)) (bool, error) {
	var event responsesStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return false, &TransientError{Err: fmt.Errorf("decode llm responses stream event failed: %w", err)}
	}
	switch event.Type {
	case "error", "response.failed":
		message := event.errorMessage()
		if strings.TrimSpace(message) == "" {
			message = "llm provider stream failed"
		}
		return false, &TransientError{Err: errors.New(boundUpstreamMessage(message, 300))}
	case "response.output_text.delta":
		if event.Delta != "" {
			onDelta(llmapp.Delta{Content: event.Delta})
		}
	case "response.reasoning_summary.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			onDelta(llmapp.Delta{ReasoningContent: event.Delta})
		}
	case "response.output_item.added":
		if event.Item == nil || event.Item.Type != "function_call" {
			return false, nil
		}
		ref := streamCallRef{id: event.Item.CallID, name: s.names.originalName(event.Item.Name)}
		if ref.id == "" {
			ref.id = event.Item.ID
		}
		s.calls[event.OutputIndex] = ref
		onDelta(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{
			Index: event.OutputIndex,
			ID:    ref.id,
			Name:  ref.name,
		}})
	case "response.function_call_arguments.delta":
		if event.Delta == "" {
			return false, nil
		}
		ref := s.calls[event.OutputIndex]
		onDelta(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{
			Index:     event.OutputIndex,
			ID:        ref.id,
			Name:      ref.name,
			Arguments: event.Delta,
		}})
	case "response.completed", "response.incomplete":
		payload := responsesResponsePayload{}
		if event.Response != nil {
			payload = *event.Response
		}
		hasToolCalls := len(s.calls) > 0
		delta := llmapp.Delta{FinishReason: responsesFinishReason(payload, hasToolCalls)}
		if payload.Usage != nil {
			delta.Usage = responsesUsageMap(payload.Usage)
		}
		// The completed response carries the full output array, so the
		// provider-issued reasoning items are captured here exactly as the
		// non-streaming normalizer captures them (spec §7.2 replay parity).
		delta.ReasoningItems = responsesReasoningItems(payload.Output)
		onDelta(delta)
		return true, nil
	}
	return false, nil
}

// applyResponsesReasoning maps the config's thinking settings onto the
// Responses payload (spec §7.2): reasoning.effort carries the effort and
// reasoning.summary is requested when thinking is enabled so the stream can
// surface the model's thinking. thinking_type auto with effort none adds no
// reasoning key at all; thinking_type disabled maps to effort none.
func applyResponsesReasoning(payload *responsesRequestPayload, cfg llm.Config) {
	thinking := cfg.EffectiveThinkingType()
	effort := cfg.EffectiveReasoningEffort()
	reasoning := responsesReasoningPayload{}
	include := false
	switch thinking {
	case llm.ThinkingTypeEnabled:
		reasoning.Summary = "auto"
		include = true
	case llm.ThinkingTypeDisabled:
		reasoning.Effort = llm.ReasoningEffortNone
		include = true
	}
	if effort != llm.ReasoningEffortNone {
		reasoning.Effort = effort
		include = true
	}
	if include {
		payload.Reasoning = &reasoning
	}
}

type responsesRequestPayload struct {
	Model           string                     `json:"model"`
	Input           []any                      `json:"input"`
	Tools           []responsesTool            `json:"tools,omitempty"`
	ToolChoice      string                     `json:"tool_choice,omitempty"`
	MaxOutputTokens int                        `json:"max_output_tokens,omitempty"`
	Stream          bool                       `json:"stream,omitempty"`
	Reasoning       *responsesReasoningPayload `json:"reasoning,omitempty"`
}

// responsesReasoningPayload is the reasoning configuration of POST /responses.
type responsesReasoningPayload struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// responsesMessageItem is one role message item. Text-only messages keep the
// byte-compatible string content; a message with images serializes content as
// the Responses content-parts array (input_text / input_image).
type responsesMessageItem struct {
	Role    string
	Content string
	Images  []llmapp.ChatImage
}

// responsesInputPart is one Responses input content part.
type responsesInputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// MarshalJSON preserves the string content for text-only messages and emits the
// content-parts array only when the message carries images.
func (m responsesMessageItem) MarshalJSON() ([]byte, error) {
	if len(m.Images) == 0 {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: m.Role, Content: m.Content})
	}
	parts := make([]responsesInputPart, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, responsesInputPart{Type: "input_text", Text: m.Content})
	}
	for _, image := range m.Images {
		if len(image.Data) == 0 {
			continue
		}
		parts = append(parts, responsesInputPart{
			Type:     "input_image",
			ImageURL: "data:" + image.MIME + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
		})
	}
	return json.Marshal(struct {
		Role    string               `json:"role"`
		Content []responsesInputPart `json:"content"`
	}{Role: m.Role, Content: parts})
}

type responsesFunctionCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type responsesResponsePayload struct {
	ID                string                `json:"id"`
	Status            string                `json:"status"`
	Error             *responsesErrorDetail `json:"error"`
	IncompleteDetails *responsesIncomplete  `json:"incomplete_details"`
	// Output stays raw so reasoning items can be replayed verbatim (spec §7.2).
	Output []json.RawMessage `json:"output"`
	Usage  *responsesUsage   `json:"usage"`
}

type responsesErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesErrorEnvelope struct {
	Error responsesErrorDetail `json:"error"`
}

type responsesIncomplete struct {
	Reason string `json:"reason"`
}

type responsesOutputItem struct {
	Type      string                 `json:"type"`
	ID        string                 `json:"id"`
	Role      string                 `json:"role"`
	Content   []responsesContentPart `json:"content"`
	Summary   []responsesContentPart `json:"summary"`
	CallID    string                 `json:"call_id"`
	Name      string                 `json:"name"`
	Arguments string                 `json:"arguments"`
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	// InputTokensDetails is the OpenAI nested prompt-cache detail. Pointers keep
	// "reported zero" distinct from "not reported".
	InputTokensDetails *responsesInputTokensDetails `json:"input_tokens_details"`
	// CacheReadInputTokens/PromptCacheHitTokens are flattened aliases used by
	// compatible gateways.
	CacheReadInputTokens *int `json:"cache_read_input_tokens"`
	PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
}

// responsesInputTokensDetails is the Responses input_tokens_details object.
type responsesInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responsesStreamEvent struct {
	Type        string                    `json:"type"`
	Delta       string                    `json:"delta"`
	OutputIndex int                       `json:"output_index"`
	Item        *responsesOutputItem      `json:"item"`
	Response    *responsesResponsePayload `json:"response"`
	Error       *responsesErrorDetail     `json:"error"`
	Code        string                    `json:"code"`
	Message     string                    `json:"message"`
}

// errorMessage resolves the provider message carried by an error event, which
// may be flat (message) or nested (error.message / response.error.message).
func (e responsesStreamEvent) errorMessage() string {
	switch {
	case strings.TrimSpace(e.Message) != "":
		return e.Message
	case e.Error != nil && strings.TrimSpace(e.Error.Message) != "":
		return e.Error.Message
	case e.Response != nil && e.Response.Error != nil && strings.TrimSpace(e.Response.Error.Message) != "":
		return e.Response.Error.Message
	default:
		return ""
	}
}
