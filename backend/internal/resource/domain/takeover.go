package domain

import (
	"encoding/json"
	"time"
)

// Takeover reasons, mirrored from the manual_takeovers.reason CHECK
// constraint.
const (
	TakeoverReasonMFA          = "mfa"
	TakeoverReasonCaptcha      = "captcha"
	TakeoverReasonApproval     = "approval"
	TakeoverReasonLoginUnknown = "login_unknown"
	TakeoverReasonOther        = "other"
)

// Takeover statuses, mirrored from the manual_takeovers.status CHECK
// constraint.
const (
	TakeoverStatusWaiting   = "waiting"
	TakeoverStatusCompleted = "completed"
	TakeoverStatusCancelled = "cancelled"
	TakeoverStatusExpired   = "expired"
)

// Takeover is a request for a human to complete a login that cannot be done
// automatically. SessionID is nil for a takeover requested before a Session
// exists. Instructions is opaque JSONB.
type Takeover struct {
	ID            int64
	SessionID     *int64
	AccountID     int64
	SystemID      int64
	EnvironmentID int64
	Reason        string
	Status        string
	Instructions  json.RawMessage
	RequestedBy   *int64
	RequestedAt   time.Time
	ExpiresAt     *time.Time
	CompletedAt   *time.Time
}

// TakeoverFilter selects a page of takeovers ordered by ID ascending.
type TakeoverFilter struct {
	Status string
	Page   Page
}
