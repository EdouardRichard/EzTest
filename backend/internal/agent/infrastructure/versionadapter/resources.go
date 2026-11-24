package versionadapter

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// The adapter implements the additive sut.read surface (v0.2.4-F P16).
var _ tools.SUTReader = (*Adapter)(nil)

// databaseKindDefault is the target-database kind exposed to the model. The
// resource domain currently accepts only postgres (resource/domain/database.go);
// the VersionView DatabaseView does not carry a Kind field yet. When the resource
// read model exposes Kind, databaseSummaries should prefer it and this constant
// stays the fallback for older rows. Task 29 owns that read model.
const databaseKindDefault = "postgres"

// ListSUTs returns the SUTs of the version's bound environment for the sut.list
// tool. v0.2.4-B: the version binds an environment directly and the SUT id is
// the model-facing sut_id key (the old per-SUT binding row is gone).
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
	suts := view.Binding.EffectiveSUTs()
	raw := rawSUTsByID(view.Binding)
	summaries := make([]tools.SUTSummary, 0, len(suts))
	for _, sut := range suts {
		// base_url/source/status are identity/label fields read from the catalog
		// row (v0.2.4-F P16 enrichment). The RANGE is still EffectiveSUTs: a
		// proposed/rejected SUT never appears here, so its labels cannot leak.
		source, status := "", ""
		if row, ok := raw[sut.SUTID]; ok {
			source, status = row.Source, row.Status
		}
		summaries = append(summaries, tools.SUTSummary{
			SUTID:       sut.SUTID,
			Name:        sut.Name,
			Environment: view.Binding.Environment.Name,
			// The counts are the EFFECTIVE scope: telling the model it can reach a
			// page nobody approved would send it at a locator that does not exist
			// in the version under test.
			Scope:   fmt.Sprintf("%d pages, %d apis", len(sut.Pages), len(sut.APIs)),
			BaseURL: sut.BaseURL,
			Source:  source,
			Status:  status,
		})
	}
	return summaries, nil
}

// ReadSUT returns one SUT of the version's bound environment in full for the
// sut.read detail tool (v0.2.4-F P16, SC-F2). Only an EFFECTIVE SUT resolves: a
// proposed/rejected SUT — or an id of another version — is a 404, exactly like
// ListPages. The asset summary is the effective scope (approved pages/APIs,
// active databases); identity/labels (intro/source/status) come from the catalog
// row. Credentials are never carried.
func (a *Adapter) ReadSUT(ctx context.Context, versionID, sutID int64) (tools.SUTDetail, error) {
	if a.versions == nil {
		return tools.SUTDetail{}, a.missing("versions")
	}
	if versionID <= 0 {
		return tools.SUTDetail{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if sutID <= 0 {
		return tools.SUTDetail{}, platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return tools.SUTDetail{}, err
	}
	for _, sut := range view.Binding.EffectiveSUTs() {
		if sut.SUTID != sutID {
			continue
		}
		detail := tools.SUTDetail{
			SUTID:         sut.SUTID,
			Name:          sut.Name,
			BaseURL:       sut.BaseURL,
			Environment:   view.Binding.Environment.Name,
			EnvironmentID: parseResourceID(view.Binding.Environment.ID),
			Family:        sut.Family,
			Pages:         make([]tools.ResourcePage, 0, len(sut.Pages)),
			APIs:          make([]tools.ResourcePage, 0, len(sut.APIs)),
			Databases:     databaseSummaries(sut.SUTID, sut.Databases),
		}
		if row, ok := rawSUTsByID(view.Binding)[sutID]; ok {
			detail.Intro = row.Intro
			detail.Source = row.Source
			detail.Status = row.Status
		}
		for _, page := range sut.Pages {
			detail.Pages = append(detail.Pages, tools.ResourcePage{
				SUTID: sut.SUTID, ID: page.ID, Kind: "page", Name: page.Name, Path: page.RouteTemplate,
			})
		}
		for _, api := range sut.APIs {
			detail.APIs = append(detail.APIs, tools.ResourcePage{
				SUTID: sut.SUTID, ID: api.ID, Kind: "api", Name: api.Name, Path: api.PathTemplate, Method: api.Method,
			})
		}
		return detail, nil
	}
	return tools.SUTDetail{}, platform.NotFound("sut")
}

// rawSUTsByID indexes the RAW catalog SUT rows by decimal id for identity/label
// lookup (intro/source/status). Reading raw rows for labels is permitted (the
// effective-scope RANGE decision stays with EffectiveSUTs); this map never widens
// the scope — callers iterate EffectiveSUTs and only borrow labels from here.
func rawSUTsByID(catalog versionapp.EnvironmentSUTCatalog) map[int64]resourceapp.SUTView {
	byID := make(map[int64]resourceapp.SUTView, len(catalog.Environment.SUTs))
	for _, row := range catalog.Environment.SUTs {
		if id := parseResourceID(row.ID); id > 0 {
			byID[id] = row
		}
	}
	return byID
}

// parseResourceID parses a decimal catalog id; invalid ids become 0.
func parseResourceID(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

// ListPages returns the pages and APIs of one SUT of the version's bound
// environment (sutID 0 means every SUT of the environment), for the
// sut.page_list tool. Only APPROVED rows are listed: the inventory is the
// version's effective scope, not the raw resource catalog, so a page or API a
// discovery run merely proposed is invisible to the model until it is approved.
func (a *Adapter) ListPages(ctx context.Context, versionID, sutID int64) ([]tools.ResourcePage, error) {
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
	for _, sut := range view.Binding.EffectiveSUTs() {
		id := sut.SUTID
		if sutID > 0 && id != sutID {
			continue
		}
		found = true
		for _, page := range sut.Pages {
			items = append(items, tools.ResourcePage{
				SUTID: id,
				ID:    page.ID,
				Kind:  "page",
				Name:  page.Name,
				Path:  page.RouteTemplate,
			})
		}
		for _, api := range sut.APIs {
			items = append(items, tools.ResourcePage{
				SUTID:  id,
				ID:     api.ID,
				Kind:   "api",
				Name:   api.Name,
				Path:   api.PathTemplate,
				Method: api.Method,
			})
		}
	}
	if sutID > 0 && !found {
		return nil, platform.NotFound("sut")
	}
	return items, nil
}

// ListDatabases returns the target databases of the SUTs of the version's bound
// environment (sutID 0 means every SUT), for the sut.database_list tool and the
// VersionPack databases inventory (v0.22 R-E1-2). Only ACTIVE databases are
// listed: execution refuses the credential of a disabled one, so offering its id
// to the model would produce a db assertion that can never run. ID is the decimal
// catalog id db assertions must reuse; it is never inferred.
func (a *Adapter) ListDatabases(ctx context.Context, versionID, sutID int64) ([]tools.DatabaseSummary, error) {
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
	for _, sut := range view.Binding.EffectiveSUTs() {
		id := sut.SUTID
		if sutID > 0 && id != sutID {
			continue
		}
		found = true
		items = append(items, databaseSummaries(id, sut.Databases)...)
	}
	if sutID > 0 && !found {
		return nil, platform.NotFound("sut")
	}
	return items, nil
}

// databaseSummaries maps one SUT's EFFECTIVE databases to the model-facing
// inventory. Passwords/DSNs are deliberately not carried.
func databaseSummaries(sutID int64, databases []resourceapp.DatabaseView) []tools.DatabaseSummary {
	items := make([]tools.DatabaseSummary, 0, len(databases))
	for _, database := range databases {
		items = append(items, tools.DatabaseSummary{
			ID:       database.ID,
			SUTID:    sutID,
			Name:     database.Name,
			Kind:     databaseKindDefault,
			ReadOnly: database.ReadOnly,
		})
	}
	return items
}
