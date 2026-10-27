package domain

import "time"

// Session is one agent conversation. ProfileKey is filled by the repositories
// so the API can expose the profile without a second lookup; it is a read-time
// projection, not a column of agent_sessions.
type Session struct {
	ID             int64
	ProfileID      int64
	ProfileKey     string
	Title          string
	CreatedBy      *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActivityAt time.Time
}

// SessionSummary is one row of the session list: the session plus its active
// (running) run and its most recent run, either of which may be nil.
type SessionSummary struct {
	Session   Session
	ActiveRun *Run
	LastRun   *Run
}

// SessionDetail is the single-session read model: running is a convenience
// derived from ActiveRun for the API's `running` field.
type SessionDetail struct {
	Session   Session
	Running   bool
	ActiveRun *Run
	LastRun   *Run
}
