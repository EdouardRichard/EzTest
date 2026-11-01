package providers

import (
	"context"
	"strconv"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
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
			node := chain.Node{
				ID:            chain.NodeID(chain.NodeScenario, scenario.ID),
				Kind:          chain.NodeScenario,
				RefID:         strconv.FormatInt(scenario.ID, 10),
				Label:         scenario.Name,
				RequirementID: optionalInt64Ptr(requirementID),
				VersionSUTIDs: sortUniqueIDs(scenario.VersionSUTIDs),
				Meta:          meta,
			}
			attachPageIDs(&node, scenario.PageIDs)
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
				node := chain.Node{
					ID:            chain.NodeID(chain.NodeCaseVersion, caseVersionID),
					Kind:          chain.NodeCaseVersion,
					RefID:         strconv.FormatInt(caseVersionID, 10),
					Label:         card.Title,
					Status:        card.Status,
					ScenarioID:    optionalInt64Ptr(scenario.ID),
					RequirementID: optionalInt64Ptr(requirement.RequirementID),
					CaseVersionID: int64Ptr(caseVersionID),
					PageID:        singlePageID(card.PageIDs),
					VersionSUTIDs: sortUniqueIDs(card.VersionSUTIDs),
					CreatedAt:     card.CreatedAt,
					UpdatedAt:     card.CreatedAt,
					Meta:          meta,
				}
				if card.DefaultVersionSUTID != nil && *card.DefaultVersionSUTID > 0 {
					node.VersionSUTID = int64Ptr(*card.DefaultVersionSUTID)
				}
				attachPageIDs(&node, card.PageIDs)
				nodes = append(nodes, node)
			}
		}
	}
	return nodes
}

// caseVersionStale marks the design statuses that let a chain reader warn that
// the case version is stale/superseded before opening it.
func caseVersionStale(status string) bool {
	return status == "stale" || status == "superseded"
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

// attachPageIDs stores the multi-page target list in node meta (the domain
// node only has one typed PageID) and mirrors a single page into the typed
// field so page filters match both representations.
func attachPageIDs(node *chain.Node, pageIDs []int64) {
	pages := sortUniqueIDs(pageIDs)
	if len(pages) == 0 {
		return
	}
	if node.Meta == nil {
		node.Meta = map[string]any{}
	}
	node.Meta["page_ids"] = pages
	if node.PageID == nil && len(pages) == 1 {
		node.PageID = int64Ptr(pages[0])
	}
}

// singlePageID returns the typed page id when the case targets exactly one
// page; multi-page targets stay in node meta.
func singlePageID(pageIDs []int64) *int64 {
	pages := sortUniqueIDs(pageIDs)
	if len(pages) != 1 {
		return nil
	}
	return int64Ptr(pages[0])
}
