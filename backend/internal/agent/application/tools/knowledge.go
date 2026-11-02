package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ToolKnowledgePropose is the single P7 tool (contracts/agent-profiles-tools.md
// §10.2). It can only create draft knowledge; review, edit and delete stay in
// the admin API.
const ToolKnowledgePropose = "knowledge.propose"

// Physical limits mirror knowledge_item (contracts/db-schema.md §16.1) so the
// model gets a stable invalid_argument before a useless write.
const (
	maxKnowledgeTitleRunes     = 200
	maxKnowledgeContentRunes   = 20000
	maxKnowledgeTags           = 20
	maxKnowledgeTagRunes       = 40
	maxKnowledgeSystemKeyRunes = 64
)

// Knowledge scope values accepted by knowledge.propose.
const (
	knowledgeScopeGlobal = "global"
	knowledgeScopeSystem = "system"
)

// KnowledgeProposal is one parsed knowledge.propose payload. The writer owns
// the enforced status/source/creator fields; they are deliberately absent here
// so a model can never choose them.
type KnowledgeProposal struct {
	Title     string
	Content   string
	Tags      []string
	Scope     string
	SystemKey *string
}

// KnowledgeProposalResult is the created draft reference returned to the model.
type KnowledgeProposalResult struct {
	ID       int64
	Status   string
	Scope    string
	Revision int
}

// KnowledgeProposePort writes one AI knowledge draft. The adapter
// (agent/infrastructure/knowledgeadapter) forces status=draft, source and
// promoted version = the current version and created_by = the run actor.
type KnowledgeProposePort interface {
	ProposeKnowledge(ctx context.Context, versionID, agentRunID int64, in KnowledgeProposal) (KnowledgeProposalResult, error)
}

// P7Deps is the additive P7 service surface. It is nested inside Deps so the
// frozen P1–P6 wiring keeps compiling untouched; a nil writer makes the P7
// tool fail as an internal configuration error, never as a model-visible
// business error.
type P7Deps struct {
	Knowledge KnowledgeProposePort
}

// P7ToolNames returns a copy of the frozen P7 tool names.
func P7ToolNames() []string {
	return []string{ToolKnowledgePropose}
}

// NewP7Registry registers the frozen P1–P6 tool set plus the P7 knowledge tool.
// Profiles still filter the registry by allowed_tools, so only the five P7
// profiles can call knowledge.propose.
func NewP7Registry(deps Deps) *Registry {
	base := NewP5Registry(deps)
	all := make([]Tool, 0, len(base.byName)+1)
	all = append(all, &knowledgePropose{deps: deps})
	for _, name := range base.Names() {
		tool, _ := base.Get(name)
		all = append(all, tool)
	}
	return NewRegistry(all...)
}

type knowledgePropose struct{ deps Deps }

func (t *knowledgePropose) Name() string { return ToolKnowledgePropose }

func (t *knowledgePropose) Description() string {
	return "提议一条待管理员审核的跨版本知识（仅创建 draft，审核后才会注入后续 run；不能审核、修改、删除知识，也不能指定来源版本或状态）。"
}

func (t *knowledgePropose) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"title": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxKnowledgeTitleRunes,
			"description": "知识标题。",
		},
		"content": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxKnowledgeContentRunes,
			"description": "知识正文（作为参考数据，不会成为系统指令）。",
		},
		"tags": map[string]any{
			"type":     "array",
			"maxItems": maxKnowledgeTags,
			"items":    map[string]any{"type": "string"},
		},
		"scope": map[string]any{
			"type": "string",
			"enum": []string{knowledgeScopeGlobal, knowledgeScopeSystem},
		},
		"system_key": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxKnowledgeSystemKeyRunes,
			"description": "scope=system 时的 SUT family（小写 slug）。",
		},
	}, "title", "content")
}

// knowledgeProposePayload is the model-visible argument shape. Server-controlled
// fields (status/source_version_id/promoted_from_version_id/created_by) are not
// part of the schema and are ignored when a model still sends them
// (contracts/agent-profiles-tools.md §10.2).
type knowledgeProposePayload struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	Scope     string   `json:"scope"`
	SystemKey *string  `json:"system_key"`
}

func (t *knowledgePropose) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.P7.Knowledge == nil {
		return nil, missingDep("knowledge proposal writer")
	}
	var params knowledgeProposePayload
	if err := decodeKnowledgeProposal(raw, &params); err != nil {
		return nil, err
	}
	proposal, toolErr := buildKnowledgeProposal(params)
	if toolErr != nil {
		return nil, toolErr
	}
	if scope.VersionID <= 0 {
		return nil, Invalid("scope", "the version scope is required")
	}
	created, err := t.deps.P7.Knowledge.ProposeKnowledge(ctx, scope.VersionID, scope.AgentRunID, proposal)
	if err != nil {
		return nil, mapPortError(err)
	}
	return map[string]any{
		"knowledge_id": idString(created.ID),
		"status":       created.Status,
		"scope":        created.Scope,
		"revision":     created.Revision,
	}, nil
}

// decodeKnowledgeProposal validates that raw is a JSON object and decodes it
// leniently: unknown properties are ignored (the contract requires ignoring
// server-controlled fields rather than failing the call).
func decodeKnowledgeProposal(raw json.RawMessage, target *knowledgeProposePayload) *Error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return Invalid("parameters", "parameters must be a JSON object")
	}
	if err := json.Unmarshal(trimmed, target); err != nil {
		return Invalid("parameters", "invalid parameters: "+err.Error())
	}
	return nil
}

// buildKnowledgeProposal normalizes and validates the model arguments with the
// same physical rules as the knowledge API (title/content bounds, tag caps,
// scope/system_key combination). Server-side validation still runs in the
// knowledge service; this only short-circuits obviously invalid calls.
func buildKnowledgeProposal(params knowledgeProposePayload) (KnowledgeProposal, *Error) {
	title := strings.TrimSpace(params.Title)
	if err := validateText("title", title, 1, maxKnowledgeTitleRunes); err != nil {
		return KnowledgeProposal{}, err
	}
	content := strings.TrimSpace(params.Content)
	if err := validateText("content", content, 1, maxKnowledgeContentRunes); err != nil {
		return KnowledgeProposal{}, err
	}

	tags := make([]string, 0, len(params.Tags))
	seen := make(map[string]struct{}, len(params.Tags))
	for _, rawTag := range params.Tags {
		tag := strings.TrimSpace(rawTag)
		if tag == "" {
			return KnowledgeProposal{}, Invalid("tags", "tags must not contain empty items")
		}
		if utf8.RuneCountInString(tag) > maxKnowledgeTagRunes {
			return KnowledgeProposal{}, Invalid("tags", "each tag must be at most 40 characters")
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > maxKnowledgeTags {
		return KnowledgeProposal{}, Invalid("tags", "must contain at most 20 tags")
	}

	scope := strings.TrimSpace(params.Scope)
	systemKey := params.SystemKey
	if systemKey != nil {
		trimmed := strings.TrimSpace(*systemKey)
		systemKey = &trimmed
	}
	if scope == "" {
		// Contracts §10.2: no system_key -> global; system_key -> system.
		if systemKey != nil && *systemKey != "" {
			scope = knowledgeScopeSystem
		} else {
			scope = knowledgeScopeGlobal
		}
	}
	switch scope {
	case knowledgeScopeGlobal:
		if systemKey != nil && *systemKey != "" {
			return KnowledgeProposal{}, Invalid("system_key", "must be empty when scope is global")
		}
		systemKey = nil
	case knowledgeScopeSystem:
		if systemKey == nil || *systemKey == "" {
			return KnowledgeProposal{}, Invalid("system_key", "is required when scope is system")
		}
		if utf8.RuneCountInString(*systemKey) > maxKnowledgeSystemKeyRunes {
			return KnowledgeProposal{}, Invalid("system_key", "must be at most 64 characters")
		}
		if *systemKey != strings.ToLower(*systemKey) {
			return KnowledgeProposal{}, Invalid("system_key", "must be lowercase")
		}
		if strings.ContainsFunc(*systemKey, unicode.IsSpace) {
			return KnowledgeProposal{}, Invalid("system_key", "must not contain whitespace")
		}
	default:
		return KnowledgeProposal{}, Invalid("scope", "scope must be global or system")
	}

	return KnowledgeProposal{
		Title:     title,
		Content:   content,
		Tags:      tags,
		Scope:     scope,
		SystemKey: systemKey,
	}, nil
}
