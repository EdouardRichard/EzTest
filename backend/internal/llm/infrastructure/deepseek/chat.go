package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// chat completions wire shapes (design §8.4, extended by §7.2).
type chatRequestPayload struct {
	Model           string               `json:"model"`
	Messages        []chatMessagePayload `json:"messages"`
	Tools           []chatToolPayload    `json:"tools,omitempty"`
	Stream          bool                 `json:"stream"`
	MaxTokens       int                  `json:"max_tokens,omitempty"`
	Thinking        *chatThinkingPayload `json:"thinking,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
}

// chatThinkingPayload is the thinking switch. The upstream expects it as an
// object ({"type":"enabled"|"disabled"}), not a bare string.
type chatThinkingPayload struct {
	Type string `json:"type"`
}

type chatToolPayload struct {
	Type     string                  `json:"type"`
	Function chatToolFunctionPayload `json:"function"`
}

type chatToolFunctionPayload struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatMessagePayload struct {
	Role             string                `json:"role"`
	Content          []chatContentPayload  `json:"content,omitempty"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCallPayload `json:"tool_calls,omitempty"`
	ToolCallID       string                `json:"tool_call_id,omitempty"`
	Name             string                `json:"name,omitempty"`
}

type chatContentPayload struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL *chatImagePayload `json:"image_url,omitempty"`
}

type chatImagePayload struct {
	URL string `json:"url"`
}

type chatToolCallPayload struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatCallFunction `json:"function"`
}

type chatCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// chatToolCallDelta is one streamed tool_calls fragment. The upstream sends the
// id and function name with the first fragment of an index and only argument
// fragments afterwards.
type chatToolCallDelta struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatCallFunction `json:"function"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []chatToolCallPayload `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsagePayload  `json:"usage"`
	Error *upstreamErrorBody `json:"error"`
}

type chatUsagePayload struct {
	PromptTokens            int                          `json:"prompt_tokens"`
	CompletionTokens        int                          `json:"completion_tokens"`
	TotalTokens             int                          `json:"total_tokens"`
	CompletionTokensDetails *chatCompletionTokensDetails `json:"completion_tokens_details"`
}

// chatCompletionTokensDetails carries the reasoning token accounting of
// thinking-mode responses.
type chatCompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string              `json:"content"`
			ReasoningContent string              `json:"reasoning_content"`
			ToolCalls        []chatToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsagePayload  `json:"usage"`
	Error *upstreamErrorBody `json:"error"`
}

// chatPayload converts a normalized request to the chat completions wire shape.
// Message content is always an array of typed parts; assistant content may be
// omitted when the turn only carried tool calls. Tools, thinking and
// reasoning_effort are sent only when set.
func (c *client) chatPayload(req llmapp.ChatRequest, stream bool) chatRequestPayload {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}
	messages := make([]chatMessagePayload, 0, len(req.Messages))
	for _, message := range req.Messages {
		payload := chatMessagePayload{
			Role:             message.Role,
			ReasoningContent: message.ReasoningContent,
			ToolCallID:       message.ToolCallID,
			Name:             message.Name,
		}
		for _, part := range message.Content {
			if part.Type == "image_url" || part.ImageURL != "" {
				payload.Content = append(payload.Content, chatContentPayload{
					Type:     "image_url",
					ImageURL: &chatImagePayload{URL: part.ImageURL},
				})
				continue
			}
			payload.Content = append(payload.Content, chatContentPayload{Type: "text", Text: part.Text})
		}
		for _, call := range message.ToolCalls {
			payload.ToolCalls = append(payload.ToolCalls, chatToolCallPayload{
				ID:       call.ID,
				Type:     "function",
				Function: chatCallFunction{Name: call.Name, Arguments: call.Arguments},
			})
		}
		messages = append(messages, payload)
	}
	payload := chatRequestPayload{
		Model:           model,
		Messages:        messages,
		Stream:          stream,
		MaxTokens:       req.MaxTokens,
		ReasoningEffort: req.ReasoningEffort,
	}
	if len(req.Tools) > 0 {
		payload.Tools = make([]chatToolPayload, 0, len(req.Tools))
		for _, tool := range req.Tools {
			payload.Tools = append(payload.Tools, chatToolPayload{
				Type: "function",
				Function: chatToolFunctionPayload{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.Parameters,
				},
			})
		}
	}
	if req.Thinking != "" {
		payload.Thinking = &chatThinkingPayload{Type: req.Thinking}
	}
	return payload
}

// Chat sends a non-streaming chat completions request and parses its text,
// reasoning content, tool calls, finish reason and usage.
func (c *client) chatCompletions(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", c.chatPayload(req, false))
	if err != nil {
		return llmapp.ChatResponse{}, err
	}
	var decoded chatCompletionResponse
	if err := decodeJSON(resp, &decoded); err != nil {
		return llmapp.ChatResponse{}, err
	}
	if decoded.Error != nil {
		return llmapp.ChatResponse{}, upstreamStreamError()
	}
	if len(decoded.Choices) == 0 {
		return llmapp.ChatResponse{}, invalidResponseError(nil)
	}
	message := decoded.Choices[0].Message
	toolCalls := toolCallsFromPayload(message.ToolCalls)
	// An empty content is valid when the model requested tool calls.
	if strings.TrimSpace(message.Content) == "" && len(toolCalls) == 0 {
		return llmapp.ChatResponse{}, invalidResponseError(nil)
	}
	return llmapp.ChatResponse{
		Text:             message.Content,
		ReasoningContent: message.ReasoningContent,
		ToolCalls:        toolCalls,
		FinishReason:     decoded.Choices[0].FinishReason,
		Usage:            decoded.Usage.llmUsage(),
	}, nil
}

// ChatStream sends a streaming chat completions request. The request is
// rejected before a channel is returned when the upstream fails to open, so the
// retry wrapper can retry that failure.
func (c *client) chatCompletionsStream(ctx context.Context, req llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", c.chatPayload(req, true))
	if err != nil {
		return nil, err
	}
	events := make(chan llmapp.StreamEvent)
	go streamChatCompletions(ctx, resp.Body, events)
	return events, nil
}

// streamChatCompletions parses chat completion SSE frames into normalized
// events. Reasoning deltas are forwarded as they arrive; tool call fragments are
// assembled by index and carried only on the terminal Done event. It emits
// exactly one terminal Done or Err event.
func streamChatCompletions(ctx context.Context, body io.ReadCloser, out chan<- llmapp.StreamEvent) {
	defer close(out)
	defer func() { _ = body.Close() }()

	reader := newSSEReader(body)
	usage := llmapp.Usage{}
	finishReason := ""
	calls := newToolCallAccumulator()
	finished := false
	for {
		frame, err := reader.next()
		if errors.Is(err, io.EOF) {
			if finished {
				emit(ctx, out, llmapp.StreamEvent{Done: true, ToolCalls: calls.snapshot(), FinishReason: finishReason, Usage: usage})
			} else {
				emit(ctx, out, llmapp.StreamEvent{Err: streamEndedError()})
			}
			return
		}
		if err != nil {
			emit(ctx, out, llmapp.StreamEvent{Err: invalidStreamPayloadError(err)})
			return
		}
		data := strings.TrimSpace(frame.data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			emit(ctx, out, llmapp.StreamEvent{Done: true, ToolCalls: calls.snapshot(), FinishReason: finishReason, Usage: usage})
			return
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			emit(ctx, out, llmapp.StreamEvent{Err: invalidStreamPayloadError(err)})
			return
		}
		if chunk.Error != nil {
			emit(ctx, out, llmapp.StreamEvent{Err: upstreamStreamError()})
			return
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.llmUsage()
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.ReasoningContent != "" {
				if !emit(ctx, out, llmapp.StreamEvent{ReasoningDelta: choice.Delta.ReasoningContent}) {
					return
				}
			}
			if choice.Delta.Content != "" {
				if !emit(ctx, out, llmapp.StreamEvent{Delta: choice.Delta.Content}) {
					return
				}
			}
			calls.apply(choice.Delta.ToolCalls)
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
				finished = true
			}
		}
		if finished {
			emit(ctx, out, llmapp.StreamEvent{Done: true, ToolCalls: calls.snapshot(), FinishReason: finishReason, Usage: usage})
			return
		}
	}
}

// toolCallsFromPayload converts the wire tool calls to normalized ones.
func toolCallsFromPayload(calls []chatToolCallPayload) []llmapp.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]llmapp.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, llmapp.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return out
}

// llmUsage converts a wire usage payload, including the optional reasoning
// token details, to the normalized usage.
func (u *chatUsagePayload) llmUsage() llmapp.Usage {
	if u == nil {
		return llmapp.Usage{}
	}
	usage := usageFromTokens(u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	if u.CompletionTokensDetails != nil {
		usage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return usage
}

// chatToolCallAccumulator assembles streamed tool call fragments by upstream
// index. The first fragment carries the id and function name; later fragments
// only append argument fragments.
type chatToolCallAccumulator struct {
	calls map[int]*llmapp.ToolCall
}

// newToolCallAccumulator returns an empty accumulator.
func newToolCallAccumulator() *chatToolCallAccumulator {
	return &chatToolCallAccumulator{calls: make(map[int]*llmapp.ToolCall)}
}

// apply merges one chunk's tool call fragments.
func (a *chatToolCallAccumulator) apply(deltas []chatToolCallDelta) {
	for _, delta := range deltas {
		call := a.calls[delta.Index]
		if call == nil {
			call = &llmapp.ToolCall{}
			a.calls[delta.Index] = call
		}
		if delta.ID != "" {
			call.ID = delta.ID
		}
		if delta.Function.Name != "" {
			call.Name = delta.Function.Name
		}
		call.Arguments += delta.Function.Arguments
	}
}

// snapshot returns the assembled calls ordered by upstream index.
func (a *chatToolCallAccumulator) snapshot() []llmapp.ToolCall {
	if len(a.calls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(a.calls))
	for index := range a.calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]llmapp.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		calls = append(calls, *a.calls[index])
	}
	return calls
}
