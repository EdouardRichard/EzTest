package agent_tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// proposeResult is the tool result of one proposal call: how many entries were
// accepted in this call, the accumulated total and the skipped-item warnings.
type proposeResult struct {
	Accepted int       `json:"accepted"`
	Total    int       `json:"total"`
	Warnings []warning `json:"warnings"`
}

// runCasePropose executes supplement.case_propose (spec §9.2). Structurally
// invalid cases are a correctable invalid_arguments failure; entries beyond the
// five-case cap are skipped with a warning.
func runCasePropose(ctx context.Context, deps Deps, args json.RawMessage) (agentdomain.ToolResult, error) {
	run, err := resolveRun(ctx, deps)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[caseProposeInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(input.Cases) == 0 {
		return agentdomain.ToolResult{}, invalidArguments("cases must contain at least one supplement case")
	}
	confirmed, err := confirmedRequirements(ctx, deps, run.VersionID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	normalized := make([]caseProposal, 0, len(input.Cases))
	for index, raw := range input.Cases {
		proposal, err := normalizeCase(raw, confirmed)
		if err != nil {
			return agentdomain.ToolResult{}, withField(fmt.Sprintf("cases[%d]", index), err)
		}
		normalized = append(normalized, proposal)
	}

	document := decodeSubmission(run.Result)
	maxCases := deps.maxCases()
	warnings := make([]warning, 0, 1)
	accepted := 0
	for _, proposal := range normalized {
		if len(document.Cases) >= maxCases {
			warnings = append(warnings, warning{
				Code:    "case_limit_reached",
				Message: fmt.Sprintf("a supplement run may add at most %d cases; the rest was skipped", maxCases),
			})
			break
		}
		document.Cases = append(document.Cases, proposal)
		accepted++
	}
	if accepted == 0 {
		return jsonResult(proposeResult{Accepted: 0, Total: len(document.Cases), Warnings: warnings})
	}
	if err := storeSubmission(ctx, deps, run, document, "caseProposed", accepted); err != nil {
		return agentdomain.ToolResult{}, err
	}
	recordToolAudit(ctx, deps, toolNameCasePropose, run, map[string]any{
		"accepted": accepted,
		"total":    len(document.Cases),
	})
	return jsonResult(proposeResult{Accepted: accepted, Total: len(document.Cases), Warnings: warnings})
}

// runAffectedCasesPropose executes supplement.affected_cases_propose (spec
// §9.2). It is only allowed in an incremental_design run and validates every
// node against the current graph.
func runAffectedCasesPropose(ctx context.Context, deps Deps, args json.RawMessage) (agentdomain.ToolResult, error) {
	run, err := resolveRun(ctx, deps)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if run.Kind != mindmapapp.RunKindIncrementalDesign {
		return agentdomain.ToolResult{}, invalidArguments(
			"supplement.affected_cases_propose is only allowed in an incremental_design run")
	}
	input, err := decodeArguments[affectedProposeInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(input.Cases) == 0 {
		return agentdomain.ToolResult{}, invalidArguments("cases must contain at least one affected case")
	}
	view, err := contextGraphView(ctx, deps, run.VersionID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	confirmed, err := confirmedRequirements(ctx, deps, run.VersionID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	byID := make(map[int64]mindmapdomain.CaseNode, len(view.Nodes))
	for _, graphNode := range view.Nodes {
		byID[graphNode.Node.ID] = graphNode.Node
	}
	normalized := make([]affectedProposal, 0, len(input.Cases))
	seen := make(map[int64]struct{}, len(input.Cases))
	for index, raw := range input.Cases {
		nodeID, err := parsePositiveID(raw.CaseNodeID)
		if err != nil {
			return agentdomain.ToolResult{}, withField(fmt.Sprintf("cases[%d].caseNodeId", index), err)
		}
		node, ok := byID[nodeID]
		if !ok || node.Kind != mindmapdomain.NodeKindCase {
			return agentdomain.ToolResult{}, invalidArguments(
				fmt.Sprintf("cases[%d].caseNodeId %d is not a case node of this version", index, nodeID))
		}
		if node.Status == mindmapdomain.NodeStatusDraft {
			return agentdomain.ToolResult{}, invalidArguments(
				fmt.Sprintf("cases[%d].caseNodeId %d is a draft case and cannot be affected", index, nodeID))
		}
		confidence := strings.TrimSpace(raw.Confidence)
		switch confidence {
		case mindmapdomain.ConfidenceHigh, mindmapdomain.ConfidenceMedium, mindmapdomain.ConfidenceLow:
		default:
			return agentdomain.ToolResult{}, invalidArguments(
				fmt.Sprintf("cases[%d].confidence must be high, medium or low", index))
		}
		reason := strings.TrimSpace(raw.Reason)
		if reason == "" {
			return agentdomain.ToolResult{}, invalidArguments(fmt.Sprintf("cases[%d].reason must not be empty", index))
		}
		if err := checkRunes("cases.reason", reason, maxDescriptionRunes); err != nil {
			return agentdomain.ToolResult{}, err
		}
		source := strings.TrimSpace(raw.Source)
		if source != "requirement" && source != "code" {
			return agentdomain.ToolResult{}, invalidArguments(
				fmt.Sprintf("cases[%d].source must be requirement or code", index))
		}
		requirementIDs, err := normalizeOptionalRequirementIDs(raw.RequirementIDs, confirmed)
		if err != nil {
			return agentdomain.ToolResult{}, withField(fmt.Sprintf("cases[%d].requirementIds", index), err)
		}
		codeRefs, err := normalizeCodeRefs(raw.CodeRefs)
		if err != nil {
			return agentdomain.ToolResult{}, withField(fmt.Sprintf("cases[%d]", index), err)
		}
		if _, duplicate := seen[nodeID]; duplicate {
			continue
		}
		seen[nodeID] = struct{}{}
		normalized = append(normalized, affectedProposal{
			CaseNodeID:     strconv.FormatInt(nodeID, 10),
			Confidence:     confidence,
			Reason:         reason,
			Source:         source,
			RequirementIDs: requirementIDs,
			CodeRefs:       codeRefs,
		})
	}

	document := decodeSubmission(run.Result)
	warnings := make([]warning, 0, 1)
	accepted := 0
	for _, proposal := range normalized {
		if len(document.AffectedCases) >= supplementMaxAffected {
			warnings = append(warnings, warning{
				Code:    "affected_limit_reached",
				Message: fmt.Sprintf("a run may propose at most %d affected cases; the rest was skipped", supplementMaxAffected),
			})
			break
		}
		document.AffectedCases = append(document.AffectedCases, proposal)
		accepted++
	}
	if accepted > 0 {
		if err := storeSubmission(ctx, deps, run, document, "affectedProposed", accepted); err != nil {
			return agentdomain.ToolResult{}, err
		}
	}
	recordToolAudit(ctx, deps, toolNameAffected, run, map[string]any{
		"accepted": accepted,
		"total":    len(document.AffectedCases),
	})
	return jsonResult(proposeResult{Accepted: accepted, Total: len(document.AffectedCases), Warnings: warnings})
}

// runEscalate executes supplement.escalate (spec §9.2): requirement gaps are
// validated and stored in the run result for the autonomy worker to materialize
// as requirement_gap issues.
func runEscalate(ctx context.Context, deps Deps, args json.RawMessage) (agentdomain.ToolResult, error) {
	run, err := resolveRun(ctx, deps)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[escalateInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	escalations, err := escalatePayload(input)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(escalations) == 0 {
		return agentdomain.ToolResult{}, invalidArguments(
			"pass one escalation object {title,detail,requirementIds} or a non-empty escalations array")
	}
	confirmed, err := confirmedRequirements(ctx, deps, run.VersionID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	normalized := make([]escalation, 0, len(escalations))
	for index, raw := range escalations {
		title := strings.TrimSpace(raw.Title)
		if title == "" {
			return agentdomain.ToolResult{}, invalidArguments(
				fmt.Sprintf("escalations[%d].title must not be empty", index))
		}
		if err := checkRunes("escalations.title", title, maxEscalationTitleRunes); err != nil {
			return agentdomain.ToolResult{}, err
		}
		if err := checkRunes("escalations.detail", raw.Detail, maxEscalationDetailRunes); err != nil {
			return agentdomain.ToolResult{}, err
		}
		requirementIDs, err := normalizeRequirementIDs(raw.RequirementIDs, confirmed)
		if err != nil {
			return agentdomain.ToolResult{}, withField(fmt.Sprintf("escalations[%d].requirementIds", index), err)
		}
		normalized = append(normalized, escalation{Title: title, Detail: raw.Detail, RequirementIDs: requirementIDs})
	}

	document := decodeSubmission(run.Result)
	warnings := make([]warning, 0, 1)
	accepted := 0
	for _, proposal := range normalized {
		if len(document.Escalations) >= supplementMaxEscalations {
			warnings = append(warnings, warning{
				Code:    "escalation_limit_reached",
				Message: fmt.Sprintf("a run may escalate at most %d requirement gaps; the rest was skipped", supplementMaxEscalations),
			})
			break
		}
		document.Escalations = append(document.Escalations, proposal)
		accepted++
	}
	if accepted > 0 {
		if err := storeSubmission(ctx, deps, run, document, "escalationsProposed", accepted); err != nil {
			return agentdomain.ToolResult{}, err
		}
	}
	recordToolAudit(ctx, deps, toolNameEscalate, run, map[string]any{
		"accepted": accepted,
		"total":    len(document.Escalations),
	})
	return jsonResult(proposeResult{Accepted: accepted, Total: len(document.Escalations), Warnings: warnings})
}

// runFinish executes the optional supplement.finish: it records the run summary
// in the result document and returns success. An empty body is valid.
func runFinish(ctx context.Context, deps Deps, args json.RawMessage) (agentdomain.ToolResult, error) {
	run, err := resolveRun(ctx, deps)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[finishInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	summary := strings.TrimSpace(input.Summary)
	if err := checkRunes("summary", summary, maxSummaryRunes); err != nil {
		return agentdomain.ToolResult{}, err
	}
	document := decodeSubmission(run.Result)
	if summary != "" {
		document.Summary = summary
	}
	if err := storeSubmission(ctx, deps, run, document, "finished", 1); err != nil {
		return agentdomain.ToolResult{}, err
	}
	recordToolAudit(ctx, deps, toolNameFinish, run, map[string]any{"summaryRunes": len([]rune(summary))})
	return jsonResult(struct {
		Status string `json:"status"`
	}{Status: "ok"})
}

// escalatePayload resolves the two accepted supplement.escalate input shapes:
// the array wrapper the tool originally shipped and the single object the spec
// §9.2 table documents. Mixing both is ambiguous and rejected.
func escalatePayload(input escalateInput) ([]escalation, error) {
	single := escalation{Title: input.Title, Detail: input.Detail, RequirementIDs: input.RequirementIDs}
	hasSingle := strings.TrimSpace(single.Title) != "" || strings.TrimSpace(single.Detail) != "" || len(single.RequirementIDs) > 0
	if len(input.Escalations) > 0 && hasSingle {
		return nil, invalidArguments("pass either a single escalation object or an escalations array, not both")
	}
	if len(input.Escalations) > 0 {
		return input.Escalations, nil
	}
	if hasSingle {
		return []escalation{single}, nil
	}
	return nil, nil
}

// normalizeOptionalRequirementIDs validates an optional requirement-id list.
func normalizeOptionalRequirementIDs(raw []string, confirmed map[int64]struct{}) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return normalizeRequirementIDs(raw, confirmed)
}

// withField prefixes a tool error message with the offending array field while
// keeping the actionable invalid_arguments code.
func withField(field string, err error) error {
	var toolErr *agentdomain.ToolError
	if errors.As(err, &toolErr) {
		return &agentdomain.ToolError{Code: toolErr.Code, Message: field + ": " + toolErr.Message}
	}
	return err
}
