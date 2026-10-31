package agentapp

// IssueTaskContext is the payload-only P4 task-context provider for
// issue.triage (contracts/task-types.md §6). It derives its result from the
// TaskRef alone — the version from the work item, the issue from the raw
// payload — so it needs no application services and can be registered from the
// composition root (Task 23) with RegisterP4TaskContext:
//
//	RegisterP4TaskContext(func(_ context.Context, ref TaskRef) (any, bool) {
//		return IssueTaskContext(ref)
//	})
//
// The returned JSON-ready map keeps only IDs. The issue, its refs and the linked
// case_run summaries are loaded by the scoped issue.read tool during the run.
func IssueTaskContext(ref TaskRef) (any, bool) {
	if ref.Type != WorkTypeIssueTriage {
		return nil, false
	}
	details := make(map[string]any, 2)
	if ref.VersionID > 0 {
		details["version_id"] = idText(ref.VersionID)
	}
	copyTaskPayload(details, ref.Payload, "issue_id")
	return details, true
}
