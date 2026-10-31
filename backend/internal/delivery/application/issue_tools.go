package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// issueToolClassificationMax bounds merge_reason (contracts/agent-profiles-tools.md §7.3).
const issueToolMergeReasonMax = 500

// issueToolClassifications is the frozen classification allow-list: the issue
// category values plus the deterministic triage suggestions. The classification
// is stored in analysis_run.output only, never in an issue column (spec §5.3).
var issueToolClassifications = map[string]bool{
	"ui":               true,
	"api":              true,
	"db":               true,
	"duplicate":        true,
	"expected_failure": true,
	"flaky":            true,
}

// IssueTools returns the frozen P4 issue-triager tool group (spec §4.4 /
// contracts/agent-profiles-tools.md §7.3). The tools only ever receive the
// runtime-injected tools.Scope: their JSON schemas never expose the version or
// task issue IDs. The composition root (Task 23) registers this group through
// tools.RegisterP4ToolGroup with captured IssueService/AnalysisService values.
func IssueTools(svc *IssueService, analysis *AnalysisService) []agenttools.Tool {
	return []agenttools.Tool{
		&issueReadTool{svc: svc},
		&issueProposeTool{svc: svc, analysis: analysis},
	}
}

// issueSchema renders the strict JSON schema shared by the issue tools.
func issueSchema(properties map[string]any, required ...string) json.RawMessage {
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

// --- issue.read -------------------------------------------------------------

type issueReadTool struct{ svc *IssueService }

func (t *issueReadTool) Name() string { return agenttools.ToolIssueRead }

func (t *issueReadTool) Description() string {
	return "Read the current task issue (or an explicit same-version issue) with its append-only refs and the summary of its linked case_runs (counts, failed assertion position, failure detail, evidence IDs). Read-only: no execution, no conversation transcript, no evidence bytes."
}

func (t *issueReadTool) Parameters() json.RawMessage {
	return issueSchema(map[string]any{
		"issue_id": map[string]any{
			"type":        "string",
			"description": "decimal issue ID within the task version; defaults to the issue referenced by the current task",
		},
	})
}

func (t *issueReadTool) Execute(ctx context.Context, scope agenttools.Scope, raw json.RawMessage) (any, error) {
	if t.svc == nil {
		return nil, errors.New("issue.read has no issue service")
	}
	var params struct {
		IssueID string `json:"issue_id"`
	}
	if err := agenttools.DecodeP4Params(raw, &params); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, agenttools.Invalid("scope", "issue.read requires the injected version scope")
	}
	issueID, idErr := issueToolTargetID(params.IssueID, scope.IssueID)
	if idErr != nil {
		return nil, idErr
	}
	detail, err := t.svc.Get(ctx, scope.VersionID, issueID)
	if err != nil {
		return nil, mapIssueToolError(err)
	}

	refs := make([]map[string]any, 0, len(detail.Refs))
	caseRunIDs := make([]int64, 0, 2)
	seenCaseRuns := make(map[int64]bool)
	for _, ref := range detail.Refs {
		entry := map[string]any{
			"kind":   ref.Kind,
			"ref_id": formatIssueID(ref.RefID),
		}
		if ref.Role != "" {
			entry["role"] = ref.Role
		}
		if ref.VersionSUTID != nil && *ref.VersionSUTID > 0 {
			entry["version_sut_id"] = formatIssueID(*ref.VersionSUTID)
		}
		refs = append(refs, entry)
		if ref.Kind == delivery.IssueRefKindCaseRun && !seenCaseRuns[ref.RefID] {
			seenCaseRuns[ref.RefID] = true
			caseRunIDs = append(caseRunIDs, ref.RefID)
		}
	}

	caseRuns := make([]map[string]any, 0, len(caseRunIDs))
	if t.svc.exec != nil {
		for _, caseRunID := range caseRunIDs {
			snapshot, err := t.svc.exec.CaseRunSnapshot(ctx, caseRunID)
			if err != nil {
				return nil, fmt.Errorf("read case run %d for issue %d: %w", caseRunID, issueID, err)
			}
			caseRuns = append(caseRuns, issueCaseRunSummary(snapshot))
		}
	}

	return map[string]any{
		"issue":     issueToolIssue(detail.Issue),
		"refs":      refs,
		"case_runs": caseRuns,
	}, nil
}

// issueToolIssue renders the issue row without its fingerprint/dedupe internals.
func issueToolIssue(issue delivery.Issue) map[string]any {
	result := map[string]any{
		"issue_id":          formatIssueID(issue.ID),
		"version_id":        formatIssueID(issue.VersionID),
		"title":             issue.Title,
		"summary":           issue.Summary,
		"status":            string(issue.Status),
		"category":          issue.Category,
		"severity":          issue.Severity,
		"source":            issue.Source,
		"expected_revision": issue.ExpectedRevision,
		"reopen_count":      issue.ReopenCount,
	}
	if issue.MergedIntoID != nil && *issue.MergedIntoID > 0 {
		result["merged_into_id"] = formatIssueID(*issue.MergedIntoID)
	}
	return result
}

// issueCaseRunSummary renders the execution summary of one linked case_run.
// Step/assertion detail stays in the execution context; only counts, the
// failure position, the failed assertion detail and evidence IDs cross the port.
func issueCaseRunSummary(snapshot CaseRunSnapshot) map[string]any {
	result := map[string]any{
		"case_run_id":            formatIssueID(snapshot.ID),
		"case_version_id":        formatIssueID(snapshot.CaseVersionID),
		"status":                 snapshot.Status,
		"failure_class":          snapshot.FailureClass,
		"summary":                snapshot.Summary,
		"step_count":             snapshot.StepCount,
		"step_failed_count":      snapshot.StepFailedCount,
		"assertion_count":        snapshot.AssertionCount,
		"assertion_failed_count": snapshot.AssertionFailedCount,
		"failed_step_seq":        snapshot.FailedStepSeq,
		"failed_assertion_seq":   snapshot.FailedAssertionSeq,
		"failed_expected":        snapshot.FailedExpected,
		"failed_actual":          snapshot.FailedActual,
		"failed_reason":          snapshot.FailedReason,
		"category":               snapshot.Category,
		"severity":               snapshot.Severity,
		"evidence_ids":           formatIssueIDs(snapshot.EvidenceIDs),
		"version_sut_ids":        formatIssueIDs(snapshot.VersionSUTIDs),
	}
	if snapshot.WaveID > 0 {
		result["wave_id"] = formatIssueID(snapshot.WaveID)
	}
	if snapshot.RequirementID > 0 {
		result["requirement_id"] = formatIssueID(snapshot.RequirementID)
	}
	if snapshot.VersionSUTID != nil && *snapshot.VersionSUTID > 0 {
		result["version_sut_id"] = formatIssueID(*snapshot.VersionSUTID)
	}
	return result
}

func formatIssueIDs(ids []int64) []string {
	if len(ids) == 0 {
		return []string{}
	}
	formatted := make([]string, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			formatted = append(formatted, formatIssueID(id))
		}
	}
	return formatted
}

// --- issue.propose ----------------------------------------------------------

type issueProposeTool struct {
	svc      *IssueService
	analysis *AnalysisService
}

func (t *issueProposeTool) Name() string { return agenttools.ToolIssuePropose }

func (t *issueProposeTool) Description() string {
	return "Submit the triage proposal of the current issue: classification, duplicate_of_issue_id, merge_reason and confidence. The server validates the duplicate target (same version, existing, not merged, not the issue itself) and writes analysis_run.output plus a delivery team-room finding. It never changes the issue status, merges or reopens."
}

func (t *issueProposeTool) Parameters() json.RawMessage {
	return issueSchema(map[string]any{
		"classification": map[string]any{
			"type": "string",
			"enum": []string{"ui", "api", "db", "duplicate", "expected_failure", "flaky"},
		},
		"duplicate_of_issue_id": map[string]any{"type": "string"},
		"merge_reason":          map[string]any{"type": "string", "maxLength": issueToolMergeReasonMax},
		"confidence":            map[string]any{"type": "number", "minimum": 0, "maximum": 1},
	})
}

func (t *issueProposeTool) Execute(ctx context.Context, scope agenttools.Scope, raw json.RawMessage) (any, error) {
	if t.svc == nil {
		return nil, errors.New("issue.propose has no issue service")
	}
	if t.analysis == nil {
		return nil, errors.New("issue.propose has no analysis service")
	}
	var params struct {
		Classification     string   `json:"classification"`
		DuplicateOfIssueID string   `json:"duplicate_of_issue_id"`
		MergeReason        string   `json:"merge_reason"`
		Confidence         *float64 `json:"confidence"`
	}
	if err := agenttools.DecodeP4Params(raw, &params); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, agenttools.Invalid("scope", "issue.propose requires the injected version scope")
	}
	if scope.IssueID <= 0 {
		return nil, agenttools.Invalid("scope", "issue.propose requires the injected issue scope")
	}

	classification := strings.TrimSpace(params.Classification)
	if classification != "" && !issueToolClassifications[classification] {
		return nil, agenttools.Invalid("classification", "must be one of ui, api, db, duplicate, expected_failure, flaky")
	}
	mergeReason := strings.TrimSpace(params.MergeReason)
	if len([]rune(mergeReason)) > issueToolMergeReasonMax {
		return nil, agenttools.Invalid("merge_reason", fmt.Sprintf("must be at most %d characters", issueToolMergeReasonMax))
	}
	if params.Confidence != nil && (*params.Confidence < 0 || *params.Confidence > 1) {
		return nil, agenttools.Invalid("confidence", "must be between 0 and 1")
	}

	detail, err := t.svc.Get(ctx, scope.VersionID, scope.IssueID)
	if err != nil {
		return nil, mapIssueToolError(err)
	}
	duplicateID := int64(0)
	if strings.TrimSpace(params.DuplicateOfIssueID) != "" {
		parsedID, idErr := parseIssueToolID("duplicate_of_issue_id", params.DuplicateOfIssueID)
		if idErr != nil {
			return nil, idErr
		}
		duplicateID = parsedID
		if duplicateID == detail.Issue.ID {
			return nil, agenttools.Invalid("duplicate_of_issue_id", "must not reference the issue itself")
		}
		target, getErr := t.svc.repo.Get(ctx, detail.Issue.VersionID, duplicateID)
		if getErr != nil {
			if errors.Is(getErr, delivery.ErrNotFound) {
				return nil, agenttools.Invalid("duplicate_of_issue_id", "must reference an existing issue of the same version")
			}
			return nil, fmt.Errorf("load duplicate issue %d: %w", duplicateID, getErr)
		}
		if target.Status == delivery.IssueStatusMerged {
			return nil, agenttools.Invalid("duplicate_of_issue_id", "must not reference a merged issue")
		}
	}

	output := map[string]any{
		"issue_id":   formatIssueID(detail.Issue.ID),
		"version_id": formatIssueID(detail.Issue.VersionID),
	}
	if classification != "" {
		output["classification"] = classification
	}
	if duplicateID > 0 {
		output["duplicate_of_issue_id"] = formatIssueID(duplicateID)
	}
	if mergeReason != "" {
		output["merge_reason"] = mergeReason
	}
	if params.Confidence != nil {
		output["confidence"] = *params.Confidence
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("encode triage output: %w", err)
	}

	run, err := t.analysis.StartTriage(ctx, detail.Issue.ID)
	if err != nil {
		return nil, mapIssueToolError(err)
	}
	// Bind the current agent run to the analysis row (the schema's 1:1 index).
	// A run already bound by an earlier attempt keeps its first binding; the
	// output is still written so a retry cannot strand the triage task.
	if scope.AgentRunID > 0 {
		if err := t.analysis.MarkRunning(ctx, run.ID, scope.AgentRunID); err != nil && !errors.Is(err, delivery.ErrStateConflict) {
			return nil, mapIssueToolError(err)
		}
	}
	if err := t.analysis.Complete(ctx, run.ID, delivery.AnalysisStatusDone, encoded, ""); err != nil {
		return nil, mapIssueToolError(err)
	}

	finding := TriageFinding{
		VersionID:     detail.Issue.VersionID,
		IssueID:       detail.Issue.ID,
		AnalysisRunID: run.ID,
		AgentRunID:    scope.AgentRunID,
		Content:       triageFindingContent(detail.Issue, classification, duplicateID, mergeReason, params.Confidence),
		Refs: []TriageFindingRef{
			{Kind: "issue", RefID: detail.Issue.ID},
		},
	}
	if duplicateID > 0 {
		finding.Refs = append(finding.Refs, TriageFindingRef{Kind: "issue", RefID: duplicateID})
	}
	t.analysis.PostTriageFinding(ctx, finding)

	result := map[string]any{
		"analysis_run_id": formatIssueID(run.ID),
		"status":          string(delivery.AnalysisStatusDone),
		"issue_id":        formatIssueID(detail.Issue.ID),
		"proposal":        output,
		"team_post":       t.analysis.poster != nil,
	}
	return result, nil
}

// triageFindingContent renders the concise delivery team-room finding of one
// proposal. The issue row itself is never mutated.
func triageFindingContent(issue delivery.Issue, classification string, duplicateID int64, mergeReason string, confidence *float64) string {
	parts := []string{fmt.Sprintf("issue #%d 分诊", issue.ID)}
	if classification != "" {
		parts = append(parts, "classification="+classification)
	}
	if duplicateID > 0 {
		parts = append(parts, "duplicate_of="+formatIssueID(duplicateID))
	}
	if confidence != nil {
		parts = append(parts, fmt.Sprintf("confidence=%.2f", *confidence))
	}
	if mergeReason != "" {
		parts = append(parts, "reason="+mergeReason)
	}
	return strings.Join(parts, " ")
}

// --- shared ID/error helpers ------------------------------------------------

// issueToolTargetID resolves the optional model-supplied issue selector against
// the runtime-injected scope. The scope is the default target; a model may only
// narrow to an explicit issue of the same version (which Get enforces).
func issueToolTargetID(raw string, scoped int64) (int64, *agenttools.Error) {
	if strings.TrimSpace(raw) == "" {
		if scoped > 0 {
			return scoped, nil
		}
		return 0, agenttools.Invalid("issue_id", "is required because the task did not inject an issue scope")
	}
	return parseIssueToolID("issue_id", raw)
}

func parseIssueToolID(field, raw string) (int64, *agenttools.Error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, agenttools.Invalid(field, "must be a positive decimal ID")
	}
	return parsed, nil
}

// mapIssueToolError turns issue/analysis sentinels into model-visible tool
// errors; unexpected failures stay internal and fail the run.
func mapIssueToolError(err error) error {
	if err == nil {
		return nil
	}
	var toolErr *agenttools.Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	switch {
	case errors.Is(err, delivery.ErrNotFound):
		return agenttools.NotFound("issue")
	case errors.Is(err, delivery.ErrStateConflict):
		return agenttools.StateConflict("issue is not in a valid state")
	case errors.Is(err, delivery.ErrInvalidArgument):
		return agenttools.Invalid("", err.Error())
	default:
		return fmt.Errorf("issue tool: %w", err)
	}
}
