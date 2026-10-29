package domain

import "testing"

// TestCanTransitionNodeStatus covers the P7 controlled status matrix of spec
// §5.2: only the listed transitions are reachable through the supplement write
// paths (AppendSupplementNode / TransitionNodeStatus / MarkExecuted / MarkMerged).
func TestCanTransitionNodeStatus(t *testing.T) {
	allowed := []struct{ from, to string }{
		{NodeStatusSupplemented, NodeStatusMerged},
		{NodeStatusSupplemented, NodeStatusRejected},
		{NodeStatusReviewed, NodeStatusNeedsRevalidation},
		{NodeStatusReviewed, NodeStatusExecuted},
		{NodeStatusMerged, NodeStatusExecuted},
		{NodeStatusMerged, NodeStatusRejected},
		{NodeStatusNeedsRevalidation, NodeStatusExecuted},
		{NodeStatusNeedsRevalidation, NodeStatusRejected},
		{NodeStatusExecuted, NodeStatusNeedsRevalidation},
	}
	for _, transition := range allowed {
		if !CanTransitionNodeStatus(transition.from, transition.to) {
			t.Errorf("transition %s -> %s must be allowed", transition.from, transition.to)
		}
	}

	disallowed := []struct{ from, to string }{
		{NodeStatusRejected, NodeStatusMerged},
		{NodeStatusRejected, NodeStatusExecuted},
		{NodeStatusRejected, NodeStatusDraft},
		{NodeStatusExecuted, NodeStatusMerged},
		{NodeStatusExecuted, NodeStatusSupplemented},
		{NodeStatusExecuted, NodeStatusRejected},
		{NodeStatusExecuted, NodeStatusReviewed},
		{NodeStatusDraft, NodeStatusNeedsRevalidation},
		{NodeStatusDraft, NodeStatusMerged},
		{NodeStatusReviewed, NodeStatusMerged},
		{NodeStatusReviewed, NodeStatusRejected},
		{NodeStatusSupplemented, NodeStatusExecuted},
		{NodeStatusSupplemented, NodeStatusNeedsRevalidation},
		{NodeStatusNeedsRevalidation, NodeStatusMerged},
		{NodeStatusNeedsRevalidation, NodeStatusReviewed},
		{NodeStatusDraft, NodeStatusDraft},
		{NodeStatusSupplemented, NodeStatusSupplemented},
		{NodeStatusMerged, NodeStatusMerged},
		{NodeStatusNeedsRevalidation, NodeStatusNeedsRevalidation},
		{NodeStatusExecuted, NodeStatusExecuted},
	}
	for _, transition := range disallowed {
		if CanTransitionNodeStatus(transition.from, transition.to) {
			t.Errorf("transition %s -> %s must not be allowed", transition.from, transition.to)
		}
	}

	if CanTransitionNodeStatus("bogus", NodeStatusMerged) || CanTransitionNodeStatus(NodeStatusReviewed, "bogus") {
		t.Fatal("unknown statuses must never transition")
	}
}
