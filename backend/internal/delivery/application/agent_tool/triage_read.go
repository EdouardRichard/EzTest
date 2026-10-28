package agent_tool

import (
	"context"
	"encoding/json"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// emptyInput is the argument of the tools that take no parameters.
type emptyInput struct{}

// triageCandidateListOutput is the triage.candidate_list projection: the failure
// candidates copied into the triage run input.
type triageCandidateListOutput struct {
	VersionID  string                    `json:"versionId"`
	BatchID    string                    `json:"batchId"`
	Candidates []domain.FailureCandidate `json:"candidates"`
}

// runTriageCandidateList implements triage.candidate_list (spec §9.4). It reads
// the candidates from the triage run's own input; the model cannot point it at
// another run or batch.
func runTriageCandidateList(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	if _, err := decodeArguments[emptyInput](args); err != nil {
		return agentdomain.ToolResult{}, err
	}
	_, run, err := scopeRun(ctx, deps, domain.AnalysisKindIssueTriage)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := domain.DecodeTriageInput(run.Input)
	if err != nil {
		return agentdomain.ToolResult{}, internalErrorf("decode triage input of run %d: %v", run.ID, err)
	}
	candidates := input.Candidates
	if candidates == nil {
		candidates = []domain.FailureCandidate{}
	}
	return jsonResult(triageCandidateListOutput{
		VersionID:  idString(run.VersionID),
		BatchID:    idString(run.BatchID),
		Candidates: candidates,
	})
}

// triageIssueListOutput is the triage.issue_list projection. The dedupe key and
// the description stay hidden (spec §9.4).
type triageIssueListOutput struct {
	Issues []openIssueValue `json:"issues"`
}

// openIssueValue is one open issue summary.
type openIssueValue struct {
	IssueID  string `json:"issueId"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

// runTriageIssueList implements triage.issue_list (spec §9.4): the open issue
// summaries captured in the triage input.
func runTriageIssueList(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	if _, err := decodeArguments[emptyInput](args); err != nil {
		return agentdomain.ToolResult{}, err
	}
	_, run, err := scopeRun(ctx, deps, domain.AnalysisKindIssueTriage)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := domain.DecodeTriageInput(run.Input)
	if err != nil {
		return agentdomain.ToolResult{}, internalErrorf("decode triage input of run %d: %v", run.ID, err)
	}
	issues := make([]openIssueValue, 0, len(input.ExistingIssues))
	for _, issue := range input.ExistingIssues {
		issues = append(issues, openIssueValue{
			IssueID:  idString(issue.IssueID),
			Title:    issue.Title,
			Severity: issue.Severity,
		})
	}
	return jsonResult(triageIssueListOutput{Issues: issues})
}

// triageContextInput is the triage.context_read argument.
type triageContextInput struct {
	CaseRunIDs  []string `json:"caseRunIds"`
	EvidenceIDs []string `json:"evidenceIds"`
}

// triageContextOutput is the triage.context_read projection: case/evidence/code
// summaries, never evidence bytes.
type triageContextOutput struct {
	Cases    []triageCaseSummary  `json:"cases"`
	Evidence []evidenceMetaValue  `json:"evidence"`
	CodeRefs []triageCodeRefValue `json:"codeRefs"`
}

// triageCaseSummary is the case summary of triage.context_read.
type triageCaseSummary struct {
	CaseRunID      string   `json:"caseRunId"`
	CaseNodeID     string   `json:"caseNodeId"`
	Title          string   `json:"title"`
	FailureClass   string   `json:"failureClass"`
	FailureReason  string   `json:"failureReason"`
	StepCount      int      `json:"stepCount"`
	AssertionCount int      `json:"assertionCount"`
	EvidenceIDs    []string `json:"evidenceIds"`
}

// triageCodeRefValue is one failure-candidate code location.
type triageCodeRefValue struct {
	CaseRunID  string `json:"caseRunId"`
	CaseNodeID string `json:"caseNodeId"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Reason     string `json:"reason"`
}

// runTriageContextRead implements triage.context_read (spec §9.4). The requested
// case runs and evidence ids must belong to the batch's failed application
// cases; a foreign id is refused (fail closed) instead of silently omitted.
func runTriageContextRead(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[triageContextInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(input.CaseRunIDs) > maxContextCaseRunIDs {
		return agentdomain.ToolResult{}, invalidArgumentsf("caseRunIds accepts at most %d ids", maxContextCaseRunIDs)
	}
	if len(input.EvidenceIDs) > maxContextEvidenceIDs {
		return agentdomain.ToolResult{}, invalidArgumentsf("evidenceIds accepts at most %d ids", maxContextEvidenceIDs)
	}
	_, run, err := scopeRun(ctx, deps, domain.AnalysisKindIssueTriage)
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
	allowedEvidence := allowedEvidenceIDs(cases)

	caseRunIDs := make([]int64, 0, len(input.CaseRunIDs))
	for position, raw := range input.CaseRunIDs {
		caseRunID, ok := parseID(raw)
		if !ok {
			return agentdomain.ToolResult{}, invalidArgumentsf(
				"caseRunIds[%d]: %q is not a positive decimal id", position, raw)
		}
		if _, owned := index[caseRunID]; !owned {
			return agentdomain.ToolResult{}, invalidArgumentsf(
				"case run %d is not a failed application case run of batch %d", caseRunID, run.BatchID)
		}
		caseRunIDs = append(caseRunIDs, caseRunID)
	}
	evidenceIDs := make([]int64, 0, len(input.EvidenceIDs))
	for position, raw := range input.EvidenceIDs {
		evidenceID, ok := parseID(raw)
		if !ok {
			return agentdomain.ToolResult{}, invalidArgumentsf(
				"evidenceIds[%d]: %q is not a positive decimal id", position, raw)
		}
		if _, owned := allowedEvidence[evidenceID]; !owned {
			return agentdomain.ToolResult{}, invalidArgumentsf(
				"evidence %d does not belong to a failed application case of the batch", evidenceID)
		}
		evidenceIDs = append(evidenceIDs, evidenceID)
	}

	out := triageContextOutput{
		Cases:    make([]triageCaseSummary, 0, len(caseRunIDs)),
		Evidence: make([]evidenceMetaValue, 0, len(evidenceIDs)),
		CodeRefs: make([]triageCodeRefValue, 0),
	}
	for _, caseRunID := range caseRunIDs {
		detail, found, err := deps.Details.CaseDetail(ctx, run.VersionID, run.BatchID, caseRunID)
		if err != nil {
			return agentdomain.ToolResult{}, internalErrorf("read case run %d detail: %v", caseRunID, err)
		}
		if !found {
			return agentdomain.ToolResult{}, notFoundf("case run %d not found", caseRunID)
		}
		out.Cases = append(out.Cases, triageCaseSummary{
			CaseRunID:      idString(detail.CaseRunID),
			CaseNodeID:     idString(detail.CaseNodeID),
			Title:          detail.Title,
			FailureClass:   detail.FailureClass,
			FailureReason:  detail.FailureReason,
			StepCount:      len(detail.Steps),
			AssertionCount: len(detail.Assertions),
			EvidenceIDs:    idStrings(detail.EvidenceIDs),
		})
	}
	if len(evidenceIDs) > 0 {
		metas, err := deps.Evidence.EvidenceMeta(ctx, run.VersionID, run.BatchID, evidenceIDs)
		if err != nil {
			return agentdomain.ToolResult{}, internalErrorf("read evidence metadata: %v", err)
		}
		out.Evidence = evidenceMetaValues(metas)
	}
	// Code locations come from the failure candidates already copied into the
	// triage input; no file content is read here.
	requestedCases := make(map[int64]struct{}, len(caseRunIDs))
	for _, caseRunID := range caseRunIDs {
		requestedCases[caseRunID] = struct{}{}
	}
	for _, candidate := range triageInput.Candidates {
		if len(requestedCases) > 0 {
			if _, wanted := requestedCases[candidate.CaseRunID]; !wanted {
				continue
			}
		}
		for _, ref := range candidate.CodeRefs {
			if len(out.CodeRefs) >= maxContextCodeRefs {
				break
			}
			out.CodeRefs = append(out.CodeRefs, triageCodeRefValue{
				CaseRunID:  idString(candidate.CaseRunID),
				CaseNodeID: idString(candidate.CaseNodeID),
				Path:       ref.Path,
				Line:       ref.Line,
				Reason:     ref.Reason,
			})
		}
	}
	return jsonResult(out)
}
