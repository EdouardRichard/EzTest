// Package audit adapts the P5 mindmap AuditRecorder port to the audit
// application recorder (spec §4.3/D30). The entry carries identifiers, counts
// and status only: mindmap bodies, LLM prompts/responses and export content are
// never recorded. P0 request metadata is stamped when the call happens inside an
// HTTP request.
package audit

import (
	"context"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// EntryRecorder is the narrow slice of the audit application recorder the
// adapter needs. *audit/application.Service satisfies it.
type EntryRecorder interface {
	Record(ctx context.Context, entry auditdomain.Entry) error
}

// Recorder is the mindmap AuditRecorder adapter over the audit application
// recorder.
type Recorder struct {
	recorder EntryRecorder
}

// NewAuditRecorder wires the adapter to the audit application recorder. A nil
// recorder disables auditing (used by partially wired processes and tests).
func NewAuditRecorder(recorder EntryRecorder) *Recorder {
	return &Recorder{recorder: recorder}
}

var _ mindmapapp.AuditRecorder = (*Recorder)(nil)

// Record appends one mindmap audit entry. The metadata map is copied so the
// persisted entry never aliases caller state, and identifier/status values keep
// the shape the caller supplied.
func (r *Recorder) Record(ctx context.Context, event mindmapapp.AuditEvent) error {
	if r == nil || r.recorder == nil {
		return nil
	}
	entry := auditdomain.Entry{
		Action:     event.Action,
		TargetType: event.TargetType,
		TargetID:   event.TargetID,
		Metadata:   cloneMetadata(event.Metadata),
	}
	if event.ActorID > 0 {
		actor := event.ActorID
		entry.ActorUserID = &actor
	}
	// Request correlation (P0): an entry recorded inside an HTTP request carries
	// that request's metadata; an async run worker keeps the request id copied
	// onto its context by the run coordinator.
	if meta := auditapp.MetaFromContext(ctx); meta != (auditapp.RequestMeta{}) {
		entry.RequestID = meta.RequestID
		entry.IP = meta.IP
		entry.UserAgent = meta.UserAgent
	}
	if entry.RequestID == "" {
		entry.RequestID = platform.RequestID(ctx)
	}
	return r.recorder.Record(ctx, entry)
}

// cloneMetadata copies the caller metadata into a fresh map. A nil map stays
// nil so the audit store applies its own default.
func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
