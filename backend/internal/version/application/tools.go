package versionapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Version steward tool descriptions (contracts/agent-profiles-tools.md §7.3).
const (
	requirementGoalMetDescription  = "Submit a requirement closure proposal. The server recomputes the eight closure checks inside the transaction; on success it writes the closure round, marks the requirement closed and publishes requirement.closed. On failure it returns 409 with details.failed (check name -> count) and changes nothing."
	versionCloseReadDescription    = "Read the version close predicate as an eight-item snapshot (version_running, no_blocking_work_items, no_active_intents, no_open_human_requests, no_active_waves, no_inflight_case_runs, requirements_closed, version_report_published) with the failing check names. Read-only."
	versionCloseProposeDescription = "Submit a version close proposal. The server re-evaluates the close predicate inside the version row-lock transaction; on success it moves running -> completed, writes closed_at/close_reason/close_run_id and publishes version.closed. On failure it returns 409 with details.failed and changes nothing."
)

// stewardTimeLayout is the JSON wire format used for close timestamps.
const stewardTimeLayout = time.RFC3339

// VersionTools returns the frozen P4 version-steward tool group (spec §4.4 /
// contracts/agent-profiles-tools.md §7.3). The tools only ever receive the
// runtime-injected tools.Scope: their JSON schemas never expose version_id and
// the strict decoder rejects model-supplied scope IDs. The composition root
// (Task 23) registers this group through tools.RegisterP4ToolGroup.
func VersionTools(closure *ClosureService, close *VersionCloseService) []tools.Tool {
	return []tools.Tool{
		&requirementGoalMetTool{closure: closure},
		&versionCloseReadTool{close: close},
		&versionCloseProposeTool{close: close},
	}
}

// stewardSchema renders the strict JSON schema shared by the steward tools.
func stewardSchema(properties map[string]any, required ...string) json.RawMessage {
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

// --- requirement.goal_met ---------------------------------------------------

type requirementGoalMetTool struct {
	closure *ClosureService
}

func (t *requirementGoalMetTool) Name() string { return tools.ToolRequirementGoalMet }

func (t *requirementGoalMetTool) Description() string { return requirementGoalMetDescription }

func (t *requirementGoalMetTool) Parameters() json.RawMessage {
	return stewardSchema(map[string]any{
		"requirement_id": map[string]any{
			"type":        "string",
			"description": "decimal requirement ID; defaults to the requirement referenced by the current task",
		},
		"note": map[string]any{
			"type":        "string",
			"maxLength":   version.RequirementReasonMax,
			"description": "optional closure note recorded in the agent transcript",
		},
	})
}

func (t *requirementGoalMetTool) Execute(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if t.closure == nil {
		return nil, errors.New("requirement.goal_met has no closure service")
	}
	var params struct {
		RequirementID string `json:"requirement_id"`
		Note          string `json:"note"`
	}
	if err := tools.DecodeP4Params(raw, &params); err != nil {
		return nil, err
	}
	requirementID, idErr := scopedStewardID("requirement_id", params.RequirementID, scope.RequirementID)
	if idErr != nil {
		return nil, idErr
	}
	// A requirement.close task is scoped to exactly one requirement: the model
	// may select it explicitly only with the same ID, never another
	// requirement of the version (spec §7.2 "不改其他需求").
	if scope.RequirementID > 0 && requirementID != scope.RequirementID {
		return nil, tools.Invalid("requirement_id", "must match the requirement referenced by the current task")
	}
	if err := validateStewardText("note", params.Note, version.RequirementReasonMax); err != nil {
		return nil, err
	}
	if err := t.closure.checkRequirementToolScope(ctx, scope, requirementID); err != nil {
		return nil, err
	}

	var runRef *int64
	if scope.AgentRunID > 0 {
		runID := scope.AgentRunID
		runRef = &runID
	}
	closure, err := t.closure.Close(ctx, requirementID, runRef)
	if err != nil {
		return nil, mapStewardToolError(err)
	}

	payload := map[string]any{
		"requirement_id":         platformid.Format(closure.RequirementID),
		"requirement_version_id": platformid.Format(closure.RequirementVersionID),
		"closure_id":             platformid.Format(closure.ID),
		"close_round":            closure.CloseRound,
		"closed_at":              closure.ClosedAt.UTC().Format(stewardTimeLayout),
		"checks":                 closureChecksPayload(closure.CheckResult.ClosureChecks),
		"evidence_refs":          closure.EvidenceRefs,
	}
	if params.Note != "" {
		payload["note"] = params.Note
	}
	return payload, nil
}

// checkRequirementToolScope validates that the target requirement is readable
// and belongs to the runtime-injected version, and that the close task still
// targets the requirement's current revision. Cross-version IDs are reported as
// not_found so a model can never close another version's requirement.
func (s *ClosureService) checkRequirementToolScope(ctx context.Context, scope tools.Scope, requirementID int64) error {
	if scope.VersionID <= 0 {
		return tools.Invalid("scope", "requirement.goal_met requires the injected version scope")
	}
	if s.requirements == nil {
		return errors.New("closure service is missing its requirement repository")
	}
	requirement, err := s.requirements.GetRequirement(ctx, requirementID)
	if err != nil {
		return mapStewardToolError(mapRequirementRepositoryError(err))
	}
	if requirement.VersionID != scope.VersionID {
		return tools.NotFound("requirement")
	}
	if scope.RequirementVersionID > 0 &&
		(requirement.CurrentVersionID == nil || *requirement.CurrentVersionID != scope.RequirementVersionID) {
		return tools.StateConflict("the requirement revision changed after the close task was created")
	}
	return nil
}

// closureChecksPayload renders the eight requirement closure checks with their
// frozen JSON keys.
func closureChecksPayload(checks version.ClosureChecks) map[string]bool {
	return map[string]bool{
		version.ClosureCheckRequirementState:   checks.RequirementState,
		version.ClosureCheckHasConfirmedCase:   checks.HasConfirmedCase,
		version.ClosureCheckCasesTerminal:      checks.CasesTerminal,
		version.ClosureCheckFailedCaseHasIssue: checks.FailedCaseHasIssue,
		version.ClosureCheckNoOpenWork:         checks.NoOpenWork,
		version.ClosureCheckNoActiveIntent:     checks.NoActiveIntent,
		version.ClosureCheckNoInflightCaseRun:  checks.NoInflightCaseRun,
		version.ClosureCheckCoverageRecorded:   checks.CoverageRecorded,
	}
}

// --- version.close_read -----------------------------------------------------

type versionCloseReadTool struct {
	close *VersionCloseService
}

func (t *versionCloseReadTool) Name() string { return tools.ToolVersionCloseRead }

func (t *versionCloseReadTool) Description() string { return versionCloseReadDescription }

func (t *versionCloseReadTool) Parameters() json.RawMessage { return stewardSchema(nil) }

func (t *versionCloseReadTool) Execute(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if t.close == nil {
		return nil, errors.New("version.close_read has no version close service")
	}
	var params struct{}
	if err := tools.DecodeP4Params(raw, &params); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, tools.Invalid("scope", "version.close_read requires the injected version scope")
	}
	predicate, err := t.close.Evaluate(ctx, scope.VersionID)
	if err != nil {
		return nil, mapStewardToolError(err)
	}
	checks := versionCloseChecksPayload(predicate)
	failed := make([]string, 0, len(checks))
	for _, check := range versionCloseCheckList(predicate) {
		if !check.passed {
			failed = append(failed, check.name)
		}
	}
	return map[string]any{
		"version_id": platformid.Format(scope.VersionID),
		"passed":     predicate.Passed(),
		"checks":     checks,
		"failed":     failed,
	}, nil
}

// versionCloseCheck is one named close condition in stable output order.
type versionCloseCheck struct {
	name   string
	passed bool
}

// versionCloseCheckList renders the eight close conditions in the frozen
// contract order.
func versionCloseCheckList(predicate version.ClosePredicate) []versionCloseCheck {
	return []versionCloseCheck{
		{version.VersionCloseCheckRunning, predicate.Running},
		{version.VersionCloseCheckNoBlockingWorkItems, predicate.NoBlockingWorkItems},
		{version.VersionCloseCheckNoActiveIntents, predicate.NoActiveIntents},
		{version.VersionCloseCheckNoOpenHumanRequests, predicate.NoOpenHumanRequests},
		{version.VersionCloseCheckNoActiveWaves, predicate.NoActiveWaves},
		{version.VersionCloseCheckNoInflightCaseRuns, predicate.NoInflightCaseRuns},
		{version.VersionCloseCheckRequirementsClosed, predicate.RequirementsClosed},
		{version.VersionCloseCheckVersionReportPublished, predicate.VersionReportPublished},
	}
}

func versionCloseChecksPayload(predicate version.ClosePredicate) map[string]bool {
	checks := make(map[string]bool, 8)
	for _, check := range versionCloseCheckList(predicate) {
		checks[check.name] = check.passed
	}
	return checks
}

// --- version.close_propose --------------------------------------------------

type versionCloseProposeTool struct {
	close *VersionCloseService
}

func (t *versionCloseProposeTool) Name() string { return tools.ToolVersionClosePropose }

func (t *versionCloseProposeTool) Description() string { return versionCloseProposeDescription }

func (t *versionCloseProposeTool) Parameters() json.RawMessage {
	return stewardSchema(map[string]any{
		"reason": map[string]any{
			"type":      "string",
			"minLength": 1,
			"maxLength": version.VersionCloseReasonMax,
		},
	}, "reason")
}

func (t *versionCloseProposeTool) Execute(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if t.close == nil {
		return nil, errors.New("version.close_propose has no version close service")
	}
	var params struct {
		Reason string `json:"reason"`
	}
	if err := tools.DecodeP4Params(raw, &params); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(params.Reason)
	if reason == "" {
		return nil, tools.Invalid("reason", "must not be empty")
	}
	if err := validateStewardText("reason", reason, version.VersionCloseReasonMax); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, tools.Invalid("scope", "version.close_propose requires the injected version scope")
	}

	closed, err := t.close.Close(ctx, scope.VersionID, scope.AgentRunID, reason)
	if err != nil {
		return nil, mapStewardToolError(err)
	}
	payload := map[string]any{
		"version_id":   platformid.Format(closed.ID),
		"status":       closed.Status,
		"close_reason": closed.CloseReason,
	}
	if closed.ClosedAt != nil {
		payload["closed_at"] = closed.ClosedAt.UTC().Format(stewardTimeLayout)
	}
	if closed.CloseRunID != nil {
		payload["close_run_id"] = platformid.Format(*closed.CloseRunID)
	}
	return payload, nil
}

// --- shared helpers ---------------------------------------------------------

// scopedStewardID resolves an optional model-supplied ID against the
// runtime-injected scope. The scope always wins over a missing parameter; the
// model can only narrow within its own task.
func scopedStewardID(field, raw string, scoped int64) (int64, *tools.Error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if scoped > 0 {
			return scoped, nil
		}
		return 0, tools.Invalid(field, "is required because the task did not inject a scope")
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, tools.Invalid(field, "must be a positive decimal ID")
	}
	return parsed, nil
}

func validateStewardText(field, value string, max int) *tools.Error {
	if len([]rune(value)) > max {
		return tools.Invalid(field, fmt.Sprintf("must be at most %d characters", max))
	}
	return nil
}

// mapStewardToolError turns domain predicate failures and platform business
// errors into model-visible tool errors. Unexpected failures stay internal and
// fail the run.
func mapStewardToolError(err error) error {
	if err == nil {
		return nil
	}
	var toolErr *tools.Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	var requirementFailed *version.ErrClosePredicateFailed
	if errors.As(err, &requirementFailed) {
		return &tools.Error{
			Code:    tools.CodeStateConflict,
			Message: "requirement close predicate failed",
			Details: map[string]any{"failed": requirementFailed.Failed},
		}
	}
	var versionFailed *version.ErrVersionClosePredicateFailed
	if errors.As(err, &versionFailed) {
		return &tools.Error{
			Code:    tools.CodeStateConflict,
			Message: "version close predicate failed",
			Details: map[string]any{"failed": versionFailed.Failed},
		}
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 404:
			return &tools.Error{Code: tools.CodeNotFound, Message: apiErr.Message, Details: apiErr.Details}
		case 409:
			return &tools.Error{Code: tools.CodeStateConflict, Message: apiErr.Message, Details: apiErr.Details}
		case 400:
			return &tools.Error{Code: tools.CodeInvalidArgument, Message: apiErr.Message, Details: apiErr.Details}
		}
	}
	return fmt.Errorf("version steward tool: %w", err)
}
