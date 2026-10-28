// This file adapts the version application service to the execution autonomous
// worker's VersionFinalizer (spec §5.1/§6.2): after the last batch chain of a
// version settles, the worker asks the version service to evaluate its own
// finalize conditions. It lives in the execution infrastructure because the
// worker consumes the port.
package version

import (
	"context"
	"errors"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// FinalizerService is the version finalize slice the adapter needs.
// *version/application.VersionService satisfies it.
type FinalizerService interface {
	TryFinalize(ctx context.Context, versionID int64) (bool, error)
	SetContinuePhase(ctx context.Context, versionID int64, phase string) error
}

// Finalizer implements execution/application.VersionFinalizer over the version
// application service. The version service owns every finalize condition; this
// adapter only forwards.
type Finalizer struct {
	versions FinalizerService
}

// NewFinalizer returns the finalizer adapter. A nil service fails closed.
func NewFinalizer(versions FinalizerService) *Finalizer {
	return &Finalizer{versions: versions}
}

var _ executionapp.VersionFinalizer = (*Finalizer)(nil)

// TryFinalize implements execution/application.VersionFinalizer.
func (f *Finalizer) TryFinalize(ctx context.Context, versionID int64) (bool, error) {
	if f == nil || f.versions == nil {
		return false, errors.New("execution: version finalizer is not configured")
	}
	return f.versions.TryFinalize(ctx, versionID)
}

// SetContinuePhase implements execution/application.VersionFinalizer.
func (f *Finalizer) SetContinuePhase(ctx context.Context, versionID int64, phase string) error {
	if f == nil || f.versions == nil {
		return errors.New("execution: version finalizer is not configured")
	}
	return f.versions.SetContinuePhase(ctx, versionID, phase)
}
