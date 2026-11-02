package versionadapter

import (
	"context"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ListSUTs returns the version's SUT bindings for the sut.list tool.
func (a *Adapter) ListSUTs(ctx context.Context, versionID int64) ([]tools.SUTSummary, error) {
	if a.versions == nil {
		return nil, a.missing("versions")
	}
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	summaries := make([]tools.SUTSummary, 0, len(view.Bindings))
	for _, binding := range view.Bindings {
		summaries = append(summaries, tools.SUTSummary{
			VersionSUTID: binding.ID,
			Name:         binding.SUT.Name,
			Environment:  binding.Environment.Name,
			Scope:        fmt.Sprintf("%d pages, %d apis", len(binding.Pages), len(binding.APIs)),
		})
	}
	return summaries, nil
}

// ListPages returns the pages and APIs bound to one version SUT (versionSUTID 0
// means all bindings of the version), for the sut.page_list tool.
func (a *Adapter) ListPages(ctx context.Context, versionID, versionSUTID int64) ([]tools.ResourcePage, error) {
	if a.versions == nil {
		return nil, a.missing("versions")
	}
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	items := make([]tools.ResourcePage, 0)
	found := false
	for _, binding := range view.Bindings {
		if versionSUTID > 0 && binding.ID != versionSUTID {
			continue
		}
		found = true
		for _, page := range binding.Pages {
			items = append(items, tools.ResourcePage{
				VersionSUTID: binding.ID,
				Kind:         "page",
				Name:         page.Name,
				Path:         page.RouteTemplate,
			})
		}
		for _, api := range binding.APIs {
			items = append(items, tools.ResourcePage{
				VersionSUTID: binding.ID,
				Kind:         "api",
				Name:         api.Name,
				Path:         api.PathTemplate,
				Method:       api.Method,
			})
		}
	}
	if versionSUTID > 0 && !found {
		return nil, platform.NotFound("version_sut")
	}
	return items, nil
}
