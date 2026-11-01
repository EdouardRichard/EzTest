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
	ProfileKeyContextScout: {
		tools.ToolCodeTree,
		tools.ToolCodeRead,
		tools.ToolCodeSearch,
		tools.ToolCodeFacts,
		tools.ToolCodeRoutes,
		tools.ToolCodeAPIs,
		tools.ToolCodeDiff,
		tools.ToolCodeImpact,
		tools.ToolSUTPagePropose,
	},
	ProfileKeyCaseAuthor: {
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
	ProfileKeyDesignPlanner: {
		tools.ToolCodeImpact,
	},
	ProfileKeyCaseExecutor: {
		tools.ToolExecutionVisualAssert,
	},
}

// P5ProfileTools returns a copy of the P5 per-profile tool increment. The
// contract keeps sut.page_propose on context_scout only; case_author gets the
// read-only code tools.
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
	additions, ok := p5ProfileTools[profile.Key]
	if !ok {
		return profile
	}
	seen := make(map[string]struct{}, len(profile.AllowedTools)+len(additions))
	merged := make([]string, 0, len(profile.AllowedTools)+len(additions))
	for _, name := range profile.AllowedTools {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	for _, name := range additions {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	profile.AllowedTools = merged
	return profile
}
