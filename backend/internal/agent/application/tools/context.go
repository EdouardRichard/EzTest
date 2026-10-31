package tools

import (
	"context"
	"encoding/json"
)

// contextVersionPack returns the version summary, SUT/page/API names, requirement
// summaries and attachment metadata for the run's version.
type contextVersionPack struct{ deps Deps }

func (t *contextVersionPack) Name() string { return ToolContextVersionPack }

func (t *contextVersionPack) Description() string {
	return "读取当前版本摘要：版本状态、SUT/页面/API、需求摘要、附件元数据与最近对话摘要。"
}

func (t *contextVersionPack) Parameters() json.RawMessage { return emptyObjectSchema }

func (t *contextVersionPack) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params struct{}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if t.deps.VersionPack == nil {
		return nil, missingDep("version pack")
	}
	return t.deps.VersionPack(ctx, scope)
}

// contextTaskPack returns the current task objective/constraints/acceptance.
type contextTaskPack struct{ deps Deps }

func (t *contextTaskPack) Name() string { return ToolContextTaskPack }

func (t *contextTaskPack) Description() string {
	return "读取当前任务目标、约束、验收标准与注入的作用域标识。"
}

func (t *contextTaskPack) Parameters() json.RawMessage { return emptyObjectSchema }

func (t *contextTaskPack) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params struct{}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if t.deps.TaskPack == nil {
		return nil, missingDep("task pack")
	}
	return t.deps.TaskPack(ctx, scope)
}

type contextTeamRoomReadParams struct {
	MaxPosts int `json:"max_posts"`
}

// contextTeamRoomRead reads the latest digest plus related clarification posts.
type contextTeamRoomRead struct{ deps Deps }

func (t *contextTeamRoomRead) Name() string { return ToolContextTeamRoomRead }

func (t *contextTeamRoomRead) Description() string {
	return "读取澄清团队频道的最新摘要与相关帖子（最多 50 条）。"
}

func (t *contextTeamRoomRead) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"max_posts": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     50,
			"description": "返回的最近帖子数量，默认 10。",
		},
	})
}

func (t *contextTeamRoomRead) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	params := contextTeamRoomReadParams{MaxPosts: 10}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if params.MaxPosts < 1 || params.MaxPosts > 50 {
		return nil, Invalid("max_posts", "must be between 1 and 50")
	}
	if t.deps.TeamRoomRead == nil {
		return nil, missingDep("team room read")
	}
	return t.deps.TeamRoomRead(ctx, scope, params.MaxPosts)
}

type contextSummaryUpdateParams struct {
	Summary     string   `json:"summary"`
	PinnedFacts []string `json:"pinned_facts"`
}

// contextSummaryUpdate updates the rolling conversation summary and pinned
// facts (Host only). The runtime persists a new conversation_summary version.
type contextSummaryUpdate struct{ deps Deps }

func (t *contextSummaryUpdate) Name() string { return ToolContextSummaryUpdate }

func (t *contextSummaryUpdate) Description() string {
	return "更新会话滚动摘要（≤8000 字）与钉住事实（≤20 条，每条 ≤500 字）。只记录记忆，不修改需求。"
}

func (t *contextSummaryUpdate) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"maxLength":   8000,
			"description": "新的会话摘要。",
		},
		"pinned_facts": map[string]any{
			"type":        "array",
			"maxItems":    20,
			"description": "必须长期保留的事实，每条最多 500 字。",
			"items":       map[string]any{"type": "string", "maxLength": 500},
		},
	})
}

func (t *contextSummaryUpdate) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params contextSummaryUpdateParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if err := validateText("summary", params.Summary, 0, 8000); err != nil {
		return nil, err
	}
	if len(params.PinnedFacts) > 20 {
		return nil, Invalid("pinned_facts", "must contain at most 20 facts")
	}
	for _, fact := range params.PinnedFacts {
		if err := validateText("pinned_facts", fact, 0, 500); err != nil {
			return nil, err
		}
	}
	if t.deps.SummaryUpdate == nil {
		return nil, missingDep("summary update")
	}
	result, err := t.deps.SummaryUpdate(ctx, scope, params.Summary, params.PinnedFacts)
	if err != nil {
		return nil, mapPortError(err)
	}
	if result == nil {
		return map[string]any{"status": "updated"}, nil
	}
	return result, nil
}
