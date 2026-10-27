package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// profileColumns is the column list shared by every profile query and the row
// order scanProfile expects.
const profileColumns = `id, key, name, description, builtin, enabled, system_prompt, model, allowed_tools, max_turns, run_seconds, hard_timeout_seconds, thinking, reasoning_effort, max_output_tokens, created_at, updated_at`

// ProfileRepository implements domain.ProfileRepository on a pgx pool.
type ProfileRepository struct {
	pool *pgxpool.Pool
}

// NewProfileRepository returns a profile repository backed by pool.
func NewProfileRepository(pool *pgxpool.Pool) *ProfileRepository {
	return &ProfileRepository{pool: pool}
}

// scanProfile scans one row in profileColumns order.
func scanProfile(row rowScanner) (agent.AgentProfile, error) {
	var profile agent.AgentProfile
	if err := row.Scan(
		&profile.ID,
		&profile.Key,
		&profile.Name,
		&profile.Description,
		&profile.Builtin,
		&profile.Enabled,
		&profile.SystemPrompt,
		&profile.Model,
		&profile.AllowedTools,
		&profile.MaxTurns,
		&profile.RunSeconds,
		&profile.HardTimeoutSeconds,
		&profile.Thinking,
		&profile.ReasoningEffort,
		&profile.MaxOutputTokens,
		&profile.CreatedAt,
		&profile.UpdatedAt,
	); err != nil {
		return agent.AgentProfile{}, err
	}
	if profile.AllowedTools == nil {
		profile.AllowedTools = []string{}
	}
	return profile, nil
}

// List returns every profile ordered by ID ascending.
func (r *ProfileRepository) List(ctx context.Context) ([]agent.AgentProfile, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+profileColumns+` FROM agent_profiles ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list agent profiles: %w", err)
	}
	defer rows.Close()

	profiles := make([]agent.AgentProfile, 0)
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan agent profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list agent profiles: %w", err)
	}
	return profiles, nil
}

// Get returns one profile by ID.
func (r *ProfileRepository) Get(ctx context.Context, id int64) (agent.AgentProfile, error) {
	profile, err := scanProfile(r.pool.QueryRow(ctx,
		`SELECT `+profileColumns+` FROM agent_profiles WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.AgentProfile{}, agent.ErrNotFound
		}
		return agent.AgentProfile{}, fmt.Errorf("postgres: get agent profile: %w", err)
	}
	return profile, nil
}

// Update loads the profile FOR UPDATE, merges and validates the partial
// update, then writes every mutable column. Loading first means a concurrent
// PATCH cannot be lost for fields the request did not mention. A failed
// validation rolls back without writing anything.
func (r *ProfileRepository) Update(ctx context.Context, id int64, params agent.UpdateParams) (agent.AgentProfile, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.AgentProfile{}, fmt.Errorf("postgres: begin update agent profile: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanProfile(tx.QueryRow(ctx,
		`SELECT `+profileColumns+` FROM agent_profiles WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.AgentProfile{}, agent.ErrNotFound
		}
		return agent.AgentProfile{}, fmt.Errorf("postgres: load agent profile for update: %w", err)
	}
	if err := existing.Apply(params); err != nil {
		return agent.AgentProfile{}, err
	}
	allowedTools := existing.AllowedTools
	if allowedTools == nil {
		allowedTools = []string{}
	}

	const query = `UPDATE agent_profiles SET
		system_prompt = $2, model = $3, allowed_tools = $4, max_turns = $5,
		run_seconds = $6, hard_timeout_seconds = $7, thinking = $8,
		reasoning_effort = $9, max_output_tokens = $10, enabled = $11,
		updated_at = now()
		WHERE id = $1
		RETURNING ` + profileColumns
	profile, err := scanProfile(tx.QueryRow(ctx, query,
		id,
		existing.SystemPrompt,
		existing.Model,
		allowedTools,
		existing.MaxTurns,
		existing.RunSeconds,
		existing.HardTimeoutSeconds,
		existing.Thinking,
		existing.ReasoningEffort,
		existing.MaxOutputTokens,
		existing.Enabled,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.AgentProfile{}, agent.ErrNotFound
		}
		return agent.AgentProfile{}, fmt.Errorf("postgres: update agent profile: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.AgentProfile{}, fmt.Errorf("postgres: commit update agent profile: %w", err)
	}
	return profile, nil
}

// EnsureBuiltin inserts the builtin profile when its key is absent. ON
// CONFLICT (key) DO NOTHING keeps the first seeded content: a later startup
// never overwrites an edited profile.
func (r *ProfileRepository) EnsureBuiltin(ctx context.Context, profile agent.AgentProfile) (bool, error) {
	allowedTools := profile.AllowedTools
	if allowedTools == nil {
		allowedTools = []string{}
	}
	const query = `INSERT INTO agent_profiles
		(key, name, description, builtin, enabled, system_prompt, model, allowed_tools,
		 max_turns, run_seconds, hard_timeout_seconds, thinking, reasoning_effort, max_output_tokens)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (key) DO NOTHING`
	tag, err := r.pool.Exec(ctx, query,
		profile.Key,
		profile.Name,
		profile.Description,
		profile.Builtin,
		profile.Enabled,
		profile.SystemPrompt,
		profile.Model,
		allowedTools,
		profile.MaxTurns,
		profile.RunSeconds,
		profile.HardTimeoutSeconds,
		profile.Thinking,
		profile.ReasoningEffort,
		profile.MaxOutputTokens,
	)
	if err != nil {
		return false, fmt.Errorf("postgres: ensure builtin agent profile: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
