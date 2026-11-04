package tools

import (
	"context"
	"encoding/json"
)

type sutList struct{ deps Deps }

func (t *sutList) Name() string { return ToolSUTList }

func (t *sutList) Description() string {
	return "列出当前版本绑定的 SUT（version_sut）及其环境，供需求目标选择。"
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
			"version_sut_id": idString(item.VersionSUTID),
			"name":           item.Name,
			"environment":    item.Environment,
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
	VersionSUTID string `json:"version_sut_id"`
}

type sutPageList struct{ deps Deps }

func (t *sutPageList) Name() string { return ToolSUTPageList }

func (t *sutPageList) Description() string {
	return "列出当前版本绑定 SUT 的页面/API 清单（只读），每条的 id 是该页面/API 的十进制 ID，供 requirement.draft_create 的 target_pages/target_apis 直接使用；version_sut_id 省略时返回该版本全部。"
}

func (t *sutPageList) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"version_sut_id": map[string]any{
			"type":        "string",
			"description": "version_sut ID（十进制字符串）；省略时返回全部绑定。",
		},
	})
}

func (t *sutPageList) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params sutPageListParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	var versionSUTID int64
	if rawID := params.VersionSUTID; rawID != "" {
		parsed, toolErr := parsePositiveID("version_sut_id", rawID)
		if toolErr != nil {
			return nil, toolErr
		}
		versionSUTID = parsed
	}
	if t.deps.Resource == nil {
		return nil, missingDep("resource")
	}
	items, err := t.deps.Resource.ListPages(ctx, scope.VersionID, versionSUTID)
	if err != nil {
		return nil, mapPortError(err)
	}
	pages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		pages = append(pages, map[string]any{
			"version_sut_id": idString(item.VersionSUTID),
			"id":             item.ID,
			"kind":           item.Kind,
			"name":           item.Name,
			"path":           item.Path,
			"method":         item.Method,
		})
	}
	return map[string]any{
		"version_id": idString(scope.VersionID),
		"pages":      pages,
		"count":      len(pages),
	}, nil
}

// DatabaseSummary is one target database of a bound version environment, as
// exposed to the model (v0.22 R-E1-2). ID is the decimal catalog id accepted by
// case assertions[].database_id; it is never inferred from a name or kind.
type DatabaseSummary struct {
	ID           string
	VersionSUTID int64
	Name         string
	Kind         string
	ReadOnly     bool
}

// DatabaseLister is the additive read surface behind sut.database_list and the
// database_id guidance of case.graph_propose. The version adapter implements it.
// It is a separate interface (not a ResourcePort method) so existing resource
// ports and test fakes keep compiling; a port that does not implement it simply
// does not expose the new tool.
type DatabaseLister interface {
	ListDatabases(ctx context.Context, versionID, versionSUTID int64) ([]DatabaseSummary, error)
}

type sutDatabaseListParams struct {
	VersionSUTID string `json:"version_sut_id"`
}

type sutDatabaseList struct{ deps Deps }

func (t *sutDatabaseList) Name() string { return ToolSUTDatabaseList }

func (t *sutDatabaseList) Description() string {
	return "列出当前版本绑定环境可用的目标数据库（只读）。每条的 id 是十进制字符串，db 断言（assertions[].kind=db）必须原样使用其中一个 database_id；不要猜测、不要用库名/DSN 代替 id。version_sut_id 省略时返回该版本全部绑定。"
}

func (t *sutDatabaseList) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"version_sut_id": map[string]any{
			"type":        "string",
			"description": "可选。version_sut ID（十进制字符串）；省略时返回该版本全部绑定。",
			"pattern":     `^[1-9][0-9]*$`,
		},
	})
}

func (t *sutDatabaseList) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params sutDatabaseListParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, retryableParamError(err)
	}
	var versionSUTID int64
	if rawID := params.VersionSUTID; rawID != "" {
		parsed, toolErr := parsePositiveID("version_sut_id", rawID)
		if toolErr != nil {
			return nil, toolErr
		}
		versionSUTID = parsed
	}
	lister, ok := t.deps.Resource.(DatabaseLister)
	if !ok {
		return nil, missingDep("database resource")
	}
	items, err := lister.ListDatabases(ctx, scope.VersionID, versionSUTID)
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
		if item.VersionSUTID > 0 {
			entry["version_sut_id"] = idString(item.VersionSUTID)
		}
		databases = append(databases, entry)
	}
	return map[string]any{
		"version_id": idString(scope.VersionID),
		"databases":  databases,
		"count":      len(databases),
		"hint":       "在 case.graph_propose 的 db 断言中原样使用这里的 id 作为 database_id",
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
// mechanism. The frozen P1 registration order is untouched; NewP4Registry and
// every later composition (the runner builds NewP7Registry) evaluate this group.
// A resource port without the DatabaseLister surface leaves the tool unregistered
// instead of failing at call time.
func init() {
	RegisterP4ToolGroup(func(deps Deps) []Tool {
		if deps.Resource == nil {
			return nil
		}
		if _, ok := deps.Resource.(DatabaseLister); !ok {
			return nil
		}
		return []Tool{&sutDatabaseList{deps: deps}}
	})
}
