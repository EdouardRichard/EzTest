package llmapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// DefaultChatMaxOutputTokens bounds the completion when neither the caller nor
// the model catalog provides a max_output value.
const DefaultChatMaxOutputTokens = 4096

// SettingRequestTimeoutSeconds and SettingMaxRetries are the P0 settings keys
// consumed by the P1 chat gateway (architecture-design §7).
const (
	SettingRequestTimeoutSeconds = "llm.request_timeout_seconds"
	SettingMaxRetries            = "llm.max_retries"
)

// Stable P1 error codes (spec §8.4). platform does not own them yet; the
// application maps upstream failures into these codes and the HTTP layer keeps
// the P0 envelope.
const (
	CodeAIUnavailable   = "ai_unavailable"
	CodeContextOverflow = "context_overflow"
)

// ErrContextOverflow marks an upstream rejection caused by the request
// exceeding the model context window. Callers recover by compacting the
// context and retrying; on its own it is not a terminal run failure.
var ErrContextOverflow = errors.New("llm context overflow")

// IsContextOverflow reports whether err is a classified context-window
// overflow, either as the raw sentinel or as the mapped platform error.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContextOverflow) {
		return true
	}
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Code == CodeContextOverflow
}

// ToolCall is one function call requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatMessage is one OpenAI-compatible transcript message. Assistant messages
// may carry ToolCalls; tool messages carry the ToolCallID they answer.
type ChatMessage struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// ToolDefinition is one OpenAI function tool exposed to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  []byte
}

// ChatRequest is one chat-completions request. Model and MaxOutputTokens are
// normally filled by the gateway from the resolved model catalog.
type ChatRequest struct {
	Model           string
	Messages        []ChatMessage
	Tools           []ToolDefinition
	MaxOutputTokens int
}

// ChatResponse is the normalized assistant turn returned by the upstream.
type ChatResponse struct {
	Content      string
	ToolCalls    []ToolCall
	Usage        map[string]int
	FinishReason string
}

// ModelInfo is the resolved target for an agent run: the default LLM
// configuration and the selected catalog model. ContextWindow/MaxOutput come
// from llm_model metadata and fall back to conservative defaults when the
// upstream catalog does not report them.
type ModelInfo struct {
	ConfigID      int64
	Model         string
	ContextWindow int
	MaxOutput     int
	SupportsTools bool
}

// ChatGateway is the agent runtime's LLM port. ResolveModel selects the
// default configuration and a catalog model; Chat performs one completion.
//
// Implementations must never leak the configured API key through returned
// errors, and must classify context-window overflows with ErrContextOverflow.
type ChatGateway interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ResolveModel(ctx context.Context, model string) (ModelInfo, error)
}

// Option configures optional P1 dependencies of Service without changing the
// frozen P0 constructor.
type Option func(*Service)

// WithChatClient enables the chat gateway on the LLM application service.
func WithChatClient(client ChatClient) Option {
	return func(s *Service) { s.chat = client }
}

// WithSettingsReader supplies the settings keys used by the chat gateway. It is
// optional: a nil reader keeps the llm_config timeout/retry values.
func WithSettingsReader(reader SettingsReader) Option {
	return func(s *Service) { s.settings = reader }
}

// ResolveModel returns the target configuration/model for a run. An empty model
// selects the first catalog model of the default configuration; a non-empty
// model must match an llm_model row (profile.model).
func (s *Service) ResolveModel(ctx context.Context, model string) (ModelInfo, error) {
	_, chosen, err := s.resolveChatTarget(ctx, model)
	if err != nil {
		return ModelInfo{}, err
	}
	return ModelInfo{
		ConfigID:      chosen.ConfigID,
		Model:         chosen.Model,
		ContextWindow: chosen.ContextWindow,
		MaxOutput:     chosen.MaxOutput,
		SupportsTools: chosen.SupportsTools,
	}, nil
}

// Chat performs one completion against the default OpenAI-compatible upstream.
func (s *Service) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	cfg, chosen, err := s.resolveChatTarget(ctx, req.Model)
	if err != nil {
		return ChatResponse{}, err
	}
	if s.settings != nil {
		// Settings are the P1 source of truth; a missing/failed read keeps the
		// stored llm_config value so the gateway still works.
		if value, err := s.settings.Int(ctx, SettingRequestTimeoutSeconds); err == nil && value > 0 {
			cfg.TimeoutSeconds = int(value)
		}
		if value, err := s.settings.Int(ctx, SettingMaxRetries); err == nil && value >= 0 {
			cfg.MaxRetries = int(value)
		}
	}

	req.Model = chosen.Model
	if req.MaxOutputTokens <= 0 {
		req.MaxOutputTokens = chosen.MaxOutput
		if req.MaxOutputTokens <= 0 {
			req.MaxOutputTokens = DefaultChatMaxOutputTokens
		}
	}

	response, err := s.chat.Chat(ctx, cfg, req)
	if err != nil {
		return ChatResponse{}, mapChatError(err, cfg.APIKey)
	}
	return response, nil
}

// resolveChatTarget selects the default llm_config and one catalog model. It
// returns stable ai_unavailable errors instead of empty successes so the worker
// can apply the model-unavailable retry policy.
func (s *Service) resolveChatTarget(ctx context.Context, model string) (llm.Config, llm.Model, error) {
	if s.chat == nil {
		return llm.Config{}, llm.Model{}, aiUnavailable("llm chat gateway is not configured")
	}
	configs, err := s.repo.ListConfigs(ctx)
	if err != nil {
		return llm.Config{}, llm.Model{}, err
	}
	var selected *llm.Config
	for i := range configs {
		if configs[i].IsDefault {
			selected = &configs[i]
			break
		}
	}
	if selected == nil {
		return llm.Config{}, llm.Model{}, aiUnavailable("no default llm config is configured")
	}
	cfg := *selected
	if cfg.Status != llm.StatusActive {
		return llm.Config{}, llm.Model{}, aiUnavailable("default llm config is disabled")
	}

	models, err := s.repo.ListModelsByConfig(ctx, cfg.ID)
	if err != nil {
		return llm.Config{}, llm.Model{}, err
	}
	name := strings.TrimSpace(model)
	if name != "" {
		for i := range models {
			if models[i].Model == name {
				return cfg, models[i], nil
			}
		}
		return llm.Config{}, llm.Model{}, aiUnavailable(fmt.Sprintf("llm model %q is not in the catalog", name))
	}
	if len(models) == 0 {
		return llm.Config{}, llm.Model{}, aiUnavailable("default llm config has no models")
	}
	return cfg, models[0], nil
}

// aiUnavailable builds the stable 503 used when no usable model target exists.
func aiUnavailable(message string) *platform.Error {
	return &platform.Error{
		Status:    503,
		Code:      CodeAIUnavailable,
		Message:   message,
		Retryable: true,
	}
}

// contextOverflowError builds the stable 503 returned when the model rejects a
// request for exceeding its context window.
func contextOverflowError() *platform.Error {
	return &platform.Error{
		Status:  503,
		Code:    CodeContextOverflow,
		Message: "model context window exceeded",
	}
}

// mapChatError normalizes upstream failures: context overflows stay
// recognizable as such (the runner compacts and retries), everything else
// becomes a retryable ai_unavailable with the API key redacted.
func mapChatError(err error, secret string) error {
	if IsContextOverflow(err) {
		return contextOverflowError()
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		apiErr.Message = redact(apiErr.Message, secret)
		return apiErr
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "llm chat request failed"
	}
	return aiUnavailable(redact(message, secret))
}
