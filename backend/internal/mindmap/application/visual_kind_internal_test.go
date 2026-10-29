package application

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// TestValidateAssertionKindAcceptsVisual pins the P8 §9.3 assertion kind set: the
// graph write path and the materializer share this validator, so accepting
// visual here covers both.
func TestValidateAssertionKindAcceptsVisual(t *testing.T) {
	for _, kind := range []string{"ui", "api", "db", "text", "visual"} {
		if err := validateAssertionKind(kind); err != nil {
			t.Fatalf("validateAssertionKind(%q) = %v", kind, err)
		}
	}
	if err := validateAssertionKind("pixel"); err == nil {
		t.Fatal("validateAssertionKind accepted an unknown kind")
	}
}

func TestValidateAssertionFieldsAcceptsVisual(t *testing.T) {
	if err := validateAssertionFields(domain.AssertionKindVisual, "#chart", "the chart matches the baseline", ""); err != nil {
		t.Fatalf("visual assertion fields: %v", err)
	}
}

// TestBuildExecutionCaseProjectsVisual proves the frozen snapshot carries the
// comparison parameters, including the approved baseline id, so the execution
// context can classify without re-reading the mindmap.
func TestBuildExecutionCaseProjectsVisual(t *testing.T) {
	baseline := int64(501)
	node := domain.CaseNode{ID: 1, Revision: 2}
	state := graphState{
		nodes: []domain.CaseNode{node},
		steps: map[int64][]domain.CaseStep{},
		assertions: map[int64][]domain.CaseAssertion{
			1: {{
				ID: 5, NodeID: 1, Kind: domain.AssertionKindVisual, Expression: "matches",
				Visual: &domain.VisualAssertion{
					AssertionID: 5, Selector: "#chart",
					Region:    &domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3},
					Threshold: 0.02, BaselineEvidenceID: &baseline,
					ViewportWidth: 800, ViewportHeight: 600, DeviceScaleFactor: 2,
				},
			}},
		},
	}
	executionCase := buildExecutionCase(node, state, "hash")
	if len(executionCase.Assertions) != 1 {
		t.Fatalf("assertions = %+v", executionCase.Assertions)
	}
	visual := executionCase.Assertions[0].Visual
	if visual == nil {
		t.Fatal("visual projection is nil")
	}
	if visual.Selector != "#chart" || visual.Threshold != 0.02 ||
		visual.BaselineEvidenceID == nil || *visual.BaselineEvidenceID != 501 ||
		visual.ViewportWidth != 800 || visual.ViewportHeight != 600 || visual.DeviceScaleFactor != 2 {
		t.Fatalf("visual projection = %+v", visual)
	}
	if visual.Region == nil || *visual.Region != (domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3}) {
		t.Fatalf("visual region = %+v", visual.Region)
	}
	if executionVisual(nil) != nil {
		t.Fatal("non-visual assertion must not project visual parameters")
	}
}
