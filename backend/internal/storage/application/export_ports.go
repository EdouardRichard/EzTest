package storageapp

import (
	"context"
	"io"

	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// ExportRepository persists export-artifact metadata rows.
type ExportRepository interface {
	// Create inserts one artifact row with its caller-assigned ID/revision.
	Create(ctx context.Context, artifact storage.ExportArtifact) (storage.ExportArtifact, error)
	// Get returns one artifact or storage.ErrNotFound.
	Get(ctx context.Context, exportID int64) (storage.ExportArtifact, error)
	// MaxRevision returns the highest revision of one owner/format tuple (0
	// when none exists).
	MaxRevision(ctx context.Context, versionID int64, ownerKind string, ownerID int64, format string) (int, error)
	// List returns artifacts of a version in ID-descending order, optionally
	// filtered by format and cursor.
	List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]storage.ExportArtifact, error)
}

// ContentLedger is the P0 content service surface consumed by exports: bytes go
// into the content-object ledger and a content_ref records the ownership.
type ContentLedger interface {
	Upload(ctx context.Context, filename, declaredMIME string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error)
	AddRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (storage.Ref, error)
	Open(ctx context.Context, sha string) (storage.Object, io.ReadCloser, error)
}
