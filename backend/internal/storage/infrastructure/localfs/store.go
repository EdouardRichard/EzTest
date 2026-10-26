// Package localfs implements the storage port on the local filesystem. Objects
// are immutable and content-addressed: the bytes of object <sha256> live at
// <root>/ab/cd/<sha256>, where ab and cd are the first four hex characters.
package localfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// ErrInvalidRoot is returned when the store was created without a root
// directory.
var ErrInvalidRoot = errors.New("storage: local store root is required")

// Store stores content-addressed objects below a local directory. It is safe
// for concurrent use: uploads are staged in a temporary file in the root and
// moved into place with an atomic rename.
type Store struct {
	root string
}

// New returns a filesystem-backed store rooted at root. The root directory is
// created lazily by the first write.
func New(root string) *Store {
	return &Store{root: root}
}

// Root returns the configured root directory.
func (s *Store) Root() string { return s.root }

// Put streams r into a temporary file, hashes it, fsyncs it and atomically
// renames it to its content-addressed path. If an object with the same hash
// already exists it is reused and never overwritten (deduplication). The
// returned Object reports the requested ContentType; the local filesystem does
// not persist it, so read paths leave ContentType empty.
func (s *Store) Put(ctx context.Context, r io.Reader, contentType string) (storage.Object, error) {
	if s.root == "" {
		return storage.Object{}, ErrInvalidRoot
	}
	if r == nil {
		return storage.Object{}, errors.New("storage: nil object reader")
	}
	if err := ctx.Err(); err != nil {
		return storage.Object{}, err
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return storage.Object{}, fmt.Errorf("storage: create root: %w", err)
	}

	tmp, err := os.CreateTemp(s.root, ".put-*")
	if err != nil {
		return storage.Object{}, fmt.Errorf("storage: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// On success the file no longer exists under tmpName after the rename; a
	// failed upload must not leave a staging file behind.
	defer func() { _ = os.Remove(tmpName) }()
	defer func() { _ = tmp.Close() }()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), r)
	if err != nil {
		return storage.Object{}, fmt.Errorf("storage: write object: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return storage.Object{}, fmt.Errorf("storage: sync object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return storage.Object{}, fmt.Errorf("storage: close object: %w", err)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	relativePath := objectPath(sum)
	finalPath := filepath.Join(s.root, filepath.FromSlash(relativePath))

	if existing, err := os.Stat(finalPath); err == nil {
		// Deduplicate: never replace an object that is already stored.
		return storage.Object{
			SHA256:       sum,
			Size:         existing.Size(),
			ContentType:  contentType,
			RelativePath: relativePath,
		}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return storage.Object{}, fmt.Errorf("storage: stat object: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return storage.Object{}, fmt.Errorf("storage: create object directory: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return storage.Object{}, fmt.Errorf("storage: commit object: %w", err)
	}
	return storage.Object{
		SHA256:       sum,
		Size:         size,
		ContentType:  contentType,
		RelativePath: relativePath,
	}, nil
}

// Open reads and verifies an object, then returns a reader over the verified
// bytes. Verification is eager so corruption is reported as ErrCorrupted before
// any of the bytes are handed to a caller (objects are bounded at 10 MiB by the
// HTTP layer).
func (s *Store) Open(ctx context.Context, id string) (io.ReadCloser, storage.Object, error) {
	if s.root == "" {
		return nil, storage.Object{}, ErrInvalidRoot
	}
	if err := storage.ValidateSHA256(id); err != nil {
		return nil, storage.Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, storage.Object{}, err
	}

	relativePath := objectPath(id)
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(relativePath)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, storage.Object{}, storage.ErrObjectNotFound
	}
	if err != nil {
		return nil, storage.Object{}, fmt.Errorf("storage: read object: %w", err)
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != id {
		return nil, storage.Object{}, fmt.Errorf("%w: %s", storage.ErrCorrupted, id)
	}
	return io.NopCloser(bytes.NewReader(data)), storage.Object{
		SHA256:       id,
		Size:         int64(len(data)),
		RelativePath: relativePath,
	}, nil
}

// Stat reports an object's metadata without reading its bytes.
func (s *Store) Stat(ctx context.Context, id string) (storage.Object, error) {
	if s.root == "" {
		return storage.Object{}, ErrInvalidRoot
	}
	if err := storage.ValidateSHA256(id); err != nil {
		return storage.Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return storage.Object{}, err
	}

	relativePath := objectPath(id)
	info, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(relativePath)))
	if errors.Is(err, fs.ErrNotExist) {
		return storage.Object{}, storage.ErrObjectNotFound
	}
	if err != nil {
		return storage.Object{}, fmt.Errorf("storage: stat object: %w", err)
	}
	return storage.Object{
		SHA256:       id,
		Size:         info.Size(),
		RelativePath: relativePath,
	}, nil
}

// objectPath returns the slash-separated content-addressed path of an object:
// the first four hex characters become two directory levels.
func objectPath(id string) string {
	return path.Join(id[:2], id[2:4], id)
}
