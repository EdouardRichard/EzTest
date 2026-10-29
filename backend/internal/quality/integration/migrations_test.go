//go:build integration

// Package integration_test holds the quality context's PostgreSQL integration
// tests: the migration contract (tables, primary keys, unique key, CHECKs) and
// the repository/service round trip against the real schema.
package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	qualitypostgres "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// qualityNow is a lossless TIMESTAMPTZ value of the round-trip test.
var qualityNow = time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)

// TestMigrationContract applies the embedded migrations (including quality_0001)
// and asserts the spec §4.2 schema contract: both tables, their named primary
// keys, the (run_id, name) uniqueness and the mode/status CHECK enums.
func TestMigrationContract(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	for _, table := range []string{"eval_run", "eval_metric"} {
		if !qualityTableExists(t, ctx, pool, table) {
			t.Fatalf("table %s does not exist", table)
		}
	}
	assertQualityPrimaryKey(t, ctx, pool, "eval_run", "pk_eval_run")
	assertQualityPrimaryKey(t, ctx, pool, "eval_metric", "pk_eval_metric")

	// A second metric with the same (run_id, name) is rejected by the composite
	// primary key, which is the natural idempotency key of a run.
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_run (id, mode, corpus_version, status) VALUES (1, 'deterministic', 'corpus', 'passed')`); err != nil {
		t.Fatalf("insert eval_run: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_metric (run_id, name, value, threshold, passed) VALUES (1, 'design_coverage', 1, 0.9, true)`); err != nil {
		t.Fatalf("insert eval_metric: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_metric (run_id, name, value, threshold, passed) VALUES (1, 'design_coverage', 0, 0.9, false)`); err == nil {
		t.Fatal("a duplicate (run_id, name) eval_metric unexpectedly succeeded")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("duplicate metric error = %v, want SQLSTATE 23505", err)
		}
	}

	// The mode/status CHECKs keep the runner and the dashboard on the two
	// documented tracks and four documented states.
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_run (id, mode, corpus_version, status) VALUES (2, 'manual', 'corpus', 'passed')`); err == nil {
		t.Fatal("eval_run accepted an unknown mode")
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_run (id, mode, corpus_version, status) VALUES (3, 'deterministic', 'corpus', 'skipped')`); err == nil {
		t.Fatal("eval_run accepted an unknown status")
	}
}

// TestRepositoryAndServiceRoundTrip proves the admin POST path persists a run and
// its metrics atomically and reads them back with the same values, verdicts and
// detail documents.
func TestRepositoryAndServiceRoundTrip(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := qualitypostgres.NewEvalRepository(pool)
	service := application.NewService(repo,
		application.WithIDGenerator(&qualityIDs{next: 9000}),
		application.WithClock(qualityClock{now: qualityNow}),
	)

	sample := domain.Sample{
		ConfirmedRequirements:  5,
		CoveredRequirements:    4,
		ImpactExpected:         4,
		ImpactHit:              3,
		ImpactSelected:         4,
		InjectedFailures:       2,
		CorrectClassifications: 2,
		AIIssues:               2,
		TruePositiveIssues:     2,
		SupplementCases:        2,
		SupplementHits:         1,
	}
	created, err := service.Record(ctx, application.RecordInput{
		Mode:          domain.ModeDeterministic,
		CorpusVersion: "2026-10-29.1",
		Model:         "fake-llm",
		Sample:        &sample,
		Baseline:      map[string]float64{domain.MetricDesignCoverage: 0.80},
		Summary:       map[string]any{"scenario": "deterministic"},
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if created.ID != 9001 {
		t.Fatalf("created id = %d, want 9001", created.ID)
	}
	if created.Status != domain.StatusFailed {
		t.Fatalf("created status = %q, want failed", created.Status)
	}

	stored, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Mode != domain.ModeDeterministic || stored.CorpusVersion != "2026-10-29.1" ||
		stored.Model != "fake-llm" || stored.Status != domain.StatusFailed {
		t.Fatalf("stored run = %+v", stored)
	}
	if stored.Summary["scenario"] != "deterministic" {
		t.Errorf("stored summary = %+v", stored.Summary)
	}
	if stored.FinishedAt == nil || !stored.FinishedAt.Equal(qualityNow) ||
		!stored.StartedAt.Equal(qualityNow) {
		t.Errorf("stored times = %v/%v, want the injected clock", stored.StartedAt, stored.FinishedAt)
	}
	if len(stored.Metrics) != 6 {
		t.Fatalf("stored metrics = %d (%+v), want 6", len(stored.Metrics), stored.Metrics)
	}
	design := qualityMetric(stored, domain.MetricDesignCoverage)
	if !almostEqual(design.Value, 0.8) || !almostEqual(design.Threshold, 0.90) || design.Passed {
		t.Errorf("design metric = %+v, want value 0.8 threshold 0.90 failed", design)
	}
	if design.Detail["reason"] != domain.MetricReasonBelowThreshold {
		t.Errorf("design reason = %v, want below_threshold", design.Detail["reason"])
	}
	if !almostEqual(design.Detail["baseline"].(float64), 0.80) {
		t.Errorf("design baseline detail = %v, want 0.80", design.Detail["baseline"])
	}

	runs, err := repo.List(ctx, 10)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != created.ID || len(runs[0].Metrics) != 6 {
		t.Fatalf("List() = %+v", runs)
	}
	latest, err := repo.Latest(ctx)
	if err != nil || latest.ID != created.ID {
		t.Fatalf("Latest() = %+v, %v want id %d", latest, err, created.ID)
	}

	// The empty-history contract the Agent centre depends on.
	if _, err := pool.Exec(ctx, `DELETE FROM eval_metric; DELETE FROM eval_run;`); err != nil {
		t.Fatalf("truncate eval tables: %v", err)
	}
	if _, err := repo.Latest(ctx); !errors.Is(err, application.ErrRunNotFound) {
		t.Errorf("Latest(empty) error = %v, want ErrRunNotFound", err)
	}
	if _, err := repo.Get(ctx, created.ID); !errors.Is(err, application.ErrRunNotFound) {
		t.Errorf("Get(deleted) error = %v, want ErrRunNotFound", err)
	}
}

// qualityIDs is a deterministic application.IDGenerator.
type qualityIDs struct{ next int64 }

func (g *qualityIDs) Next() int64 {
	g.next++
	return g.next
}

// qualityClock is a deterministic application.Clock.
type qualityClock struct{ now time.Time }

func (c qualityClock) Now() time.Time { return c.now }

// qualityTableExists reports whether a public table exists.
func qualityTableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = $1)`, table).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

// assertQualityPrimaryKey pins the named primary-key constraint of one table.
func assertQualityPrimaryKey(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, name string) {
	t.Helper()
	var found string
	if err := pool.QueryRow(ctx,
		`SELECT conname FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'p'`,
		"public."+table).Scan(&found); err != nil {
		t.Fatalf("read primary key of %s: %v", table, err)
	}
	if found != name {
		t.Errorf("%s primary key = %q, want %q", table, found, name)
	}
}

// qualityMetric finds one stored metric by name.
func qualityMetric(run domain.Run, name string) domain.Metric {
	for _, metric := range run.Metrics {
		if metric.Name == name {
			return metric
		}
	}
	return domain.Metric{}
}

func almostEqual(left, right float64) bool {
	delta := left - right
	return delta < 1e-9 && delta > -1e-9
}
