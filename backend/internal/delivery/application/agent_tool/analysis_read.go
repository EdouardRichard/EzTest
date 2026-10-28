package agent_tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// caseListInput is the analysis.case_list argument.
type caseListInput struct {
	BatchID string `json:"batchId"`
}

// caseListOutput is the analysis.case_list projection: summaries only, never
// evidence bodies.
type caseListOutput struct {
	BatchID   string             `json:"batchId"`
	Cases     []caseSummaryValue `json:"cases"`
	Truncated bool               `json:"truncated"`
}

// caseSummaryValue is one failed(application) case run summary.
type caseSummaryValue struct {
	CaseRunID     string `json:"caseRunId"`
	CaseNodeID    string `json:"caseNodeId"`
	Title         string `json:"title"`
	FailureClass  string `json:"failureClass"`
	FailureReason string `json:"failureReason"`
	EvidenceCount int    `json:"evidenceCount"`
}

// runCaseList implements analysis.case_list (spec §9.3): the failed(application)
// case runs of the calling batch, at most 20.
func runCaseList(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[caseListInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	batchID, ok := parseID(input.BatchID)
	if !ok {
		return agentdomain.ToolResult{}, invalidArgumentsf("batchId must be a positive decimal string")
	}
	invocation, run, err := scopeRun(ctx, deps, domain.AnalysisKindFailure)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if batchID != invocation.BatchID {
		return agentdomain.ToolResult{}, invalidArgumentsf(
			"batchId %d does not match the analysis run scope (batch %d)", batchID, invocation.BatchID)
	}
	cases, err := failedApplicationCases(ctx, deps, run)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	truncated := len(cases) > domain.AnalysisMaxFailureCandidates
	if truncated {
		cases = cases[:domain.AnalysisMaxFailureCandidates]
	}
	out := caseListOutput{
		BatchID:   idString(run.BatchID),
		Cases:     make([]caseSummaryValue, 0, len(cases)),
		Truncated: truncated,
	}
	for _, failedCase := range cases {
		out.Cases = append(out.Cases, caseSummaryValue{
			CaseRunID:     idString(failedCase.CaseRunID),
			CaseNodeID:    idString(failedCase.CaseNodeID),
			Title:         failedCase.Title,
			FailureClass:  failedCase.FailureClass,
			FailureReason: failedCase.FailureReason,
			EvidenceCount: len(failedCase.EvidenceIDs),
		})
	}
	return jsonResult(out)
}

// caseDetailInput is the analysis.case_detail argument.
type caseDetailInput struct {
	CaseRunID string `json:"caseRunId"`
}

// caseDetailOutput is the analysis.case_detail projection: classification,
// recorded steps/assertions and evidence ids, never artifact bytes.
type caseDetailOutput struct {
	CaseRunID     string               `json:"caseRunId"`
	CaseNodeID    string               `json:"caseNodeId"`
	Title         string               `json:"title"`
	Status        string               `json:"status"`
	FailureClass  string               `json:"failureClass"`
	FailureReason string               `json:"failureReason"`
	Steps         []caseStepValue      `json:"steps"`
	Assertions    []caseAssertionValue `json:"assertions"`
	EvidenceIDs   []string             `json:"evidenceIds"`
}

// caseStepValue is one recorded step result.
type caseStepValue struct {
	Seq           int    `json:"seq"`
	Action        string `json:"action"`
	Expected      string `json:"expected"`
	Actual        string `json:"actual"`
	Status        string `json:"status"`
	EvidenceCount int    `json:"evidenceCount"`
}

// caseAssertionValue is one recorded assertion result.
type caseAssertionValue struct {
	AssertionID   string `json:"assertionId"`
	Kind          string `json:"kind"`
	Target        string `json:"target"`
	Expected      string `json:"expected"`
	Actual        string `json:"actual"`
	Verdict       string `json:"verdict"`
	FailureReason string `json:"failureReason"`
}

// runCaseDetail implements analysis.case_detail (spec §9.3). The requested case
// run must be one of the batch's failed(application) runs; the detail reader then
// reads its recorded steps/assertions within the same version/batch scope.
func runCaseDetail(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[caseDetailInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	caseRunID, ok := parseID(input.CaseRunID)
	if !ok {
		return agentdomain.ToolResult{}, invalidArgumentsf("caseRunId must be a positive decimal string")
	}
	_, run, err := scopeRun(ctx, deps, domain.AnalysisKindFailure)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	cases, err := failedApplicationCases(ctx, deps, run)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, owned := caseRunIndex(cases)[caseRunID]; !owned {
		return agentdomain.ToolResult{}, invalidArgumentsf(
			"case run %d is not a failed application case run of batch %d", caseRunID, run.BatchID)
	}
	detail, found, err := deps.Details.CaseDetail(ctx, run.VersionID, run.BatchID, caseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, internalErrorf("read case run %d detail: %v", caseRunID, err)
	}
	if !found {
		return agentdomain.ToolResult{}, notFoundf("case run %d not found", caseRunID)
	}
	out := caseDetailOutput{
		CaseRunID:     idString(detail.CaseRunID),
		CaseNodeID:    idString(detail.CaseNodeID),
		Title:         detail.Title,
		Status:        detail.Status,
		FailureClass:  detail.FailureClass,
		FailureReason: detail.FailureReason,
		Steps:         make([]caseStepValue, 0, len(detail.Steps)),
		Assertions:    make([]caseAssertionValue, 0, len(detail.Assertions)),
		EvidenceIDs:   idStrings(detail.EvidenceIDs),
	}
	for _, step := range detail.Steps {
		out.Steps = append(out.Steps, caseStepValue{
			Seq:           step.Seq,
			Action:        step.Action,
			Expected:      step.Expected,
			Actual:        step.Actual,
			Status:        step.Status,
			EvidenceCount: step.EvidenceCount,
		})
	}
	for _, assertion := range detail.Assertions {
		out.Assertions = append(out.Assertions, caseAssertionValue{
			AssertionID:   idString(assertion.AssertionID),
			Kind:          assertion.Kind,
			Target:        assertion.Target,
			Expected:      assertion.Expected,
			Actual:        assertion.Actual,
			Verdict:       assertion.Verdict,
			FailureReason: assertion.FailureReason,
		})
	}
	return jsonResult(out)
}

// evidenceMetaInput is the analysis.evidence_meta argument.
type evidenceMetaInput struct {
	EvidenceIDs []string `json:"evidenceIds"`
}

// runEvidenceMeta implements analysis.evidence_meta (spec §9.3): metadata only,
// and only for evidence of the batch's failed application cases.
func runEvidenceMeta(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[evidenceMetaInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(input.EvidenceIDs) > maxEvidenceMetaIDs {
		return agentdomain.ToolResult{}, invalidArgumentsf(
			"evidenceIds accepts at most %d ids", maxEvidenceMetaIDs)
	}
	_, run, err := scopeRun(ctx, deps, domain.AnalysisKindFailure)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	cases, err := failedApplicationCases(ctx, deps, run)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	allowed := allowedEvidenceIDs(cases)
	requested := make([]int64, 0, len(input.EvidenceIDs))
	warnings := make([]string, 0)
	seen := make(map[int64]struct{}, len(input.EvidenceIDs))
	for index, raw := range input.EvidenceIDs {
		id, ok := parseID(raw)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("evidenceIds[%d]: %q is not a positive decimal id", index, raw))
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if _, owned := allowed[id]; !owned {
			warnings = append(warnings, fmt.Sprintf(
				"evidence %d does not belong to a failed application case of the batch", id))
			continue
		}
		requested = append(requested, id)
	}
	if len(requested) == 0 {
		return jsonResult(evidenceMetaOutput{Evidence: []evidenceMetaValue{}, Warnings: warnings})
	}
	metas, err := deps.Evidence.EvidenceMeta(ctx, run.VersionID, run.BatchID, requested)
	if err != nil {
		return agentdomain.ToolResult{}, internalErrorf("read evidence metadata: %v", err)
	}
	byID := make(map[int64]EvidenceMeta, len(metas))
	for _, meta := range metas {
		byID[meta.EvidenceID] = meta
	}
	values := make([]evidenceMetaValue, 0, len(requested))
	for _, id := range requested {
		meta, ok := byID[id]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("evidence %d has no metadata", id))
			continue
		}
		values = append(values, newEvidenceMetaValue(meta))
	}
	return jsonResult(evidenceMetaOutput{Evidence: values, Warnings: warnings})
}

// codeReadInput is the analysis.code_read argument.
type codeReadInput struct {
	Refs []codeReadRefInput `json:"refs"`
}

// codeReadRefInput is one requested code window.
type codeReadRefInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// codeReadOutput is the analysis.code_read projection.
type codeReadOutput struct {
	Files    []codeWindowValue `json:"files"`
	Warnings []string          `json:"warnings"`
}

// codeWindowValue is one returned code window.
type codeWindowValue struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// runCodeRead implements analysis.code_read (spec §9.3): read-only, commit-pinned
// windows bounded to 10 files, 80 lines per file and 64 KiB per call. Missing
// paths are warnings; a reader transport failure is fatal.
func runCodeRead(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	input, err := decodeArguments[codeReadInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(input.Refs) == 0 {
		return agentdomain.ToolResult{}, invalidArgumentsf("refs must contain at least one code window")
	}
	if len(input.Refs) > maxCodeReadRefs {
		return agentdomain.ToolResult{}, invalidArgumentsf("refs accepts at most %d code windows", maxCodeReadRefs)
	}
	for index, ref := range input.Refs {
		label := fmt.Sprintf("refs[%d]", index)
		path := strings.TrimSpace(ref.Path)
		if path == "" || len(path) > maxCodePathBytes {
			return agentdomain.ToolResult{}, invalidArgumentsf(
				"%s: path must be between 1 and %d bytes", label, maxCodePathBytes)
		}
		if ref.StartLine < 1 || ref.EndLine < ref.StartLine {
			return agentdomain.ToolResult{}, invalidArgumentsf(
				"%s: startLine/endLine must describe a positive line window", label)
		}
		if ref.EndLine-ref.StartLine+1 > maxCodeReadLinesPerRef {
			return agentdomain.ToolResult{}, invalidArgumentsf(
				"%s: a code window is limited to %d lines", label, maxCodeReadLinesPerRef)
		}
	}
	_, run, err := scopeRun(ctx, deps, domain.AnalysisKindFailure)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	out := codeReadOutput{Files: make([]codeWindowValue, 0, len(input.Refs)), Warnings: make([]string, 0)}
	used := 0
	limitReported := false
	for _, ref := range input.Refs {
		path := strings.TrimSpace(ref.Path)
		window, found, err := deps.Code.ReadCode(ctx, run.VersionID, path, ref.StartLine, ref.EndLine)
		if err != nil {
			return agentdomain.ToolResult{}, internalErrorf("read code window %q: %v", path, err)
		}
		if !found {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"code path %q is not readable in the version repository", path))
			continue
		}
		remaining := maxCodeReadBytes - used
		if remaining <= 0 {
			if !limitReported {
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"code_read reached the %d byte output limit; remaining windows were omitted", maxCodeReadBytes))
				limitReported = true
			}
			break
		}
		content := window.Content
		truncated := window.Truncated
		if len(content) > remaining {
			content = truncateBytes(content, remaining)
			truncated = true
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"code window %q was truncated at the %d byte code_read output limit", path, maxCodeReadBytes))
		}
		used += len(content)
		out.Files = append(out.Files, codeWindowValue{
			Path:      window.Path,
			StartLine: window.StartLine,
			EndLine:   window.EndLine,
			Content:   content,
			Truncated: truncated,
		})
	}
	return jsonResult(out)
}
