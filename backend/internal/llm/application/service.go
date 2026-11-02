package llmapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Service owns LLM configuration and model-catalog use cases.
type Service struct {
	repo     llm.Repository
	client   Client
	chat     ChatClient
	settings SettingsReader
	ids      id.Generator
	clock    clock.Clock
	audit    auditapp.Recorder
	events   eventingapp.Publisher
}

// New wires the LLM application service. audit and events may be nil in tests.
// Optional chat-gateway dependencies are supplied through options so the frozen
// P0 constructor and its behavior stay compatible.
func New(
	repo llm.Repository,
	client Client,
	ids id.Generator,
	clk clock.Clock,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
	opts ...Option,
) *Service {
	service := &Service{repo: repo, client: client, ids: ids, clock: clk, audit: audit, events: events}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// ListConfigs returns every configuration ordered by ID.
func (s *Service) ListConfigs(ctx context.Context) ([]llm.Config, error) {
	list, err := s.repo.ListConfigs(ctx)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	return list, nil
}

// GetConfig returns one configuration or a 404.
func (s *Service) GetConfig(ctx context.Context, id int64) (llm.Config, error) {
	cfg, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return llm.Config{}, mapRepositoryError(err)
	}
	return cfg, nil
}

// CreateConfig validates and persists a configuration, then records audit and
// an event.
func (s *Service) CreateConfig(ctx context.Context, cmd ConfigInput, actorID int64) (llm.Config, error) {
	cfg, err := buildConfig(llm.Config{}, cmd, true)
	if err != nil {
		return llm.Config{}, err
	}
	now := s.clock.Now()
	cfg.ID = s.ids.Next()
	actor := actorID
	cfg.CreatedBy = &actor
	cfg.CreatedAt = now
	cfg.UpdatedAt = now
	if err := s.validateDefaultModel(ctx, cfg); err != nil {
		return llm.Config{}, err
	}

	created, err := s.repo.CreateConfig(ctx, cfg)
	if err != nil {
		return llm.Config{}, mapRepositoryError(err)
	}
	s.recordConfigChange(ctx, "created", created, actorID)
	return created, nil
}

// UpdateConfig applies a partial update and records audit and an event.
func (s *Service) UpdateConfig(ctx context.Context, id int64, cmd ConfigInput, actorID int64) (llm.Config, error) {
	existing, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return llm.Config{}, mapRepositoryError(err)
	}
	cfg, err := buildConfig(existing, cmd, false)
	if err != nil {
		return llm.Config{}, err
	}
	cfg.ID = existing.ID
	cfg.CreatedBy = existing.CreatedBy
	cfg.CreatedAt = existing.CreatedAt
	cfg.UpdatedAt = s.clock.Now()
	if err := s.validateDefaultModel(ctx, cfg); err != nil {
		return llm.Config{}, err
	}

	updated, err := s.repo.UpdateConfig(ctx, cfg)
	if err != nil {
		return llm.Config{}, mapRepositoryError(err)
	}
	s.recordConfigChange(ctx, "updated", updated, actorID)
	return updated, nil
}

// DeleteConfig removes a configuration (cascading its models).
func (s *Service) DeleteConfig(ctx context.Context, id int64, actorID int64) error {
	cfg, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return mapRepositoryError(err)
	}
	if err := s.repo.DeleteConfig(ctx, id); err != nil {
		return mapRepositoryError(err)
	}
	s.recordConfigChange(ctx, "deleted", cfg, actorID)
	return nil
}

// TestConfig probes the upstream described by a stored configuration.
func (s *Service) TestConfig(ctx context.Context, id int64) (TestResult, error) {
	cfg, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return TestResult{}, mapRepositoryError(err)
	}
	result, err := s.client.Test(ctx, cfg)
	if err != nil {
		return TestResult{}, upstreamError(err, cfg.APIKey, "llm connectivity test failed")
	}
	result.Error = redact(result.Error, cfg.APIKey)
	return result, nil
}

// RefreshModels pulls the upstream model catalog and upserts it.
func (s *Service) RefreshModels(ctx context.Context, id int64, actorID int64) ([]llm.Model, error) {
	cfg, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	remote, err := s.client.ListModels(ctx, cfg)
	if err != nil {
		return nil, upstreamError(err, cfg.APIKey, "failed to refresh llm models")
	}

	now := s.clock.Now()
	models := make([]llm.Model, 0, len(remote))
	for _, item := range remote {
		name := strings.TrimSpace(item.ID)
		if name == "" {
			continue
		}
		models = append(models, llm.Model{
			ID:             s.ids.Next(),
			ConfigID:       id,
			Model:          name,
			DisplayName:    name,
			ContextWindow:  item.ContextWindow,
			MaxOutput:      item.MaxOutput,
			SupportsTools:  item.SupportsTools,
			SupportsVision: item.SupportsVision,
			Capabilities: llm.Capabilities{
				SupportsTools:       item.SupportsTools,
				SupportsVision:      item.SupportsVision,
				SupportsReasoning:   item.SupportsReasoning,
				SupportsPromptCache: item.SupportsPromptCache,
				ContextWindow:       item.ContextWindow,
			},
			Source:    llm.SourceCatalog,
			Status:    llm.ModelStatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	// A provider that answers 200 with an empty data array is not an
	// authoritative empty catalog: upserting it would deprecate every stored
	// model. Refuse the refresh and leave the existing catalog untouched.
	if len(models) == 0 {
		return nil, upstreamError(
			errors.New("llm provider returned an empty model catalog; existing models were left unchanged"),
			cfg.APIKey, "failed to refresh llm models")
	}

	upserted, err := s.repo.UpsertModels(ctx, id, models, now)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	return upserted, nil
}

// AddModel inserts a manually configured model. Every 0.2.1 capability is
// accepted because a gateway's /models response may not describe them.
func (s *Service) AddModel(ctx context.Context, configID int64, input ModelInput, actorID int64) (llm.Model, error) {
	if _, err := s.repo.GetConfig(ctx, configID); err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}
	name := strings.TrimSpace(input.Model)
	if err := validation.String("model", name, 1, llm.MaxModelNameLength); err != nil {
		return llm.Model{}, err
	}
	if input.ContextWindow < 0 || input.ContextWindow > llm.MaxContextWindow {
		return llm.Model{}, platform.Invalid("context_window", "must be between 0 and "+strconv.Itoa(llm.MaxContextWindow))
	}
	if input.MaxOutput < 0 || input.MaxOutput > llm.MaxMaxOutput {
		return llm.Model{}, platform.Invalid("max_output", "must be between 0 and "+strconv.Itoa(llm.MaxMaxOutput))
	}

	now := s.clock.Now()
	row := llm.Model{
		ID:             s.ids.Next(),
		ConfigID:       configID,
		Model:          name,
		DisplayName:    name,
		ContextWindow:  input.ContextWindow,
		MaxOutput:      input.MaxOutput,
		SupportsTools:  input.SupportsTools,
		SupportsVision: input.SupportsVision,
		Capabilities: llm.Capabilities{
			SupportsTools:       input.SupportsTools,
			SupportsVision:      input.SupportsVision,
			SupportsReasoning:   input.SupportsReasoning,
			SupportsPromptCache: input.SupportsPromptCache,
			ContextWindow:       input.ContextWindow,
		},
		Source:    llm.SourceManual,
		Status:    llm.ModelStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	stored, err := s.repo.InsertModel(ctx, row)
	if err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}
	return stored, nil
}

// UpdateModel patches one model row scoped to its configuration. It backs the
// admin capability editor so gateways whose /models response omits capabilities
// can be corrected without deleting and re-adding the model. Unknown rows (and
// rows owned by another configuration) are a typed 404.
func (s *Service) UpdateModel(ctx context.Context, configID, modelID int64, patch ModelPatch, actorID int64) (llm.Model, error) {
	models, err := s.repo.ListModelsByConfig(ctx, configID)
	if err != nil {
		return llm.Model{}, err
	}
	var target *llm.Model
	for i := range models {
		if models[i].ID == modelID {
			target = &models[i]
			break
		}
	}
	if target == nil {
		return llm.Model{}, platform.NotFound("llm model")
	}

	if patch.ContextWindow != nil {
		if *patch.ContextWindow < 0 || *patch.ContextWindow > llm.MaxContextWindow {
			return llm.Model{}, platform.Invalid("context_window", "must be between 0 and "+strconv.Itoa(llm.MaxContextWindow))
		}
		target.ContextWindow = *patch.ContextWindow
		target.Capabilities.ContextWindow = *patch.ContextWindow
	}
	if patch.SupportsTools != nil {
		target.SupportsTools = *patch.SupportsTools
	}
	if patch.SupportsVision != nil {
		target.SupportsVision = *patch.SupportsVision
	}
	if patch.Deprecated != nil {
		target.Deprecated = *patch.Deprecated
	}

	// Keep the legacy flat columns and the capability object in sync: the
	// capability set is authoritative for reasoning/prompt-cache, while tools,
	// vision and the context window mirror into the P0 columns.
	capabilities := target.EffectiveCapabilities()
	capabilities.SupportsTools = target.SupportsTools
	capabilities.SupportsVision = target.SupportsVision
	if patch.SupportsReasoning != nil {
		capabilities.SupportsReasoning = *patch.SupportsReasoning
	}
	if patch.SupportsPromptCache != nil {
		capabilities.SupportsPromptCache = *patch.SupportsPromptCache
	}
	if patch.ContextWindow != nil {
		capabilities.ContextWindow = *patch.ContextWindow
	}
	target.Capabilities = capabilities
	target.UpdatedAt = s.clock.Now()

	stored, err := s.repo.UpdateModel(ctx, *target)
	if err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}
	return stored, nil
}

// DeleteModel removes one catalog row.
func (s *Service) DeleteModel(ctx context.Context, modelID int64, actorID int64) error {
	if err := s.repo.DeleteModel(ctx, modelID); err != nil {
		return mapRepositoryError(err)
	}
	return nil
}

// ListModels returns the whole model catalog ordered by ID.
func (s *Service) ListModels(ctx context.Context) ([]llm.Model, error) {
	list, err := s.repo.ListModels(ctx)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	return list, nil
}

// ResolveSelectableModel resolves a conversation-level model override to a
// selectable catalog row of the default LLM configuration. An empty override
// resolves the configuration's explicit default_model_id (models[0] is never
// used, D24). Deprecated/disabled models and models outside the configuration
// are rejected with a non-retryable typed error so a conversation can never pin
// a model that the runtime would refuse to dispatch.
//
// This is deliberately narrower than ResolveModel: selecting a model for a
// conversation must not depend on the chat gateway being configured, and the
// vision/tool gates are applied when the draft/message is dispatched.
func (s *Service) ResolveSelectableModel(ctx context.Context, modelOverride string) (llm.Model, error) {
	cfg, err := s.selectConfig(ctx, 0)
	if err != nil {
		return llm.Model{}, err
	}
	models, err := s.repo.ListModelsByConfig(ctx, cfg.ID)
	if err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}

	name := strings.TrimSpace(modelOverride)
	if name == "" {
		if cfg.DefaultModelID <= 0 {
			return llm.Model{}, &platform.Error{
				Status:  400,
				Code:    CodeLLMConfigInvalid,
				Message: "llm config has no default model; set default_model_id before selecting a model (models[0] is never used)",
				Details: map[string]any{"field": "default_model_id"},
			}
		}
		for i := range models {
			if models[i].ID != cfg.DefaultModelID {
				continue
			}
			if !models[i].Selectable() {
				return llm.Model{}, llmConfigError(fmt.Sprintf(
					"default model %q is deprecated or disabled; set default_model_id to an active model", models[i].Model))
			}
			return models[i], nil
		}
		return llm.Model{}, llmConfigError(fmt.Sprintf(
			"default model %d is not in the catalog of llm config %q; refresh the model catalog or set default_model_id",
			cfg.DefaultModelID, cfg.Name))
	}

	for i := range models {
		if models[i].Model != name {
			continue
		}
		if !models[i].Selectable() {
			return llm.Model{}, platform.Invalid("model_override",
				"must name an active model that is not deprecated")
		}
		return models[i], nil
	}
	return llm.Model{}, platform.Invalid("model_override",
		"must name a model of the default llm configuration")
}

// buildConfig merges a partial input into base. On create the P0 defaults are
// applied only for fields the caller omitted, so an explicit zero (for example
// timeout_seconds=0) is rejected by validation instead of being defaulted.
func buildConfig(base llm.Config, input ConfigInput, create bool) (llm.Config, error) {
	cfg := base

	if input.Name != nil {
		cfg.Name = strings.TrimSpace(*input.Name)
	}
	if input.Provider != nil {
		cfg.Provider = strings.TrimSpace(*input.Provider)
	}
	if input.Protocol != nil {
		cfg.Protocol = strings.TrimSpace(*input.Protocol)
	}
	if input.BaseURL != nil {
		cfg.BaseURL = strings.TrimSpace(*input.BaseURL)
	}
	if input.APIKey != nil {
		cfg.APIKey = *input.APIKey
	}
	if input.Headers != nil {
		cfg.Headers = llm.NormalizeHeaders(*input.Headers)
	}
	if input.TimeoutSeconds != nil {
		cfg.TimeoutSeconds = *input.TimeoutSeconds
	}
	if input.MaxRetries != nil {
		cfg.MaxRetries = *input.MaxRetries
	}
	if input.IsDefault != nil {
		cfg.IsDefault = *input.IsDefault
	}
	if input.Status != nil {
		cfg.Status = strings.TrimSpace(*input.Status)
	}
	if input.ThinkingType != nil {
		cfg.ThinkingType = strings.TrimSpace(*input.ThinkingType)
	}
	if input.ReasoningEffort != nil {
		cfg.ReasoningEffort = strings.TrimSpace(*input.ReasoningEffort)
	}
	if input.DefaultModelID != nil {
		cfg.DefaultModelID = *input.DefaultModelID
	}

	if create {
		if input.Provider == nil {
			cfg.Provider = llm.DefaultProvider
		}
		if input.Protocol == nil {
			cfg.Protocol = llm.DefaultProtocol
		}
		if input.TimeoutSeconds == nil {
			cfg.TimeoutSeconds = llm.DefaultTimeoutSeconds
		}
		if input.MaxRetries == nil {
			cfg.MaxRetries = llm.DefaultMaxRetries
		}
		if input.Status == nil {
			cfg.Status = llm.DefaultStatus
		}
		if input.ThinkingType == nil {
			cfg.ThinkingType = llm.DefaultThinkingType
		}
		if input.ReasoningEffort == nil {
			cfg.ReasoningEffort = llm.DefaultReasoningEffort
		}
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}

	if err := cfg.Validate(); err != nil {
		return llm.Config{}, err
	}
	return cfg, nil
}

// validateDefaultModel enforces that a persisted configuration is
// dispatch-ready: a configured default_model_id must reference a selectable
// model of the same configuration, and a configuration that owns at least one
// selectable model must name a default (resolution never falls back to
// models[0]). A config may be created before its catalog is refreshed (Task 17
// carve-out) and a catalog whose models are all deprecated has nothing to
// select, so neither case requires a default.
func (s *Service) validateDefaultModel(ctx context.Context, cfg llm.Config) error {
	models, err := s.repo.ListModelsByConfig(ctx, cfg.ID)
	if err != nil {
		return err
	}
	if cfg.DefaultModelID <= 0 {
		for _, model := range models {
			if model.Selectable() {
				return defaultModelRequiredError()
			}
		}
		return nil
	}
	for _, model := range models {
		if model.ID != cfg.DefaultModelID {
			continue
		}
		if !model.Selectable() {
			return platform.Invalid("default_model_id", "must reference an active model that is not deprecated")
		}
		return nil
	}
	return platform.Invalid("default_model_id", "must reference a model of this LLM configuration")
}

// defaultModelRequiredError is the typed configuration error the admin UI maps
// back onto the default-model field: a model is selectable but no default is
// configured, which would fail every run after the config was saved.
func defaultModelRequiredError() error {
	return &platform.Error{
		Status:  http.StatusBadRequest,
		Code:    CodeLLMConfigInvalid,
		Message: "default_model_id is required when the configuration has selectable models",
		Details: map[string]any{"field": "default_model_id"},
	}
}

// recordConfigChange emits the audit entry and the system event for a config
// mutation. Audit/event delivery is best effort and never fails the business
// operation. None of the recorded fields contain the API key.
func (s *Service) recordConfigChange(ctx context.Context, verb string, cfg llm.Config, actorID int64) {
	action := "llm.config_" + verb
	targetID := id.Format(cfg.ID)
	requestID := requestid.From(ctx)
	actor := actorID
	defaultModelID := ""
	if cfg.DefaultModelID > 0 {
		defaultModelID = id.Format(cfg.DefaultModelID)
	}

	metadata := map[string]any{
		"name":             cfg.Name,
		"provider":         cfg.Provider,
		"protocol":         cfg.Protocol,
		"base_url":         cfg.BaseURL,
		"is_default":       cfg.IsDefault,
		"status":           cfg.Status,
		"thinking_type":    cfg.EffectiveThinkingType(),
		"reasoning_effort": cfg.EffectiveReasoningEffort(),
		"default_model_id": defaultModelID,
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    &actor,
			Action:     action,
			TargetKind: "llm_config",
			TargetID:   targetID,
			RequestID:  requestID,
			Metadata:   metadata,
		})
	}
	if s.events != nil {
		payload := map[string]any{
			"id":               targetID,
			"name":             cfg.Name,
			"provider":         cfg.Provider,
			"protocol":         cfg.Protocol,
			"base_url":         cfg.BaseURL,
			"is_default":       cfg.IsDefault,
			"status":           cfg.Status,
			"thinking_type":    cfg.EffectiveThinkingType(),
			"reasoning_effort": cfg.EffectiveReasoningEffort(),
			"default_model_id": defaultModelID,
		}
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   "system",
			Type:      action,
			Scope:     map[string]any{},
			Payload:   payload,
			ActorID:   &actor,
			RequestID: requestID,
		})
	}
}

// mapRepositoryError translates domain sentinels into the shared HTTP errors.
func mapRepositoryError(err error) error {
	switch {
	case errors.Is(err, llm.ErrConfigNotFound):
		return platform.NotFound("llm config")
	case errors.Is(err, llm.ErrNameTaken):
		return platform.Conflict("llm config name already exists")
	case errors.Is(err, llm.ErrModelNotFound):
		return platform.NotFound("llm model")
	case errors.Is(err, llm.ErrModelTaken):
		return platform.Conflict("llm model already exists for this config")
	default:
		return err
	}
}

// upstreamError keeps connectivity failures as retryable 503s and guarantees
// the configured API key never appears in the message.
func upstreamError(err error, secret, fallback string) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		apiErr.Message = redact(apiErr.Message, secret)
		return apiErr
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = fallback
	}
	return platform.Unavailable(redact(message, secret))
}

// redact removes the configured secret from a message if it somehow leaked.
func redact(message, secret string) string {
	if secret == "" || message == "" {
		return message
	}
	if !strings.Contains(message, secret) {
		return message
	}
	return strings.ReplaceAll(message, secret, "[redacted]")
}
