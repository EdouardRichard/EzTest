// Package audit adapts the codeintel AuditRecorder port to the audit
// application recorder (design spec §3.5/§6.10). It records code reads and
// analyses with identifiers and status only: code content, prompts, diffs and
// model responses are never part of an entry. Identifier keys are recorded as
// decimal strings (D29/INV-9, review R23) so a Snowflake id cannot lose
// precision in the persisted JSON.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// ActionCodeRead is the audit action of a file/search code read.
	ActionCodeRead = "code.read"
	// ActionCodeAnalyze is the audit action of an analysis trigger/completion.
	ActionCodeAnalyze = "code.analyze"

	// defaultTargetType is used when a caller supplies a subject without a
	// "<type>:<id>" prefix.
	defaultTargetType = "code"

	// maxExactFloat64 is the largest integer range a float64 holds exactly; a
	// decoded JSON number beyond it has already lost precision.
	maxExactFloat64 = 1 << 53
)

// allowedDetailKeys is the identifier/status allowlist of the audit metadata.
// Whatever else a caller puts in the detail map is dropped before the entry is
// recorded, so code content, diffs and prompts cannot reach the audit log. The
// commit/path/status keys are strings by contract and copied unchanged.
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

// decimalIDKeys are the allowlisted keys that carry an identifier. D29/INV-9
// require codeintel ids to be decimal strings in JSON; repoId is a JSON number
// in the P2 DTOs but is normalized here too, inside the audit metadata only, so
// the log never stores an id in a shape that a JSON consumer could round.
var decimalIDKeys = map[string]struct{}{
	"repoId":     {},
	"versionId":  {},
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
// Only the allowlisted identifier/status keys survive in the metadata, and
// identifier values are recorded as decimal strings.
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
	// Request correlation (P0): a code read/trigger recorded inside an HTTP
	// request carries that request's metadata. The async analysis worker carries
	// no HTTP metadata, but the trigger copies its request id into the run
	// context (AnalysisService.Run), so the completion entry stays correlated
	// (mirrors the Agent audit sink, T19).
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
// caller's map is neither aliased nor trusted. Identifier keys are normalized
// to decimal strings (D29/INV-9, review R23); commit/path/status values are
// copied unchanged.
func allowedMetadata(detail map[string]any) map[string]any {
	metadata := make(map[string]any, len(detail))
	for key, value := range detail {
		if _, ok := allowedDetailKeys[key]; !ok {
			continue
		}
		if _, ok := decimalIDKeys[key]; ok {
			id, ok := decimalID(value)
			if !ok {
				// A value that cannot be a decimal id is a caller bug; dropping
				// it keeps the log free of malformed identifier shapes.
				continue
			}
			metadata[key] = id
			continue
		}
		metadata[key] = value
	}
	return metadata
}

// decimalID renders an identifier as its decimal string. Callers pass either the
// int64 domain id or an already-formatted decimal string; a float64 (a decoded
// JSON number) is accepted only while it is an exact non-negative integer, which
// is the only range where converting it back to a string is lossless.
func decimalID(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return normalizeDecimalID(typed)
	case int64:
		if typed < 0 {
			return "", false
		}
		return strconv.FormatInt(typed, 10), true
	case int:
		if typed < 0 {
			return "", false
		}
		return strconv.Itoa(typed), true
	case json.Number:
		return normalizeDecimalID(typed.String())
	case float64:
		if typed < 0 || typed != math.Trunc(typed) || typed > maxExactFloat64 {
			return "", false
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return "", false
	}
}

// normalizeDecimalID accepts a non-empty unsigned decimal integer. The trimmed
// original is returned so a large Snowflake id is never reparsed through a
// float.
func normalizeDecimalID(value string) (string, bool) {
	id := strings.TrimSpace(value)
	if id == "" {
		return "", false
	}
	for _, digit := range id {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	return id, true
}
