package design

import (
	"encoding/json"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Design round activation reasons (spec §3.1 design_round.activation).
const (
	ActivationManual     = "manual"
	ActivationAutoGap    = "auto_gap"
	ActivationAutoChange = "auto_change"
	ActivationAutoNew    = "auto_new"
)

// Design round statuses. P2 only writes active; completed is reserved.
const (
	RoundStatusActive    = "active"
	RoundStatusCompleted = "completed"
)

// Design scope statuses. P2 only writes active; closed is reserved.
const (
	ScopeStatusActive = "active"
	ScopeStatusClosed = "closed"
)

// Round is one design-round row. P2 keeps every round active and treats the
// newest design conversation as the current round.
type Round struct {
	ID                    int64
	VersionID             int64
	ConversationID        int64
	Activation            string
	RequirementIDs        []int64
	RequirementVersionIDs []int64
	Scope                 json.RawMessage
	Status                string
	CreatedBy             *int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// Scope is one activated requirement of a version.
type Scope struct {
	ID                 int64
	VersionID          int64
	RequirementID      int64
	ActivatedByRoundID *int64
	ActivatedAt        time.Time
	Status             string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ValidateActivation reports whether activation is a P2 activation reason.
func ValidateActivation(activation string) error {
	switch activation {
	case ActivationManual, ActivationAutoGap, ActivationAutoChange, ActivationAutoNew:
		return nil
	default:
		return platform.Invalid("activation", "must be one of manual, auto_gap, auto_change, auto_new")
	}
}
