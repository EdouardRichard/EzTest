// Package workspaceapp aggregates the read-only cross-context todo feed of the
// P6 workspace endpoint (spec §4.3.2/§7).
package workspaceapp

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// The six todo kinds of the frozen P6 contract. Each kind is owned by exactly
// one source context; the workspace context only aggregates and orders them.
const (
	KindRequirementConfirmation = "requirement_confirmation"
	KindCaseConfirmation        = "case_confirmation"
	KindIssueConfirmation       = "issue_confirmation"
	KindDesignPending           = "design_pending"
	KindHumanRequest            = "human_request"
	KindDeadWorkItem            = "dead_work_item"
)

// Severity values of the todo contract (spec §4.3.2).
const (
	SeverityMajor   = "major"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Limit bounds of GET /api/todos (spec §4.3.2): 1–200, default 50.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Provider is the read-only todo source of one owning context. Items are
// returned without a limit so the service can count the full feed before it
// truncates the visible items; each provider must therefore fetch its source
// with the workspace maximum (MaxLimit) and never write.
type Provider interface {
	Items(ctx context.Context, viewer platform.Actor) ([]TodoItem, error)
}

// VersionTitleReader is the narrow version application port used to decorate
// the todo items whose provider cannot read another context's version table. It
// resolves every requested version ID in one batch (spec §9: version_title
// comes from the version context, never from another context's SQL). The
// version application QueryService satisfies it without an adapter.
type VersionTitleReader interface {
	VersionTitles(ctx context.Context, ids []int64) (map[int64]string, error)
}
