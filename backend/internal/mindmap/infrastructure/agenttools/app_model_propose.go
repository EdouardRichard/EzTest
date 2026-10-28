package agenttools

// app_model_propose is the structured submission tool of an exploration run
// (design spec §8.2). It validates the node cap, the kind set and the parent-key
// references, resolves the calling explore run from the P1 invocation context
// and stores the proposal in that run's result. It never imports: the
// exploration materialization step validates the resource key rules and calls
// CatalogGateway.ImportAppModels.

import (
	"context"
	"encoding/json"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// appModelProposeSchema is the frozen input schema of app_model_propose.
const appModelProposeSchema = `{"type":"object","properties":{"systemId":{"type":"string","pattern":"^[0-9]+$"},"nodes":{"type":"array","maxItems":200,"items":{"type":"object","properties":{"key":{"type":"string","minLength":1,"maxLength":512},"kind":{"type":"string","enum":["page","route","api","component"]},"name":{"type":"string","minLength":1},"path":{"type":"string"},"method":{"type":"string"},"parentKey":{"type":"string"}},"required":["key","kind","name"],"additionalProperties":false}}},"required":["systemId","nodes"],"additionalProperties":false}`

// appModelProposeInput is the app_model_propose argument document. Nodes is a
// pointer so a missing field is distinguishable from an empty array.
type appModelProposeInput struct {
	SystemID string               `json:"systemId"`
	Nodes    *[]appModelNodeInput `json:"nodes"`
}

// appModelNodeInput is one proposed application model element.
type appModelNodeInput struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Method    string `json:"method"`
	ParentKey string `json:"parentKey"`
}

// appModelNodeOutput is the stored element. Optional path/method/parentKey are
// omitted when empty.
type appModelNodeOutput struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Method    string `json:"method,omitempty"`
	ParentKey string `json:"parentKey,omitempty"`
}

// appModelProposePayload is the run.result document the materializer reads.
type appModelProposePayload struct {
	SystemID string               `json:"systemId"`
	Nodes    []appModelNodeOutput `json:"nodes"`
}

// appModelProposeOutput is the model-facing acknowledgement.
type appModelProposeOutput struct {
	Stored      bool `json:"stored"`
	Submissions int  `json:"submissions"`
	Nodes       int  `json:"nodes"`
	Existing    int  `json:"existingKeys"`
}

// runAppModelPropose executes app_model_propose.
func runAppModelPropose(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	var input appModelProposeInput
	if failure := decodeToolArguments(args, &input); failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	systemID, ok := parsePositiveID(input.SystemID)
	if !ok {
		return agentdomain.ToolResult{}, invalidToolArguments("systemId must be a positive decimal id")
	}
	if input.Nodes == nil {
		return agentdomain.ToolResult{}, invalidToolArguments("nodes is required")
	}
	nodes := *input.Nodes
	if len(nodes) == 0 {
		return agentdomain.ToolResult{}, invalidToolArguments("nodes must not be empty")
	}
	if len(nodes) > maxAppModelNodes {
		return agentdomain.ToolResult{}, invalidToolArguments("nodes has %d entries; the limit is %d", len(nodes), maxAppModelNodes)
	}

	keys := make(map[string]struct{}, len(nodes))
	payload := appModelProposePayload{SystemID: formatID(systemID), Nodes: make([]appModelNodeOutput, 0, len(nodes))}
	for index, node := range nodes {
		prefix := "nodes[" + formatID(int64(index)) + "]"
		key := strings.TrimSpace(node.Key)
		if key == "" || len(key) > maxAppModelKeyLength {
			return agentdomain.ToolResult{}, invalidToolArguments(
				"%s.key must be between 1 and %d bytes", prefix, maxAppModelKeyLength)
		}
		if _, duplicate := keys[key]; duplicate {
			return agentdomain.ToolResult{}, invalidToolArguments("%s.key %q is duplicated", prefix, key)
		}
		keys[key] = struct{}{}
		kind := strings.TrimSpace(node.Kind)
		if !validAppModelKind(kind) {
			return agentdomain.ToolResult{}, invalidToolArguments(
				"%s.kind must be page, route, api or component", prefix)
		}
		name := strings.TrimSpace(node.Name)
		if name == "" {
			return agentdomain.ToolResult{}, invalidToolArguments("%s.name must not be empty", prefix)
		}
		payload.Nodes = append(payload.Nodes, appModelNodeOutput{
			Key:       key,
			Kind:      kind,
			Name:      name,
			Path:      strings.TrimSpace(node.Path),
			Method:    strings.TrimSpace(node.Method),
			ParentKey: strings.TrimSpace(node.ParentKey),
		})
	}
	for index, node := range payload.Nodes {
		if node.ParentKey == "" {
			continue
		}
		if _, ok := keys[node.ParentKey]; !ok {
			return agentdomain.ToolResult{}, invalidToolArguments(
				"nodes[%s].parentKey %q does not reference a node of this proposal",
				formatID(int64(index)), node.ParentKey)
		}
	}

	// Report how many proposed keys already exist in the system (best-effort:
	// a catalog read failure never blocks the submission).
	existing := 0
	if deps.Catalog != nil {
		if models, err := deps.Catalog.ListAppModels(ctx, systemID); err == nil {
			existingKeys := make(map[string]struct{}, len(models))
			for _, model := range models {
				existingKeys[model.Key] = struct{}{}
			}
			for _, node := range payload.Nodes {
				if _, ok := existingKeys[node.Key]; ok {
					existing++
				}
			}
		}
	}

	run, err := resolveActiveRun(ctx, deps, mindmapdomain.RunKindExplore, 0)
	if err != nil {
		// The explore run carries the version in its input; the system id is
		// the tool's addressing key, so a version filter is not available here.
		return agentdomain.ToolResult{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	submissions, err := storeRunResult(ctx, deps, run, encoded, "submissions")
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	output := appModelProposeOutput{
		Stored:      true,
		Submissions: submissions,
		Nodes:       len(payload.Nodes),
		Existing:    existing,
	}
	recordToolAudit(ctx, deps, toolNameAppModelSubmit, run.ID, map[string]any{"nodes": output.Nodes})
	return encodeToolJSON(output)
}
