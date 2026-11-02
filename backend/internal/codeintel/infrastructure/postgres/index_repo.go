package codeintelpg

import (
	"context"
	"errors"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IndexRunRepository stores code_index_run rows.
type IndexRunRepository struct {
	pool *pgxpool.Pool
}

// NewIndexRunRepository wires the index-run repository.
func NewIndexRunRepository(pool *pgxpool.Pool) *IndexRunRepository {
	return &IndexRunRepository{pool: pool}
}

var _ codeintelapp.IndexRepository = (*IndexRunRepository)(nil)

// CreateRun inserts one queued/running index run.
func (r *IndexRunRepository) CreateRun(ctx context.Context, run codeintel.IndexRun) (codeintel.IndexRun, error) {
	stored, err := scanIndexRun(r.pool.QueryRow(ctx, `
		INSERT INTO code_index_run (id, version_id, repo_id, commit_sha, branch, status,
			files_total, files_indexed, truncated, stats, errors, started_at, finished_at,
			created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::jsonb, $12, $13, $14, $15, $16)
		RETURNING `+indexRunColumns,
		run.ID, nullableInt64(versionIDPtr(run.VersionID)), run.RepoID, run.CommitSHA, run.Branch, run.Status,
		run.FilesTotal, run.FilesIndexed, run.Truncated,
		jsonText(run.Stats, `{}`), jsonText(run.Errors, `[]`),
		run.StartedAt, run.FinishedAt, run.CreatedBy, run.CreatedAt, run.UpdatedAt))
	if err != nil {
		return codeintel.IndexRun{}, wrapDB("create code index run", err)
	}
	return stored, nil
}

// GetRun loads one run.
func (r *IndexRunRepository) GetRun(ctx context.Context, id int64) (codeintel.IndexRun, error) {
	run, err := scanIndexRun(r.pool.QueryRow(ctx, `
		SELECT `+indexRunColumns+` FROM code_index_run WHERE id = $1
	`, id))
	if err != nil {
		return codeintel.IndexRun{}, wrapDB("get code index run", err)
	}
	return run, nil
}

// LatestRun returns the newest run of a repository.
func (r *IndexRunRepository) LatestRun(ctx context.Context, repoID int64) (codeintel.IndexRun, error) {
	run, err := scanIndexRun(r.pool.QueryRow(ctx, `
		SELECT `+indexRunColumns+` FROM code_index_run
		WHERE repo_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, repoID))
	if err != nil {
		return codeintel.IndexRun{}, wrapDB("latest code index run", err)
	}
	return run, nil
}

// UpdateRun writes the mutable status/counter/timestamp columns.
func (r *IndexRunRepository) UpdateRun(ctx context.Context, run codeintel.IndexRun) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE code_index_run
		SET status = $2, files_total = $3, files_indexed = $4, truncated = $5,
		    stats = $6::jsonb, errors = $7::jsonb, started_at = $8, finished_at = $9,
		    updated_at = $10
		WHERE id = $1
	`, run.ID, run.Status, run.FilesTotal, run.FilesIndexed, run.Truncated,
		jsonText(run.Stats, `{}`), jsonText(run.Errors, `[]`),
		run.StartedAt, run.FinishedAt, run.UpdatedAt)
	if err != nil {
		return wrapDB("update code index run", err)
	}
	if tag.RowsAffected() == 0 {
		return codeintel.ErrNotFound
	}
	return nil
}

// ActiveRun returns the queued/running run of a repository when present.
func (r *IndexRunRepository) ActiveRun(ctx context.Context, repoID int64) (codeintel.IndexRun, bool, error) {
	run, err := scanIndexRun(r.pool.QueryRow(ctx, `
		SELECT `+indexRunColumns+` FROM code_index_run
		WHERE repo_id = $1 AND status IN ('queued', 'running')
		ORDER BY id DESC
		LIMIT 1
	`, repoID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return codeintel.IndexRun{}, false, nil
		}
		return codeintel.IndexRun{}, false, wrapDB("active code index run", err)
	}
	return run, true, nil
}

// DoneRunByCommit returns a completed run for the same repo+commit, backing the
// 200-reused idempotency rule.
func (r *IndexRunRepository) DoneRunByCommit(ctx context.Context, repoID int64, commitSHA string) (codeintel.IndexRun, bool, error) {
	run, err := scanIndexRun(r.pool.QueryRow(ctx, `
		SELECT `+indexRunColumns+` FROM code_index_run
		WHERE repo_id = $1 AND commit_sha = $2 AND status = 'done'
		ORDER BY id DESC
		LIMIT 1
	`, repoID, commitSHA))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return codeintel.IndexRun{}, false, nil
		}
		return codeintel.IndexRun{}, false, wrapDB("done code index run", err)
	}
	return run, true, nil
}

func versionIDPtr(versionID int64) *int64 {
	if versionID <= 0 {
		return nil
	}
	value := versionID
	return &value
}
