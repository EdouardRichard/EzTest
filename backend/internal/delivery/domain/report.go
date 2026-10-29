// Package domain is the delivery context's model: issues, reports and analysis
// runs. The report snapshot is the deterministic, secret-free projection of a
// batch (spec §5.5); every id is stored as a decimal string because the snapshot
// crosses the JSON boundary (D29).
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Report schema and statuses of spec §4.2.2/§5.5.
const (
	// ReportSchemaV1 is the only content schema understood by P7.
	ReportSchemaV1 = "eztest.report.v1"

	// ReportStatusDraft is an unpublished manual report.
	ReportStatusDraft = "draft"
	// ReportStatusPublished is a generated, downloadable report.
	ReportStatusPublished = "published"
	// ReportStatusStale is a published report whose inputs changed.
	ReportStatusStale = "stale"
)

// Report reason constants of spec §7.11 (details.reason in the HTTP envelope).
const (
	ReportReasonNotFound        = "report_not_found"
	ReportReasonExportFailed    = "report_export_failed"
	ReportReasonNotPublishable  = "report_not_publishable"
	ReportReasonStale           = "report_stale"
	ReportReasonVersionMismatch = "report_version_mismatch"
	ReportReasonInvalidFilter   = "invalid_report_filter"
)

// Report errors shared by the application and infrastructure layers. The
// application maps them to a domain.Error (and thus to a stable HTTP reason).
var (
	// ErrReportNotFound reports a report that does not exist in the version.
	ErrReportNotFound = errors.New("delivery: report not found")
	// ErrReportRevisionConflict reports a lost (batch_id, revision) uniqueness
	// race. Ensure re-reads the winning row instead of failing.
	ErrReportRevisionConflict = errors.New("delivery: report revision conflict")
)

// reportFingerprintPattern is the lowercase hex sha256 format enforced by the
// report DDL for content fingerprints and file digests.
var reportFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IsValidReportStatus reports whether status is one of the report states.
func IsValidReportStatus(status string) bool {
	switch status {
	case ReportStatusDraft, ReportStatusPublished, ReportStatusStale:
		return true
	default:
		return false
	}
}

// Report is one immutable revision of the report of a batch (spec §4.2.2). The
// Markdown artifact lives outside the database: the row only carries the
// root-relative path, the sha256 and the size (D30).
type Report struct {
	ID               int64
	VersionID        int64
	BatchID          int64
	Revision         int
	Status           string
	Title            string
	Content          ReportContent
	InputFingerprint string
	MarkdownPath     string
	MarkdownSHA256   string
	MarkdownSize     int64
	StaleReason      string
	GeneratedBy      *int64
	PublishedAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validate checks the row invariants of spec §4.2.2/§5.5. A list projection
// carries no Content and no Markdown metadata; those fields are optional.
func (r Report) Validate() error {
	if r.ID <= 0 {
		return fmt.Errorf("report id must be positive")
	}
	if r.VersionID <= 0 {
		return fmt.Errorf("report version id must be positive")
	}
	if r.BatchID <= 0 {
		return fmt.Errorf("report batch id must be positive")
	}
	if r.Revision < 1 {
		return fmt.Errorf("report revision must be at least 1")
	}
	if !IsValidReportStatus(r.Status) {
		return fmt.Errorf("report status %q is invalid", r.Status)
	}
	if !reportFingerprintPattern.MatchString(r.InputFingerprint) {
		return fmt.Errorf("report input fingerprint must be a lowercase sha256")
	}
	if r.MarkdownPath != "" {
		if !reportFingerprintPattern.MatchString(r.MarkdownSHA256) {
			return fmt.Errorf("report markdown sha256 must be a lowercase sha256")
		}
		if r.MarkdownSize < 0 {
			return fmt.Errorf("report markdown size must not be negative")
		}
	} else if r.MarkdownSHA256 != "" || r.MarkdownSize != 0 {
		return fmt.Errorf("report without markdown path must not carry markdown metadata")
	}
	if r.Content.Schema != "" {
		if err := r.Content.Validate(); err != nil {
			return fmt.Errorf("report content: %w", err)
		}
	}
	return nil
}

// ReportExport is the verified Markdown download of one report revision. It
// never exposes the filesystem path; the bytes are already checked against
// MarkdownSHA256 and Size.
type ReportExport struct {
	ReportID  int64
	VersionID int64
	BatchID   int64
	Revision  int
	Content   []byte
	SHA256    string
	Size      int64
	Filename  string
}
