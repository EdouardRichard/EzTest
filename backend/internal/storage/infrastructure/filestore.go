// Package storagepg is the PostgreSQL + filesystem adapter for the content
// ledger.
package storagepg

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// FileStore writes immutable content blobs to a sharded, content-addressed
// tree under a configured root.
type FileStore struct{}

// NewFileStore builds a stateless file store; the root is supplied per call so
// it always reflects the current paths.data_root setting.
func NewFileStore() *FileStore {
	return &FileStore{}
}

// Put atomically writes data at objects/{sha[0:2]}/{sha[2:4]}/{sha} under root
// and returns that slash-separated relative path. Files are created 0600 and
// directories 0700; content is fsynced before the rename.
func (s *FileStore) Put(ctx context.Context, root, sha string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validSHA(sha) {
		return "", fmt.Errorf("storage: invalid sha256 %q", sha)
	}
	if strings.TrimSpace(root) == "" {
		return "", errors.New("storage: data root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("storage: resolve data root: %w", err)
	}

	relativePath := path.Join("objects", sha[0:2], sha[2:4], sha)
	finalPath := filepath.Join(absoluteRoot, filepath.FromSlash(relativePath))
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return "", fmt.Errorf("storage: create data root: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("storage: create object shard: %w", err)
	}

	temp, err := os.CreateTemp(dir, "."+sha+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("storage: create temp file: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return "", fmt.Errorf("storage: write temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("storage: sync temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return "", fmt.Errorf("storage: close temp file: %w", err)
	}
	if err := os.Rename(tempName, finalPath); err != nil {
		// Content is immutable and addressed by its hash, so an existing final
		// file (possible when two uploads race, or on Windows where rename does
		// not replace) is an acceptable outcome.
		if _, statErr := os.Stat(finalPath); statErr == nil {
			_ = os.Remove(tempName)
			return relativePath, nil
		}
		_ = os.Remove(tempName)
		return "", fmt.Errorf("storage: publish object: %w", err)
	}
	return relativePath, nil
}

// ErrInvalidPath marks a relative path that is absolute, escapes the data root
// lexically, or resolves through a symlink outside it.
var ErrInvalidPath = errors.New("storage: invalid path")

// Open opens a relative path produced by Put. Absolute paths, traversal and
// symlinks that escape the data root are rejected with ErrInvalidPath.
func (s *FileStore) Open(ctx context.Context, root, relativePath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" || strings.TrimSpace(relativePath) == "" {
		return nil, fmt.Errorf("%w: data root and relative path are required", ErrInvalidPath)
	}
	if path.IsAbs(relativePath) || filepath.IsAbs(relativePath) {
		return nil, fmt.Errorf("%w: relative path must not be absolute", ErrInvalidPath)
	}
	clean := path.Clean(relativePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, fmt.Errorf("%w: relative path escapes the data root", ErrInvalidPath)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve data root: %w", err)
	}
	// Resolve the root once so a configured root that is itself a symlink is
	// compared against the canonical tree it points at.
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve data root: %w", err)
	}
	fullPath := filepath.Join(absoluteRoot, filepath.FromSlash(clean))
	within, err := filepath.Rel(absoluteRoot, fullPath)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: relative path escapes the data root", ErrInvalidPath)
	}
	// Reject every link-like component below the root before opening the file.
	// Lstat does not follow the final component, and Go's EvalSymlinks ignores
	// Windows junctions, so both checks are needed to block a link placed inside
	// the tree that redirects outside it.
	if err := rejectLinkedComponents(absoluteRoot, fullPath); err != nil {
		return nil, err
	}
	// EvalSymlinks resolves intermediate components as a second line of
	// defense (also catches a link created between the Lstat walk and the open).
	resolvedPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return nil, err
	}
	within, err = filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: relative path escapes the data root", ErrInvalidPath)
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// rejectLinkedComponents walks the path components below root and rejects any
// symlink, Windows junction or other reparse-point indirection: the content
// tree is written exclusively by Put and must contain only regular files.
// os.Lstat reports junctions as ModeIrregular because Go does not follow them
// in EvalSymlinks.
func rejectLinkedComponents(root, fullPath string) error {
	relative, err := filepath.Rel(root, fullPath)
	if err != nil {
		return fmt.Errorf("%w: relative path escapes the data root", ErrInvalidPath)
	}
	current := root
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("%w: linked path components are not allowed", ErrInvalidPath)
		}
	}
	return nil
}

func validSHA(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
