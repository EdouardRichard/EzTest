// Package qualitypg is the PostgreSQL adapter of the P6 quality context. It
// implements the quality application repository port over its own two frozen
// tables (eval_run/eval_metric). It never reads another context's tables.
package qualitypg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const runColumns = `id, mode, corpus_version, model, status, summary::text,
	started_at, finished_at, created_at`

// Repository persists and reads eval_run/eval_metric rows.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository wires the quality repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Insert writes the run and its metrics in one transaction. The quality
// context only writes its own tables (spec §5.1).
func (r *Repository) Insert(ctx context.Context, run qualityapp.EvalRun, metrics []quality.Metric) error {
	if r == nil || r.pool == nil {
		return errors.New("quality repository is not configured")
	}
	summary, err := encodeJSONObject(run.Summary)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin eval run insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO eval_run (id, mode, corpus_version, model, status, summary,
			started_at, finished_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $9)
	`, run.ID, run.Mode, run.CorpusVersion, run.Model, run.Status, summary,
		run.StartedAt, run.FinishedAt, run.CreatedAt); err != nil {
		return fmt.Errorf("insert eval run: %w", err)
	}

	for _, metric := range metrics {
		detail, err := encodeJSONObject(metric.Detail)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO eval_metric (run_id, name, value, threshold, passed, detail, created_at)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
		`, run.ID, metric.Name, metric.Value, metric.Threshold, metric.Passed,
			detail, run.CreatedAt); err != nil {
			return fmt.Errorf("insert eval metric %q: %w", metric.Name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit eval run insert: %w", err)
	}
	return nil
}

// Get returns one run with its metrics, or quality.ErrNotFound.
func (r *Repository) Get(ctx context.Context, id int64) (qualityapp.EvalRun, error) {
	if r == nil || r.pool == nil {
		return qualityapp.EvalRun{}, errors.New("quality repository is not configured")
	}
	run, err := scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM eval_run WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return qualityapp.EvalRun{}, quality.ErrNotFound
	}
	if err != nil {
		return qualityapp.EvalRun{}, fmt.Errorf("get eval run: %w", err)
	}
	byRun, err := r.metricsForRuns(ctx, []int64{run.ID})
	if err != nil {
		return qualityapp.EvalRun{}, err
	}
	run.Metrics = byRun[run.ID]
	if run.Metrics == nil {
		run.Metrics = []quality.Metric{}
	}
	return run, nil
}

// List returns the newest terminal runs ordered by created_at DESC, id DESC
// (the frozen no-cursor the contract documents). limit is normalized by the
// application service; the SQL still bounds the page.
func (r *Repository) List(ctx context.Context, limit int) ([]qualityapp.EvalRun, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("quality repository is not configured")
	}
	if limit <= 0 {
		limit = qualityapp.DefaultListLimit
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+runColumns+` FROM eval_run ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list eval runs: %w", err)
	}
	defer rows.Close()

	runs := make([]qualityapp.EvalRun, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan eval run: %w", err)
		}
		runs = append(runs, run)
		ids = append(ids, run.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list eval runs: %w", err)
	}

	byRun, err := r.metricsForRuns(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range runs {
		runs[index].Metrics = byRun[runs[index].ID]
		if runs[index].Metrics == nil {
			runs[index].Metrics = []quality.Metric{}
		}
	}
	return runs, nil
}

// Latest returns the newest run, or (nil, nil) when no run exists yet.
func (r *Repository) Latest(ctx context.Context) (*qualityapp.EvalRun, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("quality repository is not configured")
	}
	run, err := scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM eval_run ORDER BY created_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest eval run: %w", err)
	}
	byRun, err := r.metricsForRuns(ctx, []int64{run.ID})
	if err != nil {
		return nil, err
	}
	run.Metrics = byRun[run.ID]
	if run.Metrics == nil {
		run.Metrics = []quality.Metric{}
	}
	return &run, nil
}

func (r *Repository) metricsForRuns(ctx context.Context, runIDs []int64) (map[int64][]quality.Metric, error) {
	metrics := make(map[int64][]quality.Metric, len(runIDs))
	if len(runIDs) == 0 {
		return metrics, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT run_id, name, value::float8, threshold::float8, passed, detail::text
		FROM eval_metric
		WHERE run_id = ANY($1)
		ORDER BY run_id, name
	`, runIDs)
	if err != nil {
		return nil, fmt.Errorf("list eval metrics: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var runID int64
		var metric quality.Metric
		var detail string
		if err := rows.Scan(&runID, &metric.Name, &metric.Value, &metric.Threshold,
			&metric.Passed, &detail); err != nil {
			return nil, fmt.Errorf("scan eval metric: %w", err)
		}
		metric.Detail = quality.MetricDetail{}
		if detail != "" {
			if err := json.Unmarshal([]byte(detail), &metric.Detail); err != nil {
				return nil, fmt.Errorf("decode eval metric %q detail: %w", metric.Name, err)
			}
		}
		metrics[runID] = append(metrics[runID], metric)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list eval metrics: %w", err)
	}
	return metrics, nil
}

// rowScanner is satisfied by pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (qualityapp.EvalRun, error) {
	var run qualityapp.EvalRun
	var summary string
	if err := row.Scan(&run.ID, &run.Mode, &run.CorpusVersion, &run.Model, &run.Status,
		&summary, &run.StartedAt, &run.FinishedAt, &run.CreatedAt); err != nil {
		return qualityapp.EvalRun{}, err
	}
	run.Summary = map[string]any{}
	if summary != "" {
		if err := json.Unmarshal([]byte(summary), &run.Summary); err != nil {
			return qualityapp.EvalRun{}, fmt.Errorf("decode eval run summary: %w", err)
		}
	}
	return run, nil
}

func encodeJSONObject(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode eval JSON document: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "{}", nil
	}
	return string(raw), nil
}

var _ qualityapp.Repository = (*Repository)(nil)
