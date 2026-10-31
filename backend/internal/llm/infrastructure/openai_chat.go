package llminfra

import (
	"context"
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

// Chat performs one OpenAI-compatible chat completion with tools. Transient
// failures (network, timeout, HTTP 429 and 5xx) are retried up to
// cfg.MaxRetries; other 4xx responses are returned immediately. Context-window
// overflows are wrapped with llmapp.ErrContextOverflow so the runtime can
// compact and retry. The configured API key is never included in errors.
func (c *Client) Chat(ctx context.Context, cfg llm.Config, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	payload := buildChatPayload(req)
	attempts := cfg.MaxRetries + 1
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
		response, retryable, err := c.doChat(ctx, cfg, payload)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("llm chat request failed")
	}
	return llmapp.ChatResponse{}, lastErr
}

// doChat performs one attempt with the per-config request timeout and returns
// whether the failure is retryable.
func (c *Client) doChat(ctx context.Context, cfg llm.Config, payload chatRequestPayload) (llmapp.ChatResponse, bool, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := c.newRequest(attemptCtx, cfg, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return llmapp.ChatResponse{}, false, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return llmapp.ChatResponse{}, false, contextErr(ctx, err)
		}
		if isTimeout(err) {
			return llmapp.ChatResponse{}, true, errors.New("llm provider request timed out")
		}
		return llmapp.ChatResponse{}, true, errors.New("llm provider request failed")
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
		retryable, err := classifyChatFailure(cfg, response.StatusCode, raw)
		return llmapp.ChatResponse{}, retryable, err
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return llmapp.ChatResponse{}, true, errors.New("read llm chat response failed")
	}
	parsed, err := parseChatCompletion(raw)
	if err != nil {
		return llmapp.ChatResponse{}, false, err
	}
	return parsed, false, nil
}

// classifyChatFailure turns a non-2xx upstream response into a safe error and
// reports whether it may be retried. 429/5xx are transient; 4xx are permanent.
func classifyChatFailure(cfg llm.Config, status int, raw []byte) (bool, error) {
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
	message = sanitizeUpstreamMessage(message, cfg.APIKey, 300)
	if isChatContextOverflow(status, code, message) {
		return false, fmt.Errorf("%w: provider returned HTTP %d", llmapp.ErrContextOverflow, status)
	}
	retryable := status == http.StatusTooManyRequests || status >= 500
	return retryable, fmt.Errorf("llm provider returned HTTP %d: %s", status, message)
}

// buildChatPayload maps the application request to the OpenAI wire format.
func buildChatPayload(req llmapp.ChatRequest) chatRequestPayload {
	messages := make([]chatMessagePayload, 0, len(req.Messages))
	for _, message := range req.Messages {
		item := chatMessagePayload{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			item.ToolCalls = append(item.ToolCalls, chatToolCallPayload{
				ID:   call.ID,
				Type: "function",
				Function: chatFunctionCallPayload{
					Name:      call.Name,
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
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = llmapp.DefaultChatMaxOutputTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]chatToolPayload, 0, len(req.Tools))
		for _, tool := range req.Tools {
			parameters := json.RawMessage(tool.Parameters)
			if len(parameters) == 0 {
				parameters = json.RawMessage(`{}`)
			}
			tools = append(tools, chatToolPayload{
				Type: "function",
				Function: chatFunctionDefinition{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  parameters,
				},
			})
		}
		payload.Tools = tools
		payload.ToolChoice = "auto"
	}
	return payload
}

// parseChatCompletion decodes a successful OpenAI-compatible completion.
func parseChatCompletion(raw []byte) (llmapp.ChatResponse, error) {
	var payload chatCompletionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return llmapp.ChatResponse{}, errors.New("decode llm chat response failed")
	}
	if len(payload.Choices) == 0 {
		return llmapp.ChatResponse{}, errors.New("llm provider returned no choices")
	}
	choice := payload.Choices[0]
	response := llmapp.ChatResponse{FinishReason: choice.FinishReason}
	if choice.Message.Content != nil {
		response.Content = *choice.Message.Content
	}
	for _, call := range choice.Message.ToolCalls {
		response.ToolCalls = append(response.ToolCalls, llmapp.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	if payload.Usage != nil {
		response.Usage = map[string]int{
			"prompt_tokens":     payload.Usage.PromptTokens,
			"completion_tokens": payload.Usage.CompletionTokens,
			"total_tokens":      payload.Usage.TotalTokens,
		}
	}
	return response, nil
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

type chatRequestPayload struct {
	Model       string               `json:"model"`
	Messages    []chatMessagePayload `json:"messages"`
	Tools       []chatToolPayload    `json:"tools,omitempty"`
	ToolChoice  string               `json:"tool_choice,omitempty"`
	Temperature float64              `json:"temperature"`
	MaxTokens   int                  `json:"max_tokens"`
	Stream      bool                 `json:"stream"`
}

type chatMessagePayload struct {
	Role       string                `json:"role"`
	Content    string                `json:"content,omitempty"`
	ToolCalls  []chatToolCallPayload `json:"tool_calls,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
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
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
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
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type chatErrorPayload struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}
