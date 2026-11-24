package vocabscope

// The repository-wide exemption table: the MERGE of the four context gates'
// adjudicated tables. The composition-root gate guards the union of every context's
// infrastructure, so it must sanction exactly the occurrences those gates already
// adjudicated -- no more. MergeAllowlists concatenates reasons and fragment tables,
// so no adjudication is lost and every fragment stays file-bound, occurrence-scoped
// and index-keyed for the stale check.
//
// There are NO repository-only exemptions: the six contexts no context gate had
// ever covered (audit / identity / quality / resource / settings / workspace) were
// measured on the T11e-6 tree and produced ZERO findings against the full 395-value
// protected set, so they join the net with an empty exemption face.

import "github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"

// RepositoryAllowlist merges every context gate's adjudicated exemptions.
func RepositoryAllowlist() map[string]vocablint.AllowEntry {
	return vocablint.MergeAllowlists(
		vocablint.MergeAllowlists(DeliveryGateAllowlist(), VersionGateAllowlist()),
		vocablint.MergeAllowlists(OrchestrationGateAllowlist(), AgentGateAllowlist()),
	)
}
