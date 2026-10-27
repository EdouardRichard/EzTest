// Package audit adapts the codeintel AuditRecorder port to the audit
// application recorder (design spec §3.5/§6.10). It records code reads and
// analyses with identifiers and status only: code content, prompts, diffs and
// model responses are never part of an entry.
package audit

import (
	"context"
	"fmt"
	"strings"

	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
)

const (
	// ActionCodeRead is the audit action of a file/search code read.
	ActionCodeRead = "code.read"
	// ActionCodeAnalyze is the audit action of an analysis trigger/completion.
	ActionCodeAnalyze = "code.analyze"

	// defaultTargetType is used when a caller supplies a subject without a
	// "<type>:<id>" prefix.
	defaultTargetType = "code"
)

// allowedDetailKeys is the identifier/status allowlist of the audit metadata.
// Whatever else a caller puts in the detail map is dropped before the entry is
// recorded, so code content, diffs and prompts cannot reach the audit log.
var allowedDetailKeys = map[string]struct{}{
	"repoId":     {},
	"versionId":  {},
	"commit":     {},
	"baseCommit": {},
	"headCommit": {},
	"path":       {},
	"status":     {},
	"analysisId": {},
}

// EntryRecorder is the narrow slice of the audit application recorder the
// adapter needs. *audit/application.Service satisfies it.
type EntryRecorder interface {
	Record(ctx context.Context, entry auditdomain.Entry) error
}

// Recorder is the codeintel AuditRecorder adapter over the audit application
// recorder.
type Recorder struct {
	recorder EntryRecorder
}

// NewRecorder wires the adapter to the audit application recorder. A nil
// recorder disables auditing (used by partially wired processes and tests).
func NewRecorder(recorder EntryRecorder) *Recorder {
	return &Recorder{recorder: recorder}
}

var _ codeintelapp.AuditRecorder = (*Recorder)(nil)

// Record appends one code audit entry. The subject is "<targetType>:<targetID>";
// without a colon the whole subject is the target id of the generic "code" type.
// Only the allowlisted identifier/status keys survive in the metadata.
func (r *Recorder) Record(ctx context.Context, actorID int64, action, subject string, detail map[string]any) error {
	if action != ActionCodeRead && action != ActionCodeAnalyze {
		return fmt.Errorf("codeintel: unsupported audit action %q", action)
	}
	if r == nil || r.recorder == nil {
		return nil
	}

	targetType, targetID := splitSubject(subject)
	entry := auditdomain.Entry{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Metadata:   allowedMetadata(detail),
	}
	if actorID > 0 {
		actor := actorID
		entry.ActorUserID = &actor
	}
	return r.recorder.Record(ctx, entry)
}

// splitSubject maps the port's single subject string onto the audit entry's
// target pair. The convention is "<targetType>:<targetID>"; a subject without a
// colon is the id of the generic code target.
func splitSubject(subject string) (string, string) {
	prefix, rest, ok := strings.Cut(subject, ":")
	if !ok {
		return defaultTargetType, subject
	}
	if prefix == "" {
		return defaultTargetType, rest
	}
	return prefix, rest
}

// allowedMetadata copies the allowlisted detail keys into a fresh map so the
// caller's map is neither aliased nor trusted.
func allowedMetadata(detail map[string]any) map[string]any {
	metadata := make(map[string]any, len(detail))
	for key, value := range detail {
		if _, ok := allowedDetailKeys[key]; ok {
			metadata[key] = value
		}
	}
	return metadata
}
