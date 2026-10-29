// Package agentaudit adapts the audit context's recorder to the Agent runtime's
// audit port. Agent audits (tool invocations, run aborts, profile updates,
// session deletes) must survive the request or run being cancelled, so the
// adapter detaches the caller's cancellation and bounds the recorder call with a
// short timeout; a failed audit is logged and never fails the run or request
// (design spec §8.6).
package agentaudit

import (
	"context"
	"log/slog"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
)

// recordTimeout bounds one audit write so a slow or unreachable audit store can
// never hold the run's terminal path open.
const recordTimeout = 2 * time.Second

// Recorder is the slice of the audit use case the sink needs. The audit
// application Service satisfies it.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry) error
}

// Sink records Agent audit entries through the P0 audit log.
type Sink struct {
	recorder Recorder
}

// NewSink returns the audit sink. A nil recorder disables auditing (used by
// partially wired processes and tests).
func NewSink(recorder Recorder) *Sink {
	return &Sink{recorder: recorder}
}

// Record maps one Agent audit event onto an audit entry and records it. The
// recorder call runs with the caller's cancellation dropped and a 2 s timeout,
// so an audit produced while a run is aborting is still written. A recorder
// failure is logged and swallowed: auditing never blocks the caller.
func (s *Sink) Record(ctx context.Context, event agentapp.AuditEvent) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	entry := audit.Entry{
		ActorUserID: event.ActorUserID,
		Action:      event.Action,
		TargetType:  event.TargetType,
		TargetID:    event.TargetID,
		Metadata:    event.Metadata,
	}
	if meta := auditapp.MetaFromContext(ctx); meta != (auditapp.RequestMeta{}) {
		entry.RequestID = meta.RequestID
		entry.IP = meta.IP
		entry.UserAgent = meta.UserAgent
	}
	// Tool audits are recorded from the run goroutine, which carries the
	// request ID copied by RunService.Start but no HTTP request metadata.
	// Prefer the request-level values above when present; fall back to the run
	// context otherwise so every run-scoped entry stays correlated (T19).
	if entry.RequestID == "" {
		entry.RequestID = agentapp.RequestIDFromContext(ctx)
	}
	if err := s.recorder.Record(recordCtx, entry); err != nil {
		slog.ErrorContext(ctx, "agent: record audit entry",
			"action", event.Action, "target_type", event.TargetType, "target_id", event.TargetID, "error", err)
		return nil
	}
	return nil
}
