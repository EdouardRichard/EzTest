// Package llminfra contains the PostgreSQL repository and the
// OpenAI-compatible upstream client for the LLM configuration context.
package llminfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const configColumns = `id, name, provider, protocol, base_url, api_key, headers,
	timeout_seconds, max_retries, is_default, status, created_by, created_at, updated_at`

const modelColumns = `id, config_id, model, display_name, context_window, max_output,
	supports_tools, supports_vision, source, status, created_at, updated_at`

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
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13, $14)
	`, cfg.ID, cfg.Name, cfg.Provider, cfg.Protocol, cfg.BaseURL, cfg.APIKey, headers,
		cfg.TimeoutSeconds, cfg.MaxRetries, cfg.IsDefault, cfg.Status, cfg.CreatedBy,
		cfg.CreatedAt, cfg.UpdatedAt); err != nil {
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
			is_default = $10, status = $11, updated_at = $12
		WHERE id = $1
	`, cfg.ID, cfg.Name, cfg.Provider, cfg.Protocol, cfg.BaseURL, cfg.APIKey, headers,
		cfg.TimeoutSeconds, cfg.MaxRetries, cfg.IsDefault, cfg.Status, cfg.UpdatedAt)
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
		if _, err := tx.Exec(ctx, `
			INSERT INTO llm_model (`+modelColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (config_id, model) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				context_window = EXCLUDED.context_window,
				max_output = EXCLUDED.max_output,
				supports_tools = EXCLUDED.supports_tools,
				supports_vision = EXCLUDED.supports_vision,
				source = EXCLUDED.source,
				status = EXCLUDED.status,
				updated_at = EXCLUDED.updated_at
		`, model.ID, configID, model.Model, model.DisplayName, model.ContextWindow,
			model.MaxOutput, model.SupportsTools, model.SupportsVision, model.Source,
			model.Status, model.CreatedAt, model.UpdatedAt); err != nil {
			return nil, mapModelError(err)
		}
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
	_, err := r.pool.Exec(ctx, `
		INSERT INTO llm_model (`+modelColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, model.ID, model.ConfigID, model.Model, model.DisplayName, model.ContextWindow,
		model.MaxOutput, model.SupportsTools, model.SupportsVision, model.Source,
		model.Status, model.CreatedAt, model.UpdatedAt)
	if err != nil {
		return llm.Model{}, mapModelError(err)
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
	if err := row.Scan(
		&cfg.ID, &cfg.Name, &cfg.Provider, &cfg.Protocol, &cfg.BaseURL, &cfg.APIKey,
		&headers, &cfg.TimeoutSeconds, &cfg.MaxRetries, &cfg.IsDefault, &cfg.Status,
		&cfg.CreatedBy, &cfg.CreatedAt, &cfg.UpdatedAt,
	); err != nil {
		return llm.Config{}, err
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
	if err := row.Scan(
		&model.ID, &model.ConfigID, &model.Model, &model.DisplayName, &model.ContextWindow,
		&model.MaxOutput, &model.SupportsTools, &model.SupportsVision, &model.Source,
		&model.Status, &model.CreatedAt, &model.UpdatedAt,
	); err != nil {
		return llm.Model{}, err
	}
	return model, nil
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

// mapConfigError translates unique violations on llm_config.
func mapConfigError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return llm.ErrNameTaken
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
