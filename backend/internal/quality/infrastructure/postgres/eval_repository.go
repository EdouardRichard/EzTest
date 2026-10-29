// Package postgres persists the quality evaluation runs (design spec §4.2). The
// repository owns only eval_run/eval_metric: it never reads a business context
// table, and the only way results enter the platform is the admin API (D61).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// evalRunColumns is the shared column order of every eval_run query and the
// order scanEvalRun expects.
const evalRunColumns = `id, mode, corpus_version, model, status, summary, started_at, finished_at`

// evalMetricColumns is the shared column order of eval_metric.
const evalMetricColumns = `run_id, name, value, threshold, passed, detail`

// EvalRepository implements application.RunRepository on a pgx pool.
type EvalRepository struct {
	pool *pgxpool.Pool
}

// NewEvalRepository returns the evaluation-run repository backed by pool.
func NewEvalRepository(pool *pgxpool.Pool) application.RunRepository {
	return &EvalRepository{pool: pool}
}

var _ application.RunRepository = (*EvalRepository)(nil)

// Create inserts run and its metrics in one transaction. The caller supplies the
// explicit Snowflake id (D29); a duplicate id is surfaced as a generic insert
// error because the service never retries with a reused id.
func (r *EvalRepository) Create(ctx context.Context, run domain.Run) (domain.Run, error) {
	if r == nil || r.pool == nil {
		return domain.Run{}, errors.New("quality: eval repository is not configured")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Run{}, fmt.Errorf("begin eval run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := scanEvalRun(tx.QueryRow(ctx,
		`INSERT INTO eval_run (id, mode, corpus_version, model, status, summary, started_at, finished_at)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)
		 RETURNING `+evalRunColumns,
		run.ID, string(run.Mode), run.CorpusVersion, run.Model, string(run.Status),
		evalJSONParam(run.Summary), run.StartedAt, evalNullableTime(run.FinishedAt),
	))
	if err != nil {
		return domain.Run{}, fmt.Errorf("insert eval run: %w", err)
	}
	for _, metric := range run.Metrics {
		if _, err := tx.Exec(ctx,
			`INSERT INTO eval_metric (run_id, name, value, threshold, passed, detail)
			 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
			created.ID, metric.Name, metric.Value, metric.Threshold, metric.Passed,
			evalJSONParam(metric.Detail),
		); err != nil {
			return domain.Run{}, fmt.Errorf("insert eval metric %q: %w", metric.Name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Run{}, fmt.Errorf("commit eval run: %w", err)
	}
	created.Metrics = append([]domain.Metric(nil), run.Metrics...)
	return created, nil
}

// Get returns one run with its metrics, or application.ErrRunNotFound.
func (r *EvalRepository) Get(ctx context.Context, id int64) (domain.Run, error) {
	if r == nil || r.pool == nil {
		return domain.Run{}, errors.New("quality: eval repository is not configured")
	}
	run, err := scanEvalRun(r.pool.QueryRow(ctx,
		`SELECT `+evalRunColumns+` FROM eval_run WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, application.ErrRunNotFound
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("select eval run: %w", err)
	}
	metrics, err := r.loadMetrics(ctx, []int64{run.ID})
	if err != nil {
		return domain.Run{}, err
	}
	run.Metrics = metrics[run.ID]
	if run.Metrics == nil {
		run.Metrics = []domain.Metric{}
	}
	return run, nil
}

// List returns the newest runs first, bounded by limit, each with its metrics.
func (r *EvalRepository) List(ctx context.Context, limit int) ([]domain.Run, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("quality: eval repository is not configured")
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+evalRunColumns+` FROM eval_run ORDER BY started_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list eval runs: %w", err)
	}
	defer rows.Close()

	runs := make([]domain.Run, 0, limit)
	ids := make([]int64, 0, limit)
	for rows.Next() {
		run, scanErr := scanEvalRun(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan eval run: %w", scanErr)
		}
		runs = append(runs, run)
		ids = append(ids, run.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eval runs: %w", err)
	}
	metrics, err := r.loadMetrics(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range runs {
		runs[index].Metrics = metrics[runs[index].ID]
		if runs[index].Metrics == nil {
			runs[index].Metrics = []domain.Metric{}
		}
	}
	return runs, nil
}

// Latest returns the newest run, or application.ErrRunNotFound when the platform
// has never been evaluated.
func (r *EvalRepository) Latest(ctx context.Context) (domain.Run, error) {
	if r == nil || r.pool == nil {
		return domain.Run{}, errors.New("quality: eval repository is not configured")
	}
	run, err := scanEvalRun(r.pool.QueryRow(ctx,
		`SELECT `+evalRunColumns+` FROM eval_run ORDER BY started_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, application.ErrRunNotFound
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("select latest eval run: %w", err)
	}
	metrics, err := r.loadMetrics(ctx, []int64{run.ID})
	if err != nil {
		return domain.Run{}, err
	}
	run.Metrics = metrics[run.ID]
	if run.Metrics == nil {
		run.Metrics = []domain.Metric{}
	}
	return run, nil
}

// loadMetrics reads the metrics of every requested run in one statement and
// groups them by run id. The metric order of each run follows the natural
// primary key order (name), which is stable across reads.
func (r *EvalRepository) loadMetrics(ctx context.Context, runIDs []int64) (map[int64][]domain.Metric, error) {
	metrics := make(map[int64][]domain.Metric, len(runIDs))
	if len(runIDs) == 0 {
		return metrics, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+evalMetricColumns+` FROM eval_metric WHERE run_id = ANY($1) ORDER BY run_id, name`, runIDs)
	if err != nil {
		return nil, fmt.Errorf("load eval metrics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			runID  int64
			metric domain.Metric
			detail []byte
		)
		if err := rows.Scan(&runID, &metric.Name, &metric.Value, &metric.Threshold,
			&metric.Passed, &detail); err != nil {
			return nil, fmt.Errorf("scan eval metric: %w", err)
		}
		metric.Detail = evalDetailDocument(detail)
		metrics[runID] = append(metrics[runID], metric)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eval metrics: %w", err)
	}
	return metrics, nil
}

// scanEvalRun scans one row in evalRunColumns order and decodes the summary
// document. The nullable finished_at stays nil for a running run.
func scanEvalRun(row interface{ Scan(dest ...any) error }) (domain.Run, error) {
	var (
		run      domain.Run
		mode     string
		status   string
		summary  []byte
		finished *time.Time
	)
	if err := row.Scan(&run.ID, &mode, &run.CorpusVersion, &run.Model, &status, &summary,
		&run.StartedAt, &finished); err != nil {
		return domain.Run{}, err
	}
	run.Mode = domain.Mode(mode)
	run.Status = domain.Status(status)
	run.Summary = evalSummaryDocument(summary)
	run.FinishedAt = finished
	return run, nil
}

// evalSummaryDocument decodes a JSONB summary, defaulting to an empty document.
func evalSummaryDocument(raw []byte) map[string]any {
	var summary map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &summary); err != nil {
			summary = nil
		}
	}
	if summary == nil {
		summary = map[string]any{}
	}
	return summary
}

// evalDetailDocument decodes a JSONB metric detail, defaulting to an empty
// document.
func evalDetailDocument(raw []byte) map[string]any {
	detail := evalSummaryDocument(raw)
	return detail
}

// evalJSONParam marshals a document for a JSONB parameter. A nil document is
// sent as an empty object, never as SQL NULL, so the NOT NULL columns stay
// satisfied.
func evalJSONParam(document map[string]any) []byte {
	if document == nil {
		return []byte("{}")
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// evalNullableTime renders an optional timestamp for pgx.
func evalNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}
