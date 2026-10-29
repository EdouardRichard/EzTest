// Package domain holds the delivery context's core types and invariants.
package domain

// Tool names of the P7 failure-analysis and issue-triage agents (spec §9.3,
// §9.4). The names must match the tools registered by the
// delivery/application/agent_tool package exactly; the composition root
// validates every profile allowlist against the live tool registry before
// seeding (fail-closed, spec §9.1/§9.6).
const (
	// ToolAnalysisCaseList lists the failed(application) case runs of the batch.
	ToolAnalysisCaseList = "analysis.case_list"
	// ToolAnalysisCaseDetail reads one failed case run's steps, assertions,
	// classification and evidence ids.
	ToolAnalysisCaseDetail = "analysis.case_detail"
	// ToolAnalysisEvidenceMeta reads evidence metadata only (no artifact bytes).
	ToolAnalysisEvidenceMeta = "analysis.evidence_meta"
	// ToolAnalysisCodeRead reads bounded commit-pinned code windows.
	ToolAnalysisCodeRead = "analysis.code_read"
	// ToolAnalysisCandidatePropose submits failure candidates to the run result.
	ToolAnalysisCandidatePropose = "analysis.candidate_propose"
	// ToolAnalysisFinish lets the agent end the analysis explicitly.
	ToolAnalysisFinish = "analysis.finish"

	// ToolTriageCandidateList reads the failure candidates of the triage input.
	ToolTriageCandidateList = "triage.candidate_list"
	// ToolTriageIssueList reads the open issue summaries of the version.
	ToolTriageIssueList = "triage.issue_list"
	// ToolTriageContextRead reads case/evidence/code context summaries.
	ToolTriageContextRead = "triage.context_read"
	// ToolTriageIssueDraft submits issue drafts to the run result.
	ToolTriageIssueDraft = "triage.issue_draft"
	// ToolTriageFinish lets the agent end the triage explicitly.
	ToolTriageFinish = "triage.finish"
)

// FailureAnalysisAllowedTools returns the frozen failure-analysis agent tool
// allowlist of spec §9.3. Every call returns a fresh slice so a caller cannot
// mutate the seed a later startup persists.
func FailureAnalysisAllowedTools() []string {
	return []string{
		ToolAnalysisCaseList,
		ToolAnalysisCaseDetail,
		ToolAnalysisEvidenceMeta,
		ToolAnalysisCodeRead,
		ToolAnalysisCandidatePropose,
		ToolAnalysisFinish,
	}
}

// IssueTriageAllowedTools returns the frozen issue-triage agent tool allowlist
// of spec §9.4. Every call returns a fresh slice.
func IssueTriageAllowedTools() []string {
	return []string{
		ToolTriageCandidateList,
		ToolTriageIssueList,
		ToolTriageContextRead,
		ToolTriageIssueDraft,
		ToolTriageFinish,
	}
}
