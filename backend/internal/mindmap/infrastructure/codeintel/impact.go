package codeintel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
)

// P4 impact diagnostic codes the adapter projects into P4ImpactSummary.Warnings.
// They describe the P4 result itself, not the plan's own fallback: the starter
// still records impact_unavailable when the result is not consumable. The
// diagnostics keep a truncated/degraded/unreadable result distinguishable from a
// version that never had an analysis (spec §6.1 step 3a/3b/§7.6).
const (
	impactWarningUnmappedTruncated  = "unmapped_truncated"
	impactWarningDegraded           = "impact_degraded"
	impactWarningStatsUnreadable    = "impact_stats_unreadable"
	impactWarningAnalysisPending    = "impact_analysis_pending"
	impactWarningAnalysisFailed     = "impact_analysis_failed"
	impactWarningAnalysisIncomplete = "impact_analysis_incomplete"
)

// LatestAnalysisReader is the narrow slice of the P4 analysis service the impact
// summary adapter needs. *codeintel/application.AnalysisService satisfies it.
type LatestAnalysisReader interface {
	GetLatest(ctx context.Context, versionID int64) (codeinteldomain.CodeAnalysis, error)
}

// ImpactSummaryAdapter implements application.P4ImpactReader on top of the P4
// analysis read (spec §6.1 step 3b/§7.6). It only projects the latest analysis;
// the mindmap run starter owns the seeding decision and the impact_unavailable
// warning, while the adapter supplies the P4 diagnostics (truncation, degraded
// state, unavailable status, unreadable stats) so they are never silently lost.
type ImpactSummaryAdapter struct {
	analyses LatestAnalysisReader
}

// NewImpactSummaryAdapter wires the adapter to the P4 analysis read.
func NewImpactSummaryAdapter(analyses LatestAnalysisReader) *ImpactSummaryAdapter {
	return &ImpactSummaryAdapter{analyses: analyses}
}

var _ mindmapapp.P4ImpactReader = (*ImpactSummaryAdapter)(nil)

// impactAnalysisStats is the subset of the stored P4 stats document that changes
// the mindmap projection. The document carries more counters (candidates, prompt
// sizes, cache hits); only the truncation and AI-unavailable flags matter here.
type impactAnalysisStats struct {
	UnmappedTruncated bool `json:"unmappedTruncated"`
	AIUnavailable     bool `json:"aiUnavailable"`
}

// LatestImpactSummary returns the latest P4 impact projection of a version. A
// version without an analysis is the documented zero summary with a nil error. A
// completed but degraded analysis keeps its unmapped changes for diagnostics yet
// reports Available=false: spec §6.1 step 3a records impact_unavailable for a
// degraded P4 result. An available analysis whose unmapped list was truncated
// stays Available=true and carries the unmapped_truncated warning, so the plan
// never presents a silently truncated list as complete.
func (a *ImpactSummaryAdapter) LatestImpactSummary(ctx context.Context, versionID int64) (mindmapapp.P4ImpactSummary, error) {
	if a == nil || a.analyses == nil {
		return mindmapapp.P4ImpactSummary{}, errors.New("mindmap: analysis reader is not configured")
	}
	analysis, err := a.analyses.GetLatest(ctx, versionID)
	if err != nil {
		if isCodeIntelNotFound(err) {
			return mindmapapp.P4ImpactSummary{}, nil
		}
		return mindmapapp.P4ImpactSummary{}, mapCodeIntelError(err)
	}
	summary := mindmapapp.P4ImpactSummary{
		Degraded:        analysis.Degraded,
		UnmappedChanges: make([]mindmapapp.P4UnmappedChange, 0, len(analysis.Unmapped)),
	}
	if analysis.ID > 0 {
		summary.Available = analysis.Status == codeinteldomain.RunDone && !analysis.Degraded
		summary.Warnings = impactAnalysisWarnings(analysis)
	}
	for _, change := range analysis.Unmapped {
		summary.UnmappedChanges = append(summary.UnmappedChanges, mindmapapp.P4UnmappedChange{
			Path:    change.Path,
			Summary: impactUnmappedSummary(change),
		})
	}
	return summary, nil
}

// impactAnalysisWarnings projects the P4 diagnostics of one analysis: a
// non-consumable status, the degraded flag and the stats truncation flags. The
// stats document is read defensively: an unreadable document warns instead of
// failing the run start, and an empty document is simply no diagnostic.
func impactAnalysisWarnings(analysis codeinteldomain.CodeAnalysis) []mindmapapp.IncrementalPlanWarning {
	warnings := make([]mindmapapp.IncrementalPlanWarning, 0, 4)
	switch analysis.Status {
	case codeinteldomain.RunDone:
		// A done analysis either is available or is degraded; the degraded
		// diagnostic below distinguishes the latter from "no analysis".
	case codeinteldomain.RunQueued, codeinteldomain.RunRunning:
		warnings = append(warnings, mindmapapp.IncrementalPlanWarning{
			Code: impactWarningAnalysisPending,
			Message: fmt.Sprintf(
				"the latest P4 impact analysis is still %s; the incremental design plan starts without it",
				impactStatusLabel(analysis.Status)),
		})
	case codeinteldomain.RunFailed:
		warnings = append(warnings, mindmapapp.IncrementalPlanWarning{
			Code:    impactWarningAnalysisFailed,
			Message: "the latest P4 impact analysis failed; the incremental design plan starts without it",
		})
	default:
		warnings = append(warnings, mindmapapp.IncrementalPlanWarning{
			Code: impactWarningAnalysisIncomplete,
			Message: fmt.Sprintf(
				"the latest P4 impact analysis has status %q; the incremental design plan starts without it",
				impactStatusLabel(analysis.Status)),
		})
	}

	stats, statsErr := impactStatsOf(analysis.Stats)
	if analysis.Degraded {
		message := "the latest P4 impact analysis finished degraded; the incremental design plan starts without it"
		if statsErr == nil && stats.AIUnavailable {
			message = "the latest P4 impact analysis finished degraded because the AI step was unavailable; the incremental design plan starts without it"
		}
		warnings = append(warnings, mindmapapp.IncrementalPlanWarning{
			Code:    impactWarningDegraded,
			Message: message,
		})
	}
	if statsErr != nil {
		warnings = append(warnings, mindmapapp.IncrementalPlanWarning{
			Code:    impactWarningStatsUnreadable,
			Message: "the P4 analysis stats document could not be read; truncation and degradation diagnostics may be incomplete",
		})
	} else if stats.UnmappedTruncated {
		warnings = append(warnings, mindmapapp.IncrementalPlanWarning{
			Code: impactWarningUnmappedTruncated,
			Message: fmt.Sprintf(
				"the P4 unmapped change list was truncated to the first %d entries; some changed files are not listed",
				codeinteldomain.MaxUnmappedEntries),
		})
	}
	return warnings
}

// impactStatsOf decodes the stored P4 stats document. An empty document is valid
// (no diagnostics); a malformed one is returned as an error so the caller can
// surface a stats-unreadable warning without failing the run start.
func impactStatsOf(raw []byte) (impactAnalysisStats, error) {
	var stats impactAnalysisStats
	if len(strings.TrimSpace(string(raw))) == 0 {
		return stats, nil
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		return impactAnalysisStats{}, err
	}
	return stats, nil
}

// impactStatusLabel is the status fallback of the diagnostics: an empty recorded
// status is reported as unknown instead of producing a blank message.
func impactStatusLabel(status string) string {
	if strings.TrimSpace(status) == "" {
		return "unknown"
	}
	return status
}

// impactUnmappedSummary renders the plan-facing summary of one unmapped change.
// It prefers the P4 actionable suggestion, then the stable reason code, then the
// change status; a change without any of them still gets a neutral summary.
func impactUnmappedSummary(change codeinteldomain.UnmappedChange) string {
	switch {
	case strings.TrimSpace(change.Suggestion) != "":
		return change.Suggestion
	case strings.TrimSpace(change.Reason) != "":
		return change.Reason
	case strings.TrimSpace(change.Status) != "":
		return change.Status
	default:
		return "changed file"
	}
}
