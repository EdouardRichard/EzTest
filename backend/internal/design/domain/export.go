package design

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Export formats supported by P2 case exports.
const (
	ExportFormatMarkdown = "markdown"
	ExportFormatMermaid  = "mermaid"
	ExportFormatOPML     = "opml"
	ExportFormatCSV      = "csv"
)

// Export format limits.
const (
	ExportMermaidLabelMax = 60
)

// ValidExportFormat reports whether format is a P2 case export format.
func ValidExportFormat(format string) bool {
	switch format {
	case ExportFormatMarkdown, ExportFormatMermaid, ExportFormatOPML, ExportFormatCSV:
		return true
	default:
		return false
	}
}

// ExportMIME returns the content type written for a format.
func ExportMIME(format string) string {
	switch format {
	case ExportFormatMarkdown:
		return "text/markdown; charset=utf-8"
	case ExportFormatMermaid:
		return "text/plain; charset=utf-8"
	case ExportFormatOPML:
		return "text/x-opml; charset=utf-8"
	case ExportFormatCSV:
		return "text/csv; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// ExportExtension returns the filename extension for a format.
func ExportExtension(format string) string {
	switch format {
	case ExportFormatMermaid:
		return ".mmd"
	case ExportFormatOPML:
		return ".opml"
	case ExportFormatCSV:
		return ".csv"
	default:
		return ".md"
	}
}

// ExportData is the complete current-version design snapshot handed to the
// format renderers. Superseded versions never appear.
type ExportData struct {
	VersionID    int64
	VersionTitle string
	GeneratedAt  time.Time
	Requirements []ExportRequirement
}

// ExportRequirement is one requirement node of the export scope.
type ExportRequirement struct {
	ID                   int64
	RequirementVersionID int64
	Revision             int
	Title                string
	Status               string
	Scenarios            []ExportScenario
}

// ExportScenario is one scenario node of the export scope.
//
// v0.2.4-B (§3.2): the resource ID arrays are gone with the scenario fields
// they projected.
type ExportScenario struct {
	ID          int64
	Name        string
	Description string
	Cases       []ExportCase
}

// ExportCase is one current case version with its immutable children.
//
// v0.2.4-B (§3.5 "design export"): the exports describe the system under test
// in natural language and list the acceptance criteria the case covers, instead
// of naming default/version SUT and page/API ids.
type ExportCase struct {
	CaseID         int64
	VersionID      int64
	Revision       int
	Title          string
	Description    string
	SutDescription string
	Preconditions  string
	CoveredACs     []string
	TestData       json.RawMessage
	Priority       string
	Status         string
	CreatedAt      time.Time
	Steps          []CaseStep
	Assertions     []CaseAssertion
	Sources        []CaseSource
}

// RenderExport renders one format. It is a pure function: identical ExportData
// always yields identical bytes.
func RenderExport(data ExportData, format string) ([]byte, error) {
	switch format {
	case ExportFormatMarkdown:
		return RenderMarkdown(data)
	case ExportFormatMermaid:
		return RenderMermaid(data)
	case ExportFormatOPML:
		return RenderOPML(data)
	case ExportFormatCSV:
		return RenderCSV(data)
	default:
		return nil, fmt.Errorf("design: unsupported export format %q", format)
	}
}

// Export format name used in renderer errors.
func exportFormatName(format string) string {
	return format
}

// exportDash is the "nothing here" cell of the markdown/CSV renderers.
const exportDash = "-"

// exportText renders one natural-language contract field, falling back to the
// dash the renderers have always used for an absent value. It replaces
// formatOptionalID: the v0.2.4-B exports carry prose, so the empty case is an
// empty string rather than a nil pointer.
func exportText(value string) string {
	if strings.TrimSpace(value) == "" {
		return exportDash
	}
	return value
}

// formatACList renders a covered_acs set as a comma-separated id list. The
// order is the stored (already normalized, sorted) order so the export is
// byte-stable for identical content.
func formatACList(values []string) string {
	if len(values) == 0 {
		return exportDash
	}
	return strings.Join(values, ", ")
}

// exportContext carries the renderer-agnostic header lines.
type exportContext struct {
	Title       string
	GeneratedAt string
}

func newExportContext(data ExportData) exportContext {
	return exportContext{
		Title:       data.VersionTitle,
		GeneratedAt: data.GeneratedAt.UTC().Format(time.RFC3339),
	}
}
