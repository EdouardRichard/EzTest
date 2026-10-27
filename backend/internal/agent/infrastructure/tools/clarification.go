// Package tools implements the read-only tools available to Agent runs.
//
// This file adds the 11 P3 version-tools adapters (design spec §8.3): they are
// thin shells that validate the model arguments, read the run-scoped actor from
// the invocation context (never from the model) and call the injected
// VersionToolPort. The port is declared locally so this P1 infrastructure
// package never imports the P3 domain or application packages; the composition
// root injects the P3 AgentToolService, which satisfies it structurally.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// VersionToolPort is the set of structured primitives behind the 11 P3 tools.
// Every method returns (content, isError, error): content is the model-facing
// JSON or a correctable reason, isError marks a business failure and a non-nil
// error is an infrastructure failure.
type VersionToolPort interface {
	ContextGet(ctx context.Context, versionID int64) (string, bool, error)
	UpdateDraft(ctx context.Context, versionID, requirementID int64, revision int, patch json.RawMessage, actor int64) (string, bool, error)
	QuestionUpsert(ctx context.Context, versionID int64, questionID, requirementID *int64, question string, actor int64) (string, bool, error)
	QuestionAnswer(ctx context.Context, versionID, questionID int64, answer string, actor int64) (string, bool, error)
	DecisionPropose(ctx context.Context, versionID int64, requirementID, questionID *int64, topic, decision, rationale string, actor int64) (string, bool, error)
	RelationPropose(ctx context.Context, versionID, fromRequirementID, toRequirementID int64, relationType, description string, actor int64) (string, bool, error)
	AttachmentList(ctx context.Context, versionID int64, requirementID *int64) (string, bool, error)
	AttachmentReadText(ctx context.Context, versionID, attachmentID int64, maxBytes int) (string, bool, error)
	ContextSummaryUpdate(ctx context.Context, versionID int64, summary string, actor int64) (string, bool, error)
	CodeTree(ctx context.Context, versionID int64, path string, maxEntries int) (string, bool, error)
	CodeRead(ctx context.Context, versionID int64, path string, offset, maxBytes int) (string, bool, error)
}

// toolActor is the run-scoped identity read from the invocation context. A
// write tool refuses to run when it is absent, so the model can never act
// without a real actor.
type toolActor struct {
	id      int64
	present bool
}

// toolDefinition is one tool's frozen metadata plus its execution function.
type toolDefinition struct {
	name        string
	description string
	schema      string
	run         func(ctx context.Context, port VersionToolPort, actor toolActor, args json.RawMessage) (domain.ToolResult, error)
}

// clarificationTool implements domain.Tool over one definition and the
// injected port.
type clarificationTool struct {
	port VersionToolPort
	def  toolDefinition
}

// Name implements domain.Tool.
func (t clarificationTool) Name() string { return t.def.name }

// Description implements domain.Tool.
func (t clarificationTool) Description() string { return t.def.description }

// InputSchema implements domain.Tool.
func (t clarificationTool) InputSchema() json.RawMessage { return json.RawMessage(t.def.schema) }

// Execute implements domain.Tool: the run-scoped actor comes from the invocation
// context the executor injects, never from the model arguments.
func (t clarificationTool) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	actor, _ := toolInvocationActor(ctx)
	return t.def.run(ctx, t.port, actor, args)
}

// NewClarificationTools returns the 11 P3 tools in the frozen spec §8.3 order.
// A nil port disables the tool set (the composition root always passes the P3
// AgentToolService).
func NewClarificationTools(port VersionToolPort) []domain.Tool {
	if port == nil {
		return nil
	}
	definitions := []toolDefinition{
		{
			name:        "version.context_get",
			description: "Read the current version context: version metadata, requirements with open-question counts, relations, open/answered questions, proposed/accepted decisions, attachment metadata and the clarification context summary.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"}},"required":["versionId"],"additionalProperties":false}`,
			run:         runContextGet,
		},
		{
			name:        "requirement.update_draft",
			description: "Edit a draft requirement's title, description, acceptance criteria or priority. Requires the current revision; clarified requirements cannot be edited.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"requirementId":{"type":"string","pattern":"^[0-9]+$"},"revision":{"type":"integer","minimum":1},"patch":{"type":"object","properties":{"title":{"type":"string"},"description":{"type":"string"},"acceptanceCriteria":{"type":"string"},"priority":{"type":"string","enum":["must","should","could"]}},"additionalProperties":false}},"required":["versionId","requirementId","revision","patch"],"additionalProperties":false}`,
			run:         runUpdateDraft,
		},
		{
			name:        "clarification.question_upsert",
			description: "Create a structured clarification question, or update an existing open question. Creates are idempotent for the same requirement and question text.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"questionId":{"type":"string","pattern":"^[0-9]+$"},"requirementId":{"type":"string","pattern":"^[0-9]+$"},"question":{"type":"string"}},"required":["versionId","question"],"additionalProperties":false}`,
			run:         runQuestionUpsert,
		},
		{
			name:        "clarification.question_answer",
			description: "Record an explicit answer the user gave to one open question. Only transcribe user-stated answers; never invent one.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"questionId":{"type":"string","pattern":"^[0-9]+$"},"answer":{"type":"string"}},"required":["versionId","questionId","answer"],"additionalProperties":false}`,
			run:         runQuestionAnswer,
		},
		{
			name:        "decision.propose",
			description: "Propose a design decision on a requirement or question for a human to accept or reject.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"requirementId":{"type":"string","pattern":"^[0-9]+$"},"questionId":{"type":"string","pattern":"^[0-9]+$"},"topic":{"type":"string"},"decision":{"type":"string"},"rationale":{"type":"string"}},"required":["versionId","topic","decision"],"additionalProperties":false}`,
			run:         runDecisionPropose,
		},
		{
			name:        "relation.propose",
			description: "Propose a relation between two requirements of the version. A duplicate of an existing relation returns its id.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"fromRequirementId":{"type":"string","pattern":"^[0-9]+$"},"toRequirementId":{"type":"string","pattern":"^[0-9]+$"},"relationType":{"type":"string","enum":["depends_on","conflicts_with","overlaps","shares_scope"]},"description":{"type":"string"}},"required":["versionId","fromRequirementId","toRequirementId","relationType"],"additionalProperties":false}`,
			run:         runRelationPropose,
		},
		{
			name:        "attachment.list",
			description: "List attachment metadata of a requirement or of the whole version. Never returns file bytes, text or storage paths.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"requirementId":{"type":"string","pattern":"^[0-9]+$"}},"required":["versionId"],"additionalProperties":false}`,
			run:         runAttachmentList,
		},
		{
			name:        "attachment.read_text",
			description: "Read bounded UTF-8 text content from a text attachment of the version.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"attachmentId":{"type":"string","pattern":"^[0-9]+$"},"maxBytes":{"type":"integer","minimum":1}},"required":["versionId","attachmentId"],"additionalProperties":false}`,
			run:         runAttachmentReadText,
		},
		{
			name:        "context.summary_update",
			description: "Write the active clarification session's structured context summary (at most 20 KiB).",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"summary":{"type":"string"}},"required":["versionId","summary"],"additionalProperties":false}`,
			run:         runContextSummaryUpdate,
		},
		{
			name:        "code.tree",
			description: "List files of the version's baseline code tree, optionally below one repository-relative path.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"path":{"type":"string"},"maxEntries":{"type":"integer","minimum":1,"maximum":200}},"required":["versionId"],"additionalProperties":false}`,
			run:         runCodeTree,
		},
		{
			name:        "code.read",
			description: "Read a bounded text window of one file at the version's baseline commit.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"maxBytes":{"type":"integer","minimum":1}},"required":["versionId","path"],"additionalProperties":false}`,
			run:         runCodeRead,
		},
	}
	tools := make([]domain.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, clarificationTool{port: port, def: definition})
	}
	return tools
}

// runContextGet executes version.context_get.
func runContextGet(ctx context.Context, port VersionToolPort, _ toolActor, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		VersionID string `json:"versionId"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	return portResult(port.ContextGet(ctx, versionID))
}

// runUpdateDraft executes requirement.update_draft.
func runUpdateDraft(ctx context.Context, port VersionToolPort, actor toolActor, args json.RawMessage) (domain.ToolResult, error) {
	if failure, missing := requireToolActor(actor); missing {
		return failure, nil
	}
	var input struct {
		VersionID     string          `json:"versionId"`
		RequirementID string          `json:"requirementId"`
		Revision      int             `json:"revision"`
		Patch         json.RawMessage `json:"patch"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	requirementID, ok := parsePositiveID(input.RequirementID)
	if !ok {
		return invalidToolArguments("requirementId must be a positive decimal id"), nil
	}
	if input.Revision < 1 {
		return invalidToolArguments("revision must be a positive integer"), nil
	}
	if len(bytes.TrimSpace(input.Patch)) == 0 || bytes.Equal(bytes.TrimSpace(input.Patch), []byte("null")) {
		return invalidToolArguments("patch is required"), nil
	}
	return portResult(port.UpdateDraft(ctx, versionID, requirementID, input.Revision, input.Patch, actor.id))
}

// runQuestionUpsert executes clarification.question_upsert.
func runQuestionUpsert(ctx context.Context, port VersionToolPort, actor toolActor, args json.RawMessage) (domain.ToolResult, error) {
	if failure, missing := requireToolActor(actor); missing {
		return failure, nil
	}
	var input struct {
		VersionID     string  `json:"versionId"`
		QuestionID    *string `json:"questionId"`
		RequirementID *string `json:"requirementId"`
		Question      string  `json:"question"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	questionID, ok := parseOptionalPositiveID(input.QuestionID)
	if !ok {
		return invalidToolArguments("questionId must be a positive decimal id"), nil
	}
	requirementID, ok := parseOptionalPositiveID(input.RequirementID)
	if !ok {
		return invalidToolArguments("requirementId must be a positive decimal id"), nil
	}
	if strings.TrimSpace(input.Question) == "" {
		return invalidToolArguments("question is required"), nil
	}
	return portResult(port.QuestionUpsert(ctx, versionID, questionID, requirementID, input.Question, actor.id))
}

// runQuestionAnswer executes clarification.question_answer.
func runQuestionAnswer(ctx context.Context, port VersionToolPort, actor toolActor, args json.RawMessage) (domain.ToolResult, error) {
	if failure, missing := requireToolActor(actor); missing {
		return failure, nil
	}
	var input struct {
		VersionID  string `json:"versionId"`
		QuestionID string `json:"questionId"`
		Answer     string `json:"answer"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	questionID, ok := parsePositiveID(input.QuestionID)
	if !ok {
		return invalidToolArguments("questionId must be a positive decimal id"), nil
	}
	return portResult(port.QuestionAnswer(ctx, versionID, questionID, input.Answer, actor.id))
}

// runDecisionPropose executes decision.propose.
func runDecisionPropose(ctx context.Context, port VersionToolPort, actor toolActor, args json.RawMessage) (domain.ToolResult, error) {
	if failure, missing := requireToolActor(actor); missing {
		return failure, nil
	}
	var input struct {
		VersionID     string  `json:"versionId"`
		RequirementID *string `json:"requirementId"`
		QuestionID    *string `json:"questionId"`
		Topic         string  `json:"topic"`
		Decision      string  `json:"decision"`
		Rationale     string  `json:"rationale"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	requirementID, ok := parseOptionalPositiveID(input.RequirementID)
	if !ok {
		return invalidToolArguments("requirementId must be a positive decimal id"), nil
	}
	questionID, ok := parseOptionalPositiveID(input.QuestionID)
	if !ok {
		return invalidToolArguments("questionId must be a positive decimal id"), nil
	}
	return portResult(port.DecisionPropose(ctx, versionID, requirementID, questionID, input.Topic, input.Decision, input.Rationale, actor.id))
}

// runRelationPropose executes relation.propose.
func runRelationPropose(ctx context.Context, port VersionToolPort, actor toolActor, args json.RawMessage) (domain.ToolResult, error) {
	if failure, missing := requireToolActor(actor); missing {
		return failure, nil
	}
	var input struct {
		VersionID         string `json:"versionId"`
		FromRequirementID string `json:"fromRequirementId"`
		ToRequirementID   string `json:"toRequirementId"`
		RelationType      string `json:"relationType"`
		Description       string `json:"description"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	fromRequirementID, ok := parsePositiveID(input.FromRequirementID)
	if !ok {
		return invalidToolArguments("fromRequirementId must be a positive decimal id"), nil
	}
	toRequirementID, ok := parsePositiveID(input.ToRequirementID)
	if !ok {
		return invalidToolArguments("toRequirementId must be a positive decimal id"), nil
	}
	return portResult(port.RelationPropose(ctx, versionID, fromRequirementID, toRequirementID, input.RelationType, input.Description, actor.id))
}

// runAttachmentList executes attachment.list.
func runAttachmentList(ctx context.Context, port VersionToolPort, _ toolActor, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		VersionID     string  `json:"versionId"`
		RequirementID *string `json:"requirementId"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	requirementID, ok := parseOptionalPositiveID(input.RequirementID)
	if !ok {
		return invalidToolArguments("requirementId must be a positive decimal id"), nil
	}
	return portResult(port.AttachmentList(ctx, versionID, requirementID))
}

// runAttachmentReadText executes attachment.read_text.
func runAttachmentReadText(ctx context.Context, port VersionToolPort, _ toolActor, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		VersionID    string `json:"versionId"`
		AttachmentID string `json:"attachmentId"`
		MaxBytes     int    `json:"maxBytes"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	attachmentID, ok := parsePositiveID(input.AttachmentID)
	if !ok {
		return invalidToolArguments("attachmentId must be a positive decimal id"), nil
	}
	return portResult(port.AttachmentReadText(ctx, versionID, attachmentID, input.MaxBytes))
}

// runContextSummaryUpdate executes context.summary_update.
func runContextSummaryUpdate(ctx context.Context, port VersionToolPort, actor toolActor, args json.RawMessage) (domain.ToolResult, error) {
	if failure, missing := requireToolActor(actor); missing {
		return failure, nil
	}
	var input struct {
		VersionID string `json:"versionId"`
		Summary   string `json:"summary"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	return portResult(port.ContextSummaryUpdate(ctx, versionID, input.Summary, actor.id))
}

// runCodeTree executes code.tree.
func runCodeTree(ctx context.Context, port VersionToolPort, _ toolActor, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		VersionID  string `json:"versionId"`
		Path       string `json:"path"`
		MaxEntries int    `json:"maxEntries"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	return portResult(port.CodeTree(ctx, versionID, input.Path, input.MaxEntries))
}

// runCodeRead executes code.read.
func runCodeRead(ctx context.Context, port VersionToolPort, _ toolActor, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		VersionID string `json:"versionId"`
		Path      string `json:"path"`
		Offset    int    `json:"offset"`
		MaxBytes  int    `json:"maxBytes"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	if strings.TrimSpace(input.Path) == "" {
		return invalidToolArguments("path is required"), nil
	}
	if input.Offset < 0 {
		return invalidToolArguments("offset must not be negative"), nil
	}
	return portResult(port.CodeRead(ctx, versionID, input.Path, input.Offset, input.MaxBytes))
}

// toolInvocationActor reads the run-scoped actor the P1 tool executor injected.
func toolInvocationActor(ctx context.Context) (toolActor, bool) {
	invocation, ok := agentapp.InvocationFromContext(ctx)
	if !ok || invocation.ActorUserID == nil || *invocation.ActorUserID <= 0 {
		return toolActor{}, false
	}
	return toolActor{id: *invocation.ActorUserID, present: true}, true
}

// requireToolActor builds the fail-closed result for a write tool invoked
// without a run-scoped actor.
func requireToolActor(actor toolActor) (domain.ToolResult, bool) {
	if actor.present {
		return domain.ToolResult{}, false
	}
	return domain.ToolResult{
		Content: "invalid_arguments: this tool must run inside an agent run with an authenticated actor",
		IsError: true,
	}, true
}

// decodeToolArguments unmarshals the model arguments into out. An empty
// argument string is the empty object the executor normalizes to; anything that
// is not a JSON object is a correctable invalid_arguments result.
func decodeToolArguments(args json.RawMessage, out any) (domain.ToolResult, bool) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if err := json.Unmarshal(trimmed, out); err != nil {
		return invalidToolArguments("arguments must be a JSON object with the documented fields"), true
	}
	return domain.ToolResult{}, false
}

// portResult maps one port outcome onto the tool result contract.
func portResult(content string, isError bool, err error) (domain.ToolResult, error) {
	if err != nil {
		return domain.ToolResult{}, err
	}
	return domain.ToolResult{Content: content, IsError: isError}, nil
}

// invalidToolArguments builds a correctable invalid_arguments tool result.
func invalidToolArguments(format string, args ...any) domain.ToolResult {
	return domain.ToolResult{
		Content: "invalid_arguments: " + fmt.Sprintf(format, args...),
		IsError: true,
	}
}

// parsePositiveID parses a positive decimal id. The tool contract carries every
// P3 id as a JSON string (design spec §8.3).
func parsePositiveID(raw string) (int64, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 19 {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// parseOptionalPositiveID parses an optional string id; an absent or empty
// value is nil.
func parseOptionalPositiveID(raw *string) (*int64, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, true
	}
	id, ok := parsePositiveID(*raw)
	if !ok {
		return nil, false
	}
	return &id, true
}
