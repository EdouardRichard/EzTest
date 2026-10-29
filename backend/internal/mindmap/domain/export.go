package domain

import "time"

// Export formats, mirrored from the case_export.format CHECK constraint. PNG is
// rendered client-side and never stored.
const (
	ExportMarkdown = "markdown"
	ExportMermaid  = "mermaid"
	ExportOPML     = "opml"
)

// CaseExport is one server-generated export file metadata row. The file itself
// lives on the filesystem (D30): only path, sha256 and size are stored. IDs are
// application-generated Snowflakes.
type CaseExport struct {
	ID         int64
	VersionID  int64
	Format     string
	FilePath   string
	FileSHA256 string
	SizeBytes  int64
	BaselineID *int64
	NodeCount  int
	CreatedBy  int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// Reused marks a row that ExportService.Create returned from the
	// baseline-bound reuse lookup instead of writing it. It is transport
	// metadata only (the HTTP layer answers 200 instead of 201 for a reuse,
	// spec §7.2 endpoint 25) and is never persisted or serialized.
	Reused bool `json:"-"`
}
