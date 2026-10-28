package domain

import (
	"encoding/json"
	"time"
)

// Session states, mirrored from the session.state CHECK constraint. State
// transitions are CAS updates on (id, state); see spec 搂6.1.
const (
	StatePending     = "pending"
	StateAcquiring   = "acquiring"
	StateNeedsManual = "needs_manual"
	StateValid       = "valid"
	StateRefreshing  = "refreshing"
	StateExpired     = "expired"
	StateInvalid     = "invalid"
	StateRevoked     = "revoked"
	StateFailed      = "failed"
)

// Session acquisition modes, mirrored from the session.acquisition CHECK
// constraint.
const (
	AcquisitionAuto     = "auto"
	AcquisitionManual   = "manual"
	AcquisitionImported = "imported"
)

// Storage state kinds stored in session.storage_state_kind.
const (
	StorageStateKindStorageState = "storage_state"
	StorageStateKindToken        = "token"
)

// sessionExpiringFloor is the minimum "about to expire" window (spec 搂6.1).
const sessionExpiringFloor = 5 * time.Minute

// Session is one imported login state for an Account on a System/Environment.
// StorageState is plaintext JSONB by decision D28; it is never serialized by
// the HTTP layer. ExpiresAt is nil only for a Session whose state has not been
// computed yet.
type Session struct {
	ID                int64
	AccountID         int64
	SystemID          int64
	EnvironmentID     int64
	AccessID          int64
	State             string
	Acquisition       string
	StorageState      json.RawMessage
	StorageStateHash  string
	StorageStateKind  string
	CookieCount       int
	OriginCount       int
	ExpiresAt         *time.Time
	LastValidatedAt   *time.Time
	LastUsedAt        *time.Time
	FailureReason     string
	InvalidatedReason string
	CreatedBy         *int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Expiring reports whether a valid session is close to expiry: the remaining
// lifetime is below max(10% of expires_at-created_at, 5 minutes).
func (s Session) Expiring(now time.Time) bool {
	if s.State != StateValid || s.ExpiresAt == nil {
		return false
	}
	threshold := s.ExpiresAt.Sub(s.CreatedAt) / 10
	if threshold < sessionExpiringFloor {
		threshold = sessionExpiringFloor
	}
	return s.ExpiresAt.Sub(now) < threshold
}

// IsExpired reports whether the session's expires_at is at or before now. The
// caller decides whether the state machine should lazily move to expired.
func (s Session) IsExpired(now time.Time) bool {
	return s.ExpiresAt != nil && !now.Before(*s.ExpiresAt)
}

// SessionFilter selects a page of sessions ordered by ID ascending. A zero
// value in any of the ID fields means "any".
type SessionFilter struct {
	AccountID     int64
	SystemID      int64
	EnvironmentID int64
	AccessID      int64
	State         string
	Page          Page
}
