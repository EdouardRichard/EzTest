package agenttools

// case.graph_propose is the structured submission tool of a design run (design
// spec §6.7/§8.2). It validates the size and envelope of the proposal, resolves
// the calling design run from the P1 invocation context and stores the raw §6.7
// payload in that run's result. It never writes a business table: the run
// worker materializes the stored payload in one transaction after the agent run
// finishes.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// graphProposeSchema is the frozen input schema of case.graph_propose. The
// payload nested object is the §6.7 design proposal.
const graphProposeSchema = `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"mode":{"type":"string","enum":["append","replace"]},"confirmedReplace":{"type":"boolean"},"payload":{"type":"object","properties":{"mode":{"type":"string","enum":["append","replace"]},"scenarios":{"type":"array","items":{"type":"object","properties":{"ref":{"type":"string","minLength":1},"title":{"type":"string","minLength":1},"parentRef":{"type":["string","null"]},"description":{"type":"string"}},"required":["ref","title"],"additionalProperties":false}},"cases":{"type":"array","items":{"type":"object","properties":{"ref":{"type":"string","minLength":1},"scenarioRef":{"type":["string","null"]},"title":{"type":"string","minLength":1},"priority":{"type":"string","enum":["P0","P1","P2","P3"]},"requirementIds":{"type":"array","items":{"type":"string","pattern":"^[0-9]+$"}},"preconditions":{"type":"string"},"testData":{"description":"opaque valid JSON"},"steps":{"type":"array","items":{"type":"object","properties":{"action":{"type":"string"},"expected":{"type":"string"},"data":{"type":"string"}},"required":["action"],"additionalProperties":false}},"assertions":{"type":"array","items":{"type":"object","properties":{"stepIndex":{"type":["integer","null"]},"kind":{"type":"string","enum":["ui","api","db","text"]},"target":{"type":"string"},"expression":{"type":"string"},"expected":{"type":"string"}},"required":["kind","expression"],"additionalProperties":false}},"anchors":{"type":"array","items":{"type":"object","properties":{"file":{"type":"string"},"startLine":{"type":["integer","null"]},"endLine":{"type":["integer","null"]}},"required":["file"],"additionalProperties":false}}},"required":["ref","title","priority","requirementIds","steps","assertions"],"additionalProperties":false}}},"required":["scenarios","cases"],"additionalProperties":false}},"required":["versionId","payload"],"additionalProperties":false}`

// graphProposeInput is the case.graph_propose argument document. ConfirmedReplace
// is accepted for schema parity only: the authoritative replace confirmation is
// confirmReplace on the run input written by POST /runs, which the model cannot
// forge.
type graphProposeInput struct {
	VersionID        string          `json:"versionId"`
	Mode             string          `json:"mode"`
	ConfirmedReplace bool            `json:"confirmedReplace"`
	Payload          json.RawMessage `json:"payload"`
}

// graphProposeEnvelope is the part of the §6.7 payload the tool reports on.
type graphProposeEnvelope struct {
	Mode      string            `json:"mode"`
	Scenarios []json.RawMessage `json:"scenarios"`
	Cases     []json.RawMessage `json:"cases"`
}

// graphProposeOutput is the model-facing acknowledgement.
type graphProposeOutput struct {
	Stored    bool `json:"stored"`
	Proposals int  `json:"proposals"`
	Scenarios int  `json:"scenarios"`
	Cases     int  `json:"cases"`
	Bytes     int  `json:"bytes"`
}

// runGraphPropose executes case.graph_propose.
func runGraphPropose(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	var input graphProposeInput
	if failure := decodeToolArguments(args, &input); failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return agentdomain.ToolResult{}, invalidToolArguments("versionId must be a positive decimal id")
	}
	if failure := requireJSONObject(input.Payload, "payload"); failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	var envelope graphProposeEnvelope
	if err := json.Unmarshal(input.Payload, &envelope); err != nil {
		return agentdomain.ToolResult{}, invalidToolArguments("payload is not a valid §6.7 proposal: %v", err)
	}
	mode, failure := resolveProposalMode(input.Mode, envelope.Mode)
	if failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	stored := input.Payload
	if mode != "" && strings.TrimSpace(envelope.Mode) == "" {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(input.Payload, &object); err != nil || object == nil {
			return agentdomain.ToolResult{}, invalidToolArguments("payload must be a JSON object")
		}
		encodedMode, err := json.Marshal(mode)
		if err != nil {
			return agentdomain.ToolResult{}, fmt.Errorf("mindmap: encode proposal mode: %w", err)
		}
		object["mode"] = encodedMode
		merged, err := json.Marshal(object)
		if err != nil {
			return agentdomain.ToolResult{}, fmt.Errorf("mindmap: encode proposal payload: %w", err)
		}
		stored = merged
	}
	if len(stored) > maxProposalBytes {
		return agentdomain.ToolResult{}, toolFailure(toolErrorProposalTooLarge,
			"proposal is %d bytes; the limit is %d bytes", len(stored), maxProposalBytes)
	}

	run, err := resolveActiveRun(ctx, deps, mindmapdomain.RunKindDesign, versionID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	proposals, err := storeRunResult(ctx, deps, run, stored, "proposals")
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	output := graphProposeOutput{
		Stored:    true,
		Proposals: proposals,
		Scenarios: len(envelope.Scenarios),
		Cases:     len(envelope.Cases),
		Bytes:     len(stored),
	}
	recordToolAudit(ctx, deps, toolNameGraphPropose, run.ID, map[string]any{
		"proposals": proposals,
		"scenarios": output.Scenarios,
		"cases":     output.Cases,
	})
	return encodeToolJSON(output)
}

// resolveProposalMode validates the optional tool-level mode and the payload
// mode, rejecting a conflict between the two. The empty result means the
// materializer falls back to the run input mode.
func resolveProposalMode(toolMode, payloadMode string) (string, *agentdomain.ToolError) {
	toolMode = strings.TrimSpace(toolMode)
	payloadMode = strings.TrimSpace(payloadMode)
	if toolMode != "" && toolMode != "append" && toolMode != "replace" {
		return "", invalidToolArguments("mode must be append or replace")
	}
	if payloadMode != "" && payloadMode != "append" && payloadMode != "replace" {
		return "", invalidToolArguments("payload.mode must be append or replace")
	}
	if toolMode != "" && payloadMode != "" && toolMode != payloadMode {
		return "", invalidToolArguments("mode %q conflicts with payload.mode %q", toolMode, payloadMode)
	}
	if payloadMode != "" {
		return payloadMode, nil
	}
	return toolMode, nil
}
