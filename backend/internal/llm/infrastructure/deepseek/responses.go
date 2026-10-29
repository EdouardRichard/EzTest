package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// responses wire shapes (design §8.4).
type responsesRequestPayload struct {
	Model           string             `json:"model"`
	Input           []responsesMessage `json:"input"`
	Stream          bool               `json:"stream"`
	MaxOutputTokens int                `json:"max_output_tokens,omitempty"`
}

type responsesMessage struct {
	Role    string                 `json:"role"`
	Content []responsesContentPart `json:"content"`
}

type responsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesResponse struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *responsesUsagePayload `json:"usage"`
	Error *upstreamErrorBody     `json:"error"`
}

type responsesUsagePayload struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type responsesStreamEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Response *struct {
		Usage *responsesUsagePayload `json:"usage"`
		Error *upstreamErrorBody     `json:"error"`
	} `json:"response"`
	Error *upstreamErrorBody `json:"error"`
}

// responsesPayload converts a normalized request to the responses wire shape.
// Text parts become input_text and image parts become input_image with the
// image URL as a plain string.
func (c *client) responsesPayload(req llmapp.ChatRequest, stream bool) responsesRequestPayload {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}
	input := make([]responsesMessage, 0, len(req.Messages))
	for _, message := range req.Messages {
		content := make([]responsesContentPart, 0, len(message.Content))
		for _, part := range message.Content {
			if part.Type == "image_url" || part.ImageURL != "" {
				content = append(content, responsesContentPart{Type: "input_image", ImageURL: part.ImageURL})
				continue
			}
			content = append(content, responsesContentPart{Type: "input_text", Text: part.Text})
		}
		input = append(input, responsesMessage{Role: message.Role, Content: content})
	}
	return responsesRequestPayload{
		Model:           model,
		Input:           input,
		Stream:          stream,
		MaxOutputTokens: req.MaxTokens,
	}
}

// Chat sends a non-streaming responses request and parses its text and usage.
// Tools are rejected: P1's agent runtime only supports tool calling on chat
// completions (design §7.3).
func (c *client) responsesChat(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	if len(req.Tools) > 0 {
		return llmapp.ChatResponse{}, toolsUnsupportedError()
	}
	resp, err := c.do(ctx, http.MethodPost, "/responses", c.responsesPayload(req, false))
	if err != nil {
		return llmapp.ChatResponse{}, err
	}
	var decoded responsesResponse
	if err := decodeJSON(resp, &decoded); err != nil {
		return llmapp.ChatResponse{}, err
	}
	if decoded.Error != nil {
		return llmapp.ChatResponse{}, upstreamStreamError()
	}
	text := responseText(decoded)
	if strings.TrimSpace(text) == "" {
		return llmapp.ChatResponse{}, invalidResponseError(nil)
	}
	usage := llmapp.Usage{}
	if decoded.Usage != nil {
		usage = usageFromTokens(decoded.Usage.InputTokens, decoded.Usage.OutputTokens, decoded.Usage.TotalTokens)
	}
	return llmapp.ChatResponse{Text: text, Usage: usage}, nil
}

// responsesChatStream sends a streaming responses request. Tools are rejected
// before the upstream is called (design §7.3).
func (c *client) responsesChatStream(ctx context.Context, req llmapp.ChatRequest) (<-chan llmapp.StreamEvent, error) {
	if len(req.Tools) > 0 {
		return nil, toolsUnsupportedError()
	}
	resp, err := c.do(ctx, http.MethodPost, "/responses", c.responsesPayload(req, true))
	if err != nil {
		return nil, err
	}
	events := make(chan llmapp.StreamEvent)
	go streamResponses(ctx, resp.Body, events)
	return events, nil
}

// toolsUnsupportedError is the typed rejection for a responses request carrying
// tools. P1 deliberately supports tool calling on chat completions only, so the
// adapter fails defensively before the upstream is called (design §7.3).
func toolsUnsupportedError() *llmapp.LLMError {
	return llmapp.NewLLMError(platform.CodeLLMBadRequest, "llm tools require the chat completions protocol", false, 0, nil)
}

// responseText extracts the assistant text from a non-streaming responses
// payload, preferring the convenience output_text field.
func responseText(decoded responsesResponse) string {
	if strings.TrimSpace(decoded.OutputText) != "" {
		return decoded.OutputText
	}
	var builder strings.Builder
	for _, item := range decoded.Output {
		for _, content := range item.Content {
			switch content.Type {
			case "output_text", "text":
				builder.WriteString(content.Text)
			}
		}
	}
	return builder.String()
}

// streamResponses parses the responses event stream into normalized events. It
// emits exactly one terminal Done or Err event.
func streamResponses(ctx context.Context, body io.ReadCloser, out chan<- llmapp.StreamEvent) {
	defer close(out)
	defer func() { _ = body.Close() }()

	reader := newSSEReader(body)
	usage := llmapp.Usage{}
	for {
		frame, err := reader.next()
		if errors.Is(err, io.EOF) {
			// A stream that only ended because the connection closed is a
			// failure: the protocol requires a response.completed event.
			emit(ctx, out, llmapp.StreamEvent{Err: streamEndedError()})
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
			emit(ctx, out, llmapp.StreamEvent{Done: true, Usage: usage})
			return
		}
		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			emit(ctx, out, llmapp.StreamEvent{Err: invalidStreamPayloadError(err)})
			return
		}
		if event.Error != nil || (event.Response != nil && event.Response.Error != nil) {
			emit(ctx, out, llmapp.StreamEvent{Err: upstreamStreamError()})
			return
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				if !emit(ctx, out, llmapp.StreamEvent{Delta: event.Delta}) {
					return
				}
			}
		case "response.completed", "response.done":
			if event.Response != nil && event.Response.Usage != nil {
				usage = usageFromTokens(event.Response.Usage.InputTokens, event.Response.Usage.OutputTokens, event.Response.Usage.TotalTokens)
			}
			emit(ctx, out, llmapp.StreamEvent{Done: true, Usage: usage})
			return
		case "response.failed", "error":
			emit(ctx, out, llmapp.StreamEvent{Err: upstreamStreamError()})
			return
		default:
			// response.created, response.in_progress, output items and other
			// lifecycle events carry no user-visible text.
		}
	}
}
