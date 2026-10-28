package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxScenarioDepth is the number of scenario layers allowed above a leaf case:
// version -> scenario -> scenario -> case (spec §6.1 rule 1).
const maxScenarioDepth = 2

// GraphSnapshot is the complete in-memory mindmap of one version. Steps and
// assertions are keyed by their owning case node id. It is the input of both
// validation and canonical hashing.
type GraphSnapshot struct {
	VersionID  int64
	Nodes      []CaseNode
	Steps      map[int64][]CaseStep
	Assertions map[int64][]CaseAssertion
	Sources    []CaseSource
}

// ValidateGraph enforces the tree rules of spec §6.1: every case is a leaf with
// at least one step and one assertion, steps are numbered 1..N contiguously
// (N <= 30), scenarios never own steps/assertions, scenario nesting is at most
// two levels, and an assertion's step belongs to the same case. All failures are
// returned as *Error{Kind: "invalid", Reason: "invalid_case_tree"}.
func ValidateGraph(nodes []CaseNode, steps map[int64][]CaseStep, assertions map[int64][]CaseAssertion) error {
	byID := make(map[int64]CaseNode, len(nodes))
	for _, node := range nodes {
		if node.ID <= 0 {
			return invalidCaseTree("node id must be a positive id")
		}
		if _, duplicate := byID[node.ID]; duplicate {
			return invalidCaseTree(fmt.Sprintf("duplicate node id %d", node.ID))
		}
		switch node.Kind {
		case NodeKindScenario, NodeKindCase:
		default:
			return invalidCaseTree(fmt.Sprintf("node %d has unknown kind %q", node.ID, node.Kind))
		}
		byID[node.ID] = node
	}

	// Parent links: the parent must exist, a case is always a leaf, and the
	// scenario nesting depth is bounded.
	for _, node := range nodes {
		if node.ParentID == nil {
			continue
		}
		if *node.ParentID == node.ID {
			return invalidCaseTree(fmt.Sprintf("node %d is its own parent", node.ID))
		}
		parent, ok := byID[*node.ParentID]
		if !ok {
			return invalidCaseTree(fmt.Sprintf("node %d references missing parent %d", node.ID, *node.ParentID))
		}
		if parent.Kind == NodeKindCase {
			return invalidCaseTree(fmt.Sprintf("case node %d has a child %d", parent.ID, node.ID))
		}
	}

	// Walk the ancestor chain of every node to reject cycles and over-deep
	// scenario nesting.
	for _, node := range nodes {
		seen := map[int64]struct{}{}
		scenarioAncestors := 0
		current := node
		for current.ParentID != nil {
			if _, cycle := seen[current.ID]; cycle {
				return invalidCaseTree(fmt.Sprintf("cycle detected at node %d", current.ID))
			}
			seen[current.ID] = struct{}{}
			parent := byID[*current.ParentID]
			if parent.Kind == NodeKindScenario {
				scenarioAncestors++
			}
			if scenarioAncestors > maxScenarioDepth {
				return invalidCaseTree(fmt.Sprintf("node %d exceeds the maximum scenario nesting depth", node.ID))
			}
			current = parent
		}
		if node.Kind == NodeKindScenario && scenarioAncestors >= maxScenarioDepth {
			return invalidCaseTree(fmt.Sprintf("scenario %d exceeds the maximum scenario nesting depth", node.ID))
		}
	}

	// Steps only belong to case nodes and are contiguous per case.
	for nodeID, list := range steps {
		if len(list) == 0 {
			continue
		}
		node, ok := byID[nodeID]
		if !ok {
			return invalidCaseTree(fmt.Sprintf("steps reference missing node %d", nodeID))
		}
		if node.Kind != NodeKindCase {
			return invalidCaseTree(fmt.Sprintf("scenario %d must not own steps", nodeID))
		}
		seenSeq := make(map[int]struct{}, len(list))
		for _, step := range list {
			if step.NodeID != nodeID {
				return invalidCaseTree(fmt.Sprintf("step %d belongs to node %d, not %d", step.ID, step.NodeID, nodeID))
			}
			if step.Seq < 1 || step.Seq > MaxStepsPerCase {
				return invalidCaseTree(fmt.Sprintf("step %d has seq %d outside 1..%d", step.ID, step.Seq, MaxStepsPerCase))
			}
			if _, duplicate := seenSeq[step.Seq]; duplicate {
				return invalidCaseTree(fmt.Sprintf("node %d has duplicate step seq %d", nodeID, step.Seq))
			}
			seenSeq[step.Seq] = struct{}{}
			if strings.TrimSpace(step.Action) == "" {
				return invalidCaseTree(fmt.Sprintf("step %d has an empty action", step.ID))
			}
		}
	}

	// Assertions only belong to case nodes; their optional step must be part of
	// the same case.
	for nodeID, list := range assertions {
		if len(list) == 0 {
			continue
		}
		node, ok := byID[nodeID]
		if !ok {
			return invalidCaseTree(fmt.Sprintf("assertions reference missing node %d", nodeID))
		}
		if node.Kind != NodeKindCase {
			return invalidCaseTree(fmt.Sprintf("scenario %d must not own assertions", nodeID))
		}
		boolStepIDs := make(map[int64]struct{}, len(steps[nodeID]))
		for _, step := range steps[nodeID] {
			boolStepIDs[step.ID] = struct{}{}
		}
		for _, assertion := range list {
			if assertion.NodeID != nodeID {
				return invalidCaseTree(fmt.Sprintf("assertion %d belongs to node %d, not %d", assertion.ID, assertion.NodeID, nodeID))
			}
			if assertion.StepID != nil {
				if _, ok := boolStepIDs[*assertion.StepID]; !ok {
					return invalidCaseTree(fmt.Sprintf("assertion %d references step %d of another case", assertion.ID, *assertion.StepID))
				}
			}
			if strings.TrimSpace(assertion.Expression) == "" {
				return invalidCaseTree(fmt.Sprintf("assertion %d has an empty expression", assertion.ID))
			}
		}
	}

	// Every case has at least one step and one assertion, and step seqs are
	// exactly 1..N.
	for _, node := range nodes {
		if node.Kind != NodeKindCase {
			continue
		}
		stepsOfNode := steps[node.ID]
		if len(stepsOfNode) == 0 {
			return invalidCaseTree(fmt.Sprintf("case %d has no steps", node.ID))
		}
		if len(stepsOfNode) > MaxStepsPerCase {
			return invalidCaseTree(fmt.Sprintf("case %d has more than %d steps", node.ID, MaxStepsPerCase))
		}
		stepSeqs := make(map[int]struct{}, len(stepsOfNode))
		for _, step := range stepsOfNode {
			stepSeqs[step.Seq] = struct{}{}
		}
		for seq := 1; seq <= len(stepsOfNode); seq++ {
			if _, ok := stepSeqs[seq]; !ok {
				return invalidCaseTree(fmt.Sprintf("case %d has a gap in step sequence at %d", node.ID, seq))
			}
		}
		if len(assertions[node.ID]) == 0 {
			return invalidCaseTree(fmt.Sprintf("case %d has no assertions", node.ID))
		}
	}
	return nil
}

// invalidCaseTree returns the stable tree validation error of spec §6.1.
func invalidCaseTree(message string) error {
	return NewInvalid("graph", "invalid_case_tree", message)
}

// canonicalNode is the fixed-field representation hashed by CanonicalGraphHash.
// Volatile metadata (timestamps, revision, actors) is intentionally excluded so
// the hash describes the graph content only.
type canonicalNode struct {
	ID            int64                `json:"id"`
	ParentID      *int64               `json:"parentId"`
	Kind          string               `json:"kind"`
	Title         string               `json:"title"`
	Description   string               `json:"description"`
	Priority      string               `json:"priority"`
	Preconditions string               `json:"preconditions"`
	TestData      any                  `json:"testData"`
	Source        string               `json:"source"`
	SortOrder     int                  `json:"sortOrder"`
	Steps         []canonicalStep      `json:"steps"`
	Assertions    []canonicalAssertion `json:"assertions"`
}

type canonicalStep struct {
	ID       int64  `json:"id"`
	NodeID   int64  `json:"nodeId"`
	Seq      int    `json:"seq"`
	Action   string `json:"action"`
	Expected string `json:"expected"`
	Data     string `json:"data"`
	Source   string `json:"source"`
}

type canonicalAssertion struct {
	ID         int64  `json:"id"`
	NodeID     int64  `json:"nodeId"`
	StepID     *int64 `json:"stepId"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Expression string `json:"expression"`
	Expected   string `json:"expected"`
	Source     string `json:"source"`
}

type canonicalSource struct {
	ID         int64  `json:"id"`
	NodeID     int64  `json:"nodeId"`
	RefKind    string `json:"refKind"`
	RefID      *int64 `json:"refId"`
	RefKey     string `json:"refKey"`
	Source     string `json:"source"`
	Confidence string `json:"confidence"`
}

type canonicalGraph struct {
	VersionID int64             `json:"versionId"`
	Nodes     []canonicalNode   `json:"nodes"`
	Sources   []canonicalSource `json:"sources"`
}

// CanonicalGraphHash returns the lowercase hex sha256 of the canonical graph
// JSON (spec §6.4): nodes are ordered by sort_order then id, steps by seq then
// id, assertions by step then id, and sources by node then reference. Field
// order is fixed by the canonical structs.
func CanonicalGraphHash(g GraphSnapshot) string {
	nodes := append([]CaseNode(nil), g.Nodes...)
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].SortOrder != nodes[j].SortOrder {
			return nodes[i].SortOrder < nodes[j].SortOrder
		}
		return nodes[i].ID < nodes[j].ID
	})

	sources := append([]CaseSource(nil), g.Sources...)
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].NodeID != sources[j].NodeID {
			return sources[i].NodeID < sources[j].NodeID
		}
		if sources[i].RefKind != sources[j].RefKind {
			return sources[i].RefKind < sources[j].RefKind
		}
		if refIDValue(sources[i].RefID) != refIDValue(sources[j].RefID) {
			return refIDValue(sources[i].RefID) < refIDValue(sources[j].RefID)
		}
		if sources[i].RefKey != sources[j].RefKey {
			return sources[i].RefKey < sources[j].RefKey
		}
		return sources[i].ID < sources[j].ID
	})

	canonical := canonicalGraph{
		VersionID: g.VersionID,
		Nodes:     make([]canonicalNode, 0, len(nodes)),
		Sources:   make([]canonicalSource, 0, len(sources)),
	}
	for _, node := range nodes {
		steps := append([]CaseStep(nil), g.Steps[node.ID]...)
		sort.Slice(steps, func(i, j int) bool {
			if steps[i].Seq != steps[j].Seq {
				return steps[i].Seq < steps[j].Seq
			}
			return steps[i].ID < steps[j].ID
		})
		canonicalSteps := make([]canonicalStep, 0, len(steps))
		for _, step := range steps {
			canonicalSteps = append(canonicalSteps, canonicalStep{
				ID:       step.ID,
				NodeID:   step.NodeID,
				Seq:      step.Seq,
				Action:   step.Action,
				Expected: step.Expected,
				Data:     step.Data,
				Source:   step.Source,
			})
		}

		assertions := append([]CaseAssertion(nil), g.Assertions[node.ID]...)
		sort.Slice(assertions, func(i, j int) bool {
			left, right := refIDValue(assertions[i].StepID), refIDValue(assertions[j].StepID)
			if left != right {
				return left < right
			}
			return assertions[i].ID < assertions[j].ID
		})
		canonicalAssertions := make([]canonicalAssertion, 0, len(assertions))
		for _, assertion := range assertions {
			canonicalAssertions = append(canonicalAssertions, canonicalAssertion{
				ID:         assertion.ID,
				NodeID:     assertion.NodeID,
				StepID:     assertion.StepID,
				Kind:       assertion.Kind,
				Target:     assertion.Target,
				Expression: assertion.Expression,
				Expected:   assertion.Expected,
				Source:     assertion.Source,
			})
		}

		canonical.Nodes = append(canonical.Nodes, canonicalNode{
			ID:            node.ID,
			ParentID:      node.ParentID,
			Kind:          node.Kind,
			Title:         node.Title,
			Description:   node.Description,
			Priority:      node.Priority,
			Preconditions: node.Preconditions,
			TestData:      canonicalJSONValue(node.TestData),
			Source:        node.Source,
			SortOrder:     node.SortOrder,
			Steps:         canonicalSteps,
			Assertions:    canonicalAssertions,
		})
	}
	for _, source := range sources {
		canonical.Sources = append(canonical.Sources, canonicalSource{
			ID:         source.ID,
			NodeID:     source.NodeID,
			RefKind:    source.RefKind,
			RefID:      source.RefID,
			RefKey:     source.RefKey,
			Source:     source.Source,
			Confidence: source.Confidence,
		})
	}

	data, err := json.Marshal(canonical)
	if err != nil {
		// Every field is a plain Go value or a JSON value normalized below, so
		// this is unreachable; return the empty hash rather than panic.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// canonicalJSONValue normalizes a JSONB column for hashing: absent values become
// null and object keys are re-marshaled deterministically by encoding/json.
func canonicalJSONValue(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

// refIDValue maps a nil reference id to 0 for ordering purposes only.
func refIDValue(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}
