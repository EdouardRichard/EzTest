package tools

import (
	"context"
	"encoding/json"
)

type sutList struct{ deps Deps }

func (t *sutList) Name() string { return ToolSUTList }

func (t *sutList) Description() string {
	return "列出当前版本绑定环境的 SUT 目录（name/base_url/source/status/environment 与有效资产计数 scope）。v0.2.4 起需求与用例都不再绑定资源 ID：本清单供执行/设计参考被测系统、以及为 db 断言取 sut_id，不用于选择需求目标。执行团队解析用例 sut_description 时先读本目录，再用 sut.read 读候选 SUT 的 intro 与资产详情确认匹配。"
}

func (t *sutList) Parameters() json.RawMessage { return emptyObjectSchema }

func (t *sutList) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params struct{}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if t.deps.Resource == nil {
		return nil, missingDep("resource")
	}
	items, err := t.deps.Resource.ListSUTs(ctx, scope.VersionID)
	if err != nil {
		return nil, mapPortError(err)
	}
	suts := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{
			"sut_id":      idString(item.SUTID),
			"name":        item.Name,
			"environment": item.Environment,
			"base_url":    item.BaseURL,
			"source":      item.Source,
			"status":      item.Status,
		}
		if item.Scope != "" {
			entry["scope"] = item.Scope
		}
		suts = append(suts, entry)
	}
	return map[string]any{
		"version_id": idString(scope.VersionID),
		"suts":       suts,
		"count":      len(suts),
	}, nil
}

type sutPageListParams struct {
	SUTID string `json:"sut_id"`
}

type sutPageList struct{ deps Deps }

func (t *sutPageList) Name() string { return ToolSUTPageList }

func (t *sutPageList) Description() string {
	return "列出当前版本绑定 SUT 的页面/API 清单（只读），每条的 id 是该页面/API 的十进制 ID；sut_id 省略时返回该版本全部。v0.2.4 起需求不再绑定资源 ID（requirement.draft_create 用自然语言 sut_description 描述被测系统），本工具用于了解范围内有哪些页面/接口，供澄清与探索参考。"
}

func (t *sutPageList) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id": map[string]any{
			"type":        "string",
			"description": "SUT ID（十进制字符串）；省略时返回全部绑定。",
		},
	})
}

func (t *sutPageList) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params sutPageListParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	var sutID int64
	if rawID := params.SUTID; rawID != "" {
		parsed, toolErr := parsePositiveID("sut_id", rawID)
		if toolErr != nil {
			return nil, toolErr
		}
		sutID = parsed
	}
	if t.deps.Resource == nil {
		return nil, missingDep("resource")
	}
	items, err := t.deps.Resource.ListPages(ctx, scope.VersionID, sutID)
	if err != nil {
		return nil, mapPortError(err)
	}
	pages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		pages = append(pages, map[string]any{
			"sut_id": idString(item.SUTID),
			"id":     item.ID,
			"kind":   item.Kind,
			"name":   item.Name,
			"path":   item.Path,
			"method": item.Method,
		})
	}
	return map[string]any{
		"version_id": idString(scope.VersionID),
		"pages":      pages,
		"count":      len(pages),
	}, nil
}

// DatabaseSummary is one target database of a bound version environment, as
// exposed to the model (v0.22 R-E1-2). ID is the decimal catalog id, never
// inferred from a name or kind.
//
// v0.2.4-B: this list is NO LONGER the source of a design db assertion's target.
// case.graph_propose assertions[].kind=db now takes sut_id (the SUT whose data is
// read, presence-checked only), so the database id here serves the execution
// db.query / database.schema_read tools and human orientation, not the design
// contract.
// The json tags are load-bearing, not cosmetic: context.task_pack emits
// pack.Databases straight into the tool result (runner.go versionPackResult), so
// without them the model saw {"ID":"1501","SUTID":9001,...} in PascalCase while
// every hint told it to read a `sut_id` field -- and the only decimal id it could
// actually find was the database id, which presence-only validation accepts.
//
// NAMES match the keys sut.database_list builds by hand below. TYPES must match
// too, and that is what `,string` on sut_id is for: sut.database_list emits
// idString(item.SUTID) and sut.list -- the primary source in every design hint --
// emits a string as well, while case.graph_propose parses assertions[].sut_id as
// *string against a ^[1-9][0-9]*$ schema. Without `,string` this one surface
// emitted a bare number, so a model that copied the pack's value verbatim hit a
// json.UnmarshalTypeError and burned a turn, and the row was internally
// inconsistent (id a string, sut_id a number) while the hint called them "two
// different id spaces" -- implying the same shape, differing only in meaning.
//
// `omitempty` is kept alongside `,string` to match sut.database_list's
// `if item.SUTID > 0` guard and the prompt section's, so an unresolved row omits
// the key on all three surfaces instead of emitting a 0 (or "0") that the design
// gate rejects for not being positive.
type DatabaseSummary struct {
	ID       string `json:"id"`
	SUTID    int64  `json:"sut_id,omitempty,string"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ReadOnly bool   `json:"read_only"`
}

// DatabaseLister is the additive read surface behind sut.database_list. The
// version adapter implements it.
// It is a separate interface (not a ResourcePort method) so existing resource
// ports and test fakes keep compiling; a port that does not implement it simply
// does not expose the new tool.
type DatabaseLister interface {
	ListDatabases(ctx context.Context, versionID, sutID int64) ([]DatabaseSummary, error)
}

type sutDatabaseListParams struct {
	SUTID string `json:"sut_id"`
}

type sutDatabaseList struct{ deps Deps }

func (t *sutDatabaseList) Name() string { return ToolSUTDatabaseList }

func (t *sutDatabaseList) Description() string {
	return "列出当前版本绑定环境可用的目标数据库（只读），供执行期 db.query / database.schema_read 与人工核对使用。每条的 id 是十进制字符串，不要猜测、不要用库名/DSN 代替。注意：case.graph_propose 的 db 断言（assertions[].kind=db）不再使用 database_id，而是必填 sut_id（被断言数据所属 SUT 的十进制 ID，取自 sut.list 或本清单每条的 sut_id 字段）。sut_id 参数省略时返回该版本全部绑定。"
}

func (t *sutDatabaseList) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id": map[string]any{
			"type":        "string",
			"description": "可选。SUT ID（十进制字符串）；省略时返回该版本全部绑定。",
			"pattern":     `^[1-9][0-9]*$`,
		},
	})
}

func (t *sutDatabaseList) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params sutDatabaseListParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, retryableParamError(err)
	}
	var sutID int64
	if rawID := params.SUTID; rawID != "" {
		parsed, toolErr := parsePositiveID("sut_id", rawID)
		if toolErr != nil {
			return nil, retryableParamError(toolErr)
		}
		sutID = parsed
	}
	lister, ok := t.deps.Resource.(DatabaseLister)
	if !ok {
		return nil, missingDep("database resource")
	}
	items, err := lister.ListDatabases(ctx, scope.VersionID, sutID)
	if err != nil {
		return nil, mapPortError(err)
	}
	databases := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{
			"id":        item.ID,
			"name":      item.Name,
			"kind":      item.Kind,
			"read_only": item.ReadOnly,
		}
		if item.SUTID > 0 {
			entry["sut_id"] = idString(item.SUTID)
		}
		databases = append(databases, entry)
	}
	return map[string]any{
		"version_id": idString(scope.VersionID),
		"databases":  databases,
		"count":      len(databases),
		"hint":       "执行期 db.query/database.schema_read 用这里的 database id；case.graph_propose 的 db 断言改用每条的 sut_id（不是 database id）",
	}, nil
}

// retryableParamError promotes a raw-parameter decode failure to the v0.22 E-1
// retryable field-level shape. The model can always fix the arguments and retry.
func retryableParamError(err *Error) *Error {
	if err == nil {
		return nil
	}
	err.Retryable = true
	if err.Field == "" {
		if field, _ := err.Details["field"].(string); field != "" {
			err.Field = field
		}
	}
	return err
}

// init registers the additive database inventory tool through the P4 tool-group
// mechanism. The frozen P1 registration order is untouched; NewToolGroupRegistry and
// every later composition (the runner builds NewKnowledgeRegistry) evaluate this group.
// A resource port without the DatabaseLister surface leaves the tool unregistered
// instead of failing at call time.
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.Resource == nil {
			return nil
		}
		if _, ok := deps.Resource.(DatabaseLister); !ok {
			return nil
		}
		return []Tool{&sutDatabaseList{deps: deps}}
	})
}
