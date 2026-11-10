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

// RefreshModels pulls the upstream model catalog and merges it into the stored
// catalog. For an existing model only parameters whose field source is catalog
// (or whose stored value is 0/unknown) are updated: an administrator's manual
// window/output survives every refresh (A §4.4/R-A1-3). The same field-level
// rule now applies to the four boolean capabilities: a manually corrected
// capability keeps its value and provenance while catalog-sourced capabilities
// follow the upstream (S-02). The latest upstream values are still persisted
// inside capabilities so reset-params can restore them, and a successful
// refresh emits llm.config_refreshed with the merge counts (S-05).
func (s *Service) RefreshModels(ctx context.Context, id int64, actorID int64) ([]llm.Model, error) {
	cfg, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	remote, err := s.client.ListModels(ctx, cfg)
	if err != nil {
		return nil, upstreamError(err, cfg.APIKey, "failed to refresh llm models")
	}
	existing, err := s.repo.ListModelsByConfig(ctx, id)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	byName := make(map[string]llm.Model, len(existing))
	for _, model := range existing {
		byName[model.Model] = model
	}

	now := s.clock.Now()
	models := make([]llm.Model, 0, len(remote))
	for _, item := range remote {
		name := strings.TrimSpace(item.ID)
		if name == "" {
			continue
		}
		window := item.ContextWindow
		output := item.MaxOutput
		windowSource := llm.SourceCatalog
		outputSource := llm.SourceCatalog
		source := llm.SourceCatalog
		var previous *llm.Model
		if stored, ok := byName[name]; ok {
			previous = &stored
			// Manual numeric values are never overwritten. A 0 stored value
			// means unknown/auto and may be filled from the upstream even when
			// the source marker says manual.
			windowSource = stored.EffectiveWindowSource()
			outputSource = stored.EffectiveOutputSource()
			if windowSource != llm.SourceCatalog && stored.ContextWindow != 0 {
				window = stored.ContextWindow
			}
			if outputSource != llm.SourceCatalog && stored.MaxOutput != 0 {
				output = stored.MaxOutput
			}
			if stored.Source != "" {
				source = stored.Source
			}
		}
		// Capability booleans merge per field: an administrator's manual value
		// survives the refresh, a catalog-sourced one follows the upstream
		// (S-02). The numeric parameters keep their own existing merge above.
		capabilities := mergeRefreshedCapabilities(previous, llm.Capabilities{
			SupportsTools:       item.SupportsTools,
			SupportsVision:      item.SupportsVision,
			SupportsReasoning:   item.SupportsReasoning,
			SupportsPromptCache: item.SupportsPromptCache,
		})
		// Legacy mirrors stay in sync with the effective values, while the
		// catalog_* keys keep the raw upstream values for reset.
		capabilities.ContextWindow = window
		capabilities.CatalogContextWindow = item.ContextWindow
		capabilities.CatalogMaxOutput = item.MaxOutput
		models = append(models, llm.Model{
			ID:             s.ids.Next(),
			ConfigID:       id,
			Model:          name,
			DisplayName:    name,
			ContextWindow:  window,
			MaxOutput:      output,
			SupportsTools:  capabilities.SupportsTools,
			SupportsVision: capabilities.SupportsVision,
			Capabilities:   capabilities,
			Source:         source,
			WindowSource:   windowSource,
			OutputSource:   outputSource,
			Status:         llm.ModelStatusActive,
			CreatedAt:      now,
			UpdatedAt:      now,
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
	// R-E2-2: a catalog refresh must not make the default-model vision
	// invariant reversible by silently downgrading the configured default
	// model. Reject the refresh before the upsert so no capability change is
	// persisted; the stored catalog (and the default's vision support) stays
	// intact and the admin can choose another vision-capable default first.
	if err := validateRefreshedDefaultModel(cfg, existing, models); err != nil {
		return nil, err
	}

	upserted, err := s.repo.UpsertModels(ctx, id, models, now)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	s.recordConfigRefreshed(ctx, cfg, existing, models, len(upserted), actorID)
	for i := range upserted {
		upserted[i] = withModelView(upserted[i])
	}
	return upserted, nil
}

// AddModel inserts a manually configured model. Every 0.2.1 capability is
// accepted because a gateway's /models response may not describe them.
func (s *Service) AddModel(ctx context.Context, configID int64, input ModelInput, actorID int64) (llm.Model, error) {
	cfg, err := s.repo.GetConfig(ctx, configID)
	if err != nil {
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
	if err := validateParamPair(int64(input.ContextWindow), int64(input.MaxOutput)); err != nil {
		return llm.Model{}, err
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
			SupportsTools:             input.SupportsTools,
			SupportsVision:            input.SupportsVision,
			SupportsReasoning:         input.SupportsReasoning,
			SupportsPromptCache:       input.SupportsPromptCache,
			SupportsToolsSource:       llm.SourceManual,
			SupportsVisionSource:      llm.SourceManual,
			SupportsReasoningSource:   llm.SourceManual,
			SupportsPromptCacheSource: llm.SourceManual,
			ContextWindow:             input.ContextWindow,
		},
		Source: llm.SourceManual,
		// A manually added model's numeric parameters are administrator input,
		// so an upstream refresh that later reports the same name must keep
		// them unless the value is still 0 (unknown).
		WindowSource: llm.SourceManual,
		OutputSource: llm.SourceManual,
		Status:       llm.ModelStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	stored, err := s.repo.InsertModel(ctx, row)
	if err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}
	capabilities := stored.EffectiveCapabilities()
	s.recordModelAudit(ctx, "llm.model_added", cfg.ID, cfg.Name, stored.ID, actorID, map[string]any{
		"model":              stored.Model,
		"display_name":       stored.DisplayName,
		"source":             stored.Source,
		"context_window":     stored.ContextWindow,
		"max_output":         stored.MaxOutput,
		"window_source":      stored.EffectiveWindowSource(),
		"output_source":      stored.EffectiveOutputSource(),
		"capability_sources": capabilitySources(capabilities),
	})
	return withModelView(stored), nil
}

// UpdateModel patches one model row scoped to its configuration. It backs the
// admin capability editor so gateways whose /models response omits capabilities
// can be corrected without deleting and re-adding the model, and it also
// accepts the context_window/max_output parameters (whose field sources become
// manual on write). Unknown rows (and rows owned by another configuration) are
// a typed 404.
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
	before := *target

	window := int64(target.ContextWindow)
	output := int64(target.MaxOutput)
	if patch.ContextWindow != nil {
		if *patch.ContextWindow < 0 || *patch.ContextWindow > llm.MaxContextWindow {
			return llm.Model{}, platform.Invalid("context_window", "must be between 0 and "+strconv.Itoa(llm.MaxContextWindow))
		}
		window = int64(*patch.ContextWindow)
	}
	if patch.MaxOutput != nil {
		if *patch.MaxOutput < 0 || *patch.MaxOutput > llm.MaxMaxOutput {
			return llm.Model{}, platform.Invalid("max_output", "must be between 0 and "+strconv.Itoa(llm.MaxMaxOutput))
		}
		output = *patch.MaxOutput
	}
	// Validate only when a numeric parameter is being written: an unrelated
	// capability edit must not fail because an older row is inconsistent.
	if patch.ContextWindow != nil || patch.MaxOutput != nil {
		if err := validateParamPair(window, output); err != nil {
			return llm.Model{}, err
		}
	}
	if patch.ContextWindow != nil {
		target.ContextWindow = int(window)
		target.WindowSource = llm.SourceManual
	}
	if patch.MaxOutput != nil {
		target.MaxOutput = int(output)
		target.OutputSource = llm.SourceManual
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
	// Only the capability fields the administrator actually supplied become
	// manual; omitted fields keep their previous provenance (S-02).
	if patch.SupportsTools != nil {
		capabilities.SupportsToolsSource = llm.SourceManual
	}
	if patch.SupportsVision != nil {
		capabilities.SupportsVisionSource = llm.SourceManual
	}
	if patch.SupportsReasoning != nil {
		capabilities.SupportsReasoning = *patch.SupportsReasoning
		capabilities.SupportsReasoningSource = llm.SourceManual
	}
	if patch.SupportsPromptCache != nil {
		capabilities.SupportsPromptCache = *patch.SupportsPromptCache
		capabilities.SupportsPromptCacheSource = llm.SourceManual
	}
	if patch.ContextWindow != nil {
		capabilities.ContextWindow = target.ContextWindow
	}
	target.Capabilities = capabilities
	target.UpdatedAt = s.clock.Now()

	// R-E2-2 irreversibility: gating only the default assignment would let a
	// capability PATCH remove supports_vision from the model that is already
	// the configured default. Reject that write (and any update that would
	// leave the default non-vision) before it is persisted. A missing config is
	// tolerated for repository fixtures: such a catalog has no default to
	// protect, and the repository itself scopes the update by (id, config_id).
	if !target.EffectiveCapabilities().SupportsVision {
		cfg, configErr := s.repo.GetConfig(ctx, configID)
		switch {
		case configErr == nil && cfg.DefaultModelID == target.ID:
			return llm.Model{}, invalidVisionDefaultModelError(
				"supports_vision", target.Model, "default_model_vision_downgrade")
		case configErr != nil && !errors.Is(configErr, llm.ErrConfigNotFound):
			return llm.Model{}, mapRepositoryError(configErr)
		}
	}

	stored, err := s.repo.UpdateModel(ctx, *target)
	if err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}
	changed, oldValues, newValues := changedModelFields(before, stored)
	s.recordModelAudit(ctx, "llm.model_updated", configID, s.configName(ctx, configID), stored.ID, actorID, map[string]any{
		"model":              stored.Model,
		"changed_fields":     changed,
		"old_values":         oldValues,
		"new_values":         newValues,
		"capability_sources": capabilitySources(stored.EffectiveCapabilities()),
		"window_source":      stored.EffectiveWindowSource(),
		"output_source":      stored.EffectiveOutputSource(),
	})
	return withModelView(stored), nil
}

// UpdateModelParams applies the per-model numeric-parameter edit from the model
// catalog dialog (A §4.3). Supplied values are persisted and the matching field
// source becomes manual; nil keeps the stored value. The service-level patch
// above remains the transport for the capability editor.
func (s *Service) UpdateModelParams(ctx context.Context, configID, modelID int64, params UpdateModelParams) (llm.Model, error) {
	patch := ModelPatch{MaxOutput: params.MaxOutput}
	if params.ContextWindow != nil {
		if *params.ContextWindow < 0 || *params.ContextWindow > llm.MaxContextWindow {
			return llm.Model{}, platform.Invalid("context_window", "must be between 0 and "+strconv.Itoa(llm.MaxContextWindow))
		}
		window := int(*params.ContextWindow)
		patch.ContextWindow = &window
	}
	return s.UpdateModel(ctx, configID, modelID, patch, 0)
}

// ResetModelParams restores one model's window/output to the most recent
// upstream values recorded during a catalog refresh and flips both field
// sources back to catalog (A §4.3/R-A1-4). A model without an upstream snapshot
// resets to 0 (unknown/auto).
func (s *Service) ResetModelParams(ctx context.Context, configID, modelID, actorID int64) (llm.Model, error) {
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
	before := *target

	capabilities := target.EffectiveCapabilities()
	target.ContextWindow = capabilities.CatalogContextWindow
	target.MaxOutput = capabilities.CatalogMaxOutput
	target.Capabilities = capabilities
	target.Capabilities.ContextWindow = target.ContextWindow
	target.WindowSource = llm.SourceCatalog
	target.OutputSource = llm.SourceCatalog
	target.UpdatedAt = s.clock.Now()

	stored, err := s.repo.UpdateModel(ctx, *target)
	if err != nil {
		return llm.Model{}, mapRepositoryError(err)
	}
	changed, oldValues, newValues := changedModelFields(before, stored)
	s.recordModelAudit(ctx, "llm.model_params_reset", configID, s.configName(ctx, configID), stored.ID, actorID, map[string]any{
		"model":              stored.Model,
		"changed_fields":     changed,
		"old_values":         oldValues,
		"new_values":         newValues,
		"capability_sources": capabilitySources(stored.EffectiveCapabilities()),
		"window_source":      stored.EffectiveWindowSource(),
		"output_source":      stored.EffectiveOutputSource(),
	})
	return withModelView(stored), nil
}

// DeleteModel removes one catalog row. A model that is currently the
// configuration's explicit default_model_id cannot be deleted: the delete would
// bypass default-model validation and silently leave a configuration that can
// never resolve a model (M-07). The administrator must reassign first.
func (s *Service) DeleteModel(ctx context.Context, modelID int64, actorID int64) error {
	models, err := s.repo.ListModels(ctx)
	if err != nil {
		return mapRepositoryError(err)
	}
	var target *llm.Model
	for i := range models {
		if models[i].ID == modelID {
			target = &models[i]
			break
		}
	}
	if target == nil {
		return platform.NotFound("llm model")
	}
	configs, err := s.repo.ListConfigs(ctx)
	if err != nil {
		return mapRepositoryError(err)
	}
	for _, cfg := range configs {
		if cfg.DefaultModelID == target.ID {
			return defaultModelInUseError(cfg, *target)
		}
	}
	if err := s.repo.DeleteModel(ctx, modelID); err != nil {
		return mapRepositoryError(err)
	}
	s.recordModelAudit(ctx, "llm.model_deleted", target.ConfigID, s.configName(ctx, target.ConfigID), target.ID, actorID, map[string]any{
		"model":              target.Model,
		"display_name":       target.DisplayName,
		"source":             target.Source,
		"context_window":     target.ContextWindow,
		"max_output":         target.MaxOutput,
		"capability_sources": capabilitySources(target.EffectiveCapabilities()),
	})
	return nil
}

// ListModels returns the whole model catalog ordered by ID. Every row carries
// the derived max_input view value (A §4.3).
func (s *Service) ListModels(ctx context.Context) ([]llm.Model, error) {
	list, err := s.repo.ListModels(ctx)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	for i := range list {
		list[i] = withModelView(list[i])
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
			return llm.Model{}, withResolveDetails(&platform.Error{
				Status:  400,
				Code:    CodeLLMConfigInvalid,
				Message: "llm config has no default model; set default_model_id before selecting a model (models[0] is never used)",
				Details: map[string]any{"field": "default_model_id"},
			}, cfg)
		}
		for i := range models {
			if models[i].ID != cfg.DefaultModelID {
				continue
			}
			if !models[i].Selectable() {
				return llm.Model{}, withResolveDetails(llmConfigError(fmt.Sprintf(
					"default model %q is deprecated or disabled; set default_model_id to an active model", models[i].Model)), cfg)
			}
			return models[i], nil
		}
		return llm.Model{}, withResolveDetails(llmConfigError(fmt.Sprintf(
			"default model %d is not in the catalog of llm config %q; refresh the model catalog or set default_model_id",
			cfg.DefaultModelID, cfg.Name)), cfg)
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
		// R-E2-2 (spec §4.25): document images are analyzed with the default
		// model, so a model without supports_vision can never be the default.
		// Without this gate the configuration would be persistable but every
		// image-bearing attachment would fail only at analysis time.
		if !model.EffectiveCapabilities().SupportsVision {
			return invalidVisionDefaultModelError("default_model_id", model.Model, "")
		}
		return nil
	}
	return platform.Invalid("default_model_id", "must reference a model of this LLM configuration")
}

// validateRefreshedDefaultModel enforces the default-model vision invariant
// against the merged upstream catalog before a refresh is persisted (R-E2-2).
// The configured default is identified by ID in the stored catalog and matched
// to the refresh entry by model name (the upsert preserves the stored row ID).
// A default model that disappeared from the upstream catalog is handled by the
// repository's deprecation rule, not by this vision guard.
func validateRefreshedDefaultModel(cfg llm.Config, existing, refreshed []llm.Model) error {
	if cfg.DefaultModelID <= 0 {
		return nil
	}
	defaultName := ""
	for _, model := range existing {
		if model.ID == cfg.DefaultModelID {
			defaultName = model.Model
			break
		}
	}
	if defaultName == "" {
		return nil
	}
	var previous *llm.Model
	for i := range existing {
		if existing[i].Model == defaultName {
			previous = &existing[i]
			break
		}
	}
	for _, model := range refreshed {
		if model.Model != defaultName {
			continue
		}
		// Evaluate the merged capability set, not the raw upstream one: a
		// manual supports_vision correction must keep the default valid even
		// when the provider's /models response omits the capability (S-02).
		merged := mergeRefreshedCapabilities(previous, model.EffectiveCapabilities())
		if !merged.SupportsVision {
			return invalidVisionDefaultModelError("default_model_id", model.Model, "upstream_catalog_lost_vision")
		}
		return nil
	}
	return nil
}

// mergeRefreshedCapabilities merges one upstream capability set into the stored
// row: manual-sourced booleans keep the administrator's value, every other
// field follows the upstream and is recorded as catalog. previous is nil for a
// brand-new model.
func mergeRefreshedCapabilities(previous *llm.Model, upstream llm.Capabilities) llm.Capabilities {
	merged := upstream
	merged.SupportsToolsSource = llm.SourceCatalog
	merged.SupportsVisionSource = llm.SourceCatalog
	merged.SupportsReasoningSource = llm.SourceCatalog
	merged.SupportsPromptCacheSource = llm.SourceCatalog
	if previous == nil {
		return merged
	}
	stored := previous.EffectiveCapabilities()
	if stored.SupportsToolsSource == llm.SourceManual {
		merged.SupportsTools = stored.SupportsTools
		merged.SupportsToolsSource = llm.SourceManual
	}
	if stored.SupportsVisionSource == llm.SourceManual {
		merged.SupportsVision = stored.SupportsVision
		merged.SupportsVisionSource = llm.SourceManual
	}
	if stored.SupportsReasoningSource == llm.SourceManual {
		merged.SupportsReasoning = stored.SupportsReasoning
		merged.SupportsReasoningSource = llm.SourceManual
	}
	if stored.SupportsPromptCacheSource == llm.SourceManual {
		merged.SupportsPromptCache = stored.SupportsPromptCache
		merged.SupportsPromptCacheSource = llm.SourceManual
	}
	return merged
}

// invalidVisionDefaultModelError is the typed 400 for a non-vision default
// model. The stable invalid_argument code and the field let the admin UI map it
// back onto the offending control instead of showing a generic failure. reason
// is an optional stable explanation (for example that the upstream catalog lost
// the capability) surfaced in the error details.
func invalidVisionDefaultModelError(field, model, reason string) error {
	apiErr := platform.Invalid(field,
		"the default model must have supports_vision=true; document images and image drafts require vision analysis")
	apiErr.Details["model"] = model
	apiErr.Details["missing_capabilities"] = []string{"vision"}
	if reason != "" {
		apiErr.Details["reason"] = reason
	}
	return apiErr
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

// configName returns the stored configuration name for audit metadata. It is
// best effort: a repository fixture without a configuration row still records
// the config ID, and a failed read never fails the business operation.
func (s *Service) configName(ctx context.Context, configID int64) string {
	cfg, err := s.repo.GetConfig(ctx, configID)
	if err != nil {
		return ""
	}
	return cfg.Name
}

// recordModelAudit emits one model-level audit entry (S-05). targetKind selects
// llm_model for catalog mutations and llm_config for a catalog refresh. Audit
// delivery is best effort and never fails the business operation.
func (s *Service) recordModelAudit(ctx context.Context, action string, configID int64, configName string, modelID, actorID int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["config_id"] = id.Format(configID)
	if configName != "" {
		metadata["config_name"] = configName
	}
	targetKind := "llm_model"
	targetID := ""
	if modelID > 0 {
		targetID = id.Format(modelID)
	} else {
		targetKind = "llm_config"
		targetID = id.Format(configID)
	}
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actor,
		Action:     action,
		TargetKind: targetKind,
		TargetID:   targetID,
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}

// recordConfigRefreshed audits a successful catalog refresh with the merge
// counts and the manual capabilities that were preserved (S-02/S-05).
func (s *Service) recordConfigRefreshed(ctx context.Context, cfg llm.Config, existing, refreshed []llm.Model, total int, actorID int64) {
	if s.audit == nil {
		return
	}
	previousByName := make(map[string]llm.Model, len(existing))
	for _, model := range existing {
		previousByName[model.Model] = model
	}
	present := make(map[string]bool, len(refreshed))
	added, updated := 0, 0
	for _, model := range refreshed {
		present[model.Model] = true
		if _, ok := previousByName[model.Model]; ok {
			updated++
		} else {
			added++
		}
	}
	deprecated := 0
	for _, model := range existing {
		if model.Source != llm.SourceManual && !present[model.Model] {
			deprecated++
		}
	}
	manual := make(map[string][]string)
	for _, model := range refreshed {
		if fields := manualCapabilityFields(model); len(fields) > 0 {
			manual[model.Model] = fields
		}
	}
	s.recordModelAudit(ctx, "llm.config_refreshed", cfg.ID, cfg.Name, 0, actorID, map[string]any{
		"added":               added,
		"updated":             updated,
		"deprecated":          deprecated,
		"total":               total,
		"manual_capabilities": manual,
	})
}

// manualCapabilityFields lists the boolean capabilities whose stored value is
// administrator-sourced and therefore preserved across a refresh.
func manualCapabilityFields(model llm.Model) []string {
	capabilities := model.EffectiveCapabilities()
	fields := make([]string, 0, 4)
	if capabilities.SupportsToolsSource == llm.SourceManual {
		fields = append(fields, "supports_tools")
	}
	if capabilities.SupportsVisionSource == llm.SourceManual {
		fields = append(fields, "supports_vision")
	}
	if capabilities.SupportsReasoningSource == llm.SourceManual {
		fields = append(fields, "supports_reasoning")
	}
	if capabilities.SupportsPromptCacheSource == llm.SourceManual {
		fields = append(fields, "supports_prompt_cache")
	}
	return fields
}

// capabilitySources flattens the four capability provenance markers into the
// audit metadata shape.
func capabilitySources(capabilities llm.Capabilities) map[string]any {
	return map[string]any{
		"supports_tools":        capabilities.SupportsToolsSource,
		"supports_vision":       capabilities.SupportsVisionSource,
		"supports_reasoning":    capabilities.SupportsReasoningSource,
		"supports_prompt_cache": capabilities.SupportsPromptCacheSource,
	}
}

// changedModelFields diffs the audit-relevant model fields (capabilities,
// parameters, provenance and deprecation) into old/new metadata. Model rows
// carry no secrets, so every listed field is safe to audit.
func changedModelFields(before, after llm.Model) ([]string, map[string]any, map[string]any) {
	fields := make([]string, 0, 12)
	oldValues := make(map[string]any)
	newValues := make(map[string]any)
	add := func(name string, oldValue, newValue any) {
		if oldValue != newValue {
			fields = append(fields, name)
			oldValues[name] = oldValue
			newValues[name] = newValue
		}
	}
	add("context_window", before.ContextWindow, after.ContextWindow)
	add("max_output", before.MaxOutput, after.MaxOutput)
	add("deprecated", before.Deprecated, after.Deprecated)
	add("window_source", before.EffectiveWindowSource(), after.EffectiveWindowSource())
	add("output_source", before.EffectiveOutputSource(), after.EffectiveOutputSource())

	beforeCapabilities := before.EffectiveCapabilities()
	afterCapabilities := after.EffectiveCapabilities()
	add("supports_tools", beforeCapabilities.SupportsTools, afterCapabilities.SupportsTools)
	add("supports_vision", beforeCapabilities.SupportsVision, afterCapabilities.SupportsVision)
	add("supports_reasoning", beforeCapabilities.SupportsReasoning, afterCapabilities.SupportsReasoning)
	add("supports_prompt_cache", beforeCapabilities.SupportsPromptCache, afterCapabilities.SupportsPromptCache)
	add("supports_tools_source", beforeCapabilities.SupportsToolsSource, afterCapabilities.SupportsToolsSource)
	add("supports_vision_source", beforeCapabilities.SupportsVisionSource, afterCapabilities.SupportsVisionSource)
	add("supports_reasoning_source", beforeCapabilities.SupportsReasoningSource, afterCapabilities.SupportsReasoningSource)
	add("supports_prompt_cache_source", beforeCapabilities.SupportsPromptCacheSource, afterCapabilities.SupportsPromptCacheSource)
	return fields, oldValues, newValues
}

// defaultModelInUseError is the typed 409 for deleting the configured default
// model. Its details point at the configuration and the reassign-first
// resolution instead of only restating the conflict.
func defaultModelInUseError(cfg llm.Config, model llm.Model) *platform.Error {
	return &platform.Error{
		Status: http.StatusConflict,
		Code:   CodeDefaultModelInUse,
		Message: fmt.Sprintf("model %q is the default model of llm config %q; set default_model_id to another model before deleting it",
			model.Model, cfg.Name),
		Details: map[string]any{
			"config_id":        id.Format(cfg.ID),
			"config_name":      cfg.Name,
			"default_model_id": id.Format(model.ID),
			"field":            "default_model_id",
			"next_action":      "choose another model as default_model_id, then delete this model",
		},
	}
}

// validateParamPair enforces the cross-field rule from A §4.1/R-A1-6: when the
// context window is known (>0), max_output must stay within it. Equal values are
// allowed (the UI warns about a zero input budget instead). A window of 0 means
// auto/unknown, so any non-negative max_output is acceptable.
func validateParamPair(window, output int64) error {
	if output < 0 {
		return platform.Invalid("max_output", "must not be negative")
	}
	if window > 0 && output > window {
		return platform.Invalid("max_output", fmt.Sprintf(
			"must not exceed context_window (%d tokens)", window))
	}
	return nil
}

// withModelView fills the derived, read-only fields of a model before it is
// returned to the HTTP layer. They are never persisted.
func withModelView(model llm.Model) llm.Model {
	model.MaxInput = MaxInput(model)
	return model
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
