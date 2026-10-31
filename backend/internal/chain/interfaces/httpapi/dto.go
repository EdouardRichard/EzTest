package httpapi

import (
	"strconv"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
)

// chainViewDTO is the ChainView response body of spec §4.2. Entity IDs are
// decimal strings at the API boundary; event-sequence bounds are integers.
type chainViewDTO struct {
	VersionID      string             `json:"version_id"`
	GeneratedAt    string             `json:"generated_at"`
	SnapshotCursor int64              `json:"snapshot_cursor"`
	Filters        chainFilterDTO     `json:"filters"`
	Counts         map[string]int     `json:"counts"`
	Nodes          []chainNodeDTO     `json:"nodes"`
	Edges          []chainEdgeDTO     `json:"edges"`
	Truncated      bool               `json:"truncated"`
	Truncation     chainTruncationDTO `json:"truncation"`
	Replay         chainReplayDTO     `json:"replay"`
}

// chainFilterDTO echoes the normalized filter. Unset values are null.
type chainFilterDTO struct {
	SUTID     *string `json:"sut_id"`
	PageID    *string `json:"page_id"`
	WaveID    *string `json:"wave_id"`
	Status    *string `json:"status"`
	Result    *string `json:"result"`
	EventFrom *int64  `json:"event_from"`
	EventTo   *int64  `json:"event_to"`
}

// chainNodeDTO is one chain node. Optional ids stay null when the node does not
// carry them.
type chainNodeDTO struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	RefID         string         `json:"ref_id"`
	Label         string         `json:"label"`
	Status        *string        `json:"status"`
	VersionSUTID  *string        `json:"version_sut_id"`
	RequirementID *string        `json:"requirement_id"`
	CaseVersionID *string        `json:"case_version_id"`
	WaveID        *string        `json:"wave_id"`
	CaseRunID     *string        `json:"case_run_id"`
	IssueID       *string        `json:"issue_id"`
	ReportID      *string        `json:"report_id"`
	CreatedAt     string         `json:"created_at"`
	Meta          map[string]any `json:"meta,omitempty"`
}

// chainEdgeDTO is one directed chain edge.
type chainEdgeDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// chainTruncationDTO reports the effective budget and truncation reason.
type chainTruncationDTO struct {
	MaxNodes        int    `json:"max_nodes"`
	MaxEdges        int    `json:"max_edges"`
	MaxRequirements int    `json:"max_requirements"`
	Reason          string `json:"reason"`
}

// chainReplayDTO is the replay envelope. Approximate is always true in P4: the
// event-range mode maps seq bounds to entity timestamps and never promises an
// arbitrary as-of snapshot.
type chainReplayDTO struct {
	Mode        string  `json:"mode"`
	WaveID      *string `json:"wave_id"`
	EventFrom   *int64  `json:"event_from"`
	EventTo     *int64  `json:"event_to"`
	Approximate bool    `json:"approximate"`
}

func toChainViewDTO(view chain.ChainView) chainViewDTO {
	nodes := make([]chainNodeDTO, 0, len(view.Nodes))
	for _, node := range view.Nodes {
		nodes = append(nodes, toChainNodeDTO(node))
	}
	edges := make([]chainEdgeDTO, 0, len(view.Edges))
	for _, edge := range view.Edges {
		edges = append(edges, chainEdgeDTO{From: edge.From, To: edge.To, Kind: string(edge.Kind)})
	}
	return chainViewDTO{
		VersionID:      view.VersionID,
		GeneratedAt:    formatChainTime(view.GeneratedAt),
		SnapshotCursor: view.SnapshotCursor,
		Filters:        toChainFilterDTO(view.Filters),
		Counts:         nonNilCounts(view.Counts),
		Nodes:          nodes,
		Edges:          edges,
		Truncated:      view.Truncated,
		Truncation: chainTruncationDTO{
			MaxNodes:        view.Truncation.MaxNodes,
			MaxEdges:        view.Truncation.MaxEdges,
			MaxRequirements: view.Truncation.MaxRequirements,
			Reason:          view.Truncation.Reason,
		},
		Replay: chainReplayDTO{
			Mode:        view.Replay.Mode,
			WaveID:      formatChainID(view.Replay.WaveID),
			EventFrom:   copyInt64(view.Replay.EventFrom),
			EventTo:     copyInt64(view.Replay.EventTo),
			Approximate: true,
		},
	}
}

func toChainFilterDTO(filter chain.ChainFilter) chainFilterDTO {
	return chainFilterDTO{
		SUTID:     formatChainID(filter.VersionSUTID),
		PageID:    formatChainID(filter.PageID),
		WaveID:    formatChainID(filter.WaveID),
		Status:    formatChainText(filter.Status),
		Result:    formatChainText(filter.Result),
		EventFrom: copyInt64(filter.EventFrom),
		EventTo:   copyInt64(filter.EventTo),
	}
}

func toChainNodeDTO(node chain.Node) chainNodeDTO {
	return chainNodeDTO{
		ID:            node.ID,
		Kind:          string(node.Kind),
		RefID:         node.RefID,
		Label:         node.Label,
		Status:        formatChainText(node.Status),
		VersionSUTID:  formatChainID(node.VersionSUTID),
		RequirementID: formatChainID(node.RequirementID),
		CaseVersionID: formatChainID(node.CaseVersionID),
		WaveID:        formatChainID(node.WaveID),
		CaseRunID:     formatChainID(node.CaseRunID),
		IssueID:       formatChainID(node.IssueID),
		ReportID:      formatChainID(node.ReportID),
		CreatedAt:     formatChainTime(node.CreatedAt),
		Meta:          node.Meta,
	}
}

// formatChainTime renders a node/view timestamp as RFC3339 UTC. A zero time is
// rendered as the zero RFC3339 instant because created_at is a required field
// of the contract and providers only leave it unset for entities without a
// readable timestamp.
func formatChainTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func formatChainID(value *int64) *string {
	if value == nil || *value <= 0 {
		return nil
	}
	formatted := strconv.FormatInt(*value, 10)
	return &formatted
}

func formatChainText(value string) *string {
	if value == "" {
		return nil
	}
	formatted := value
	return &formatted
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func nonNilCounts(counts map[string]int) map[string]int {
	if counts == nil {
		return map[string]int{}
	}
	return counts
}
