// Package application implements the LLM configuration use cases on top of the
// domain repository port and the audit recorder. It never logs or audits
// secrets: only the configuration id, name, protocol, model and default flag
// are recorded.
package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// ConfigService manages LLM gateway configurations for the admin settings
// screen and seeds the first default configuration from the environment.
type ConfigService struct {
	repo     llm.Repository
	recorder audit.Recorder
}

// NewConfigService wires the configuration service to its repository and audit
// recorder. A nil recorder is treated as a no-op so a partially wired process
// cannot panic on the first write.
func NewConfigService(repo llm.Repository, recorder audit.Recorder) *ConfigService {
	if recorder == nil {
		recorder = audit.NopRecorder{}
	}
	return &ConfigService{repo: repo, recorder: recorder}
}

// withRequestContext merges the HTTP request metadata and the authenticated
// actor carried by ctx into entry. Values the service set explicitly win.
func withRequestContext(ctx context.Context, entry auditdomain.Entry) auditdomain.Entry {
	meta := audit.MetaFromContext(ctx)
	if entry.RequestID == "" {
		entry.RequestID = meta.RequestID
	}
	if entry.IP == "" {
		entry.IP = meta.IP
	}
	if entry.UserAgent == "" {
		entry.UserAgent = meta.UserAgent
	}
	if entry.ActorUserID == nil {
		if actorID, ok := audit.ActorIDFromContext(ctx); ok {
			entry.ActorUserID = &actorID
		}
	}
	return entry
}

// recordAudit writes one audit entry. Auditing is best-effort: a failed write
// must not turn a completed action into an error, but it is logged. The log
// line never includes the entry metadata, so secrets cannot leak through it.
func (s *ConfigService) recordAudit(ctx context.Context, entry auditdomain.Entry) {
	entry = withRequestContext(ctx, entry)
	if err := s.recorder.Record(ctx, entry); err != nil {
		slog.ErrorContext(ctx, "audit: record llm config entry", "action", entry.Action, "error", err)
	}
}

// configMetadata is the non-sensitive audit metadata for a configuration. It
// deliberately omits the API key and header values.
func configMetadata(cfg llm.Config) map[string]any {
	return map[string]any{
		"name":       cfg.Name,
		"protocol":   string(cfg.Protocol),
		"model":      cfg.Model,
		"is_default": cfg.IsDefault,
	}
}

// DefaultCreateParams returns the explicit defaults for a create request. It is
// the service-level default policy used by the admin API: retry_count 0 is a
// valid explicit value, so the HTTP layer resolves this policy first and only
// overrides fields the request actually provides. The values mirror the
// llm_config column defaults.
func DefaultCreateParams() llm.CreateParams {
	return llm.CreateParams{
		RetryCount:        llm.DefaultRetryCount,
		RetryIntervalMS:   llm.DefaultRetryIntervalMS,
		TimeoutMS:         llm.DefaultTimeoutMS,
		StreamEnabled:     true,
		MultimodalEnabled: true,
	}
}

// normalizeCreateParams trims the persisted strings. Header values are kept
// byte-for-byte; only their names are trimmed.
func normalizeCreateParams(params llm.CreateParams) llm.CreateParams {
	params.Name = strings.TrimSpace(params.Name)
	params.BaseURL = strings.TrimSpace(params.BaseURL)
	params.Model = strings.TrimSpace(params.Model)
	params.Headers = normalizeHeaderNames(params.Headers)
	return params
}

// normalizeUpdateParams trims the explicitly provided strings and header names.
func normalizeUpdateParams(params llm.UpdateParams) llm.UpdateParams {
	if params.Name != nil {
		value := strings.TrimSpace(*params.Name)
		params.Name = &value
	}
	if params.BaseURL != nil {
		value := strings.TrimSpace(*params.BaseURL)
		params.BaseURL = &value
	}
	if params.Model != nil {
		value := strings.TrimSpace(*params.Model)
		params.Model = &value
	}
	if params.Headers != nil {
		normalized := normalizeHeaderNames(*params.Headers)
		params.Headers = &normalized
	}
	return params
}

// normalizeHeaderNames trims header names. A nil map stays nil so an omitted
// map is not turned into an empty replacement.
func normalizeHeaderNames(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	normalized := make(map[string]string, len(headers))
	for name, value := range headers {
		normalized[strings.TrimSpace(name)] = value
	}
	return normalized
}

// Create validates and stores a new configuration and records llm.config_created.
// Omitted timing fields (zero interval/timeout, whose valid ranges exclude zero)
// fall back to the documented defaults.
func (s *ConfigService) Create(ctx context.Context, params llm.CreateParams) (llm.Config, error) {
	params = normalizeCreateParams(params)
	if params.RetryIntervalMS == 0 {
		params.RetryIntervalMS = llm.DefaultRetryIntervalMS
	}
	if params.TimeoutMS == 0 {
		params.TimeoutMS = llm.DefaultTimeoutMS
	}
	if err := params.Validate(); err != nil {
		return llm.Config{}, err
	}
	cfg, err := s.repo.Create(ctx, params)
	if err != nil {
		return llm.Config{}, err
	}
	s.recordAudit(ctx, auditdomain.Entry{
		Action:     audit.ActionLLMConfigCreated,
		TargetType: "llm_config",
		TargetID:   strconv.FormatInt(cfg.ID, 10),
		Metadata:   configMetadata(cfg),
	})
	return cfg, nil
}

// Update applies a partial update and records llm.config_updated.
func (s *ConfigService) Update(ctx context.Context, id int64, params llm.UpdateParams) (llm.Config, error) {
	params = normalizeUpdateParams(params)
	if err := params.Validate(); err != nil {
		return llm.Config{}, err
	}
	cfg, err := s.repo.Update(ctx, id, params)
	if err != nil {
		return llm.Config{}, err
	}
	s.recordAudit(ctx, auditdomain.Entry{
		Action:     audit.ActionLLMConfigUpdated,
		TargetType: "llm_config",
		TargetID:   strconv.FormatInt(cfg.ID, 10),
		Metadata:   configMetadata(cfg),
	})
	return cfg, nil
}

// Delete removes a configuration and records llm.config_deleted.
func (s *ConfigService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, auditdomain.Entry{
		Action:     audit.ActionLLMConfigDeleted,
		TargetType: "llm_config",
		TargetID:   strconv.FormatInt(id, 10),
	})
	return nil
}

// Get returns one configuration with its secrets decrypted for internal use.
func (s *ConfigService) Get(ctx context.Context, id int64) (llm.Config, error) {
	return s.repo.Get(ctx, id)
}

// List returns every configuration with secrets decrypted for internal use.
func (s *ConfigService) List(ctx context.Context) ([]llm.Config, error) {
	return s.repo.List(ctx)
}

// GetDefault returns the default configuration or ErrConfigNotFound when the
// deployment has not configured LLM access yet.
func (s *ConfigService) GetDefault(ctx context.Context) (llm.Config, error) {
	return s.repo.GetDefault(ctx)
}

// SeedFromEnv creates the default configuration from the optional environment
// seed when no default exists yet. It is a no-op when any of the three values
// is missing, and idempotent once a default is configured. It reports whether
// this call created the configuration.
func (s *ConfigService) SeedFromEnv(ctx context.Context, baseURL, apiKey, model string) (bool, error) {
	baseURL = strings.TrimSpace(baseURL)
	apiKey = strings.TrimSpace(apiKey)
	model = strings.TrimSpace(model)
	if baseURL == "" || apiKey == "" || model == "" {
		return false, nil
	}

	_, err := s.repo.GetDefault(ctx)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, llm.ErrConfigNotFound):
		return false, fmt.Errorf("llm: check default configuration: %w", err)
	}

	_, err = s.Create(ctx, llm.CreateParams{
		Name:              llm.SeedConfigName,
		Protocol:          llm.ProtocolChat,
		BaseURL:           baseURL,
		Model:             model,
		APIKey:            apiKey,
		RetryCount:        llm.SeedRetryCount,
		RetryIntervalMS:   llm.DefaultRetryIntervalMS,
		TimeoutMS:         llm.DefaultTimeoutMS,
		StreamEnabled:     llm.SeedStreamEnabled,
		MultimodalEnabled: llm.SeedMultimodalEnabled,
		IsDefault:         true,
	})
	if err != nil {
		// A concurrent startup can win the seed between the check and the
		// insert. The partial unique index allows one default, so once a
		// default exists this seed is complete.
		if errors.Is(err, llm.ErrNameTaken) || errors.Is(err, llm.ErrDefaultConflict) {
			_, getErr := s.repo.GetDefault(ctx)
			switch {
			case getErr == nil:
				return false, nil
			case !errors.Is(getErr, llm.ErrConfigNotFound):
				return false, fmt.Errorf("llm: check default configuration after seed conflict: %w", getErr)
			case errors.Is(err, llm.ErrNameTaken):
				// A non-default configuration already uses the seed name. That
				// is not a fatal startup condition: keep the operator's row
				// untouched and let an administrator promote a configuration
				// explicitly.
				slog.WarnContext(ctx,
					"llm: default seed skipped because a non-default configuration already uses the name",
					"name", llm.SeedConfigName)
				return false, nil
			}
		}
		return false, fmt.Errorf("llm: seed default configuration: %w", err)
	}
	return true, nil
}
