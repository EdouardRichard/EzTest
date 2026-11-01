package agentapp

import (
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// p7ProfileTools is the P7 allowed_tools increment per existing profile key
// (contracts/agent-profiles-tools.md §10.1). P7 adds no profile; the increment
// is merged into the startup seed as a union so P1–P6 tools keep their
// positions and administrator edits are still never overwritten. Only the
// Host/Scout/Planner/Author profiles may propose knowledge.
var p7ProfileTools = map[string][]string{
	ProfileKeyClarificationHost: {
		tools.ToolKnowledgePropose,
	},
	ProfileKeyContextScout: {
		tools.ToolKnowledgePropose,
	},
	ProfileKeyDesignHost: {
		tools.ToolKnowledgePropose,
	},
	ProfileKeyDesignPlanner: {
		tools.ToolKnowledgePropose,
	},
	ProfileKeyCaseAuthor: {
		tools.ToolKnowledgePropose,
	},
}

// P7ProfileTools returns a copy of the P7 per-profile tool increment.
func P7ProfileTools() map[string][]string {
	cloned := make(map[string][]string, len(p7ProfileTools))
	for key, names := range p7ProfileTools {
		cloned[key] = append([]string(nil), names...)
	}
	return cloned
}

// applyP7ProfileTools merges the P7 increment into one seed profile.
func applyP7ProfileTools(profile agent.AgentProfile) agent.AgentProfile {
	return mergeProfileTools(profile, p7ProfileTools[profile.Key])
}

// mergeProfileTools applies one allowed_tools increment as a set union: an
// already-seeded tool is not duplicated and the existing order is preserved.
func mergeProfileTools(profile agent.AgentProfile, additions []string) agent.AgentProfile {
	if len(additions) == 0 {
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
