package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// runColumnList is the shared column order of every case_design_run query and
// the order scanRun expects.
const runColumnList = `id, version_id, kind, status, agent_session_id, agent_run_id,
	model, prompt_version, input, result, stats, errors, created_by,
	created_at, updated_at, started_at, finished_at`

// activeRunStatuses is the single-flight status set of
// uk_case_design_run_active (spec §6.6).
const activeRunStatuses = `('queued', 'running')`

// staleRunErrors is the errors document written by RecoverStale: the run was
// abandoned by a worker restart (spec §6.6). It only fills an empty document; a
// reason already recorded by the failed run is kept.
const staleRunErrors = `{"reason":"worker_restart"}`

// RunRepository implements application.RunRepository on a pgx querier.
type RunRepository struct {
	q querier
}

// NewRunRepository returns a design-run repository backed by pool.
func NewRunRepository(pool *pgxpool.Pool) application.RunRepository {
	return newRunRepository(pool)
}

// newRunRepository binds the repository to q.
func newRunRepository(q querier) *RunRepository {
	return &RunRepository{q: q}
}

var _ application.RunRepository = (*RunRepository)(nil)

// runNotFoundForVersion is the version-scoped not-found of the by-kind
// lookups: the caller addresses the run by version and kind, not by run id.
func runNotFoundForVersion(versionID int64, kind string) error {
	return &domain.Error{
		Kind:    domain.KindNotFound,
		Reason:  "run_not_found",
		Message: fmt.Sprintf("version %d has no %s run", versionID, kind),
	}
}

// scanRun scans one row in runColumnList order.
func scanRun(row rowScanner) (domain.CaseDesignRun, error) {
	var (
		run       domain.CaseDesignRun
		input     []byte
		result    []byte
		stats     []byte
		runErrors []byte
	)
	if err := row.Scan(
		&run.ID,
		&run.VersionID,
		&run.Kind,
		&run.Status,
		&run.AgentSessionID,
		&run.AgentRunID,
		&run.Model,
		&run.PromptVersion,
		&input,
		&result,
		&stats,
		&runErrors,
		&run.CreatedBy,
		&run.CreatedAt,
		&run.UpdatedAt,
		&run.StartedAt,
		&run.FinishedAt,
	); err != nil {
		return domain.CaseDesignRun{}, err
	}
	run.Input = input
	run.Result = result
	run.Stats = stats
	run.Errors = runErrors
	return run, nil
}

// Create inserts r with its explicit id and the caller-supplied timestamps. A
// second queued/running run of the same (version, kind) violates
// uk_case_design_run_active and is a run_in_progress conflict (spec §6.6).
func (r *RunRepository) Create(ctx context.Context, run domain.CaseDesignRun) (domain.CaseDesignRun, error) {
	const query = `INSERT INTO case_design_run
		(id, version_id, kind, status, agent_session_id, agent_run_id, model,
		 prompt_version, input, result, stats, errors, created_by, created_at,
		 updated_at, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb,
		        $11::jsonb, $12::jsonb, $13, $14, $15, $16, $17)
		RETURNING ` + runColumnList
	created, err := scanRun(r.q.QueryRow(ctx, query,
		run.ID, run.VersionID, run.Kind, run.Status,
		nullableInt(run.AgentSessionID), nullableInt(run.AgentRunID), run.Model,
		run.PromptVersion, jsonParam(run.Input), jsonParam(run.Result),
		jsonParam(run.Stats), jsonParam(run.Errors), run.CreatedBy,
		run.CreatedAt, run.UpdatedAt, nullableTime(run.StartedAt),
		nullableTime(run.FinishedAt),
	))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_case_design_run":
			return domain.CaseDesignRun{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("run %d already exists", run.ID))
		case "uk_case_design_run_active":
			return domain.CaseDesignRun{}, domain.NewConflict("run_in_progress",
				fmt.Sprintf("a %s run of version %d is already queued or running", run.Kind, run.VersionID))
		}
		return domain.CaseDesignRun{}, fmt.Errorf("postgres: create case design run: %w", err)
	}
	return created, nil
}

// Get returns one run of versionID.
func (r *RunRepository) Get(ctx context.Context, versionID, runID int64) (domain.CaseDesignRun, error) {
	run, err := scanRun(r.q.QueryRow(ctx,
		`SELECT `+runColumnList+` FROM case_design_run WHERE id = $1 AND version_id = $2`,
		runID, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseDesignRun{}, notFound("run", runID)
		}
		return domain.CaseDesignRun{}, fmt.Errorf("postgres: get case design run: %w", err)
	}
	return run, nil
}

// List returns one page ordered by created_at DESC, id DESC with an opaque
// keyset cursor. Empty kind and status match everything.
func (r *RunRepository) List(ctx context.Context, versionID int64, kind, status string, limit int, cursor string) ([]domain.CaseDesignRun, string, bool, error) {
	if limit <= 0 {
		return nil, "", false, fmt.Errorf("postgres: list case design runs: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case design runs: %w", err)
	}
	var (
		cursorCreated time.Time
		hasCursor     bool
	)
	if cursorID != 0 {
		if err := r.q.QueryRow(ctx,
			`SELECT created_at FROM case_design_run WHERE id = $1 AND version_id = $2`,
			cursorID, versionID).Scan(&cursorCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []domain.CaseDesignRun{}, "", false, nil
			}
			return nil, "", false, fmt.Errorf("postgres: list case design runs: read cursor: %w", err)
		}
		hasCursor = true
	}

	const query = `SELECT ` + runColumnList + `
		FROM case_design_run
		WHERE version_id = $1
		  AND ($2 = '' OR kind = $2)
		  AND ($3 = '' OR status = $3)
		  AND ($4::boolean = false OR (created_at, id) < ($5::timestamptz, $6::bigint))
		ORDER BY created_at DESC, id DESC
		LIMIT $7`
	rows, err := r.q.Query(ctx, query, versionID, kind, status, hasCursor, cursorCreated, cursorID, limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case design runs: %w", err)
	}
	defer rows.Close()

	runs := make([]domain.CaseDesignRun, 0, limit+1)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, "", false, fmt.Errorf("postgres: scan case design run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case design runs: %w", err)
	}

	more := len(runs) > limit
	nextCursor := ""
	if more {
		runs = runs[:limit]
		nextCursor = platform.EncodeCursor(runs[len(runs)-1].ID)
	}
	return runs, nextCursor, more, nil
}

// UpdateStatus is a status compare-and-swap: the patch is applied only when the
// stored status equals from, and the updated row is returned. A missing run is
// run_not_found; a status that already moved on is a run_state_conflict for the
// worker to log.
func (r *RunRepository) UpdateStatus(ctx context.Context, runID int64, from, to string, patch application.RunPatch) (domain.CaseDesignRun, error) {
	const query = `UPDATE case_design_run
		SET status = $3,
		    agent_session_id = COALESCE($4, agent_session_id),
		    agent_run_id = COALESCE($5, agent_run_id),
		    model = COALESCE($6, model),
		    prompt_version = COALESCE($7, prompt_version),
		    input = COALESCE($8::jsonb, input),
		    result = COALESCE($9::jsonb, result),
		    stats = COALESCE($10::jsonb, stats),
		    errors = COALESCE($11::jsonb, errors),
		    started_at = COALESCE($12, started_at),
		    finished_at = COALESCE($13, finished_at),
		    updated_at = now()
		WHERE id = $1 AND status = $2
		RETURNING ` + runColumnList
	updated, err := scanRun(r.q.QueryRow(ctx, query,
		runID, from, to,
		nullableInt(patch.AgentSessionID), nullableInt(patch.AgentRunID),
		nullableString(patch.Model), nullableString(patch.PromptVersion),
		jsonParam(patch.Input), jsonParam(patch.Result), jsonParam(patch.Stats),
		jsonParam(patch.Errors), nullableTime(patch.StartedAt),
		nullableTime(patch.FinishedAt),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseDesignRun{}, r.updateStatusConflict(ctx, runID, from)
		}
		return domain.CaseDesignRun{}, fmt.Errorf("postgres: update case design run status: %w", err)
	}
	return updated, nil
}

// updateStatusConflict classifies a zero-row run status CAS.
func (r *RunRepository) updateStatusConflict(ctx context.Context, runID int64, from string) error {
	var status string
	if err := r.q.QueryRow(ctx,
		`SELECT status FROM case_design_run WHERE id = $1`, runID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("run", runID)
		}
		return fmt.Errorf("postgres: read case design run status after CAS: %w", err)
	}
	return domain.NewStateConflict("run_state_conflict",
		fmt.Sprintf("run %d is %s, not %s", runID, status, from))
}

// LatestByKind returns the newest run of one kind of the version.
func (r *RunRepository) LatestByKind(ctx context.Context, versionID int64, kind string) (domain.CaseDesignRun, error) {
	run, err := scanRun(r.q.QueryRow(ctx,
		`SELECT `+runColumnList+`
		 FROM case_design_run
		 WHERE version_id = $1 AND kind = $2
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, versionID, kind))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseDesignRun{}, runNotFoundForVersion(versionID, kind)
		}
		return domain.CaseDesignRun{}, fmt.Errorf("postgres: get latest case design run: %w", err)
	}
	return run, nil
}

// ActiveByKind returns the queued/running run of one kind of the version. The
// partial unique index guarantees at most one.
func (r *RunRepository) ActiveByKind(ctx context.Context, versionID int64, kind string) (domain.CaseDesignRun, error) {
	run, err := scanRun(r.q.QueryRow(ctx,
		`SELECT `+runColumnList+`
		 FROM case_design_run
		 WHERE version_id = $1 AND kind = $2 AND status IN `+activeRunStatuses+`
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, versionID, kind))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseDesignRun{}, runNotFoundForVersion(versionID, kind)
		}
		return domain.CaseDesignRun{}, fmt.Errorf("postgres: get active case design run: %w", err)
	}
	return run, nil
}

// ListActive returns the queued/running runs across versions, oldest first, so
// the run worker polls agent sessions in start order (spec §6.6).
func (r *RunRepository) ListActive(ctx context.Context, limit int) ([]domain.CaseDesignRun, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("postgres: list active case design runs: limit must be positive")
	}
	rows, err := r.q.Query(ctx,
		`SELECT `+runColumnList+`
		 FROM case_design_run
		 WHERE status IN `+activeRunStatuses+`
		 ORDER BY created_at ASC, id ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active case design runs: %w", err)
	}
	defer rows.Close()

	runs := make([]domain.CaseDesignRun, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan case design run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list active case design runs: %w", err)
	}
	return runs, nil
}

// RecoverStale marks queued/running runs last updated before the cutoff as
// failed (worker restart, spec §6.6) and returns the number recovered. An
// already-recorded reason is kept; an empty errors document gets
// reason=worker_restart.
func (r *RunRepository) RecoverStale(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `
		UPDATE case_design_run
		SET status = 'failed',
		    finished_at = COALESCE(finished_at, now()),
		    errors = COALESCE(errors, $2::jsonb),
		    updated_at = now()
		WHERE status IN `+activeRunStatuses+` AND updated_at < $1`, before, staleRunErrors)
	if err != nil {
		return 0, fmt.Errorf("postgres: recover stale case design runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
