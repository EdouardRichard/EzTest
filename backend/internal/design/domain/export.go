package design

import (
	"encoding/json"
	"fmt"
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
	TargetVersionSUTIDs  []int64
	Scenarios            []ExportScenario
}

// ExportScenario is one scenario node of the export scope.
type ExportScenario struct {
	ID            int64
	Name          string
	Description   string
	VersionSUTIDs []int64
	PageIDs       []int64
	APIIDs        []int64
	Cases         []ExportCase
}

// ExportCase is one current case version with its immutable children.
type ExportCase struct {
	CaseID              int64
	VersionID           int64
	Revision            int
	Title               string
	Description         string
	Preconditions       string
	TestData            json.RawMessage
	Priority            string
	Status              string
	DefaultVersionSUTID *int64
	VersionSUTIDs       []int64
	PageIDs             []int64
	APIIDs              []int64
	CreatedAt           time.Time
	Steps               []CaseStep
	Assertions          []CaseAssertion
	Sources             []CaseSource
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

// formatIDs renders an int64 list as comma-separated decimal IDs.
func formatIDs(values []int64) string {
	if len(values) == 0 {
		return "-"
	}
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ", "
		}
		result += formatInt64(value)
	}
	return result
}

// formatOptionalID renders an optional ID or "-".
func formatOptionalID(value *int64) string {
	if value == nil {
		return "-"
	}
	return formatInt64(*value)
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
