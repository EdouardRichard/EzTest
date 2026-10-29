package agenttools

// case.graph_propose is the structured submission tool of a design run (design
// spec §6.7/§8.2). It validates the size and envelope of the proposal, resolves
// the calling design run from the P1 invocation context and stores the normalized
// §6.7 payload in that run's result. It never writes a business table: the run
// worker materializes the stored payload in one transaction after the agent run
// finishes.
//
// Two input shapes are accepted and normalized to one stored payload:
//
//	flat   (spec §6.7, the fake LLM and real callers): {versionId, mode?,
//	       confirmReplace?, scenarios, cases, anchors?}
//	nested (compatibility): {versionId, mode?, payload: {scenarios, cases, ...}}
//
// When both shapes are present the flat top-level fields win and the nested
// payload is ignored; this is the documented ambiguity rule. The stored payload
// is the payload object with the top-level mode/confirmReplace merged in, so the
// materializer always sees {scenarios, cases, mode?, confirmReplace?}. The
// authoritative replace gate stays confirmReplace on the POST /runs input, which
// the model cannot forge.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// graphProposeSchema is the frozen input schema of case.graph_propose. It
// declares both the flat §6.7 proposal fields and the nested payload object; the
// anyOf clause requires at least one of the two shapes. versionId is the only
// unconditionally required field.
const graphProposeSchema = `{
  "type": "object",
  "properties": {
    "versionId": {"type": "string", "pattern": "^[0-9]+$"},
    "mode": {"type": "string", "enum": ["append", "replace"]},
    "confirmReplace": {"type": "boolean"},
    "confirmedReplace": {"type": "boolean"},
    "scenarios": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "ref": {"type": "string", "minLength": 1},
          "title": {"type": "string", "minLength": 1},
          "parentRef": {"type": ["string", "null"]},
          "description": {"type": "string"}
        },
        "required": ["ref", "title"],
        "additionalProperties": false
      }
    },
    "cases": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "ref": {"type": "string", "minLength": 1},
          "scenarioRef": {"type": ["string", "null"]},
          "title": {"type": "string", "minLength": 1},
          "priority": {"type": "string", "enum": ["P0", "P1", "P2", "P3"]},
          "requirementIds": {"type": "array", "items": {"type": "string", "pattern": "^[0-9]+$"}},
          "preconditions": {"type": "string"},
          "testData": {"description": "opaque valid JSON"},
          "steps": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "action": {"type": "string"},
                "expected": {"type": "string"},
                "data": {"type": "string"}
              },
              "required": ["action"],
              "additionalProperties": false
            }
          },
          "assertions": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "stepIndex": {"type": ["integer", "null"], "description": "0-based index into the case steps array (0 = first step)"},
                "kind": {"type": "string", "enum": ["ui", "api", "db", "text", "visual"]},
                "target": {"type": "string"},
                "expression": {"type": "string"},
                "expected": {"type": "string"}
              },
              "required": ["kind", "expression"],
              "additionalProperties": false
            }
          },
          "anchors": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "file": {"type": "string"},
                "startLine": {"type": ["integer", "null"]},
                "endLine": {"type": ["integer", "null"]}
              },
              "required": ["file"],
              "additionalProperties": false
            }
          }
        },
        "required": ["ref", "title", "priority", "requirementIds", "steps", "assertions"],
        "additionalProperties": false
      }
    },
    "anchors": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "file": {"type": "string"},
          "startLine": {"type": ["integer", "null"]},
          "endLine": {"type": ["integer", "null"]}
        },
        "required": ["file"],
        "additionalProperties": false
      }
    },
    "payload": {
      "type": "object",
      "properties": {
        "mode": {"type": "string", "enum": ["append", "replace"]},
        "confirmReplace": {"type": "boolean"},
        "scenarios": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "ref": {"type": "string", "minLength": 1},
              "title": {"type": "string", "minLength": 1},
              "parentRef": {"type": ["string", "null"]},
              "description": {"type": "string"}
            },
            "required": ["ref", "title"],
            "additionalProperties": false
          }
        },
        "cases": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "ref": {"type": "string", "minLength": 1},
              "scenarioRef": {"type": ["string", "null"]},
              "title": {"type": "string", "minLength": 1},
              "priority": {"type": "string", "enum": ["P0", "P1", "P2", "P3"]},
              "requirementIds": {"type": "array", "items": {"type": "string", "pattern": "^[0-9]+$"}},
              "preconditions": {"type": "string"},
              "testData": {"description": "opaque valid JSON"},
              "steps": {
                "type": "array",
                "items": {
                  "type": "object",
                  "properties": {
                    "action": {"type": "string"},
                    "expected": {"type": "string"},
                    "data": {"type": "string"}
                  },
                  "required": ["action"],
                  "additionalProperties": false
                }
              },
              "assertions": {
                "type": "array",
                "items": {
                  "type": "object",
                  "properties": {
                    "stepIndex": {"type": ["integer", "null"], "description": "0-based index into the case steps array (0 = first step)"},
                    "kind": {"type": "string", "enum": ["ui", "api", "db", "text", "visual"]},
                    "target": {"type": "string"},
                    "expression": {"type": "string"},
                    "expected": {"type": "string"}
                  },
                  "required": ["kind", "expression"],
                  "additionalProperties": false
                }
              },
              "anchors": {
                "type": "array",
                "items": {
                  "type": "object",
                  "properties": {
                    "file": {"type": "string"},
                    "startLine": {"type": ["integer", "null"]},
                    "endLine": {"type": ["integer", "null"]}
                  },
                  "required": ["file"],
                  "additionalProperties": false
                }
              }
            },
            "required": ["ref", "title", "priority", "requirementIds", "steps", "assertions"],
            "additionalProperties": false
          }
        }
      },
      "required": ["scenarios", "cases"],
      "additionalProperties": false
    }
  },
  "required": ["versionId"],
  "anyOf": [{"required": ["payload"]}, {"required": ["scenarios", "cases"]}],
  "additionalProperties": false
}`

// graphProposeInput is the case.graph_propose argument document. Both the flat
// §6.7 fields and the nested payload are decoded; raw messages keep the shape
// distinguishable so an absent field is not confused with an explicit null.
// ConfirmedReplace is the legacy name accepted for schema parity only: the
// authoritative replace confirmation is confirmReplace on the run input written
// by POST /runs, which the model cannot forge.
type graphProposeInput struct {
	VersionID        string          `json:"versionId"`
	Mode             string          `json:"mode"`
	ConfirmReplace   *bool           `json:"confirmReplace"`
	ConfirmedReplace *bool           `json:"confirmedReplace"`
	Payload          json.RawMessage `json:"payload"`
	Scenarios        json.RawMessage `json:"scenarios"`
	Cases            json.RawMessage `json:"cases"`
	Anchors          json.RawMessage `json:"anchors"`
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
	stored, envelope, failure := normalizeGraphProposePayload(input)
	if failure != nil {
		return agentdomain.ToolResult{}, failure
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

// normalizeGraphProposePayload accepts either input shape and returns the single
// payload the materializer reads plus the envelope used for the reply counts.
// The flat shape wins when both are present.
func normalizeGraphProposePayload(input graphProposeInput) (json.RawMessage, graphProposeEnvelope, error) {
	flatPresent := rawPresent(input.Scenarios) || rawPresent(input.Cases) || rawPresent(input.Anchors)
	if flatPresent {
		return normalizeFlatGraphProposal(input)
	}
	if !rawPresent(input.Payload) {
		return nil, graphProposeEnvelope{}, invalidToolArguments(
			"scenarios and cases (flat §6.7 shape) or payload (nested shape) is required")
	}
	return normalizeNestedGraphProposal(input)
}

// normalizeFlatGraphProposal builds the canonical stored object from the flat
// §6.7 fields. The top-level mode and confirmReplace are merged into it.
func normalizeFlatGraphProposal(input graphProposeInput) (json.RawMessage, graphProposeEnvelope, error) {
	envelope := graphProposeEnvelope{}
	scenarios, failure := requireJSONArray(input.Scenarios, "scenarios")
	if failure != nil {
		return nil, envelope, failure
	}
	cases, failure := requireJSONArray(input.Cases, "cases")
	if failure != nil {
		return nil, envelope, failure
	}
	stored := map[string]json.RawMessage{
		"scenarios": compactRaw(input.Scenarios),
		"cases":     compactRaw(input.Cases),
	}
	if rawPresent(input.Anchors) {
		if _, failure := requireJSONArray(input.Anchors, "anchors"); failure != nil {
			return nil, envelope, failure
		}
		stored["anchors"] = compactRaw(input.Anchors)
	}
	mode, failure := resolveProposalMode(input.Mode, "")
	if failure != nil {
		return nil, envelope, failure
	}
	envelope.Mode = mode
	envelope.Scenarios = scenarios
	envelope.Cases = cases
	if failure := mergeGraphProposeMode(stored, mode); failure != nil {
		return nil, envelope, failure
	}
	if failure := mergeGraphProposeConfirmReplace(stored, input); failure != nil {
		return nil, envelope, failure
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, envelope, fmt.Errorf("mindmap: encode proposal payload: %w", err)
	}
	return encoded, envelope, nil
}

// normalizeNestedGraphProposal keeps the compatibility shape: the nested payload
// object is preserved as-is and the top-level mode/confirmReplace are merged in.
func normalizeNestedGraphProposal(input graphProposeInput) (json.RawMessage, graphProposeEnvelope, error) {
	envelope := graphProposeEnvelope{}
	if failure := requireJSONObject(input.Payload, "payload"); failure != nil {
		return nil, envelope, failure
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(input.Payload, &stored); err != nil || stored == nil {
		return nil, envelope, invalidToolArguments("payload must be a JSON object")
	}
	if err := json.Unmarshal(input.Payload, &envelope); err != nil {
		return nil, envelope, invalidToolArguments("payload is not a valid §6.7 proposal: %v", err)
	}
	mode, failure := resolveProposalMode(input.Mode, envelope.Mode)
	if failure != nil {
		return nil, envelope, failure
	}
	envelope.Mode = mode
	if failure := mergeGraphProposeMode(stored, mode); failure != nil {
		return nil, envelope, failure
	}
	if failure := mergeGraphProposeConfirmReplace(stored, input); failure != nil {
		return nil, envelope, failure
	}
	if rawPresent(input.Anchors) {
		if _, failure := requireJSONArray(input.Anchors, "anchors"); failure != nil {
			return nil, envelope, failure
		}
		stored["anchors"] = compactRaw(input.Anchors)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, envelope, fmt.Errorf("mindmap: encode proposal payload: %w", err)
	}
	return encoded, envelope, nil
}

// mergeGraphProposeMode writes a resolved mode into the stored object. An empty
// mode leaves the payload untouched so the materializer can fall back to the
// mode requested on POST /runs.
func mergeGraphProposeMode(stored map[string]json.RawMessage, mode string) *agentdomain.ToolError {
	if mode == "" {
		return nil
	}
	encoded, err := json.Marshal(mode)
	if err != nil {
		return invalidToolArguments("payload.mode is not encodable: %v", err)
	}
	stored["mode"] = encoded
	return nil
}

// mergeGraphProposeConfirmReplace copies the top-level confirmation into the
// stored payload (spec name confirmReplace; confirmedReplace is the legacy
// schema name). The run input remains the authoritative replace gate.
func mergeGraphProposeConfirmReplace(stored map[string]json.RawMessage, input graphProposeInput) *agentdomain.ToolError {
	confirm := input.ConfirmReplace
	if confirm == nil {
		confirm = input.ConfirmedReplace
	}
	if confirm == nil {
		return nil
	}
	encoded, err := json.Marshal(*confirm)
	if err != nil {
		return invalidToolArguments("confirmReplace is not encodable: %v", err)
	}
	stored["confirmReplace"] = encoded
	return nil
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

// rawPresent reports whether an optional raw JSON field was supplied. An
// explicit null is present (and is then rejected by the shape validation).
func rawPresent(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) > 0
}

// compactRaw trims the insignificant whitespace around one validated raw value.
func compactRaw(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(bytes.TrimSpace(raw))
}
