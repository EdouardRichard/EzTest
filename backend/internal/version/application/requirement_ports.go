package versionapp

import (
	"context"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// VersionReader is the slice of the version service consumed by the
// requirement and conversation services: existence/binding lookup plus the
// initial -> running projection.
type VersionReader interface {
	Get(ctx context.Context, id int64) (VersionView, error)
	MarkRunning(ctx context.Context, id int64) error
}

// ResourceCatalog resolves page/API targets against the resource catalog.
type ResourceCatalog interface {
	GetPage(ctx context.Context, id int64) (resourceapp.PageView, error)
	GetAPI(ctx context.Context, id int64) (resourceapp.APIView, error)
}

// CaseStaleHook is the P2 stale-marking port. P1 wires the no-op
// implementation: the event still carries affected_case_version_ids (empty).
type CaseStaleHook interface {
	MarkRequirementChanged(ctx context.Context, versionID, requirementID int64, reason string, revision int) ([]string, error)
}

// RequirementVersionReopener clears the completed projection when a new
// requirement draft is created (new requirement or new draft revision) on a
// completed version (spec §7.5). It is best effort: a failure never rolls back
// the requirement write, and the reconciler is the fallback. The composition
// root wires the version close service.
type RequirementVersionReopener interface {
	ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error
}

// NoopCaseStaleHook is the P1 stale hook: no case tables exist yet.
type NoopCaseStaleHook struct{}

// MarkRequirementChanged always reports no affected case versions.
func (NoopCaseStaleHook) MarkRequirementChanged(context.Context, int64, int64, string, int) ([]string, error) {
	return []string{}, nil
}
