// Package version adapts the version context's execution-state service to the
// execution context's VersionPort (spec §4.2). Execution never owns version
// status: this adapter only reads the version projection, mirrors the pause
// flag, records the newest batch number and completes a finished execution. It
// imports the version application interface only; no version infrastructure is
// referenced.
package version

import (
	"context"
	"errors"
	"sync"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// errNotConfigured is returned by every adapter call when no version service was
// wired (a partially wired process must fail closed, never silently no-op).
var errNotConfigured = errors.New("execution: version adapter is not configured")

// Service is the narrow version application slice the adapter needs.
// *version/application.VersionService satisfies it.
type Service interface {
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
	MarkPaused(ctx context.Context, id int64, paused bool, mode, reason string, actor int64) (*versionapp.Version, error)
	SetBatchNo(ctx context.Context, id int64, batchNo int, actor int64) (*versionapp.Version, error)
	CompleteExecution(ctx context.Context, id int64, actor int64) (*versionapp.Version, error)
}

// batchNoAllocator is the optional native allocate/release surface. The current
// version application service does not expose it; a future version service can,
// and the adapter will prefer it over the read-then-write fallback.
type batchNoAllocator interface {
	AllocateBatchNo(ctx context.Context, versionID int64) (int, error)
	ReleaseBatchNo(ctx context.Context, versionID int64, allocated int) error
}

// Adapter implements application.VersionPort over the version application
// service.
type Adapter struct {
	versions Service

	// mu serialises the fallback allocation. P6 is single-process (spec §5.3),
	// so this is enough to make read-then-write allocation monotonic in the API
	// process; the version service's own monotonic SQL guard is the second line
	// of defence.
	mu sync.Mutex
}

// New returns the adapter. A nil service fails closed on every call.
func New(versions Service) *Adapter {
	return &Adapter{versions: versions}
}

var _ executionapp.VersionPort = (*Adapter)(nil)

// The production version service must keep satisfying the adapter contract; a
// signature drift fails the build here rather than at the composition root.
var _ Service = (*versionapp.VersionService)(nil)

// GetVersion returns the version projection with owned copies of the optional
// binding fields.
func (a *Adapter) GetVersion(ctx context.Context, versionID int64) (executionapp.VersionRef, error) {
	if a == nil || a.versions == nil {
		return executionapp.VersionRef{}, errNotConfigured
	}
	version, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.VersionRef{}, mapVersionError(err)
	}
	return executionapp.VersionRef{
		ID:                 version.ID,
		Status:             version.Status,
		Paused:             version.Paused,
		PauseMode:          version.PauseMode,
		BatchNo:            version.BatchNo,
		SystemID:           copyInt64(version.SystemID),
		EnvironmentID:      copyInt64(version.EnvironmentID),
		CodeBaselineCommit: copyString(version.CodeBaselineCommit),
	}, nil
}

// MarkPaused mirrors the batch pause flag onto the version without changing its
// status.
func (a *Adapter) MarkPaused(ctx context.Context, versionID int64, paused bool, mode, reason string, actor int64) error {
	if a == nil || a.versions == nil {
		return errNotConfigured
	}
	_, err := a.versions.MarkPaused(ctx, versionID, paused, mode, reason, actor)
	return mapVersionError(err)
}

// SetBatchNo records an already allocated batch number.
func (a *Adapter) SetBatchNo(ctx context.Context, versionID int64, batchNo int, actor int64) error {
	if a == nil || a.versions == nil {
		return errNotConfigured
	}
	_, err := a.versions.SetBatchNo(ctx, versionID, batchNo, actor)
	return mapVersionError(err)
}

// AllocateBatchNo returns the version's next batch number and records it before
// the batch row is written. It prefers a native atomic allocator; without one it
// reads the version and writes the incremented number under the adapter mutex.
// The write goes through the version service's monotonic guard, so a number can
// never move backwards.
func (a *Adapter) AllocateBatchNo(ctx context.Context, versionID int64) (int, error) {
	if a == nil || a.versions == nil {
		return 0, errNotConfigured
	}
	if allocator, ok := a.versions.(batchNoAllocator); ok {
		allocated, err := allocator.AllocateBatchNo(ctx, versionID)
		return allocated, mapVersionError(err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	version, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return 0, mapVersionError(err)
	}
	next := version.BatchNo + 1
	if _, err := a.versions.SetBatchNo(ctx, versionID, next, 0); err != nil {
		return 0, mapVersionError(err)
	}
	return next, nil
}

// ReleaseBatchNo hands an unused number back after a failed batch insert. The
// version application service exposes no decrement, so without a native
// allocator this is a tolerated no-op: batch numbers are monotonic rather than
// gapless (application.VersionPort), and a skipped number is harmless.
func (a *Adapter) ReleaseBatchNo(ctx context.Context, versionID int64, allocated int) error {
	if a == nil || a.versions == nil {
		return errNotConfigured
	}
	if allocator, ok := a.versions.(batchNoAllocator); ok {
		return mapVersionError(allocator.ReleaseBatchNo(ctx, versionID, allocated))
	}
	return nil
}

// CompleteExecution moves an executing, unpaused version to completed.
func (a *Adapter) CompleteExecution(ctx context.Context, versionID, actor int64) error {
	if a == nil || a.versions == nil {
		return errNotConfigured
	}
	_, err := a.versions.CompleteExecution(ctx, versionID, actor)
	return mapVersionError(err)
}

// mapVersionError translates a version domain error into the execution context's
// ReasonError so callers (BatchService, the HTTP layer) never depend on another
// context's error type. Unknown errors are returned unchanged.
func mapVersionError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *versiondomain.Error
	if !errors.As(err, &domainErr) {
		return err
	}
	switch domainErr.Kind {
	case versiondomain.KindInvalid:
		return executiondomain.NewReasonError("invalid_argument", domainErr.Reason, domainErr.Message, false, nil)
	case versiondomain.KindNotFound:
		return executiondomain.NewReasonError("not_found", "not_found", domainErr.Message, false, nil)
	default:
		reason := domainErr.Reason
		if reason == "" {
			reason = string(domainErr.Kind)
		}
		return executiondomain.NewReasonError(reason, reason, domainErr.Message, false, nil)
	}
}

// copyInt64 returns an owned copy of an optional id.
func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// copyString returns an owned copy of an optional string.
func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
