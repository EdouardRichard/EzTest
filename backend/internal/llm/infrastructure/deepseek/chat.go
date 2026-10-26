package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// chat completions wire shapes (design §8.4).
type chatRequestPayload struct {
	Model     string               `json:"model"`
	Messages  []chatMessagePayload `json:"messages"`
	Stream    bool                 `json:"stream"`
	MaxTokens int                  `json:"max_tokens,omitempty"`
}

type chatMessagePayload struct {
	Role    string               `json:"role"`
	Content []chatContentPayload `json:"content"`
}

type chatContentPayload struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL *chatImagePayload `json:"image_url,omitempty"`
}

type chatImagePayload struct {
	URL string `json:"url"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *chatUsagePayload  `json:"usage"`
	Error *upstreamErrorBody `json:"error"`
}

type chatUsagePayload struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsagePayload  `json:"usage"`
	Error *upstreamErrorBody `json:"error"`
}

// chatPayload converts a normalized request to the chat completions wire shape.
// Message content is always an array of typed parts.
func (c *client) chatPayload(req llmapp.ChatRequest, stream bool) chatRequestPayload {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}
	messages := make([]chatMessagePayload, 0, len(req.Messages))
	for _, message := range req.Messages {
		content := make([]chatContentPayload, 0, len(message.Content))
		for _, part := range message.Content {
			if part.Type == "image_url" || part.ImageURL != "" {
				content = append(content, chatContentPayload{
					Type:     "image_url",
					ImageURL: &chatImagePayload{URL: part.ImageURL},
				})
				continue
			}
			content = append(content, chatContentPayload{Type: "text", Text: part.Text})
		}
		messages = append(messages, chatMessagePayload{Role: message.Role, Content: content})
	}
	return chatRequestPayload{
		Model:     model,
		Messages:  messages,
		Stream:    stream,
		MaxTokens: req.MaxTokens,
	}
}

// Chat sends a non-streaming chat completions request and parses its text and
// usage.
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
	text := decoded.Choices[0].Message.Content
	if strings.TrimSpace(text) == "" {
		return llmapp.ChatResponse{}, invalidResponseError(nil)
	}
	usage := llmapp.Usage{}
	if decoded.Usage != nil {
		usage = usageFromTokens(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens, decoded.Usage.TotalTokens)
	}
	return llmapp.ChatResponse{Text: text, Usage: usage}, nil
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
// events. It emits exactly one terminal Done or Err event.
func streamChatCompletions(ctx context.Context, body io.ReadCloser, out chan<- llmapp.StreamEvent) {
	defer close(out)
	defer func() { _ = body.Close() }()

	reader := newSSEReader(body)
	usage := llmapp.Usage{}
	finished := false
	for {
		frame, err := reader.next()
		if errors.Is(err, io.EOF) {
			if finished {
				emit(ctx, out, llmapp.StreamEvent{Done: true, Usage: usage})
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
			emit(ctx, out, llmapp.StreamEvent{Done: true, Usage: usage})
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
			usage = usageFromTokens(chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, chunk.Usage.TotalTokens)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if !emit(ctx, out, llmapp.StreamEvent{Delta: choice.Delta.Content}) {
					return
				}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finished = true
			}
		}
		if finished {
			emit(ctx, out, llmapp.StreamEvent{Done: true, Usage: usage})
			return
		}
	}
}
