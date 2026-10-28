// Package filestore implements the delivery report filesystem adapter (spec
// §6.5/§16.1, D30). Report Markdown lives under EZTEST_REPORT_DIR and PostgreSQL
// only stores the root-relative path, sha256 and size, so the database never
// carries report bytes.
//
// The layout is versions/{version_id}/reports/{report_id}-r{revision}.md. Writes
// go to a temporary file in the destination directory, are fsynced, chmod-ed to
// 0600 and atomically renamed into place. Directories are 0700 on platforms with
// Unix permission bits. Every path is validated against the root: absolute
// paths, traversal, Windows volumes, reserved characters and symlinked
// directories/targets are rejected.
package filestore

import (
	"context"
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
)

const (
	// dirPerm is the least-privilege mode of the report root and its
	// subdirectories (D30).
	dirPerm os.FileMode = 0o700
	// filePerm is the least-privilege mode of one stored report file.
	filePerm os.FileMode = 0o600
	// tempPrefix marks an in-flight write so orphan temporary files are
	// identifiable after a crash.
	tempPrefix = ".report-"
	// maxPathSegments bounds the accepted relative path depth. The production
	// layout has exactly four segments (versions/{id}/reports/{file}).
	maxPathSegments = 8
	// maxSegmentRunes bounds one path component.
	maxSegmentRunes = 128
)

// ErrInvalidPath reports a path that is not a safe root-relative report path
// (empty, absolute, traversing, a volume or containing reserved characters).
var ErrInvalidPath = errors.New("filestore: report path is not a safe root-relative path")

// Store is the filesystem report store. It is safe for concurrent use: every
// write uses its own temporary file and renames it onto its unique target.
type Store struct {
	root string
}

// New returns a report store rooted at root. The root is created on the first
// Write with 0700 permissions when it does not exist; the composition root
// normally creates it from EZTEST_REPORT_DIR. An empty root is a configuration
// error.
func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("filestore: report root is not configured")
	}
	return &Store{root: root}, nil
}

// Write stores data at the root-relative path rel and returns the normalized
// stored path, the lowercase hex sha256 of the content and its size. The file
// appears only complete: it is written to a temporary file in the destination
// directory, fsynced, chmod-ed to 0600 and renamed into place.
func (s *Store) Write(ctx context.Context, rel string, data []byte) (string, string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	segments, err := validatePath(rel)
	if err != nil {
		return "", "", 0, err
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
		return "", "", 0, invalidPath("report path escapes the report root")
	}
	// A pre-created symlink at the target would be replaced by the rename, but
	// rejecting it keeps the store's contract uniform with Open.
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", 0, invalidPath("report path must not be a symlink")
	}

	temp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return "", "", 0, fmt.Errorf("filestore: create report temp file: %w", err)
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
		return "", "", 0, fmt.Errorf("filestore: set report file mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return "", "", 0, fmt.Errorf("filestore: write report: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return "", "", 0, fmt.Errorf("filestore: sync report: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", "", 0, fmt.Errorf("filestore: close report: %w", err)
	}
	if err := os.Rename(tempName, target); err != nil {
		return "", "", 0, fmt.Errorf("filestore: commit report: %w", err)
	}
	committed = true
	// The rename is durable once the directory itself is fsynced. Windows does
	// not support flushing a directory handle, so this is best effort there.
	syncDir(dir)

	sum := sha256.Sum256(data)
	return strings.Join(segments, "/"), hex.EncodeToString(sum[:]), int64(len(data)), nil
}

// Open resolves a stored report path inside the root and returns its content.
// Paths that escape the root (through "..", an absolute path or a symlink) are
// rejected as invalid; a missing file is reported as fs.ErrNotExist so the
// export service can classify it as report_export_failed.
func (s *Store) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("filestore: open report: %w", err)
	}
	return file, nil
}

// ensureRoot creates the report root when necessary and returns its
// symlink-resolved absolute path, which every confinement check uses as the
// trusted base.
func (s *Store) ensureRoot() (string, error) {
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve report root: %w", err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return "", fmt.Errorf("filestore: create report root: %w", err)
	}
	root, err := s.existingRoot()
	if err != nil {
		return "", err
	}
	// Tighten a root created by an older/looser process; the mode is a no-op on
	// Windows but still enforces 0700 where it applies.
	if err := os.Chmod(root, dirPerm); err != nil {
		return "", fmt.Errorf("filestore: set report root mode: %w", err)
	}
	return root, nil
}

// existingRoot returns the symlink-resolved absolute root without creating it.
func (s *Store) existingRoot() (string, error) {
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve report root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("filestore: resolve report root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("filestore: stat report root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("filestore: report root %q is not a directory", resolved)
	}
	return resolved, nil
}

// ensureDir creates and validates every directory component below root. A
// component that is a symlink is rejected instead of followed: it is the one
// component an attacker could pre-create to redirect a write outside the root.
func ensureDir(root, dir string) error {
	if !within(root, dir) {
		return invalidPath("report path escapes the report root")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return fmt.Errorf("filestore: resolve report directory: %w", err)
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
				return invalidPath("report directory must not be a symlink")
			}
			if !info.IsDir() {
				return fmt.Errorf("filestore: report path %q is not a directory", current)
			}
		case errors.Is(err, fs.ErrNotExist):
			// MkdirAll, not Mkdir: two concurrent first writes into the same
			// new version directory both observe ErrNotExist here, and only
			// treating the peer's directory as success keeps the second write
			// from failing with fs.ErrExist. Symlink checks still run below.
			if err := os.MkdirAll(current, dirPerm); err != nil {
				return fmt.Errorf("filestore: create report directory: %w", err)
			}
		default:
			return fmt.Errorf("filestore: stat report directory: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("filestore: resolve report directory: %w", err)
		}
		if !within(root, resolved) {
			return invalidPath("report directory escapes the report root")
		}
		// Tighten a directory created by an older/looser process; the mode is
		// a no-op on Windows but still enforces 0700 where it applies.
		if err := os.Chmod(resolved, dirPerm); err != nil {
			return fmt.Errorf("filestore: set report directory mode: %w", err)
		}
		current = resolved
	}
	return nil
}

// resolvePath validates a root-relative report path, rejects symlinks and proves
// the resolved target stays inside root.
func resolvePath(root string, segments []string) (string, error) {
	candidate := filepath.Join(append([]string{root}, segments...)...)
	info, err := os.Lstat(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("filestore: stat report: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", invalidPath("report path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return "", invalidPath("report path is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve report path: %w", err)
	}
	if !within(root, resolved) {
		return "", invalidPath("report path escapes the report root")
	}
	return resolved, nil
}

// validatePath splits and validates a root-relative report path: at least one
// directory and one file component, no volume, no absolute path, no "." or
// "..", no empty component, no colon (a Windows drive or stream separator) and
// no reserved filename characters.
func validatePath(rel string) ([]string, error) {
	if rel == "" || !utf8.ValidString(rel) {
		return nil, invalidPath("report path must be a relative path inside the report root")
	}
	// Treat both separators as separators on every platform so a path crafted
	// for Windows cannot slip past the checks on Unix, and vice versa.
	normalized := strings.ReplaceAll(rel, `\`, "/")
	if strings.HasPrefix(normalized, "/") || filepath.VolumeName(rel) != "" ||
		strings.ContainsRune(normalized, ':') {
		return nil, invalidPath("report path must be a relative path inside the report root")
	}
	segments := strings.Split(normalized, "/")
	if len(segments) < 2 || len(segments) > maxPathSegments {
		return nil, invalidPath("report path must be a relative path inside the report root")
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, invalidPath("report path must be a relative path inside the report root")
		}
		if strings.ContainsRune(segment, 0) || strings.ContainsAny(segment, `*?"<>|`) {
			return nil, invalidPath("report path must not contain reserved characters")
		}
		if utf8.RuneCountInString(segment) > maxSegmentRunes {
			return nil, invalidPath("report path component is too long")
		}
	}
	return segments, nil
}

// invalidPath wraps ErrInvalidPath with the concrete refusal reason.
func invalidPath(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidPath, message)
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
