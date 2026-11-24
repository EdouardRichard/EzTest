package agentapp

// IssueTaskContext is the payload-only P4 task-context provider for
// issue.triage (contracts/task-types.md §6). It derives its result from the
// TaskRef alone — the version from the work item, the issue from the raw
// payload — so it needs no application services and can be registered from the
// composition root (Task 23) with RegisterDeliveryTaskContext:
//
//	RegisterDeliveryTaskContext(func(_ context.Context, ref TaskRef) (any, bool) {
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

// copyTaskPayload copies the present keys of the raw work-item payload into the
// task context. Missing keys stay missing instead of materializing zero IDs.
func copyTaskPayload(target, payload map[string]any, keys ...string) {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		target[key] = value
	}
}
