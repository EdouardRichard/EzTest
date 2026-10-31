// Package chain defines the read-only exploration-chain domain: the node/edge
// graph, its filters, budget and truncation metadata, and the replay envelope
// (spec §8). It never reads another context's tables; providers map application
// read models onto Node and Edge values.
package chain

import (
	"strconv"
	"strings"
	"time"
)

// NodeKind identifies one exploration-chain node type (spec §8.2). Steps,
// assertions and evidence are deliberately not nodes.
type NodeKind string

// Node kinds.
const (
	NodeRequirement NodeKind = "requirement"
	NodeScenario    NodeKind = "scenario"
	NodeCaseVersion NodeKind = "case_version"
	NodeWave        NodeKind = "wave"
	NodeCaseRun     NodeKind = "case_run"
	NodeIssue       NodeKind = "issue"
	NodeReport      NodeKind = "report"
	NodeClosure     NodeKind = "closure"
)

// EdgeKind identifies one exploration-chain edge type (spec §8.2).
type EdgeKind string

// Edge kinds.
const (
	EdgeHasScenario EdgeKind = "has_scenario"
	EdgeHasCase     EdgeKind = "has_case"
	EdgeExecutedBy  EdgeKind = "executed_by"
	EdgeContains    EdgeKind = "contains"
	EdgeRaised      EdgeKind = "raised"
	EdgeReports     EdgeKind = "reports"
	EdgeClosedBy    EdgeKind = "closed_by"
	EdgeIncludedIn  EdgeKind = "included_in"
	EdgeMergedInto  EdgeKind = "merged_into"
)

// NodeID formats the stable node identifier "{kind}:{ref_id}" of spec §8.2.
func NodeID(kind NodeKind, refID int64) string {
	return string(kind) + ":" + strconv.FormatInt(refID, 10)
}

// ChainFilter is the exploration-chain query filter (spec §8.3). Pointer fields
// are optional; FocusNode is a full node ID. Normalize should run before the
// filter is applied.
type ChainFilter struct {
	VersionSUTID *int64
	PageID       *int64
	WaveID       *int64
	Status       string
	Result       string
	FocusNode    string
	EventFrom    *int64
	EventTo      *int64
}

// Normalize returns a copy of the filter with the canonical query semantics:
// non-positive IDs and event bounds are dropped, enum-ish text filters are
// trimmed and lowercased, focus_node is trimmed, and a reversed event interval
// is put in chronological order. The receiver is not modified.
func (f ChainFilter) Normalize() ChainFilter {
	f.VersionSUTID = positiveID(f.VersionSUTID)
	f.PageID = positiveID(f.PageID)
	f.WaveID = positiveID(f.WaveID)
	f.EventFrom = positiveID(f.EventFrom)
	f.EventTo = positiveID(f.EventTo)
	f.Status = strings.ToLower(strings.TrimSpace(f.Status))
	f.Result = strings.ToLower(strings.TrimSpace(f.Result))
	f.FocusNode = strings.TrimSpace(f.FocusNode)
	if f.EventFrom != nil && f.EventTo != nil && *f.EventFrom > *f.EventTo {
		f.EventFrom, f.EventTo = f.EventTo, f.EventFrom
	}
	return f
}

func positiveID(id *int64) *int64 {
	if id == nil || *id <= 0 {
		return nil
	}
	value := *id
	return &value
}

// Node is one exploration-chain node (spec §8.2). ID is "{kind}:{ref_id}";
// RefID is the decimal identifier of the underlying entity. The typed link
// fields let the builder attach the induced edges without parsing Meta.
type Node struct {
	ID           string
	Kind         NodeKind
	RefID        string
	Label        string
	Status       string
	Result       string
	VersionSUTID *int64
	PageID       *int64
	// RequirementID, ScenarioID, CaseVersionID, WaveID, CaseRunID, IssueID,
	// ReportID and MergedIntoID are the typed edge endpoints of the node.
	RequirementID *int64
	ScenarioID    *int64
	CaseVersionID *int64
	WaveID        *int64
	CaseRunID     *int64
	IssueID       *int64
	ReportID      *int64
	MergedIntoID  *int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// Meta carries kind-specific details (counts, failure_class, close_round,
	// as_of, ...). It never carries evidence bytes.
	Meta map[string]any
}

// Edge is one directed exploration-chain edge (spec §8.2).
type Edge struct {
	From string
	To   string
	Kind EdgeKind
}

// Budget caps the exploration-chain read model (spec §4.5/§8.3).
type Budget struct {
	MaxNodes        int
	MaxEdges        int
	MaxRequirements int
}

// Budget defaults and hard bounds from settings keys chain.max_nodes,
// chain.max_edges and chain.max_requirements (spec §4.5).
const (
	DefaultMaxNodes        = 300
	DefaultMaxEdges        = 600
	DefaultMaxRequirements = 100

	MaxNodesLimit        = 1000
	MaxEdgesLimit        = 2000
	MaxRequirementsLimit = 500
)

// ClampBudget returns a budget whose fields are inside their hard bounds. A
// zero, negative or above-limit field falls back to its spec default; fields
// are clamped independently.
func ClampBudget(b Budget) Budget {
	return Budget{
		MaxNodes:        clampBudgetField(b.MaxNodes, DefaultMaxNodes, MaxNodesLimit),
		MaxEdges:        clampBudgetField(b.MaxEdges, DefaultMaxEdges, MaxEdgesLimit),
		MaxRequirements: clampBudgetField(b.MaxRequirements, DefaultMaxRequirements, MaxRequirementsLimit),
	}
}

func clampBudgetField(value, fallback, limit int) int {
	if value < 1 || value > limit {
		return fallback
	}
	return value
}

// Truncation reasons (spec §8.3).
const (
	TruncationReasonNodeBudget        = "node_budget"
	TruncationReasonEdgeBudget        = "edge_budget"
	TruncationReasonRequirementBudget = "requirement_budget"
)

// Truncation reports the effective budget and why the graph was cut. Reason is
// empty when nothing was truncated.
type Truncation struct {
	MaxNodes        int
	MaxEdges        int
	MaxRequirements int
	Reason          string
}

// Replay modes (spec §8.4). P4 replays live, one wave, or an approximate event
// range; it never promises an arbitrary as-of snapshot.
const (
	ReplayModeLive  = "live"
	ReplayModeWave  = "wave"
	ReplayModeRange = "range"
)

// ReplayInfo tells the UI which replay window was applied. Approximate is true
// for event-range replay because seq boundaries are mapped to entity
// created_at bounds.
type ReplayInfo struct {
	Mode        string
	WaveID      *int64
	EventFrom   *int64
	EventTo     *int64
	Approximate bool
}

// ChainView is the exploration-chain response body (spec §4.2). VersionID is a
// decimal string because IDs are strings at the API boundary; IDs inside Node
// are likewise strings.
type ChainView struct {
	VersionID      string
	GeneratedAt    time.Time
	SnapshotCursor int64
	Filters        ChainFilter
	Counts         map[string]int
	Nodes          []Node
	Edges          []Edge
	Truncated      bool
	Truncation     Truncation
	Replay         ReplayInfo
}
