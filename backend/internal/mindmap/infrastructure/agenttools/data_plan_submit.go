package agenttools

// case.data_plan_submit is the structured submission tool of a data-plan run
// (design spec §8.2). It validates the item cap, the node revision and the text
// bounds, resolves the calling data-plan run from the P1 invocation context and
// stores the items in that run's result. It never updates a node: the run worker
// applies preconditions/test_data under the node revision CAS.

import (
	"bytes"
	"context"
	"encoding/json"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// dataPlanSubmitSchema is the frozen input schema of case.data_plan_submit.
const dataPlanSubmitSchema = `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"items":{"type":"array","maxItems":60,"items":{"type":"object","properties":{"nodeId":{"type":"string","pattern":"^[0-9]+$"},"nodeRevision":{"type":"integer","minimum":1},"preconditions":{"type":"string"},"testData":{"description":"opaque valid JSON"}},"required":["nodeId","nodeRevision"],"additionalProperties":false}}},"required":["versionId","items"],"additionalProperties":false}`

// dataPlanSubmitInput is the case.data_plan_submit argument document. Items is a
// pointer so a missing field is distinguishable from an empty array.
type dataPlanSubmitInput struct {
	VersionID string               `json:"versionId"`
	Items     *[]dataPlanItemInput `json:"items"`
}

// dataPlanItemInput is one proposed node update.
type dataPlanItemInput struct {
	NodeID        string          `json:"nodeId"`
	NodeRevision  int             `json:"nodeRevision"`
	Preconditions *string         `json:"preconditions"`
	TestData      json.RawMessage `json:"testData"`
}

// dataPlanItemOutput is the stored item. Optional fields are omitted when the
// model did not send a value.
type dataPlanItemOutput struct {
	NodeID        string          `json:"nodeId"`
	NodeRevision  int             `json:"nodeRevision"`
	Preconditions *string         `json:"preconditions,omitempty"`
	TestData      json.RawMessage `json:"testData,omitempty"`
}

// dataPlanSubmitPayload is the run.result document the materializer reads.
type dataPlanSubmitPayload struct {
	Items []dataPlanItemOutput `json:"items"`
}

// dataPlanSubmitOutput is the model-facing acknowledgement.
type dataPlanSubmitOutput struct {
	Stored      bool `json:"stored"`
	Submissions int  `json:"submissions"`
	Items       int  `json:"items"`
}

// runDataPlanSubmit executes case.data_plan_submit.
func runDataPlanSubmit(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	var input dataPlanSubmitInput
	if failure := decodeToolArguments(args, &input); failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return agentdomain.ToolResult{}, invalidToolArguments("versionId must be a positive decimal id")
	}
	if input.Items == nil {
		return agentdomain.ToolResult{}, invalidToolArguments("items is required")
	}
	items := *input.Items
	if len(items) == 0 {
		return agentdomain.ToolResult{}, invalidToolArguments("items must not be empty")
	}
	if len(items) > maxDataPlanItems {
		return agentdomain.ToolResult{}, invalidToolArguments("items has %d entries; the limit is %d", len(items), maxDataPlanItems)
	}
	payload := dataPlanSubmitPayload{Items: make([]dataPlanItemOutput, 0, len(items))}
	for index, item := range items {
		prefix := "items[" + formatID(int64(index)) + "]"
		nodeID, ok := parsePositiveID(item.NodeID)
		if !ok {
			return agentdomain.ToolResult{}, invalidToolArguments("%s.nodeId must be a positive decimal id", prefix)
		}
		if item.NodeRevision <= 0 {
			return agentdomain.ToolResult{}, invalidToolArguments("%s.nodeRevision must be positive", prefix)
		}
		var preconditions *string
		if item.Preconditions != nil {
			if runeCount(*item.Preconditions) > maxPreconditionsRunes {
				return agentdomain.ToolResult{}, invalidToolArguments(
					"%s.preconditions has %d characters; the limit is %d", prefix, runeCount(*item.Preconditions), maxPreconditionsRunes)
			}
			preconditions = optionalString(*item.Preconditions)
		}
		testData, failure := normalizeOptionalTestData(item.TestData, prefix)
		if failure != nil {
			return agentdomain.ToolResult{}, failure
		}
		payload.Items = append(payload.Items, dataPlanItemOutput{
			NodeID:        formatID(nodeID),
			NodeRevision:  item.NodeRevision,
			Preconditions: preconditions,
			TestData:      testData,
		})
	}

	run, err := resolveActiveRun(ctx, deps, mindmapdomain.RunKindDataPlan, versionID)
	if err != nil {
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
	output := dataPlanSubmitOutput{Stored: true, Submissions: submissions, Items: len(payload.Items)}
	recordToolAudit(ctx, deps, toolNameDataPlanSubmit, run.ID, map[string]any{"items": output.Items})
	return encodeToolJSON(output)
}

// normalizeOptionalTestData returns nil for an absent/null document and keeps
// any other valid JSON value as-is. The materializer re-validates the JSONB
// shape when it applies the item.
func normalizeOptionalTestData(raw json.RawMessage, prefix string) (json.RawMessage, *agentdomain.ToolError) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if !json.Valid(trimmed) {
		return nil, invalidToolArguments("%s.testData must be valid JSON", prefix)
	}
	return json.RawMessage(trimmed), nil
}
