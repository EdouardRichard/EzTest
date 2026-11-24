// Package quality contains the P6 quality domain: the real-LLM evaluation
// sample, the 6+1 metric derivation with report thresholds and baseline
// regression marking, and the operational-metric read model. It is pure: no
// I/O, no storage and no dependency on another bounded context.
package quality

import (
	"errors"
	"math"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// EvalModeRealLLM is the P6 real-LLM evaluation track. The deterministic
// fake-LLM track of 0.1 P8 is intentionally not implemented (spec
// §5.4/P6-D-12).
const EvalModeRealLLM = "real_llm"

// EvalModeSnapshot is the v0.22 admin-triggered snapshot evaluation (W2-14,
// R-W2-6): the server derives four metrics from the version's current real
// product data instead of a client-supplied sample. No new LLM pipeline.
const EvalModeSnapshot = "snapshot"

// Terminal statuses of one eval run (contracts/openapi.yaml EvalRunView).
const (
	StatusRunning = "running"
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusError   = "error"
)

// Metric names of spec §5.3. The impact KPI reports recall and precision
// separately. The v0.22 snapshot track (W2-14) reuses design_coverage, promotes
// the operational execution pass rate into an eval metric and adds case_coverage
// (confirmed case versions over confirmed requirements, capped at 1).
const (
	MetricDesignCoverage         = "design_coverage"
	MetricImpactRecall           = "impact_recall"
	MetricImpactPrecision        = "impact_precision"
	MetricClassificationAccuracy = "classification_accuracy"
	MetricIssueFalsePositiveRate = "issue_false_positive_rate"
	MetricSupplementHitRate      = "supplement_hit_rate"
	MetricExecutionPassRate      = "execution_pass_rate"
	MetricCaseCoverage           = "case_coverage"
)

// Real-LLM report thresholds of spec §5.3. They are observable quality
// signals only; they never block the phase acceptance (P6-D-12).
const (
	ThresholdDesignCoverage         = 0.80
	ThresholdImpactRecall           = 0.70
	ThresholdImpactPrecision        = 0.60
	ThresholdClassificationAccuracy = 0.80
	ThresholdIssueFalsePositiveRate = 0.15
	ThresholdSupplementHitRate      = 0.50
	ThresholdExecutionPassRate      = 0.80
	ThresholdCaseCoverage           = 0.80
)

// Metric failure reasons recorded in MetricDetail (contract enum).
const (
	ReasonOK                 = "ok"
	ReasonBelowThreshold     = "below_threshold"
	ReasonAboveThreshold     = "above_threshold"
	ReasonBaselineRegression = "baseline_regression"
)

// DefaultRegressionTolerance is the settings default of
// quality.eval_baseline_regression (contracts/settings.md).
const DefaultRegressionTolerance = 0.05

// comparisonEpsilon absorbs binary floating error around exact thresholds. It
// is far below the NUMERIC(8,5) precision of the stored values.
const comparisonEpsilon = 1e-9

// ErrNotFound is returned by the eval repository when a run does not exist. The
// application service maps it onto the shared 404 envelope.
var ErrNotFound = errors.New("quality: eval run not found")

// Sample is one real-LLM evaluation observation. It carries counts only: the
// runner derives them from the real API data and the committed ground truth, so
// stored metrics stay auditable without persisting prompts, evidence or corpus
// text.
type Sample struct {
	ConfirmedRequirements  int
	CoveredRequirements    int
	ImpactExpected         int
	ImpactHit              int
	ImpactSelected         int
	InjectedFailures       int
	CorrectClassifications int
	AIIssues               int
	TruePositiveIssues     int
	SupplementCases        int
	SupplementHits         int
}

// SnapshotSample is one server-derived mode=snapshot observation (W2-14). The
// service builds it from the other contexts' read ports, never from a client
// payload: requirements closed/total (version), finished case runs passed/total
// (execution) and confirmed case versions and requirement total (design).
type SnapshotSample struct {
	ConfirmedRequirements int
	CoveredRequirements   int
	ConfirmedCases        int
	ExecutionPassed       int
	ExecutionFinished     int
}

// ValidateSnapshot rejects a physically impossible snapshot observation. It is
// the server-side counterpart of ValidateSample: a count metric may never exceed
// its own denominator.
func ValidateSnapshot(sample SnapshotSample) error {
	nonNegative := []struct {
		field string
		value int
	}{
		{"confirmed_requirements", sample.ConfirmedRequirements},
		{"covered_requirements", sample.CoveredRequirements},
		{"confirmed_cases", sample.ConfirmedCases},
		{"execution_passed", sample.ExecutionPassed},
		{"execution_finished", sample.ExecutionFinished},
	}
	for _, item := range nonNegative {
		if item.value < 0 {
			return platform.Invalid(item.field, "must not be negative")
		}
	}
	if sample.CoveredRequirements > sample.ConfirmedRequirements {
		return platform.Invalid("covered_requirements", "must not exceed confirmed_requirements")
	}
	if sample.ExecutionPassed > sample.ExecutionFinished {
		return platform.Invalid("execution_passed", "must not exceed execution_finished")
	}
	return nil
}

// EvaluateSnapshot derives the three mode=snapshot metrics from one validated
// observation. baseline is the previous snapshot of the same version; it only
// marks regressions (warn-only, never a block). case_coverage is the confirmed
// case-version count over the confirmed requirement count with the numerator
// capped at the denominator (the W2-14 brief: a requirement needs at least one
// confirmed case, and extra case versions cannot push coverage above 100%). The
// status is passed only when all three metrics pass their threshold and none
// regressed.
func EvaluateSnapshot(sample SnapshotSample, baseline map[string]float64, regressionTolerance float64) (string, []Metric) {
	if regressionTolerance < 0 {
		regressionTolerance = 0
	}
	caseNumerator := sample.ConfirmedCases
	if caseNumerator > sample.ConfirmedRequirements {
		caseNumerator = sample.ConfirmedRequirements
	}
	metrics := []Metric{
		evaluateRatio(MetricDesignCoverage, sample.CoveredRequirements, sample.ConfirmedRequirements, baseline, regressionTolerance),
		evaluateRatio(MetricExecutionPassRate, sample.ExecutionPassed, sample.ExecutionFinished, baseline, regressionTolerance),
		evaluateRatio(MetricCaseCoverage, caseNumerator, sample.ConfirmedRequirements, baseline, regressionTolerance),
	}
	status := StatusPassed
	for _, metric := range metrics {
		if !metric.Passed {
			status = StatusFailed
			break
		}
	}
	return status, metrics
}

type MetricDetail struct {
	Numerator       int      `json:"numerator"`
	Denominator     int      `json:"denominator"`
	ThresholdPassed bool     `json:"threshold_passed"`
	Baseline        *float64 `json:"baseline,omitempty"`
	Regressed       bool     `json:"regressed"`
	Reason          string   `json:"reason"`
	ZeroDenominator bool     `json:"zero_denominator"`
}

// Metric is one derived evaluation KPI.
type Metric struct {
	Name      string
	Value     float64
	Threshold float64
	Passed    bool
	Detail    MetricDetail
}

type threshold struct {
	value float64
	// max marks an upper-bound metric (issue false-positive rate). Every other
	// metric has a lower bound.
	max bool
}

var realLLMThresholds = map[string]threshold{
	MetricDesignCoverage:         {value: ThresholdDesignCoverage},
	MetricImpactRecall:           {value: ThresholdImpactRecall},
	MetricImpactPrecision:        {value: ThresholdImpactPrecision},
	MetricClassificationAccuracy: {value: ThresholdClassificationAccuracy},
	MetricIssueFalsePositiveRate: {value: ThresholdIssueFalsePositiveRate, max: true},
	MetricSupplementHitRate:      {value: ThresholdSupplementHitRate},
	MetricExecutionPassRate:      {value: ThresholdExecutionPassRate},
	MetricCaseCoverage:           {value: ThresholdCaseCoverage},
}

// SnapshotMetricOrder is the stable metric order of a mode=snapshot run
// (W2-14): design coverage, execution pass rate and case coverage.
var SnapshotMetricOrder = []string{
	MetricDesignCoverage,
	MetricExecutionPassRate,
	MetricCaseCoverage,
}

// IsKnownMetric reports whether name is a metric this context derives. The
// trends endpoint rejects unknown metric names instead of returning an
// always-empty series.
func IsKnownMetric(name string) bool {
	_, ok := realLLMThresholds[name]
	return ok
}

// ValidateSample rejects a physically impossible observation. The runner must
// never be able to raise a metric by reporting more hits than opportunities.
// Field names match the snake_case request contract so the HTTP layer can
// return 400 details.field without remapping.
func ValidateSample(sample Sample) error {
	nonNegative := []struct {
		field string
		value int
	}{
		{"confirmed_requirements", sample.ConfirmedRequirements},
		{"covered_requirements", sample.CoveredRequirements},
		{"impact_expected", sample.ImpactExpected},
		{"impact_hit", sample.ImpactHit},
		{"impact_selected", sample.ImpactSelected},
		{"injected_failures", sample.InjectedFailures},
		{"correct_classifications", sample.CorrectClassifications},
		{"ai_issues", sample.AIIssues},
		{"true_positive_issues", sample.TruePositiveIssues},
		{"supplement_cases", sample.SupplementCases},
		{"supplement_hits", sample.SupplementHits},
	}
	for _, item := range nonNegative {
		if item.value < 0 {
			return platform.Invalid(item.field, "must not be negative")
		}
	}

	if sample.CoveredRequirements > sample.ConfirmedRequirements {
		return platform.Invalid("covered_requirements", "must not exceed confirmed_requirements")
	}
	if sample.ImpactHit > sample.ImpactExpected {
		return platform.Invalid("impact_hit", "must not exceed impact_expected")
	}
	if sample.ImpactHit > sample.ImpactSelected {
		return platform.Invalid("impact_hit", "must not exceed impact_selected")
	}
	if sample.CorrectClassifications > sample.InjectedFailures {
		return platform.Invalid("correct_classifications", "must not exceed injected_failures")
	}
	if sample.TruePositiveIssues > sample.AIIssues {
		return platform.Invalid("true_positive_issues", "must not exceed ai_issues")
	}
	if sample.SupplementHits > sample.SupplementCases {
		return platform.Invalid("supplement_hits", "must not exceed supplement_cases")
	}
	return nil
}

// Evaluate derives the six metrics from one validated sample and returns the
// run status together with the metrics in stable order. baseline may be nil and
// is only used to mark regressions; regressionTolerance is the configured
// quality.eval_baseline_regression tolerance. A metric with a zero denominator
// gets value 0, zero_denominator=true and never passes: an absent opportunity is
// recorded, never turned into a fake pass (spec §5.3).
func Evaluate(sample Sample, baseline map[string]float64, regressionTolerance float64) (string, []Metric) {
	if regressionTolerance < 0 {
		regressionTolerance = 0
	}
	metrics := make([]Metric, 0, 6)
	metrics = append(metrics,
		evaluateRatio(MetricDesignCoverage, sample.CoveredRequirements, sample.ConfirmedRequirements, baseline, regressionTolerance),
		evaluateRatio(MetricImpactRecall, sample.ImpactHit, sample.ImpactExpected, baseline, regressionTolerance),
		evaluateRatio(MetricImpactPrecision, sample.ImpactHit, sample.ImpactSelected, baseline, regressionTolerance),
		evaluateRatio(MetricClassificationAccuracy, sample.CorrectClassifications, sample.InjectedFailures, baseline, regressionTolerance),
		evaluateRatio(MetricIssueFalsePositiveRate, sample.AIIssues-sample.TruePositiveIssues, sample.AIIssues, baseline, regressionTolerance),
		evaluateRatio(MetricSupplementHitRate, sample.SupplementHits, sample.SupplementCases, baseline, regressionTolerance),
	)
	status := StatusPassed
	for _, metric := range metrics {
		if !metric.Passed {
			status = StatusFailed
			break
		}
	}
	return status, metrics
}

func evaluateRatio(name string, numerator, denominator int, baseline map[string]float64, tolerance float64) Metric {
	rule := realLLMThresholds[name]
	zeroDenominator := denominator <= 0
	value := 0.0
	if !zeroDenominator {
		value = float64(numerator) / float64(denominator)
	}

	// No opportunity means no evidence, so the metric cannot pass even for the
	// upper-bound false-positive rate where a numeric 0 would look ideal.
	thresholdPassed := false
	if !zeroDenominator {
		if rule.max {
			thresholdPassed = value <= rule.value+comparisonEpsilon
		} else {
			thresholdPassed = value+comparisonEpsilon >= rule.value
		}
	}

	var baselineValue *float64
	regressed := false
	if base, ok := baseline[name]; ok {
		baselineCopy := base
		baselineValue = &baselineCopy
		if !zeroDenominator {
			if rule.max {
				regressed = value > base+tolerance+comparisonEpsilon
			} else {
				regressed = value < base-tolerance-comparisonEpsilon
			}
		}
	}

	reason := ReasonOK
	switch {
	case zeroDenominator:
		reason = ReasonBelowThreshold
	case regressed:
		reason = ReasonBaselineRegression
	case !thresholdPassed && rule.max:
		reason = ReasonAboveThreshold
	case !thresholdPassed:
		reason = ReasonBelowThreshold
	}
	// Keep math imported and explicitly reject non-finite values; JSON cannot
	// carry them and a corrupted sample must not produce one.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		value = 0
	}

	return Metric{
		Name:      name,
		Value:     value,
		Threshold: rule.value,
		Passed:    thresholdPassed && !regressed,
		Detail: MetricDetail{
			Numerator:       numerator,
			Denominator:     denominator,
			ThresholdPassed: thresholdPassed,
			Baseline:        baselineValue,
			Regressed:       regressed,
			Reason:          reason,
			ZeroDenominator: zeroDenominator,
		},
	}
}
