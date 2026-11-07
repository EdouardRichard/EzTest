package quality

import (
	"errors"
	"math"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// snapshotPassingSample is above every W2-14 threshold.
func snapshotPassingSample() SnapshotSample {
	return SnapshotSample{
		ConfirmedRequirements: 10,
		CoveredRequirements:   9,
		ConfirmedCases:        8,
		ExecutionPassed:       9,
		ExecutionFinished:     10,
		VisualPassed:          9,
		VisualEvaluated:       10,
	}
}

func snapshotMetricByName(t *testing.T, metrics []Metric, name string) Metric {
	t.Helper()
	for _, metric := range metrics {
		if metric.Name == name {
			return metric
		}
	}
	t.Fatalf("missing metric %q in %+v", name, metrics)
	return Metric{}
}

// TestEvaluateSnapshotDerivesFourMetrics pins the W2-14 metric contract: the
// snapshot track derives design_coverage, execution_pass_rate, case_coverage and
// visual_classification_accuracy in order, from real product counters only.
func TestEvaluateSnapshotDerivesFourMetrics(t *testing.T) {
	status, metrics := EvaluateSnapshot(snapshotPassingSample(), nil, 0.05)
	if status != StatusPassed {
		t.Fatalf("status = %q, want passed", status)
	}
	if len(metrics) != 4 {
		t.Fatalf("metrics = %d, want 4", len(metrics))
	}
	want := SnapshotMetricOrder
	for index, name := range want {
		if metrics[index].Name != name {
			t.Fatalf("metric %d = %q, want %q", index, metrics[index].Name, name)
		}
	}
	design := snapshotMetricByName(t, metrics, MetricDesignCoverage)
	if math.Abs(design.Value-0.9) > 1e-9 || design.Detail.Numerator != 9 || design.Detail.Denominator != 10 {
		t.Fatalf("design metric = %+v", design)
	}
	exec := snapshotMetricByName(t, metrics, MetricExecutionPassRate)
	if math.Abs(exec.Value-0.9) > 1e-9 || exec.Threshold != ThresholdExecutionPassRate {
		t.Fatalf("execution metric = %+v", exec)
	}
	cases := snapshotMetricByName(t, metrics, MetricCaseCoverage)
	if math.Abs(cases.Value-0.8) > 1e-9 || cases.Threshold != ThresholdCaseCoverage {
		t.Fatalf("case coverage metric = %+v", cases)
	}
	visual := snapshotMetricByName(t, metrics, MetricVisualClassificationAccuracy)
	if math.Abs(visual.Value-0.9) > 1e-9 {
		t.Fatalf("visual metric = %+v", visual)
	}
}

// TestEvaluateSnapshotZeroDenominatorsNeverPass proves a missing opportunity is
// recorded, never turned into a fake pass, even for the snapshot track.
func TestEvaluateSnapshotZeroDenominatorsNeverPass(t *testing.T) {
	status, metrics := EvaluateSnapshot(SnapshotSample{}, nil, 0.05)
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if len(metrics) != 4 {
		t.Fatalf("metrics = %d, want 4", len(metrics))
	}
	for _, metric := range metrics {
		if metric.Passed {
			t.Fatalf("zero-denominator metric %q passed", metric.Name)
		}
		if !metric.Detail.ZeroDenominator {
			t.Fatalf("metric %q zero_denominator = false", metric.Name)
		}
		if metric.Value != 0 {
			t.Fatalf("metric %q value = %v, want 0", metric.Name, metric.Value)
		}
	}
}

// TestEvaluateSnapshotCaseCoverageCapsAtOne: one requirement needs at least one
// confirmed case; extra cases do not push coverage above 100%.
func TestEvaluateSnapshotCaseCoverageCapsAtOne(t *testing.T) {
	sample := snapshotPassingSample()
	sample.ConfirmedCases = 25
	sample.ConfirmedRequirements = 10
	_, metrics := EvaluateSnapshot(sample, nil, 0.05)
	metric := snapshotMetricByName(t, metrics, MetricCaseCoverage)
	if metric.Value != 1 {
		t.Fatalf("case_coverage = %v, want 1", metric.Value)
	}
	if metric.Detail.Numerator != 10 || metric.Detail.Denominator != 10 {
		t.Fatalf("case_coverage detail = %+v", metric.Detail)
	}
}

// TestEvaluateSnapshotBaselineRegressionWarnsOnly pins the warn-only baseline:
// the metric is marked regressed and its status fails, but the function still
// returns the full derived series (nothing is blocked or dropped).
func TestEvaluateSnapshotBaselineRegressionWarnsOnly(t *testing.T) {
	baseline := map[string]float64{
		MetricDesignCoverage:               0.99,
		MetricExecutionPassRate:            0.99,
		MetricCaseCoverage:                 0.99,
		MetricVisualClassificationAccuracy: 0.99,
	}
	status, metrics := EvaluateSnapshot(snapshotPassingSample(), baseline, 0.05)
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if len(metrics) != 4 {
		t.Fatalf("metrics = %d, want 4", len(metrics))
	}
	design := snapshotMetricByName(t, metrics, MetricDesignCoverage)
	if !design.Detail.Regressed || design.Passed || design.Detail.Reason != ReasonBaselineRegression {
		t.Fatalf("design metric = %+v", design)
	}
	if design.Detail.Baseline == nil || math.Abs(*design.Detail.Baseline-0.99) > 1e-9 {
		t.Fatalf("baseline = %+v", design.Detail.Baseline)
	}
}

func TestValidateSnapshotRejectsImpossibleCounters(t *testing.T) {
	invalid := []SnapshotSample{
		{ConfirmedRequirements: 5, CoveredRequirements: 6},
		{ExecutionFinished: 5, ExecutionPassed: 6},
		{VisualEvaluated: 5, VisualPassed: 6},
		{ConfirmedCases: -1},
	}
	for _, sample := range invalid {
		if err := ValidateSnapshot(sample); err == nil {
			t.Fatalf("ValidateSnapshot(%+v) = nil", sample)
		} else {
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) || apiErr.Status != 400 {
				t.Fatalf("ValidateSnapshot(%+v) err = %v", sample, err)
			}
		}
	}
	if err := ValidateSnapshot(snapshotPassingSample()); err != nil {
		t.Fatalf("valid sample rejected: %v", err)
	}
}
