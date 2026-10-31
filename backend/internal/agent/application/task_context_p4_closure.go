package agentapp

// ClosureTaskContext is the payload-only P4 task-context provider for the two
// steward close work items (contracts/task-types.md §6). It derives its result
// from the TaskRef alone — version from the work item, the rest from the raw
// payload — so it needs no application services and can be registered from the
// composition root (Task 23) with RegisterP4TaskContext:
//
//	RegisterP4TaskContext(func(_ context.Context, ref TaskRef) (any, bool) {
//		return ClosureTaskContext(ref)
//	})
//
// The returned JSON-ready map keeps the payload's decimal-string IDs; data
// beyond the work-item reference (checks, requirement/version details) is read
// by the scoped steward tools during the run.
func ClosureTaskContext(ref TaskRef) (any, bool) {
	details := make(map[string]any, 5)
	if ref.VersionID > 0 {
		details["version_id"] = idText(ref.VersionID)
	}
	switch ref.Type {
	case WorkTypeRequirementClose:
		copyTaskPayload(details, ref.Payload,
			"requirement_id", "requirement_version_id", "close_round")
	case WorkTypeVersionClose:
		copyTaskPayload(details, ref.Payload,
			"report_revision_id", "predicate_fingerprint")
	default:
		return nil, false
	}
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
