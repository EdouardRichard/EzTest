//go:build integration

package qualitypg_test

import (
	"context"
	"testing"
	"time"

	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	qualitypg "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func fixtureSnapshotRun(id, versionID int64, createdAt time.Time) qualityapp.EvalRun {
	finished := createdAt.Add(5 * time.Second)
	version := versionID
	return qualityapp.EvalRun{
		ID:            id,
		Mode:          quality.EvalModeSnapshot,
		CorpusVersion: "snapshot:7",
		Status:        quality.StatusPassed,
		Summary:       map[string]any{"version_id": "7"},
		VersionID:     &version,
		StartedAt:     createdAt,
		FinishedAt:    &finished,
		CreatedAt:     createdAt,
	}
}

func fixtureSnapshotMetrics(pass float64) []quality.Metric {
	return []quality.Metric{
		{Name: quality.MetricDesignCoverage, Value: 0.9, Threshold: 0.8, Passed: true},
		{Name: quality.MetricExecutionPassRate, Value: pass, Threshold: 0.8, Passed: pass >= 0.8},
		{Name: quality.MetricCaseCoverage, Value: 1, Threshold: 0.8, Passed: true},
		{Name: quality.MetricVisualClassificationAccuracy, Value: 0.9, Threshold: 0.8, Passed: true},
	}
}

// TestEvalRepositorySnapshotVersionAndTrends proves the v0.22 migration columns
// are read/written and the trend query is scoped by version and metric.
func TestEvalRepositorySnapshotVersionAndTrends(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := qualitypg.NewRepository(pool)
	ctx := context.Background()

	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	first := fixtureSnapshotRun(2001, 7, base)
	if err := repo.Insert(ctx, first, fixtureSnapshotMetrics(0.9)); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	second := fixtureSnapshotRun(2002, 7, base.Add(time.Minute))
	if err := repo.Insert(ctx, second, fixtureSnapshotMetrics(0.5)); err != nil {
		t.Fatalf("insert second: %v", err)
	}
	other := fixtureSnapshotRun(2003, 8, base.Add(2*time.Minute))
	if err := repo.Insert(ctx, other, fixtureSnapshotMetrics(0.9)); err != nil {
		t.Fatalf("insert other version: %v", err)
	}

	loaded, err := repo.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	if loaded.VersionID == nil || *loaded.VersionID != 7 || loaded.Mode != quality.EvalModeSnapshot {
		t.Fatalf("loaded = %+v", loaded)
	}
	if len(loaded.Metrics) != 4 {
		t.Fatalf("metrics = %d, want 4", len(loaded.Metrics))
	}

	latest, err := repo.LatestForVersion(ctx, 7)
	if err != nil || latest == nil || latest.ID != second.ID {
		t.Fatalf("latest for version = %+v err=%v", latest, err)
	}
	if missing, err := repo.LatestForVersion(ctx, 99); err != nil || missing != nil {
		t.Fatalf("missing version = %+v err=%v", missing, err)
	}

	points, err := repo.Trends(ctx, 7, quality.MetricExecutionPassRate, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("trend points = %d, want 2", len(points))
	}
	if points[0].RunID != first.ID || points[1].RunID != second.ID {
		t.Fatalf("trend order = %d,%d", points[0].RunID, points[1].RunID)
	}
	if points[0].VersionID == nil || *points[0].VersionID != 7 || points[1].Regressed {
		t.Fatalf("trend rows = %+v", points)
	}

	bounded, err := repo.Trends(ctx, 7, quality.MetricExecutionPassRate, base.Add(30*time.Second), time.Time{})
	if err != nil {
		t.Fatalf("bounded trends: %v", err)
	}
	if len(bounded) != 1 || bounded[0].RunID != second.ID {
		t.Fatalf("bounded points = %+v", bounded)
	}

	design, err := repo.Trends(ctx, 7, quality.MetricDesignCoverage, time.Time{}, time.Time{})
	if err != nil || len(design) != 2 {
		t.Fatalf("design trends = %d err=%v", len(design), err)
	}
}
