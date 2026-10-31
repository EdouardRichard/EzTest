package providers

import (
	"context"
	"testing"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

type stubDesignReader struct {
	graph designapp.GraphView
	rows  []designapp.CaseVersionHistoryView
	err   error
}

func (s *stubDesignReader) ReadGraph(context.Context, int64) (designapp.GraphView, error) {
	if s.err != nil {
		return designapp.GraphView{}, s.err
	}
	return s.graph, nil
}

func (s *stubDesignReader) ReadCaseVersionsByIDs(context.Context, int64, []int64) ([]designapp.CaseVersionHistoryView, error) {
	return s.rows, s.err
}

func TestDesignProviderScenariosHaveNonZeroCreatedAt(t *testing.T) {
	graphAt := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	reader := &stubDesignReader{graph: designapp.GraphView{
		VersionID:   42,
		GeneratedAt: graphAt,
		Requirements: []designapp.RequirementGraphView{{
			RequirementID: 1,
			Scenarios: []designapp.ScenarioGraphView{
				{
					ID:      10,
					Name:    "With a case",
					Cases:   []designapp.CaseCardView{{CaseID: 100, CurrentVersionID: 101, CreatedAt: graphAt.Add(-30 * time.Minute)}},
					PageIDs: []int64{50},
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
		if node.CreatedAt.IsZero() {
			t.Errorf("scenario %s created_at is zero", node.ID)
			continue
		}
		if !node.CreatedAt.Equal(graphAt) {
			t.Errorf("scenario %s created_at = %v, want the graph snapshot time %v", node.ID, node.CreatedAt, graphAt)
		}
		if !node.UpdatedAt.Equal(node.CreatedAt) {
			t.Errorf("scenario %s updated_at = %v, want %v", node.ID, node.UpdatedAt, node.CreatedAt)
		}
	}
}
