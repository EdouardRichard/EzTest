// Package auditapp defines the audit recording port shared by the other
// bounded contexts. The concrete service lives in this package and is wired by
// cmd/eztest.
package auditapp

import "context"

// Event is one audit entry as submitted by a caller. The service fills the ID
// and timestamp.
type Event struct {
	ActorID    *int64
	Action     string
	TargetKind string
	TargetID   string
	RequestID  string
	IP         string
	UserAgent  string
	Metadata   map[string]any
}

// Recorder is the best-effort audit sink. Implementations must not fail the
// caller's business operation when recording fails.
type Recorder interface {
	Record(ctx context.Context, event Event) error
}
