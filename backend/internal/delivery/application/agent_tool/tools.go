package agent_tool

import (
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// Schemas of the eleven tools. Every schema is a closed object; the tools
// additionally validate the values (caps, enums, ownership) at execution time.
const (
	caseListSchema = `{"type":"object","properties":{"batchId":{"type":"string","pattern":"^[0-9]+$"}},"required":["batchId"],"additionalProperties":false}`

	caseDetailSchema = `{"type":"object","properties":{"caseRunId":{"type":"string","pattern":"^[0-9]+$"}},"required":["caseRunId"],"additionalProperties":false}`

	evidenceMetaSchema = `{"type":"object","properties":{"evidenceIds":{"type":"array","maxItems":100,"items":{"type":"string","pattern":"^[0-9]+$"}}},"required":["evidenceIds"],"additionalProperties":false}`

	codeReadSchema = `{"type":"object","properties":{"refs":{"type":"array","minItems":1,"maxItems":10,"items":{"type":"object","properties":{"path":{"type":"string"},"startLine":{"type":"integer","minimum":1},"endLine":{"type":"integer","minimum":1}},"required":["path","startLine","endLine"],"additionalProperties":false}}},"required":["refs"],"additionalProperties":false}`

	candidateProposeSchema = `{"type":"object","properties":{"candidates":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"object","properties":{"caseRunId":{"type":"string","pattern":"^[0-9]+$"},"caseNodeId":{"type":"string","pattern":"^[0-9]+$"},"title":{"type":"string"},"description":{"type":"string"},"severity":{"type":"string","enum":["blocker","major","minor","info"]},"confidence":{"type":"string","enum":["high","medium","low"]},"failureClass":{"type":"string"},"rerunSuggestion":{"type":"string"},"evidenceIds":{"type":"array","items":{"type":"string","pattern":"^[0-9]+$"}},"codeRefs":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer","minimum":1},"reason":{"type":"string"}},"required":["path","line","reason"],"additionalProperties":false}}},"required":["caseRunId","title","severity","confidence"],"additionalProperties":false}}},"required":["candidates"],"additionalProperties":false}`

	analysisFinishSchema = `{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`

	emptyArgumentsSchema = `{"type":"object","properties":{},"additionalProperties":false}`

	triageContextSchema = `{"type":"object","properties":{"caseRunIds":{"type":"array","maxItems":20,"items":{"type":"string","pattern":"^[0-9]+$"}},"evidenceIds":{"type":"array","maxItems":100,"items":{"type":"string","pattern":"^[0-9]+$"}}},"additionalProperties":false}`

	issueDraftSchema = `{"type":"object","properties":{"issues":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"object","properties":{"dedupeKey":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},"reproSteps":{"type":"string"},"expected":{"type":"string"},"actual":{"type":"string"},"severity":{"type":"string","enum":["blocker","major","minor","info"]},"confidence":{"type":"string","enum":["high","medium","low"]},"caseRunId":{"type":"string","pattern":"^[0-9]+$"},"caseNodeId":{"type":"string","pattern":"^[0-9]+$"},"evidenceIds":{"type":"array","items":{"type":"string","pattern":"^[0-9]+$"}},"codeRefs":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer","minimum":1},"reason":{"type":"string"}},"required":["path","line","reason"],"additionalProperties":false}},"mergeIntoIssueId":{"type":"string","pattern":"^[0-9]+$"}},"required":["title","severity","confidence"],"additionalProperties":false}}},"required":["issues"],"additionalProperties":false}`

	triageFinishSchema = analysisFinishSchema
)

// NewTools returns the eleven P7 delivery agent tools in their frozen spec §9.3/
// §9.4 order. A missing required port disables the whole set (nil), mirroring
// the P5/P6 tool constructors: a half-wired registry must fail the startup
// profile validation instead of offering fewer tools than a profile declares.
func NewTools(deps ToolDeps) []agentdomain.Tool {
	if !deps.ready() {
		return nil
	}
	definitions := []toolDefinition{
		{
			name:        domain.ToolAnalysisCaseList,
			description: "List the failed(application) case runs of this analysis batch (at most 20): case run/node ids, title, failure class and short reason. No evidence body.",
			schema:      caseListSchema,
			run:         runCaseList,
		},
		{
			name:        domain.ToolAnalysisCaseDetail,
			description: "Read one failed(application) case run of this batch: classification, recorded steps, assertions and evidence ids (metadata only, no artifact bytes).",
			schema:      caseDetailSchema,
			run:         runCaseDetail,
		},
		{
			name:        domain.ToolAnalysisEvidenceMeta,
			description: "Read evidence metadata (kind/sha256/size/mime) for evidence ids of the failed application cases. Foreign or malformed ids are returned as warnings; artifact content is never exposed.",
			schema:      evidenceMetaSchema,
			run:         runEvidenceMeta,
		},
		{
			name:        domain.ToolAnalysisCodeRead,
			description: "Read commit-pinned code windows from the version's bound repository: at most 10 files and 80 lines per file, 64 KiB total. Paths outside the repository are reported as warnings.",
			schema:      codeReadSchema,
			run:         runCodeRead,
		},
		{
			name:        domain.ToolAnalysisCandidatePropose,
			description: "Submit failure-analysis candidates (at most 20): caseRunId, title, description, severity, confidence, rerunSuggestion, evidenceIds and codeRefs. failureClass is always application; invalid entries become warnings.",
			schema:      candidateProposeSchema,
			run:         runCandidatePropose,
		},
		{
			name:        domain.ToolAnalysisFinish,
			description: "End the failure-analysis turn with a short summary. The server materializes the proposals after the agent run finishes.",
			schema:      analysisFinishSchema,
			run:         runAnalysisFinish,
		},
		{
			name:        domain.ToolTriageCandidateList,
			description: "Read the failure-analysis candidates this triage run was started with (ids are decimal strings; no evidence bytes).",
			schema:      emptyArgumentsSchema,
			run:         runTriageCandidateList,
		},
		{
			name:        domain.ToolTriageIssueList,
			description: "Read the open issue summaries of the version (id/title/severity only; dedupe keys and descriptions stay hidden).",
			schema:      emptyArgumentsSchema,
			run:         runTriageIssueList,
		},
		{
			name:        domain.ToolTriageContextRead,
			description: "Read case/evidence/code-location summaries for failed application case runs and their evidence ids. Foreign ids are refused.",
			schema:      triageContextSchema,
			run:         runTriageContextRead,
		},
		{
			name:        domain.ToolTriageIssueDraft,
			description: "Submit issue drafts (at most 20) with a reproducible description, expected/actual behavior, severity and confidence. The server injects category=defect and failureClass=application and derives the dedupe key when it is missing.",
			schema:      issueDraftSchema,
			run:         runIssueDraft,
		},
		{
			name:        domain.ToolTriageFinish,
			description: "End the issue-triage turn with a short summary. The server materializes the drafts after the agent run finishes.",
			schema:      triageFinishSchema,
			run:         runTriageFinish,
		},
	}
	tools := make([]agentdomain.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, analysisTool{deps: deps, def: definition})
	}
	return tools
}

// ValidateTools verifies that every tool the two delivery agent profiles allow is
// present in the live registry (spec §9.1/§9.6). The composition root must call
// it before seeding and treat a failure as fatal: a missing tool would silently
// offer the agent fewer tools than the profile declares.
func ValidateTools(registry *agentapp.ToolRegistry) error {
	for _, profile := range domain.AnalysisProfiles() {
		for _, name := range profile.AllowedTools {
			if tool, ok := registry.Get(name); !ok || tool == nil {
				return fmt.Errorf("delivery: agent profile %q allows tool %q, which is not registered",
					profile.Key, name)
			}
		}
	}
	return nil
}
