package versionapp

import (
	"context"
	"errors"
	"fmt"
)

// ClosureFinalizer verifies that a requirement.close run actually recorded the
// closure for the requirement version it was scoped to. A transient predicate
// rejection (active intent, in-flight case run) is model-visible and the model
// may still finish; without this check the work item would be marked done while
// nextCloseRound() keeps the same round and EnsureClosures cannot retry it.
type ClosureFinalizer struct {
	closures *ClosureService
}

// NewClosureFinalizer returns the requirement.close task finalizer.
func NewClosureFinalizer(closures *ClosureService) *ClosureFinalizer {
	return &ClosureFinalizer{closures: closures}
}

// Finalize returns a retryable error when the current requirement version has
// no closure round yet. The agent runner maps it to a retryable task failure, so
// the same work item is retried after backoff once intents/runs drain.
func (f *ClosureFinalizer) Finalize(ctx context.Context, requirementID, requirementVersionID int64) error {
	if f == nil || f.closures == nil {
		return errors.New("requirement finalizer is missing its closure service")
	}
	closed, err := f.closures.ClosedForVersion(ctx, requirementID, requirementVersionID)
	if err != nil {
		return err
	}
	if !closed {
		return fmt.Errorf("requirement %d version %d has no closure round yet", requirementID, requirementVersionID)
	}
	return nil
}
