// Package audit adapts the resource context's db-target audit event to the P0
// audit recorder (spec §11.2). Entries carry the environment/target ids, host,
// database and outcome only: the password and the connection string are never
// part of them.
package audit

import (
	"context"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// Recorder is the narrow slice of the P0 audit recorder the sink needs.
// *audit/application.Service satisfies it.
type Recorder interface {
	Record(ctx context.Context, entry auditdomain.Entry) error
}

// Sink records resource db-target audit entries through the P0 audit log.
type Sink struct {
	recorder Recorder
}

// NewSink returns the db-target audit sink. A nil recorder disables auditing.
func NewSink(recorder Recorder) *Sink {
	return &Sink{recorder: recorder}
}

var (
	_ resourceapp.DBTargetAuditSink = (*Sink)(nil)
	_ resourceapp.AuditSink         = (*Sink)(nil)
	_ Recorder                      = (*auditapp.Service)(nil)
)

// RecordResourceAudit implements resource/application.AuditSink. It records one
// Session lifecycle entry: ids, state and environment id only, never the stored
// login state, cookies or takeover instructions (spec §10.4).
func (s *Sink) RecordResourceAudit(ctx context.Context, event resourceapp.AuditEvent) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	metadata := make(map[string]any, len(event.Metadata))
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	entry := auditdomain.Entry{
		Action:     event.Action,
		TargetType: event.TargetType,
		TargetID:   event.TargetID,
		Metadata:   metadata,
	}
	if event.ActorID > 0 {
		actorID := event.ActorID
		entry.ActorUserID = &actorID
	} else if actor, ok := auditapp.ActorIDFromContext(ctx); ok && actor > 0 {
		actorID := actor
		entry.ActorUserID = &actorID
	}
	if meta := auditapp.MetaFromContext(ctx); meta != (auditapp.RequestMeta{}) {
		entry.RequestID = meta.RequestID
		entry.IP = meta.IP
		entry.UserAgent = meta.UserAgent
	}
	if entry.RequestID == "" {
		entry.RequestID = platform.RequestID(ctx)
	}
	return s.recorder.Record(ctx, entry)
}

// RecordDBTargetAudit implements resource/application.DBTargetAuditSink.
func (s *Sink) RecordDBTargetAudit(ctx context.Context, event resourceapp.DBTargetAuditEvent) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	targetType := event.TargetType
	if targetType == "" {
		targetType = resourceapp.DBTargetAuditTargetType
	}
	entry := auditdomain.Entry{
		Action:     event.Action,
		TargetType: targetType,
		TargetID:   event.TargetID,
		Metadata:   event.Metadata,
	}
	if event.ActorID > 0 {
		actorID := event.ActorID
		entry.ActorUserID = &actorID
	} else if actor, ok := auditapp.ActorIDFromContext(ctx); ok && actor > 0 {
		actorID := actor
		entry.ActorUserID = &actorID
	}
	if meta := auditapp.MetaFromContext(ctx); meta != (auditapp.RequestMeta{}) {
		entry.RequestID = meta.RequestID
		entry.IP = meta.IP
		entry.UserAgent = meta.UserAgent
	}
	if entry.RequestID == "" {
		entry.RequestID = platform.RequestID(ctx)
	}
	return s.recorder.Record(ctx, entry)
}
