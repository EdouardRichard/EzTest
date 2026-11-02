package llmapp

import (
	"context"
	"errors"
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
			Source:         llm.SourceCatalog,
			Status:         llm.ModelStatusActive,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}

	upserted, err := s.repo.UpsertModels(ctx, id, models, now)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	return upserted, nil
}

// AddModel inserts a manually configured model.
func (s *Service) AddModel(ctx context.Context, configID int64, model string, contextWindow, maxOutput int, tools, vision bool, actorID int64) (llm.Model, error) {
	if _, err := s.repo.GetConfig(ctx, configID); err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}
	name := strings.TrimSpace(model)
	if err := validation.String("model", name, 1, llm.MaxModelNameLength); err != nil {
		return llm.Model{}, err
	}
	if contextWindow < 0 || contextWindow > llm.MaxContextWindow {
		return llm.Model{}, platform.Invalid("context_window", "must be between 0 and "+strconv.Itoa(llm.MaxContextWindow))
	}
	if maxOutput < 0 || maxOutput > llm.MaxMaxOutput {
		return llm.Model{}, platform.Invalid("max_output", "must be between 0 and "+strconv.Itoa(llm.MaxMaxOutput))
	}

	now := s.clock.Now()
	row := llm.Model{
		ID:             s.ids.Next(),
		ConfigID:       configID,
		Model:          name,
		DisplayName:    name,
		ContextWindow:  contextWindow,
		MaxOutput:      maxOutput,
		SupportsTools:  tools,
		SupportsVision: vision,
		Source:         llm.SourceManual,
		Status:         llm.ModelStatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	stored, err := s.repo.InsertModel(ctx, row)
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
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}

	if err := cfg.Validate(); err != nil {
		return llm.Config{}, err
	}
	return cfg, nil
}

// recordConfigChange emits the audit entry and the system event for a config
// mutation. Audit/event delivery is best effort and never fails the business
// operation. None of the recorded fields contain the API key.
func (s *Service) recordConfigChange(ctx context.Context, verb string, cfg llm.Config, actorID int64) {
	action := "llm.config_" + verb
	targetID := id.Format(cfg.ID)
	requestID := requestid.From(ctx)
	actor := actorID

	metadata := map[string]any{
		"name":       cfg.Name,
		"provider":   cfg.Provider,
		"protocol":   cfg.Protocol,
		"base_url":   cfg.BaseURL,
		"is_default": cfg.IsDefault,
		"status":     cfg.Status,
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
			"id":         targetID,
			"name":       cfg.Name,
			"provider":   cfg.Provider,
			"protocol":   cfg.Protocol,
			"base_url":   cfg.BaseURL,
			"is_default": cfg.IsDefault,
			"status":     cfg.Status,
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
