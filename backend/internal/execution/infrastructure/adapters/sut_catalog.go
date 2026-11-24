package adapters

import (
	"context"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// SUTCatalogAdapter adapts the version binding read model to the P16 resolution
// service's effective-catalog port (v0.2.4-F T9, SC-F2). It derives every row
// from versionapp.EnvironmentSUTCatalog.EffectiveSUTs — the SINGLE effective-scope
// projection (approved SUTs + approved/active assets) — so a proposed/rejected
// SUT never enters the resolution range (R-B4 trust boundary). Identity/label
// fields (intro/source/status) are borrowed from the raw catalog row, which is
// permitted for labels; the RANGE decision stays with EffectiveSUTs.
type SUTCatalogAdapter struct {
	versions VersionService
}

// NewSUTCatalogAdapter wires the resolution catalog reader over the version read
// surface (*versionapp.Service satisfies VersionService).
func NewSUTCatalogAdapter(versions VersionService) *SUTCatalogAdapter {
	return &SUTCatalogAdapter{versions: versions}
}

var _ executionapp.SUTCatalogReader = (*SUTCatalogAdapter)(nil)

// EffectiveSUTCatalog returns the effective SUTs of the version's bound
// environment with their effective page/API asset ids.
func (a *SUTCatalogAdapter) EffectiveSUTCatalog(ctx context.Context, versionID int64) ([]executionapp.ResolvedSUT, error) {
	if a.versions == nil {
		return nil, errNotConfigured("version service")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	catalog := view.Binding
	environmentID := parseViewID(catalog.Environment.ID)
	raw := rawCatalogSUTsByID(catalog)
	suts := catalog.EffectiveSUTs()
	resolved := make([]executionapp.ResolvedSUT, 0, len(suts))
	for _, sut := range suts {
		row := executionapp.ResolvedSUT{
			SUTID:         sut.SUTID,
			EnvironmentID: environmentID,
			Name:          sut.Name,
			BaseURL:       sut.BaseURL,
			PageIDs:       make([]int64, 0, len(sut.Pages)),
			APIIDs:        make([]int64, 0, len(sut.APIs)),
		}
		if rawRow, ok := raw[sut.SUTID]; ok {
			row.Intro = rawRow.Intro
			row.Source = rawRow.Source
			row.Status = rawRow.Status
		}
		for _, page := range sut.Pages {
			if id := parseViewID(page.ID); id > 0 {
				row.PageIDs = append(row.PageIDs, id)
			}
		}
		for _, api := range sut.APIs {
			if id := parseViewID(api.ID); id > 0 {
				row.APIIDs = append(row.APIIDs, id)
			}
		}
		resolved = append(resolved, row)
	}
	return resolved, nil
}

// rawCatalogSUTsByID indexes the raw catalog SUT rows by decimal id for
// identity/label lookup only (intro/source/status). It never widens the scope.
func rawCatalogSUTsByID(catalog versionapp.EnvironmentSUTCatalog) map[int64]resourceapp.SUTView {
	byID := make(map[int64]resourceapp.SUTView, len(catalog.Environment.SUTs))
	for _, row := range catalog.Environment.SUTs {
		if id := parseViewID(row.ID); id > 0 {
			byID[id] = row
		}
	}
	return byID
}
