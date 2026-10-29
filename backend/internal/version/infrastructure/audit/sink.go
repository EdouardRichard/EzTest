// Package audit adapts the version context's lifecycle audit events to the P0
// audit recorder (spec §11.2/D52). Entries carry ids, statuses and the continue
// phase only: never a request payload.
package audit

import (
	"context"
	"strconv"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// versionTargetType is the audit target type of a version lifecycle entry.
const versionTargetType = "version"

// Recorder is the narrow slice of the P0 audit recorder the sink needs.
// *audit/application.Service satisfies it.
type Recorder interface {
	Record(ctx context.Context, entry auditdomain.Entry) error
}

// Sink records version lifecycle audit entries through the P0 audit log.
type Sink struct {
	recorder Recorder
}

// NewSink returns the version audit sink. A nil recorder disables auditing.
func NewSink(recorder Recorder) *Sink {
	return &Sink{recorder: recorder}
}

var (
	_ versionapp.VersionAuditSink = (*Sink)(nil)
	_ Recorder                    = (*auditapp.Service)(nil)
)

// Record implements version/application.VersionAuditSink.
func (s *Sink) Record(ctx context.Context, event versionapp.VersionAuditEvent) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	metadata := make(map[string]any, len(event.Metadata)+3)
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	if event.From != "" || event.To != "" {
		metadata["from"] = event.From
		metadata["to"] = event.To
	}
	if event.Phase != "" {
		metadata["phase"] = event.Phase
	}
	targetType := event.TargetType
	if targetType == "" {
		targetType = versionTargetType
	}
	targetID := event.TargetID
	if targetID == "" {
		targetID = strconv.FormatInt(event.VersionID, 10)
	}
	// An aggregate write (e.g. a requirement) targets the aggregate id, so the
	// owning version would otherwise be lost: it must stay traceable even after
	// the aggregate row is deleted (spec §10.4). A caller-supplied version_id
	// wins; a version-targeted entry already names the version in target_id.
	if targetType != versionTargetType && event.VersionID > 0 {
		if _, exists := metadata["version_id"]; !exists {
			metadata["version_id"] = event.VersionID
		}
	}
	entry := auditdomain.Entry{
		Action:     event.Action,
		TargetType: targetType,
		TargetID:   targetID,
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
