package httpapi

import (
	"encoding/json"
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// VisualAssertionView is the GET/PUT visual assertion projection (spec §4.2
// #30–#32). It never carries actual/diff bytes.
type VisualAssertionView struct {
	ID                string                 `json:"id"`
	VersionID         string                 `json:"version_id"`
	CaseVersionID     string                 `json:"case_version_id"`
	CaseAssertionID   string                 `json:"case_assertion_id"`
	StepSeq           int                    `json:"step_seq"`
	AssertionSeq      int                    `json:"assertion_seq"`
	VersionSUTID      string                 `json:"version_sut_id"`
	Selector          string                 `json:"selector"`
	Region            json.RawMessage        `json:"region"`
	Threshold         float64                `json:"threshold"`
	ViewportWidth     int                    `json:"viewport_width"`
	ViewportHeight    int                    `json:"viewport_height"`
	DeviceScaleFactor float64                `json:"device_scale_factor"`
	WaitPolicy        json.RawMessage        `json:"wait_policy"`
	ConfigRevision    int                    `json:"config_revision"`
	Status            string                 `json:"status"`
	Baseline          *VisualBaselineSummary `json:"baseline"`
	CreatedAt         string                 `json:"created_at"`
	UpdatedAt         string                 `json:"updated_at"`
}

// VisualBaselineSummary is the active baseline block of an assertion view.
type VisualBaselineSummary struct {
	ID                     string `json:"id"`
	SHA256                 string `json:"sha256"`
	Width                  int    `json:"width"`
	Height                 int    `json:"height"`
	ConfigRevision         int    `json:"config_revision"`
	EnvironmentFingerprint string `json:"environment_fingerprint"`
	ApprovedAt             string `json:"approved_at"`
}

// VisualBaselineView is the baseline history projection (spec §4.2 #33/#34).
type VisualBaselineView struct {
	ID                     string  `json:"id"`
	VisualAssertionID      string  `json:"visual_assertion_id"`
	CaseVersionID          string  `json:"case_version_id"`
	VersionSUTID           string  `json:"version_sut_id"`
	EvidenceID             string  `json:"evidence_id"`
	SHA256                 string  `json:"sha256"`
	SizeBytes              int64   `json:"size_bytes"`
	Width                  int     `json:"width"`
	Height                 int     `json:"height"`
	ViewportWidth          int     `json:"viewport_width"`
	ViewportHeight         int     `json:"viewport_height"`
	DeviceScaleFactor      float64 `json:"device_scale_factor"`
	ConfigRevision         int     `json:"config_revision"`
	EnvironmentFingerprint string  `json:"environment_fingerprint"`
	Status                 string  `json:"status"`
	ApprovedBy             string  `json:"approved_by"`
	ApprovedAt             string  `json:"approved_at"`
	SupersededAt           *string `json:"superseded_at"`
	CreatedAt              string  `json:"created_at"`
}

// VisualDiffView is the comparison summary projection (spec §4.2 #35).
type VisualDiffView struct {
	ID                string  `json:"id"`
	VisualAssertionID string  `json:"visual_assertion_id"`
	CaseRunID         string  `json:"case_run_id"`
	AssertionResultID string  `json:"assertion_result_id"`
	BaselineID        *string `json:"baseline_id"`
	ActualEvidenceID  string  `json:"actual_evidence_id"`
	DiffEvidenceID    *string `json:"diff_evidence_id"`
	DiffRatio         float64 `json:"diff_ratio"`
	Threshold         float64 `json:"threshold"`
	Classification    string  `json:"classification"`
	Reason            string  `json:"reason"`
	EnvironmentMatch  bool    `json:"environment_match"`
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	CreatedAt         string  `json:"created_at"`
}

// VisualMismatchBox is one coarse differing rectangle.
type VisualMismatchBox struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// VisualEvidenceRef is the environment block of a diff detail. It is nullable
// because P5 stores no environment snapshot on visual_diff.
type VisualEvidenceRef struct {
	ViewportWidth     int     `json:"viewport_width"`
	ViewportHeight    int     `json:"viewport_height"`
	DeviceScaleFactor float64 `json:"device_scale_factor"`
	Browser           *string `json:"browser"`
	Version           *string `json:"version"`
}

// VisualDiffDetailView is the comparison detail projection (spec §4.2 #36).
type VisualDiffDetailView struct {
	VisualDiffView
	MismatchBoxes []VisualMismatchBox `json:"mismatch_boxes"`
	Environment   *VisualEvidenceRef  `json:"environment"`
}

// VisualInconclusiveReason is one inconclusive reason bucket.
type VisualInconclusiveReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// VisualDiffLatency is the nullable diff latency block. P5 does not store a
// comparison duration, so the view renders null instead of a fabricated value.
type VisualDiffLatency struct {
	P50MS int `json:"p50_ms"`
	P95MS int `json:"p95_ms"`
}

// VisualOverviewView is the version overview projection (spec §4.2 #37/§7.5).
type VisualOverviewView struct {
	VersionID             string                     `json:"version_id"`
	AssertionTotal        int                        `json:"assertion_total"`
	BaselineCovered       int                        `json:"baseline_covered"`
	BaselineCoverageRatio *float64                   `json:"baseline_coverage_ratio"`
	Passed                int                        `json:"passed"`
	Failed                int                        `json:"failed"`
	Inconclusive          int                        `json:"inconclusive"`
	Blocked               int                        `json:"blocked"`
	PassRatio             *float64                   `json:"pass_ratio"`
	InconclusiveReasons   []VisualInconclusiveReason `json:"inconclusive_reasons"`
	DiffLatencyMS         *VisualDiffLatency         `json:"diff_latency_ms"`
	DiffRatioP50          *float64                   `json:"diff_ratio_p50"`
	DiffRatioP95          *float64                   `json:"diff_ratio_p95"`
	RecentFailures        []VisualDiffView           `json:"recent_failures"`
}

// VisualMetricTotals is the quality metrics totals block.
type VisualMetricTotals struct {
	Assertions   int `json:"assertions"`
	Baselines    int `json:"baselines"`
	Diffs        int `json:"diffs"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Inconclusive int `json:"inconclusive"`
	Blocked      int `json:"blocked"`
}

// VisualMetricRatios is the quality metrics ratios block.
type VisualMetricRatios struct {
	BaselineCoverage *float64 `json:"baseline_coverage"`
	PassRate         *float64 `json:"pass_rate"`
	InconclusiveRate *float64 `json:"inconclusive_rate"`
}

// VisualMetricsScope is the metrics scope block.
type VisualMetricsScope struct {
	VersionID *string `json:"version_id"`
}

// VisualMetricsView is GET /api/quality/visual-metrics (spec §4.2 #38).
type VisualMetricsView struct {
	Scope                  VisualMetricsScope         `json:"scope"`
	Totals                 VisualMetricTotals         `json:"totals"`
	Ratios                 VisualMetricRatios         `json:"ratios"`
	DiffLatencyMS          *VisualDiffLatency         `json:"diff_latency_ms"`
	TopInconclusiveReasons []VisualInconclusiveReason `json:"top_inconclusive_reasons"`
	GeneratedAt            string                     `json:"generated_at"`
}

func toAssertionView(assertion visual.Assertion) VisualAssertionView {
	return VisualAssertionView{
		ID: platformid.Format(assertion.ID), VersionID: platformid.Format(assertion.VersionID),
		CaseVersionID:   platformid.Format(assertion.CaseVersionID),
		CaseAssertionID: platformid.Format(assertion.CaseAssertionID),
		StepSeq:         assertion.StepSeq, AssertionSeq: assertion.AssertionSeq,
		VersionSUTID: platformid.Format(assertion.VersionSUTID), Selector: assertion.Selector,
		Region: rawJSONObject(assertion.Region), Threshold: assertion.Threshold,
		ViewportWidth: assertion.ViewportWidth, ViewportHeight: assertion.ViewportHeight,
		DeviceScaleFactor: assertion.DeviceScaleFactor,
		WaitPolicy:        rawJSONObject(assertion.WaitPolicy), ConfigRevision: assertion.ConfigRevision,
		Status: string(assertion.Status), CreatedAt: formatTime(assertion.CreatedAt),
		UpdatedAt: formatTime(assertion.UpdatedAt),
	}
}

func toBaselineSummary(baseline visual.Baseline) VisualBaselineSummary {
	return VisualBaselineSummary{
		ID: platformid.Format(baseline.ID), SHA256: baseline.SHA256,
		Width: baseline.Width, Height: baseline.Height, ConfigRevision: baseline.ConfigRevision,
		EnvironmentFingerprint: baseline.EnvironmentFingerprint,
		ApprovedAt:             formatTime(baseline.ApprovedAt),
	}
}

func toBaselineView(baseline visual.Baseline) VisualBaselineView {
	return VisualBaselineView{
		ID:                platformid.Format(baseline.ID),
		VisualAssertionID: platformid.Format(baseline.VisualAssertionID),
		CaseVersionID:     platformid.Format(baseline.CaseVersionID),
		VersionSUTID:      platformid.Format(baseline.VersionSUTID),
		EvidenceID:        platformid.Format(baseline.EvidenceID), SHA256: baseline.SHA256,
		SizeBytes: baseline.SizeBytes, Width: baseline.Width, Height: baseline.Height,
		ViewportWidth: baseline.ViewportWidth, ViewportHeight: baseline.ViewportHeight,
		DeviceScaleFactor: baseline.DeviceScaleFactor, ConfigRevision: baseline.ConfigRevision,
		EnvironmentFingerprint: baseline.EnvironmentFingerprint, Status: string(baseline.Status),
		ApprovedBy:   platformid.Format(baseline.ApprovedBy),
		ApprovedAt:   formatTime(baseline.ApprovedAt),
		SupersededAt: formatOptionalTime(baseline.SupersededAt),
		CreatedAt:    formatTime(baseline.CreatedAt),
	}
}

func toDiffView(diff visual.Diff) VisualDiffView {
	view := VisualDiffView{
		ID: platformid.Format(diff.ID), VisualAssertionID: platformid.Format(diff.VisualAssertionID),
		CaseRunID:         platformid.Format(diff.CaseRunID),
		AssertionResultID: platformid.Format(diff.AssertionResultID),
		ActualEvidenceID:  platformid.Format(diff.ActualEvidenceID), DiffRatio: diff.DiffRatio,
		Threshold: diff.Threshold, Classification: string(diff.Classification), Reason: diff.Reason,
		EnvironmentMatch: diff.EnvironmentMatch, Width: diff.Width, Height: diff.Height,
		CreatedAt: formatTime(diff.CreatedAt),
	}
	if diff.BaselineID != nil {
		value := platformid.Format(*diff.BaselineID)
		view.BaselineID = &value
	}
	if diff.DiffEvidenceID != nil {
		value := platformid.Format(*diff.DiffEvidenceID)
		view.DiffEvidenceID = &value
	}
	return view
}

func toDiffDetailView(diff visual.Diff) VisualDiffDetailView {
	boxes, err := visual.ValidateMismatchBoxes(diff.MismatchBoxes)
	if err != nil {
		boxes = nil
	}
	views := make([]VisualMismatchBox, 0, len(boxes))
	for _, box := range boxes {
		views = append(views, VisualMismatchBox{X: box.X, Y: box.Y, Width: box.Width, Height: box.Height})
	}
	return VisualDiffDetailView{VisualDiffView: toDiffView(diff), MismatchBoxes: views, Environment: nil}
}

func toOverviewView(overview visualapp.Overview) VisualOverviewView {
	reasons := make([]VisualInconclusiveReason, 0, len(overview.InconclusiveReasons))
	for _, reason := range overview.InconclusiveReasons {
		reasons = append(reasons, VisualInconclusiveReason{Reason: reason.Reason, Count: reason.Count})
	}
	failures := make([]VisualDiffView, 0, len(overview.RecentFailures))
	for _, diff := range overview.RecentFailures {
		failures = append(failures, toDiffView(diff))
	}
	return VisualOverviewView{
		VersionID:      platformid.Format(overview.VersionID),
		AssertionTotal: overview.AssertionTotal, BaselineCovered: overview.BaselineCovered,
		BaselineCoverageRatio: overview.BaselineCoverageRatio,
		Passed:                overview.Passed, Failed: overview.Failed,
		Inconclusive: overview.Inconclusive, Blocked: overview.Blocked,
		PassRatio: overview.PassRatio, InconclusiveReasons: reasons,
		DiffLatencyMS: nil, DiffRatioP50: overview.DiffRatioP50, DiffRatioP95: overview.DiffRatioP95,
		RecentFailures: failures,
	}
}

func toMetricsView(metrics visualapp.Metrics) VisualMetricsView {
	reasons := make([]VisualInconclusiveReason, 0, len(metrics.TopInconclusiveReasons))
	for _, reason := range metrics.TopInconclusiveReasons {
		reasons = append(reasons, VisualInconclusiveReason{Reason: reason.Reason, Count: reason.Count})
	}
	scope := VisualMetricsScope{}
	if metrics.VersionID != nil {
		value := platformid.Format(*metrics.VersionID)
		scope.VersionID = &value
	}
	return VisualMetricsView{
		Scope: scope,
		Totals: VisualMetricTotals{
			Assertions: metrics.Assertions, Baselines: metrics.Baselines, Diffs: metrics.Diffs,
			Passed: metrics.Passed, Failed: metrics.Failed,
			Inconclusive: metrics.Inconclusive, Blocked: metrics.Blocked,
		},
		Ratios: VisualMetricRatios{
			BaselineCoverage: metrics.BaselineCoverage, PassRate: metrics.PassRate,
			InconclusiveRate: metrics.InconclusiveRate,
		},
		DiffLatencyMS: nil, TopInconclusiveReasons: reasons,
		GeneratedAt: formatTime(metrics.GeneratedAt),
	}
}

func rawJSONObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func formatID(id int64) string { return platformid.Format(id) }

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}
