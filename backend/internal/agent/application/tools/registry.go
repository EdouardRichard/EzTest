// Package tools implements the P1 agent tool registry: scoped, validated tool
// definitions executed by the runtime. JSON schemas never expose the injected
// scope (version_id/conversation_id/requirement_id) so the model cannot pick
// another version; the runner passes Scope to Execute out of band.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Stable P1 tool error codes (spec §8.4). They are returned to the model and
// recorded in the tool transcript; internal failures are not masked as them.
const (
	CodeInvalidArgument = "invalid_argument"
	CodeNotFound        = "not_found"
	CodeStateConflict   = "state_conflict"
	CodeLeaseLost       = "lease_lost"
	CodeAIUnavailable   = "ai_unavailable"
)

// P1 tool names. P2+ tools stay reserved and are not registered.
const (
	ToolContextVersionPack         = "context.version_pack"
	ToolContextTaskPack            = "context.task_pack"
	ToolContextTeamRoomRead        = "context.team_room_read"
	ToolContextSummaryUpdate       = "context.summary_update"
	ToolRequirementRead            = "requirement.read"
	ToolRequirementDraftCreate     = "requirement.draft_create"
	ToolRequirementDraftUpdate     = "requirement.draft_update"
	ToolRequirementRelationPropose = "requirement.relation_propose"
	ToolRequirementReopenPropose   = "requirement.reopen_propose"
	ToolAttachmentList             = "attachment.list"
	ToolAttachmentReadText         = "attachment.read_text"
	ToolAttachmentImageRef         = "attachment.image_ref"
	ToolSUTList                    = "sut.list"
	ToolSUTPageList                = "sut.page_list"
	// ToolSUTDatabaseList is the v0.22 E-1 read-only database inventory
	// (R-E1-2). It is additive: the frozen P1 registration order above does not
	// include it; resources.go registers it through RegisterP4ToolGroup when the
	// resource port also implements the DatabaseLister surface.
	ToolSUTDatabaseList = "sut.database_list"
	// ToolToolResultRead is the v0.22 A-6 continuation tool (R-A6-1). It is
	// registered by NewP7Registry only when the content-store port is wired;
	// the frozen P1 registration order above is unchanged.
	ToolToolResultRead = "tool_result.read"
)

// p1ToolNames is the frozen P1 registration order.
var p1ToolNames = []string{
	ToolContextVersionPack,
	ToolContextTaskPack,
	ToolContextTeamRoomRead,
	ToolContextSummaryUpdate,
	ToolRequirementRead,
	ToolRequirementDraftCreate,
	ToolRequirementDraftUpdate,
	ToolRequirementRelationPropose,
	ToolRequirementReopenPropose,
	ToolAttachmentList,
	ToolAttachmentReadText,
	ToolAttachmentImageRef,
	ToolSUTList,
	ToolSUTPageList,
}

// P1ToolNames returns a copy of the frozen P1 tool names.
func P1ToolNames() []string {
	return append([]string(nil), p1ToolNames...)
}

// Scope is injected by the runtime for every tool execution. It is never part
// of a tool JSON schema.
type Scope struct {
	VersionID            int64
	ConversationID       int64
	RequirementID        int64
	RequirementVersionID int64
	CaseID               int64
	CaseVersionID        int64
	RoundID              int64
	AgentRunID           int64
	VersionSUTIDs        []int64
	// P3 execution scope (contracts/agent-profiles-tools.md P3 increment). The
	// case_run dispatch epoch/token fence every result write.
	WaveID        int64
	CaseRunID     int64
	IntentID      int64
	DispatchEpoch int64
	DispatchToken string
	// P4 delivery/steward scope (contracts/agent-profiles-tools.md §7.2). The
	// task target IDs are copied from the work-item payload by the runner;
	// RequirementID/RequirementVersionID above are reused by requirement.close.
	IssueID              int64
	ReportID             int64
	ReportRevision       int32
	CloseRound           int32
	ReportRevisionID     int64
	PredicateFingerprint string
	// Extraction is the v0.22 A-7 deterministic extraction context of one
	// requirement.extraction_emit call (spec R-A7-2/4/5). The extraction worker
	// (Task 17/18) injects it out of band; it is never part of a tool schema.
	// SegmentText is the exact chunk the model was shown and is the only
	// accepted source of source_quote substrings.
	Extraction ExtractionScope
}

// Tool is one scoped, schema-described agent tool.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error)
}

// Error is a structured business failure returned to the model as
// {"error":{"code","message","retryable","field","allowed","hint","details"}}.
// Internal failures are plain errors and fail the run.
//
// Retryable reports whether the same call can plausibly succeed on a retry.
// Field-level corrections (invalid_argument) are retryable because the model can
// fix the arguments; stale-revision conflicts are not blindly retryable and must
// re-read the current revision first. Field/Allowed/Hint carry the v0.22 E-1
// self-description contract (spec R-E1-3): the exact JSON path, the legal enum
// values and the next action the model should take. They are also mirrored into
// Details for older consumers (the frontend cards read both shapes).
type Error struct {
	Code      string
	Message   string
	Retryable bool
	Field     string
	Allowed   []string
	Hint      string
	Details   map[string]any
	// Cause is an optional sentinel wrapped by this tool error. It lets callers
	// classify a failure with errors.Is while still receiving the typed
	// invalid_argument shape (for example ErrSourceQuoteNotFound on
	// requirement.extraction_emit). It is never rendered into Payload.
	Cause error
}

// Error implements error.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Unwrap returns the optional classification sentinel.
func (e *Error) Unwrap() error { return e.Cause }

// NewError builds a structured tool error.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Invalid builds an invalid_argument tool error for one field.
func Invalid(field, message string) *Error {
	err := &Error{Code: CodeInvalidArgument, Message: message}
	if field != "" {
		err.Details = map[string]any{"field": field}
	}
	return err
}

// InvalidField builds a retryable field-level invalid_argument error (v0.22
// R-E1-3). The model can fix the named argument and retry the same call.
func InvalidField(field, message string) *Error {
	err := Invalid(field, message)
	err.Retryable = true
	err.Field = field
	return err
}

// InvalidWithAllowed builds a retryable field-level invalid_argument error that
// also names the accepted values and the next action. Used by the self-describing
// case.graph_propose contract for enums, database ids and unknown fields.
func InvalidWithAllowed(field, message string, allowed []string, hint string) *Error {
	err := InvalidField(field, message)
	err.Allowed = append([]string(nil), allowed...)
	err.Hint = hint
	return err
}

// NotFound builds a not_found tool error for one entity kind.
func NotFound(kind string) *Error {
	return &Error{Code: CodeNotFound, Message: kind + " not found", Details: map[string]any{"kind": kind}}
}

// StateConflict builds a state_conflict tool error.
func StateConflict(message string) *Error {
	return &Error{Code: CodeStateConflict, Message: message}
}

// Payload renders the error in the shared tool result shape. The v0.22 E-1
// contract exposes code/message/retryable/field/allowed/hint at the top level
// and mirrors field/allowed/hint into details so both the new frontend cards and
// older readers (which only look at details) see the same correction data.
func (e *Error) Payload() map[string]any {
	details := make(map[string]any, len(e.Details)+3)
	for key, value := range e.Details {
		details[key] = value
	}
	body := map[string]any{
		"code":      e.Code,
		"message":   e.Message,
		"retryable": e.Retryable,
		"details":   details,
	}
	if e.Field != "" {
		details["field"] = e.Field
		body["field"] = e.Field
	}
	if len(e.Allowed) > 0 {
		allowed := append([]string(nil), e.Allowed...)
		details["allowed"] = allowed
		body["allowed"] = allowed
	}
	if e.Hint != "" {
		details["hint"] = e.Hint
		body["hint"] = e.Hint
	}
	return map[string]any{"error": body}
}

// Registry is the immutable set of registered tools.
type Registry struct {
	byName map[string]Tool
}

// NewRegistry builds a registry from tool implementations.
func NewRegistry(list ...Tool) *Registry {
	byName := make(map[string]Tool, len(list))
	for _, tool := range list {
		if tool == nil {
			continue
		}
		byName[tool.Name()] = tool
	}
	return &Registry{byName: byName}
}

// Get returns one registered tool.
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	tool, ok := r.byName[name]
	return tool, ok
}

// Names returns the registered tool names in sorted order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Definitions renders llm tool definitions for the profile's allowed tools.
// Unknown names (for example a profile seeded before a tool was removed) are
// skipped defensively.
func (r *Registry) Definitions(names []string) []llmapp.ToolDefinition {
	if r == nil {
		return nil
	}
	definitions := make([]llmapp.ToolDefinition, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		tool, ok := r.byName[name]
		if !ok {
			continue
		}
		seen[name] = struct{}{}
		definitions = append(definitions, llmapp.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
		})
	}
	return definitions
}

// Execute runs one tool with the injected scope.
func (r *Registry) Execute(ctx context.Context, scope Scope, name string, raw json.RawMessage) (any, error) {
	tool, ok := r.Get(name)
	if !ok {
		return nil, Invalid("tool", "unknown tool: "+name)
	}
	return tool.Execute(ctx, scope, raw)
}

// Deps wires the P1 tools to their owning bounded contexts. Context/* closures
// return JSON-ready values so the tools package stays independent of the
// version/team-room application types; requirement/attachment/resource ports
// use the typed DTOs below.
type Deps struct {
	VersionPack   func(ctx context.Context, scope Scope) (any, error)
	TaskPack      func(ctx context.Context, scope Scope) (any, error)
	TeamRoomRead  func(ctx context.Context, scope Scope, maxPosts int) (any, error)
	SummaryUpdate func(ctx context.Context, scope Scope, summary string, pinnedFacts []string) (any, error)
	Requirement   RequirementPort
	Attachment    AttachmentPort
	Resource      ResourcePort
	// Design is the P2 design context surface. It is nil for P1-only wiring; the
	// design tools then fail as an internal configuration error.
	Design           DesignPort
	RollbackProposal func(ctx context.Context, scope Scope, requirementID int64, reason string) (any, error)
	// Execution is the P3 execution context surface. It is nil for P1/P2-only
	// wiring; P3 tools then fail as an internal configuration error.
	Execution ExecutionPort
	// P5 is the additive P5 codeintel/visual service surface. P1–P4 wiring
	// leaves it zero; the P5 tools then fail as an internal configuration
	// error. It is nested so the frozen P1–P4 Deps fields stay unchanged.
	P5 P5Deps
	// P7 is the additive P7 knowledge tool surface. It is nested so the frozen
	// P1–P6 Deps fields stay unchanged; a zero value disables the P7 tools.
	P7 P7Deps
	// Completion is the additive v0.22 task.complete surface (spec R-A3). It is
	// nested so the frozen P1–P7 Deps fields stay unchanged. The composition
	// root fills it per run (Task 36) and registers TaskCompleteTools through
	// RegisterP4ToolGroup; a zero value leaves task.complete unregistered.
	Completion CompletionDeps
	// ToolResult is the additive v0.22 A-6 tool-result governance surface
	// (R-A6-1..R-A6-5). The composition root fills it per run (Task 36); a nil
	// Content leaves tool_result.read unregistered and a nil Settings falls
	// back to the catalog default threshold.
	ToolResult ToolResultDeps
	// Extraction is the additive v0.22 A-7 deterministic requirement extraction
	// surface (R-A7-4/R-A7-5). A nil Sink leaves requirement.extraction_emit
	// unregistered; the extraction worker (Task 17/18) wires the requirement
	// draft/upsert sink. It is nested so the frozen P1-P7 Deps fields stay
	// unchanged.
	Extraction ExtractionDeps
}

// ExtractionDeps is the per-run wiring of the v0.22 A-7 deterministic
// requirement extraction tool. A nil Sink leaves requirement.extraction_emit
// unregistered: the additive tool group is evaluated on every registry build
// (P4 and later compose it), and the extraction worker (Task 17/18) supplies the
// requirement draft/upsert sink. It is never half-wired.
type ExtractionDeps struct {
	Sink ExtractionSink
}

// registerExtractionToolGroup appends the extraction tool only when its sink is
// wired. It uses the additive P4 tool-group mechanism from registry.go so the
// frozen P1-P7 registration sets are not edited by this task; NewP4Registry and
// every later composition (P5-P7, the runtime registry) evaluate it.
func init() {
	RegisterP4ToolGroup(func(deps Deps) []Tool {
		if deps.Extraction.Sink == nil {
			return nil
		}
		return []Tool{NewExtractionEmitTool(deps.Extraction.Sink)}
	})
}

// CompletionDeps is the per-run wiring of the task.complete tool: the
// server-side Validator selects the completion criterion by task type and
// resolves the single verified target plan item, and the CompletionWriter
// persists that verified completion. Both must be present for the tool to be
// registered; a nil writer would let a forged declaration end the run without
// the plan/fact write, so the tool is never half-wired. Validators whose reader
// is not wired fail closed with an internal missing-dependency error.
type CompletionDeps struct {
	Validator Validator
	Writer    CompletionWriter
}

// RequirementSummary is one requirement row shown to the model.
type RequirementSummary struct {
	ID               int64
	CurrentVersionID int64
	Revision         int
	Title            string
	Status           string
	Priority         string
}

// RequirementDetail is the full current requirement revision.
type RequirementDetail struct {
	RequirementSummary
	Description        string
	AcceptanceCriteria string
	Constraints        json.RawMessage
	TargetVersionSUTs  []string
	TargetPages        []string
	TargetAPIs         []string
}

// RequirementDraft is the validated draft payload submitted by a model.
type RequirementDraft struct {
	Title              string
	Description        string
	AcceptanceCriteria string
	Priority           string
	Constraints        json.RawMessage
	TargetVersionSUTs  []string
	TargetPages        []string
	TargetAPIs         []string
}

// RequirementPort is the version context's requirement application surface.
// Adapters must enforce the version scope (cross-version IDs -> not_found) and
// return *tools.Error or *platform.Error for business failures.
type RequirementPort interface {
	ListRequirements(ctx context.Context, versionID int64) ([]RequirementSummary, error)
	GetRequirement(ctx context.Context, versionID, requirementID int64) (RequirementDetail, error)
	CreateDraft(ctx context.Context, versionID int64, draft RequirementDraft) (RequirementSummary, error)
	UpdateDraft(ctx context.Context, versionID, requirementID int64, draft RequirementDraft) (RequirementSummary, error)
	ProposeRelation(ctx context.Context, versionID, fromID, toID int64, kind, description string) error
}

// AttachmentMeta is one attachment row shown to the model.
type AttachmentMeta struct {
	ID             int64
	ConversationID int64
	RequirementID  int64
	Name           string
	MIME           string
	Size           int64
	Status         string
}

// AttachmentText is one page of extracted attachment text plus the structured
// parse status. A non-done status is data, never a tool error: the model is
// told "解析中，可重试" or the concrete parse failure reason (§8.5).
type AttachmentText struct {
	AttachmentID int64
	Name         string
	MIME         string
	Status       string
	Offset       int
	Text         string
	TotalChars   int
	Truncated    bool
	Error        string
	Retryable    bool
}

// AttachmentImage is image metadata; P1 performs no vision understanding.
type AttachmentImage struct {
	AttachmentID int64
	SHA256       string
	URL          string
	MIME         string
}

// AttachmentPort is the version context's attachment application surface.
type AttachmentPort interface {
	ListAttachments(ctx context.Context, versionID, conversationID int64) ([]AttachmentMeta, error)
	ReadAttachmentText(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (AttachmentText, error)
	AttachmentImageRef(ctx context.Context, versionID, conversationID, attachmentID int64) (AttachmentImage, error)
}

// SUTSummary is one version-SUT binding shown to the model.
type SUTSummary struct {
	VersionSUTID int64
	Name         string
	Environment  string
	Scope        string
}

// ResourcePage is one page or API bound to a version SUT.
type ResourcePage struct {
	VersionSUTID int64
	// ID is the bound page/API catalog ID as a decimal string. requirement
	// target_pages/target_apis only accept these IDs; paths are rejected by the
	// version context, so the model must receive them from sut.page_list.
	ID     string
	Kind   string
	Name   string
	Path   string
	Method string
}

// ResourcePort is the resource/version context read surface for SUT data.
type ResourcePort interface {
	ListSUTs(ctx context.Context, versionID int64) ([]SUTSummary, error)
	ListPages(ctx context.Context, versionID, versionSUTID int64) ([]ResourcePage, error)
}

// NewP1Registry registers exactly the P1 tool set (spec §5.5).
func NewP1Registry(deps Deps) *Registry {
	return NewRegistry(p1Tools(deps)...)
}

// NewP2Registry registers the frozen P1 tool set plus the P2 design tools.
// case.confirm and the granular design.* write tools are deliberately absent:
// confirmation is user/server-only and graph writes go through
// case.graph_propose (P2-D-5).
func NewP2Registry(deps Deps) *Registry {
	return NewRegistry(append(p1Tools(deps),
		&designGraphRead{deps: deps},
		&caseGraphPropose{deps: deps},
		&designReviewSubmit{deps: deps},
		&designDataPlanSubmit{deps: deps},
	)...)
}

func p1Tools(deps Deps) []Tool {
	return []Tool{
		&contextVersionPack{deps: deps},
		&contextTaskPack{deps: deps},
		&contextTeamRoomRead{deps: deps},
		&contextSummaryUpdate{deps: deps},
		&requirementRead{deps: deps},
		&requirementDraftCreate{deps: deps},
		&requirementDraftUpdate{deps: deps},
		&requirementRelationPropose{deps: deps},
		&requirementReopenPropose{deps: deps},
		&attachmentList{deps: deps},
		&attachmentReadText{deps: deps},
		&attachmentImageRef{deps: deps},
		&sutList{deps: deps},
		&sutPageList{deps: deps},
	}
}

// --- shared helpers --------------------------------------------------------

var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

func schemaObject(properties map[string]any, required ...string) json.RawMessage {
	if properties == nil {
		// Providers reject "properties": null; a no-argument tool still needs
		// an empty JSON object. The real DeepSeek API returned HTTP 400 for
		// execution.coverage_read before this guard.
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
		return emptyObjectSchema
	}
	return raw
}

// decodeParams validates that raw is a JSON object, rejects unknown properties
// (including model-supplied scope IDs) and decodes into target.
func decodeParams(raw json.RawMessage, target any) *Error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return Invalid("parameters", "parameters must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return Invalid("parameters", "invalid parameters: "+err.Error())
	}
	return nil
}

func validateText(field, value string, min, max int) *Error {
	if err := validation.String(field, value, min, max); err != nil {
		return invalidFromPlatform(err)
	}
	return nil
}

func validateStringList(field string, values []string, max int) *Error {
	if err := validation.StringList(field, values, max); err != nil {
		return invalidFromPlatform(err)
	}
	return nil
}

func parsePositiveID(field, raw string) (int64, *Error) {
	value, err := validation.NonEmptyID(field, raw)
	if err != nil {
		return 0, invalidFromPlatform(err)
	}
	return value, nil
}

func invalidFromPlatform(err error) *Error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return &Error{Code: CodeInvalidArgument, Message: apiErr.Message, Details: apiErr.Details}
	}
	return Invalid("", err.Error())
}

// mapPortError turns adapter failures into model-visible business errors when
// the adapter classified them, and keeps internal failures internal.
func mapPortError(err error) error {
	if err == nil {
		return nil
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == 404:
			return &Error{Code: CodeNotFound, Message: apiErr.Message, Details: apiErr.Details}
		case apiErr.Code == "lease_lost":
			return &Error{Code: CodeLeaseLost, Message: apiErr.Message, Details: apiErr.Details}
		case apiErr.Status == 409:
			return &Error{Code: CodeStateConflict, Message: apiErr.Message, Details: apiErr.Details}
		case apiErr.Status == 400:
			return &Error{Code: CodeInvalidArgument, Message: apiErr.Message, Details: apiErr.Details}
		}
	}
	return fmt.Errorf("tool port failure: %w", err)
}

func missingDep(name string) error {
	return fmt.Errorf("tool dependency %q is not configured", name)
}

func idString(value int64) string { return strconv.FormatInt(value, 10) }
