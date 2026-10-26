// Package audit is the audit context's domain model: immutable entries that
// record who did what, to which target, and when. Entries never carry
// credentials: passwords, tokens, keys and header values are excluded by the
// services that create them.
package audit

import "time"

// Entry is one audit-log record. ID and CreatedAt are assigned by the
// repository; callers leave them zero when recording.
type Entry struct {
	// ID is the database-assigned identifier and the pagination cursor.
	ID int64
	// ActorUserID is the authenticated user that performed the action. It is
	// nil for anonymous actions such as a failed login.
	ActorUserID *int64
	// Action names what happened, for example "auth.login_succeeded".
	Action string
	// TargetType and TargetID identify the object the action applied to.
	TargetType string
	TargetID   string
	// RequestID, IP and UserAgent capture the originating request when it is
	// known. They are empty for actions without an HTTP request.
	RequestID string
	IP        string
	UserAgent string
	// Metadata holds non-sensitive action details such as a username, role or
	// status. It must never contain passwords, tokens, hashes, keys or header
	// values.
	Metadata map[string]any
	// CreatedAt is the database write time (UTC).
	CreatedAt time.Time
}
