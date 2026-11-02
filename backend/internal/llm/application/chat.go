package llmapp

import (
	"context"
	"encoding/json"
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

	// CodeLLMConfigInvalid marks a non-retryable configuration error such as a
	// missing default model or a default model that is not in the catalog.
	CodeLLMConfigInvalid = "llm_config_invalid"
	// CodeLLMCapabilityMismatch marks a non-retryable capability error: the
	// resolved model cannot serve the role (tools/vision/reasoning).
	CodeLLMCapabilityMismatch = "llm_capability_mismatch"
)

// ErrContextOverflow marks an upstream rejection caused by the request
// exceeding the model context window. Callers recover by compacting the
// context and retrying; on its own it is not a terminal run failure.
var ErrContextOverflow = errors.New("llm context overflow")

// ErrNotImplemented marks a protocol adapter capability whose wire
// implementation has not landed yet. The OpenAI Responses adapter is fully
// implemented in infrastructure/responses.go; chat-completions streaming still
// fails closed instead of silently falling back to another protocol. Callers
// must surface this error rather than pretend the request succeeded.
var ErrNotImplemented = errors.New("llm protocol adapter is not implemented")

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
//
// ReasoningContent is the provider's thinking text for an assistant turn. It
// must be replayed on later turns for reasoning models that require it
// (spec §7.2): the adapter emits it only when non-empty.
//
// ReasoningItems carries opaque provider-issued reasoning output items (OpenAI
// Responses). The Responses adapter replays them verbatim before the matching
// tool calls because the wire item (id/summary/encrypted content) cannot be
// reconstructed from text alone. It is nil for chat-completions turns.
type ChatMessage struct {
	Role             string
	Content          string
	ToolCalls        []ToolCall
	ToolCallID       string
	ReasoningContent string
	ReasoningItems   []json.RawMessage
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
// ReasoningContent captures the provider thinking text (chat completions
// reasoning_content / Responses reasoning summary) so the runner can replay it
// on the next turn (spec §7.2). ReasoningItems keeps the opaque Responses
// reasoning items for the same replay.
type ChatResponse struct {
	Content          string
	ReasoningContent string
	ReasoningItems   []json.RawMessage
	ToolCalls        []ToolCall
	Usage            map[string]int
	FinishReason     string
}

// Delta is one normalized streaming chunk emitted by an Adapter. Usually one
// payload field is set per chunk: Content for assistant text, ReasoningContent
// for thinking text, ToolCall for one function-call fragment. The terminal
// chunk carries FinishReason and, when the upstream reports it, Usage.
type Delta struct {
	Content          string
	ReasoningContent string
	ToolCall         *ToolCallDelta
	FinishReason     string
	Usage            map[string]int
}

// ToolCallDelta is one streamed fragment of a function call. Index identifies
// the call being assembled; Arguments is the raw JSON fragment as received.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// Adapter is one config-bound upstream protocol implementation. The chat
// gateway selects the adapter from the configuration protocol and delegates
// every completion and stream to it. Implementations must never include the
// configured API key (or request headers) in returned errors.
type Adapter interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	Stream(ctx context.Context, req ChatRequest, onDelta func(Delta)) error
}

// ModelRequirements declares the capabilities a calling role needs before a
// run is dispatched (spec §7.3). The zero value requires nothing.
type ModelRequirements struct {
	Tools     bool
	Vision    bool
	Reasoning bool
}

// ModelInfo is the resolved target for an agent run: the default LLM
// configuration and the selected catalog model. ContextWindow/MaxOutput come
// from llm_model metadata and fall back to conservative defaults when the
// upstream catalog does not report them. Capabilities is the merged 0.2.1
// capability set used by role/vision checks.
type ModelInfo struct {
	ConfigID      int64
	ModelID       int64
	Model         string
	ContextWindow int
	MaxOutput     int
	SupportsTools bool
	Capabilities  llm.Capabilities
	// FallbackFrom records the model that was requested but not used. P4 keeps
	// it empty: a model is never switched automatically (D24).
	FallbackFrom string
}

// ChatGateway is the agent runtime's LLM port. ResolveModel selects the
// configuration (explicit cfgID or the default config) and a catalog model
// (explicit model name or the config's default_model_id), then validates that
// the model can serve the caller's capabilities; Chat performs one completion.
//
// Implementations must never leak the configured API key through returned
// errors, and must classify context-window overflows with ErrContextOverflow.
type ChatGateway interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ResolveModel(ctx context.Context, cfgID int64, modelID string, required ...ModelRequirements) (ModelInfo, error)
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

// ResolveModel returns the target configuration/model for a run. An explicit
// cfgID selects that configuration (0 selects the default configuration), and
// a non-empty modelID selects that catalog model. An empty modelID selects the
// configuration's explicit default_model_id; models[0] is never used.
func (s *Service) ResolveModel(ctx context.Context, cfgID int64, modelID string, required ...ModelRequirements) (ModelInfo, error) {
	cfg, chosen, err := s.resolveChatTarget(ctx, cfgID, modelID)
	if err != nil {
		return ModelInfo{}, err
	}
	requirements := append([]ModelRequirements(nil), required...)
	if err := validateThinkingSettings(cfg); err != nil {
		return ModelInfo{}, err
	}
	// Both adapters forward any non-none reasoning effort, independent of the
	// thinking mode, so an explicit effort also requires a reasoning-capable
	// model (spec §7.2/§7.3).
	if cfg.EffectiveThinkingType() == llm.ThinkingTypeEnabled ||
		cfg.EffectiveReasoningEffort() != llm.ReasoningEffortNone {
		requirements = append(requirements, ModelRequirements{Reasoning: true})
	}
	if err := validateModelCapabilities(chosen, requirements...); err != nil {
		return ModelInfo{}, err
	}
	capabilities := chosen.EffectiveCapabilities()
	return ModelInfo{
		ConfigID: chosen.ConfigID,
		ModelID:  chosen.ID,
		Model:    chosen.Model,
		// The structured capability set is authoritative; EffectiveCapabilities
		// falls back to the legacy llm_model.context_window column.
		ContextWindow: capabilities.ContextWindow,
		MaxOutput:     chosen.MaxOutput,
		SupportsTools: capabilities.SupportsTools,
		Capabilities:  capabilities,
	}, nil
}

// validateThinkingSettings rejects the contradictory combination of disabled
// thinking with a non-none reasoning effort: "disabled" asks the provider not
// to think while the effort asks it to tune reasoning. Resolution fails with the
// stable non-retryable configuration error instead of forwarding both; the
// stored pair is left for the operator to fix (spec §7.4).
func validateThinkingSettings(cfg llm.Config) error {
	effort := cfg.EffectiveReasoningEffort()
	if cfg.EffectiveThinkingType() != llm.ThinkingTypeDisabled || effort == llm.ReasoningEffortNone {
		return nil
	}
	return &platform.Error{
		Status: 400,
		Code:   CodeLLMConfigInvalid,
		Message: fmt.Sprintf(
			"thinking_type=disabled cannot be combined with reasoning_effort=%q; set reasoning_effort=none or enable thinking",
			effort),
		Details: map[string]any{
			"thinking_type":    llm.ThinkingTypeDisabled,
			"reasoning_effort": effort,
		},
	}
}

// Chat performs one completion against the default OpenAI-compatible upstream.
func (s *Service) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	cfg, chosen, err := s.resolveChatTarget(ctx, 0, req.Model)
	if err != nil {
		return ChatResponse{}, err
	}
	if err := validateThinkingSettings(cfg); err != nil {
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

// resolveChatTarget selects the configuration and one catalog model. It
// returns stable ai_unavailable errors for a missing/disabled configuration and
// typed non-retryable configuration errors for an invalid default model, so a
// misconfiguration is not retried while a temporarily unavailable gateway is.
func (s *Service) resolveChatTarget(ctx context.Context, cfgID int64, model string) (llm.Config, llm.Model, error) {
	if s.chat == nil {
		return llm.Config{}, llm.Model{}, aiUnavailable("llm chat gateway is not configured")
	}
	cfg, err := s.selectConfig(ctx, cfgID)
	if err != nil {
		return llm.Config{}, llm.Model{}, err
	}

	models, err := s.repo.ListModelsByConfig(ctx, cfg.ID)
	if err != nil {
		return llm.Config{}, llm.Model{}, err
	}
	name := strings.TrimSpace(model)
	if name != "" {
		for i := range models {
			if models[i].Model != name {
				continue
			}
			if !models[i].Selectable() {
				return llm.Config{}, llm.Model{}, llmConfigError(
					fmt.Sprintf("llm model %q is deprecated or disabled; choose another model", name))
			}
			return cfg, models[i], nil
		}
		return llm.Config{}, llm.Model{}, aiUnavailable(fmt.Sprintf("llm model %q is not in the catalog", name))
	}

	// The spec forbids deriving the target from models[0]: a config without an
	// explicit default_model_id is a configuration error, not a silent pick.
	if cfg.DefaultModelID <= 0 {
		return llm.Config{}, llm.Model{}, &platform.Error{
			Status:  400,
			Code:    CodeLLMConfigInvalid,
			Message: "llm config has no default model; set default_model_id before running agents (models[0] is never used)",
			Details: map[string]any{"field": "default_model_id"},
		}
	}
	for i := range models {
		if models[i].ID != cfg.DefaultModelID {
			continue
		}
		if !models[i].Selectable() {
			return llm.Config{}, llm.Model{}, llmConfigError(fmt.Sprintf(
				"default model %q is deprecated or disabled; set default_model_id to an active model", models[i].Model))
		}
		return cfg, models[i], nil
	}
	return llm.Config{}, llm.Model{}, llmConfigError(fmt.Sprintf(
		"default model %d is not in the catalog of llm config %q; refresh the model catalog or set default_model_id",
		cfg.DefaultModelID, cfg.Name))
}

// selectConfig loads the explicitly requested configuration, or the unique
// default configuration when cfgID is zero.
func (s *Service) selectConfig(ctx context.Context, cfgID int64) (llm.Config, error) {
	var cfg llm.Config
	if cfgID > 0 {
		selected, err := s.repo.GetConfig(ctx, cfgID)
		if err != nil {
			return llm.Config{}, mapRepositoryError(err)
		}
		cfg = selected
		if cfg.Status != llm.StatusActive {
			return llm.Config{}, aiUnavailable("llm config is disabled")
		}
		return cfg, nil
	}
	configs, err := s.repo.ListConfigs(ctx)
	if err != nil {
		return llm.Config{}, err
	}
	for i := range configs {
		if configs[i].IsDefault {
			cfg = configs[i]
			break
		}
	}
	if cfg.ID == 0 {
		return llm.Config{}, aiUnavailable("no default llm config is configured")
	}
	if cfg.Status != llm.StatusActive {
		return llm.Config{}, aiUnavailable("default llm config is disabled")
	}
	return cfg, nil
}

// validateModelCapabilities rejects a model that cannot serve the calling role.
// Every requirement is checked so the error names all missing capabilities.
func validateModelCapabilities(model llm.Model, required ...ModelRequirements) error {
	capabilities := model.EffectiveCapabilities()
	missing := make([]string, 0, 3)
	for _, requirement := range required {
		if requirement.Tools && !capabilities.SupportsTools && !containsString(missing, "tools") {
			missing = append(missing, "tools")
		}
		if requirement.Vision && !capabilities.SupportsVision && !containsString(missing, "vision") {
			missing = append(missing, "vision")
		}
		if requirement.Reasoning && !capabilities.SupportsReasoning && !containsString(missing, "reasoning") {
			missing = append(missing, "reasoning")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &platform.Error{
		Status: 400,
		Code:   CodeLLMCapabilityMismatch,
		Message: fmt.Sprintf("llm model %q does not support %s required by this role; choose a model with that capability",
			model.Model, strings.Join(missing, ", ")),
		Details: map[string]any{"model": model.Model, "missing_capabilities": missing},
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// llmConfigError builds the non-retryable 400 used for invalid LLM
// configuration that an operator must fix (spec §7.3/§7.4).
func llmConfigError(message string) *platform.Error {
	return &platform.Error{
		Status:  400,
		Code:    CodeLLMConfigInvalid,
		Message: message,
	}
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
