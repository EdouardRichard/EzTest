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

// ExecutionAssertion is one frozen assertion of an execution case snapshot. ID
// is the mindmap assertion id; StepSeq is the sequence of the owning step when
// the assertion is attached to one, otherwise nil (spec §5.2 step_seq).
type ExecutionAssertion struct {
	ID         int64
	StepSeq    *int
	Kind       string
	Target     string
	Expression string
	Expected   string
}

// ExecutionCase is the immutable per-case projection of a frozen baseline
// consumed by the execution context (spec §4.2 MindmapPort.SnapshotCases,
// §5.2). TestData is the raw JSONB document of the case; a nil document stays
// nil. Steps are ordered by seq and assertions by id so the snapshot is
// deterministic.
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
}
