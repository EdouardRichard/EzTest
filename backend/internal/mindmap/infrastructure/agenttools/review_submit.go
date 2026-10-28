package agenttools

// case.review_submit is the structured submission tool of a review run (design
// spec §8.2). It validates the finding cap, text bound and enums, resolves the
// calling review run from the P1 invocation context and stores the findings in
// that run's result. It never writes case_review/case_review_finding rows: the
// run worker materializes them after the agent run finishes.

import (
	"context"
	"encoding/json"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// reviewSubmitSchema is the frozen input schema of case.review_submit.
const reviewSubmitSchema = `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"findings":{"type":"array","maxItems":200,"items":{"type":"object","properties":{"nodeId":{"type":"string","pattern":"^[0-9]+$"},"category":{"type":"string","enum":["coverage","assertion","executability","duplicate","code_uncovered","other"]},"severity":{"type":"string","enum":["blocker","major","minor","info"]},"detail":{"type":"string","minLength":1,"maxLength":2000}},"required":["category","severity","detail"],"additionalProperties":false}}},"required":["versionId","findings"],"additionalProperties":false}`

// reviewSubmitInput is the case.review_submit argument document. Findings is a
// pointer so a missing field is distinguishable from an explicit empty array.
type reviewSubmitInput struct {
	VersionID string                `json:"versionId"`
	Findings  *[]reviewFindingInput `json:"findings"`
}

// reviewFindingInput is one proposed finding.
type reviewFindingInput struct {
	NodeID   *string `json:"nodeId"`
	Category string  `json:"category"`
	Severity string  `json:"severity"`
	Detail   string  `json:"detail"`
}

// reviewFindingOutput is the stored finding. A version-level finding omits
// nodeId.
type reviewFindingOutput struct {
	NodeID   string `json:"nodeId,omitempty"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// reviewSubmitPayload is the run.result document the materializer reads.
type reviewSubmitPayload struct {
	Findings []reviewFindingOutput `json:"findings"`
}

// reviewSubmitOutput is the model-facing acknowledgement.
type reviewSubmitOutput struct {
	Stored      bool `json:"stored"`
	Submissions int  `json:"submissions"`
	Findings    int  `json:"findings"`
	Blockers    int  `json:"blockers"`
}

// runReviewSubmit executes case.review_submit.
func runReviewSubmit(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	var input reviewSubmitInput
	if failure := decodeToolArguments(args, &input); failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return agentdomain.ToolResult{}, invalidToolArguments("versionId must be a positive decimal id")
	}
	if input.Findings == nil {
		return agentdomain.ToolResult{}, invalidToolArguments("findings is required (send an empty array when there is nothing to report)")
	}
	findings := *input.Findings
	if len(findings) > maxReviewFindings {
		return agentdomain.ToolResult{}, invalidToolArguments("findings has %d entries; the limit is %d", len(findings), maxReviewFindings)
	}
	payload := reviewSubmitPayload{Findings: make([]reviewFindingOutput, 0, len(findings))}
	blockers := 0
	for index, finding := range findings {
		prefix := "findings[" + formatID(int64(index)) + "]"
		var nodeID string
		if finding.NodeID != nil {
			parsed, ok := parsePositiveID(*finding.NodeID)
			if !ok {
				return agentdomain.ToolResult{}, invalidToolArguments("%s.nodeId must be a positive decimal id", prefix)
			}
			nodeID = formatID(parsed)
		}
		category := strings.TrimSpace(finding.Category)
		if !validFindingCategory(category) {
			return agentdomain.ToolResult{}, invalidToolArguments(
				"%s.category must be coverage, assertion, executability, duplicate, code_uncovered or other", prefix)
		}
		severity := strings.TrimSpace(finding.Severity)
		if !validFindingSeverity(severity) {
			return agentdomain.ToolResult{}, invalidToolArguments(
				"%s.severity must be blocker, major, minor or info", prefix)
		}
		detail := strings.TrimSpace(finding.Detail)
		if detail == "" {
			return agentdomain.ToolResult{}, invalidToolArguments("%s.detail must not be empty", prefix)
		}
		if runeCount(detail) > maxFindingDetailRunes {
			return agentdomain.ToolResult{}, invalidToolArguments(
				"%s.detail has %d characters; the limit is %d", prefix, runeCount(detail), maxFindingDetailRunes)
		}
		if severity == mindmapdomain.SeverityBlocker {
			blockers++
		}
		payload.Findings = append(payload.Findings, reviewFindingOutput{
			NodeID:   nodeID,
			Category: category,
			Severity: severity,
			Detail:   detail,
		})
	}

	run, err := resolveActiveRun(ctx, deps, mindmapdomain.RunKindReview, versionID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	submissions, err := storeRunResult(ctx, deps, run, encoded, "submissions")
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	output := reviewSubmitOutput{
		Stored:      true,
		Submissions: submissions,
		Findings:    len(payload.Findings),
		Blockers:    blockers,
	}
	recordToolAudit(ctx, deps, toolNameReviewSubmit, run.ID, map[string]any{
		"findings": output.Findings,
		"blockers": output.Blockers,
	})
	return encodeToolJSON(output)
}

// validFindingCategory reports whether category is one of the review finding
// categories (spec §8.2).
func validFindingCategory(category string) bool {
	switch category {
	case mindmapdomain.FindingCoverage, mindmapdomain.FindingAssertion,
		mindmapdomain.FindingExecutability, mindmapdomain.FindingDuplicate,
		mindmapdomain.FindingCodeUncovered, mindmapdomain.FindingOther:
		return true
	default:
		return false
	}
}

// validFindingSeverity reports whether severity is one of the review severities
// (spec §8.2).
func validFindingSeverity(severity string) bool {
	switch severity {
	case mindmapdomain.SeverityBlocker, mindmapdomain.SeverityMajor,
		mindmapdomain.SeverityMinor, mindmapdomain.SeverityInfo:
		return true
	default:
		return false
	}
}
