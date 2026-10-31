// Package audit is the audit context's domain model: append-only entries that
// record who did what, to which target, and when. Entries never carry
// credentials; the services that create them exclude passwords, token hashes,
// keys and header values.
package audit

import "time"

// Entry is one audit-log record. The application assigns ID and CreatedAt
// before recording; the repository stores the remaining fields as given.
type Entry struct {
	// ID is the snowflake identifier and the pagination cursor.
	ID int64
	// ActorID is the authenticated user that performed the action. It is nil
	// for anonymous actions such as a failed login.
	ActorID *int64
	// Action names what happened, for example "auth.login".
	Action string
	// TargetKind and TargetID identify the object the action applied to.
	// TargetID is always the decimal string form of a business ID.
	TargetKind string
	TargetID   string
	// RequestID, IP and UserAgent capture the originating request when known.
	RequestID string
	IP        string
	UserAgent string
	// Metadata holds non-sensitive action details. It must never contain
	// passwords, tokens, hashes, keys or header values.
	Metadata map[string]any
	// CreatedAt is the write time in UTC.
	CreatedAt time.Time
}
