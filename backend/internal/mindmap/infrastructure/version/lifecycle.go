// Package version adapts the P3 version application services to the P5 mindmap
// ports (spec §4.3): the version lifecycle write port and the read-only
// requirement reader. It lives in the mindmap infrastructure layer so the
// mindmap application never imports another context; the composition root
// injects the concrete P3 services, which satisfy the narrow interfaces declared
// here. The adapter only maps projections and errors: the version context stays
// the single writer of version.status.
package version

import (
	"context"
	"errors"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// VersionService is the narrow slice of the P3 version service the lifecycle
// adapter needs. *version/application.VersionService satisfies it.
type VersionService interface {
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
	EnterReview(ctx context.Context, id int64, actor int64) (*versionapp.Version, error)
	RejectReview(ctx context.Context, id int64, actor int64) (*versionapp.Version, error)
	ApproveBaseline(ctx context.Context, id int64, actor int64) (*versionapp.Version, error)
}

// LifecycleAdapter implements application.VersionLifecycle on top of the P3
// version service.
type LifecycleAdapter struct {
	versions VersionService
}

// NewLifecycleAdapter wires the adapter to the P3 version service.
func NewLifecycleAdapter(versions VersionService) *LifecycleAdapter {
	return &LifecycleAdapter{versions: versions}
}

var _ mindmapapp.VersionLifecycle = (*LifecycleAdapter)(nil)

// The P7 run/plan extension is optional on application.VersionLifecycle; the
// production adapter always satisfies it so the incremental_design gate sees the
// real continue phase instead of falling back to the version status alone.
var _ mindmapapp.ContinuePhaseReader = (*LifecycleAdapter)(nil)

// GetVersion returns the mindmap projection of one version. The version status
// drives editability and the review state machine, so no defaulting happens
// here: the P3 value is copied as-is.
func (a *LifecycleAdapter) GetVersion(ctx context.Context, versionID int64) (mindmapapp.VersionSnapshot, error) {
	if a == nil || a.versions == nil {
		return mindmapapp.VersionSnapshot{}, errors.New("mindmap: version service is not configured")
	}
	version, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return mindmapapp.VersionSnapshot{}, mapVersionError(err)
	}
	return versionSnapshot(version, versionID)
}

// EnterReview moves the version to reviewing through the P3 state machine.
func (a *LifecycleAdapter) EnterReview(ctx context.Context, versionID, actor int64) (mindmapapp.VersionSnapshot, error) {
	if a == nil || a.versions == nil {
		return mindmapapp.VersionSnapshot{}, errors.New("mindmap: version service is not configured")
	}
	version, err := a.versions.EnterReview(ctx, versionID, actor)
	if err != nil {
		return mindmapapp.VersionSnapshot{}, mapVersionError(err)
	}
	return versionSnapshot(version, versionID)
}

// RejectReview returns the version to designing through the P3 state machine.
func (a *LifecycleAdapter) RejectReview(ctx context.Context, versionID, actor int64) (mindmapapp.VersionSnapshot, error) {
	if a == nil || a.versions == nil {
		return mindmapapp.VersionSnapshot{}, errors.New("mindmap: version service is not configured")
	}
	version, err := a.versions.RejectReview(ctx, versionID, actor)
	if err != nil {
		return mindmapapp.VersionSnapshot{}, mapVersionError(err)
	}
	return versionSnapshot(version, versionID)
}

// ApproveBaseline moves the version to executing through the P3 state machine.
// Freezing the case baseline is the mindmap context's job around this call.
func (a *LifecycleAdapter) ApproveBaseline(ctx context.Context, versionID, actor int64) (mindmapapp.VersionSnapshot, error) {
	if a == nil || a.versions == nil {
		return mindmapapp.VersionSnapshot{}, errors.New("mindmap: version service is not configured")
	}
	version, err := a.versions.ApproveBaseline(ctx, versionID, actor)
	if err != nil {
		return mindmapapp.VersionSnapshot{}, mapVersionError(err)
	}
	return versionSnapshot(version, versionID)
}

// ContinuePhase implements the optional P7 application.ContinuePhaseReader: it
// exposes version.continue_phase so the incremental_design run gate and the
// incremental plan read the real phase. A version without a continuing phase
// yields "", which the gate rejects. Version errors keep their mindmap mapping.
func (a *LifecycleAdapter) ContinuePhase(ctx context.Context, versionID int64) (string, error) {
	if a == nil || a.versions == nil {
		return "", errors.New("mindmap: version service is not configured")
	}
	version, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return "", mapVersionError(err)
	}
	if version == nil {
		return "", mindmapdomain.NewNotFound("version", versionID)
	}
	return version.ContinuePhase, nil
}

// versionSnapshot projects one P3 version onto the mindmap read model. Optional
// values are cloned so the snapshot never aliases the version service's state.
func versionSnapshot(version *versionapp.Version, versionID int64) (mindmapapp.VersionSnapshot, error) {
	if version == nil {
		return mindmapapp.VersionSnapshot{}, mindmapdomain.NewNotFound("version", versionID)
	}
	return mindmapapp.VersionSnapshot{
		ID:                 version.ID,
		Name:               version.Name,
		Status:             version.Status,
		SystemID:           cloneInt64(version.SystemID),
		EnvironmentID:      cloneInt64(version.EnvironmentID),
		CodeRepositoryID:   cloneInt64(version.CodeRepositoryID),
		CodeBaselineCommit: cloneString(version.CodeBaselineCommit),
	}, nil
}

// mapVersionError translates a P3 version domain error into the mindmap error
// kinds and reasons (spec §7.4). The stable reason and message are preserved;
// an operational failure is returned unchanged so it stays an internal error.
func mapVersionError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *versiondomain.Error
	if !errors.As(err, &domainErr) {
		return err
	}
	switch domainErr.Kind {
	case versiondomain.KindNotFound:
		return &mindmapdomain.Error{
			Kind:    mindmapdomain.KindNotFound,
			Reason:  "not_found",
			Message: domainErr.Error(),
		}
	case versiondomain.KindStateConflict:
		return mindmapdomain.NewStateConflict(
			reasonOrDefault(domainErr.Reason, "version_state_conflict"),
			domainErr.Error(),
		)
	case versiondomain.KindConflict:
		return mindmapdomain.NewConflict(
			reasonOrDefault(domainErr.Reason, "conflict"),
			domainErr.Error(),
		)
	case versiondomain.KindDependency:
		// The mindmap error model has no dependency kind; a blocked deletion is
		// reported as a 409 conflict with the P3 reason preserved.
		return mindmapdomain.NewConflict(
			reasonOrDefault(domainErr.Reason, "has_dependencies"),
			domainErr.Error(),
		)
	case versiondomain.KindInvalid:
		return mindmapdomain.NewInvalid(
			domainErr.Field,
			reasonOrDefault(domainErr.Reason, "invalid"),
			domainErr.Error(),
		)
	default:
		return err
	}
}

// reasonOrDefault returns reason, or fallback when the P3 error carried none.
func reasonOrDefault(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}

// cloneInt64 copies an optional id so the projection never aliases P3 state.
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// cloneString copies an optional string so the projection never aliases P3
// state.
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
