package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// authFlowColumnList is the shared projection and the scan order scanAuthFlow
// expects.
const authFlowColumnList = `id, system_id, name, kind, definition, enabled, created_at, updated_at`

// AuthFlowRepository implements the login recipe persistence port on a pgx
// pool. It mints the Snowflake id for every new row through its IDGenerator.
type AuthFlowRepository struct {
	pool *pgxpool.Pool
	ids  domain.IDGenerator
}

// NewAuthFlowRepository returns an auth-flow repository backed by pool. ids
// mints the Snowflake primary keys (platform/id in production, a counter in
// tests).
func NewAuthFlowRepository(pool *pgxpool.Pool, ids domain.IDGenerator) *AuthFlowRepository {
	return &AuthFlowRepository{pool: pool, ids: ids}
}

var _ domain.AuthFlowRepository = (*AuthFlowRepository)(nil)

// scanAuthFlow scans one row in authFlowColumnList order and decodes the JSONB
// definition.
func scanAuthFlow(row rowScanner) (domain.AuthFlow, error) {
	var (
		flow       domain.AuthFlow
		definition []byte
	)
	if err := row.Scan(
		&flow.ID,
		&flow.SystemID,
		&flow.Name,
		&flow.Kind,
		&definition,
		&flow.Enabled,
		&flow.CreatedAt,
		&flow.UpdatedAt,
	); err != nil {
		return domain.AuthFlow{}, err
	}
	if err := json.Unmarshal(definition, &flow.Definition); err != nil {
		return domain.AuthFlow{}, fmt.Errorf("postgres: decode auth flow definition: %w", err)
	}
	return flow, nil
}

// CreateAuthFlow inserts a login recipe with a freshly minted Snowflake id. The
// full stored row (id and timestamps included) is written back to f; an
// unavailable plugin (nil generator) is a wiring error surfaced as such.
func (r *AuthFlowRepository) CreateAuthFlow(ctx context.Context, f *domain.AuthFlow) error {
	if r.ids == nil {
		return fmt.Errorf("postgres: create auth flow: id generator is not configured")
	}
	definition, err := encodeJSON(f.Definition)
	if err != nil {
		return fmt.Errorf("postgres: encode auth flow definition: %w", err)
	}
	const query = `INSERT INTO auth_flow (id, system_id, name, kind, definition, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + authFlowColumnList
	created, err := scanAuthFlow(r.pool.QueryRow(ctx, query,
		r.ids.Next(), f.SystemID, f.Name, f.Kind, definition, f.Enabled))
	if err != nil {
		return catalogWriteError(err, "an auth flow with this name already exists for the system", "create auth flow")
	}
	*f = created
	return nil
}

// GetAuthFlow returns one login recipe by id.
func (r *AuthFlowRepository) GetAuthFlow(ctx context.Context, id int64) (*domain.AuthFlow, error) {
	flow, err := scanAuthFlow(r.pool.QueryRow(ctx,
		`SELECT `+authFlowColumnList+` FROM auth_flow WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "auth flow", id, "get auth flow")
	}
	return &flow, nil
}

// ListAuthFlows returns one page ordered by id ascending. SystemID zero matches
// every system; more reports whether another row exists after the page.
func (r *AuthFlowRepository) ListAuthFlows(ctx context.Context, f domain.AuthFlowFilter) ([]domain.AuthFlow, bool, error) {
	if f.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list auth flows: limit must be positive")
	}
	const query = `SELECT ` + authFlowColumnList + `
		FROM auth_flow
		WHERE id > $1
		  AND ($2 = 0 OR system_id = $2)
		ORDER BY id ASC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, query, f.Page.AfterID, f.SystemID, f.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list auth flows: %w", err)
	}
	defer rows.Close()

	flows := make([]domain.AuthFlow, 0, f.Page.Limit+1)
	for rows.Next() {
		flow, err := scanAuthFlow(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan auth flow: %w", err)
		}
		flows = append(flows, flow)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list auth flows: %w", err)
	}

	more := len(flows) > f.Page.Limit
	if more {
		flows = flows[:f.Page.Limit]
	}
	return flows, more, nil
}

// UpdateAuthFlow writes the mutable recipe columns and refreshes the server
// timestamps on f. The system and id are immutable after creation.
func (r *AuthFlowRepository) UpdateAuthFlow(ctx context.Context, f *domain.AuthFlow) error {
	definition, err := encodeJSON(f.Definition)
	if err != nil {
		return fmt.Errorf("postgres: encode auth flow definition: %w", err)
	}
	const query = `UPDATE auth_flow
		SET name = $2, kind = $3, definition = $4, enabled = $5, updated_at = now()
		WHERE id = $1
		RETURNING created_at, updated_at`
	if err := r.pool.QueryRow(ctx, query,
		f.ID, f.Name, f.Kind, definition, f.Enabled,
	).Scan(&f.CreatedAt, &f.UpdatedAt); err != nil {
		return catalogWriteBackError(err, "auth flow", f.ID,
			"an auth flow with this name already exists for the system", "update auth flow")
	}
	return nil
}

// DeleteAuthFlow removes one login recipe. Accesses that referenced it fall
// back to NULL through ON DELETE SET NULL.
func (r *AuthFlowRepository) DeleteAuthFlow(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM auth_flow WHERE id = $1`, id)
	if err != nil {
		return catalogWriteError(err, "duplicate key", "delete auth flow")
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("auth flow", id)
	}
	return nil
}
