package adapters

import (
	"context"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VersionCloseExecution adapts the requirement-scoped ClosureReaders execution
// methods to the version-scoped VersionExecutionReader consumed by version
// close. The two interfaces deliberately share method names; passing the raw
// ClosureReaders as VersionExecutionReader would treat a version ID as a
// requirement ID and silently fail every close evaluation.
type VersionCloseExecution struct {
	readers *ClosureReaders
}

// NewVersionCloseExecution returns the version-scoped execution adapter.
func NewVersionCloseExecution(readers *ClosureReaders) *VersionCloseExecution {
	return &VersionCloseExecution{readers: readers}
}

var _ versionapp.VersionExecutionReader = (*VersionCloseExecution)(nil)

// CountActiveIntents counts open/claimed/running intents of every requirement
// in the version.
func (a *VersionCloseExecution) CountActiveIntents(ctx context.Context, versionID int64) (int, error) {
	return a.readers.CountVersionActiveIntents(ctx, versionID)
}

// CountActiveWaves counts planned/running/paused waves of the version.
func (a *VersionCloseExecution) CountActiveWaves(ctx context.Context, versionID int64) (int, error) {
	return a.readers.CountActiveWaves(ctx, versionID)
}

// CountInflightCaseRuns counts queued/running case runs of the version.
func (a *VersionCloseExecution) CountInflightCaseRuns(ctx context.Context, versionID int64) (int, error) {
	return a.readers.CountVersionInflightCaseRuns(ctx, versionID)
}
