package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Report narrative bounds (contracts/agent-profiles-tools.md §7.3).
const (
	reportNarrativeSummaryMax    = 4000
	reportNarrativeHighlightsMax = 20
	reportNarrativeHighlightMax  = 500
)

// ErrReportNotPublished reports a report task whose current revision is still
// draft. The agent runner maps it to a retryable task failure.
var ErrReportNotPublished = errors.New("delivery: report revision is not published")

// ReportNarrativeDraft is the run-local narrative candidate proposed through
// report.narrative_propose. It is staged for the publish step instead of
// publishing directly; the report writer never invokes ReportService.Publish.
type ReportNarrativeDraft struct {
	ReportID        int64
	Revision        int32
	SummaryMarkdown string
	Highlights      []string
}

// reportNarrativeDrafts is the process-local staging area of narrative
// candidates. Delivery is single-API-instance (like the issue/report
// repositories); a lost draft only makes the report task retry, never publishes
// wrong statistics.
var reportNarrativeDrafts sync.Map

func reportNarrativeKey(reportID int64, revision int32) string {
	return platformid.Format(reportID) + ":" + platformid.Format(int64(revision))
}

// stageReportNarrative records the latest narrative candidate of a report
// revision.
func stageReportNarrative(draft ReportNarrativeDraft) {
	reportNarrativeDrafts.Store(reportNarrativeKey(draft.ReportID, draft.Revision), draft)
}

// TakeReportNarrative removes and returns the narrative staged for one report
// revision. The publish step (composition root) consumes it; it is deliberately
// not part of the model-facing tool result.
func TakeReportNarrative(reportID int64, revision int32) (ReportNarrativeDraft, bool) {
	value, ok := reportNarrativeDrafts.LoadAndDelete(reportNarrativeKey(reportID, revision))
	if !ok {
		return ReportNarrativeDraft{}, false
	}
	draft, ok := value.(ReportNarrativeDraft)
	return draft, ok
}

// ReportTools returns the frozen P4 report-writer tool group (spec §4.4 /
// contracts/agent-profiles-tools.md §7.3). The tools only ever receive the
// runtime-injected tools.Scope: their JSON schemas never expose the report or
// version IDs. The composition root (Task 23) registers this group through
// tools.RegisterP4ToolGroup with a captured ReportService.
func ReportTools(svc *ReportService) []agenttools.Tool {
	return []agenttools.Tool{
		&reportDataReadTool{svc: svc},
		&reportNarrativeProposeTool{svc: svc},
	}
}

func reportToolSchema(properties map[string]any, required ...string) json.RawMessage {
	if properties == nil {
		properties = map[string]any{}
	}
	document := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		document["required"] = required
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
	return raw
}

// --- report.data_read -------------------------------------------------------

type reportDataReadTool struct{ svc *ReportService }

func (t *reportDataReadTool) Name() string { return agenttools.ToolReportDataRead }

func (t *reportDataReadTool) Description() string {
	return "Read the deterministic summary of the current report task: IDs, section/status counts, coverage counts and truncation markers. Never returns titles, narrative text, evidence bytes/hashes, credentials or filesystem paths. Read-only."
}

func (t *reportDataReadTool) Parameters() json.RawMessage { return reportToolSchema(nil) }

func (t *reportDataReadTool) Execute(ctx context.Context, scope agenttools.Scope, raw json.RawMessage) (any, error) {
	if t.svc == nil {
		return nil, errors.New("report.data_read has no report service")
	}
	var params struct{}
	if err := agenttools.DecodeP4Params(raw, &params); err != nil {
		return nil, err
	}
	report, revision, err := t.svc.reportToolTarget(ctx, scope)
	if err != nil {
		return nil, err
	}
	snapshot, err := t.svc.snapshotFor(ctx, report)
	if err != nil {
		return nil, mapReportToolInternal(err)
	}
	return reportSnapshotSummary(report, revision.Revision, snapshot), nil
}

// reportSnapshotSummary renders the counts/IDs-only projection of one report
// snapshot. Titles, summaries, narrative and evidence metadata deliberately do
// not cross the tool boundary.
func reportSnapshotSummary(report delivery.Report, revision int32, snapshot delivery.ReportSnapshot) map[string]any {
	requirementStatus := make(map[string]int)
	for _, requirement := range snapshot.Requirements {
		requirementStatus[requirement.Status]++
	}
	waveStatus := make(map[string]int)
	for _, wave := range snapshot.Waves {
		waveStatus[wave.Status]++
	}
	caseRunStatus := make(map[string]int)
	caseRunFailureClass := make(map[string]int)
	evidenceCount := 0
	for _, run := range snapshot.CaseRuns {
		caseRunStatus[run.Status]++
		if run.FailureClass != "" {
			caseRunFailureClass[run.FailureClass]++
		}
		evidenceCount += len(run.Evidence)
	}
	issueStatus := make(map[string]int)
	issueSeverity := make(map[string]int)
	issueCategory := make(map[string]int)
	for _, issue := range snapshot.Issues {
		issueStatus[string(issue.Status)]++
		issueSeverity[issue.Severity]++
		issueCategory[issue.Category]++
	}
	gaps := make([]map[string]any, 0, len(snapshot.Coverage.Gaps))
	for _, gap := range snapshot.Coverage.Gaps {
		gaps = append(gaps, map[string]any{
			"requirement_id":  platformid.Format(gap.RequirementID),
			"case_version_id": platformid.Format(gap.CaseVersionID),
			"reason":          gap.Reason,
		})
	}
	versionSUTIDs := make([]string, 0, len(snapshot.VersionSUTs))
	for _, sut := range snapshot.VersionSUTs {
		if sut.ID > 0 {
			versionSUTIDs = append(versionSUTIDs, platformid.Format(sut.ID))
		}
	}
	byVersionSUT := make([]map[string]any, 0)
	for _, wave := range snapshot.Waves {
		for _, counts := range wave.ByVersionSUT {
			byVersionSUT = append(byVersionSUT, map[string]any{
				"version_sut_id": platformid.Format(counts.VersionSUTID),
				"total":          counts.Total,
				"passed":         counts.Passed,
				"failed":         counts.Failed,
				"blocked":        counts.Blocked,
				"inconclusive":   counts.Inconclusive,
			})
		}
	}
	truncated := snapshot.TruncatedSections
	if truncated == nil {
		truncated = []string{}
	}
	result := map[string]any{
		"report_id":  platformid.Format(report.ID),
		"version_id": platformid.Format(report.VersionID),
		"kind":       string(report.Kind),
		"revision":   revision,
		"requirements": map[string]any{
			"total":     len(snapshot.Requirements),
			"by_status": requirementStatus,
		},
		"waves": map[string]any{
			"total":          len(snapshot.Waves),
			"by_status":      waveStatus,
			"by_version_sut": byVersionSUT,
		},
		"case_runs": map[string]any{
			"total":            len(snapshot.CaseRuns),
			"by_status":        caseRunStatus,
			"by_failure_class": caseRunFailureClass,
		},
		"issues": map[string]any{
			"total":       len(snapshot.Issues),
			"by_status":   issueStatus,
			"by_severity": issueSeverity,
			"by_category": issueCategory,
		},
		"coverage": map[string]any{
			"requirements_total":    snapshot.Coverage.RequirementsTotal,
			"requirements_covered":  snapshot.Coverage.RequirementsCovered,
			"case_versions_total":   snapshot.Coverage.CaseVersionsTotal,
			"case_versions_covered": snapshot.Coverage.CaseVersionsCovered,
			"gaps":                  gaps,
		},
		"version_sut_ids":    versionSUTIDs,
		"evidence":           map[string]any{"count": evidenceCount},
		"truncated_sections": truncated,
	}
	if report.WaveID != nil && *report.WaveID > 0 {
		result["wave_id"] = platformid.Format(*report.WaveID)
	}
	return result
}

// --- report.narrative_propose ----------------------------------------------

type reportNarrativeProposeTool struct{ svc *ReportService }

func (t *reportNarrativeProposeTool) Name() string { return agenttools.ToolReportNarrativePropose }

func (t *reportNarrativeProposeTool) Description() string {
	return "Submit the plain-text narrative candidate of the current report revision (summary_markdown 1..4000 characters plus optional highlights). The narrative is staged for the report publish step; statistical numbers, coverage and evidence remain server-computed, and this tool never publishes the report."
}

func (t *reportNarrativeProposeTool) Parameters() json.RawMessage {
	return reportToolSchema(map[string]any{
		"summary_markdown": map[string]any{
			"type":      "string",
			"minLength": 1,
			"maxLength": reportNarrativeSummaryMax,
		},
		"highlights": map[string]any{
			"type":     "array",
			"maxItems": reportNarrativeHighlightsMax,
			"items": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": reportNarrativeHighlightMax,
			},
		},
	})
}

func (t *reportNarrativeProposeTool) Execute(ctx context.Context, scope agenttools.Scope, raw json.RawMessage) (any, error) {
	if t.svc == nil {
		return nil, errors.New("report.narrative_propose has no report service")
	}
	var params struct {
		SummaryMarkdown string   `json:"summary_markdown"`
		Highlights      []string `json:"highlights"`
	}
	if err := agenttools.DecodeP4Params(raw, &params); err != nil {
		return nil, err
	}
	report, revision, err := t.svc.reportToolTarget(ctx, scope)
	if err != nil {
		return nil, err
	}
	if _, err := t.svc.repo.GetRevision(ctx, report.ID, revision.Revision); err != nil {
		return nil, mapReportToolError(err)
	}
	summary := strings.TrimSpace(params.SummaryMarkdown)
	if summary == "" {
		return nil, agenttools.Invalid("summary_markdown", "must not be empty")
	}
	if len([]rune(summary)) > reportNarrativeSummaryMax {
		return nil, agenttools.Invalid("summary_markdown", fmt.Sprintf("must be at most %d characters", reportNarrativeSummaryMax))
	}
	if len(params.Highlights) > reportNarrativeHighlightsMax {
		return nil, agenttools.Invalid("highlights", fmt.Sprintf("must contain at most %d items", reportNarrativeHighlightsMax))
	}
	highlights := make([]string, 0, len(params.Highlights))
	for _, rawHighlight := range params.Highlights {
		highlight := strings.TrimSpace(rawHighlight)
		if highlight == "" {
			return nil, agenttools.Invalid("highlights", "items must not be empty")
		}
		if len([]rune(highlight)) > reportNarrativeHighlightMax {
			return nil, agenttools.Invalid("highlights", fmt.Sprintf("items must be at most %d characters", reportNarrativeHighlightMax))
		}
		highlights = append(highlights, highlight)
	}
	stageReportNarrative(ReportNarrativeDraft{
		ReportID:        report.ID,
		Revision:        revision.Revision,
		SummaryMarkdown: summary,
		Highlights:      highlights,
	})
	return map[string]any{
		"report_id":        platformid.Format(report.ID),
		"revision":         revision.Revision,
		"accepted":         true,
		"summary_chars":    len([]rune(summary)),
		"highlights_count": len(highlights),
	}, nil
}

// --- finalizer / publish verification --------------------------------------

// RequirePublished verifies that the numbered revision of a report is
// published. It is the check behind the report task finalizer: the report
// renderer/publisher owns the actual publish, and a report.* run that finished
// without a published revision is retried by the runner.
func (s *ReportService) RequirePublished(ctx context.Context, reportID int64, revision int32) error {
	if s == nil || s.repo == nil {
		return reportServiceNotConfigured("report repository")
	}
	if reportID <= 0 {
		return fmt.Errorf("%w: report id must be a positive decimal ID", delivery.ErrInvalidArgument)
	}
	if revision <= 0 {
		return fmt.Errorf("%w: revision must be a positive integer", delivery.ErrInvalidArgument)
	}
	report, err := s.repo.GetReportByID(ctx, reportID)
	if err != nil {
		return err
	}
	stored, err := s.repo.GetRevision(ctx, reportID, revision)
	if err != nil {
		return err
	}
	if stored.Status != delivery.ReportStatusPublished ||
		report.CurrentRevisionID == nil || *report.CurrentRevisionID != stored.ID {
		return fmt.Errorf("%w: report %d revision %d", ErrReportNotPublished, reportID, revision)
	}
	return nil
}

// ReportFinalizer checks a report.wave/report.version run's revision. It
// implements the agent runner's ReportFinalizer port structurally; a draft
// revision is an error so the work item is retried.
type ReportFinalizer struct{ svc *ReportService }

// NewReportFinalizer wraps a report service as the runner finalizer.
func NewReportFinalizer(svc *ReportService) *ReportFinalizer {
	return &ReportFinalizer{svc: svc}
}

// Finalize requires the current revision to be published.
func (f *ReportFinalizer) Finalize(ctx context.Context, reportID int64, revision int32) error {
	if f == nil || f.svc == nil {
		return reportServiceNotConfigured("report service")
	}
	return f.svc.RequirePublished(ctx, reportID, revision)
}

// reportToolTarget resolves and validates the runtime scope of a report tool:
// the report must belong to the injected version and, for wave reports, match
// the injected wave. The numbered task revision must exist on that report.
func (s *ReportService) reportToolTarget(ctx context.Context, scope agenttools.Scope) (delivery.Report, delivery.ReportRevision, error) {
	if scope.VersionID <= 0 {
		return delivery.Report{}, delivery.ReportRevision{}, agenttools.Invalid("scope", "report tool requires the injected version scope")
	}
	if scope.ReportID <= 0 {
		return delivery.Report{}, delivery.ReportRevision{}, agenttools.Invalid("scope", "report tool requires the injected report scope")
	}
	if scope.ReportRevision <= 0 {
		return delivery.Report{}, delivery.ReportRevision{}, agenttools.Invalid("scope", "report tool requires the injected report revision")
	}
	report, err := s.repo.GetReport(ctx, scope.VersionID, scope.ReportID)
	if err != nil {
		return delivery.Report{}, delivery.ReportRevision{}, mapReportToolError(err)
	}
	switch report.Kind {
	case delivery.ReportKindWave:
		// The report row is the authority for its wave; a diverging injected
		// wave is an out-of-scope read. A task without wave_id still reads the
		// report's own deterministic snapshot.
		if report.WaveID == nil {
			return delivery.Report{}, delivery.ReportRevision{}, agenttools.NotFound("report")
		}
		if scope.WaveID > 0 && *report.WaveID != scope.WaveID {
			return delivery.Report{}, delivery.ReportRevision{}, agenttools.NotFound("report")
		}
	case delivery.ReportKindVersion:
		if scope.WaveID > 0 {
			return delivery.Report{}, delivery.ReportRevision{}, agenttools.NotFound("report")
		}
	}
	revision, err := s.repo.GetRevision(ctx, report.ID, scope.ReportRevision)
	if err != nil {
		return delivery.Report{}, delivery.ReportRevision{}, mapReportToolError(err)
	}
	return report, revision, nil
}

// mapReportToolError turns report sentinels into model-visible tool errors.
func mapReportToolError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrReportNotFound):
		return agenttools.NotFound("report revision")
	case errors.Is(err, delivery.ErrInvalidArgument):
		return agenttools.Invalid("", err.Error())
	case errors.Is(err, delivery.ErrStateConflict):
		return agenttools.StateConflict("report is not in a valid state")
	default:
		return mapReportToolInternal(err)
	}
}

func mapReportToolInternal(err error) error {
	if err == nil {
		return nil
	}
	var toolErr *agenttools.Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	return fmt.Errorf("report tool: %w", err)
}
