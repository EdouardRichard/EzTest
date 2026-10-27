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

// indexRunColumnList is the shared column order of every index-run query and
// the order scanIndexRun expects.
const indexRunColumnList = `id, repo_id, commit_sha, source, status, stats, errors,
	started_at, finished_at, created_by, created_at`

// scanIndexRun scans one row in indexRunColumnList order and decodes the JSONB
// columns.
func scanIndexRun(row rowScanner) (domain.IndexRun, error) {
	var (
		run    domain.IndexRun
		stats  []byte
		errors []byte
	)
	if err := row.Scan(
		&run.ID,
		&run.RepoID,
		&run.CommitSHA,
		&run.Source,
		&run.Status,
		&stats,
		&errors,
		&run.StartedAt,
		&run.FinishedAt,
		&run.CreatedBy,
		&run.CreatedAt,
	); err != nil {
		return domain.IndexRun{}, err
	}
	decodedErrors, err := decodeAnalysisErrors(errors)
	if err != nil {
		return domain.IndexRun{}, fmt.Errorf("decode index run errors: %w", err)
	}
	run.Stats = stats
	run.Errors = decodedErrors
	return run, nil
}

// IndexRunRepo implements domain.IndexRunRepository on a pgx pool.
type IndexRunRepo struct {
	pool *pgxpool.Pool
}

// NewIndexRunRepository returns an index-run repository backed by pg.
func NewIndexRunRepository(pg *pgxpool.Pool) *IndexRunRepo {
	return &IndexRunRepo{pool: pg}
}

var _ domain.IndexRunRepository = (*IndexRunRepo)(nil)

// Create inserts one queued t0 index run. An empty source/status falls back to
// the DDL defaults so callers may omit them.
func (r *IndexRunRepo) Create(ctx context.Context, run domain.IndexRun) error {
	source := run.Source
	if source == "" {
		source = "t0"
	}
	status := run.Status
	if status == "" {
		status = domain.RunQueued
	}
	encodedErrors, err := marshalAnalysisErrors(run.Errors)
	if err != nil {
		return fmt.Errorf("postgres: encode index run errors: %w", err)
	}
	const query = `INSERT INTO code_index_runs
		(id, repo_id, commit_sha, source, status, stats, errors, started_at, finished_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10)`
	if _, err := r.pool.Exec(ctx, query,
		run.ID, run.RepoID, run.CommitSHA, source, status,
		jsonParam(run.Stats, jsonFallbackEmptyObject), encodedErrors,
		run.StartedAt, run.FinishedAt, run.CreatedBy,
	); err != nil {
		if isUniqueViolation(err) {
			return domain.NewConflict("duplicate_key",
				fmt.Sprintf("codeintel: index run %d already exists", run.ID))
		}
		return fmt.Errorf("postgres: create index run: %w", err)
	}
	return nil
}

// Update rewrites the mutable state of one run: status, stats, errors and the
// start/finish timestamps. The run identity (repo, commit, source, creator) is
// immutable.
func (r *IndexRunRepo) Update(ctx context.Context, run domain.IndexRun) error {
	encodedErrors, err := marshalAnalysisErrors(run.Errors)
	if err != nil {
		return fmt.Errorf("postgres: encode index run errors: %w", err)
	}
	const query = `UPDATE code_index_runs
		SET status = $3, stats = $4::jsonb, errors = $5::jsonb,
		    started_at = $6, finished_at = $7
		WHERE id = $1 AND repo_id = $2`
	tag, err := r.pool.Exec(ctx, query,
		run.ID, run.RepoID, run.Status,
		jsonParam(run.Stats, jsonFallbackEmptyObject), encodedErrors,
		run.StartedAt, run.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: update index run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundError("index_run", run.ID)
	}
	return nil
}

// GetByID returns one run of repoID.
func (r *IndexRunRepo) GetByID(ctx context.Context, repoID, id int64) (domain.IndexRun, error) {
	run, err := scanIndexRun(r.pool.QueryRow(ctx,
		`SELECT `+indexRunColumnList+` FROM code_index_runs WHERE id = $1 AND repo_id = $2`,
		id, repoID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.IndexRun{}, notFoundError("index_run", id)
		}
		return domain.IndexRun{}, fmt.Errorf("postgres: get index run: %w", err)
	}
	return run, nil
}

// GetLatest returns the newest run of repoID. The id breaks created_at ties so
// the result is deterministic.
func (r *IndexRunRepo) GetLatest(ctx context.Context, repoID int64) (domain.IndexRun, error) {
	run, err := scanIndexRun(r.pool.QueryRow(ctx,
		`SELECT `+indexRunColumnList+`
		 FROM code_index_runs
		 WHERE repo_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, repoID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.IndexRun{}, domain.NewNotFound("index_run",
				fmt.Sprintf("codeintel: repository %d has no index run", repoID))
		}
		return domain.IndexRun{}, fmt.Errorf("postgres: get latest index run: %w", err)
	}
	return run, nil
}

// RecoverStale marks runs that a previous process left queued or running before
// before as failed(interrupted), the startup recovery rule of design spec
// §6.8. A queued run is aged by created_at, a running one by started_at (with
// created_at as fallback). It returns the number of recovered runs.
func (r *IndexRunRepo) RecoverStale(ctx context.Context, before time.Time) (int64, error) {
	const query = `UPDATE code_index_runs
		SET status = 'failed',
		    finished_at = now(),
		    errors = errors || '[{"code":"interrupted"}]'::jsonb
		WHERE status IN ('queued', 'running')
		  AND ((status = 'queued' AND created_at < $1)
		    OR (status = 'running' AND COALESCE(started_at, created_at) < $1))`
	tag, err := r.pool.Exec(ctx, query, before)
	if err != nil {
		return 0, fmt.Errorf("postgres: recover stale index runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
