package agentapp

import "context"

// RequirementFinalizer verifies that a requirement.close run actually wrote a
// closure for the requirement version it was scoped to. A nil finalizer is a
// configuration error for requirement.close tasks (retryable), mirroring the
// report finalizer contract.
type RequirementFinalizer interface {
	Finalize(ctx context.Context, requirementID, requirementVersionID int64) error
}
