package agentapp

import (
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ProfileKeyKnowledgeCurator is the 0.2.1 profile that executes the
// knowledge_curator role (spec §5.2, D19). The role catalog previously had no
// dedicated profile, which left the structured retrieval tools registered but
// unreachable by any model: the runner exposes profile.AllowedTools ∩ role
// whitelist. Seeding the profile makes knowledge.search/knowledge.read
// model-visible while the runtime RoleToolGuard keeps the surface closed.
const ProfileKeyKnowledgeCurator = "knowledge_curator"

// knowledgeCuratorProfilePrompt is the role boundary prompt plus the
// [profile:<key>] marker used by the deterministic fake LLM and the prompt
// revision seed (the [role:<key>] marker is kept for the role catalog).
const knowledgeCuratorProfilePrompt = knowledgeCuratorRolePrompt + "\n[profile:knowledge_curator]"

// knowledgeCuratorProfileTools is the knowledge_curator profile surface: the
// structured retrieval tools (spec §10.2) plus the existing draft writer. Every
// entry is also in the role's closed whitelist.
var knowledgeCuratorProfileTools = []string{
	tools.ToolKnowledgeSearch,
	tools.ToolKnowledgeRead,
	tools.ToolKnowledgePropose,
}

// P7Profiles returns the 0.2.1 profile seeds introduced by the knowledge
// increment. They are appended by Profiles() after the frozen P1–P4 seeds; the
// v0.22 converged agent-center catalog lists this background profile in the
// execution/analysis team, and the admin profile surface and the startup seed
// list it too.
func P7Profiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyKnowledgeCurator,
			Name:         "知识维护员",
			SystemPrompt: knowledgeCuratorProfilePrompt,
			AllowedTools: append([]string(nil), knowledgeCuratorProfileTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 8, SoftSeconds: 180, HardSeconds: 300},
			Enabled:      true,
		},
	}
}

// p7ProfileTools is the P7 allowed_tools increment per existing profile key
// (contracts/agent-profiles-tools.md §10.1). P7 adds the draft writer to the
// profiles that may propose knowledge; the knowledge_curator profile (above) is
// a new seed rather than an increment. The increment is merged into the startup
// seed as a union so P1–P6 tools keep their positions and administrator edits
// are still never overwritten.
var p7ProfileTools = map[string][]string{
	ProfileKeyClarificationHost: {
		tools.ToolKnowledgePropose,
	},
	ProfileKeyDesignHost: {
		tools.ToolKnowledgePropose,
	},
	ProfileKeyDesignAuthor: {
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
