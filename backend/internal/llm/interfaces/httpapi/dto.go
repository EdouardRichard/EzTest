package llmapi

import (
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// configRequest is the create/update payload. Pointer fields distinguish
// "omitted" (keep/default) from an explicit zero value. api_key is write-only:
// it is accepted but never echoed back. default_model_id is a decimal ID string
// to match the IDs used by every response; the empty string clears it.
type configRequest struct {
	Name            *string            `json:"name"`
	Provider        *string            `json:"provider"`
	Protocol        *string            `json:"protocol"`
	BaseURL         *string            `json:"base_url"`
	APIKey          *string            `json:"api_key"`
	Headers         *map[string]string `json:"headers"`
	TimeoutSeconds  *int               `json:"timeout_seconds"`
	MaxRetries      *int               `json:"max_retries"`
	IsDefault       *bool              `json:"is_default"`
	Status          *string            `json:"status"`
	ThinkingType    *string            `json:"thinking_type"`
	ReasoningEffort *string            `json:"reasoning_effort"`
	DefaultModelID  *string            `json:"default_model_id"`
}

// input validates the 0.2.1 enums at the HTTP boundary (so unknown values are a
// typed 400 even before the service runs) and maps the payload onto ConfigInput.
// The cross-row rule that default_model_id must name an active model of the
// config stays in the application service, which owns the repository.
func (r configRequest) input() (llmapp.ConfigInput, error) {
	input := llmapp.ConfigInput{
		Name:           r.Name,
		Provider:       r.Provider,
		BaseURL:        r.BaseURL,
		APIKey:         r.APIKey,
		Headers:        r.Headers,
		TimeoutSeconds: r.TimeoutSeconds,
		MaxRetries:     r.MaxRetries,
		IsDefault:      r.IsDefault,
		Status:         r.Status,
	}
	if r.Protocol != nil {
		protocol := strings.TrimSpace(*r.Protocol)
		if !llm.ValidProtocol(protocol) {
			return llmapp.ConfigInput{}, platform.Invalid("protocol",
				"must be one of openai_chat_completions, openai_responses (legacy openai/openai_compatible accepted)")
		}
		input.Protocol = &protocol
	}
	if r.ThinkingType != nil {
		thinking := strings.TrimSpace(*r.ThinkingType)
		if !llm.ValidThinkingType(thinking) {
			return llmapp.ConfigInput{}, platform.Invalid("thinking_type", "must be one of auto, enabled, disabled")
		}
		input.ThinkingType = &thinking
	}
	if r.ReasoningEffort != nil {
		effort := strings.TrimSpace(*r.ReasoningEffort)
		if !llm.ValidReasoningEffort(effort) {
			return llmapp.ConfigInput{}, platform.Invalid("reasoning_effort",
				"must be one of none, low, medium, high, xhigh, max")
		}
		input.ReasoningEffort = &effort
	}
	if r.DefaultModelID != nil {
		value, err := parseDefaultModelID(*r.DefaultModelID)
		if err != nil {
			return llmapp.ConfigInput{}, err
		}
		input.DefaultModelID = &value
	}
	return input, nil
}

// parseDefaultModelID accepts the decimal ID string used by responses and maps
// the empty string to 0 ("not configured"). Negative and non-numeric values are
// a typed 400 so a malformed client cannot reach the repository.
func parseDefaultModelID(raw string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value < 0 {
		return 0, platform.Invalid("default_model_id", "must be a non-negative decimal model ID")
	}
	return value, nil
}

type configDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Provider        string            `json:"provider"`
	Protocol        string            `json:"protocol"`
	BaseURL         string            `json:"base_url"`
	APIKeySet       bool              `json:"api_key_set"`
	Headers         map[string]string `json:"headers"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	MaxRetries      int               `json:"max_retries"`
	IsDefault       bool              `json:"is_default"`
	Status          string            `json:"status"`
	ThinkingType    string            `json:"thinking_type"`
	ReasoningEffort string            `json:"reasoning_effort"`
	DefaultModelID  string            `json:"default_model_id,omitempty"`
	Models          []modelDTO        `json:"models"`
	CreatedBy       string            `json:"created_by,omitempty"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
}

// toConfigDTO maps one config plus its catalog rows. Protocol is the stored
// value: canonical values round-trip unchanged and legacy P0 values
// (openai/openai_compatible) stay visible so the admin UI can label them
// "OpenAI 兼容" instead of hiding existing configurations.
func toConfigDTO(cfg llm.Config, models []llm.Model) configDTO {
	headers := cfg.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	items := make([]modelDTO, 0, len(models))
	for _, model := range models {
		items = append(items, toModelDTO(model))
	}
	dto := configDTO{
		ID:              strconv.FormatInt(cfg.ID, 10),
		Name:            cfg.Name,
		Provider:        cfg.Provider,
		Protocol:        cfg.Protocol,
		BaseURL:         cfg.BaseURL,
		APIKeySet:       cfg.APIKeySet(),
		Headers:         headers,
		TimeoutSeconds:  cfg.TimeoutSeconds,
		MaxRetries:      cfg.MaxRetries,
		IsDefault:       cfg.IsDefault,
		Status:          cfg.Status,
		ThinkingType:    cfg.EffectiveThinkingType(),
		ReasoningEffort: cfg.EffectiveReasoningEffort(),
		Models:          items,
		CreatedAt:       formatTime(cfg.CreatedAt),
		UpdatedAt:       formatTime(cfg.UpdatedAt),
	}
	if cfg.DefaultModelID > 0 {
		dto.DefaultModelID = strconv.FormatInt(cfg.DefaultModelID, 10)
	}
	if cfg.CreatedBy != nil {
		dto.CreatedBy = strconv.FormatInt(*cfg.CreatedBy, 10)
	}
	return dto
}

type modelDTO struct {
	ID             string           `json:"id"`
	ConfigID       string           `json:"config_id"`
	Model          string           `json:"model"`
	DisplayName    string           `json:"display_name"`
	ContextWindow  int              `json:"context_window"`
	MaxOutput      int              `json:"max_output"`
	MaxInput       int64            `json:"max_input"`
	SupportsTools  bool             `json:"supports_tools"`
	SupportsVision bool             `json:"supports_vision"`
	Capabilities   llm.Capabilities `json:"capabilities"`
	Deprecated     bool             `json:"deprecated"`
	Source         string           `json:"source"`
	WindowSource   string           `json:"window_source"`
	OutputSource   string           `json:"output_source"`
	Status         string           `json:"status"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
}

// publicModelDTO is the login-user model catalog item (OpenAPI
// PublicModelView). It deliberately omits api_key/base_url/headers and the
// internal catalog metadata: only what a model picker needs.
type publicModelDTO struct {
	ID            string           `json:"id"`
	ConfigID      string           `json:"config_id"`
	ConfigName    string           `json:"config_name"`
	Model         string           `json:"model"`
	DisplayName   string           `json:"display_name"`
	ContextWindow int              `json:"context_window"`
	MaxOutput     int              `json:"max_output"`
	MaxInput      int64            `json:"max_input"`
	WindowSource  string           `json:"window_source"`
	OutputSource  string           `json:"output_source"`
	Capabilities  llm.Capabilities `json:"capabilities"`
	Default       bool             `json:"default"`
	Deprecated    bool             `json:"deprecated"`
}

// toPublicModelDTO maps one selectable model of an active configuration. Default
// is true only for the configuration's explicit default_model_id; the picker
// must never fall back to models[0] (D24).
func toPublicModelDTO(model llm.Model, cfg llm.Config) publicModelDTO {
	capabilities := model.EffectiveCapabilities()
	return publicModelDTO{
		ID:            strconv.FormatInt(model.ID, 10),
		ConfigID:      strconv.FormatInt(model.ConfigID, 10),
		ConfigName:    cfg.Name,
		Model:         model.Model,
		DisplayName:   model.DisplayName,
		ContextWindow: capabilities.ContextWindow,
		MaxOutput:     model.MaxOutput,
		MaxInput:      llmapp.MaxInput(model),
		WindowSource:  model.EffectiveWindowSource(),
		OutputSource:  model.EffectiveOutputSource(),
		Capabilities:  capabilities,
		Default:       cfg.DefaultModelID == model.ID,
		Deprecated:    model.Deprecated,
	}
}

func toModelDTO(model llm.Model) modelDTO {
	return modelDTO{
		ID:             strconv.FormatInt(model.ID, 10),
		ConfigID:       strconv.FormatInt(model.ConfigID, 10),
		Model:          model.Model,
		DisplayName:    model.DisplayName,
		ContextWindow:  model.ContextWindow,
		MaxOutput:      model.MaxOutput,
		MaxInput:       llmapp.MaxInput(model),
		SupportsTools:  model.SupportsTools,
		SupportsVision: model.SupportsVision,
		Capabilities:   model.EffectiveCapabilities(),
		Deprecated:     model.Deprecated,
		Source:         model.Source,
		WindowSource:   model.EffectiveWindowSource(),
		OutputSource:   model.EffectiveOutputSource(),
		Status:         model.Status,
		CreatedAt:      formatTime(model.CreatedAt),
		UpdatedAt:      formatTime(model.UpdatedAt),
	}
}

type remoteModelDTO struct {
	ID             string `json:"id"`
	ContextWindow  int    `json:"context_window"`
	MaxOutput      int    `json:"max_output"`
	SupportsTools  bool   `json:"supports_tools"`
	SupportsVision bool   `json:"supports_vision"`
}

type testResultDTO struct {
	OK        bool             `json:"ok"`
	LatencyMS int64            `json:"latency_ms"`
	Models    []remoteModelDTO `json:"models"`
	Error     string           `json:"error,omitempty"`
}

func toTestResultDTO(result llmapp.TestResult) testResultDTO {
	models := make([]remoteModelDTO, 0, len(result.Models))
	for _, model := range result.Models {
		models = append(models, remoteModelDTO{
			ID:             model.ID,
			ContextWindow:  model.ContextWindow,
			MaxOutput:      model.MaxOutput,
			SupportsTools:  model.SupportsTools,
			SupportsVision: model.SupportsVision,
		})
	}
	return testResultDTO{
		OK:        result.OK,
		LatencyMS: result.LatencyMS,
		Models:    models,
		Error:     result.Error,
	}
}

// addModelRequest accepts the full 0.2.1 capability set. Gateways whose
// /models response omits capabilities can be described manually (spec §7.3).
type addModelRequest struct {
	Model               string `json:"model"`
	ContextWindow       int    `json:"context_window"`
	MaxOutput           int    `json:"max_output"`
	SupportsTools       bool   `json:"supports_tools"`
	SupportsVision      bool   `json:"supports_vision"`
	SupportsReasoning   bool   `json:"supports_reasoning"`
	SupportsPromptCache bool   `json:"supports_prompt_cache"`
}

func (r addModelRequest) input() llmapp.ModelInput {
	return llmapp.ModelInput{
		Model:               r.Model,
		ContextWindow:       r.ContextWindow,
		MaxOutput:           r.MaxOutput,
		SupportsTools:       r.SupportsTools,
		SupportsVision:      r.SupportsVision,
		SupportsReasoning:   r.SupportsReasoning,
		SupportsPromptCache: r.SupportsPromptCache,
	}
}

// updateModelRequest is the partial model patch. Pointer fields keep "omitted"
// distinct from an explicit false/zero so the admin editor can flip a single
// checkbox without resetting the others. context_window/max_output are the
// per-model numeric parameters (raw tokens, A §4.3).
type updateModelRequest struct {
	ContextWindow       *int   `json:"context_window"`
	MaxOutput           *int64 `json:"max_output"`
	SupportsTools       *bool  `json:"supports_tools"`
	SupportsVision      *bool  `json:"supports_vision"`
	SupportsReasoning   *bool  `json:"supports_reasoning"`
	SupportsPromptCache *bool  `json:"supports_prompt_cache"`
	Deprecated          *bool  `json:"deprecated"`
}

func (r updateModelRequest) patch() llmapp.ModelPatch {
	return llmapp.ModelPatch{
		ContextWindow:       r.ContextWindow,
		MaxOutput:           r.MaxOutput,
		SupportsTools:       r.SupportsTools,
		SupportsVision:      r.SupportsVision,
		SupportsReasoning:   r.SupportsReasoning,
		SupportsPromptCache: r.SupportsPromptCache,
		Deprecated:          r.Deprecated,
	}
}

type deletedDTO struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
