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

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// analysisColumnList is the shared column order of every analysis_run query and
// the order scanAnalysisRun expects.
const analysisColumnList = `id, version_id, batch_id, kind, status, agent_session_id,
	agent_run_id, model, prompt_version, input, result, stats, errors, created_by,
	created_at, updated_at, started_at, finished_at`

// analysisActiveStatuses is the single-flight status set of
// uk_analysis_run_active (spec §4.2.3).
const analysisActiveStatuses = `('queued', 'running')`

// analysisStaleErrors is the errors document written by boot recovery
// (spec §6.7/§5.6).
const analysisStaleErrors = `{"reason":"stale_run_recovered"}`

// analysisQuerier is the pgx surface the repository uses. Both *pgxpool.Pool
// and a transaction satisfy it.
type analysisQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// analysisRowScanner is the Scan surface shared by pgx.Row and pgx.Rows.
type analysisRowScanner interface {
	Scan(dest ...any) error
}

// AnalysisRepository implements application.AnalysisStore on a pgx querier.
type AnalysisRepository struct {
	q analysisQuerier
}

// NewAnalysisRepository returns the analysis-run repository backed by pool.
func NewAnalysisRepository(pool *pgxpool.Pool) application.AnalysisStore {
	return newAnalysisRepository(pool)
}

// newAnalysisRepository binds the repository to q.
func newAnalysisRepository(q analysisQuerier) *AnalysisRepository {
	return &AnalysisRepository{q: q}
}

var _ application.AnalysisStore = (*AnalysisRepository)(nil)

// scanAnalysisRun scans one row in analysisColumnList order. NULL JSONB columns
// become empty documents so a queued run needs no special handling.
func scanAnalysisRun(row analysisRowScanner) (domain.AnalysisRun, error) {
	var (
		run     domain.AnalysisRun
		input   []byte
		result  []byte
		stats   []byte
		failure []byte
	)
	if err := row.Scan(
		&run.ID,
		&run.VersionID,
		&run.BatchID,
		&run.Kind,
		&run.Status,
		&run.AgentSessionID,
		&run.AgentRunID,
		&run.Model,
		&run.PromptVersion,
		&input,
		&result,
		&stats,
		&failure,
		&run.CreatedBy,
		&run.CreatedAt,
		&run.UpdatedAt,
		&run.StartedAt,
		&run.FinishedAt,
	); err != nil {
		return domain.AnalysisRun{}, err
	}
	run.Input = input
	run.Result = result
	run.Stats = stats
	run.Errors = failure
	return run, nil
}

// Create inserts run with its explicit Snowflake id (D29). The partial unique
// index uk_analysis_run_active is mapped to the analysis_active conflict.
func (r *AnalysisRepository) Create(ctx context.Context, run domain.AnalysisRun) (domain.AnalysisRun, error) {
	const query = `INSERT INTO analysis_run
		(id, version_id, batch_id, kind, status, agent_session_id, agent_run_id,
		 model, prompt_version, input, result, stats, errors, created_by,
		 created_at, updated_at, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::jsonb,
		        $12::jsonb, $13::jsonb, $14, $15, $16, $17, $18)
		RETURNING ` + analysisColumnList
	created, err := scanAnalysisRun(r.q.QueryRow(ctx, query,
		run.ID, run.VersionID, run.BatchID, run.Kind, run.Status,
		analysisNullableInt(run.AgentSessionID), analysisNullableInt(run.AgentRunID),
		run.Model, run.PromptVersion, analysisJSONParam(run.Input),
		analysisJSONParam(run.Result), analysisJSONParam(run.Stats),
		analysisJSONParam(run.Errors), run.CreatedBy, run.CreatedAt, run.UpdatedAt,
		analysisNullableTime(run.StartedAt), analysisNullableTime(run.FinishedAt),
	))
	if err != nil {
		switch analysisConstraint(err) {
		case "pk_analysis_run":
			return domain.AnalysisRun{}, domain.NewAnalysisConflict("duplicate_key",
				fmt.Sprintf("analysis run %d already exists", run.ID))
		case "uk_analysis_run_active":
			return domain.AnalysisRun{}, domain.NewAnalysisConflict(domain.ReasonAnalysisActive,
				fmt.Sprintf("a %s analysis of batch %d is already queued or running", run.Kind, run.BatchID))
		}
		return domain.AnalysisRun{}, fmt.Errorf("postgres: create analysis run: %w", err)
	}
	return created, nil
}

// Get returns one analysis run scoped to its version and batch.
func (r *AnalysisRepository) Get(ctx context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error) {
	run, err := scanAnalysisRun(r.q.QueryRow(ctx,
		`SELECT `+analysisColumnList+`
		 FROM analysis_run
		 WHERE id = $1 AND version_id = $2 AND batch_id = $3`,
		analysisID, versionID, batchID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnalysisRun{}, application.ErrAnalysisRunNotFound
		}
		return domain.AnalysisRun{}, fmt.Errorf("postgres: get analysis run: %w", err)
	}
	return run, nil
}

// GetByID returns one analysis run by id (agent submission path).
func (r *AnalysisRepository) GetByID(ctx context.Context, analysisID int64) (domain.AnalysisRun, error) {
	run, err := scanAnalysisRun(r.q.QueryRow(ctx,
		`SELECT `+analysisColumnList+` FROM analysis_run WHERE id = $1`, analysisID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnalysisRun{}, application.ErrAnalysisRunNotFound
		}
		return domain.AnalysisRun{}, fmt.Errorf("postgres: get analysis run by id: %w", err)
	}
	return run, nil
}

// List returns the analysis runs of one batch, newest first.
func (r *AnalysisRepository) List(ctx context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error) {
	rows, err := r.q.Query(ctx,
		`SELECT `+analysisColumnList+`
		 FROM analysis_run
		 WHERE version_id = $1 AND batch_id = $2
		 ORDER BY created_at DESC, id DESC`, versionID, batchID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list analysis runs: %w", err)
	}
	defer rows.Close()

	runs := make([]domain.AnalysisRun, 0)
	for rows.Next() {
		run, err := scanAnalysisRun(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan analysis run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list analysis runs: %w", err)
	}
	return runs, nil
}

// Active returns the queued/running run of one (batch, kind).
func (r *AnalysisRepository) Active(ctx context.Context, batchID int64, kind string) (domain.AnalysisRun, error) {
	run, err := scanAnalysisRun(r.q.QueryRow(ctx,
		`SELECT `+analysisColumnList+`
		 FROM analysis_run
		 WHERE batch_id = $1 AND kind = $2 AND status IN `+analysisActiveStatuses+`
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, batchID, kind))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnalysisRun{}, application.ErrAnalysisRunNotFound
		}
		return domain.AnalysisRun{}, fmt.Errorf("postgres: get active analysis run: %w", err)
	}
	return run, nil
}

// Latest returns the newest run of one (batch, kind).
func (r *AnalysisRepository) Latest(ctx context.Context, batchID int64, kind string) (domain.AnalysisRun, error) {
	run, err := scanAnalysisRun(r.q.QueryRow(ctx,
		`SELECT `+analysisColumnList+`
		 FROM analysis_run
		 WHERE batch_id = $1 AND kind = $2
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, batchID, kind))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnalysisRun{}, application.ErrAnalysisRunNotFound
		}
		return domain.AnalysisRun{}, fmt.Errorf("postgres: get latest analysis run: %w", err)
	}
	return run, nil
}

// ListActive returns queued/running runs across batches, oldest first, so the
// 2s worker polls them in start order.
func (r *AnalysisRepository) ListActive(ctx context.Context, limit int) ([]domain.AnalysisRun, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("postgres: list active analysis runs: limit must be positive")
	}
	rows, err := r.q.Query(ctx,
		`SELECT `+analysisColumnList+`
		 FROM analysis_run
		 WHERE status IN `+analysisActiveStatuses+`
		 ORDER BY created_at ASC, id ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active analysis runs: %w", err)
	}
	defer rows.Close()

	runs := make([]domain.AnalysisRun, 0, limit)
	for rows.Next() {
		run, err := scanAnalysisRun(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan analysis run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list active analysis runs: %w", err)
	}
	return runs, nil
}

// UpdateStatus is a status compare-and-swap. A lost CAS is classified by
// re-reading the stored status so the caller can distinguish a missing run from
// a run that already moved on.
func (r *AnalysisRepository) UpdateStatus(ctx context.Context, analysisID int64, from, to string, patch application.AnalysisRunPatch) (domain.AnalysisRun, error) {
	const query = `UPDATE analysis_run
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
		RETURNING ` + analysisColumnList
	updated, err := scanAnalysisRun(r.q.QueryRow(ctx, query,
		analysisID, from, to,
		analysisNullableInt(patch.AgentSessionID), analysisNullableInt(patch.AgentRunID),
		analysisNullableString(patch.Model), analysisNullableString(patch.PromptVersion),
		analysisJSONParam(patch.Input), analysisJSONParam(patch.Result),
		analysisJSONParam(patch.Stats), analysisJSONParam(patch.Errors),
		analysisNullableTime(patch.StartedAt), analysisNullableTime(patch.FinishedAt),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnalysisRun{}, r.updateStatusConflict(ctx, analysisID, from)
		}
		return domain.AnalysisRun{}, fmt.Errorf("postgres: update analysis run status: %w", err)
	}
	return updated, nil
}

// updateStatusConflict classifies a zero-row status CAS.
func (r *AnalysisRepository) updateStatusConflict(ctx context.Context, analysisID int64, from string) error {
	var status string
	if err := r.q.QueryRow(ctx,
		`SELECT status FROM analysis_run WHERE id = $1`, analysisID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrAnalysisRunNotFound
		}
		return fmt.Errorf("postgres: read analysis run status after CAS: %w", err)
	}
	return fmt.Errorf("%w: analysis run %d is %s, not %s", application.ErrAnalysisStateConflict, analysisID, status, from)
}

// RecoverStale fails every queued/running analysis run of the previous process
// (spec §6.7) and returns the recovered rows. The run documents are kept; the
// stable stale_run_recovered reason replaces any reason they had.
func (r *AnalysisRepository) RecoverStale(ctx context.Context) ([]domain.AnalysisRun, error) {
	rows, err := r.q.Query(ctx, `
		UPDATE analysis_run
		SET status = 'failed',
		    errors = '`+analysisStaleErrors+`'::jsonb,
		    finished_at = COALESCE(finished_at, now()),
		    updated_at = now()
		WHERE status IN `+analysisActiveStatuses+`
		RETURNING `+analysisColumnList)
	if err != nil {
		return nil, fmt.Errorf("postgres: recover stale analysis runs: %w", err)
	}
	defer rows.Close()

	recovered := make([]domain.AnalysisRun, 0)
	for rows.Next() {
		run, err := scanAnalysisRun(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan recovered analysis run: %w", err)
		}
		recovered = append(recovered, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: recover stale analysis runs: %w", err)
	}
	return recovered, nil
}

// analysisConstraint returns the violated constraint name of a PostgreSQL
// unique violation, or "".
func analysisConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}

// analysisNullableInt maps an optional id to a NULL-able parameter.
func analysisNullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// analysisNullableString maps an optional string to a NULL-able parameter.
func analysisNullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// analysisNullableTime maps an optional timestamp to a NULL-able parameter.
func analysisNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// analysisJSONParam maps a JSON document to a NULL-able jsonb parameter; the
// query carries the explicit ::jsonb cast and the value is bound as text (the
// same convention as the other repositories).
func analysisJSONParam(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
