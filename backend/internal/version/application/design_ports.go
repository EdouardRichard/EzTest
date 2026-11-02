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
