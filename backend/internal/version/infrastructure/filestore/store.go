// Package filestore implements the version attachment filesystem adapter
// (spec §6.7, D30). Every attachment file lives under one configured root and
// PostgreSQL only stores the root-relative storage_path, so the database never
// carries bytes and the P0 content-addressed object store is not involved.
//
// The layout is <root>/<versionID>/<attachmentID>_<filename>. Writes stream to a
// temporary file in the same directory while hashing and enforcing the size
// limit, are fsynced, and are atomically renamed into place, so a reader never
// observes a partially written attachment. File and directory permissions are
// 0600/0700 on platforms with Unix permission bits. Every filesystem path is
// validated against the root: relative and absolute traversal, a symlinked
// version directory and a symlink pointing outside the root are all rejected.
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
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	// dirPerm is the least-privilege mode of the attachment root and version
	// directories (spec §6.7).
	dirPerm os.FileMode = 0o700
	// filePerm is the least-privilege mode of one stored attachment.
	filePerm os.FileMode = 0o600
	// tempPrefix marks an in-flight upload so orphan temporary files are
	// identifiable after a crash.
	tempPrefix = ".upload-"
)

// Store is the filesystem attachment store. It implements
// application.AttachmentStore and is safe for concurrent use: each Save writes
// its own temporary file and renames it onto a unique attachment path.
type Store struct {
	root     string
	maxBytes int64
}

// Option customises a Store.
type Option func(*Store)

// WithMaxBytes overrides the per-file size limit. Tests use it to cover the
// limit boundary cheaply; production uses the spec §4.1 10 MiB constant.
func WithMaxBytes(maxBytes int64) Option {
	return func(s *Store) {
		if maxBytes > 0 {
			s.maxBytes = maxBytes
		}
	}
}

// New returns a store rooted at root. The root is created on the first Save
// with 0700 permissions when it does not exist; the composition root (T16)
// normally creates it from EZTEST_VERSION_ATTACHMENT_DIR.
func New(root string, opts ...Option) *Store {
	store := &Store{root: root, maxBytes: domain.MaxAttachmentBytes}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	return store
}

var _ application.AttachmentStore = (*Store)(nil)

// Save streams r to <root>/<versionID>/<attachmentID>_<filename>, enforcing the
// size limit and computing the sha256 on the way. The file is written to a
// temporary file in the destination directory, fsynced and renamed into place;
// a failed write leaves no file behind. The returned StoragePath is
// root-relative and slash-separated so it is portable across platforms.
func (s *Store) Save(ctx context.Context, versionID, attachmentID int64, filename string, r io.Reader) (application.SavedAttachment, error) {
	if err := ctx.Err(); err != nil {
		return application.SavedAttachment{}, err
	}
	if r == nil {
		return application.SavedAttachment{}, domain.NewInvalid("file", "invalid_attachment_content", "attachment content is required")
	}
	if versionID <= 0 {
		return application.SavedAttachment{}, domain.NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if attachmentID <= 0 {
		return application.SavedAttachment{}, domain.NewInvalid("attachmentId", "invalid_id", "attachmentId must be a positive id")
	}
	if err := validateFilename(filename); err != nil {
		return application.SavedAttachment{}, err
	}

	root, err := s.ensureRoot()
	if err != nil {
		return application.SavedAttachment{}, err
	}
	versionDirName := strconv.FormatInt(versionID, 10)
	versionDir, err := ensureVersionDir(root, versionDirName)
	if err != nil {
		return application.SavedAttachment{}, err
	}

	temp, err := os.CreateTemp(versionDir, tempPrefix+"*")
	if err != nil {
		return application.SavedAttachment{}, fmt.Errorf("filestore: create attachment temp file: %w", err)
	}
	tempName := temp.Name()
	committed := false
	// Any early return removes the temporary file; a successful rename makes
	// committed true and the deferred Remove a no-op for the old name.
	defer func() {
		if !committed {
			_ = temp.Close()
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(filePerm); err != nil {
		return application.SavedAttachment{}, fmt.Errorf("filestore: set attachment file mode: %w", err)
	}

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(r, s.maxBytes+1))
	if err != nil {
		return application.SavedAttachment{}, fmt.Errorf("filestore: write attachment: %w", err)
	}
	if size > s.maxBytes {
		return application.SavedAttachment{}, domain.NewInvalid("file", "attachment_too_large",
			fmt.Sprintf("attachment must be at most %d bytes", s.maxBytes))
	}
	if err := temp.Sync(); err != nil {
		return application.SavedAttachment{}, fmt.Errorf("filestore: sync attachment: %w", err)
	}
	if err := temp.Close(); err != nil {
		return application.SavedAttachment{}, fmt.Errorf("filestore: close attachment: %w", err)
	}

	targetName := strconv.FormatInt(attachmentID, 10) + "_" + filename
	target := filepath.Join(versionDir, targetName)
	if !within(root, target) {
		return application.SavedAttachment{}, domain.NewInvalid("filename", "invalid_filename",
			"attachment path escapes the attachment root")
	}
	if err := os.Rename(tempName, target); err != nil {
		return application.SavedAttachment{}, fmt.Errorf("filestore: commit attachment: %w", err)
	}
	committed = true
	// The rename is durable once the directory itself is fsynced. Windows does
	// not support flushing a directory handle, so this is best effort there.
	syncDir(versionDir)

	return application.SavedAttachment{
		StoragePath: versionDirName + "/" + targetName,
		SHA256:      hex.EncodeToString(hasher.Sum(nil)),
		Size:        size,
	}, nil
}

// Open resolves storagePath inside the root and returns the attachment content.
// Paths that escape the root (through "..", an absolute path or a symlink) are
// rejected as invalid; a missing file is reported as fs.ErrNotExist so callers
// can distinguish an orphaned metadata row from an infrastructure failure.
func (s *Store) Open(ctx context.Context, storagePath string) (io.ReadSeekCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := s.existingRoot()
	if err != nil {
		return nil, err
	}
	target, err := resolveStoragePath(root, storagePath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("filestore: open attachment: %w", err)
	}
	return file, nil
}

// Remove deletes the file behind storagePath. A missing file is not an error:
// the attachment service already committed the metadata delete and treats file
// cleanup as best effort (spec §6.7). Unsafe paths are still rejected.
func (s *Store) Remove(ctx context.Context, storagePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.existingRoot()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	target, err := resolveStoragePath(root, storagePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("filestore: remove attachment: %w", err)
	}
	return nil
}

// ensureRoot creates the attachment root when necessary and returns its
// symlink-resolved absolute path, which every confinement check uses as the
// trusted base.
func (s *Store) ensureRoot() (string, error) {
	if strings.TrimSpace(s.root) == "" {
		return "", errors.New("filestore: attachment root is not configured")
	}
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve attachment root: %w", err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return "", fmt.Errorf("filestore: create attachment root: %w", err)
	}
	root, err := s.existingRoot()
	if err != nil {
		return "", err
	}
	// Tighten a root created by an older/looser process; the mode is a no-op on
	// Windows (except for the read-only bit) but still enforces the spec's 0700
	// where it applies.
	if err := os.Chmod(root, dirPerm); err != nil {
		return "", fmt.Errorf("filestore: set attachment root mode: %w", err)
	}
	return root, nil
}

// existingRoot returns the symlink-resolved absolute root without creating it.
func (s *Store) existingRoot() (string, error) {
	if strings.TrimSpace(s.root) == "" {
		return "", errors.New("filestore: attachment root is not configured")
	}
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve attachment root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("filestore: resolve attachment root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("filestore: stat attachment root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("filestore: attachment root %q is not a directory", resolved)
	}
	return resolved, nil
}

// ensureVersionDir creates and validates <root>/<versionID>. A version
// directory that is a symlink is rejected instead of followed: it is the one
// component an attacker could pre-create to redirect writes outside the root.
func ensureVersionDir(root, versionDirName string) (string, error) {
	versionDir := filepath.Join(root, versionDirName)
	info, err := os.Lstat(versionDir)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", domain.NewInvalid("versionId", "invalid_storage_path",
				"attachment version directory must not be a symlink")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("filestore: attachment version path %q is not a directory", versionDir)
		}
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(versionDir, dirPerm); err != nil {
			return "", fmt.Errorf("filestore: create attachment version directory: %w", err)
		}
	default:
		return "", fmt.Errorf("filestore: stat attachment version directory: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(versionDir)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve attachment version directory: %w", err)
	}
	if !within(root, resolved) {
		return "", domain.NewInvalid("versionId", "invalid_storage_path",
			"attachment version directory escapes the attachment root")
	}
	// Tighten a directory created by an older/looser process; the mode is a
	// no-op on Windows but still enforces the spec's 0700 where it applies.
	if err := os.Chmod(resolved, dirPerm); err != nil {
		return "", fmt.Errorf("filestore: set attachment directory mode: %w", err)
	}
	return resolved, nil
}

// resolveStoragePath validates a root-relative storage path, rejects symlinks
// and proves the resolved target stays inside root.
func resolveStoragePath(root, storagePath string) (string, error) {
	segments, err := storagePathSegments(storagePath)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(append([]string{root}, segments...)...)
	info, err := os.Lstat(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("filestore: stat attachment: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", domain.NewInvalid("storagePath", "invalid_storage_path",
			"attachment path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return "", domain.NewInvalid("storagePath", "invalid_storage_path",
			"attachment path is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("filestore: resolve attachment path: %w", err)
	}
	if !within(root, resolved) {
		return "", domain.NewInvalid("storagePath", "invalid_storage_path",
			"attachment path escapes the attachment root")
	}
	return resolved, nil
}

// storagePathSegments splits and validates a stored path. The schema is exactly
// <versionID>/<filename>: exactly two non-empty segments, no volume, no
// absolute path, no "." or "..", no colon (a Windows drive or stream
// separator) and a positive numeric version segment.
func storagePathSegments(storagePath string) ([]string, error) {
	invalid := func() ([]string, error) {
		return nil, domain.NewInvalid("storagePath", "invalid_storage_path",
			"attachment storage path must be a relative path inside the attachment root")
	}
	if storagePath == "" || !utf8.ValidString(storagePath) {
		return invalid()
	}
	// Treat both separators as separators on every platform so a path crafted
	// for Windows cannot slip past the checks on Unix, and vice versa.
	normalized := strings.ReplaceAll(storagePath, `\`, "/")
	if strings.HasPrefix(normalized, "/") || filepath.VolumeName(storagePath) != "" ||
		strings.ContainsRune(normalized, ':') {
		return invalid()
	}
	segments := strings.Split(normalized, "/")
	if len(segments) != 2 {
		return invalid()
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return invalid()
		}
	}
	versionID, err := strconv.ParseInt(segments[0], 10, 64)
	if err != nil || versionID <= 0 {
		return invalid()
	}
	return segments, nil
}

// validateFilename rejects a filename that is not a single safe path component.
// The attachment service sanitizes user filenames before calling Save, so a
// value reaching this error is either a bug or a direct store misuse.
func validateFilename(filename string) error {
	invalid := func(message string) error {
		return domain.NewInvalid("filename", "invalid_filename", message)
	}
	if filename == "" || filename == "." || filename == ".." {
		return invalid("filename is required")
	}
	if !utf8.ValidString(filename) {
		return invalid("filename must be valid UTF-8")
	}
	if strings.ContainsAny(filename, `/\`) || strings.ContainsRune(filename, 0) ||
		strings.ContainsRune(filename, ':') || strings.ContainsAny(filename, `*?"<>|`) {
		return invalid("filename must not contain path separators or reserved characters")
	}
	if filepath.IsAbs(filename) || filepath.VolumeName(filename) != "" {
		return invalid("filename must not be an absolute path")
	}
	if utf8.RuneCountInString(filename) > domain.MaxAttachmentFilenameChars {
		return invalid(fmt.Sprintf("filename must be at most %d characters", domain.MaxAttachmentFilenameChars))
	}
	return nil
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
