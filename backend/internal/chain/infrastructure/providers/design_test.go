package providers

import (
	"context"
	"testing"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

type stubDesignReader struct {
	graph      designapp.GraphView
	rows       []designapp.CaseVersionHistoryView
	err        error
	graphReads int
}

func (s *stubDesignReader) ReadGraph(context.Context, int64) (designapp.GraphView, error) {
	s.graphReads++
	if s.err != nil {
		return designapp.GraphView{}, s.err
	}
	return s.graph, nil
}

func (s *stubDesignReader) ReadCaseVersionsByIDs(context.Context, int64, []int64) ([]designapp.CaseVersionHistoryView, error) {
	return s.rows, s.err
}

// TestDesignProviderProjectsTypedCaseContract pins U8: the design provider
// projects the case card's sut_description / covered_acs onto the typed node
// fields, not the untyped Meta map (constitution 五.1).
func TestDesignProviderProjectsTypedCaseContract(t *testing.T) {
	graphAt := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	reader := &stubDesignReader{graph: designapp.GraphView{
		VersionID:   42,
		GeneratedAt: graphAt,
		Requirements: []designapp.RequirementGraphView{{
			RequirementID: 1,
			Scenarios: []designapp.ScenarioGraphView{{
				ID:   10,
				Name: "Checkout",
				Cases: []designapp.CaseCardView{{
					CaseID:           100,
					CurrentVersionID: 101,
					CreatedAt:        graphAt,
					SutDescription:   "The web storefront checkout.",
					CoveredACs:       []string{"AC-1", "AC-2"},
				}},
			}},
		}},
	}}
	provider := NewDesignProvider(reader)

	nodes, err := provider.Cases(context.Background(), 42)
	if err != nil {
		t.Fatalf("Cases: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %+v, want 1 case version", nodes)
	}
	node := nodes[0]
	if node.SutDescription != "The web storefront checkout." {
		t.Errorf("SutDescription = %q, want the typed field populated", node.SutDescription)
	}
	if len(node.CoveredACs) != 2 || node.CoveredACs[0] != "AC-1" || node.CoveredACs[1] != "AC-2" {
		t.Errorf("CoveredACs = %v, want [AC-1 AC-2]", node.CoveredACs)
	}
	if _, found := node.Meta["sut_description"]; found {
		t.Error("sut_description still in untyped Meta; want a typed field")
	}
	if _, found := node.Meta["covered_acs"]; found {
		t.Error("covered_acs still in untyped Meta; want a typed field")
	}
}

func TestDesignProviderScenariosKeepZeroCreatedAt(t *testing.T) {
	graphAt := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	reader := &stubDesignReader{graph: designapp.GraphView{
		VersionID:   42,
		GeneratedAt: graphAt,
		Requirements: []designapp.RequirementGraphView{{
			RequirementID: 1,
			Scenarios: []designapp.ScenarioGraphView{
				{
					ID:    10,
					Name:  "With a case",
					Cases: []designapp.CaseCardView{{CaseID: 100, CurrentVersionID: 101, CreatedAt: graphAt.Add(-30 * time.Minute)}},
				},
				{ID: 11, Name: "Without a case"},
			},
		}},
	}}
	provider := NewDesignProvider(reader)

	nodes, err := provider.Scenarios(context.Background(), 42)
	if err != nil {
		t.Fatalf("Scenarios: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %+v, want 2", nodes)
	}
	for _, node := range nodes {
		// The graph view does not project scenario timestamps, and stamping it
		// with the request-time graph generated_at would make event-range replay
		// drop every scenario. Zero timestamps are kept by the replay filter.
		if !node.CreatedAt.IsZero() || !node.UpdatedAt.IsZero() {
			t.Errorf("scenario %s timestamps = %v/%v, want both zero", node.ID, node.CreatedAt, node.UpdatedAt)
		}
	}
}
