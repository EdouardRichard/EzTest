package quality

import (
	"errors"
	"math"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func ptrInt(value int) *int { return &value }

// validSample is comfortably above every real-LLM threshold in spec §5.3 and
// inside every physical constraint of §4.3.3.
func validSample() Sample {
	return Sample{
		ConfirmedRequirements:  10,
		CoveredRequirements:    9,
		ImpactExpected:         5,
		ImpactHit:              4,
		ImpactSelected:         6,
		InjectedFailures:       5,
		CorrectClassifications: 5,
		AIIssues:               10,
		TruePositiveIssues:     9,
		SupplementCases:        4,
		SupplementHits:         3,
		VisualInjected:         ptrInt(5),
		VisualCorrect:          ptrInt(5),
	}
}

func fieldOfError(t *testing.T, err error) string {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *platform.Error", err)
	}
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("error status/code = %d/%s", apiErr.Status, apiErr.Code)
	}
	field, _ := apiErr.Details["field"].(string)
	return field
}

func metricByName(t *testing.T, metrics []Metric, name string) Metric {
	t.Helper()
	for _, metric := range metrics {
		if metric.Name == name {
			return metric
		}
	}
	t.Fatalf("metric %q not found in %+v", name, metrics)
	return Metric{}
}

func TestValidateSampleAcceptsValidSample(t *testing.T) {
	if err := ValidateSample(validSample()); err != nil {
		t.Fatalf("ValidateSample(valid) = %v", err)
	}

	// Visual counters are optional as a pair.
	sample := validSample()
	sample.VisualInjected = nil
	sample.VisualCorrect = nil
	if err := ValidateSample(sample); err != nil {
		t.Fatalf("ValidateSample(no visual) = %v", err)
	}
}

func TestValidateSampleRejectsNegativeCounts(t *testing.T) {
	cases := []struct {
		name  string
		field string
		apply func(*Sample)
	}{
		{"confirmed", "confirmed_requirements", func(s *Sample) { s.ConfirmedRequirements = -1 }},
		{"covered", "covered_requirements", func(s *Sample) { s.CoveredRequirements = -1 }},
		{"impact expected", "impact_expected", func(s *Sample) { s.ImpactExpected = -1 }},
		{"impact hit", "impact_hit", func(s *Sample) { s.ImpactHit = -1 }},
		{"impact selected", "impact_selected", func(s *Sample) { s.ImpactSelected = -1 }},
		{"injected", "injected_failures", func(s *Sample) { s.InjectedFailures = -1 }},
		{"correct", "correct_classifications", func(s *Sample) { s.CorrectClassifications = -1 }},
		{"ai issues", "ai_issues", func(s *Sample) { s.AIIssues = -1 }},
		{"true positives", "true_positive_issues", func(s *Sample) { s.TruePositiveIssues = -1 }},
		{"supplement cases", "supplement_cases", func(s *Sample) { s.SupplementCases = -1 }},
		{"supplement hits", "supplement_hits", func(s *Sample) { s.SupplementHits = -1 }},
		{"visual injected", "visual_injected", func(s *Sample) { s.VisualInjected = ptrInt(-1) }},
		{"visual correct", "visual_correct", func(s *Sample) { s.VisualCorrect = ptrInt(-1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sample := validSample()
			tc.apply(&sample)
			if field := fieldOfError(t, ValidateSample(sample)); field != tc.field {
				t.Fatalf("field = %q, want %q", field, tc.field)
			}
		})
	}
}

func TestValidateSampleRejectsPhysicalImpossibilities(t *testing.T) {
	cases := []struct {
		name  string
		field string
		apply func(*Sample)
	}{
		{
			"covered above confirmed",
			"covered_requirements",
			func(s *Sample) { s.CoveredRequirements = s.ConfirmedRequirements + 1 },
		},
		{
			"impact hit above expected",
			"impact_hit",
			func(s *Sample) { s.ImpactHit = s.ImpactExpected + 1 },
		},
		{
			"impact hit above selected",
			"impact_hit",
			func(s *Sample) { s.ImpactSelected = s.ImpactHit - 1 },
		},
		{
			"correct above injected",
			"correct_classifications",
			func(s *Sample) { s.CorrectClassifications = s.InjectedFailures + 1 },
		},
		{
			"true positive above ai issues",
			"true_positive_issues",
			func(s *Sample) { s.TruePositiveIssues = s.AIIssues + 1 },
		},
		{
			"supplement hits above cases",
			"supplement_hits",
			func(s *Sample) { s.SupplementHits = s.SupplementCases + 1 },
		},
		{
			"visual correct above injected",
			"visual_correct",
			func(s *Sample) { s.VisualCorrect = ptrInt(*s.VisualInjected + 1) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sample := validSample()
			tc.apply(&sample)
			if field := fieldOfError(t, ValidateSample(sample)); field != tc.field {
				t.Fatalf("field = %q, want %q", field, tc.field)
			}
		})
	}
}

func TestValidateSampleRequiresBothVisualCounters(t *testing.T) {
	missingCorrect := validSample()
	missingCorrect.VisualCorrect = nil
	if field := fieldOfError(t, ValidateSample(missingCorrect)); field != "visual_correct" {
		t.Fatalf("missing correct field = %q", field)
	}

	missingInjected := validSample()
	missingInjected.VisualInjected = nil
	if field := fieldOfError(t, ValidateSample(missingInjected)); field != "visual_injected" {
		t.Fatalf("missing injected field = %q", field)
	}
}

func TestEvaluateDerivesRealLLMMetrics(t *testing.T) {
	baseline := map[string]float64{
		MetricDesignCoverage:              0.9,
		MetricImpactRecall:                0.8,
		MetricImpactPrecision:             0.6,
		MetricClassificationAccuracy:      1.0,
		MetricIssueFalsePositiveRate:      0.1,
		MetricSupplementHitRate:           0.5,
		MetricVisualClassificationAccuracy: 1.0,
	}
	status, metrics := Evaluate(validSample(), baseline, 0.05)

	if status != StatusPassed {
		t.Fatalf("status = %q, want %q", status, StatusPassed)
	}
	if len(metrics) != 7 {
		t.Fatalf("metrics = %d, want 7", len(metrics))
	}

	wantNames := []string{
		MetricDesignCoverage,
		MetricImpactRecall,
		MetricImpactPrecision,
		MetricClassificationAccuracy,
		MetricIssueFalsePositiveRate,
		MetricSupplementHitRate,
		MetricVisualClassificationAccuracy,
	}
	for index, name := range wantNames {
		if metrics[index].Name != name {
			t.Fatalf("metric[%d] = %q, want %q", index, metrics[index].Name, name)
		}
	}

	cases := []struct {
		name      string
		value     float64
		threshold float64
		num       int
		den       int
	}{
		{MetricDesignCoverage, 0.9, 0.80, 9, 10},
		{MetricImpactRecall, 0.8, 0.70, 4, 5},
		{MetricImpactPrecision, 4.0 / 6.0, 0.60, 4, 6},
		{MetricClassificationAccuracy, 1.0, 0.80, 5, 5},
		{MetricIssueFalsePositiveRate, 0.1, 0.15, 1, 10},
		{MetricSupplementHitRate, 0.75, 0.50, 3, 4},
		{MetricVisualClassificationAccuracy, 1.0, 0.80, 5, 5},
	}
	for _, tc := range cases {
		metric := metricByName(t, metrics, tc.name)
		if math.Abs(metric.Value-tc.value) > 1e-9 {
			t.Fatalf("%s value = %v, want %v", tc.name, metric.Value, tc.value)
		}
		if metric.Threshold != tc.threshold {
			t.Fatalf("%s threshold = %v, want %v", tc.name, metric.Threshold, tc.threshold)
		}
		if metric.Detail.Numerator != tc.num || metric.Detail.Denominator != tc.den {
			t.Fatalf("%s detail = %+v", tc.name, metric.Detail)
		}
		if !metric.Passed || !metric.Detail.ThresholdPassed || metric.Detail.Regressed {
			t.Fatalf("%s not passed: %+v", tc.name, metric)
		}
		if metric.Detail.Reason != ReasonOK {
			t.Fatalf("%s reason = %q", tc.name, metric.Detail.Reason)
		}
		if metric.Detail.Baseline == nil {
			t.Fatalf("%s baseline missing", tc.name)
		}
	}
}

func TestEvaluateThresholdsAreInclusive(t *testing.T) {
	sample := Sample{
		ConfirmedRequirements:  10,
		CoveredRequirements:    8,
		ImpactExpected:         10,
		ImpactHit:              7,
		ImpactSelected:         10,
		InjectedFailures:       10,
		CorrectClassifications: 8,
		AIIssues:               20,
		TruePositiveIssues:     17,
		SupplementCases:        10,
		SupplementHits:         5,
	}
	status, metrics := Evaluate(sample, nil, 0.05)
	if status != StatusPassed {
		for _, metric := range metrics {
			t.Logf("%s value=%v threshold=%v passed=%v reason=%s", metric.Name, metric.Value, metric.Threshold, metric.Passed, metric.Detail.Reason)
		}
		t.Fatalf("status = %q, want %q", status, StatusPassed)
	}
}

func TestEvaluateFalsePositiveDirection(t *testing.T) {
	sample := validSample()
	sample.AIIssues = 10
	sample.TruePositiveIssues = 5 // 0.5 > 0.15
	status, metrics := Evaluate(sample, nil, 0.05)
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	metric := metricByName(t, metrics, MetricIssueFalsePositiveRate)
	if metric.Value != 0.5 || metric.Passed || metric.Detail.ThresholdPassed {
		t.Fatalf("false positive metric = %+v", metric)
	}
	if metric.Detail.Reason != ReasonAboveThreshold {
		t.Fatalf("reason = %q, want %q", metric.Detail.Reason, ReasonAboveThreshold)
	}

	// All other metrics still pass; only the false-positive rate fails.
	for _, other := range metrics {
		if other.Name == MetricIssueFalsePositiveRate {
			continue
		}
		if !other.Passed {
			t.Fatalf("%s unexpectedly failed: %+v", other.Name, other)
		}
	}
}

func TestEvaluateLowerBoundThresholdFailure(t *testing.T) {
	sample := validSample()
	sample.CoveredRequirements = 7 // 0.70 < 0.80
	status, metrics := Evaluate(sample, nil, 0.05)
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	metric := metricByName(t, metrics, MetricDesignCoverage)
	if metric.Passed || metric.Detail.ThresholdPassed {
		t.Fatalf("coverage metric = %+v", metric)
	}
	if metric.Detail.Reason != ReasonBelowThreshold {
		t.Fatalf("reason = %q, want %q", metric.Detail.Reason, ReasonBelowThreshold)
	}
}

func TestEvaluateBaselineRegressionMarksMetricFailed(t *testing.T) {
	// coverage is 0.9, more than 0.05 below the 1.0 baseline.
	baseline := map[string]float64{MetricDesignCoverage: 1.0}
	status, metrics := Evaluate(validSample(), baseline, 0.05)
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	metric := metricByName(t, metrics, MetricDesignCoverage)
	if !metric.Detail.Regressed || metric.Passed {
		t.Fatalf("coverage metric = %+v", metric)
	}
	if metric.Detail.Reason != ReasonBaselineRegression {
		t.Fatalf("reason = %q, want %q", metric.Detail.Reason, ReasonBaselineRegression)
	}
	if metric.Detail.Baseline == nil || *metric.Detail.Baseline != 1.0 {
		t.Fatalf("baseline detail = %+v", metric.Detail)
	}
}

func TestEvaluateBaselineRegressionDirectionOfUpperBoundMetric(t *testing.T) {
	// False-positive rate 0.1 is more than 0.05 above the 0.0 baseline.
	sample := validSample()
	sample.TruePositiveIssues = 9 // 0.1
	baseline := map[string]float64{MetricIssueFalsePositiveRate: 0.0}
	_, metrics := Evaluate(sample, baseline, 0.05)
	metric := metricByName(t, metrics, MetricIssueFalsePositiveRate)
	if !metric.Detail.Regressed || metric.Passed {
		t.Fatalf("false positive metric = %+v", metric)
	}
	if metric.Detail.Reason != ReasonBaselineRegression {
		t.Fatalf("reason = %q", metric.Detail.Reason)
	}
}

func TestEvaluateRegressionToleranceIsInclusive(t *testing.T) {
	// coverage 0.9 is exactly 0.05 below the 0.95 baseline: not a regression.
	baseline := map[string]float64{MetricDesignCoverage: 0.95}
	_, metrics := Evaluate(validSample(), baseline, 0.05)
	metric := metricByName(t, metrics, MetricDesignCoverage)
	if metric.Detail.Regressed {
		t.Fatalf("coverage regressed = true: %+v", metric)
	}

	// 0.9 vs 0.96 is 0.06 below: a regression.
	baseline[MetricDesignCoverage] = 0.96
	_, metrics = Evaluate(validSample(), baseline, 0.05)
	metric = metricByName(t, metrics, MetricDesignCoverage)
	if !metric.Detail.Regressed {
		t.Fatalf("coverage regressed = false: %+v", metric)
	}
}

func TestEvaluateZeroDenominatorIsNotAFakePass(t *testing.T) {
	status, metrics := Evaluate(Sample{}, nil, 0.05)
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if len(metrics) != 6 {
		t.Fatalf("metrics = %d, want 6 without visual counters", len(metrics))
	}
	for _, metric := range metrics {
		if metric.Value != 0 {
			t.Fatalf("%s value = %v, want 0", metric.Name, metric.Value)
		}
		if !metric.Detail.ZeroDenominator {
			t.Fatalf("%s zero_denominator = false", metric.Name)
		}
		if metric.Passed || metric.Detail.ThresholdPassed {
			t.Fatalf("%s must not pass with a zero denominator: %+v", metric.Name, metric)
		}
		if metric.Detail.Numerator != 0 || metric.Detail.Denominator != 0 {
			t.Fatalf("%s detail = %+v", metric.Name, metric.Detail)
		}
	}
}

func TestEvaluateOmitsVisualMetricWhenCountersAreAbsent(t *testing.T) {
	sample := validSample()
	sample.VisualInjected = nil
	sample.VisualCorrect = nil
	_, metrics := Evaluate(sample, nil, 0.05)
	for _, metric := range metrics {
		if metric.Name == MetricVisualClassificationAccuracy {
			t.Fatalf("visual metric returned without counters")
		}
	}
}

func TestEvaluateVisualMetricUsesThreshold(t *testing.T) {
	sample := validSample()
	sample.VisualInjected = ptrInt(10)
	sample.VisualCorrect = ptrInt(5)
	status, metrics := Evaluate(sample, nil, 0.05)
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	metric := metricByName(t, metrics, MetricVisualClassificationAccuracy)
	if metric.Value != 0.5 || metric.Passed || metric.Detail.Reason != ReasonBelowThreshold {
		t.Fatalf("visual metric = %+v", metric)
	}
}

func TestEvaluateWithoutBaselineHasNoBaselineDetail(t *testing.T) {
	_, metrics := Evaluate(validSample(), nil, 0.05)
	for _, metric := range metrics {
		if metric.Detail.Baseline != nil {
			t.Fatalf("%s baseline = %v, want nil", metric.Name, *metric.Detail.Baseline)
		}
		if metric.Detail.Regressed {
			t.Fatalf("%s regressed without baseline", metric.Name)
		}
	}
}

func TestEvaluateBaselineOnlyAffectsKnownMetric(t *testing.T) {
	baseline := map[string]float64{"unknown_metric": 0.5}
	status, metrics := Evaluate(validSample(), baseline, 0.05)
	if status != StatusPassed {
		t.Fatalf("status = %q, want passed", status)
	}
	for _, metric := range metrics {
		if metric.Detail.Baseline != nil {
			t.Fatalf("%s picked up an unknown baseline", metric.Name)
		}
	}
}

func TestEvaluateUsesRealLLMThresholds(t *testing.T) {
	_, metrics := Evaluate(validSample(), nil, 0.05)
	want := map[string]float64{
		MetricDesignCoverage:               0.80,
		MetricImpactRecall:                 0.70,
		MetricImpactPrecision:              0.60,
		MetricClassificationAccuracy:       0.80,
		MetricIssueFalsePositiveRate:       0.15,
		MetricSupplementHitRate:            0.50,
		MetricVisualClassificationAccuracy: 0.80,
	}
	for name, threshold := range want {
		if got := metricByName(t, metrics, name).Threshold; got != threshold {
			t.Fatalf("%s threshold = %v, want %v", name, got, threshold)
		}
	}
}
