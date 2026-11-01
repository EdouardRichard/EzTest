package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ErrGCRunning reports that another gc_run is already in status 'running'.
// The database enforces this with the partial unique index uk_gc_run_running.
var ErrGCRunning = errors.New("storage: another gc run is already running")

// GCMode is the gc_run execution mode (spec §4.4). A dry run classifies and
// reports candidates; apply performs the deletions.
type GCMode string

const (
	// GCModeDryRun scans and writes the report only.
	GCModeDryRun GCMode = "dry_run"
	// GCModeApply deletes the reported candidates.
	GCModeApply GCMode = "apply"
)

// Valid reports whether the mode is a schema value.
func (m GCMode) Valid() bool { return m == GCModeDryRun || m == GCModeApply }

// RequiresConfirmation reports whether an API caller must pass confirm=true.
// Only apply destroys data; the HTTP handler enforces the flag.
func (m GCMode) RequiresConfirmation() bool { return m == GCModeApply }

// ValidGCMode reports whether mode is a schema value.
func ValidGCMode(mode GCMode) bool { return mode.Valid() }

// ParseGCMode trims and validates an API mode value.
func ParseGCMode(raw string) (GCMode, error) {
	mode := GCMode(strings.TrimSpace(raw))
	if !mode.Valid() {
		return "", platform.Invalid("mode", "mode must be dry_run or apply")
	}
	return mode, nil
}

// GCRunStatus is the gc_run lifecycle state.
type GCRunStatus string

const (
	// GCRunRunning is set while a run scans and applies candidates.
	GCRunRunning GCRunStatus = "running"
	// GCRunCompleted is set once a run finished its work.
	GCRunCompleted GCRunStatus = "completed"
	// GCRunFailed is set when a run could not finish.
	GCRunFailed GCRunStatus = "failed"
)

// ValidGCRunStatus reports whether status is a schema value.
func ValidGCRunStatus(status GCRunStatus) bool {
	switch status {
	case GCRunRunning, GCRunCompleted, GCRunFailed:
		return true
	default:
		return false
	}
}

// GCActorKind identifies who triggered a run: an admin user or the periodic
// system worker.
type GCActorKind string

const (
	// GCActorUser marks a manual admin run.
	GCActorUser GCActorKind = "user"
	// GCActorSystem marks the leader-scheduled worker.
	GCActorSystem GCActorKind = "system"
)

// ValidGCActorKind reports whether kind is a schema value.
func ValidGCActorKind(kind GCActorKind) bool {
	return kind == GCActorUser || kind == GCActorSystem
}

// GC candidate classes (spec §4.4). The values are shared with
// gc_tombstone.reason.
const (
	// GCCandidateUnreferenced is a content_object without any live content_ref.
	GCCandidateUnreferenced = "unreferenced"
	// GCCandidateTemp is an orphan file on disk with no content_object row.
	GCCandidateTemp = "temp"
	// GCCandidateSupersededExport is an export_artifact replaced by a newer
	// revision of the same (version, owner_kind, owner_id, file_format) tuple.
	GCCandidateSupersededExport = "superseded_export"
	// GCCandidateOrphanFile is a live content_ref whose owning entity is gone.
	GCCandidateOrphanFile = "orphan_file"
)

// ValidGCCandidateClass reports whether class is a GC candidate class.
func ValidGCCandidateClass(class string) bool {
	switch class {
	case GCCandidateUnreferenced, GCCandidateTemp, GCCandidateSupersededExport, GCCandidateOrphanFile:
		return true
	default:
		return false
	}
}

// GCCandidate is one object/file selected by a GC pass. RefID, OwnerKind,
// OwnerID and ExportArtifactID are zero for candidates that have no reference.
// DeleteObject records whether the candidate also removes the content_object
// row and the file (only after the reference ledger says it is safe).
type GCCandidate struct {
	ObjectSHA        string
	Size             int64
	MIME             string
	RelativePath     string
	Class            string
	RefID            int64
	OwnerKind        string
	OwnerID          int64
	ExportArtifactID int64
	AgeSeconds       int64
	DeleteObject     bool
	DeletionReason   string
}

// Validate enforces the candidate invariants the deletion pipeline relies on.
func (c GCCandidate) Validate() error {
	if !ValidSHA256(c.ObjectSHA) {
		return platform.Invalid("object_sha", "object_sha must be 64 lowercase hex characters")
	}
	if c.Size < 0 {
		return platform.Invalid("size", "size must not be negative")
	}
	if strings.TrimSpace(c.RelativePath) == "" {
		return platform.Invalid("relative_path", "relative_path is required")
	}
	if strings.TrimSpace(c.MIME) == "" {
		return platform.Invalid("mime", "mime is required")
	}
	if !ValidGCCandidateClass(c.Class) {
		return platform.Invalid("class", "class must be one of unreferenced, temp, superseded_export, orphan_file")
	}
	if c.AgeSeconds < 0 {
		return platform.Invalid("age_seconds", "age_seconds must not be negative")
	}
	if !ValidGCCandidateClass(c.DeletionReason) {
		return platform.Invalid("deletion_reason", "deletion_reason must be a candidate class")
	}
	switch c.Class {
	case GCCandidateUnreferenced:
		if c.RefID != 0 || c.ExportArtifactID != 0 {
			return platform.Invalid("ref_id", "unreferenced candidates must not carry refs")
		}
	case GCCandidateTemp:
		if c.RefID != 0 || c.ExportArtifactID != 0 {
			return platform.Invalid("ref_id", "temp candidates must not carry refs")
		}
	case GCCandidateSupersededExport:
		if c.RefID <= 0 || c.ExportArtifactID <= 0 {
			return platform.Invalid("ref_id", "superseded_export candidates require a ref and an export artifact")
		}
	case GCCandidateOrphanFile:
		if c.RefID <= 0 || strings.TrimSpace(c.OwnerKind) == "" || c.OwnerID <= 0 {
			return platform.Invalid("ref_id", "orphan_file candidates require ref, owner_kind and owner_id")
		}
	}
	return nil
}

// GCRun is one gc_run row. Report is the JSONB candidate/deletion report.
type GCRun struct {
	ID             int64
	Mode           GCMode
	Status         GCRunStatus
	ActorID        *int64
	ActorKind      GCActorKind
	StartedAt      time.Time
	FinishedAt     *time.Time
	ScannedObjects int
	ScannedRefs    int
	DeletedObjects int
	DeletedRefs    int
	DeletedBytes   int64
	Report         json.RawMessage
	Error          string
}

// Validate enforces the run invariants shared by the repository and the API.
func (r GCRun) Validate() error {
	if r.ID <= 0 {
		return platform.Invalid("id", "gc run id must be a positive decimal ID")
	}
	if !r.Mode.Valid() {
		return platform.Invalid("mode", "mode must be dry_run or apply")
	}
	if !ValidGCRunStatus(r.Status) {
		return platform.Invalid("status", "status must be running, completed or failed")
	}
	if !ValidGCActorKind(r.ActorKind) {
		return platform.Invalid("actor_kind", "actor_kind must be user or system")
	}
	if r.StartedAt.IsZero() {
		return platform.Invalid("started_at", "started_at is required")
	}
	if r.ScannedObjects < 0 || r.ScannedRefs < 0 || r.DeletedObjects < 0 || r.DeletedRefs < 0 || r.DeletedBytes < 0 {
		return platform.Invalid("counters", "gc counters must not be negative")
	}
	if len(r.Report) > 0 {
		trimmed := strings.TrimSpace(string(r.Report))
		if !strings.HasPrefix(trimmed, "{") {
			return platform.Invalid("report", "report must be a JSON object")
		}
	}
	if r.Error != "" && len(r.Error) > 4000 {
		return platform.Invalid("error", "error must be at most 4000 characters")
	}
	return nil
}

// IsTerminal reports whether the run reached completed or failed.
func (r GCRun) IsTerminal() bool { return r.Status == GCRunCompleted || r.Status == GCRunFailed }

// GCTombstone is one deleted object's immutable record. It is written before
// the content_object row and the file are removed (spec §4.4).
type GCTombstone struct {
	ID           int64
	RunID        int64
	ObjectSHA    string
	Size         int64
	MIME         string
	RelativePath string
	Reason       string
	DeletedAt    time.Time
}

// Validate enforces the gc_tombstone column invariants.
func (t GCTombstone) Validate() error {
	if t.ID <= 0 {
		return platform.Invalid("id", "tombstone id must be a positive decimal ID")
	}
	if t.RunID <= 0 {
		return platform.Invalid("run_id", "run_id must be a positive decimal ID")
	}
	if !ValidSHA256(t.ObjectSHA) {
		return platform.Invalid("object_sha", "object_sha must be 64 lowercase hex characters")
	}
	if t.Size < 0 {
		return platform.Invalid("size", "size must not be negative")
	}
	if strings.TrimSpace(t.MIME) == "" {
		return platform.Invalid("mime", "mime is required")
	}
	if strings.TrimSpace(t.RelativePath) == "" {
		return platform.Invalid("relative_path", "relative_path is required")
	}
	if !ValidGCCandidateClass(t.Reason) {
		return platform.Invalid("reason", "reason must be a candidate class")
	}
	if t.DeletedAt.IsZero() {
		return platform.Invalid("deleted_at", "deleted_at is required")
	}
	return nil
}

// ValidSHA256 reports whether value is a lowercase hex SHA-256. It mirrors the
// application-level helper so the domain can validate candidates without an
// import cycle.
func ValidSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, digit := range value {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}
