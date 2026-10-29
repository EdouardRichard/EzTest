package application

import (
	"context"
	"strconv"
)

// formatID renders a positive id as the decimal string the audit target id
// column stores.
func formatID(id int64) string { return strconv.FormatInt(id, 10) }

// Resource write-path audit actions of spec §10.4. Each name is stable because
// it is persisted and filtered on. Action names follow the <context>.<verb>
// convention of the P0 audit log.
const (
	ActionSessionImported    = "resource.session_imported"
	ActionSessionValidated   = "resource.session_validated"
	ActionSessionConfirmed   = "resource.session_confirmed"
	ActionSessionInvalidated = "resource.session_invalidated"
	ActionSessionRevoked     = "resource.session_revoked"
	ActionTakeoverRequested  = "resource.takeover_requested"
	ActionTakeoverCompleted  = "resource.takeover_completed"
	ActionTakeoverCancelled  = "resource.takeover_cancelled"
)

// AuditTargetSession and AuditTargetTakeover are the audit target types of the
// Session lifecycle entries.
const (
	AuditTargetSession  = "session"
	AuditTargetTakeover = "takeover"
)

// AuditEvent is one resource write-path audit entry. It carries identifiers and
// a state/status only: session storage state, cookies, headers and manual
// takeover instructions are never part of it (spec §10.4).
type AuditEvent struct {
	Action     string
	TargetType string
	TargetID   string
	// ActorID is the authenticated user that performed the action. Zero means
	// "unknown"; the recorder adapter falls back to the request context actor.
	ActorID int64
	// Metadata holds non-sensitive details such as state, environment id and
	// takeover id.
	Metadata map[string]any
}

// AuditSink records resource write-path audit entries. A record failure is
// logged by the adapter/caller and never fails the business request.
type AuditSink interface {
	RecordResourceAudit(ctx context.Context, event AuditEvent) error
}
