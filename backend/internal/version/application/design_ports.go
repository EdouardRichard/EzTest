package versionapp

import "context"

// DesignStarter starts (or repairs) the design round for a newly created
// design conversation. The version handler calls it best effort and the agent
// reconciler remains the fallback; main injects the design Planner adapter.
type DesignStarter interface {
	StartRound(ctx context.Context, versionID, conversationID, actorID int64) error
}

// DesignMetrics is the design context's projection surface for the version
// detail. main injects the design Planner adapter; a nil or failing metrics
// source falls back to the P1 clarified count and zero case counts.
type DesignMetrics interface {
	CountPendingDesign(ctx context.Context, versionID int64) (int, error)
	CaseCounts(ctx context.Context, versionID int64) (CaseCounts, error)
}

// DesignReviewGate is the shared designing -> case_review review predicate (T9,
// spec SC-E2/SC-E4/SC-E5): for every activated requirement, AC coverage complete
// (confirmed union current drafts) AND every current draft review-complete
// (pass / escalated_fail / judge_unavailable; only pending/reworking block).
//
// The design context's *designapp.ReviewGateService satisfies it. The version
// application never imports the design domain/application: the composition root
// (T13) adapts ONE gate instance into both this manual-advance port and the
// design reconciler's converge path, so the two advance paths share a single
// truth. A nil port fails closed (requireCaseDrafts): manual advance can never
// bypass the review gate.
type DesignReviewGate interface {
	ReviewGate(ctx context.Context, versionID int64) error
}
