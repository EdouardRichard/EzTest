package domain

import "time"

// Lease is an exclusive, time-bounded right to use a Session. At most one
// active lease (released_at IS NULL) exists per Session, enforced by the
// uq_session_active_lease partial unique index. Leases are application-level
// only; P2 exposes no HTTP endpoint for them.
type Lease struct {
	ID             int64
	SessionID      int64
	Holder         string
	AcquiredAt     time.Time
	ExpiresAt      time.Time
	ReleasedAt     *time.Time
	ReleasedReason string
}
