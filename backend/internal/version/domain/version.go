// Package version is the version/binding bounded context. A version pins a
// testable snapshot of one ENVIRONMENT (v0.2.4-B SC-B2): the binding is the
// environment_id on the version row itself, the SUT catalog and the code pool
// are derived from that environment through the resource catalog, and the
// version only persists the account subset it executes with.
package version

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Version statuses. P0 only ever writes initial; running/completed belong to
// later phases. These constants are the single authority for the status enum
// (V-0017): the frozen version.status CHECK and the frontend VersionStatus union
// are validated against Statuses() rather than hand-maintained.
const (
	StatusInitial   = "initial"
	StatusRunning   = "running"
	StatusCompleted = "completed"
)

// Statuses returns every version status in canonical order. It is the SINGLE
// authority for the status enum (V-0017); the enum-authority gate validates the
// frozen DB CHECK and the frontend VersionStatus union against it.
func Statuses() []string {
	return []string{StatusInitial, StatusRunning, StatusCompleted}
}

// MaxTitleLength caps the version title.
const MaxTitleLength = 200

// Version is one version workspace.
type Version struct {
	ID    int64
	Title string
	// Description and Goal are the free-form version metadata of v0.22 R-C3-4.
	Description               string
	Goal                      string
	Status                    string
	AutoDesignNewRequirements bool
	// EnvironmentID is the environment this version binds (v0.2.4-B). It is a
	// logical cross-context reference into the resource catalog, so it carries no
	// foreign key; the resource delete guard protects it instead.
	EnvironmentID int64
	// Stage is the deterministic stage-machine state (spec §5.3). Only the
	// stage machine writes it; a new version starts at StageCreated.
	Stage          Stage
	StageEnteredAt time.Time
	// Blocker is the user-visible reason recorded while the version is
	// suspended in blocked/paused; it is empty otherwise.
	Blocker string
	// SuspendedFrom is the workflow stage recorded when the version entered
	// blocked/paused. A resume can only return to this stage.
	SuspendedFrom *Stage
	ClosedAt      *time.Time
	CloseReason   string
	CloseRunID    *int64
	// ClosureKind classifies how the version closed (v0.2.4-G SC-G11):
	// ClosureKindNormal for a full-predicate close, ClosureKindWaived for an
	// admin waiver close. Unverified marks a waived close whose report carries
	// unverified acceptance criteria. Both default to normal/false; the waiver
	// write path sets them through the single Close terminal entry
	// (WaiverService.WaiveClose → VersionCloseService.CloseWaived), and reopen
	// clears them.
	ClosureKind string
	Unverified  bool
	CreatedBy   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate checks the editable version fields, including the mandatory
// environment binding.
func (v Version) Validate() error {
	if err := validation.String("title", v.Title, 1, MaxTitleLength); err != nil {
		return err
	}
	if v.EnvironmentID <= 0 {
		return platform.Invalid("environment_id", "must be a positive decimal ID")
	}
	switch v.Status {
	case "", StatusInitial, StatusRunning, StatusCompleted:
		return nil
	default:
		return platform.Invalid("status", "must be one of initial, running, completed")
	}
}
