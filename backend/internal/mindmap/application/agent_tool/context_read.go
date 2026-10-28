package agent_tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// The two run modes of spec §6.4.1.
const (
	supplementModePostBatch      = "post_batch"
	supplementModeIncremental    = "incremental_design"
	contextWarningImpactUnavail  = "impact_unavailable"
	contextWarningResultsUnavail = "results_unavailable"
)

// contextReadInput is the validated argument shape of supplement.context_read.
type contextReadInput struct {
	Mode           string   `json:"mode"`
	BatchID        string   `json:"batchId"`
	RequirementIDs []string `json:"requirementIds"`
}

// contextReadResult is the read-only context payload. All ids are decimal
// strings (D29); no evidence body, credential or prompt is ever included.
type contextReadResult struct {
	VersionID     string                `json:"versionId"`
	BatchID       string                `json:"batchId"`
	RunID         string                `json:"runId"`
	RunKind       string                `json:"runKind"`
	Mode          string                `json:"mode"`
	Requirements  []contextRequirement  `json:"requirements"`
	Cases         []contextCase         `json:"cases"`
	Coverage      contextCoverage       `json:"coverage"`
	Impact        *contextImpact        `json:"impact"`
	RecentResults *contextRecentResults `json:"recentResults"`
	Warnings      []warning             `json:"warnings"`
}

// contextRequirement is one clarified requirement summary.
type contextRequirement struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Priority           string `json:"priority,omitempty"`
	Status             string `json:"status,omitempty"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptanceCriteria,omitempty"`
}

// contextCase is one existing case summary with its requirement sources.
type contextCase struct {
	NodeID         string   `json:"nodeId"`
	Title          string   `json:"title"`
	Priority       string   `json:"priority,omitempty"`
	Status         string   `json:"status"`
	Source         string   `json:"source"`
	StepCount      int      `json:"stepCount"`
	AssertionCount int      `json:"assertionCount"`
	RequirementIDs []string `json:"requirementIds"`
}

// contextCoverage is the requirement coverage matrix summary.
type contextCoverage struct {
	RequirementIDs          []string              `json:"requirementIds"`
	UncoveredRequirementIDs []string              `json:"uncoveredRequirementIds"`
	Matrix                  []contextCoverageCell `json:"matrix"`
}

// contextCoverageCell is one covered requirement row.
type contextCoverageCell struct {
	RequirementID string   `json:"requirementId"`
	CaseIDs       []string `json:"caseIds"`
}

// contextImpact is the latest P4 impact summary.
type contextImpact struct {
	AnalysisID string                   `json:"analysisId"`
	BaseCommit string                   `json:"baseCommit"`
	HeadCommit string                   `json:"headCommit"`
	Status     string                   `json:"status"`
	Degraded   bool                     `json:"degraded"`
	Candidates []contextImpactCandidate `json:"candidates"`
}

// contextImpactCandidate is one P4 impact candidate.
type contextImpactCandidate struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subjectKind"`
	SubjectID   string `json:"subjectId"`
	Reason      string `json:"reason"`
	Confidence  string `json:"confidence"`
	Source      string `json:"source"`
}

// contextRecentResults is the latest terminal batch result summary.
type contextRecentResults struct {
	BatchID      string              `json:"batchId"`
	Status       string              `json:"status"`
	Passed       int                 `json:"passed"`
	Failed       int                 `json:"failed"`
	Blocked      int                 `json:"blocked"`
	Inconclusive int                 `json:"inconclusive"`
	Skipped      int                 `json:"skipped"`
	Cases        []contextRecentCase `json:"cases"`
}

// contextRecentCase is one non-passed case of the recent batch.
type contextRecentCase struct {
	CaseNodeID   string `json:"caseNodeId"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	FailureClass string `json:"failureClass,omitempty"`
}

// runContextRead executes supplement.context_read (spec §9.2/§6.4.2): the
// confirmed requirements, current cases, coverage matrix, recent result summary
// and P4 impact summary of the calling run.
func runContextRead(ctx context.Context, deps Deps, args json.RawMessage) (agentdomain.ToolResult, error) {
	run, err := resolveRun(ctx, deps)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[contextReadInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	mode := strings.TrimSpace(input.Mode)
	if mode != supplementModePostBatch && mode != supplementModeIncremental {
		return agentdomain.ToolResult{}, invalidArguments("mode must be post_batch or incremental_design")
	}
	if mode != runMode(run.Kind) {
		return agentdomain.ToolResult{}, invalidArguments(
			fmt.Sprintf("mode %q does not match the %s run", mode, run.Kind))
	}
	if strings.TrimSpace(input.BatchID) != "" {
		batchID, err := parsePositiveID(input.BatchID)
		if err != nil {
			return agentdomain.ToolResult{}, invalidArguments("batchId must be a positive decimal string")
		}
		if batchID != runBatchID(run) {
			return agentdomain.ToolResult{}, invalidArguments("batchId does not match the parent batch of this run")
		}
	}

	view, err := contextGraphView(ctx, deps, run.VersionID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	result := contextReadResult{
		VersionID: strconv.FormatInt(run.VersionID, 10),
		BatchID:   strconv.FormatInt(runBatchID(run), 10),
		RunID:     strconv.FormatInt(run.ID, 10),
		RunKind:   run.Kind,
		Mode:      mode,
		Warnings:  make([]warning, 0, 2),
	}
	requirements, err := contextRequirements(ctx, deps, run.VersionID, input.RequirementIDs)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	result.Requirements = requirements
	result.Cases = contextCases(view)
	result.Coverage = contextCoverageOf(view.Coverage)
	result.Impact, result.Warnings = contextImpactOf(ctx, deps, run.VersionID, result.Warnings)
	result.RecentResults, result.Warnings = contextRecentResultsOf(ctx, deps, run.VersionID, runBatchID(run), result.Warnings)
	return jsonResult(result)
}

// runMode returns the spec §6.4.1 mode of one supplement run kind.
func runMode(kind string) string {
	if kind == mindmapapp.RunKindIncrementalDesign {
		return supplementModeIncremental
	}
	return supplementModePostBatch
}

// contextRequirements loads the requested (or all) clarified requirement
// summaries, capped at 20.
func contextRequirements(ctx context.Context, deps Deps, versionID int64, requested []string) ([]contextRequirement, error) {
	snapshots, err := deps.Requirements.ListConfirmed(ctx, versionID)
	if err != nil {
		slog.ErrorContext(ctx, "mindmap: supplement context_read could not read the clarified requirements",
			"version_id", versionID, "error", err)
		return nil, internalError("the clarified requirements could not be read")
	}
	wanted := make(map[int64]struct{}, len(requested))
	order := make([]int64, 0, len(requested))
	for _, raw := range requested {
		id, err := parsePositiveID(raw)
		if err != nil {
			return nil, invalidArguments("requirementIds must be positive decimal strings")
		}
		if _, duplicate := wanted[id]; duplicate {
			continue
		}
		wanted[id] = struct{}{}
		order = append(order, id)
	}
	byID := make(map[int64]mindmapapp.RequirementSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byID[snapshot.ID] = snapshot
	}
	ids := order
	if len(ids) == 0 {
		ids = make([]int64, 0, len(snapshots))
		for _, snapshot := range snapshots {
			ids = append(ids, snapshot.ID)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	requirements := make([]contextRequirement, 0, len(ids))
	for _, id := range ids {
		snapshot, ok := byID[id]
		if !ok {
			continue
		}
		if len(requirements) >= maxContextRequirements {
			break
		}
		requirements = append(requirements, contextRequirement{
			ID:                 strconv.FormatInt(snapshot.ID, 10),
			Title:              snapshot.Title,
			Priority:           snapshot.Priority,
			Status:             snapshot.Status,
			Description:        truncateRunes(snapshot.Description, maxContextFieldRunes),
			AcceptanceCriteria: truncateRunes(snapshot.AcceptanceCriteria, maxContextFieldRunes),
		})
	}
	return requirements, nil
}

// contextCases renders the current cases of the graph view, capped at 300.
func contextCases(view mindmapapp.GraphView) []contextCase {
	cases := make([]contextCase, 0, len(view.Nodes))
	for _, graphNode := range view.Nodes {
		if graphNode.Node.Kind != mindmapdomain.NodeKindCase {
			continue
		}
		if len(cases) >= maxContextCases {
			break
		}
		requirementIDs := make([]int64, 0)
		for _, source := range graphNode.Sources {
			if source.RefKind == mindmapdomain.RefRequirement && source.RefID != nil {
				requirementIDs = append(requirementIDs, *source.RefID)
			}
		}
		sort.Slice(requirementIDs, func(i, j int) bool { return requirementIDs[i] < requirementIDs[j] })
		cases = append(cases, contextCase{
			NodeID:         strconv.FormatInt(graphNode.Node.ID, 10),
			Title:          graphNode.Node.Title,
			Priority:       graphNode.Node.Priority,
			Status:         graphNode.Node.Status,
			Source:         graphNode.Node.Source,
			StepCount:      len(graphNode.Steps),
			AssertionCount: len(graphNode.Assertions),
			RequirementIDs: formatIDs(requirementIDs),
		})
	}
	return cases
}

// contextCoverageOf renders the requirement coverage matrix.
func contextCoverageOf(coverage mindmapdomain.Coverage) contextCoverage {
	rendered := contextCoverage{
		RequirementIDs:          formatIDs(coverage.RequirementIDs),
		UncoveredRequirementIDs: formatIDs(coverage.UncoveredRequirementIDs),
		Matrix:                  make([]contextCoverageCell, 0, len(coverage.Matrix)),
	}
	if rendered.RequirementIDs == nil {
		rendered.RequirementIDs = []string{}
	}
	if rendered.UncoveredRequirementIDs == nil {
		rendered.UncoveredRequirementIDs = []string{}
	}
	for _, cell := range coverage.Matrix {
		rendered.Matrix = append(rendered.Matrix, contextCoverageCell{
			RequirementID: strconv.FormatInt(cell.RequirementID, 10),
			CaseIDs:       formatIDs(cell.CaseIDs),
		})
	}
	return rendered
}

// contextImpactOf renders the latest P4 impact summary, or a warning when no P4
// analysis is available (spec §9.7 degraded mode).
func contextImpactOf(ctx context.Context, deps Deps, versionID int64, warnings []warning) (*contextImpact, []warning) {
	if deps.Impact == nil {
		return nil, append(warnings, warning{
			Code:    contextWarningImpactUnavail,
			Message: "no P4 impact reader is configured for this version",
		})
	}
	snapshot, err := deps.Impact.GetLatestAnalysis(ctx, versionID)
	if err != nil || snapshot.AnalysisID <= 0 {
		return nil, append(warnings, warning{
			Code:    contextWarningImpactUnavail,
			Message: "no P4 impact result is available for this version",
		})
	}
	impact := &contextImpact{
		AnalysisID: strconv.FormatInt(snapshot.AnalysisID, 10),
		BaseCommit: snapshot.BaseCommit,
		HeadCommit: snapshot.HeadCommit,
		Status:     snapshot.Status,
		Degraded:   snapshot.Degraded,
		Candidates: make([]contextImpactCandidate, 0),
	}
	candidates, err := deps.Impact.ListCandidates(ctx, snapshot.AnalysisID)
	if err != nil {
		return nil, append(warnings, warning{
			Code:    contextWarningImpactUnavail,
			Message: "the P4 impact candidates could not be read",
		})
	}
	for _, candidate := range candidates {
		if len(impact.Candidates) >= maxContextCandidates {
			break
		}
		impact.Candidates = append(impact.Candidates, contextImpactCandidate{
			ID:          strconv.FormatInt(candidate.ID, 10),
			SubjectKind: candidate.SubjectKind,
			SubjectID:   strconv.FormatInt(candidate.SubjectID, 10),
			Reason:      truncateRunes(candidate.Reason, maxContextFieldRunes),
			Confidence:  candidate.Confidence,
			Source:      candidate.Source,
		})
	}
	return impact, warnings
}

// contextRecentResultsOf renders the recent batch result summary, or a warning
// when the optional reader is not wired.
func contextRecentResultsOf(ctx context.Context, deps Deps, versionID, batchID int64, warnings []warning) (*contextRecentResults, []warning) {
	if batchID <= 0 {
		return nil, warnings
	}
	if deps.Results == nil {
		return nil, append(warnings, warning{
			Code:    contextWarningResultsUnavail,
			Message: "no execution result reader is configured for this version",
		})
	}
	summary, err := deps.Results.LatestBatchResults(ctx, versionID, batchID)
	if err != nil {
		return nil, append(warnings, warning{
			Code:    contextWarningResultsUnavail,
			Message: "the recent batch result summary could not be read",
		})
	}
	recent := &contextRecentResults{
		BatchID:      strconv.FormatInt(summary.BatchID, 10),
		Status:       summary.Status,
		Passed:       summary.Passed,
		Failed:       summary.Failed,
		Blocked:      summary.Blocked,
		Inconclusive: summary.Inconclusive,
		Skipped:      summary.Skipped,
		Cases:        make([]contextRecentCase, 0, len(summary.Cases)),
	}
	for index, recentCase := range summary.Cases {
		if index >= maxContextCases {
			break
		}
		recent.Cases = append(recent.Cases, contextRecentCase{
			CaseNodeID:   strconv.FormatInt(recentCase.CaseNodeID, 10),
			Title:        recentCase.Title,
			Status:       recentCase.Status,
			FailureClass: recentCase.FailureClass,
		})
	}
	return recent, warnings
}

// formatIDs renders ids as decimal strings with a stable order.
func formatIDs(ids []int64) []string {
	rendered := make([]string, 0, len(ids))
	for _, id := range ids {
		rendered = append(rendered, strconv.FormatInt(id, 10))
	}
	return rendered
}

// truncateRunes cuts value to at most max runes (UTF-8 safe).
func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
