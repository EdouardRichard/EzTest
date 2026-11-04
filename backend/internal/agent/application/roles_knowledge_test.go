package agentapp_test

import (
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// TestKnowledgeCuratorReadToolset pins the knowledge_curator role tools: the
// curator must be able to read findings/evidence/recipes and the source version
// before proposing knowledge, but stays a closed, read-only whitelist plus
// knowledge.propose (spec §5.2, §10.1).
func TestKnowledgeCuratorReadToolset(t *testing.T) {
	allowed := []string{
		tools.ToolContextVersionPack,
		tools.ToolContextTaskPack,
		tools.ToolContextTeamRoomRead,
		tools.ToolRequirementRead,
		tools.ToolExecutionResultRead,
		tools.ToolExecutionEvidenceRead,
		tools.ToolSUTList,
		tools.ToolSUTPageList,
		tools.ToolCodeFacts,
		tools.ToolCodeRoutes,
		tools.ToolCodeAPIs,
		tools.ToolKnowledgePropose,
		// Registered by the structured-retrieval task; whitelisting the names
		// now keeps the role closed while letting the later tools land without
		// widening permissions again.
		"knowledge.search",
		"knowledge.read",
	}
	for _, name := range allowed {
		if !agentapp.ToolAllowed(agentapp.RoleKnowledgeCurator, name) {
			t.Fatalf("knowledge_curator must allow %q", name)
		}
	}

	denied := []string{
		"browser.goto",
		"execution.case_finish",
		"execution.step_record",
		"case.confirm",
		tools.ToolCaseGraphPropose,
		tools.ToolDesignReviewSubmit,
		tools.ToolRequirementDraftCreate,
		tools.ToolRequirementDraftUpdate,
	}
	for _, name := range denied {
		if agentapp.ToolAllowed(agentapp.RoleKnowledgeCurator, name) {
			t.Fatalf("knowledge_curator must not allow %q", name)
		}
	}
}
