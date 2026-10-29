package domain

import (
	"encoding/hex"
	"errors"
	"testing"
)

func int64Ptr(v int64) *int64 { return &v }

func testNode(id int64, parentID *int64, kind string) CaseNode {
	return CaseNode{
		ID:          id,
		VersionID:   7,
		ParentID:    parentID,
		Kind:        kind,
		Title:       "node",
		Description: "",
		Priority:    PriorityP2,
		Source:      NodeSourceUserInput,
		Status:      NodeStatusDraft,
		SortOrder:   int(id),
	}
}

func testStep(id, nodeID int64, seq int) CaseStep {
	return CaseStep{
		ID:       id,
		NodeID:   nodeID,
		Seq:      seq,
		Action:   "action",
		Expected: "expected",
		Source:   NodeSourceUserInput,
	}
}

func testAssertion(id, nodeID int64, stepID *int64) CaseAssertion {
	return CaseAssertion{
		ID:         id,
		NodeID:     nodeID,
		StepID:     stepID,
		Kind:       AssertionKindUI,
		Target:     "/target",
		Expression: "expression",
		Expected:   "expected",
		Source:     NodeSourceUserInput,
	}
}

// validGraph returns a valid two-level tree: one scenario with two cases, every
// case with contiguous steps and an assertion.
func validGraph() ([]CaseNode, map[int64][]CaseStep, map[int64][]CaseAssertion) {
	scenario := testNode(1, nil, NodeKindScenario)
	caseA := testNode(2, int64Ptr(1), NodeKindCase)
	caseB := testNode(3, int64Ptr(1), NodeKindCase)
	nodes := []CaseNode{scenario, caseA, caseB}
	steps := map[int64][]CaseStep{
		2: {testStep(10, 2, 1), testStep(11, 2, 2)},
		3: {testStep(12, 3, 1)},
	}
	assertions := map[int64][]CaseAssertion{
		2: {testAssertion(20, 2, int64Ptr(10))},
		3: {testAssertion(21, 3, nil)},
	}
	return nodes, steps, assertions
}

func requireInvalidCaseTree(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("ValidateGraph returned nil, want invalid_case_tree error")
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("ValidateGraph error type = %T, want *Error", err)
	}
	if domainErr.Kind != KindInvalid {
		t.Errorf("Error.Kind = %q, want %q", domainErr.Kind, KindInvalid)
	}
	if domainErr.Reason != "invalid_case_tree" {
		t.Errorf("Error.Reason = %q, want invalid_case_tree", domainErr.Reason)
	}
}

func TestValidateGraphAcceptsValidTree(t *testing.T) {
	nodes, steps, assertions := validGraph()
	if err := ValidateGraph(nodes, steps, assertions); err != nil {
		t.Fatalf("ValidateGraph returned %v, want nil", err)
	}
}

func TestValidateGraphRejectsCaseWithChildren(t *testing.T) {
	nodes, steps, assertions := validGraph()
	// A case at any depth must be a leaf: give the first case a valid child.
	child := testNode(4, int64Ptr(2), NodeKindCase)
	nodes = append(nodes, child)
	steps[4] = []CaseStep{testStep(13, 4, 1)}
	assertions[4] = []CaseAssertion{testAssertion(22, 4, nil)}

	requireInvalidCaseTree(t, ValidateGraph(nodes, steps, assertions))
}

func TestValidateGraphRejectsNonContiguousSteps(t *testing.T) {
	nodes, steps, assertions := validGraph()
	// A gap (1, 3) breaks the 1..N contract.
	steps[2] = []CaseStep{testStep(10, 2, 1), testStep(11, 2, 3)}

	requireInvalidCaseTree(t, ValidateGraph(nodes, steps, assertions))
}

func TestValidateGraphRequiresStepsAndAssertions(t *testing.T) {
	t.Run("case without steps", func(t *testing.T) {
		nodes, steps, assertions := validGraph()
		delete(steps, 2)
		requireInvalidCaseTree(t, ValidateGraph(nodes, steps, assertions))
	})
	t.Run("case without assertions", func(t *testing.T) {
		nodes, steps, assertions := validGraph()
		delete(assertions, 2)
		requireInvalidCaseTree(t, ValidateGraph(nodes, steps, assertions))
	})
}

func TestValidateGraphRejectsScenarioWithSteps(t *testing.T) {
	t.Run("steps", func(t *testing.T) {
		nodes, steps, assertions := validGraph()
		steps[1] = []CaseStep{testStep(13, 1, 1)}
		requireInvalidCaseTree(t, ValidateGraph(nodes, steps, assertions))
	})
	t.Run("assertions", func(t *testing.T) {
		nodes, steps, assertions := validGraph()
		assertions[1] = []CaseAssertion{testAssertion(22, 1, nil)}
		requireInvalidCaseTree(t, ValidateGraph(nodes, steps, assertions))
	})
}

func TestValidateGraphAcceptsEmptyChildMapsForScenarios(t *testing.T) {
	nodes, steps, assertions := validGraph()
	// Graph assembly may materialize a map entry for every node; an empty entry
	// for a scenario carries no steps/assertions and must be accepted.
	steps[1] = nil
	assertions[1] = []CaseAssertion{}
	if err := ValidateGraph(nodes, steps, assertions); err != nil {
		t.Fatalf("ValidateGraph returned %v, want nil", err)
	}
}

func TestValidateGraphRejectsStepAssertionFromOtherCase(t *testing.T) {
	nodes, steps, assertions := validGraph()
	// The assertion belongs to case 3 but points at case 2's step 10.
	assertions[3] = []CaseAssertion{testAssertion(22, 3, int64Ptr(10))}

	requireInvalidCaseTree(t, ValidateGraph(nodes, steps, assertions))
}

func TestCanonicalGraphHashStableAcrossInputOrder(t *testing.T) {
	nodes, steps, assertions := validGraph()
	sources := []CaseSource{
		{ID: 30, VersionID: 7, NodeID: 2, RefKind: RefRequirement, RefID: int64Ptr(100), Source: NodeSourceUserInput, Confidence: ConfidenceHigh},
		{ID: 31, VersionID: 7, NodeID: 3, RefKind: RefRequirement, RefID: int64Ptr(200), Source: NodeSourceUserInput, Confidence: ConfidenceHigh},
	}
	original := GraphSnapshot{VersionID: 7, Nodes: nodes, Steps: steps, Assertions: assertions, Sources: sources}

	// Same content, every slice reversed and the step slices swapped in order.
	reversedNodes := make([]CaseNode, len(nodes))
	for i := range nodes {
		reversedNodes[len(nodes)-1-i] = nodes[i]
	}
	reversedSteps := map[int64][]CaseStep{
		2: {steps[2][1], steps[2][0]},
		3: {steps[3][0]},
	}
	reversedAssertions := map[int64][]CaseAssertion{
		2: {assertions[2][0]},
		3: {assertions[3][0]},
	}
	reversedSources := []CaseSource{sources[1], sources[0]}
	permuted := GraphSnapshot{VersionID: 7, Nodes: reversedNodes, Steps: reversedSteps, Assertions: reversedAssertions, Sources: reversedSources}

	first := CanonicalGraphHash(original)
	second := CanonicalGraphHash(permuted)
	if first != second {
		t.Fatalf("hash changed with input order: %s != %s", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("hash length = %d, want 64 hex characters", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("hash %q is not hex: %v", first, err)
	}
}

func TestCanonicalGraphHashChangesWithContent(t *testing.T) {
	nodes, steps, assertions := validGraph()
	snapshot := GraphSnapshot{VersionID: 7, Nodes: nodes, Steps: steps, Assertions: assertions}
	before := CanonicalGraphHash(snapshot)

	nodes[1].Title = "changed"
	afterTitle := CanonicalGraphHash(snapshot)
	if before == afterTitle {
		t.Fatal("hash did not change when the node title changed")
	}

	nodes[1].Title = "node"
	steps[2][0].Action = "changed action"
	afterStep := CanonicalGraphHash(snapshot)
	if before == afterStep {
		t.Fatal("hash did not change when a step action changed")
	}
}
