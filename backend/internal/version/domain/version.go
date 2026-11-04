// Package version is the version/binding bounded context. A version pins a
// testable snapshot of one or more SUTs: every binding selects an environment,
// optional code repository, page/API scope, target databases and test accounts.
package version

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Version statuses. P0 only ever writes initial; running/completed belong to
// later phases.
const (
	StatusInitial   = "initial"
	StatusRunning   = "running"
	StatusCompleted = "completed"
)

// Binding cardinality and title limits.
const (
	MinBindings    = 1
	MaxBindings    = 20
	MaxTitleLength = 200
)

// Version is one version workspace.
type Version struct {
	ID                        int64
	Title                     string
	Status                    string
	AutoDesignNewRequirements bool
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
	CreatedBy     int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate checks the editable version fields.
func (v Version) Validate() error {
	if err := validation.String("title", v.Title, 1, MaxTitleLength); err != nil {
		return err
	}
	switch v.Status {
	case "", StatusInitial, StatusRunning, StatusCompleted:
		return nil
	default:
		return platform.Invalid("status", "must be one of initial, running, completed")
	}
}
