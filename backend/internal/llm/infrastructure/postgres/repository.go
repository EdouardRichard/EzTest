// Package postgres implements the LLM configuration repository port with
// PostgreSQL. API keys and custom headers are encrypted with the platform
// keyring before they are stored and decrypted when a row is read.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/crypto"
)

const (
	// uniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
	uniqueViolationCode = "23505"
)

// configColumns is the column list shared by every configuration query and the
// row order scanConfig expects.
const configColumns = `id, name, protocol, base_url, model, api_key_encrypted, key_version, headers_encrypted, retry_count, retry_interval_ms, timeout_ms, stream_enabled, multimodal_enabled, is_default, model_catalog, catalog_fetched_at, created_at, updated_at`

// Field AAD values bind each ciphertext to its column, so a value copied from
// one column into the other fails authentication.
var (
	aadAPIKey  = []byte("eztest.llm.api_key")
	aadHeaders = []byte("eztest.llm.headers")
)

// ConfigRepository implements llm.Repository on a pgx pool.
type ConfigRepository struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
}

// NewConfigRepository returns a configuration repository backed by pool and
// the keyring used to encrypt and decrypt secrets.
func NewConfigRepository(pool *pgxpool.Pool, keyring *crypto.Keyring) *ConfigRepository {
	return &ConfigRepository{pool: pool, keyring: keyring}
}

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one config.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanConfig scans one row in configColumns order and decrypts its secrets.
func (r *ConfigRepository) scanConfig(row rowScanner) (llm.Config, error) {
	var (
		cfg        llm.Config
		protocol   string
		apiKeyEnc  []byte
		headersEnc []byte
		catalog    []byte
		createdAt  time.Time
		updatedAt  time.Time
	)
	if err := row.Scan(
		&cfg.ID,
		&cfg.Name,
		&protocol,
		&cfg.BaseURL,
		&cfg.Model,
		&apiKeyEnc,
		&cfg.KeyVersion,
		&headersEnc,
		&cfg.RetryCount,
		&cfg.RetryIntervalMS,
		&cfg.TimeoutMS,
		&cfg.StreamEnabled,
		&cfg.MultimodalEnabled,
		&cfg.IsDefault,
		&catalog,
		&cfg.CatalogFetchedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return llm.Config{}, err
	}
	cfg.Protocol = llm.Protocol(protocol)
	cfg.Headers = make(map[string]string)
	cfg.ModelCatalog = make([]llm.ModelInfo, 0)

	if len(apiKeyEnc) > 0 {
		plaintext, err := r.keyring.DecryptWithAAD(apiKeyEnc, cfg.KeyVersion, aadAPIKey)
		if err != nil {
			return llm.Config{}, fmt.Errorf("postgres: decrypt api key: %w", err)
		}
		cfg.APIKey = string(plaintext)
	}
	if len(headersEnc) > 0 {
		plaintext, err := r.keyring.DecryptWithAAD(headersEnc, cfg.KeyVersion, aadHeaders)
		if err != nil {
			return llm.Config{}, fmt.Errorf("postgres: decrypt headers: %w", err)
		}
		if err := json.Unmarshal(plaintext, &cfg.Headers); err != nil {
			return llm.Config{}, fmt.Errorf("postgres: decode headers: %w", err)
		}
		if cfg.Headers == nil {
			cfg.Headers = make(map[string]string)
		}
	}
	if len(catalog) > 0 {
		if err := json.Unmarshal(catalog, &cfg.ModelCatalog); err != nil {
			return llm.Config{}, fmt.Errorf("postgres: decode model catalog: %w", err)
		}
		if cfg.ModelCatalog == nil {
			cfg.ModelCatalog = make([]llm.ModelInfo, 0)
		}
	}
	return cfg, nil
}

// encryptSecrets encrypts the API key and headers with the active key version.
// An empty API key or an empty header map is stored as NULL. The returned
// version is the active key version, even when both secrets are empty, so
// key_version always records the version a later save would use.
func (r *ConfigRepository) encryptSecrets(apiKey string, headers map[string]string) ([]byte, []byte, int, error) {
	version := r.keyring.ActiveVersion()
	var apiKeyEnc, headersEnc []byte
	if apiKey != "" {
		encrypted, encryptedVersion, err := r.keyring.EncryptWithAAD([]byte(apiKey), aadAPIKey)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("postgres: encrypt api key: %w", err)
		}
		apiKeyEnc, version = encrypted, encryptedVersion
	}
	if len(headers) > 0 {
		plaintext, err := json.Marshal(headers)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("postgres: encode headers: %w", err)
		}
		encrypted, encryptedVersion, err := r.keyring.EncryptWithAAD(plaintext, aadHeaders)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("postgres: encrypt headers: %w", err)
		}
		headersEnc, version = encrypted, encryptedVersion
	}
	return apiKeyEnc, headersEnc, version, nil
}

// Create inserts a configuration. When it is the default, the previous default
// is cleared in the same transaction so the partial unique index never sees
// two defaults.
func (r *ConfigRepository) Create(ctx context.Context, params llm.CreateParams) (llm.Config, error) {
	retryIntervalMS := params.RetryIntervalMS
	if retryIntervalMS == 0 {
		retryIntervalMS = llm.DefaultRetryIntervalMS
	}
	timeoutMS := params.TimeoutMS
	if timeoutMS == 0 {
		timeoutMS = llm.DefaultTimeoutMS
	}
	apiKeyEnc, headersEnc, keyVersion, err := r.encryptSecrets(params.APIKey, params.Headers)
	if err != nil {
		return llm.Config{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return llm.Config{}, fmt.Errorf("postgres: begin create llm config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if params.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE llm_configs SET is_default = false, updated_at = now() WHERE is_default`); err != nil {
			return llm.Config{}, fmt.Errorf("postgres: clear default llm config: %w", err)
		}
	}

	const query = `INSERT INTO llm_configs
		(name, protocol, base_url, model, api_key_encrypted, key_version, headers_encrypted,
		 retry_count, retry_interval_ms, timeout_ms, stream_enabled, multimodal_enabled, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING ` + configColumns
	cfg, err := r.scanConfig(tx.QueryRow(ctx, query,
		params.Name,
		string(params.Protocol),
		params.BaseURL,
		params.Model,
		apiKeyEnc,
		keyVersion,
		headersEnc,
		params.RetryCount,
		retryIntervalMS,
		timeoutMS,
		params.StreamEnabled,
		params.MultimodalEnabled,
		params.IsDefault,
	))
	if err != nil {
		if mapped := mapUniqueViolation(err); mapped != nil {
			return llm.Config{}, mapped
		}
		return llm.Config{}, fmt.Errorf("postgres: create llm config: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return llm.Config{}, fmt.Errorf("postgres: commit create llm config: %w", err)
	}
	return cfg, nil
}

// Update applies a partial update to an existing configuration. It loads the
// row FOR UPDATE, merges the requested fields, re-encrypts the API key and
// headers with the active key version and clears any other default in the same
// transaction when this configuration becomes the default.
func (r *ConfigRepository) Update(ctx context.Context, id int64, params llm.UpdateParams) (llm.Config, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return llm.Config{}, fmt.Errorf("postgres: begin update llm config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := r.scanConfig(tx.QueryRow(ctx,
		`SELECT `+configColumns+` FROM llm_configs WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return llm.Config{}, llm.ErrConfigNotFound
		}
		return llm.Config{}, fmt.Errorf("postgres: load llm config for update: %w", err)
	}

	name := existing.Name
	if params.Name != nil {
		name = *params.Name
	}
	protocol := existing.Protocol
	if params.Protocol != nil {
		protocol = *params.Protocol
	}
	baseURL := existing.BaseURL
	if params.BaseURL != nil {
		baseURL = *params.BaseURL
	}
	model := existing.Model
	if params.Model != nil {
		model = *params.Model
	}
	apiKey := existing.APIKey
	if params.APIKey != nil {
		apiKey = *params.APIKey
	}
	headers := existing.Headers
	if params.Headers != nil {
		headers = *params.Headers
	}
	retryCount := existing.RetryCount
	if params.RetryCount != nil {
		retryCount = *params.RetryCount
	}
	retryIntervalMS := existing.RetryIntervalMS
	if params.RetryIntervalMS != nil {
		retryIntervalMS = *params.RetryIntervalMS
	}
	if retryIntervalMS == 0 {
		retryIntervalMS = llm.DefaultRetryIntervalMS
	}
	timeoutMS := existing.TimeoutMS
	if params.TimeoutMS != nil {
		timeoutMS = *params.TimeoutMS
	}
	if timeoutMS == 0 {
		timeoutMS = llm.DefaultTimeoutMS
	}
	streamEnabled := existing.StreamEnabled
	if params.StreamEnabled != nil {
		streamEnabled = *params.StreamEnabled
	}
	multimodalEnabled := existing.MultimodalEnabled
	if params.MultimodalEnabled != nil {
		multimodalEnabled = *params.MultimodalEnabled
	}
	isDefault := existing.IsDefault
	if params.IsDefault != nil {
		isDefault = *params.IsDefault
	}
	catalog := existing.ModelCatalog
	if params.ModelCatalog != nil {
		catalog = *params.ModelCatalog
	}
	catalogFetchedAt := existing.CatalogFetchedAt
	switch {
	case params.ModelCatalog != nil && params.CatalogFetchedAt != nil:
		catalogFetchedAt = params.CatalogFetchedAt
	case params.ModelCatalog != nil:
		now := time.Now().UTC()
		catalogFetchedAt = &now
	case params.CatalogFetchedAt != nil:
		catalogFetchedAt = params.CatalogFetchedAt
	}

	if catalog == nil {
		catalog = make([]llm.ModelInfo, 0)
	}
	catalogJSON, err := json.Marshal(catalog)
	if err != nil {
		return llm.Config{}, fmt.Errorf("postgres: encode model catalog: %w", err)
	}
	apiKeyEnc, headersEnc, keyVersion, err := r.encryptSecrets(apiKey, headers)
	if err != nil {
		return llm.Config{}, err
	}

	if isDefault {
		if _, err := tx.Exec(ctx,
			`UPDATE llm_configs SET is_default = false, updated_at = now() WHERE is_default AND id <> $1`, id); err != nil {
			return llm.Config{}, fmt.Errorf("postgres: clear other default llm config: %w", err)
		}
	}

	const query = `UPDATE llm_configs SET
		name = $2, protocol = $3, base_url = $4, model = $5,
		api_key_encrypted = $6, key_version = $7, headers_encrypted = $8,
		retry_count = $9, retry_interval_ms = $10, timeout_ms = $11,
		stream_enabled = $12, multimodal_enabled = $13, is_default = $14,
		model_catalog = $15::jsonb, catalog_fetched_at = $16, updated_at = now()
		WHERE id = $1
		RETURNING ` + configColumns
	cfg, err := r.scanConfig(tx.QueryRow(ctx, query,
		id,
		name,
		string(protocol),
		baseURL,
		model,
		apiKeyEnc,
		keyVersion,
		headersEnc,
		retryCount,
		retryIntervalMS,
		timeoutMS,
		streamEnabled,
		multimodalEnabled,
		isDefault,
		string(catalogJSON),
		catalogFetchedAt,
	))
	if err != nil {
		if mapped := mapUniqueViolation(err); mapped != nil {
			return llm.Config{}, mapped
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return llm.Config{}, llm.ErrConfigNotFound
		}
		return llm.Config{}, fmt.Errorf("postgres: update llm config: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return llm.Config{}, fmt.Errorf("postgres: commit update llm config: %w", err)
	}
	return cfg, nil
}

// Delete removes a configuration.
func (r *ConfigRepository) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM llm_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete llm config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return llm.ErrConfigNotFound
	}
	return nil
}

// Get returns one decrypted configuration by ID.
func (r *ConfigRepository) Get(ctx context.Context, id int64) (llm.Config, error) {
	cfg, err := r.scanConfig(r.pool.QueryRow(ctx,
		`SELECT `+configColumns+` FROM llm_configs WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return llm.Config{}, llm.ErrConfigNotFound
		}
		return llm.Config{}, fmt.Errorf("postgres: get llm config: %w", err)
	}
	return cfg, nil
}

// List returns every decrypted configuration ordered by ID.
func (r *ConfigRepository) List(ctx context.Context) ([]llm.Config, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+configColumns+` FROM llm_configs ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list llm configs: %w", err)
	}
	defer rows.Close()

	configs := make([]llm.Config, 0)
	for rows.Next() {
		cfg, err := r.scanConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan llm config: %w", err)
		}
		configs = append(configs, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list llm configs: %w", err)
	}
	return configs, nil
}

// GetDefault returns the default configuration or ErrConfigNotFound.
func (r *ConfigRepository) GetDefault(ctx context.Context) (llm.Config, error) {
	cfg, err := r.scanConfig(r.pool.QueryRow(ctx,
		`SELECT `+configColumns+` FROM llm_configs WHERE is_default ORDER BY id LIMIT 1`))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return llm.Config{}, llm.ErrConfigNotFound
		}
		return llm.Config{}, fmt.Errorf("postgres: get default llm config: %w", err)
	}
	return cfg, nil
}

// mapUniqueViolation translates the llm_configs table's unique violations into
// domain errors. It returns nil when err is not a unique violation.
func mapUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return nil
	}
	switch pgErr.ConstraintName {
	case "llm_configs_name_key":
		return llm.ErrNameTaken
	case "llm_configs_one_default":
		return llm.ErrDefaultConflict
	default:
		return nil
	}
}
