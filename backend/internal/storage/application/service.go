// Package application implements the object-storage use cases. It coordinates
// the storage port, validates upload inputs and records the audit trail; the
// HTTP interface depends only on this package.
package application

import (
	"context"
	"io"
	"log/slog"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// Service is the object-storage use case: it validates the upload Content-Type,
// delegates to the storage port and records the create audit entry.
type Service struct {
	store    storage.Store
	recorder auditapp.Recorder
}

// NewService returns the storage use case. A nil recorder degrades to a no-op
// so a partially wired process cannot panic on the first upload.
func NewService(store storage.Store, recorder auditapp.Recorder) *Service {
	if recorder == nil {
		recorder = auditapp.NopRecorder{}
	}
	return &Service{store: store, recorder: recorder}
}

// Put stores an object and records storage.object_created. The Content-Type is
// required: it is the media type the caller stores the object with and the one
// reported in the API response.
func (s *Service) Put(ctx context.Context, r io.Reader, contentType string) (storage.Object, error) {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return storage.Object{}, storage.ErrInvalidContentType
	}
	obj, err := s.store.Put(ctx, r, contentType)
	if err != nil {
		return storage.Object{}, err
	}
	s.recordCreated(ctx, obj)
	return obj, nil
}

// Open returns the verified bytes of an object.
func (s *Service) Open(ctx context.Context, id string) (io.ReadCloser, storage.Object, error) {
	return s.store.Open(ctx, id)
}

// Stat returns an object's metadata.
func (s *Service) Stat(ctx context.Context, id string) (storage.Object, error) {
	return s.store.Stat(ctx, id)
}

// recordCreated writes storage.object_created. Auditing is best-effort: the
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
