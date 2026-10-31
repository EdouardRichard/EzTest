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
	return "列出当前版本绑定 SUT 的页面/API 清单（只读）；version_sut_id 省略时返回该版本全部。"
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
