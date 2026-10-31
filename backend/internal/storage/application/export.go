package storageapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// DefaultExportLimit and MaxExportLimit bound the export list page.
const (
	DefaultExportLimit = 20
	MaxExportLimit     = 100
)

// ExportService records export artifacts. Bytes are stored in the P0 content
// ledger (the explicit P2-D-29 reuse exception), metadata lives in
// export_artifact.
type ExportService struct {
	repo    ExportRepository
	content ContentLedger
	ids     platformid.Generator
	clock   clock.Clock
}

// NewExportService wires the export artifact service.
func NewExportService(repo ExportRepository, content ContentLedger, ids platformid.Generator, clk clock.Clock) *ExportService {
	if clk == nil {
		clk = clock.System{}
	}
	return &ExportService{repo: repo, content: content, ids: ids, clock: clk}
}

// CreateExportInput is one export creation request.
type CreateExportInput struct {
	VersionID int64
	OwnerID   int64
	OwnerKind string
	Format    string
	MIME      string
	Filename  string
	Data      []byte
}

// Create stores the rendered bytes in the content ledger, records the
// owner_kind=export content reference and inserts the next export revision.
func (s *ExportService) Create(ctx context.Context, in CreateExportInput, actorID int64) (storage.ExportArtifact, error) {
	if in.VersionID <= 0 {
		return storage.ExportArtifact{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if in.OwnerID <= 0 {
		return storage.ExportArtifact{}, platform.Invalid("owner_id", "must be a positive decimal ID")
	}
	ownerKind := strings.TrimSpace(in.OwnerKind)
	if !storage.ValidExportOwnerKind(ownerKind) {
		return storage.ExportArtifact{}, platform.Invalid("owner_kind", "must be one of case, report, chain")
	}
	format := strings.TrimSpace(in.Format)
	if !storage.ValidExportFormat(format) {
		return storage.ExportArtifact{}, platform.Invalid("format", "must be one of markdown, mermaid, opml, csv, html, pdf")
	}
	mime := strings.TrimSpace(in.MIME)
	if err := validation.String("mime", mime, 1, 200); err != nil {
		return storage.ExportArtifact{}, err
	}
	filename := strings.TrimSpace(in.Filename)
	if err := validation.String("filename", filename, 1, 255); err != nil {
		return storage.ExportArtifact{}, err
	}
	if int64(len(in.Data)) > MaxUploadBytes {
		return storage.ExportArtifact{}, platform.NewError(413, "payload_too_large", "export content exceeds the 10 MiB limit")
	}
	if s.ids == nil {
		return storage.ExportArtifact{}, platform.NewError(500, "internal", "id generator is not configured")
	}
	if s.content == nil {
		return storage.ExportArtifact{}, platform.NewError(500, "internal", "content ledger is not configured")
	}

	maxRevision, err := s.repo.MaxRevision(ctx, in.VersionID, ownerKind, in.OwnerID, format)
	if err != nil {
		return storage.ExportArtifact{}, err
	}
	artifactID := s.ids.Next()
	object, err := s.content.Upload(ctx, filename, mime, bytes.NewReader(in.Data), MaxUploadBytes, actorID)
	if err != nil {
		return storage.ExportArtifact{}, err
	}
	if object.Size != int64(len(in.Data)) {
		return storage.ExportArtifact{}, platform.NewError(500, "internal", "content ledger size mismatch")
	}
	if _, err := s.content.AddRef(ctx, object.SHA256, storage.OwnerKindExport, artifactID, "content"); err != nil {
		return storage.ExportArtifact{}, err
	}

	artifact := storage.ExportArtifact{
		ID:              artifactID,
		VersionID:       in.VersionID,
		OwnerID:         in.OwnerID,
		OwnerKind:       ownerKind,
		Revision:        maxRevision + 1,
		Format:          format,
		ContentObjectID: object.SHA256,
		Size:            object.Size,
		MIME:            mime,
		Filename:        filename,
		Status:          storage.ExportStatusReady,
		CreatedAt:       s.clock.Now().UTC(),
	}
	if actorID > 0 {
		actor := actorID
		artifact.CreatedBy = &actor
	}
	stored, err := s.repo.Create(ctx, artifact)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicate) {
			return storage.ExportArtifact{}, platform.Conflict("export revision already exists")
		}
		return storage.ExportArtifact{}, err
	}
	return stored, nil
}

// Get returns one export artifact.
func (s *ExportService) Get(ctx context.Context, exportID int64) (storage.ExportArtifact, error) {
	if exportID <= 0 {
		return storage.ExportArtifact{}, platform.Invalid("export_id", "must be a positive decimal ID")
	}
	artifact, err := s.repo.Get(ctx, exportID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return storage.ExportArtifact{}, platform.NotFound("export")
		}
		return storage.ExportArtifact{}, err
	}
	return artifact, nil
}

// List returns one page of export artifacts in ID-descending order.
func (s *ExportService) List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]storage.ExportArtifact, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	format = strings.TrimSpace(format)
	if format != "" && !storage.ValidExportFormat(format) {
		return nil, false, platform.Invalid("format", "must be one of markdown, mermaid, opml, csv, html, pdf")
	}
	if beforeID != nil && *beforeID <= 0 {
		return nil, false, platform.Invalid("before", "must be a positive decimal ID")
	}
	if limit <= 0 {
		limit = DefaultExportLimit
	}
	if limit > MaxExportLimit {
		limit = MaxExportLimit
	}
	artifacts, err := s.repo.List(ctx, versionID, format, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(artifacts) > limit
	if hasMore {
		artifacts = artifacts[:limit]
	}
	return artifacts, hasMore, nil
}

// Open returns one artifact and a reader over hash-verified stored bytes.
func (s *ExportService) Open(ctx context.Context, exportID int64) (storage.ExportArtifact, io.ReadCloser, error) {
	artifact, err := s.Get(ctx, exportID)
	if err != nil {
		return storage.ExportArtifact{}, nil, err
	}
	if s.content == nil {
		return storage.ExportArtifact{}, nil, platform.NewError(500, "internal", "content ledger is not configured")
	}
	object, reader, err := s.content.Open(ctx, artifact.ContentObjectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return storage.ExportArtifact{}, nil, storage.ErrCorrupt
		}
		return storage.ExportArtifact{}, nil, err
	}
	if object.Size != artifact.Size || object.SHA256 != artifact.ContentObjectID {
		_ = reader.Close()
		return storage.ExportArtifact{}, nil, storage.ErrCorrupt
	}
	return artifact, reader, nil
}

// NextCursor renders the list cursor of the last item on a page.
func NextCursor(artifacts []storage.ExportArtifact, hasMore bool) (string, bool) {
	if !hasMore || len(artifacts) == 0 {
		return "", false
	}
	last := artifacts[len(artifacts)-1]
	return platformid.Format(last.ID), true
}
