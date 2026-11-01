package tools

import "encoding/json"

// P4 version-steward tool names (contracts/agent-profiles-tools.md §7.3). The
// version application context implements the tools (versionapp.VersionTools);
// the constants stay here so the agent registry, the profile seeds and the
// version-context tests share one frozen spelling.
const (
	ToolRequirementGoalMet  = "requirement.goal_met"
	ToolVersionCloseRead    = "version.close_read"
	ToolVersionClosePropose = "version.close_propose"
)

// DecodeP4Params decodes the parameters of a P4 domain tool. It validates that
// raw is a JSON object and rejects unknown properties, so a model can never
// inject the runtime scope (version_id, agent_run_id, ...) into a tool call.
// P4 tool groups live outside this package; this shared decoder keeps their
// validation identical to the P1-P3 tools.
func DecodeP4Params(raw json.RawMessage, target any) *Error {
	return decodeParams(raw, target)
}

// VersionTools wraps the version context's tool group factory into the
// deps-aware factory RegisterP4ToolGroup expects. The version application
// package owns the three steward tools and imports this package for Tool/Scope,
// so importing it here would create an import cycle; instead the composition
// root (Task 23) captures the wired services:
//
//	tools.RegisterP4ToolGroup(tools.VersionTools(func() []Tool {
//		return versionapp.VersionTools(closure, versionClose)
//	}))
//
// A nil factory yields a nil group, which RegisterP4ToolGroup ignores.
func VersionTools(factory func() []Tool) func(Deps) []Tool {
	if factory == nil {
		return nil
	}
	return func(Deps) []Tool { return factory() }
}
