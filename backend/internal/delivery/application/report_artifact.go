package deliveryapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Report artifact formats (spec §4.29 R-W2-3).
const (
	ReportArtifactFormatHTML     = "html"
	ReportArtifactFormatPDF      = "pdf"
	ReportArtifactFormatMarkdown = "markdown"

	// exportEventStarted/Completed/Failed are the v0.22 sync export lifecycle
	// events (spec §7 EV-17). They are published on the version channel around
	// the artifact write so a report page can consume the state transition.
	exportEventStarted   = "export.started"
	exportEventCompleted = "export.completed"
	exportEventFailed    = "export.failed"

	// exportEventMaxError bounds the payload error string.
	exportEventMaxError = 2000
)

// ReportArtifactStoreInput is one report artifact write. The delivery context
// owns this narrow contract so it never imports the storage application types.
type ReportArtifactStoreInput struct {
	ID        int64
	VersionID int64
	OwnerID   int64
	OwnerKind string
	Format    string
	MIME      string
	Filename  string
	Data      []byte
	ActorID   int64
}

// ReportArtifactRecord is the stored artifact identity the storage adapter
// returns (ContentObjectID is the sha256 content key).
type ReportArtifactRecord struct {
	ID              int64
	Format          string
	ContentObjectID string
	Size            int64
	Status          string
}

// ReportArtifact is the delivery read model of one created artifact.
type ReportArtifact struct {
	ID              int64
	Format          string
	ContentObjectID string
	Size            int64
	Status          string
}

// ReportArtifactStore persists report artifact bytes through the storage
// content ledger and export_artifact table.
type ReportArtifactStore interface {
	CreateReportArtifact(ctx context.Context, in ReportArtifactStoreInput) (ReportArtifactRecord, error)
}

// WithArtifactStore attaches the report artifact writer and the ID generator
// that preallocates the artifact ID (so export.started can name it). Both are
// optional; without them CreateArtifact answers a safe 500.
func (s *ReportService) WithArtifactStore(store ReportArtifactStore, ids platformid.Generator) *ReportService {
	s.artifacts = store
	s.artifactIDs = ids
	return s
}

// CreateArtifact renders one report revision in the requested format and stores
// it as an export_artifact (owner_kind=report). HTML/PDF reuse the verified
// OpenHTML/OpenPDF reads; Markdown uses the deterministic renderer. The export
// lifecycle is published synchronously: export.started before generation and
// export.completed/export.failed after the write, on the version channel.
func (s *ReportService) CreateArtifact(ctx context.Context, versionID, reportID int64, format string, actorID int64) (ReportArtifact, error) {
	if versionID <= 0 || reportID <= 0 {
		return ReportArtifact{}, platform.Invalid("report_id", "must be a positive decimal ID")
	}
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case ReportArtifactFormatHTML, ReportArtifactFormatPDF, ReportArtifactFormatMarkdown:
	default:
		return ReportArtifact{}, platform.Invalid("format", "must be one of html, pdf, markdown")
	}
	report, err := s.repo.GetReport(ctx, versionID, reportID)
	if err != nil {
		return ReportArtifact{}, mapReportError(err)
	}
	revision, err := s.currentExportRevision(ctx, report)
	if err != nil {
		return ReportArtifact{}, err
	}
	if s.artifacts == nil {
		return ReportArtifact{}, reportServiceNotConfigured("report artifact store")
	}
	if s.artifactIDs == nil {
		return ReportArtifact{}, reportServiceNotConfigured("report artifact ID generator")
	}
	artifactID := s.artifactIDs.Next()
	if artifactID <= 0 {
		return ReportArtifact{}, reportServiceNotConfigured("report artifact ID generator")
	}

	s.publishExportEvent(ctx, report, artifactID, format, exportEventStarted, "started", "", "")
	data, err := s.reportArtifactBytes(ctx, report, revision, format)
	if err != nil {
		s.publishExportEvent(ctx, report, artifactID, format, exportEventFailed, "failed", "", exportErrorText(err))
		return ReportArtifact{}, err
	}
	record, err := s.artifacts.CreateReportArtifact(ctx, ReportArtifactStoreInput{
		ID:        artifactID,
		VersionID: report.VersionID,
		OwnerID:   report.ID,
		OwnerKind: "report",
		Format:    format,
		MIME:      reportArtifactMIME(format),
		Filename:  reportArtifactFilename(report.ID, revision.Revision, format),
		Data:      data,
		ActorID:   actorID,
	})
	if err != nil {
		s.publishExportEvent(ctx, report, artifactID, format, exportEventFailed, "failed", "", exportErrorText(err))
		return ReportArtifact{}, err
	}
	sha := record.ContentObjectID
	if !validReportSHA(sha) {
		sum := sha256.Sum256(data)
		sha = hex.EncodeToString(sum[:])
	}
	size := record.Size
	if size <= 0 {
		size = int64(len(data))
	}
	status := record.Status
	if status == "" {
		status = "ready"
	}
	if record.ID <= 0 {
		record.ID = artifactID
	}
	s.publishExportEvent(ctx, report, artifactID, format, exportEventCompleted, "completed", sha, "")
	return ReportArtifact{
		ID:              record.ID,
		Format:          format,
		ContentObjectID: sha,
		Size:            size,
		Status:          status,
	}, nil
}

// reportArtifactBytes renders or reuses the bytes of one artifact. HTML and PDF
// are the already published immutable revision bytes; Markdown is rendered
// deterministically from the current snapshot stamped with the revision as_of.
func (s *ReportService) reportArtifactBytes(ctx context.Context, report delivery.Report, revision delivery.ReportRevision, format string) ([]byte, error) {
	switch format {
	case ReportArtifactFormatHTML:
		html, _, err := s.OpenHTML(ctx, report.VersionID, report.ID, revision.Revision)
		return html, err
	case ReportArtifactFormatPDF:
		pdf, _, err := s.OpenPDF(ctx, report.VersionID, report.ID, revision.Revision)
		return pdf, err
	case ReportArtifactFormatMarkdown:
		snapshot, err := s.snapshotFor(ctx, report)
		if err != nil {
			return nil, err
		}
		return renderReportMarkdown(stampReportSnapshot(snapshot, report, revision), revision), nil
	default:
		return nil, platform.Invalid("format", "must be one of html, pdf, markdown")
	}
}

// currentExportRevision resolves the report's current revision. Only an
// immutable published+rendered revision is exportable; a draft or a report
// without a current revision is a stable 409 report_not_ready.
func (s *ReportService) currentExportRevision(ctx context.Context, report delivery.Report) (delivery.ReportRevision, error) {
	if report.CurrentRevisionID == nil || *report.CurrentRevisionID <= 0 {
		return delivery.ReportRevision{}, reportArtifactNotReady()
	}
	revisions, err := s.repo.ListRevisions(ctx, report.ID)
	if err != nil {
		return delivery.ReportRevision{}, mapReportError(err)
	}
	for _, revision := range revisions {
		if revision.ID != *report.CurrentRevisionID {
			continue
		}
		if revision.Status != delivery.ReportStatusPublished || revision.RenderStatus != reportRenderDone {
			return delivery.ReportRevision{}, reportArtifactNotReady()
		}
		return revision, nil
	}
	return delivery.ReportRevision{}, reportArtifactNotReady()
}

// publishExportEvent writes one export lifecycle event. Event failures are
// best-effort and never roll back an artifact (spec §4.3).
func (s *ReportService) publishExportEvent(
	ctx context.Context,
	report delivery.Report,
	exportID int64,
	format, eventType, status, sha, errorText string,
) {
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"export_id":  platformid.Format(exportID),
		"report_id":  platformid.Format(report.ID),
		"version_id": platformid.Format(report.VersionID),
		"format":     format,
		"status":     status,
		"sha256":     sha,
		"error":      errorText,
	}
	_ = s.events.Publish(ctx, eventType, report.VersionID, payload)
}

func reportArtifactMIME(format string) string {
	switch format {
	case ReportArtifactFormatHTML:
		return "text/html; charset=utf-8"
	case ReportArtifactFormatPDF:
		return "application/pdf"
	default:
		return "text/markdown; charset=utf-8"
	}
}

func reportArtifactFilename(reportID int64, revision int32, format string) string {
	extension := ".md"
	switch format {
	case ReportArtifactFormatHTML:
		extension = ".html"
	case ReportArtifactFormatPDF:
		extension = ".pdf"
	}
	return fmt.Sprintf("report-%s-r%d%s", platformid.Format(reportID), revision, extension)
}

// exportErrorText returns the bounded human-readable failure reason.
func exportErrorText(err error) string {
	apiErr := platform.AsError(err)
	message := strings.TrimSpace(apiErr.Message)
	if message == "" {
		message = err.Error()
	}
	if len(message) > exportEventMaxError {
		message = message[:exportEventMaxError]
	}
	return message
}

// reportArtifactNotReady is the stable 409 of a report whose current revision
// is not published/rendered yet.
func reportArtifactNotReady() error {
	apiErr := platform.StateConflict("report revision is not ready for export")
	apiErr.Details = map[string]any{"reason": "report_not_ready"}
	return apiErr
}
