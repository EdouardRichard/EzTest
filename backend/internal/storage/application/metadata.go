package application

import (
	"context"
	"time"
)

// ObjectMetadata is the durable metadata of one stored object. It is the
// storage_object row: the content-addressed id, the verified byte size, the
// media type declared on first upload, the relative storage path and when the
// row was created. The table is append-only.
type ObjectMetadata struct {
	SHA256       string
	Size         int64
	ContentType  string
	RelativePath string
	CreatedAt    time.Time
}

// MetadataRepository is the insert-only port over the storage_object table.
// Implementations never update or delete a row:
//
//   - Insert persists metadata and reports whether a new row was created.
//     Uploading bytes that already have a row is a no-op (the insert conflicts
//     with the sha256 primary key) and reports false.
//   - Find returns the stored metadata, or ok=false when no row exists.
type MetadataRepository interface {
	Insert(ctx context.Context, metadata ObjectMetadata) (created bool, err error)
	Find(ctx context.Context, sha256 string) (metadata ObjectMetadata, ok bool, err error)
}
