//go:build integration

package qualitypg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	qualitypg "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
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

// integrationSnapshotPorts is never read by the unknown-version tests: the
// version-existence probe must abort the trigger before these counters run.
type integrationSnapshotPorts struct{ calls int }

func (p *integrationSnapshotPorts) CountRequirementClosure(context.Context, *int64) (int, int, error) {
	p.calls++
	return 0, 0, nil
}

func (p *integrationSnapshotPorts) CountCaseConfirmation(context.Context, *int64) (int, int, error) {
	p.calls++
	return 0, 0, nil
}

func (p *integrationSnapshotPorts) CountExecutionPass(context.Context, *int64) (int, int, error) {
	p.calls++
	return 0, 0, nil
}

func (p *integrationSnapshotPorts) VisualClassification(context.Context, *int64) (int, int, error) {
	p.calls++
	return 0, 0, nil
}

// TestSnapshotTriggerAndTrendsUnknownVersion404WithRealVersionRead wires the
// real version read service and its SQL existence probe (not a fake-only error)
// and proves an unknown version is a 404 with no eval_run row and no product
// counter read, for both the snapshot trigger and the trend series.
func TestSnapshotTriggerAndTrendsUnknownVersion404WithRealVersionRead(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := qualitypg.NewRepository(pool)
	ctx := context.Background()

	designReader, err := designpg.NewQueryReader(pool)
	if err != nil {
		t.Fatalf("design reader: %v", err)
	}
	versionReader, err := versionpg.NewQueryReader(pool, designapp.NewQueryService(designReader))
	if err != nil {
		t.Fatalf("version reader: %v", err)
	}
	ids, err := platformid.NewSnowflake(14)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	ports := &integrationSnapshotPorts{}
	service := qualityapp.NewService(repo, ids, clock.System{}, nil, nil, nil).
		WithSnapshotPorts(ports, ports, ports, ports).
		WithVersionExistence(versionapp.NewVersionReadService(versionReader))

	const unknown int64 = 999999
	_, err = service.TriggerSnapshot(ctx, platform.Actor{ID: 9, Role: "admin"}, unknown)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Details["kind"] != "version" {
		t.Fatalf("trigger error = %v", err)
	}
	if ports.calls != 0 {
		t.Fatalf("unknown version read %d product counters", ports.calls)
	}
	var runs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM eval_run`).Scan(&runs); err != nil {
		t.Fatalf("count eval_run: %v", err)
	}
	if runs != 0 {
		t.Fatalf("unknown version persisted %d eval run(s)", runs)
	}

	_, err = service.Trends(ctx, unknown, quality.MetricDesignCoverage, time.Time{}, time.Time{})
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Details["kind"] != "version" {
		t.Fatalf("trends error = %v", err)
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
