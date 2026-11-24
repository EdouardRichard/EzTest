package agentapp

import (
	"context"
	"errors"
)

// RetiredCloseWorkTypes are the GD-17 (v0.2.4-G, R-G4) retired Steward close work
// types. They have NO producer and NO consumer after the Steward removal: closure
// is a deterministic reconciler action. This vocabulary exists ONLY so startup
// disposal can terminalize in-flight rows an upgrade left behind — without it,
// those dead rows would count toward NoBlockingWorkItems and block closure
// forever (SC-G10 existing-row disposal). It is the single sanctioned production
// occurrence of these strings (pinned by steward_removed_gate_test.go).
var RetiredCloseWorkTypes = []string{"requirement.close", "version.close"}

// retiredCloseWorkDisposalReason is recorded in work_item.last_error of every
// disposed row so the terminalization is attributable, not silent (七.6). It is
// admin-visible at runtime, so it carries no development decision codes
// (AGENTS §2.2 runtime cleanliness); the GD-17 decision traceability lives in
// this comment and the composition-root startup log.
const retiredCloseWorkDisposalReason = "闭环已改为服务端确定性直闭，该任务类型退役"

// RetiredWorkItemDisposer is the persistence port of the startup disposal sweep.
// The agent work-item repository satisfies it. DisposeWorkItemsByTypes
// terminalizes every claimable row of the given legacy types (status failed at
// the attempt cap, so no claim path resurrects it) and returns the affected count.
type RetiredWorkItemDisposer interface {
	DisposeWorkItemsByTypes(ctx context.Context, types []string, reason string) (int64, error)
}

// DisposeRetiredCloseWorkItems terminalizes in-flight requirement.close/
// version.close rows at startup (GD-17 / SC-G10). It is idempotent: a second call
// finds no claimable rows and returns 0. The composition root calls it once after
// migrations and before the workers start, and logs the disposed count.
func DisposeRetiredCloseWorkItems(ctx context.Context, repo RetiredWorkItemDisposer) (int64, error) {
	if repo == nil {
		return 0, errors.New("retired close work disposal has no work-item repository")
	}
	return repo.DisposeWorkItemsByTypes(ctx, RetiredCloseWorkTypes, retiredCloseWorkDisposalReason)
}
