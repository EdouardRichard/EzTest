package providers

import (
	"context"
	"strconv"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// DesignReader is the design application surface consumed by the chain design
// provider: the current design graph plus the version-scoped historical
// case-version reader. The design graph service satisfies it.
type DesignReader interface {
	ReadGraph(ctx context.Context, versionID int64) (designapp.GraphView, error)
	ReadCaseVersionsByIDs(ctx context.Context, versionID int64, caseVersionIDs []int64) ([]designapp.CaseVersionHistoryView, error)
}

// DesignProvider supplies scenario and case-version nodes. Cases are the
// current case versions; CaseVersionsByIDs is the historical reader that keeps
// superseded rows (title/status) in the chain.
type DesignProvider struct {
	reader DesignReader
}

// NewDesignProvider wires the design graph/history reader.
func NewDesignProvider(reader DesignReader) *DesignProvider {
	return &DesignProvider{reader: reader}
}

var _ chainapp.DesignChainProvider = (*DesignProvider)(nil)

// Scenarios returns every scenario of the version design graph. The graph view
// does not project scenario timestamps, so the node keeps zero CreatedAt and
// UpdatedAt: event-range replay must keep scenarios instead of dropping them
// against a request-time timestamp, and the HTTP DTO supplies a display
// timestamp from generated_at.
func (p *DesignProvider) Scenarios(ctx context.Context, versionID int64) ([]chain.Node, error) {
	graph, err := p.reader.ReadGraph(ctx, versionID)
	if err != nil {
		return nil, err
	}
	return p.scenariosFromGraph(graph), nil
}

// DesignSnapshot is the P6 one-read fast path: scenarios and cases are derived
// from the same design graph snapshot, so the chain service issues one design
// read per request instead of two.
func (p *DesignProvider) DesignSnapshot(ctx context.Context, versionID int64) ([]chain.Node, []chain.Node, error) {
	graph, err := p.reader.ReadGraph(ctx, versionID)
	if err != nil {
		return nil, nil, err
	}
	return p.scenariosFromGraph(graph), p.casesFromGraph(graph), nil
}

func (p *DesignProvider) scenariosFromGraph(graph designapp.GraphView) []chain.Node {
	nodes := make([]chain.Node, 0)
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			if scenario.ID <= 0 {
				continue
			}
			requirementID := requirement.RequirementID
			meta := map[string]any{
				"requirement_version_id": scenario.RequirementVersionID,
				"sort_order":             scenario.SortOrder,
				"case_count":             len(scenario.Cases),
			}
			if requirementID > 0 {
				meta["requirement_id"] = strconv.FormatInt(requirementID, 10)
			}
			// v0.2.4-B (T9 minimal adaptation; the chain re-key itself is T13):
			// a scenario no longer carries version_sut/page/api ids, so the node
			// has no SUTIDs and no page list to attach. Nothing replaces them --
			// a scenario is a pure grouping node now.
			node := chain.Node{
				ID:            chain.NodeID(chain.NodeScenario, scenario.ID),
				Kind:          chain.NodeScenario,
				RefID:         strconv.FormatInt(scenario.ID, 10),
				Label:         scenario.Name,
				RequirementID: optionalInt64Ptr(requirementID),
				Meta:          meta,
			}
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// Cases returns the current case version of every case in the version design
// graph. A case node carries its scenario/requirement endpoints so the builder
// can induce has_case edges.
func (p *DesignProvider) Cases(ctx context.Context, versionID int64) ([]chain.Node, error) {
	graph, err := p.reader.ReadGraph(ctx, versionID)
	if err != nil {
		return nil, err
	}
	return p.casesFromGraph(graph), nil
}

func (p *DesignProvider) casesFromGraph(graph designapp.GraphView) []chain.Node {
	nodes := make([]chain.Node, 0)
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			for _, card := range scenario.Cases {
				if card.CurrentVersionID <= 0 {
					continue
				}
				caseVersionID := card.CurrentVersionID
				meta := map[string]any{
					"revision":        card.Revision,
					"priority":        card.Priority,
					"step_count":      card.StepCount,
					"assertion_count": card.AssertionCount,
					"stale":           caseVersionStale(card.Status),
				}
				if scenario.ID > 0 {
					meta["scenario_id"] = strconv.FormatInt(scenario.ID, 10)
				}
				// v0.2.4-B: the case card no longer carries version_sut/page/api
				// ids, so node.SUTIDs/SUTID/PageID stay unset. The natural-language
				// contract that replaced them is projected onto the typed
				// SutDescription/CoveredACs fields (T27 promoted them out of the
				// untyped Meta map), so the chain view can still say what a case
				// tests without parsing Meta.
				node := chain.Node{
					ID:             chain.NodeID(chain.NodeCaseVersion, caseVersionID),
					Kind:           chain.NodeCaseVersion,
					RefID:          strconv.FormatInt(caseVersionID, 10),
					Label:          card.Title,
					Status:         card.Status,
					ScenarioID:     optionalInt64Ptr(scenario.ID),
					RequirementID:  optionalInt64Ptr(requirement.RequirementID),
					CaseVersionID:  int64Ptr(caseVersionID),
					CreatedAt:      card.CreatedAt,
					UpdatedAt:      card.CreatedAt,
					SutDescription: card.SutDescription,
					CoveredACs:     append([]string(nil), card.CoveredACs...),
					Meta:           meta,
				}
				nodes = append(nodes, node)
			}
		}
	}
	return nodes
}

// caseVersionStale marks the design statuses that let a chain reader warn that
// the case version is stale/superseded before opening it.
func caseVersionStale(status string) bool {
	return status == design.CaseStatusStale || status == design.CaseStatusSuperseded
}

// CaseVersionsByIDs is the historical reader. It returns any status (including
// superseded) for IDs that belong to the requested version; unknown or foreign
// IDs are skipped by the design application service.
func (p *DesignProvider) CaseVersionsByIDs(ctx context.Context, versionID int64, ids []int64) ([]chain.Node, error) {
	rows, err := p.reader.ReadCaseVersionsByIDs(ctx, versionID, ids)
	if err != nil {
		return nil, err
	}
	nodes := make([]chain.Node, 0, len(rows))
	for _, row := range rows {
		if row.ID <= 0 {
			continue
		}
		caseVersionID := row.ID
		nodes = append(nodes, chain.Node{
			ID:            chain.NodeID(chain.NodeCaseVersion, caseVersionID),
			Kind:          chain.NodeCaseVersion,
			RefID:         strconv.FormatInt(caseVersionID, 10),
			Label:         row.Title,
			Status:        row.Status,
			CaseVersionID: int64Ptr(caseVersionID),
			Meta: map[string]any{
				"revision":   row.Revision,
				"historical": true,
			},
		})
	}
	return nodes, nil
}

// attachPageIDs / singlePageID were removed with the design case card's page_ids
// (v0.2.4-B, T9): a design case no longer names catalog pages, so there is no
// page list to mirror into chain.Node.PageID or into node meta. The other
// providers (delivery/execution/version) never used them.
