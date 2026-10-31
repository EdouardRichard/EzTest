package storageapp

import (
	"context"
	"io"
)

// SettingsReader reads typed settings such as paths.data_root.
type SettingsReader interface {
	String(ctx context.Context, key string) (string, error)
}

// FileStore stores immutable blobs by SHA-256 under a configured root and
// returns the slash-separated path relative to that root.
type FileStore interface {
	// Put writes data at the sharded path for sha and returns the relative path.
	Put(ctx context.Context, root, sha256 string, data []byte) (relativePath string, err error)
	// Open opens a relative path previously returned by Put.
	Open(ctx context.Context, root, relativePath string) (io.ReadCloser, error)
}
