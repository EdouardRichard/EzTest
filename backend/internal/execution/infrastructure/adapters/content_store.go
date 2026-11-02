package adapters

import (
	"bytes"
	"context"
	"io"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// ContentService is the P0 content-ledger surface consumed by the adapter
// (*storageapp.Service satisfies it).
type ContentService interface {
	Upload(ctx context.Context, filename, declaredMIME string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error)
	AddRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (storage.Ref, error)
	Open(ctx context.Context, sha string) (storage.Object, io.ReadCloser, error)
}

// ContentStoreAdapter stores evidence bytes in the P0 content ledger and reads
// them back for execution.evidence_read.
type ContentStoreAdapter struct {
	storage ContentService
}

// NewContentStore wires the content-ledger evidence adapter.
func NewContentStore(storage ContentService) *ContentStoreAdapter {
	return &ContentStoreAdapter{storage: storage}
}

var _ executionapp.ContentStore = (*ContentStoreAdapter)(nil)

// PutEvidence uploads the evidence bytes (content-addressed) and records the
// evidence owner reference with the evidence kind as the ref role.
func (c *ContentStoreAdapter) PutEvidence(ctx context.Context, evidenceID int64, kind execution.EvidenceKind, filename string, data []byte) (executionapp.ContentRef, error) {
	if c.storage == nil {
		return executionapp.ContentRef{}, errNotConfigured("content service")
	}
	object, err := c.storage.Upload(ctx, filename, kind.MIME(), bytes.NewReader(data), storageapp.MaxUploadBytes, 0)
	if err != nil {
		return executionapp.ContentRef{}, err
	}
	ref, err := c.storage.AddRef(ctx, object.SHA256, "evidence", evidenceID, string(kind))
	if err != nil {
		return executionapp.ContentRef{}, err
	}
	return executionapp.ContentRef{
		ID:        ref.ID,
		SHA256:    object.SHA256,
		SizeBytes: object.Size,
		MIME:      object.MIME,
	}, nil
}

// Open reads verified evidence bytes by SHA-256 (the storage service re-verifies
// hash/size before returning).
func (c *ContentStoreAdapter) Open(ctx context.Context, sha256 string) ([]byte, error) {
	if c.storage == nil {
		return nil, errNotConfigured("content service")
	}
	_, reader, err := c.storage.Open(ctx, sha256)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, storageapp.MaxUploadBytes+1))
}
