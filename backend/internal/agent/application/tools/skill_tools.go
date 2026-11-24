package tools

import (
	"context"
	"encoding/json"
	"strings"
)

// ToolSkillRead is the skill capability-domain tool (v0.2.4-C T19, SC-C12):
// the on-demand BODY layer of progressive disclosure. The run context carries
// only each package's name/description metadata (resident layer); skill.read
// loads the full SKILL.md body when the model decides it needs that knowledge.
// Every read outcome (hit or miss) is recorded in the skill_usage ledger.
// Skills are convenience knowledge, never a capability ceiling: the general
// exec/shell channels remain the escape valve (constitution art.2.6).
const ToolSkillRead = "skill.read"

// SkillContent is one skill package body read on demand. The json tags are the
// model-facing result contract (surface ③ of the six-surface discipline).
type SkillContent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Version     int    `json:"version"`
}

// SkillReader loads one package body from the runtime skill library. found is
// false for an unknown (or hidden) package name — a miss, not an error; err is
// reserved for internal failures and classified argument errors (mapped through
// mapPortError). The composition root adapts *skillapp.Service onto it.
type SkillReader interface {
	ReadSkill(ctx context.Context, name string) (SkillContent, bool, error)
}

// SkillUsageRecorder appends one skill_usage fact per read outcome.
// *skillapp.Service satisfies it directly.
type SkillUsageRecorder interface {
	RecordUsage(ctx context.Context, runID int64, skillName string, found bool) error
}

// SkillDeps is the per-deployment wiring of the skill capability domain. Both
// ports must be present for skill.read to register: a read without accounting
// would silently lose usage facts, so the tool is never half-wired
// (CompletionDeps precedent).
type SkillDeps struct {
	Reader SkillReader
	Usage  SkillUsageRecorder
}

type skillReadArgs struct {
	Name string `json:"name"`
}

type skillReadTool struct{ deps Deps }

func (t *skillReadTool) Name() string { return ToolSkillRead }

func (t *skillReadTool) Description() string {
	return "Read the full body of one skill package on demand (skill capability domain). " +
		"The context's skill section lists available packages as name + description metadata only; " +
		"call skill.read with the exact package name (e.g. playwright-cli, api-recon) to load its SKILL.md body before applying that knowledge. " +
		"Every read (hit or miss) is recorded in the skill usage ledger. " +
		"Skills are convenience knowledge, not a capability ceiling — exec.run/shell remain the general escape valve."
}

func (t *skillReadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "the skill package name from the resident skill metadata section (e.g. playwright-cli)",
		},
	}, "name")
}

func (t *skillReadTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	reader := t.deps.Skill.Reader
	usage := t.deps.Skill.Usage
	if reader == nil || usage == nil {
		return nil, missingDep("skill library")
	}
	var args skillReadArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, Invalid("name", "name is required")
	}
	content, found, err := reader.ReadSkill(ctx, name)
	if err != nil {
		// Internal failures and classified argument errors (invalid name shape)
		// surface without a ledger row: the outcome is unknown, not a miss.
		return nil, mapPortError(err)
	}
	// Record the fact BEFORE returning either outcome. A failed fact write is a
	// retryable error, never a silently unaccounted read (constitution art.7.6).
	if usageErr := usage.RecordUsage(ctx, scope.AgentRunID, name, found); usageErr != nil {
		return nil, &Error{
			Code:      CodeStateConflict,
			Message:   "skill usage ledger write failed; retry the read: " + usageErr.Error(),
			Retryable: true,
		}
	}
	if !found {
		return nil, NotFound("skill")
	}
	return content, nil
}

// skillDomainTools builds the skill capability-domain tools. Today that is the
// single skill.read on-demand body reader; the domain is the organizing unit,
// not a ceiling (constitution art.2.1).
//
// Capability three-list (constitution Constraints B.1):
//   - skill metadata disclosure — Enabled: resident context section (name/description).
//   - skill body read — Enabled: skill.read (runtime library, admin-editable at
//     runtime without rebuild/restart, R-C11).
//   - Constrained: none. Forbidden: none (GD-01; no security-based restriction).
func skillDomainTools(deps Deps) []Tool {
	return []Tool{&skillReadTool{deps: deps}}
}

// init registers the skill domain through the additive P4 tool-group mechanism
// (browser domain precedent): an unwired reader or usage ledger leaves
// skill.read unregistered instead of failing at call time.
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.Skill.Reader == nil || deps.Skill.Usage == nil {
			return nil
		}
		return skillDomainTools(deps)
	})
}
