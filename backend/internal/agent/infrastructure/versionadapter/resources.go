package versionadapter

import (
	"context"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// databaseKindDefault is the target-database kind exposed to the model. The
// resource domain currently accepts only postgres (resource/domain/database.go);
// the VersionView DatabaseView does not carry a Kind field yet. When the resource
// read model exposes Kind, databaseSummaries should prefer it and this constant
// stays the fallback for older rows. Task 29 owns that read model.
const databaseKindDefault = "postgres"

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
				ID:           page.ID,
				Kind:         "page",
				Name:         page.Name,
				Path:         page.RouteTemplate,
			})
		}
		for _, api := range binding.APIs {
			items = append(items, tools.ResourcePage{
				VersionSUTID: binding.ID,
				ID:           api.ID,
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

// ListDatabases returns the target databases of the version's bound environments
// (versionSUTID 0 means all bindings), for the sut.database_list tool and the
// VersionPack databases inventory (v0.22 R-E1-2). ID is the decimal catalog id
// db assertions must reuse; it is never inferred.
func (a *Adapter) ListDatabases(ctx context.Context, versionID, versionSUTID int64) ([]tools.DatabaseSummary, error) {
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
	items := make([]tools.DatabaseSummary, 0)
	found := false
	for _, binding := range view.Bindings {
		if versionSUTID > 0 && binding.ID != versionSUTID {
			continue
		}
		found = true
		items = append(items, databaseSummaries(binding)...)
	}
	if versionSUTID > 0 && !found {
		return nil, platform.NotFound("version_sut")
	}
	return items, nil
}

// databaseSummaries maps one binding's selected databases to the model-facing
// inventory. Passwords/DSNs are deliberately not carried.
func databaseSummaries(binding versionapp.BindingView) []tools.DatabaseSummary {
	items := make([]tools.DatabaseSummary, 0, len(binding.Databases))
	for _, database := range binding.Databases {
		items = append(items, tools.DatabaseSummary{
			ID:           database.ID,
			VersionSUTID: binding.ID,
			Name:         database.Name,
			Kind:         databaseKindDefault,
			ReadOnly:     database.ReadOnly,
		})
	}
	return items
}
