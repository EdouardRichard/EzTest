package agentapp

import (
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// p5ProfileTools is the P5 allowed_tools increment per existing profile key
// (spec §4.4 / contracts/agent-profiles-tools.md §8.1). P5 adds no profile; the
// increment is merged into the startup seed as a union so P1–P4 tools keep
// their positions and administrator edits are still never overwritten.
var p5ProfileTools = map[string][]string{
	ProfileKeyDesignAuthor: {
		tools.ToolCodeFacts,
		tools.ToolCodeRoutes,
		tools.ToolCodeAPIs,
		tools.ToolCodeDiff,
		tools.ToolCodeImpact,
		tools.ToolCodeAnchors,
	},
	ProfileKeyFailureAnalyst: {
		tools.ToolCodeDiff,
		tools.ToolCodeFacts,
		tools.ToolCodeAnchors,
	},
	ProfileKeyDesignHost: {
		tools.ToolCodeFacts,
		tools.ToolCodeRoutes,
		tools.ToolCodeAPIs,
	},
	ProfileKeyCaseExecutor: {
		tools.ToolExecutionVisualAssert,
	},
}

// P5ProfileTools returns a copy of the P5 per-profile tool increment. The
// removed Scout/Planner entries are gone; the design author keeps the read-only
// code tools it inherited from the old author seed.
func P5ProfileTools() map[string][]string {
	cloned := make(map[string][]string, len(p5ProfileTools))
	for key, names := range p5ProfileTools {
		cloned[key] = append([]string(nil), names...)
	}
	return cloned
}

// applyP5ProfileTools merges the P5 increment into one seed profile. It is a set
// union: an already-seeded tool such as execution.visual_assert is not
// duplicated, and P1–P4 ordering is preserved.
func applyP5ProfileTools(profile agent.AgentProfile) agent.AgentProfile {
	return mergeProfileTools(profile, p5ProfileTools[profile.Key])
}
