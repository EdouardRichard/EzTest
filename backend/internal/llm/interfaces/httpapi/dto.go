package llmapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// configRequest is the create/update payload. Pointer fields distinguish
// "omitted" (keep/default) from an explicit zero value. api_key is write-only:
// it is accepted but never echoed back.
type configRequest struct {
	Name           *string            `json:"name"`
	Provider       *string            `json:"provider"`
	Protocol       *string            `json:"protocol"`
	BaseURL        *string            `json:"base_url"`
	APIKey         *string            `json:"api_key"`
	Headers        *map[string]string `json:"headers"`
	TimeoutSeconds *int               `json:"timeout_seconds"`
	MaxRetries     *int               `json:"max_retries"`
	IsDefault      *bool              `json:"is_default"`
	Status         *string            `json:"status"`
}

func (r configRequest) input() llmapp.ConfigInput {
	return llmapp.ConfigInput{
		Name:           r.Name,
		Provider:       r.Provider,
		Protocol:       r.Protocol,
		BaseURL:        r.BaseURL,
		APIKey:         r.APIKey,
		Headers:        r.Headers,
		TimeoutSeconds: r.TimeoutSeconds,
		MaxRetries:     r.MaxRetries,
		IsDefault:      r.IsDefault,
		Status:         r.Status,
	}
}

type configDTO struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Provider       string            `json:"provider"`
	Protocol       string            `json:"protocol"`
	BaseURL        string            `json:"base_url"`
	APIKeySet      bool              `json:"api_key_set"`
	Headers        map[string]string `json:"headers"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxRetries     int               `json:"max_retries"`
	IsDefault      bool              `json:"is_default"`
	Status         string            `json:"status"`
	CreatedBy      string            `json:"created_by,omitempty"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
}

func toConfigDTO(cfg llm.Config) configDTO {
	headers := cfg.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	dto := configDTO{
		ID:             strconv.FormatInt(cfg.ID, 10),
		Name:           cfg.Name,
		Provider:       cfg.Provider,
		Protocol:       cfg.Protocol,
		BaseURL:        cfg.BaseURL,
		APIKeySet:      cfg.APIKeySet(),
		Headers:        headers,
		TimeoutSeconds: cfg.TimeoutSeconds,
		MaxRetries:     cfg.MaxRetries,
		IsDefault:      cfg.IsDefault,
		Status:         cfg.Status,
		CreatedAt:      formatTime(cfg.CreatedAt),
		UpdatedAt:      formatTime(cfg.UpdatedAt),
	}
	if cfg.CreatedBy != nil {
		dto.CreatedBy = strconv.FormatInt(*cfg.CreatedBy, 10)
	}
	return dto
}

type modelDTO struct {
	ID             string `json:"id"`
	ConfigID       string `json:"config_id"`
	Model          string `json:"model"`
	DisplayName    string `json:"display_name"`
	ContextWindow  int    `json:"context_window"`
	MaxOutput      int    `json:"max_output"`
	SupportsTools  bool   `json:"supports_tools"`
	SupportsVision bool   `json:"supports_vision"`
	Source         string `json:"source"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func toModelDTO(model llm.Model) modelDTO {
	return modelDTO{
		ID:             strconv.FormatInt(model.ID, 10),
		ConfigID:       strconv.FormatInt(model.ConfigID, 10),
		Model:          model.Model,
		DisplayName:    model.DisplayName,
		ContextWindow:  model.ContextWindow,
		MaxOutput:      model.MaxOutput,
		SupportsTools:  model.SupportsTools,
		SupportsVision: model.SupportsVision,
		Source:         model.Source,
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

type addModelRequest struct {
	Model          string `json:"model"`
	ContextWindow  int    `json:"context_window"`
	MaxOutput      int    `json:"max_output"`
	SupportsTools  bool   `json:"supports_tools"`
	SupportsVision bool   `json:"supports_vision"`
}

type deletedDTO struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
