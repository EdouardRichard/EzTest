package main

// v0.2.4-F T14 (SC-F10, R-E7): the composition-root adapters that feed the
// case_author discovery-fact producer (designapp.NewDiscoveryFactsProducer).
// The design context declares neutral read ports; these adapters are the only
// place that joins the execution fact stores with the version's EFFECTIVE SUT
// catalog (the single effective-scope projection — a proposed/rejected SUT, or
// an asset an admin later rejected, never reaches the design read face).
//
// page/api: the P17 asset_registration facts per effective SUT, reduced by the
// SINGLE "latest wins + approved" authority
// (executionapp.LatestApprovedAssetRegistrations), then labelled from the
// effective catalog row. A proven asset with no current effective row (later
// rejected/deleted) is out of the version scope and is skipped — the fact stays
// in the append-only store; nothing is fabricated in its place.
//
// login_session: the P18 session fixture facts of the version, gated to the
// effective SUTs. The latest-100 window is the producer's bounded projection
// (the case-author read face caps at 50 facts per kind); the admin listing
// pages the full store separately. The storage-state reference never crosses
// into the design context (credential-free read face).

import (
	"context"
	"strconv"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// designVersionViewReader is the narrow version read surface the discovery
// adapters need (*versionapp.Service satisfies it).
type designVersionViewReader interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// designDiscoverySessionWindow bounds the fixture projection window (the repo's
// page cap; the read face caps at 50 per kind anyway).
const designDiscoverySessionWindow = 100

// designDiscoveryAssetFacts adapts the P17 asset_registration fact store onto
// the design producer's proven-asset port.
type designDiscoveryAssetFacts struct {
	versions      designVersionViewReader
	registrations execution.AssetRegistrationRepository
}

func newDesignDiscoveryAssetFacts(versions designVersionViewReader, registrations execution.AssetRegistrationRepository) *designDiscoveryAssetFacts {
	return &designDiscoveryAssetFacts{versions: versions, registrations: registrations}
}

var _ designapp.ProvenAssetFactsReader = (*designDiscoveryAssetFacts)(nil)

// ProvenAssetFacts returns the version's proven page/api registrations with
// their current effective catalog labels.
func (a *designDiscoveryAssetFacts) ProvenAssetFacts(ctx context.Context, versionID int64) ([]designapp.ProvenAssetFact, error) {
	if a.versions == nil || a.registrations == nil {
		return nil, platform.Unavailable("discovery asset facts reader is not configured")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	out := make([]designapp.ProvenAssetFact, 0)
	for _, sut := range view.Binding.EffectiveSUTs() {
		registrations, err := a.registrations.ListAssetRegistrations(ctx, sut.SUTID)
		if err != nil {
			return nil, err
		}
		pages := make(map[int64]int, len(sut.Pages))
		for index, page := range sut.Pages {
			if id, parseErr := strconv.ParseInt(page.ID, 10, 64); parseErr == nil {
				pages[id] = index
			}
		}
		apis := make(map[int64]int, len(sut.APIs))
		for index, api := range sut.APIs {
			if id, parseErr := strconv.ParseInt(api.ID, 10, 64); parseErr == nil {
				apis[id] = index
			}
		}
		for _, registration := range executionapp.LatestApprovedAssetRegistrations(registrations) {
			switch registration.AssetKind {
			case execution.AssetKindPage:
				index, ok := pages[registration.AssetID]
				if !ok {
					// Proven once, but no longer in the effective catalog
					// (admin rejected/deleted): out of version scope.
					continue
				}
				page := sut.Pages[index]
				out = append(out, designapp.ProvenAssetFact{
					FactID:        registration.ID,
					SUTID:         sut.SUTID,
					AssetID:       registration.AssetID,
					AssetKind:     designapp.DiscoveryFactKindPage,
					Name:          page.Name,
					URL:           page.URL,
					RouteTemplate: page.RouteTemplate,
				})
			case execution.AssetKindAPI:
				index, ok := apis[registration.AssetID]
				if !ok {
					continue
				}
				api := sut.APIs[index]
				out = append(out, designapp.ProvenAssetFact{
					FactID:       registration.ID,
					SUTID:        sut.SUTID,
					AssetID:      registration.AssetID,
					AssetKind:    designapp.DiscoveryFactKindAPI,
					Name:         api.Name,
					Method:       api.Method,
					PathTemplate: api.PathTemplate,
					AuthRole:     api.AuthRole,
				})
			}
		}
	}
	return out, nil
}

// designDiscoverySessionFacts adapts the P18 session fixture fact store onto
// the design producer's saved-session port.
type designDiscoverySessionFacts struct {
	versions designVersionViewReader
	fixtures executionapp.SessionFixtureVersionReader
}

func newDesignDiscoverySessionFacts(versions designVersionViewReader, fixtures executionapp.SessionFixtureVersionReader) *designDiscoverySessionFacts {
	return &designDiscoverySessionFacts{versions: versions, fixtures: fixtures}
}

var _ designapp.SavedSessionFactsReader = (*designDiscoverySessionFacts)(nil)

// SavedSessionFacts returns the version's saved login fixtures (newest first),
// gated to the effective SUTs. The producer collapses re-saves to the latest
// fact per reuse key.
func (a *designDiscoverySessionFacts) SavedSessionFacts(ctx context.Context, versionID int64) ([]designapp.SavedSessionFact, error) {
	if a.versions == nil || a.fixtures == nil {
		return nil, platform.Unavailable("discovery session facts reader is not configured")
	}
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	effective := make(map[int64]bool)
	for _, sut := range view.Binding.EffectiveSUTs() {
		effective[sut.SUTID] = true
	}
	rows, _, err := a.fixtures.ListSessionFixturesByVersion(ctx, versionID, designDiscoverySessionWindow, nil)
	if err != nil {
		return nil, err
	}
	out := make([]designapp.SavedSessionFact, 0, len(rows))
	for _, row := range rows {
		if !effective[row.SUTID] {
			// The fixture's SUT left the effective scope (admin rejected it
			// after the save): it must not seed design knowledge.
			continue
		}
		out = append(out, designapp.SavedSessionFact{
			FactID:        row.ID,
			SUTID:         row.SUTID,
			EnvironmentID: row.EnvironmentID,
			Role:          row.Role,
		})
	}
	return out, nil
}
