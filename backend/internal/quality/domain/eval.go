// Package domain holds the evaluation-run aggregate of the P8 quality platform
// (design spec §9.1/D61): the corpus metrics, their deterministic/report
// thresholds, the regression rule and the persisted run shape. It has no
// infrastructure or HTTP dependency.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Mode is the evaluation track. The deterministic track is the regression gate;
// the real-LLM track is a gated report that never blocks a release (D61).
type Mode string

const (
	// ModeDeterministic runs the fixed corpus against the deterministic fake LLM
	// and executor; its metrics and baseline regression rule are the CI gate.
	ModeDeterministic Mode = "deterministic"
	// ModeRealLLM records a real-model report. Its thresholds are report-only.
	ModeRealLLM Mode = "real_llm"
)

// Status is the terminal state of one eval run.
type Status string

const (
	// StatusRunning is kept for compatibility with the migration CHECK; the
	// runner posts only terminal runs.
	StatusRunning Status = "running"
	// StatusPassed means every metric cleared its threshold and regression rule.
	StatusPassed Status = "passed"
	// StatusFailed means at least one metric failed.
	StatusFailed Status = "failed"
	// StatusError means the scenario itself could not be evaluated; no metric is
	// invented for it.
	StatusError Status = "error"
)

// Metric names of design spec §9.1. The impact KPI reports recall and precision
// separately; the dashboard groups them as one row.
const (
	MetricDesignCoverage         = "design_coverage"
	MetricImpactRecall           = "impact_recall"
	MetricImpactPrecision        = "impact_precision"
	MetricClassificationAccuracy = "classification_accuracy"
	MetricIssueFalsePositiveRate = "issue_false_positive_rate"
	MetricSupplementHitRate      = "supplement_hit_rate"
)

// Metric failure reasons recorded in the metric detail document.
const (
	MetricReasonOK                 = "ok"
	MetricReasonBelowThreshold     = "below_threshold"
	MetricReasonAboveThreshold     = "above_threshold"
	MetricReasonBaselineRegression = "baseline_regression"
)

// RegressionTolerance is the §9.1 rule: no deterministic metric may fall more
// than 0.05 below the committed baseline.
const RegressionTolerance = 0.05

// comparisonEpsilon absorbs binary floating error around exact thresholds; it
// is far below the NUMERIC(8,5) precision of the stored values.
const comparisonEpsilon = 1e-9

// Threshold is one metric's acceptance rule. Max marks a metric that must stay
// at or below Value (the issue false-positive rate); every other metric must
// stay at or above it.
type Threshold struct {
	Value float64
	Max   bool
}

// DeterministicThresholds are the hard gate thresholds of spec §9.1.
var DeterministicThresholds = map[string]Threshold{
	MetricDesignCoverage:         {Value: 0.90},
	MetricImpactRecall:           {Value: 0.90},
	MetricImpactPrecision:        {Value: 0.80},
	MetricClassificationAccuracy: {Value: 1.00},
	MetricIssueFalsePositiveRate: {Value: 0.05, Max: true},
	MetricSupplementHitRate:      {Value: 0.80},
}

// RealLLMThresholds are the report-only thresholds of spec §9.1. The impact
// precision value is a reporting guideline because the spec lists only recall
// as a real-LLM report threshold; it never gates a release.
var RealLLMThresholds = map[string]Threshold{
	MetricDesignCoverage:         {Value: 0.80},
	MetricImpactRecall:           {Value: 0.70},
	MetricImpactPrecision:        {Value: 0.60},
	MetricClassificationAccuracy: {Value: 0.80},
	MetricIssueFalsePositiveRate: {Value: 0.15, Max: true},
	MetricSupplementHitRate:      {Value: 0.50},
}

// ThresholdsFor returns the threshold table of a mode. An unknown mode returns
// the deterministic table (callers validate the mode before evaluation).
func ThresholdsFor(mode Mode) map[string]Threshold {
	if mode == ModeRealLLM {
		return RealLLMThresholds
	}
	return DeterministicThresholds
}

// IsValid reports whether mode is one of the two documented tracks.
func (m Mode) IsValid() bool {
	return m == ModeDeterministic || m == ModeRealLLM
}

// IsTerminal reports whether status is one of the persisted terminal states.
func (s Status) IsTerminal() bool {
	return s == StatusPassed || s == StatusFailed || s == StatusError
}

// Sample is one corpus observation. It carries only counts: the runner derives
// them from the real API data and the committed ground truth, so the stored
// metrics stay auditable without persisting case titles, prompts or evidence.
type Sample struct {
	ConfirmedRequirements  int `json:"confirmedRequirements"`
	CoveredRequirements    int `json:"coveredRequirements"`
	ImpactExpected         int `json:"impactExpected"`
	ImpactHit              int `json:"impactHit"`
	ImpactSelected         int `json:"impactSelected"`
	InjectedFailures       int `json:"injectedFailures"`
	CorrectClassifications int `json:"correctClassifications"`
	AIIssues               int `json:"aiIssues"`
	TruePositiveIssues     int `json:"truePositiveIssues"`
	SupplementCases        int `json:"supplementCases"`
	SupplementHits         int `json:"supplementHits"`
}

// Validate rejects a physically impossible observation. The runner must never
// be able to raise a metric by reporting more hits than opportunities.
func (s Sample) Validate() error {
	nonNegative := []struct {
		field string
		value int
	}{
		{"confirmedRequirements", s.ConfirmedRequirements},
		{"coveredRequirements", s.CoveredRequirements},
		{"impactExpected", s.ImpactExpected},
		{"impactHit", s.ImpactHit},
		{"impactSelected", s.ImpactSelected},
		{"injectedFailures", s.InjectedFailures},
		{"correctClassifications", s.CorrectClassifications},
		{"aiIssues", s.AIIssues},
		{"truePositiveIssues", s.TruePositiveIssues},
		{"supplementCases", s.SupplementCases},
		{"supplementHits", s.SupplementHits},
	}
	for _, item := range nonNegative {
		if item.value < 0 {
			return fmt.Errorf("%s must not be negative", item.field)
		}
	}
	if s.CoveredRequirements > s.ConfirmedRequirements {
		return errors.New("coveredRequirements must not exceed confirmedRequirements")
	}
	if s.ImpactHit > s.ImpactExpected {
		return errors.New("impactHit must not exceed impactExpected")
	}
	if s.ImpactHit > s.ImpactSelected {
		return errors.New("impactHit must not exceed impactSelected")
	}
	if s.CorrectClassifications > s.InjectedFailures {
		return errors.New("correctClassifications must not exceed injectedFailures")
	}
	if s.TruePositiveIssues > s.AIIssues {
		return errors.New("truePositiveIssues must not exceed aiIssues")
	}
	if s.SupplementHits > s.SupplementCases {
		return errors.New("supplementHits must not exceed supplementCases")
	}
	return nil
}

// SampleFieldError is a validated Sample failure that names the offending
// request field for the HTTP layer.
type SampleFieldError struct {
	Field   string
	Message string
}

// Error implements error.
func (e *SampleFieldError) Error() string { return e.Message }

// ValidateFields validates the sample and names the first offending field.
func (s Sample) ValidateFields() error {
	if err := s.Validate(); err != nil {
		return &SampleFieldError{Field: fieldOf(err.Error()), Message: err.Error()}
	}
	return nil
}

// fieldOf maps a Sample.Validate message back to its request field. Keeping the
// mapping here makes the validation messages and the HTTP field names agree.
func fieldOf(message string) string {
	for _, field := range []string{
		"confirmedRequirements", "coveredRequirements", "impactExpected", "impactHit",
		"impactSelected", "injectedFailures", "correctClassifications", "aiIssues",
		"truePositiveIssues", "supplementCases", "supplementHits",
	} {
		if len(message) >= len(field) && message[:len(field)] == field {
			return field
		}
	}
	return "sample"
}

// Metric is one evaluated KPI. Detail carries the numerator/denominator (and
// the baseline when present) so the dashboard and a failing gate can explain
// the verdict without exposing corpus content.
type Metric struct {
	Name      string         `json:"name"`
	Value     float64        `json:"value"`
	Threshold float64        `json:"threshold"`
	Passed    bool           `json:"passed"`
	Detail    map[string]any `json:"detail"`
}

// Run is one persisted evaluation. FinishedAt is nil only for a running run.
type Run struct {
	ID            int64
	Mode          Mode
	CorpusVersion string
	Model         string
	Status        Status
	Summary       map[string]any
	StartedAt     time.Time
	FinishedAt    *time.Time
	Metrics       []Metric
}

// Evaluate derives the six §9.1 metrics from one Sample and applies the mode's
// thresholds. In deterministic mode it additionally applies the +0.05 baseline
// regression rule; the real-LLM report never does (D61). The caller must pass a
// validated sample and a known mode.
func Evaluate(sample Sample, mode Mode, baseline map[string]float64) []Metric {
	thresholds := ThresholdsFor(mode)
	metrics := []Metric{
		metricOf(MetricDesignCoverage, sample.CoveredRequirements, sample.ConfirmedRequirements,
			thresholds, baseline, mode),
		metricOf(MetricImpactRecall, sample.ImpactHit, sample.ImpactExpected,
			thresholds, baseline, mode),
		metricOf(MetricImpactPrecision, sample.ImpactHit, sample.ImpactSelected,
			thresholds, baseline, mode),
		metricOf(MetricClassificationAccuracy, sample.CorrectClassifications, sample.InjectedFailures,
			thresholds, baseline, mode),
		metricOf(MetricIssueFalsePositiveRate, sample.AIIssues-sample.TruePositiveIssues, sample.AIIssues,
			thresholds, baseline, mode),
		metricOf(MetricSupplementHitRate, sample.SupplementHits, sample.SupplementCases,
			thresholds, baseline, mode),
	}
	return metrics
}

// metricOf builds one ratio metric. A zero denominator reports 0.0 rather than
// NaN; the deterministic corpus guarantees non-zero opportunities for every
// gated metric, so the value only appears on the report track where an empty
// scenario is itself a finding.
func metricOf(name string, numerator, denominator int, thresholds map[string]Threshold,
	baseline map[string]float64, mode Mode) Metric {
	value := 0.0
	if denominator > 0 {
		value = float64(numerator) / float64(denominator)
	}
	threshold := thresholds[name]
	thresholdPassed := false
	if threshold.Max {
		thresholdPassed = value <= threshold.Value+comparisonEpsilon
	} else {
		thresholdPassed = value+comparisonEpsilon >= threshold.Value
	}

	detail := map[string]any{
		"numerator":       numerator,
		"denominator":     denominator,
		"thresholdPassed": thresholdPassed,
	}
	regressed := false
	if base, ok := baseline[name]; ok {
		detail["baseline"] = base
		if mode == ModeDeterministic {
			if threshold.Max {
				regressed = value > base+RegressionTolerance+comparisonEpsilon
			} else {
				regressed = value < base-RegressionTolerance-comparisonEpsilon
			}
		}
	}
	detail["regressed"] = regressed

	reason := MetricReasonOK
	switch {
	case regressed:
		reason = MetricReasonBaselineRegression
	case !thresholdPassed && threshold.Max:
		reason = MetricReasonAboveThreshold
	case !thresholdPassed:
		reason = MetricReasonBelowThreshold
	}
	detail["reason"] = reason

	return Metric{
		Name:      name,
		Value:     value,
		Threshold: threshold.Value,
		Passed:    thresholdPassed && !regressed,
		Detail:    detail,
	}
}
