package agentpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const skillColumns = `id, name, description, body, metadata, version, enabled, created_at, updated_at`

const skillVersionColumns = `id, skill_id, version, name, description, body, metadata, enabled,
	source, actor_id, note, created_at`

// SkillRepository stores the runtime skill library: skill (current revision,
// soft-deletable) and skill_version (append-only history). The DB is the
// authoritative store; skills/ files only seed it once.
type SkillRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewSkillRepository wires the skill repository.
func NewSkillRepository(pool *pgxpool.Pool, ids platformid.Generator) *SkillRepository {
	return &SkillRepository{pool: pool, ids: ids}
}

// ListSkills returns every active skill ordered by name.
func (r *SkillRepository) ListSkills(ctx context.Context) ([]agent.Skill, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+skillColumns+` FROM skill WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()
	skills := make([]agent.Skill, 0)
	for rows.Next() {
		skill, err := scanSkill(rows)
		if err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		skills = append(skills, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	return skills, nil
}

// GetSkill loads one active skill by name, or agent.ErrNotFound.
func (r *SkillRepository) GetSkill(ctx context.Context, name string) (agent.Skill, error) {
	skill, err := scanSkill(r.pool.QueryRow(ctx, `
		SELECT `+skillColumns+` FROM skill WHERE name = $1 AND deleted_at IS NULL`, name))
	if err != nil {
		return agent.Skill{}, wrapDB("get skill", err)
	}
	return skill, nil
}

// CreateSkill inserts a new skill and its first admin revision in one
// transaction. A duplicate active name returns agent.ErrConflict; a name
// previously deleted starts a fresh aggregate at version 1.
func (r *SkillRepository) CreateSkill(ctx context.Context, skill agent.Skill, actorID *int64, note string) (agent.Skill, error) {
	if err := validateSkillForWrite(skill); err != nil {
		return agent.Skill{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.Skill{}, fmt.Errorf("begin skill create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stored, err := insertSkill(ctx, tx, r.ids, skill)
	if err != nil {
		return agent.Skill{}, err
	}
	if err := insertSkillVersion(ctx, tx, r.ids, stored, agent.SkillSourceAdmin, actorID, note); err != nil {
		return agent.Skill{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.Skill{}, fmt.Errorf("commit skill create: %w", err)
	}
	return stored, nil
}

// EnsureSkill seeds one package only when no row with that name has ever
// existed — including soft-deleted rows, so a restart never resurrects an
// administrator's deletion and never overwrites an edit.
func (r *SkillRepository) EnsureSkill(ctx context.Context, seed agent.Skill) (agent.Skill, bool, error) {
	if err := validateSkillForWrite(seed); err != nil {
		return agent.Skill{}, false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.Skill{}, false, fmt.Errorf("begin skill seed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanSkill(tx.QueryRow(ctx, `
		SELECT `+skillColumns+` FROM skill WHERE name = $1`, seed.Name))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return agent.Skill{}, false, fmt.Errorf("commit skill seed no-op: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agent.Skill{}, false, wrapDB("lock skill seed", err)
	}
	stored, err := insertSkill(ctx, tx, r.ids, seed)
	if err != nil {
		if errors.Is(err, agent.ErrConflict) {
			// A concurrent writer (e.g. another instance seeding at startup)
			// inserted the name first: the seed is a no-op, not a failure.
			_ = tx.Rollback(ctx)
			existing, getErr := scanSkill(r.pool.QueryRow(ctx, `
				SELECT `+skillColumns+` FROM skill WHERE name = $1`, seed.Name))
			if getErr != nil {
				return agent.Skill{}, false, wrapDB("re-read skill seed", getErr)
			}
			return existing, false, nil
		}
		return agent.Skill{}, false, err
	}
	if err := insertSkillVersion(ctx, tx, r.ids, stored, agent.SkillSourceSeed, nil, ""); err != nil {
		return agent.Skill{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.Skill{}, false, fmt.Errorf("commit skill seed: %w", err)
	}
	return stored, true, nil
}

// UpdateSkill applies one partial edit under a row lock. A changed edit
// bumps the version and appends one admin revision in the same transaction;
// a no-op never appends and never bumps updated_at. ExpectedVersion mismatch
// returns agent.ErrSkillVersionConflict.
func (r *SkillRepository) UpdateSkill(ctx context.Context, name string, update agent.SkillUpdate, actorID *int64, note string) (agent.Skill, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.Skill{}, false, fmt.Errorf("begin skill update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanSkill(tx.QueryRow(ctx, `
		SELECT `+skillColumns+` FROM skill WHERE name = $1 AND deleted_at IS NULL FOR UPDATE`, name))
	if err != nil {
		return agent.Skill{}, false, wrapDB("lock skill", err)
	}
	if update.ExpectedVersion != nil && current.Version != *update.ExpectedVersion {
		return agent.Skill{}, false, agent.ErrSkillVersionConflict
	}

	next := current
	if update.Description != nil {
		next.Description = *update.Description
	}
	if update.Body != nil {
		next.Body = *update.Body
	}
	if update.Metadata != nil {
		next.Metadata = update.Metadata
	}
	if update.Enabled != nil {
		next.Enabled = *update.Enabled
	}
	if err := validateSkillForWrite(next); err != nil {
		return agent.Skill{}, false, err
	}

	changed := current.Description != next.Description ||
		current.Body != next.Body ||
		current.Enabled != next.Enabled ||
		!sameJSONValue(current.Metadata, next.Metadata)
	if !changed {
		if err := tx.Commit(ctx); err != nil {
			return agent.Skill{}, false, fmt.Errorf("commit skill no-op: %w", err)
		}
		return current, false, nil
	}

	encodedMetadata, err := encodeJSONObject("skill metadata", normalizeEmptyObject(next.Metadata))
	if err != nil {
		return agent.Skill{}, false, err
	}
	next.Version = current.Version + 1
	stored, err := scanSkill(tx.QueryRow(ctx, `
		UPDATE skill
		SET description = $2, body = $3, metadata = $4::jsonb, enabled = $5,
			version = $6, updated_at = now()
		WHERE id = $1
		RETURNING `+skillColumns,
		current.ID, next.Description, next.Body, encodedMetadata, next.Enabled, next.Version))
	if err != nil {
		return agent.Skill{}, false, wrapDB("update skill", err)
	}
	if err := insertSkillVersion(ctx, tx, r.ids, stored, agent.SkillSourceAdmin, actorID, note); err != nil {
		return agent.Skill{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.Skill{}, false, fmt.Errorf("commit skill update: %w", err)
	}
	return stored, true, nil
}

// DeleteSkill soft-deletes one active skill: the row is hidden from every
// read but its append-only version history is preserved for audit.
func (r *SkillRepository) DeleteSkill(ctx context.Context, name string) (agent.Skill, error) {
	deleted, err := scanSkill(r.pool.QueryRow(ctx, `
		UPDATE skill SET deleted_at = now(), updated_at = now()
		WHERE name = $1 AND deleted_at IS NULL
		RETURNING `+skillColumns, name))
	if err != nil {
		return agent.Skill{}, wrapDB("delete skill", err)
	}
	return deleted, nil
}

// ListSkillVersions returns one version DESC page of a skill's history.
func (r *SkillRepository) ListSkillVersions(ctx context.Context, name string, limit int, beforeVersion *int) ([]agent.SkillVersion, bool, error) {
	skill, err := r.GetSkill(ctx, name)
	if err != nil {
		return nil, false, err
	}
	args := []any{skill.ID}
	query := `SELECT ` + skillVersionColumns + ` FROM skill_version WHERE skill_id = $1`
	if beforeVersion != nil {
		args = append(args, *beforeVersion)
		query += fmt.Sprintf(" AND version < $%d", len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(" ORDER BY version DESC LIMIT $%d", len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list skill versions: %w", err)
	}
	defer rows.Close()
	versions := make([]agent.SkillVersion, 0, limit)
	for rows.Next() {
		version, err := scanSkillVersion(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan skill version: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list skill versions: %w", err)
	}
	hasMore := len(versions) > limit
	if hasMore {
		versions = versions[:limit]
	}
	return versions, hasMore, nil
}

/* -------------------------------- helpers -------------------------------- */

func validateSkillForWrite(skill agent.Skill) error {
	if err := agent.ValidateSkillName(skill.Name); err != nil {
		return err
	}
	if err := agent.ValidateSkillDescription(skill.Description); err != nil {
		return err
	}
	if err := agent.ValidateSkillBody(skill.Body); err != nil {
		return err
	}
	return agent.ValidateSkillMetadata(skill.Metadata)
}

func insertSkill(ctx context.Context, tx pgx.Tx, ids platformid.Generator, skill agent.Skill) (agent.Skill, error) {
	if skill.ID <= 0 {
		skill.ID = ids.Next()
	}
	encodedMetadata, err := encodeJSONObject("skill metadata", normalizeEmptyObject(skill.Metadata))
	if err != nil {
		return agent.Skill{}, err
	}
	stored, err := scanSkill(tx.QueryRow(ctx, `
		INSERT INTO skill (id, name, description, body, metadata, version, enabled)
		VALUES ($1, $2, $3, $4, $5::jsonb, 1, $6)
		RETURNING `+skillColumns,
		skill.ID, skill.Name, skill.Description, skill.Body, encodedMetadata, skill.Enabled))
	if err != nil {
		return agent.Skill{}, wrapDB("insert skill", err)
	}
	return stored, nil
}

func insertSkillVersion(ctx context.Context, tx pgx.Tx, ids platformid.Generator, skill agent.Skill, source string, actorID *int64, note string) error {
	encodedMetadata, err := encodeJSONObject("skill metadata", normalizeEmptyObject(skill.Metadata))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO skill_version (id, skill_id, version, name, description, body,
			metadata, enabled, source, actor_id, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11)`,
		ids.Next(), skill.ID, skill.Version, skill.Name, skill.Description, skill.Body,
		encodedMetadata, skill.Enabled, source, actorID, note)
	if err != nil {
		return wrapDB("insert skill version", err)
	}
	return nil
}

func scanSkill(row rowScanner) (agent.Skill, error) {
	var skill agent.Skill
	var metadata []byte
	err := row.Scan(&skill.ID, &skill.Name, &skill.Description, &skill.Body, &metadata,
		&skill.Version, &skill.Enabled, &skill.CreatedAt, &skill.UpdatedAt)
	if err != nil {
		return agent.Skill{}, err
	}
	skill.Metadata = json.RawMessage(metadata)
	return skill, nil
}

func scanSkillVersion(row rowScanner) (agent.SkillVersion, error) {
	var version agent.SkillVersion
	var metadata []byte
	err := row.Scan(&version.ID, &version.SkillID, &version.Version, &version.Name,
		&version.Description, &version.Body, &metadata, &version.Enabled,
		&version.Source, &version.ActorID, &version.Note, &version.CreatedAt)
	if err != nil {
		return agent.SkillVersion{}, err
	}
	version.Metadata = json.RawMessage(metadata)
	return version, nil
}

// sameJSONValue compares two JSON documents semantically (jsonb normalizes
// key order and whitespace, so a reordered object is not a change).
func sameJSONValue(a, b json.RawMessage) bool {
	var left, right any
	if err := json.Unmarshal(normalizeEmptyObject(a), &left); err != nil {
		return false
	}
	if err := json.Unmarshal(normalizeEmptyObject(b), &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func normalizeEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
