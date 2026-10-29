package domain

import "encoding/json"

// EventCaseStatusChanged is the event published when execution marks a case
// node executed (spec §5.10). It lives in the mindmap context because the
// transition is owned here; the execution context only calls MarkExecuted.
const EventCaseStatusChanged = "case.status_changed"

// ExecutionBaseline is the frozen baseline projection consumed by the execution
// context (spec §4.2 MindmapPort.GetBaseline): the canonical graph hash, the
// optional code baseline commit and every baseline case node id in ascending
// order. Execution never mutates any of these values.
type ExecutionBaseline struct {
	GraphHash          string
	CodeBaselineCommit *string
	CaseIDs            []int64
}

// ExecutionStep is one frozen step of an execution case snapshot. Seq is the
// 1-based position of the step in the case (spec §5.2).
type ExecutionStep struct {
	Seq      int
	Action   string
	Expected string
	Data     string
}

// ExecutionVisual is the frozen comparison configuration of one kind=visual
// assertion inside an execution snapshot (design spec §9.3). BaselineEvidenceID
// is nil when no baseline was approved at snapshot time; the execution context
// then classifies the assertion inconclusive(case, missing_visual_baseline).
type ExecutionVisual struct {
	Selector           string
	Region             *VisualRegion
	Threshold          float64
	BaselineEvidenceID *int64
	ViewportWidth      int
	ViewportHeight     int
	DeviceScaleFactor  float64
}

// ExecutionAssertion is one frozen assertion of an execution case snapshot. ID
// is the mindmap assertion id; StepSeq is the sequence of the owning step when
// the assertion is attached to one, otherwise nil (spec §5.2 step_seq). Visual
// is set only for kind=visual assertions.
type ExecutionAssertion struct {
	ID         int64
	StepSeq    *int
	Kind       string
	Target     string
	Expression string
	Expected   string
	Visual     *ExecutionVisual
}

// Execution case origins consumed by the execution context's P7 batch gates
// (spec §5.2/§5.3): a case came either from the frozen baseline or from the AI
// supplement path. The value mirrors case_node.source, normalized to the
// execution-side origin set.
const (
	ExecutionOriginBaseline     = "baseline"
	ExecutionOriginAISupplement = "ai_supplement"
)

// ExecutionCase is the immutable per-case projection of a frozen baseline
// consumed by the execution context (spec §4.2 MindmapPort.SnapshotCases,
// §5.2). TestData is the raw JSONB document of the case; a nil document stays
// nil. Steps are ordered by seq and assertions by id so the snapshot is
// deterministic. Origin and Status are the P7 read-only gate inputs: Origin is
// the normalized baseline|ai_supplement provenance and Status the live
// case_node status.
type ExecutionCase struct {
	NodeID         int64
	Revision       int
	Title          string
	Priority       string
	Preconditions  string
	TestData       json.RawMessage
	Steps          []ExecutionStep
	Assertions     []ExecutionAssertion
	RequirementIDs []int64
	GraphHash      string
	Origin         string
	Status         string
}
