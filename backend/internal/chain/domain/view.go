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
// are optional; FocusNode is a full node ID. Limit overrides the node budget
// when set: zero means "unset" and lets the service apply the configured
// budget. Normalize should run before the filter is applied.
type ChainFilter struct {
	VersionSUTID *int64
	PageID       *int64
	WaveID       *int64
	Status       string
	Result       string
	FocusNode    string
	// Q is the P6 fuzzy title filter over requirement/scenario/case-version
	// labels (max 100 characters, enforced by the HTTP layer). The domain keeps
	// the trimmed value and matches it case-insensitively.
	Q string
	// AbnormalOnly keeps only abnormal case-run nodes
	// (failed/blocked/inconclusive/interrupted).
	AbnormalOnly bool
	// IssueStatus and IssueSeverity prune issue nodes only; they never remove
	// the rest of the graph.
	IssueStatus   string
	IssueSeverity string
	// RequirementLimit overrides chain.max_requirements for this request when
	// set (1..500); zero means "use the configured budget".
	RequirementLimit int
	// EventFrom/EventTo are inclusive channel-local event seqs used for the
	// approximate created_at replay. Zero means unset: Normalize drops zero and
	// negative values, so event_from=0&event_to=0 selects live mode rather than
	// an empty range. The application service rejects negative bounds.
	EventFrom *int64
	EventTo   *int64
	// Limit is the optional node-limit override. Normalize keeps values in
	// 1..1000 and resets every other value to 0 (unset/default), so an invalid
	// client value can never raise the service budget cap.
	Limit int
}

// maxChainFilterLimit is the hard cap of ChainFilter.Limit (spec §8.3).
const maxChainFilterLimit = 1000

// Normalize returns a copy of the filter with the canonical query semantics:
// non-positive IDs and event bounds are dropped, enum-ish text filters are
// trimmed and lowercased, focus_node is trimmed, an out-of-range limit becomes
// unset, and a reversed event interval is put in chronological order. The
// receiver is not modified.
func (f ChainFilter) Normalize() ChainFilter {
	f.VersionSUTID = positiveID(f.VersionSUTID)
	f.PageID = positiveID(f.PageID)
	f.WaveID = positiveID(f.WaveID)
	f.EventFrom = positiveID(f.EventFrom)
	f.EventTo = positiveID(f.EventTo)
	f.Status = strings.ToLower(strings.TrimSpace(f.Status))
	f.Result = strings.ToLower(strings.TrimSpace(f.Result))
	f.FocusNode = strings.TrimSpace(f.FocusNode)
	f.Q = strings.TrimSpace(f.Q)
	f.IssueStatus = strings.ToLower(strings.TrimSpace(f.IssueStatus))
	f.IssueSeverity = strings.ToLower(strings.TrimSpace(f.IssueSeverity))
	if f.RequirementLimit < 1 || f.RequirementLimit > MaxRequirementsLimit {
		f.RequirementLimit = 0
	}
	f.Limit = normalizeChainFilterLimit(f.Limit)
	if f.EventFrom != nil && f.EventTo != nil && *f.EventFrom > *f.EventTo {
		f.EventFrom, f.EventTo = f.EventTo, f.EventFrom
	}
	return f
}

func normalizeChainFilterLimit(limit int) int {
	if limit < 1 || limit > maxChainFilterLimit {
		// Zero means unset: the service applies the configured default budget.
		return 0
	}
	return limit
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
	// VersionSUTIDs lists every version SUT the node's case version uses, for
	// multi-SUT labels. VersionSUTID stays the single SUT that owns the failing
	// step/assertion (or the node's only target) when one exists.
	VersionSUTIDs []int64
	PageID        *int64
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

// TruncatedLayers marks which P6 node layers lost nodes to a budget cut. It is
// independent of the single Truncation.Reason because one node-budget cut can
// empty more than one layer.
type TruncatedLayers struct {
	Requirements bool
	Cases        bool
	CaseRuns     bool
	Issues       bool
	Reports      bool
}

// CaseRunStatusCounts is the P6 counts.by_case_run_status projection. Only the
// six terminal result statuses are tallied; pending/running/paused runs stay
// out of the buckets, mirroring the contract.
type CaseRunStatusCounts struct {
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
	Skipped      int
	Interrupted  int
}

// IssueSeverityCounts is the P6 counts.by_issue_severity projection.
type IssueSeverityCounts struct {
	Major   int
	Minor   int
	Trivial int
}

// IssueStatusCounts is the P6 counts.by_issue_status projection.
type IssueStatusCounts struct {
	Open      int
	Confirmed int
	Dismissed int
	Merged    int
}

// CoverageCounts is the P6 counts.coverage projection. Covered means the
// requirement had at least one case version in the filtered graph; uncovered
// is its complement.
type CoverageCounts struct {
	Requirements int
	Covered      int
	Uncovered    int
}

// Degraded provider layers of the chain read model (spec §4.3.6). The order is
// the canonical response order.
const (
	LayerRequirement = "requirement"
	LayerDesign      = "design"
	LayerExecution   = "execution"
	LayerDelivery    = "delivery"
	LayerCodeIntel   = "codeintel"
)

// DegradedLayerOrder is the contract order of degraded_layers.
var DegradedLayerOrder = []string{LayerRequirement, LayerDesign, LayerExecution, LayerDelivery, LayerCodeIntel}

// Replay event-limit defaults and hard bounds from chain.replay_event_limit
// (spec §4.5).
const (
	DefaultReplayEventLimit = 500
	MaxReplayEventLimit     = 2000
)

// ClampReplayEventLimit bounds the configured replay event limit.
func ClampReplayEventLimit(limit int) int {
	if limit < 1 || limit > MaxReplayEventLimit {
		return DefaultReplayEventLimit
	}
	return limit
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
	// EventLimit is the chain.replay_event_limit consumed by this request;
	// Truncated is true when the requested event range holds more events.
	EventLimit int
	Truncated  bool
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
	// P6 additions. Counts stays the legacy scalar map for P4 clients; the
	// typed projections below are serialized next to it.
	CaseRunStatusCounts CaseRunStatusCounts
	IssueSeverityCounts IssueSeverityCounts
	IssueStatusCounts   IssueStatusCounts
	Coverage            CoverageCounts
	DegradedLayers      []string
	TruncatedLayers     TruncatedLayers
}
