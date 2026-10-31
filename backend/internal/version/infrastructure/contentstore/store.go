// Package contentstore adapts the P0 storage application service to the
// version context's ContentStore port. It lives in the version infrastructure
// layer so the version application never imports the storage implementation.
package contentstore

import (
	"context"
	"errors"
	"io"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagedomain "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// Store adapts *storageapp.Service to versionapp.ContentStore.
type Store struct {
	svc *storageapp.Service
}

var _ versionapp.ContentStore = (*Store)(nil)

// New wires the adapter.
func New(svc *storageapp.Service) *Store {
	return &Store{svc: svc}
}

// Upload stores bytes through the content ledger and returns its metadata.
func (s *Store) Upload(ctx context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (versionapp.ContentObject, error) {
	object, err := s.svc.Upload(ctx, filename, mime, r, maxBytes, actorID)
	if err != nil {
		return versionapp.ContentObject{}, mapError(err)
	}
	return versionapp.ContentObject{SHA256: object.SHA256, Size: object.Size, MIME: object.MIME}, nil
}

// Open opens a content object and returns its metadata plus a reader.
func (s *Store) Open(ctx context.Context, sha256 string) (versionapp.ContentObject, io.ReadCloser, error) {
	object, reader, err := s.svc.Open(ctx, sha256)
	if err != nil {
		return versionapp.ContentObject{}, nil, mapError(err)
	}
	return versionapp.ContentObject{SHA256: object.SHA256, Size: object.Size, MIME: object.MIME}, reader, nil
}

// AddRef records a semantic owner reference for a content object.
func (s *Store) AddRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (versionapp.ContentRef, error) {
	ref, err := s.svc.AddRef(ctx, objectID, ownerKind, ownerID, role)
	if err != nil {
		return versionapp.ContentRef{}, mapError(err)
	}
	return versionapp.ContentRef{
		ID: ref.ID, ObjectID: ref.ObjectID, OwnerKind: ref.OwnerKind,
		OwnerID: ref.OwnerID, Role: ref.Role,
	}, nil
}

// ListRefs returns the active references for one owner.
func (s *Store) ListRefs(ctx context.Context, ownerKind string, ownerID int64) ([]versionapp.ContentRef, error) {
	refs, err := s.svc.ListRefs(ctx, ownerKind, ownerID)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]versionapp.ContentRef, 0, len(refs))
	for _, ref := range refs {
		result = append(result, versionapp.ContentRef{
			ID: ref.ID, ObjectID: ref.ObjectID, OwnerKind: ref.OwnerKind,
			OwnerID: ref.OwnerID, Role: ref.Role,
		})
	}
	return result, nil
}

func mapError(err error) error {
	if errors.Is(err, storagedomain.ErrNotFound) {
		return platform.NotFound("content")
	}
	return err
}
