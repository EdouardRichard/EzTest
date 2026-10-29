package agent_tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// candidateProposeInput is the analysis.candidate_propose argument.
type candidateProposeInput struct {
	Candidates []candidateInput `json:"candidates"`
}

// candidateInput is one model-supplied failure candidate.
type candidateInput struct {
	CaseRunID       string         `json:"caseRunId"`
	CaseNodeID      string         `json:"caseNodeId"`
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Severity        string         `json:"severity"`
	Confidence      string         `json:"confidence"`
	FailureClass    string         `json:"failureClass"`
	RerunSuggestion string         `json:"rerunSuggestion"`
	EvidenceIDs     []string       `json:"evidenceIds"`
	CodeRefs        []codeRefInput `json:"codeRefs"`
}

// candidateProposeOutput is the analysis.candidate_propose projection: the
// accumulated candidate count after the submission and all warnings.
type candidateProposeOutput struct {
	CandidateCount int      `json:"candidateCount"`
	Warnings       []string `json:"warnings"`
}

// runCandidatePropose implements analysis.candidate_propose (spec §9.3). The
// tool injects failureClass=application, resolves the case node from the
// batch's failed(application) set, filters foreign evidence ids and malformed
// code refs, and drops invalid candidates with warnings. The analysis service
// still validates and accumulates the submission (T4 materializes).
func runCandidatePropose(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[candidateProposeInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(input.Candidates) == 0 {
		return agentdomain.ToolResult{}, invalidArgumentsf("candidates must contain at least one candidate")
	}
	if len(input.Candidates) > maxCandidatesPerCall {
		return agentdomain.ToolResult{}, invalidArgumentsf(
			"candidates accepts at most %d candidates", maxCandidatesPerCall)
	}
	_, run, err := scopeRunningRun(ctx, deps, domain.AnalysisKindFailure)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	cases, _, err := failedApplicationCases(ctx, deps, run)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	index := caseRunIndex(cases)
	allowed := allowedEvidenceIDs(cases)
	warnings := make([]string, 0)
	valid := make([]domain.FailureCandidate, 0, len(input.Candidates))
	seen := make(map[int64]struct{}, len(input.Candidates))
	for position, candidate := range input.Candidates {
		label := fmt.Sprintf("candidate %d", position+1)
		built, itemWarnings, ok := buildFailureCandidate(candidate, label, index, allowed)
		warnings = append(warnings, itemWarnings...)
		if !ok {
			continue
		}
		if _, duplicate := seen[built.CaseRunID]; duplicate {
			warnings = append(warnings, fmt.Sprintf("%s: duplicate candidate for case_run %d", label, built.CaseRunID))
			continue
		}
		seen[built.CaseRunID] = struct{}{}
		valid = append(valid, built)
	}
	if len(valid) == 0 {
		return jsonResult(candidateProposeOutput{
			CandidateCount: acceptedCandidateCount(run.Result),
			Warnings:       warnings,
		})
	}
	updated, serviceWarnings, err := deps.Submissions.ProposeFailureCandidates(ctx, run.ID, valid)
	if err != nil {
		return agentdomain.ToolResult{}, mapAnalysisError(err)
	}
	warnings = append(warnings, serviceWarnings...)
	return jsonResult(candidateProposeOutput{
		CandidateCount: acceptedCandidateCount(updated.Result),
		Warnings:       warnings,
	})
}

// buildFailureCandidate validates and normalizes one model-supplied candidate.
// Invalid entries are reported through warnings and skipped, never fatal.
func buildFailureCandidate(in candidateInput, label string, cases map[int64]deliveryapp.AnalysisFailedCase, allowedEvidence map[int64]struct{}) (domain.FailureCandidate, []string, bool) {
	warnings := make([]string, 0)
	caseRunID, ok := parseID(in.CaseRunID)
	if !ok {
		return domain.FailureCandidate{}, append(warnings,
			label+": caseRunId must be a positive decimal string"), false
	}
	failedCase, owned := cases[caseRunID]
	if !owned {
		return domain.FailureCandidate{}, append(warnings, fmt.Sprintf(
			"%s: case run %d is not a failed application case run of the batch", label, caseRunID)), false
	}
	caseNodeID := failedCase.CaseNodeID
	if strings.TrimSpace(in.CaseNodeID) != "" {
		parsed, ok := parseID(in.CaseNodeID)
		if !ok {
			return domain.FailureCandidate{}, append(warnings,
				label+": caseNodeId must be a positive decimal string"), false
		}
		if parsed != failedCase.CaseNodeID {
			return domain.FailureCandidate{}, append(warnings, fmt.Sprintf(
				"%s: case node %d does not belong to case run %d", label, parsed, caseRunID)), false
		}
		caseNodeID = parsed
	}
	failureClass := strings.TrimSpace(in.FailureClass)
	if failureClass == "" {
		failureClass = domain.FailureClassApplication
	}
	if failureClass != domain.FailureClassApplication {
		return domain.FailureCandidate{}, append(warnings, fmt.Sprintf(
			"%s: failure class %q is not application", label, failureClass)), false
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.FailureCandidate{}, append(warnings, label+": title must not be empty"), false
	}
	if err := domain.ValidateSeverity(in.Severity); err != nil {
		return domain.FailureCandidate{}, append(warnings, fmt.Sprintf(
			"%s: invalid severity %q", label, in.Severity)), false
	}
	if err := domain.ValidateConfidence(in.Confidence); err != nil {
		return domain.FailureCandidate{}, append(warnings, fmt.Sprintf(
			"%s: invalid confidence %q", label, in.Confidence)), false
	}
	codeRefs, refWarnings := sanitizeCodeRefs(in.CodeRefs, label)
	warnings = append(warnings, refWarnings...)
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
	if len(evidenceIDs) == 0 && len(codeRefs) == 0 {
		return domain.FailureCandidate{}, append(warnings,
			label+": candidate needs at least one evidence id or code reference"), false
	}
	return domain.FailureCandidate{
		CaseNodeID:      caseNodeID,
		CaseRunID:       caseRunID,
		Title:           title,
		Description:     in.Description,
		Severity:        in.Severity,
		Confidence:      in.Confidence,
		FailureClass:    failureClass,
		RerunSuggestion: in.RerunSuggestion,
		CodeRefs:        codeRefs,
		EvidenceIDs:     evidenceIDs,
	}, warnings, true
}

// acceptedCandidateCount decodes the persisted candidate count of a result
// document. A malformed document degrades to zero, never fails the tool.
func acceptedCandidateCount(raw json.RawMessage) int {
	result, err := domain.DecodeFailureResult(raw)
	if err != nil {
		return 0
	}
	return len(result.Candidates)
}

// finishInput is the analysis.finish / triage.finish argument.
type finishInput struct {
	Summary string `json:"summary"`
}

// finishOutput is the explicit end acknowledgement.
type finishOutput struct {
	Finished bool `json:"finished"`
}

// validateFinishSummary enforces the bounded, non-empty summary of the optional
// finish tools.
func validateFinishSummary(raw string) (string, error) {
	summary := strings.TrimSpace(raw)
	if summary == "" {
		return "", invalidArgumentsf("summary must not be empty")
	}
	if utf8.RuneCountInString(summary) > maxFinishSummaryRunes {
		return "", invalidArgumentsf("summary accepts at most %d characters", maxFinishSummaryRunes)
	}
	return summary, nil
}

// runAnalysisFinish implements analysis.finish (spec §9.3). The tool only ends
// the agent turn: the analysis service materializes the accumulated candidates
// when the P1 run finishes, so the tool never writes issues.
func runAnalysisFinish(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[finishInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, err := validateFinishSummary(input.Summary); err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, _, err := scopeRunningRun(ctx, deps, domain.AnalysisKindFailure); err != nil {
		return agentdomain.ToolResult{}, err
	}
	return jsonResult(finishOutput{Finished: true})
}
