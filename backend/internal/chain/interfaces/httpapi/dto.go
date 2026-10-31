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
		Meta:          sanitizeChainMeta(node.Meta),
	}
}

// chainMetaIDArrayKeys are the node meta keys that carry entity ID arrays.
// Meta is an open JSON object, but the public API contract of P4 is that P3+
// entity IDs are decimal strings; the DTO boundary converts these known arrays
// (and the ref_id fields of "refs") so the chain response never leaks JSON
// numbers for snowflake IDs.
var chainMetaIDArrayKeys = map[string]bool{
	"page_ids":     true,
	"issue_ids":    true,
	"case_run_ids": true,
}

// sanitizeChainMeta deep-copies the node meta and converts the documented ID
// arrays to decimal strings. Other values are preserved as-is (counts, labels,
// timestamps and maps are not entity IDs).
func sanitizeChainMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		switch {
		case chainMetaIDArrayKeys[key]:
			out[key] = decimalStringArray(value)
		case key == "refs":
			out[key] = sanitizeChainMetaRefs(value)
		default:
			out[key] = value
		}
	}
	return out
}

func decimalStringArray(value any) any {
	switch values := value.(type) {
	case []int64:
		out := make([]string, 0, len(values))
		for _, item := range values {
			out = append(out, strconv.FormatInt(item, 10))
		}
		return out
	case []int:
		out := make([]string, 0, len(values))
		for _, item := range values {
			out = append(out, strconv.Itoa(item))
		}
		return out
	case []int32:
		out := make([]string, 0, len(values))
		for _, item := range values {
			out = append(out, strconv.FormatInt(int64(item), 10))
		}
		return out
	case []float64:
		out := make([]string, 0, len(values))
		for _, item := range values {
			out = append(out, strconv.FormatFloat(item, 'f', -1, 64))
		}
		return out
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]any, 0, len(values))
		for _, item := range values {
			if formatted, ok := decimalStringValue(item); ok {
				out = append(out, formatted)
			} else {
				out = append(out, item)
			}
		}
		return out
	default:
		return value
	}
}

func sanitizeChainMetaRefs(value any) any {
	switch refs := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(refs))
		for _, ref := range refs {
			out = append(out, sanitizeChainMetaRef(ref))
		}
		return out
	case []any:
		out := make([]any, 0, len(refs))
		for _, item := range refs {
			ref, ok := item.(map[string]any)
			if !ok {
				out = append(out, item)
				continue
			}
			out = append(out, sanitizeChainMetaRef(ref))
		}
		return out
	default:
		return value
	}
}

func sanitizeChainMetaRef(ref map[string]any) map[string]any {
	out := make(map[string]any, len(ref))
	for key, value := range ref {
		if key == "ref_id" {
			if formatted, ok := decimalStringValue(value); ok {
				out[key] = formatted
				continue
			}
		}
		out[key] = value
	}
	return out
}

func decimalStringValue(value any) (string, bool) {
	switch number := value.(type) {
	case int:
		return strconv.Itoa(number), true
	case int32:
		return strconv.FormatInt(int64(number), 10), true
	case int64:
		return strconv.FormatInt(number, 10), true
	case uint:
		return strconv.FormatUint(uint64(number), 10), true
	case uint64:
		return strconv.FormatUint(number, 10), true
	case float64:
		return strconv.FormatFloat(number, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(number), 'f', -1, 32), true
	case string:
		return number, true
	default:
		return "", false
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
