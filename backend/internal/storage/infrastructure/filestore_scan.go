package storagepg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
)

// FileStore implements storageapp.GCFileStore: the GC scan/delete primitives
// and the shared-data-root write probe.
var _ storageapp.GCFileStore = (*FileStore)(nil)

// ScanObjects walks {root}/objects and returns every regular file. Symlinks and
// other reparse points are never followed (the content tree is written only by
// Put). SHA256 is filled for ledger-layout names ("<sha>" or ".<sha>.tmp-*") and
// left empty for anything else so GC ignores non-ledger files.
func (s *FileStore) ScanObjects(ctx context.Context, root string) ([]storageapp.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("storage: data root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve data root: %w", err)
	}
	objectsDir := filepath.Join(absoluteRoot, "objects")
	info, err := os.Lstat(objectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []storageapp.FileInfo{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: stat objects directory: %w", err)
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return nil, fmt.Errorf("%w: objects directory must be a regular directory", ErrInvalidPath)
	}

	files := make([]storageapp.FileInfo, 0)
	walkErr := filepath.WalkDir(objectsDir, func(currentPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !entryInfo.Mode().IsRegular() {
			// Symlinks, junctions and devices are not part of the ledger.
			return nil
		}
		relative, err := filepath.Rel(absoluteRoot, currentPath)
		if err != nil {
			return err
		}
		files = append(files, storageapp.FileInfo{
			SHA256:       ledgerSHA(entry.Name()),
			RelativePath: filepath.ToSlash(relative),
			Size:         entryInfo.Size(),
			ModTime:      entryInfo.ModTime(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("storage: scan objects: %w", walkErr)
	}
	return files, nil
}

// DeleteObject removes one root-relative blob. Absolute paths, traversal and
// path components that are links are rejected with ErrInvalidPath; a missing
// file is reported as os.ErrNotExist so GC can record it as "missing".
func (s *FileStore) DeleteObject(ctx context.Context, root, relativePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(root) == "" || strings.TrimSpace(relativePath) == "" {
		return fmt.Errorf("%w: data root and relative path are required", ErrInvalidPath)
	}
	if path.IsAbs(relativePath) || filepath.IsAbs(relativePath) {
		return fmt.Errorf("%w: relative path must not be absolute", ErrInvalidPath)
	}
	clean := path.Clean(relativePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: relative path escapes the data root", ErrInvalidPath)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("storage: resolve data root: %w", err)
	}
	fullPath := filepath.Join(absoluteRoot, filepath.FromSlash(clean))
	within, err := filepath.Rel(absoluteRoot, fullPath)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%w: relative path escapes the data root", ErrInvalidPath)
	}
	if err := rejectLinkedComponents(absoluteRoot, fullPath); err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil {
		return err
	}
	return nil
}

// ProbeWritable writes and removes {root}/.probe-<sha256(instanceKey)[:16]> to
// prove the shared data root is writable (readiness, spec §4.5). The hashed
// name avoids characters that are illegal in paths on any replica.
func (s *FileStore) ProbeWritable(ctx context.Context, root, instanceKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(root) == "" {
		return errors.New("storage: data root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("storage: resolve data root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return fmt.Errorf("storage: create data root: %w", err)
	}
	sum := sha256.Sum256([]byte(instanceKey))
	probePath := filepath.Join(absoluteRoot, ".probe-"+hex.EncodeToString(sum[:])[:16])
	file, err := os.OpenFile(probePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("storage: create writability probe: %w", err)
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(probePath)
	}
	if _, err := file.WriteString(time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		cleanup()
		return fmt.Errorf("storage: write writability probe: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(probePath)
		return fmt.Errorf("storage: close writability probe: %w", err)
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("storage: remove writability probe: %w", err)
	}
	return nil
}

// ledgerSHA extracts the content SHA from a ledger-layout file name:
// "<sha>" (published object) or ".<sha>.tmp-*" (crashed Put staging file).
// Non-ledger names return "".
func ledgerSHA(name string) string {
	if validSHA(name) {
		return name
	}
	trimmed := strings.TrimPrefix(name, ".")
	if index := strings.Index(trimmed, ".tmp-"); index > 0 {
		candidate := trimmed[:index]
		if validSHA(candidate) {
			return candidate
		}
	}
	return ""
}
