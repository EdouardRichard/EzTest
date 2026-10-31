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
	VersionID      int64
	ConversationID int64
	RequirementID  int64
	AgentRunID     int64
	VersionSUTIDs  []int64
}

// Tool is one scoped, schema-described agent tool.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error)
}

// Error is a structured business failure returned to the model as
// {"error":{"code","message","details"}}. Internal failures are plain errors
// and fail the run.
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

// Error implements error.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

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

// NotFound builds a not_found tool error for one entity kind.
func NotFound(kind string) *Error {
	return &Error{Code: CodeNotFound, Message: kind + " not found", Details: map[string]any{"kind": kind}}
}

// StateConflict builds a state_conflict tool error.
func StateConflict(message string) *Error {
	return &Error{Code: CodeStateConflict, Message: message}
}

// Payload renders the error in the shared tool result shape.
func (e *Error) Payload() map[string]any {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	return map[string]any{
		"error": map[string]any{
			"code":    e.Code,
			"message": e.Message,
			"details": details,
		},
	}
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
	VersionPack      func(ctx context.Context, scope Scope) (any, error)
	TaskPack         func(ctx context.Context, scope Scope) (any, error)
	TeamRoomRead     func(ctx context.Context, scope Scope, maxPosts int) (any, error)
	SummaryUpdate    func(ctx context.Context, scope Scope, summary string, pinnedFacts []string) (any, error)
	Requirement      RequirementPort
	Attachment       AttachmentPort
	Resource         ResourcePort
	RollbackProposal func(ctx context.Context, scope Scope, requirementID int64, reason string) (any, error)
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

// AttachmentText is one page of extracted attachment text.
type AttachmentText struct {
	AttachmentID int64
	Name         string
	MIME         string
	Offset       int
	Text         string
	TotalChars   int
	Truncated    bool
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
	Kind         string
	Name         string
	Path         string
	Method       string
}

// ResourcePort is the resource/version context read surface for SUT data.
type ResourcePort interface {
	ListSUTs(ctx context.Context, versionID int64) ([]SUTSummary, error)
	ListPages(ctx context.Context, versionID, versionSUTID int64) ([]ResourcePage, error)
}

// NewP1Registry registers exactly the P1 tool set (spec §5.5).
func NewP1Registry(deps Deps) *Registry {
	return NewRegistry(
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
	)
}

// --- shared helpers --------------------------------------------------------

var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

func schemaObject(properties map[string]any, required ...string) json.RawMessage {
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
