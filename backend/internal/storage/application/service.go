// Package application implements the object-storage use cases. It coordinates
// the storage port, validates upload inputs and records the audit trail; the
// HTTP interface depends only on this package.
package application

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// Service is the object-storage use case: it validates the upload Content-Type,
// delegates to the storage port, persists the durable object metadata and
// records the create audit entry. Reads take the media type from the metadata
// row, the single durable source of truth.
type Service struct {
	store    storage.Store
	metadata MetadataRepository
	recorder auditapp.Recorder
}

// NewService returns the storage use case. metadata is the insert-only
// storage_objects repository. A nil recorder degrades to a no-op so a
// partially wired process cannot panic on the first upload.
func NewService(store storage.Store, metadata MetadataRepository, recorder auditapp.Recorder) *Service {
	if recorder == nil {
		recorder = auditapp.NopRecorder{}
	}
	return &Service{store: store, metadata: metadata, recorder: recorder}
}

// Put stores an object, persists its metadata row and records
// storage.object_created only when that row was newly inserted. A re-upload of
// bytes that are already stored is a dedupe: no new row is created, so no
// created audit is written, and the object is reported with the persisted
// content type. The Content-Type is required: it is the media type an object is
// first stored with and the one reported by later reads.
func (s *Service) Put(ctx context.Context, r io.Reader, contentType string) (storage.Object, error) {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return storage.Object{}, storage.ErrInvalidContentType
	}
	obj, err := s.store.Put(ctx, r, contentType)
	if err != nil {
		return storage.Object{}, err
	}
	created, err := s.metadata.Insert(ctx, ObjectMetadata{
		SHA256:       obj.SHA256,
		Size:         obj.Size,
		ContentType:  contentType,
		RelativePath: obj.RelativePath,
	})
	if err != nil {
		// The bytes are durable but their metadata is not; fail the request so
		// the caller can retry instead of returning an object the database
		// does not know about.
		return storage.Object{}, fmt.Errorf("storage: persist object metadata: %w", err)
	}
	if !created {
		// Report the durable content type of the object this upload deduped
		// to, not the type the duplicate request happened to send.
		existing, ok, err := s.metadata.Find(ctx, obj.SHA256)
		if err != nil {
			return storage.Object{}, fmt.Errorf("storage: load object metadata: %w", err)
		}
		if ok {
			obj.ContentType = existing.ContentType
		}
		return obj, nil
	}
	s.recordCreated(ctx, obj)
	return obj, nil
}

// Open returns the verified bytes of an object with its persisted media type.
func (s *Service) Open(ctx context.Context, id string) (io.ReadCloser, storage.Object, error) {
	rc, obj, err := s.store.Open(ctx, id)
	if err != nil {
		return nil, storage.Object{}, err
	}
	if err := s.fillContentType(ctx, &obj); err != nil {
		_ = rc.Close()
		return nil, storage.Object{}, err
	}
	return rc, obj, nil
}

// Stat returns an object's metadata with its persisted media type.
func (s *Service) Stat(ctx context.Context, id string) (storage.Object, error) {
	obj, err := s.store.Stat(ctx, id)
	if err != nil {
		return storage.Object{}, err
	}
	if err := s.fillContentType(ctx, &obj); err != nil {
		return storage.Object{}, err
	}
	return obj, nil
}

// fillContentType copies the durable Content-Type onto obj. An object whose
// metadata row is missing keeps an empty type; the HTTP layer then falls back
// to application/octet-stream.
func (s *Service) fillContentType(ctx context.Context, obj *storage.Object) error {
	metadata, ok, err := s.metadata.Find(ctx, obj.SHA256)
	if err != nil {
		return fmt.Errorf("storage: load object metadata: %w", err)
	}
	if ok {
		obj.ContentType = metadata.ContentType
	}
	return nil
}

// recordCreated writes storage.object_created for a genuinely new object, that
// is, one whose metadata row was just inserted. Auditing is best-effort: the
// upload response is still returned when the audit write fails, and only the
// failure is logged (never the object bytes).
func (s *Service) recordCreated(ctx context.Context, obj storage.Object) {
	entry := auditdomain.Entry{
		Action:     auditapp.ActionStorageObjectCreated,
		TargetType: "storage_object",
		TargetID:   obj.SHA256,
		Metadata: map[string]any{
			"sha256":       obj.SHA256,
			"size":         obj.Size,
			"content_type": obj.ContentType,
		},
	}
	meta := auditapp.MetaFromContext(ctx)
	entry.RequestID = meta.RequestID
	entry.IP = meta.IP
	entry.UserAgent = meta.UserAgent
	if actorID, ok := auditapp.ActorIDFromContext(ctx); ok {
		entry.ActorUserID = &actorID
	}
	if err := s.recorder.Record(ctx, entry); err != nil {
		slog.ErrorContext(ctx, "audit: record storage object entry", "action", entry.Action, "error", err)
	}
}
