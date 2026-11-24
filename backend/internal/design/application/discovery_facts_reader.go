package designapp

// Case-author discovery-fact PRODUCER (v0.2.4-F T14, SC-F10, R-E7).
//
// R-E7 (owner 2026-11-18) routed the producer E->F because it depends on the
// execution-side exploration facts E has no source for. This producer turns
// those facts into the neutral DiscoveryFactRecord projection the case-author
// read face (CaseAuthorFacts) renders:
//
//   - page / api  <- the P17 asset_registration facts: assets the execution
//     team explored, PROVED (evidence exists + sha256 + linked assertion
//     passed, server verdict) and registered into the catalog;
//   - login_session <- the P18 session fixture library (session.saved): login
//     states saved for cross-case reuse under (version, sut, environment, role).
//
// gap stays reserved with due version 0.3 (design §6.6 fact set has no gap;
// SC-E7 requires F to land the producer and promote by 0.3, NOT all four kinds
// this version). No gap producer is fabricated here (controller ruling
// P3/SC-F10).
//
// The source ports are declared over neutral records so the design context
// does not import the execution context (the DiscoveryFactsReader precedent).
// The composition root adapts the execution fact stores + the version's
// EFFECTIVE SUT catalog onto them (cmd/eztest/discovery_facts_wiring.go): only
// assets/SUTs inside the effective version scope reach this producer, and the
// labels it renders come from the catalog rows, never from prose heuristics.
// No facts -> an empty projection; an id is never fabricated (C T22 ruling:
// heuristic id fabrication is forgery).

import (
	"context"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ProvenAssetFact is the neutral read model of one proven asset registration:
// the execution asset_registration fact joined with its CURRENT effective
// catalog labels. AssetKind is "page" or "api" (db assets are not a case-author
// discovery kind). The label fields are empty when the catalog row carries no
// such label — they are never invented.
type ProvenAssetFact struct {
	FactID        int64
	SUTID         int64
	AssetID       int64
	AssetKind     string
	Name          string
	URL           string
	RouteTemplate string
	Method        string
	PathTemplate  string
	AuthRole      string
}

// ProvenAssetFactsReader reads one version's proven page/api registrations
// (latest approved fact per asset, effective scope only).
type ProvenAssetFactsReader interface {
	ProvenAssetFacts(ctx context.Context, versionID int64) ([]ProvenAssetFact, error)
}

// SavedSessionFact is the neutral read model of one saved login fixture (the
// P18 session.saved chain). It carries the reuse key and role only — never the
// storage-state reference or the state bytes (the read face is credential-free).
type SavedSessionFact struct {
	FactID        int64
	SUTID         int64
	EnvironmentID int64
	Role          string
}

// SavedSessionFactsReader reads one version's saved session fixtures (effective
// scope only).
type SavedSessionFactsReader interface {
	SavedSessionFacts(ctx context.Context, versionID int64) ([]SavedSessionFact, error)
}

// DiscoveryFactsSources wires the producer. A nil source leaves its kinds
// empty (partial assembly stays safe); a read failure propagates so the
// CaseAuthorFacts read face can degrade best effort with a single rule.
type DiscoveryFactsSources struct {
	Assets   ProvenAssetFactsReader
	Sessions SavedSessionFactsReader
}

type discoveryFactsProducer struct {
	assets   ProvenAssetFactsReader
	sessions SavedSessionFactsReader
}

// NewDiscoveryFactsProducer builds the case_author discovery-fact producer.
// The composition root attaches it via GraphService.SetDiscoveryFacts.
func NewDiscoveryFactsProducer(sources DiscoveryFactsSources) DiscoveryFactsReader {
	return &discoveryFactsProducer{assets: sources.Assets, sessions: sources.Sessions}
}

var _ DiscoveryFactsReader = (*discoveryFactsProducer)(nil)

// Current projects the version's execution facts into discovery records of one
// kind (an empty kind widens to every PRODUCED kind: page/api/login_session).
// gap always yields nothing — it is reserved, due 0.3, with no producer this
// version. Append-only re-registrations/re-saves collapse to the LATEST fact
// per knowledge key.
func (p *discoveryFactsProducer) Current(ctx context.Context, versionID int64, kind string) ([]DiscoveryFactRecord, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	records := make([]DiscoveryFactRecord, 0)
	if kind == "" || kind == DiscoveryFactKindPage || kind == DiscoveryFactKindAPI {
		if p.assets != nil {
			facts, err := p.assets.ProvenAssetFacts(ctx, versionID)
			if err != nil {
				return nil, err
			}
			for _, fact := range facts {
				if fact.AssetKind != DiscoveryFactKindPage && fact.AssetKind != DiscoveryFactKindAPI {
					continue
				}
				if kind != "" && fact.AssetKind != kind {
					continue
				}
				records = append(records, provenAssetDiscoveryRecord(fact))
			}
		}
	}
	if kind == "" || kind == DiscoveryFactKindLoginSession {
		if p.sessions != nil {
			facts, err := p.sessions.SavedSessionFacts(ctx, versionID)
			if err != nil {
				return nil, err
			}
			for _, fact := range facts {
				records = append(records, savedSessionDiscoveryRecord(fact))
			}
		}
	}
	return latestDiscoveryRecords(records), nil
}

// provenAssetDiscoveryRecord renders one proven registration as a discovery
// record. The knowledge key is the asset's catalog identity (page URL / API
// method+path); an asset without any label falls back to its decimal id — an
// honest reference, never an invented route.
func provenAssetDiscoveryRecord(fact ProvenAssetFact) DiscoveryFactRecord {
	payload := make(map[string]any)
	var key string
	switch fact.AssetKind {
	case DiscoveryFactKindPage:
		pageKey := fact.URL
		if pageKey == "" {
			pageKey = fact.RouteTemplate
		}
		if pageKey == "" {
			pageKey = fmt.Sprintf("asset:%d", fact.AssetID)
		}
		key = "page:" + pageKey
		payload["page_key"] = pageKey
		if fact.RouteTemplate != "" {
			payload["route_template"] = fact.RouteTemplate
		}
		if fact.Name != "" {
			payload["title"] = fact.Name
		}
	case DiscoveryFactKindAPI:
		apiKey := strings.TrimSpace(fact.Method + " " + fact.PathTemplate)
		if apiKey == "" {
			apiKey = fmt.Sprintf("asset:%d", fact.AssetID)
		}
		key = "api:" + apiKey
		payload["api_key"] = apiKey
		if fact.AuthRole != "" {
			payload["auth"] = fact.AuthRole
		}
	}
	return DiscoveryFactRecord{ID: fact.FactID, Kind: fact.AssetKind, Key: key, Payload: payload}
}

// savedSessionDiscoveryRecord renders one saved fixture as a login_session
// record: the reuse key + account role only. The storage-state reference is
// deliberately NOT carried (credential-free read face); the fixture has no
// login route fact, so none is rendered.
func savedSessionDiscoveryRecord(fact SavedSessionFact) DiscoveryFactRecord {
	return DiscoveryFactRecord{
		ID:      fact.FactID,
		Kind:    DiscoveryFactKindLoginSession,
		Key:     fmt.Sprintf("sut:%d:role:%s", fact.SUTID, fact.Role),
		Payload: map[string]any{"account_role": fact.Role},
	}
}

// latestDiscoveryRecords collapses append-only duplicates: per (kind, key) the
// record with the highest fact id wins (latest wins, the fact-store rule).
// First-seen order is kept; the read face sorts by id per kind.
func latestDiscoveryRecords(records []DiscoveryFactRecord) []DiscoveryFactRecord {
	type recordKey struct{ kind, key string }
	latest := make(map[recordKey]int, len(records))
	out := make([]DiscoveryFactRecord, 0, len(records))
	for _, record := range records {
		key := recordKey{record.Kind, record.Key}
		if index, seen := latest[key]; seen {
			if record.ID > out[index].ID {
				out[index] = record
			}
			continue
		}
		latest[key] = len(out)
		out = append(out, record)
	}
	return out
}
