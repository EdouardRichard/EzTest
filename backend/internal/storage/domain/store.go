package storage

import (
	"context"
	"io"
)

// Store is the object-storage port. Implementations store immutable bytes
// content-addressed by their SHA-256:
//
//   - Put streams an object, returns its Object metadata and never overwrites
//     existing bytes; uploading bytes that are already stored returns the
//     existing object (deduplication).
//   - Open returns a reader over verified bytes plus the object metadata. A
//     stored file that no longer hashes to its id returns ErrCorrupted.
//   - Stat returns the object metadata without reading the bytes.
type Store interface {
	Put(ctx context.Context, r io.Reader, contentType string) (Object, error)
	Open(ctx context.Context, sha256 string) (io.ReadCloser, Object, error)
	Stat(ctx context.Context, sha256 string) (Object, error)
}
