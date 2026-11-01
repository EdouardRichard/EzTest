package httpapi

import (
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

/* --------------------------------- requests -------------------------------- */

// createEvalRunRequest is the POST /api/quality/eval-runs body (frozen P6
// contract). Unknown fields are intentionally ignored (0.2 convention), so a
// client-sent verdict/passed field is ignored instead of rejected. The sample
// counts are validated again by the domain, which owns the physical rules.
type createEvalRunRequest struct {
	Mode          string            `json:"mode"`
	CorpusVersion string            `json:"corpus_version"`
	Model         string            `json:"model"`
	Status        string            `json:"status"`
	Sample        *evalSampleRequest `json:"sample"`
	Baseline      map[string]float64 `json:"baseline"`
	Summary       map[string]any     `json:"summary"`
	StartedAt     *time.Time         `json:"started_at"`
	FinishedAt    *time.Time         `json:"finished_at"`
}

type evalSampleRequest struct {
	ConfirmedRequirements  int  `json:"confirmed_requirements"`
	CoveredRequirements    int  `json:"covered_requirements"`
	ImpactExpected         int  `json:"impact_expected"`
	ImpactHit              int  `json:"impact_hit"`
	ImpactSelected         int  `json:"impact_selected"`
	InjectedFailures       int  `json:"injected_failures"`
	CorrectClassifications int  `json:"correct_classifications"`
	AIIssues               int  `json:"ai_issues"`
	TruePositiveIssues     int  `json:"true_positive_issues"`
	SupplementCases        int  `json:"supplement_cases"`
	SupplementHits         int  `json:"supplement_hits"`
	VisualInjected         *int `json:"visual_injected"`
	VisualCorrect          *int `json:"visual_correct"`
}

func (r createEvalRunRequest) toInput() qualityapp.RecordInput {
	input := qualityapp.RecordInput{
		Mode:          r.Mode,
		CorpusVersion: r.CorpusVersion,
		Model:         r.Model,
		Status:        r.Status,
		Baseline:      r.Baseline,
		Summary:       r.Summary,
		StartedAt:     r.StartedAt,
		FinishedAt:    r.FinishedAt,
	}
	if r.Sample != nil {
		input.Sample = &quality.Sample{
			ConfirmedRequirements:  r.Sample.ConfirmedRequirements,
			CoveredRequirements:    r.Sample.CoveredRequirements,
			ImpactExpected:         r.Sample.ImpactExpected,
			ImpactHit:              r.Sample.ImpactHit,
			ImpactSelected:         r.Sample.ImpactSelected,
			InjectedFailures:       r.Sample.InjectedFailures,
			CorrectClassifications: r.Sample.CorrectClassifications,
			AIIssues:               r.Sample.AIIssues,
			TruePositiveIssues:     r.Sample.TruePositiveIssues,
			SupplementCases:        r.Sample.SupplementCases,
			SupplementHits:         r.Sample.SupplementHits,
			VisualInjected:         r.Sample.VisualInjected,
			VisualCorrect:          r.Sample.VisualCorrect,
		}
	}
	return input
}

/* ---------------------------------- views ---------------------------------- */

type evalMetricDetailView struct {
	Numerator       int      `json:"numerator"`
	Denominator     int      `json:"denominator"`
	ThresholdPassed bool     `json:"threshold_passed"`
	Baseline        *float64 `json:"baseline,omitempty"`
	Regressed       bool     `json:"regressed"`
	Reason          string   `json:"reason"`
	ZeroDenominator bool     `json:"zero_denominator"`
}

type evalMetricView struct {
	Name      string              `json:"name"`
	Value     float64             `json:"value"`
	Threshold float64             `json:"threshold"`
	Passed    bool                `json:"passed"`
	Detail    evalMetricDetailView `json:"detail"`
}

type evalRunView struct {
	ID            string           `json:"id"`
	Mode          string           `json:"mode"`
	CorpusVersion string           `json:"corpus_version"`
	Model         string           `json:"model"`
	Status        string           `json:"status"`
	Summary       map[string]any   `json:"summary"`
	Metrics       []evalMetricView `json:"metrics"`
	StartedAt     string           `json:"started_at"`
	FinishedAt    *string          `json:"finished_at"`
	CreatedAt     string           `json:"created_at"`
}

type overviewScopeView struct {
	VersionID *string `json:"version_id"`
}

type operationalMetricView struct {
	Name        string   `json:"name"`
	Value       *float64 `json:"value"`
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Available   bool     `json:"available"`
	Definition  string   `json:"definition"`
}

type evalSummaryView struct {
	ID            string `json:"id"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	MetricsCount  int    `json:"metrics_count"`
	MetricsPassed int    `json:"metrics_passed"`
}

type qualityOverviewView struct {
	Scope       overviewScopeView       `json:"scope"`
	GeneratedAt string                  `json:"generated_at"`
	Operational []operationalMetricView `json:"operational"`
	LatestEval  *evalSummaryView        `json:"latest_eval"`
}

func toEvalRunView(run qualityapp.EvalRun) evalRunView {
	view := evalRunView{
		ID:            platformid.Format(run.ID),
		Mode:          run.Mode,
		CorpusVersion: run.CorpusVersion,
		Model:         run.Model,
		Status:        run.Status,
		Summary:       run.Summary,
		Metrics:       make([]evalMetricView, 0, len(run.Metrics)),
		StartedAt:     formatTime(run.StartedAt),
		CreatedAt:     formatTime(run.CreatedAt),
	}
	if view.Summary == nil {
		view.Summary = map[string]any{}
	}
	if run.FinishedAt != nil {
		finished := formatTime(*run.FinishedAt)
		view.FinishedAt = &finished
	}
	for _, metric := range run.Metrics {
		view.Metrics = append(view.Metrics, toEvalMetricView(metric))
	}
	return view
}

func toEvalMetricView(metric quality.Metric) evalMetricView {
	return evalMetricView{
		Name:      metric.Name,
		Value:     metric.Value,
		Threshold: metric.Threshold,
		Passed:    metric.Passed,
		Detail: evalMetricDetailView{
			Numerator:       metric.Detail.Numerator,
			Denominator:     metric.Detail.Denominator,
			ThresholdPassed: metric.Detail.ThresholdPassed,
			Baseline:        metric.Detail.Baseline,
			Regressed:       metric.Detail.Regressed,
			Reason:          metric.Detail.Reason,
			ZeroDenominator: metric.Detail.ZeroDenominator,
		},
	}
}

func toQualityOverviewView(overview qualityapp.Overview) qualityOverviewView {
	view := qualityOverviewView{
		GeneratedAt: formatTime(overview.GeneratedAt),
		Operational: make([]operationalMetricView, 0, len(overview.Operational)),
	}
	if overview.ScopeVersionID != nil {
		scopeID := platformid.Format(*overview.ScopeVersionID)
		view.Scope.VersionID = &scopeID
	}
	for _, metric := range overview.Operational {
		view.Operational = append(view.Operational, operationalMetricView{
			Name:        metric.Name,
			Value:       metric.Value,
			Numerator:   metric.Numerator,
			Denominator: metric.Denominator,
			Available:   metric.Available,
			Definition:  metric.Definition,
		})
	}
	if overview.LatestEval != nil {
		view.LatestEval = &evalSummaryView{
			ID:            platformid.Format(overview.LatestEval.ID),
			Mode:          overview.LatestEval.Mode,
			Status:        overview.LatestEval.Status,
			CreatedAt:     formatTime(overview.LatestEval.CreatedAt),
			MetricsCount:  overview.LatestEval.MetricsCount,
			MetricsPassed: overview.LatestEval.MetricsPassed,
		}
	}
	return view
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
