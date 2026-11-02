// Package llminfra contains the PostgreSQL repository and the
// OpenAI-compatible upstream client for the LLM configuration context.
package llminfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// protocolCheckConstraint is the named 0.2.1 CHECK on llm_config.protocol
// (llm_0003_protocol021.sql). A CHECK violation on this constraint means the
// caller bypassed application validation with an unsupported protocol.
const protocolCheckConstraint = "chk_llm_config_protocol"

const configColumns = `id, name, provider, protocol, base_url, api_key, headers,
	timeout_seconds, max_retries, is_default, status, created_by, created_at, updated_at,
	thinking_type, reasoning_effort, default_model_id`

const modelColumns = `id, config_id, model, display_name, context_window, max_output,
	supports_tools, supports_vision, source, status, created_at, updated_at,
	capabilities, deprecated`

// Repository stores llm_config and llm_model rows.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository wraps a pgx pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ListConfigs returns every configuration ordered by ID.
func (r *Repository) ListConfigs(ctx context.Context) ([]llm.Config, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+configColumns+` FROM llm_config ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list llm configs: %w", err)
	}
	defer rows.Close()

	configs := make([]llm.Config, 0)
	for rows.Next() {
		cfg, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list llm configs: %w", err)
	}
	return configs, nil
}

// GetConfig returns one configuration or llm.ErrConfigNotFound.
func (r *Repository) GetConfig(ctx context.Context, id int64) (llm.Config, error) {
	cfg, err := scanConfig(r.pool.QueryRow(ctx, `SELECT `+configColumns+` FROM llm_config WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return llm.Config{}, llm.ErrConfigNotFound
		}
		return llm.Config{}, err
	}
	return cfg, nil
}

// CreateConfig inserts a configuration. When config.IsDefault is set, the
// previously default row is demoted in the same transaction.
func (r *Repository) CreateConfig(ctx context.Context, cfg llm.Config) (llm.Config, error) {
	headers, err := encodeHeaders(cfg.Headers)
	if err != nil {
		return llm.Config{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return llm.Config{}, fmt.Errorf("create llm config: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if cfg.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE llm_config SET is_default = false WHERE is_default`); err != nil {
			return llm.Config{}, fmt.Errorf("create llm config: clear defaults: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO llm_config (`+configColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, cfg.ID, cfg.Name, cfg.Provider, cfg.Protocol, cfg.BaseURL, cfg.APIKey, headers,
		cfg.TimeoutSeconds, cfg.MaxRetries, cfg.IsDefault, cfg.Status, cfg.CreatedBy,
		cfg.CreatedAt, cfg.UpdatedAt, effectiveThinkingType(cfg), effectiveReasoningEffort(cfg),
		nullableModelID(cfg.DefaultModelID)); err != nil {
		return llm.Config{}, mapConfigError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return llm.Config{}, fmt.Errorf("create llm config: commit: %w", err)
	}
	return cfg, nil
}

// UpdateConfig rewrites a configuration and keeps at most one default.
func (r *Repository) UpdateConfig(ctx context.Context, cfg llm.Config) (llm.Config, error) {
	headers, err := encodeHeaders(cfg.Headers)
	if err != nil {
		return llm.Config{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return llm.Config{}, fmt.Errorf("update llm config: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if cfg.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE llm_config SET is_default = false WHERE is_default AND id <> $1`, cfg.ID); err != nil {
			return llm.Config{}, fmt.Errorf("update llm config: clear defaults: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE llm_config SET
			name = $2, provider = $3, protocol = $4, base_url = $5, api_key = $6,
			headers = $7::jsonb, timeout_seconds = $8, max_retries = $9,
			is_default = $10, status = $11, updated_at = $12,
			thinking_type = $13, reasoning_effort = $14, default_model_id = $15
		WHERE id = $1
	`, cfg.ID, cfg.Name, cfg.Provider, cfg.Protocol, cfg.BaseURL, cfg.APIKey, headers,
		cfg.TimeoutSeconds, cfg.MaxRetries, cfg.IsDefault, cfg.Status, cfg.UpdatedAt,
		effectiveThinkingType(cfg), effectiveReasoningEffort(cfg), nullableModelID(cfg.DefaultModelID))
	if err != nil {
		return llm.Config{}, mapConfigError(err)
	}
	if tag.RowsAffected() == 0 {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return llm.Config{}, fmt.Errorf("update llm config: commit: %w", err)
	}
	return cfg, nil
}

// DeleteConfig removes a configuration; llm_model rows cascade.
func (r *Repository) DeleteConfig(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM llm_config WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete llm config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return llm.ErrConfigNotFound
	}
	return nil
}

// ListModels returns the whole model catalog ordered by ID.
func (r *Repository) ListModels(ctx context.Context) ([]llm.Model, error) {
	return r.queryModels(ctx, `SELECT `+modelColumns+` FROM llm_model ORDER BY id`)
}

// ListModelsByConfig returns one configuration's models ordered by ID.
func (r *Repository) ListModelsByConfig(ctx context.Context, configID int64) ([]llm.Model, error) {
	return r.queryModels(ctx, `SELECT `+modelColumns+` FROM llm_model WHERE config_id = $1 ORDER BY id`, configID)
}

func (r *Repository) queryModels(ctx context.Context, sql string, args ...any) ([]llm.Model, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list llm models: %w", err)
	}
	defer rows.Close()

	models := make([]llm.Model, 0)
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list llm models: %w", err)
	}
	return models, nil
}

// UpsertModels inserts or updates models matched by (config_id, model) and
// returns the configuration's complete catalog in ID order.
func (r *Repository) UpsertModels(ctx context.Context, configID int64, models []llm.Model, at time.Time) ([]llm.Model, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("upsert llm models: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, model := range models {
		encodedCapabilities, err := encodeCapabilities(model.EffectiveCapabilities())
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO llm_model (`+modelColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14)
			ON CONFLICT (config_id, model) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				context_window = EXCLUDED.context_window,
				max_output = EXCLUDED.max_output,
				supports_tools = EXCLUDED.supports_tools,
				supports_vision = EXCLUDED.supports_vision,
				source = EXCLUDED.source,
				status = EXCLUDED.status,
				capabilities = EXCLUDED.capabilities,
				deprecated = EXCLUDED.deprecated,
				updated_at = EXCLUDED.updated_at
		`, model.ID, configID, model.Model, model.DisplayName, model.ContextWindow,
			model.MaxOutput, model.SupportsTools, model.SupportsVision, model.Source,
			model.Status, model.CreatedAt, model.UpdatedAt, encodedCapabilities, model.Deprecated); err != nil {
			return nil, mapModelError(err)
		}
	}

	if err := markMissingCatalogModelsDeprecated(ctx, tx, configID, models, at); err != nil {
		return nil, err
	}

	result, err := r.queryModelsTx(ctx, tx, configID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("upsert llm models: commit: %w", err)
	}
	return result, nil
}

func (r *Repository) queryModelsTx(ctx context.Context, tx pgx.Tx, configID int64) ([]llm.Model, error) {
	rows, err := tx.Query(ctx, `SELECT `+modelColumns+` FROM llm_model WHERE config_id = $1 ORDER BY id`, configID)
	if err != nil {
		return nil, fmt.Errorf("list llm models: %w", err)
	}
	defer rows.Close()

	models := make([]llm.Model, 0)
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list llm models: %w", err)
	}
	return models, nil
}

// InsertModel inserts one manual model.
func (r *Repository) InsertModel(ctx context.Context, model llm.Model) (llm.Model, error) {
	encodedCapabilities, err := encodeCapabilities(model.EffectiveCapabilities())
	if err != nil {
		return llm.Model{}, err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO llm_model (`+modelColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14)
	`, model.ID, model.ConfigID, model.Model, model.DisplayName, model.ContextWindow,
		model.MaxOutput, model.SupportsTools, model.SupportsVision, model.Source,
		model.Status, model.CreatedAt, model.UpdatedAt, encodedCapabilities, model.Deprecated)
	if err != nil {
		return llm.Model{}, mapModelError(err)
	}
	return model, nil
}

// UpdateModel rewrites one existing row's capability set and deprecated flag.
// The (id, config_id) pair scopes the write so a model cannot be edited through
// another configuration. Legacy tools/vision/context columns stay authoritative
// for P0 readers and are rewritten together with the capability object.
func (r *Repository) UpdateModel(ctx context.Context, model llm.Model) (llm.Model, error) {
	encodedCapabilities, err := encodeCapabilities(model.EffectiveCapabilities())
	if err != nil {
		return llm.Model{}, err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE llm_model SET
			context_window = $3, supports_tools = $4, supports_vision = $5,
			capabilities = $6::jsonb, deprecated = $7, updated_at = $8
		WHERE id = $1 AND config_id = $2
	`, model.ID, model.ConfigID, model.ContextWindow, model.SupportsTools,
		model.SupportsVision, encodedCapabilities, model.Deprecated, model.UpdatedAt)
	if err != nil {
		return llm.Model{}, mapModelError(err)
	}
	if tag.RowsAffected() == 0 {
		return llm.Model{}, llm.ErrModelNotFound
	}
	return model, nil
}

// DeleteModel removes one catalog row.
func (r *Repository) DeleteModel(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM llm_model WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete llm model: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return llm.ErrModelNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConfig(row rowScanner) (llm.Config, error) {
	var cfg llm.Config
	var headers []byte
	var defaultModelID *int64
	if err := row.Scan(
		&cfg.ID, &cfg.Name, &cfg.Provider, &cfg.Protocol, &cfg.BaseURL, &cfg.APIKey,
		&headers, &cfg.TimeoutSeconds, &cfg.MaxRetries, &cfg.IsDefault, &cfg.Status,
		&cfg.CreatedBy, &cfg.CreatedAt, &cfg.UpdatedAt,
		&cfg.ThinkingType, &cfg.ReasoningEffort, &defaultModelID,
	); err != nil {
		return llm.Config{}, err
	}
	if defaultModelID != nil {
		cfg.DefaultModelID = *defaultModelID
	}
	cfg.Headers = make(map[string]string)
	if len(headers) > 0 {
		if err := json.Unmarshal(headers, &cfg.Headers); err != nil {
			return llm.Config{}, fmt.Errorf("decode llm config headers: %w", err)
		}
		if cfg.Headers == nil {
			cfg.Headers = make(map[string]string)
		}
	}
	return cfg, nil
}

func scanModel(row rowScanner) (llm.Model, error) {
	var model llm.Model
	var capabilitiesRaw []byte
	if err := row.Scan(
		&model.ID, &model.ConfigID, &model.Model, &model.DisplayName, &model.ContextWindow,
		&model.MaxOutput, &model.SupportsTools, &model.SupportsVision, &model.Source,
		&model.Status, &model.CreatedAt, &model.UpdatedAt,
		&capabilitiesRaw, &model.Deprecated,
	); err != nil {
		return llm.Model{}, err
	}
	if err := decodeCapabilities(capabilitiesRaw, &model); err != nil {
		return llm.Model{}, err
	}
	return model, nil
}

// encodeCapabilities serializes the full capability set into the JSONB object.
// The legacy columns stay authoritative for tools/vision/context_window; the
// object also carries reasoning/prompt-cache for readers that need them.
func encodeCapabilities(capabilities llm.Capabilities) ([]byte, error) {
	raw, err := json.Marshal(capabilities)
	if err != nil {
		return nil, fmt.Errorf("encode llm model capabilities: %w", err)
	}
	return raw, nil
}

// decodeCapabilities merges the stored JSONB capability object with the legacy
// columns. Older rows have an empty object, which leaves the columns intact.
func decodeCapabilities(raw []byte, model *llm.Model) error {
	if len(raw) == 0 {
		model.Capabilities = model.EffectiveCapabilities()
		return nil
	}
	var stored llm.Capabilities
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("decode llm model capabilities: %w", err)
	}
	model.Capabilities = stored
	model.Capabilities = model.EffectiveCapabilities()
	return nil
}

// markMissingCatalogModelsDeprecated keeps models that disappeared from the
// upstream catalog: they are deprecated instead of silently deleted (spec
// §7.3). Manual rows are not remote-catalog entries and stay untouched.
func markMissingCatalogModelsDeprecated(ctx context.Context, tx pgx.Tx, configID int64, models []llm.Model, at time.Time) error {
	names := make([]string, 0, len(models))
	for _, model := range models {
		if model.Source == llm.SourceCatalog || model.Source == "" {
			names = append(names, model.Model)
		}
	}
	var err error
	if len(names) == 0 {
		_, err = tx.Exec(ctx, `
			UPDATE llm_model SET deprecated = true, updated_at = $2
			WHERE config_id = $1 AND source = $3 AND NOT deprecated
		`, configID, at, llm.SourceCatalog)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE llm_model SET deprecated = true, updated_at = $2
			WHERE config_id = $1 AND source = $3 AND NOT deprecated AND model <> ALL($4)
		`, configID, at, llm.SourceCatalog, names)
	}
	if err != nil {
		return fmt.Errorf("mark retired llm models deprecated: %w", err)
	}
	return nil
}

func effectiveThinkingType(cfg llm.Config) string {
	if cfg.ThinkingType == "" {
		return llm.DefaultThinkingType
	}
	return cfg.ThinkingType
}

func effectiveReasoningEffort(cfg llm.Config) string {
	if cfg.ReasoningEffort == "" {
		return llm.DefaultReasoningEffort
	}
	return cfg.ReasoningEffort
}

// nullableModelID maps the domain's zero (not configured) to SQL NULL.
func nullableModelID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func encodeHeaders(headers map[string]string) (string, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return "", fmt.Errorf("encode llm config headers: %w", err)
	}
	return string(raw), nil
}

// mapConfigError translates unique violations and unsupported-protocol CHECK
// violations on llm_config. A value rejected by chk_llm_config_protocol is a
// stable 400: it cannot be persisted even though application validation
// normally rejects it first, so it must not surface as an unmapped 500.
func mapConfigError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return llm.ErrNameTaken
		case "23514":
			if pgErr.ConstraintName == protocolCheckConstraint {
				return &platform.Error{
					Status:  http.StatusBadRequest,
					Code:    "invalid_protocol",
					Message: "protocol must be one of openai_chat_completions, openai_responses (legacy openai/openai_compatible accepted)",
					Details: map[string]any{"field": "protocol"},
				}
			}
		}
	}
	return err
}

// mapModelError translates unique and foreign-key violations on llm_model.
func mapModelError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return llm.ErrModelTaken
		case "23503":
			return llm.ErrConfigNotFound
		}
	}
	return err
}
