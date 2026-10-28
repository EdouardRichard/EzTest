package agent_tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// issueDraftInput is the triage.issue_draft argument.
type issueDraftInput struct {
	Issues []issueDraftItem `json:"issues"`
}

// issueDraftItem is one model-supplied issue draft. Category and failure class
// are not part of the tool contract: the tool injects defect/application (spec
// §9.4, T4 handoff). The dedupe key is optional and derived when absent or
// malformed.
type issueDraftItem struct {
	DedupeKey        string         `json:"dedupeKey"`
	Title            string         `json:"title"`
	Description      string         `json:"description"`
	ReproSteps       string         `json:"reproSteps"`
	Expected         string         `json:"expected"`
	Actual           string         `json:"actual"`
	Severity         string         `json:"severity"`
	Confidence       string         `json:"confidence"`
	CaseRunID        *string        `json:"caseRunId"`
	CaseNodeID       *string        `json:"caseNodeId"`
	EvidenceIDs      []string       `json:"evidenceIds"`
	CodeRefs         []codeRefInput `json:"codeRefs"`
	MergeIntoIssueID *string        `json:"mergeIntoIssueId"`
}

// issueDraftOutput is the triage.issue_draft projection: the accumulated draft
// count after the submission and all warnings.
type issueDraftOutput struct {
	DraftCount int      `json:"draftCount"`
	Warnings   []string `json:"warnings"`
}

// runIssueDraft implements triage.issue_draft (spec §9.4). The tool injects
// category=defect and failureClass=application, resolves the case identity from
// the batch's failed(application) set, filters foreign evidence/merge targets
// and malformed code refs with warnings, and derives the §5.4 dedupe key when
// the model did not supply a well-formed one. The analysis service still
// validates and accumulates the drafts (T4 materializes).
func runIssueDraft(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[issueDraftInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(input.Issues) == 0 {
		return agentdomain.ToolResult{}, invalidArgumentsf("issues must contain at least one draft")
	}
	if len(input.Issues) > maxDraftsPerCall {
		return agentdomain.ToolResult{}, invalidArgumentsf(
			"issues accepts at most %d drafts", maxDraftsPerCall)
	}
	_, run, err := scopeRunningRun(ctx, deps, domain.AnalysisKindIssueTriage)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	triageInput, err := domain.DecodeTriageInput(run.Input)
	if err != nil {
		return agentdomain.ToolResult{}, internalErrorf("decode triage input of run %d: %v", run.ID, err)
	}
	cases, err := failedApplicationCases(ctx, deps, run)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	index := caseRunIndex(cases)
	failedNodes := make(map[int64]struct{}, len(cases))
	for _, failedCase := range cases {
		failedNodes[failedCase.CaseNodeID] = struct{}{}
	}
	allowedEvidence := allowedEvidenceIDs(cases)
	openIssues := openIssueIDs(triageInput.ExistingIssues)

	warnings := make([]string, 0)
	valid := make([]domain.IssueDraft, 0, len(input.Issues))
	for position, item := range input.Issues {
		label := fmt.Sprintf("draft %d", position+1)
		draft, itemWarnings, ok := buildIssueDraft(item, label, run, index, failedNodes, allowedEvidence, openIssues)
		warnings = append(warnings, itemWarnings...)
		if !ok {
			continue
		}
		valid = append(valid, draft)
	}
	if len(valid) == 0 {
		return jsonResult(issueDraftOutput{
			DraftCount: acceptedDraftCount(run.Result),
			Warnings:   warnings,
		})
	}
	updated, serviceWarnings, err := deps.Submissions.ProposeIssueDrafts(ctx, run.ID, valid)
	if err != nil {
		return agentdomain.ToolResult{}, mapAnalysisError(err)
	}
	warnings = append(warnings, serviceWarnings...)
	return jsonResult(issueDraftOutput{
		DraftCount: acceptedDraftCount(updated.Result),
		Warnings:   warnings,
	})
}

// buildIssueDraft validates and normalizes one model-supplied draft. Structural
// errors drop the draft with a warning; evidence/code/merge anomalies are
// stripped with a warning and the draft still lands.
func buildIssueDraft(in issueDraftItem, label string, run domain.AnalysisRun, cases map[int64]deliveryapp.AnalysisFailedCase, failedNodes map[int64]struct{}, allowedEvidence map[int64]struct{}, openIssues map[int64]struct{}) (domain.IssueDraft, []string, bool) {
	warnings := make([]string, 0)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.IssueDraft{}, append(warnings, label+": title must not be empty"), false
	}
	if err := domain.ValidateSeverity(in.Severity); err != nil {
		return domain.IssueDraft{}, append(warnings, fmt.Sprintf("%s: invalid severity %q", label, in.Severity)), false
	}
	if err := domain.ValidateConfidence(in.Confidence); err != nil {
		return domain.IssueDraft{}, append(warnings, fmt.Sprintf("%s: invalid confidence %q", label, in.Confidence)), false
	}

	caseRunID, ok := parseOptionalID(in.CaseRunID)
	if !ok {
		return domain.IssueDraft{}, append(warnings, label+": caseRunId must be a positive decimal string"), false
	}
	caseNodeID, ok := parseOptionalID(in.CaseNodeID)
	if !ok {
		return domain.IssueDraft{}, append(warnings, label+": caseNodeId must be a positive decimal string"), false
	}
	resolvedNode := int64(0)
	if caseRunID > 0 {
		failedCase, owned := cases[caseRunID]
		if !owned {
			return domain.IssueDraft{}, append(warnings, fmt.Sprintf(
				"%s: case run %d is not a failed application case run of the batch", label, caseRunID)), false
		}
		resolvedNode = failedCase.CaseNodeID
		if caseNodeID > 0 && caseNodeID != resolvedNode {
			return domain.IssueDraft{}, append(warnings, fmt.Sprintf(
				"%s: case node %d does not belong to case run %d", label, caseNodeID, caseRunID)), false
		}
	} else if caseNodeID > 0 {
		if _, owned := failedNodes[caseNodeID]; !owned {
			return domain.IssueDraft{}, append(warnings, fmt.Sprintf(
				"%s: case node %d is not a failed application case of the batch", label, caseNodeID)), false
		}
		resolvedNode = caseNodeID
	} else {
		return domain.IssueDraft{}, append(warnings,
			label+": a draft needs a caseRunId or caseNodeId"), false
	}

	evidenceIDs := make([]int64, 0, len(in.EvidenceIDs))
	for _, raw := range in.EvidenceIDs {
		evidenceID, ok := parseID(raw)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: evidence id %q is not a positive decimal id", label, raw))
			continue
		}
		if _, owned := allowedEvidence[evidenceID]; !owned {
			warnings = append(warnings, fmt.Sprintf(
				"%s: evidence %d does not belong to a failed application case of the batch", label, evidenceID))
			continue
		}
		evidenceIDs = append(evidenceIDs, evidenceID)
	}
	codeRefs, refWarnings := sanitizeCodeRefs(in.CodeRefs, label)
	warnings = append(warnings, refWarnings...)
	// Warn about missing proof only when the model supplied none: a draft whose
	// evidence ids were all foreign already carries that warning.
	if len(in.EvidenceIDs) == 0 && len(in.CodeRefs) == 0 {
		warnings = append(warnings,
			label+": draft has no evidence id or code reference; the issue will carry no supporting evidence")
	}

	var mergeInto *int64
	if mergeID, ok := parseOptionalID(in.MergeIntoIssueID); !ok {
		warnings = append(warnings, label+": mergeIntoIssueId must be a positive decimal string")
	} else if mergeID > 0 {
		if _, open := openIssues[mergeID]; !open {
			warnings = append(warnings, fmt.Sprintf(
				"%s: mergeIntoIssueId %d is not an open issue of the version", label, mergeID))
		} else {
			mergeInto = &mergeID
		}
	}

	dedupeKey := strings.TrimSpace(in.DedupeKey)
	if domain.ValidateDedupeKey(dedupeKey) != nil {
		dedupeKey = domain.FailureDedupeKey(run.VersionID, resolvedNode, domain.FailureClassApplication, title)
	}

	draft := domain.IssueDraft{
		DedupeKey:        dedupeKey,
		Title:            title,
		Description:      in.Description,
		ReproSteps:       in.ReproSteps,
		Expected:         in.Expected,
		Actual:           in.Actual,
		Severity:         in.Severity,
		Confidence:       in.Confidence,
		Category:         domain.CategoryDefect,
		FailureClass:     domain.FailureClassApplication,
		CaseNodeID:       &resolvedNode,
		EvidenceIDs:      evidenceIDs,
		CodeRefs:         codeRefs,
		MergeIntoIssueID: mergeInto,
	}
	if caseRunID > 0 {
		draft.CaseRunID = &caseRunID
	}
	return draft, warnings, true
}

// acceptedDraftCount decodes the persisted draft count of a triage result
// document. A malformed document degrades to zero, never fails the tool.
func acceptedDraftCount(raw json.RawMessage) int {
	result, err := domain.DecodeTriageResult(raw)
	if err != nil {
		return 0
	}
	return len(result.Drafts)
}

// runTriageFinish implements triage.finish (spec §9.4). Like analysis.finish it
// only ends the agent turn; materialization stays in the analysis service.
func runTriageFinish(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[finishInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, err := validateFinishSummary(input.Summary); err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, _, err := scopeRunningRun(ctx, deps, domain.AnalysisKindIssueTriage); err != nil {
		return agentdomain.ToolResult{}, err
	}
	return jsonResult(finishOutput{Finished: true})
}
