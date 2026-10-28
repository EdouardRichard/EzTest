package agenttools

// case.graph_read is the read-only mindmap summary tool of design spec §8.2. It
// returns one node line per read node (id/kind/title/priority/source/status/
// revision/counts/requirement sources/anchor count/open blockers), the coverage
// uncovered ids and whether a baseline exists. It never returns descriptions,
// steps, assertions or file content, caps the node list at 300 and the encoded
// document at 64 KiB, and marks a truncated reply.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// graphReadNode is one node summary line. Every mindmap id is a decimal string
// (D29); optional parentId is null for a root node.
type graphReadNode struct {
	ID               string   `json:"id"`
	ParentID         *string  `json:"parentId"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title"`
	Priority         string   `json:"priority"`
	Source           string   `json:"source"`
	Status           string   `json:"status"`
	Revision         int      `json:"revision"`
	StepCount        int      `json:"stepCount"`
	AssertionCount   int      `json:"assertionCount"`
	RequirementIDs   []string `json:"requirementIds"`
	AnchorCount      int      `json:"anchorCount"`
	OpenBlockerCount int      `json:"openBlockerCount"`
}

// graphReadCoverage is the coverage projection of the summary.
type graphReadCoverage struct {
	RequirementIDs          []string `json:"requirementIds"`
	UncoveredRequirementIDs []string `json:"uncoveredRequirementIds"`
	StaleRequirementIDs     []string `json:"staleRequirementIds"`
}

// graphReadBaseline reports whether the version is frozen. Present=false means
// no baseline yet; the remaining fields are then omitted.
type graphReadBaseline struct {
	Present   bool   `json:"present"`
	ID        string `json:"id,omitempty"`
	GraphHash string `json:"graphHash,omitempty"`
	CaseCount int    `json:"caseCount,omitempty"`
	NodeCount int    `json:"nodeCount,omitempty"`
	FrozenAt  string `json:"frozenAt,omitempty"`
}

// graphReadOutput is the model-facing payload of case.graph_read.
type graphReadOutput struct {
	VersionID        string            `json:"versionId"`
	VersionStatus    string            `json:"versionStatus"`
	Editable         bool              `json:"editable"`
	NodeCount        int               `json:"nodeCount"`
	Truncated        bool              `json:"truncated"`
	Nodes            []graphReadNode   `json:"nodes"`
	Coverage         graphReadCoverage `json:"coverage"`
	Baseline         graphReadBaseline `json:"baseline"`
	OpenBlockerCount int               `json:"openBlockerCount"`
}

// runGraphRead executes case.graph_read.
func runGraphRead(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	var input struct {
		VersionID string `json:"versionId"`
	}
	if failure := decodeToolArguments(args, &input); failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return agentdomain.ToolResult{}, invalidToolArguments("versionId must be a positive decimal id")
	}
	view, err := deps.Graphs.Get(ctx, versionID)
	if err != nil {
		return graphReadFailure(err)
	}
	output := buildGraphReadOutput(ctx, deps, view, versionID)
	result, err := encodeGraphRead(output)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	recordToolAudit(ctx, deps, toolNameGraphRead, 0, map[string]any{
		"nodeCount": output.NodeCount,
		"truncated": output.Truncated,
	})
	return result, nil
}

// graphReadFailure maps a graph-read error onto the tool contract. A mindmap
// domain error (for example a missing version or graph_too_large) is a
// correctable invalid_arguments reply; anything else is an infrastructure
// failure.
func graphReadFailure(err error) (agentdomain.ToolResult, error) {
	var domainErr *mindmapdomain.Error
	if errors.As(err, &domainErr) {
		return agentdomain.ToolResult{}, invalidToolArguments("%s", domainErr.Error())
	}
	return agentdomain.ToolResult{}, err
}

// buildGraphReadOutput projects the GraphView into the summary shape and applies
// the 300-node cap. The optional anchor/finding readers only add counts.
func buildGraphReadOutput(ctx context.Context, deps ToolDeps, view mindmapapp.GraphView, versionID int64) graphReadOutput {
	output := graphReadOutput{
		VersionID:     formatID(versionID),
		VersionStatus: view.VersionStatus,
		Editable:      view.Editable,
		NodeCount:     len(view.Nodes),
		Nodes:         make([]graphReadNode, 0),
		Coverage: graphReadCoverage{
			RequirementIDs:          formatIDs(view.Coverage.RequirementIDs),
			UncoveredRequirementIDs: formatIDs(view.Coverage.UncoveredRequirementIDs),
			StaleRequirementIDs:     formatIDs(view.Coverage.StaleRequirementIDs),
		},
	}
	if view.Baseline != nil {
		output.Baseline = graphReadBaseline{
			Present:   true,
			ID:        formatID(view.Baseline.ID),
			GraphHash: view.Baseline.GraphHash,
			CaseCount: view.Baseline.CaseCount,
			NodeCount: view.Baseline.NodeCount,
			FrozenAt:  formatTime(view.Baseline.FrozenAt),
		}
	}

	// Open blockers are read once for the version; a nil port keeps the count at
	// zero (the tool still works without the optional finding repository).
	blockersByNode := map[int64]int{}
	if deps.Findings != nil {
		findings, err := deps.Findings.List(ctx, versionID, mindmapdomain.FindingOpen, mindmapdomain.SeverityBlocker, nil)
		if err == nil {
			output.OpenBlockerCount = len(findings)
			for _, finding := range findings {
				if finding.NodeID != nil {
					blockersByNode[*finding.NodeID]++
				}
			}
		}
	}

	nodes := view.Nodes
	if len(nodes) > maxGraphReadNodes {
		nodes = nodes[:maxGraphReadNodes]
		output.Truncated = true
	}
	for _, graphNode := range nodes {
		view := graphReadNode{
			ID:               formatID(graphNode.Node.ID),
			Kind:             graphNode.Node.Kind,
			Title:            graphNode.Node.Title,
			Priority:         graphNode.Node.Priority,
			Source:           graphNode.Node.Source,
			Status:           graphNode.Node.Status,
			Revision:         graphNode.Node.Revision,
			StepCount:        len(graphNode.Steps),
			AssertionCount:   len(graphNode.Assertions),
			RequirementIDs:   requirementSourceIDs(graphNode.Sources),
			OpenBlockerCount: blockersByNode[graphNode.Node.ID],
		}
		if graphNode.Node.ParentID != nil {
			parentID := formatID(*graphNode.Node.ParentID)
			view.ParentID = &parentID
		}
		if deps.Anchors != nil {
			if anchors, err := deps.Anchors.List(ctx, versionID, graphNode.Node.ID); err == nil {
				view.AnchorCount = len(anchors)
			}
		}
		output.Nodes = append(output.Nodes, view)
	}
	return output
}

// requirementSourceIDs returns the distinct requirement ids of one node's
// sources in stored order.
func requirementSourceIDs(sources []mindmapdomain.CaseSource) []string {
	ids := make([]string, 0, len(sources))
	seen := make(map[int64]struct{}, len(sources))
	for _, source := range sources {
		if source.RefKind != mindmapdomain.RefRequirement || source.RefID == nil {
			continue
		}
		if _, duplicate := seen[*source.RefID]; duplicate {
			continue
		}
		seen[*source.RefID] = struct{}{}
		ids = append(ids, formatID(*source.RefID))
	}
	return ids
}

// encodeGraphRead marshals the summary and enforces the 64 KiB cap by dropping
// nodes from the tail (and, as a last resort, the coverage detail lists) while
// marking truncated. The reply is always a valid JSON object.
func encodeGraphRead(output graphReadOutput) (agentdomain.ToolResult, error) {
	nodes := output.Nodes
	for {
		output.Nodes = nodes
		encoded, err := json.Marshal(output)
		if err != nil {
			return agentdomain.ToolResult{}, fmt.Errorf("mindmap: encode graph summary: %w", err)
		}
		if len(encoded) <= maxGraphReadBytes {
			return agentdomain.ToolResult{Content: string(encoded)}, nil
		}
		output.Truncated = true
		if len(nodes) > 0 {
			next := len(nodes) * 3 / 4
			if next >= len(nodes) {
				next = len(nodes) - 1
			}
			if next < 0 {
				next = 0
			}
			nodes = nodes[:next]
			continue
		}
		// With no nodes left the coverage lists alone can exceed the cap;
		// drop them once, then fall back to a minimal valid document.
		if len(output.Coverage.RequirementIDs) > 0 ||
			len(output.Coverage.UncoveredRequirementIDs) > 0 ||
			len(output.Coverage.StaleRequirementIDs) > 0 {
			output.Coverage = graphReadCoverage{
				RequirementIDs:          []string{},
				UncoveredRequirementIDs: []string{},
				StaleRequirementIDs:     []string{},
			}
			continue
		}
		return encodeToolJSON(graphReadOutput{
			VersionID:     output.VersionID,
			VersionStatus: output.VersionStatus,
			Editable:      output.Editable,
			NodeCount:     output.NodeCount,
			Truncated:     true,
			Nodes:         []graphReadNode{},
			Coverage: graphReadCoverage{
				RequirementIDs:          []string{},
				UncoveredRequirementIDs: []string{},
				StaleRequirementIDs:     []string{},
			},
			Baseline:         output.Baseline,
			OpenBlockerCount: output.OpenBlockerCount,
		})
	}
}
