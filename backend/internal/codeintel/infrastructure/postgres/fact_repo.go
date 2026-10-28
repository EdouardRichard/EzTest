package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// FactVersionRepo implements domain.FactVersionRepository on a pgx pool.
type FactVersionRepo struct {
	pool *pgxpool.Pool
}

// NewFactVersionRepository returns a fact-version repository backed by pg.
func NewFactVersionRepository(pg *pgxpool.Pool) *FactVersionRepo {
	return &FactVersionRepo{pool: pg}
}

var _ domain.FactVersionRepository = (*FactVersionRepo)(nil)

// Current returns the current entity snapshot of a fact together with its
// current version. A missing entity is a not-found error.
func (r *FactVersionRepo) Current(ctx context.Context, repoID int64, commit, factKey string) (*domain.CodeEntity, int, error) {
	entity, err := scanEntity(r.pool.QueryRow(ctx,
		`SELECT `+entityColumnList+`
		 FROM code_entity
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3`,
		repoID, commit, factKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, domain.NewNotFound("fact_not_found",
				fmt.Sprintf("codeintel: current fact %q not found at commit %s", factKey, commit))
		}
		return nil, 0, fmt.Errorf("postgres: get current fact: %w", err)
	}
	if entity.FactVersion == nil {
		return nil, 0, domain.NewNotFound("fact_not_found",
			fmt.Sprintf("codeintel: current fact %q has no version at commit %s", factKey, commit))
	}
	return &entity, *entity.FactVersion, nil
}

// GetVersion returns the canonical body stored for one fact version. The stored
// body is what the analysis service compares against a newly computed body
// before deciding whether the current version can be reused or must be bumped
// (design spec §6.4). A version without a stored body is a not-found error.
func (r *FactVersionRepo) GetVersion(ctx context.Context, repoID int64, commit, factKey string, version int) ([]byte, error) {
	var body []byte
	err := r.pool.QueryRow(ctx,
		`SELECT body FROM code_fact_version
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3 AND version = $4`,
		repoID, commit, factKey, version).Scan(&body)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("fact_not_found",
				fmt.Sprintf("codeintel: fact %q version %d not found at commit %s", factKey, version, commit))
		}
		return nil, fmt.Errorf("postgres: get fact version body: %w", err)
	}
	return body, nil
}

// Create appends one fact body version under the caller-supplied Snowflake id.
// Versions are append-only: callers bump the version instead of updating an
// existing row (design spec §6.4). A zero id is rejected with
// ErrInvalid/missing_id because this package never mints ids (D29).
func (r *FactVersionRepo) Create(ctx context.Context, id int64, repoID int64, commit, factKey, model, prompt string, version int, body []byte, analysisID int64) error {
	if id == 0 {
		return missingIDError("fact version")
	}
	const query = `INSERT INTO code_fact_version
		(id, repo_id, commit_sha, fact_key, version, model, prompt_version, body, analysis_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)`
	if _, err := r.pool.Exec(ctx, query,
		id, repoID, commit, factKey, version, model, prompt,
		jsonParam(body, jsonFallbackEmptyObject), nullableID(analysisID),
	); err != nil {
		if isUniqueViolation(err) {
			return domain.NewConflict("duplicate_key",
				fmt.Sprintf("codeintel: fact %q version %d already exists", factKey, version))
		}
		return fmt.Errorf("postgres: create fact version: %w", err)
	}
	return nil
}

// Supersede marks every version of a fact older than before as superseded at
// at. The current version is never superseded, and an already superseded
// version keeps its original timestamp.
func (r *FactVersionRepo) Supersede(ctx context.Context, repoID int64, commit, factKey string, before int, at time.Time) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE code_fact_version
		 SET superseded_at = $5
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3
		   AND version < $4 AND superseded_at IS NULL`,
		repoID, commit, factKey, before, at); err != nil {
		return fmt.Errorf("postgres: supersede fact versions: %w", err)
	}
	return nil
}

// nullableID renders an optional logical reference as a SQL parameter: the zero
// id means "no reference".
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
