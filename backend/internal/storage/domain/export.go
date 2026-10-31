package storage

import (
	"errors"
	"time"
)

// Export artifact owner kinds (spec §3.2). P2 only creates case exports; the
// other enum values are reserved by the frozen schema.
const (
	OwnerKindCase   = "case"
	OwnerKindReport = "report"
	OwnerKindChain  = "chain"
	// OwnerKindExport is the content_ref owner kind for export bytes.
	OwnerKindExport = "export"
)

// Export file formats.
const (
	FormatMarkdown = "markdown"
	FormatMermaid  = "mermaid"
	FormatOPML     = "opml"
	FormatCSV      = "csv"
	FormatHTML     = "html"
	FormatPDF      = "pdf"
)

// Export artifact statuses.
const (
	ExportStatusReady  = "ready"
	ExportStatusFailed = "failed"
)

// ErrDuplicate reports an export-artifact unique-constraint collision.
var ErrDuplicate = errors.New("storage: export artifact already exists")

// ExportArtifact is one export revision. The bytes live in the content ledger;
// ContentObjectID is the SHA-256 object key.
type ExportArtifact struct {
	ID              int64
	VersionID       int64
	OwnerID         int64
	OwnerKind       string
	Revision        int
	Format          string
	ContentObjectID string
	Size            int64
	MIME            string
	Filename        string
	Status          string
	CreatedBy       *int64
	CreatedAt       time.Time
}

// ValidExportOwnerKind reports whether kind is a schema owner kind.
func ValidExportOwnerKind(kind string) bool {
	switch kind {
	case OwnerKindCase, OwnerKindReport, OwnerKindChain:
		return true
	default:
		return false
	}
}

// ValidExportFormat reports whether format is a schema file format.
func ValidExportFormat(format string) bool {
	switch format {
	case FormatMarkdown, FormatMermaid, FormatOPML, FormatCSV, FormatHTML, FormatPDF:
		return true
	default:
		return false
	}
}

// ExportExtension returns the canonical filename extension for a format.
func ExportExtension(format string) string {
	switch format {
	case FormatMermaid:
		return ".mmd"
	case FormatOPML:
		return ".opml"
	case FormatCSV:
		return ".csv"
	case FormatHTML:
		return ".html"
	case FormatPDF:
		return ".pdf"
	default:
		return ".md"
	}
}

// ExportMIME returns the content type written for a format.
func ExportMIME(format string) string {
	switch format {
	case FormatMarkdown, FormatHTML, FormatCSV, FormatOPML:
		return "text/" + format + "; charset=utf-8"
	case FormatMermaid:
		return "text/plain; charset=utf-8"
	case FormatPDF:
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}
