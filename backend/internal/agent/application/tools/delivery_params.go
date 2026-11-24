package tools

import "encoding/json"

// DecodeDeliveryParams decodes the parameters of a P4 domain tool. It validates that
// raw is a JSON object and rejects unknown properties, so a model can never
// inject the runtime scope (version_id, agent_run_id, ...) into a tool call.
// P4 tool groups live outside this package; this shared decoder keeps their
// validation identical to the P1-P3 tools.
//
// GD-17 (v0.2.4-G) removed the version-steward tool group
// (requirement.goal_met / version.close_read / version.close_propose) together
// with the version_steward profile: closure is a deterministic reconciler
// action, not a model tool. The delivery issue/report tool groups still use this
// decoder.
func DecodeDeliveryParams(raw json.RawMessage, target any) *Error {
	return decodeParams(raw, target)
}
