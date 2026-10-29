// Package mindmap adapts the P5 mindmap application services to the delivery
// report's ReportCaseGraphReader (spec §6.5): the report's requirement section
// needs the version requirements, their status and the case nodes each one
// covers. Delivery never reads the mindmap tables itself.
package mindmap

import (
	"context"
	"errors"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// graphRequirementPageLimit bounds the requirement read of one report. A
// version with more requirements than this is beyond the P7 product envelope;
// the page is a single read because the report service takes no cursor.
const graphRequirementPageLimit = 1000

// GraphReader is the mindmap graph slice the adapter needs.
// *mindmap/application.GraphService satisfies it.
type GraphReader interface {
	Get(ctx context.Context, versionID int64) (mindmapapp.GraphView, error)
}

// RequirementLister is the version requirement slice the adapter needs.
// *version/application.RequirementService satisfies it.
type RequirementLister interface {
	List(ctx context.Context, versionID int64, status string, page versiondomain.Page) ([]versionapp.Requirement, bool, error)
}

// ReportGraphReader implements delivery/application.ReportCaseGraphReader.
type ReportGraphReader struct {
	graphs       GraphReader
	requirements RequirementLister
}

// NewReportGraphReader wires the report graph reader.
func NewReportGraphReader(graphs GraphReader, requirements RequirementLister) *ReportGraphReader {
	return &ReportGraphReader{graphs: graphs, requirements: requirements}
}

var _ deliveryapp.ReportCaseGraphReader = (*ReportGraphReader)(nil)

// ListReportRequirements implements delivery/application.ReportCaseGraphReader.
// The coverage matrix of the graph view maps each requirement to the current
// working-graph case nodes it covers; requirements without a covered case are
// still returned with an empty case list.
func (r *ReportGraphReader) ListReportRequirements(ctx context.Context, versionID int64) ([]deliveryapp.ReportRequirementSource, error) {
	if r == nil || r.graphs == nil || r.requirements == nil {
		return nil, errors.New("delivery: report graph reader is not configured")
	}
	requirements, _, err := r.requirements.List(ctx, versionID, "",
		versiondomain.Page{Limit: graphRequirementPageLimit})
	if err != nil {
		return nil, err
	}
	graph, err := r.graphs.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	covered := make(map[int64][]int64, len(graph.Coverage.Matrix))
	for _, cell := range graph.Coverage.Matrix {
		if cell.RequirementID <= 0 {
			continue
		}
		covered[cell.RequirementID] = append([]int64(nil), cell.CaseIDs...)
	}
	sources := make([]deliveryapp.ReportRequirementSource, 0, len(requirements))
	for _, requirement := range requirements {
		sources = append(sources, deliveryapp.ReportRequirementSource{
			RequirementID: requirement.ID,
			Title:         requirement.Title,
			Status:        requirement.Status,
			CaseNodeIDs:   covered[requirement.ID],
		})
	}
	return sources, nil
}
