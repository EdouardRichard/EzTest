//go:build integration

package qualitypg_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	qualitypg "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func floatPtr(value float64) *float64 { return &value }

func fixtureRun(id int64, status string, createdAt time.Time) qualityapp.EvalRun {
	finished := createdAt.Add(30 * time.Second)
	return qualityapp.EvalRun{
		ID:            id,
		Mode:          quality.EvalModeRealLLM,
		CorpusVersion: "v02-2026-11-01.1",
		Model:         "deepseek-flash",
		Status:        status,
		Summary:       map[string]any{"version_id": "10", "scenario": "eval"},
		StartedAt:     createdAt,
		FinishedAt:    &finished,
		CreatedAt:     createdAt,
	}
}

func fixtureMetrics() []quality.Metric {
	return []quality.Metric{
		{
			Name:      quality.MetricDesignCoverage,
			Value:     0.8,
			Threshold: 0.80,
			Passed:    true,
			Detail: quality.MetricDetail{
				Numerator: 8, Denominator: 10, ThresholdPassed: true,
				Baseline: floatPtr(0.7), Regressed: false, Reason: quality.ReasonOK,
			},
		},
		{
			Name:      quality.MetricIssueFalsePositiveRate,
			Value:     0.2,
			Threshold: 0.15,
			Passed:    false,
			Detail: quality.MetricDetail{
				Numerator: 2, Denominator: 10, ThresholdPassed: false,
				Regressed: false, Reason: quality.ReasonAboveThreshold,
			},
		},
	}
}

func TestEvalRepositoryInsertGetListLatest(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := qualitypg.NewRepository(pool)
	ctx := context.Background()

	first := fixtureRun(1001, quality.StatusPassed, time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))
	metrics := fixtureMetrics()
	if err := repo.Insert(ctx, first, metrics); err != nil {
		t.Fatalf("insert first: %v", err)
	}

	loaded, err := repo.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	if loaded.ID != first.ID || loaded.Mode != quality.EvalModeRealLLM ||
		loaded.CorpusVersion != first.CorpusVersion || loaded.Model != first.Model ||
		loaded.Status != quality.StatusPassed {
		t.Fatalf("loaded header = %+v", loaded)
	}
	if !loaded.StartedAt.Equal(first.StartedAt) || loaded.FinishedAt == nil || !loaded.FinishedAt.Equal(*first.FinishedAt) {
		t.Fatalf("loaded times = %v / %v", loaded.StartedAt, loaded.FinishedAt)
	}
	if loaded.Summary["version_id"] != "10" || loaded.Summary["scenario"] != "eval" {
		t.Fatalf("loaded summary = %+v", loaded.Summary)
	}
	if len(loaded.Metrics) != 2 {
		t.Fatalf("loaded metrics = %d", len(loaded.Metrics))
	}
	design := loaded.Metrics[0]
	if design.Name != quality.MetricDesignCoverage || math.Abs(design.Value-0.8) > 1e-9 ||
		design.Threshold != 0.80 || !design.Passed || design.Detail.Numerator != 8 || design.Detail.Denominator != 10 {
		t.Fatalf("design metric = %+v", design)
	}
	if design.Detail.Baseline == nil || math.Abs(*design.Detail.Baseline-0.7) > 1e-9 {
		t.Fatalf("design baseline = %+v", design.Detail.Baseline)
	}
	fp := loaded.Metrics[1]
	if fp.Name != quality.MetricIssueFalsePositiveRate || fp.Passed ||
		fp.Detail.Reason != quality.ReasonAboveThreshold || fp.Detail.ThresholdPassed {
		t.Fatalf("false-positive metric = %+v", fp)
	}

	// Newer created_at wins; when equal, the larger id wins.
	second := fixtureRun(1002, quality.StatusFailed, first.CreatedAt.Add(time.Minute))
	if err := repo.Insert(ctx, second, nil); err != nil {
		t.Fatalf("insert second: %v", err)
	}
	tie := fixtureRun(1003, quality.StatusError, second.CreatedAt)
	if err := repo.Insert(ctx, tie, nil); err != nil {
		t.Fatalf("insert tie: %v", err)
	}

	list, err := repo.List(ctx, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d rows", len(list))
	}
	if list[0].ID != 1003 || list[1].ID != 1002 {
		t.Fatalf("list order = %d,%d", list[0].ID, list[1].ID)
	}
	if len(list[0].Metrics) != 0 || len(list[1].Metrics) != 0 {
		t.Fatalf("runs without metrics got invented rows")
	}

	latest, err := repo.Latest(ctx)
	if err != nil || latest == nil || latest.ID != 1003 {
		t.Fatalf("latest = %+v err=%v", latest, err)
	}
	if latest.Status != quality.StatusError || latest.FinishedAt == nil {
		t.Fatalf("latest run = %+v", latest)
	}

	if _, err := repo.Get(ctx, 9999); !errors.Is(err, quality.ErrNotFound) {
		t.Fatalf("missing get err = %v", err)
	}
}
