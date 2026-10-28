// Package filestore implements the mindmap export filesystem adapter (spec
// §6.10, D30). Generated Markdown/Mermaid/OPML files live under one configured
// root (EZTEST_EXPORT_DIR, default .data/exports) and PostgreSQL only stores the
// root-relative path, sha256 and size of each file, so the database never
// carries export bytes.
//
// The layout is versions/{versionId}/(baseline-{baselineId}|draft)/{format}-{exportId}.{ext}.
// Writes go to a temporary file in the destination directory, are fsynced and
// atomically renamed into place, so a reader never observes a partially written
// export. File and directory permissions are 0600/0700 on platforms with Unix
// permission bits. Every path is validated against the root: relative and
// absolute traversal, symlinked directories and symlink targets are rejected,
// and the resolved target must stay inside the root.
package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

const (
	// dirPerm is the least-privilege mode of the export root and its
	// subdirectories (D30).
	dirPerm os.FileMode = 0o700
	// filePerm is the least-privilege mode of one stored export.
	filePerm os.FileMode = 0o600
	// tempPrefix marks an in-flight write so orphan temporary files are
	// identifiable after a crash.
	tempPrefix = ".export-"
	// MaxFileBytes is the per-export size cap of spec §6.10/D30 (20 MiB). A
	// 300-node mindmap stays far below it; the cap is a storage safeguard.
	MaxFileBytes int64 = 20 << 20
	// maxPathSegments bounds the accepted relative path depth. The production
	// layout has four segments (versions/{id}/{scope}/{file}).
	maxPathSegments = 8
	// maxSegmentRunes bounds one path component.
	maxSegmentRunes = 128
)

// Store is the filesystem export store. It is safe for concurrent use: every
// write uses its own temporary file and renames it onto its unique target.
type Store struct {
	root     string
	maxBytes int64
}

// Option customises a Store.
type Option func(*Store)

// WithMaxBytes overrides the per-file size limit. Tests use it to cover the
// limit boundary cheaply; production uses MaxFileBytes.
func WithMaxBytes(maxBytes int64) Option {
	return func(s *Store) {
		if maxBytes > 0 {
			s.maxBytes = maxBytes
		}
	}
}

// New returns a store rooted at root. The root is created on the first Write
// with 0700 permissions when it does not exist; the composition root normally
// creates it from EZTEST_EXPORT_DIR. An empty root is a configuration error.
func New(root string, opts ...Option) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("filestore: export root is not configured")
	}
	store := &Store{root: root, maxBytes: MaxFileBytes}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	return store, nil
}

// Write stores data at the root-relative path rel and returns the stored path,
// the lowercase hex sha256 of the content and its size. The file appears only
// complete: it is written to a temporary file in the destination directory,
// fsynced, chmod-ed to 0600 and renamed into place. An oversized payload is
// rejected before anything is written.
func (s *Store) Write(rel string, data []byte) (string, string, int64, error) {
	segments, err := validatePath(rel)
	if err != nil {
		return "", "", 0, err
	}
	if int64(len(data)) > s.maxBytes {
		return "", "", 0, fmt.Errorf("filestore: export of %d bytes exceeds the %d byte limit",
			len(data), s.maxBytes)
	}
	root, err := s.ensureRoot()
	if err != nil {
		return "", "", 0, err
	}
	dir := filepath.Join(append([]string{root}, segments[:len(segments)-1]...)...)
	if err := ensureDir(root, dir); err != nil {
		return "", "", 0, err
	}
	target := filepath.Join(dir, segments[len(segments)-1])
	if !within(root, target) {
		return "", "", 0, invalidPath("export path escapes the export root")
	}
	// A pre-created symlink at the target would be replaced by the rename, but
	// rejecting it keeps the store's contract uniform with Open/Remove.
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", 0, invalidPath("export path must not be a symlink")
	}

	temp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return "", "", 0, fmt.Errorf("filestore: create export temp file: %w", err)
	}
	tempName := temp.Name()
	committed := false
	// Any early return removes the temporary file; a successful rename flips
	// committed so the deferred Remove no longer runs.
	defer func() {
		if !committed {
			_ = temp.Close()
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(filePerm); err != nil {
		return "", "", 0, fmt.Errorf("filestore: set export file mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return "", "", 0, fmt.Errorf("filestore: write export: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return "", "", 0, fmt.Errorf("filestore: sync export: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", "", 0, fmt.Errorf("filestore: close export: %w", err)
	}
	if err := os.Rename(tempName, target); err != nil {
		return "", "", 0, fmt.Errorf("filestore: commit export: %w", err)
	}
	committed = true
	// The rename is durable once the directory itself is fsynced. Windows does
	// not support flushing a directory handle, so this is best effort there.
	syncDir(dir)

	sum := sha256.Sum256(data)
	return strings.Join(segments, "/"), hex.EncodeToString(sum[:]), int64(len(data)), nil
}

// Open resolves a stored export path inside the root and returns its content.
// Paths that escape the root (through "..", an absolute path or a symlink) are
// rejected as invalid; a missing file is reported as fs.ErrNotExist so callers
// can distinguish an orphaned metadata row from an infrastructure failure.
func (s *Store) Open(rel string) (io.ReadCloser, error) {
	segments, err := validatePath(rel)
	if err != nil {
		return nil, err
	}
	root, err := s.existingRoot()
	if err != nil {
		return nil, err
	}
	target, err := resolvePath(root, segments)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("filestore: open export: %w", err)
	}
	return file, nil
}

// Remove deletes the file behind rel. A missing file is not an error: metadata
// cleanup is best effort. Unsafe paths are still rejected.
func (s *Store) Remove(rel string) error {
	segments, err := validatePath(rel)
	if err != nil {
		return err
	}
	root, err := s.existingRoot()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	target, err := resolvePath(root, segments)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("filestore: remove export: %w", err)
	}
	return nil
}

// ensureRoot creates the export root when necessary and returns its
// symlink-resolved absolute path, which every confinement check uses as the
// trusted base.
func (s *Store) ensureRoot() (string, error) {
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve export root: %w", err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return "", fmt.Errorf("filestore: create export root: %w", err)
	}
	root, err := s.existingRoot()
	if err != nil {
		return "", err
	}
	// Tighten a root created by an older/looser process; the mode is a no-op on
	// Windows (except for the read-only bit) but still enforces 0700 where it
	// applies.
	if err := os.Chmod(root, dirPerm); err != nil {
		return "", fmt.Errorf("filestore: set export root mode: %w", err)
	}
	return root, nil
}

// existingRoot returns the symlink-resolved absolute root without creating it.
func (s *Store) existingRoot() (string, error) {
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve export root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("filestore: resolve export root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("filestore: stat export root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("filestore: export root %q is not a directory", resolved)
	}
	return resolved, nil
}

// ensureDir creates and validates every directory component below root. A
// component that is a symlink is rejected instead of followed: it is the one
// component an attacker could pre-create to redirect a write outside the root.
func ensureDir(root, dir string) error {
	if !within(root, dir) {
		return invalidPath("export path escapes the export root")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return fmt.Errorf("filestore: resolve export directory: %w", err)
	}
	current := root
	for _, name := range strings.Split(filepath.ToSlash(rel), "/") {
		if name == "." || name == "" {
			continue
		}
		current = filepath.Join(current, name)
		info, err := os.Lstat(current)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return invalidPath("export directory must not be a symlink")
			}
			if !info.IsDir() {
				return fmt.Errorf("filestore: export path %q is not a directory", current)
			}
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(current, dirPerm); err != nil {
				return fmt.Errorf("filestore: create export directory: %w", err)
			}
		default:
			return fmt.Errorf("filestore: stat export directory: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("filestore: resolve export directory: %w", err)
		}
		if !within(root, resolved) {
			return invalidPath("export directory escapes the export root")
		}
		// Tighten a directory created by an older/looser process; the mode is a
		// no-op on Windows but still enforces 0700 where it applies.
		if err := os.Chmod(resolved, dirPerm); err != nil {
			return fmt.Errorf("filestore: set export directory mode: %w", err)
		}
		current = resolved
	}
	return nil
}

// resolvePath validates a root-relative export path, rejects symlinks and proves
// the resolved target stays inside root.
func resolvePath(root string, segments []string) (string, error) {
	candidate := filepath.Join(append([]string{root}, segments...)...)
	info, err := os.Lstat(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("filestore: stat export: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", invalidPath("export path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return "", invalidPath("export path is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve export path: %w", err)
	}
	if !within(root, resolved) {
		return "", invalidPath("export path escapes the export root")
	}
	return resolved, nil
}

// validatePath splits and validates a root-relative export path: at least one
// directory and one file component, no volume, no absolute path, no "." or
// "..", no empty component, no colon (a Windows drive or stream separator) and
// no reserved filename characters.
func validatePath(rel string) ([]string, error) {
	if rel == "" || !utf8.ValidString(rel) {
		return nil, invalidPath("export path must be a relative path inside the export root")
	}
	// Treat both separators as separators on every platform so a path crafted
	// for Windows cannot slip past the checks on Unix, and vice versa.
	normalized := strings.ReplaceAll(rel, `\`, "/")
	if strings.HasPrefix(normalized, "/") || filepath.VolumeName(rel) != "" ||
		strings.ContainsRune(normalized, ':') {
		return nil, invalidPath("export path must be a relative path inside the export root")
	}
	segments := strings.Split(normalized, "/")
	if len(segments) < 2 || len(segments) > maxPathSegments {
		return nil, invalidPath("export path must be a relative path inside the export root")
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, invalidPath("export path must be a relative path inside the export root")
		}
		if strings.ContainsRune(segment, 0) || strings.ContainsAny(segment, `*?"<>|`) {
			return nil, invalidPath("export path must not contain reserved characters")
		}
		if utf8.RuneCountInString(segment) > maxSegmentRunes {
			return nil, invalidPath("export path component is too long")
		}
	}
	return segments, nil
}

// invalidPath builds the stable invalid_storage_path error of the export store.
func invalidPath(message string) error {
	return domain.NewInvalid("path", "invalid_storage_path", message)
}

// within reports whether target is root itself or a descendant of root.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// syncDir flushes a directory entry change. Directory fsync is unsupported on
// Windows, so failures are deliberately ignored everywhere.
func syncDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = handle.Sync()
	_ = handle.Close()
}
