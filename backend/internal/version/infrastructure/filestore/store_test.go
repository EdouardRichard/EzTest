package filestore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/filestore"
)

// testContext is the plain context every store call uses.
func testContext() context.Context { return context.Background() }

// newTestStore returns a store rooted in a fresh subdirectory of the test
// temporary directory, plus the absolute root.
func newTestStore(t *testing.T) (*filestore.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "version-attachments")
	return filestore.New(root), root
}

// assertInvalidReason asserts err is a *domain.Error with the invalid kind and
// the given reason.
func assertInvalidReason(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want invalid %q, got nil", reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	if domainErr.Kind != domain.KindInvalid || domainErr.Reason != reason {
		t.Fatalf("error kind/reason = %s/%s, want invalid/%s (%v)",
			domainErr.Kind, domainErr.Reason, reason, err)
	}
}

// dirNames returns the entry names of dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestSaveWritesRootRelativePathAndHash pins the stored layout and metadata:
// <versionID>/<attachmentID>_<filename> relative to the root, plus size and
// sha256 of the streamed content.
func TestSaveWritesRootRelativePathAndHash(t *testing.T) {
	store, root := newTestStore(t)
	content := []byte("checkout flow notes\n")

	saved, err := store.Save(testContext(), 100, 5001, "notes.txt", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	wantSHA := sha256.Sum256(content)
	if saved.StoragePath != "100/5001_notes.txt" {
		t.Fatalf("storage path = %q, want 100/5001_notes.txt", saved.StoragePath)
	}
	if saved.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", saved.Size, len(content))
	}
	if saved.SHA256 != hex.EncodeToString(wantSHA[:]) {
		t.Fatalf("sha256 = %q, want %q", saved.SHA256, hex.EncodeToString(wantSHA[:]))
	}

	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(saved.StoragePath)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("stored bytes = %q, want %q", stored, content)
	}
	if names := dirNames(t, filepath.Join(root, "100")); len(names) != 1 || names[0] != "5001_notes.txt" {
		t.Fatalf("version dir entries = %v, want [5001_notes.txt]", names)
	}
}

// TestSaveRejectsTraversalAndSymlink covers the path-confinement rules of spec
// §6.7/INV-8: neither Save nor Open may escape the attachment root, through a
// relative/absolute filename or a symlink.
func TestSaveRejectsTraversalAndSymlink(t *testing.T) {
	ctx := testContext()

	t.Run("filename traversal and absolute paths", func(t *testing.T) {
		store, root := newTestStore(t)
		for _, filename := range []string{
			"../escape.txt",
			"..\\escape.txt",
			"sub/dir.txt",
			"sub\\dir.txt",
			"/etc/passwd.txt",
			`C:\windows\evil.txt`,
			"bad*name.txt",
			"bad?name.txt",
			"bad|name.txt",
			"..",
			".",
			"",
		} {
			t.Run(filename, func(t *testing.T) {
				_, err := store.Save(ctx, 100, 5001, filename, strings.NewReader("x"))
				assertInvalidReason(t, err, "invalid_filename")
			})
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Save escaped the root: %v", err)
		}
	})

	t.Run("storage path traversal", func(t *testing.T) {
		store, root := newTestStore(t)
		saved, err := store.Save(ctx, 100, 5001, "notes.txt", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(saved.StoragePath))); err != nil {
			t.Fatalf("stored file missing: %v", err)
		}
		for _, storagePath := range []string{
			"",
			"../escape.txt",
			`..\escape.txt`,
			"/etc/passwd",
			`C:\windows\evil.txt`,
			"100/../../escape.txt",
			"./100/5001_notes.txt",
		} {
			t.Run(storagePath, func(t *testing.T) {
				_, err := store.Open(ctx, storagePath)
				assertInvalidReason(t, err, "invalid_storage_path")
			})
		}
	})

	t.Run("symlinked file cannot be read through the root", func(t *testing.T) {
		store, root := newTestStore(t)
		outside := t.TempDir()
		secret := filepath.Join(outside, "secret.txt")
		if err := os.WriteFile(secret, []byte("outside the root"), 0o600); err != nil {
			t.Fatalf("write outside file: %v", err)
		}
		versionDir := filepath.Join(root, "100")
		if err := os.MkdirAll(versionDir, 0o700); err != nil {
			t.Fatalf("mkdir version dir: %v", err)
		}
		link := filepath.Join(versionDir, "5001_secret.txt")
		if err := os.Symlink(secret, link); err != nil {
			t.Skipf("symlink creation is not permitted on this host: %v", err)
		}

		if _, err := store.Open(ctx, "100/5001_secret.txt"); err == nil {
			t.Fatal("Open followed a symlink outside the attachment root")
		}
	})

	t.Run("symlinked version directory is rejected by Save", func(t *testing.T) {
		store, root := newTestStore(t)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		outside := t.TempDir()
		link := filepath.Join(root, "100")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink creation is not permitted on this host: %v", err)
		}

		if _, err := store.Save(ctx, 100, 5001, "notes.txt", strings.NewReader("x")); err == nil {
			t.Fatal("Save wrote through a symlinked version directory")
		}
		if names := dirNames(t, outside); len(names) != 0 {
			t.Fatalf("Save wrote outside the root: %v", names)
		}
	})
}

// TestSaveEnforces10MiB covers the 10 MiB per-file limit (spec §4.1/§6.7). The
// boundary is inclusive and a rejected upload leaves no file behind.
func TestSaveEnforces10MiB(t *testing.T) {
	store, root := newTestStore(t)
	ctx := testContext()

	exact := bytes.Repeat([]byte("a"), domain.MaxAttachmentBytes)
	saved, err := store.Save(ctx, 100, 5001, "exact.txt", bytes.NewReader(exact))
	if err != nil {
		t.Fatalf("Save exactly 10 MiB: %v", err)
	}
	if saved.Size != domain.MaxAttachmentBytes {
		t.Fatalf("size = %d, want %d", saved.Size, domain.MaxAttachmentBytes)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(saved.StoragePath)))
	if err != nil {
		t.Fatalf("stat stored file: %v", err)
	}
	if info.Size() != domain.MaxAttachmentBytes {
		t.Fatalf("stored file size = %d, want %d", info.Size(), domain.MaxAttachmentBytes)
	}

	over := bytes.Repeat([]byte("b"), domain.MaxAttachmentBytes+1)
	if _, err := store.Save(ctx, 100, 5002, "over.txt", bytes.NewReader(over)); err == nil {
		t.Fatal("Save accepted a file larger than 10 MiB")
	} else {
		assertInvalidReason(t, err, "attachment_too_large")
	}

	// Only the accepted file remains: the rejected stream's temporary file was
	// removed before Save returned.
	names := dirNames(t, filepath.Join(root, "100"))
	if len(names) != 1 || names[0] != "5001_exact.txt" {
		t.Fatalf("version dir entries = %v, want only the accepted file", names)
	}
}

// TestSaveMaxBytesOption covers the injectable limit: the boundary is
// inclusive and the option is independent of the production 10 MiB constant.
func TestSaveMaxBytesOption(t *testing.T) {
	store := filestore.New(filepath.Join(t.TempDir(), "version-attachments"), filestore.WithMaxBytes(4))
	ctx := testContext()

	if _, err := store.Save(ctx, 100, 5001, "four.txt", strings.NewReader("abcd")); err != nil {
		t.Fatalf("Save at the injected limit: %v", err)
	}
	if _, err := store.Save(ctx, 100, 5002, "five.txt", strings.NewReader("abcde")); err == nil {
		t.Fatal("Save accepted a file over the injected limit")
	} else {
		assertInvalidReason(t, err, "attachment_too_large")
	}
}

// TestSaveAtomicAndPermissions covers the atomic write contract of spec §6.7:
// the file appears only complete, no temporary file leaks, a reader failure
// cleans up, and the file/directory permissions are 0600/0700 on platforms that
// model Unix permission bits.
func TestSaveAtomicAndPermissions(t *testing.T) {
	ctx := testContext()

	t.Run("success leaves no temporary file", func(t *testing.T) {
		store, root := newTestStore(t)
		if _, err := store.Save(ctx, 100, 5001, "notes.txt", strings.NewReader("hello")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if names := dirNames(t, filepath.Join(root, "100")); len(names) != 1 || names[0] != "5001_notes.txt" {
			t.Fatalf("version dir entries = %v, want [5001_notes.txt]", names)
		}
	})

	t.Run("reader failure removes the temporary file", func(t *testing.T) {
		store, root := newTestStore(t)
		_, err := store.Save(ctx, 100, 5001, "notes.txt", &failingReader{data: []byte("partial")})
		if err == nil {
			t.Fatal("Save accepted a reader that failed mid-stream")
		}
		if names := dirNames(t, filepath.Join(root, "100")); len(names) != 0 {
			t.Fatalf("failed Save left files behind: %v", names)
		}
	})

	t.Run("file and directory modes", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not expose Unix permission bits through os.FileMode")
		}
		store, root := newTestStore(t)
		saved, err := store.Save(ctx, 100, 5001, "notes.txt", strings.NewReader("hello"))
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		fileInfo, err := os.Stat(filepath.Join(root, filepath.FromSlash(saved.StoragePath)))
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		if mode := fileInfo.Mode().Perm(); mode != 0o600 {
			t.Fatalf("file mode = %o, want 600", mode)
		}
		for _, dir := range []string{root, filepath.Join(root, "100")} {
			dirInfo, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("stat dir %s: %v", dir, err)
			}
			if mode := dirInfo.Mode().Perm(); mode != 0o700 {
				t.Fatalf("dir %s mode = %o, want 700", dir, mode)
			}
		}
	})
}

// TestEnsureRootTightensExistingMode covers the root permission rule: an
// already-existing root is chmod-ed to 0700 where Unix permission bits apply.
// Windows only models the read-only bit, so the same code path is observed there
// by making the root read-only and checking that Save has made it writable
// again.
func TestEnsureRootTightensExistingMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "version-attachments")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("pre-create root: %v", err)
	}
	loose := os.FileMode(0o755)
	if runtime.GOOS == "windows" {
		loose = 0o500
	}
	if err := os.Chmod(root, loose); err != nil {
		t.Fatalf("pre-create looser root mode: %v", err)
	}

	store := filestore.New(root)
	if _, err := store.Save(testContext(), 100, 5001, "notes.txt", strings.NewReader("hello")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if runtime.GOOS == "windows" {
		if info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("root mode = %o, want the write bit restored by the 0700 chmod", info.Mode().Perm())
		}
		return
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("root mode = %o, want 700", mode)
	}
}

// TestOpenAndRemove covers the read/remove half of the port: Open is seekable,
// a missing file is fs.ErrNotExist, Remove deletes and is idempotent.
func TestOpenAndRemove(t *testing.T) {
	store, root := newTestStore(t)
	ctx := testContext()
	content := []byte("seekable content")
	saved, err := store.Save(ctx, 100, 5001, "notes.txt", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	readCloser, err := store.Open(ctx, saved.StoragePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first, err := io.ReadAll(readCloser)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(first, content) {
		t.Fatalf("content = %q, want %q", first, content)
	}
	if _, err := readCloser.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	second, err := io.ReadAll(readCloser)
	if err != nil {
		t.Fatalf("ReadAll after Seek: %v", err)
	}
	if !bytes.Equal(second, content) {
		t.Fatalf("content after Seek = %q, want %q", second, content)
	}
	if err := readCloser.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := store.Remove(ctx, saved.StoragePath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(saved.StoragePath))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still exists after Remove: %v", err)
	}
	if _, err := store.Open(ctx, saved.StoragePath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open missing file error = %v, want fs.ErrNotExist", err)
	}
	if err := store.Remove(ctx, saved.StoragePath); err != nil {
		t.Fatalf("Remove missing file must be a no-op, got %v", err)
	}

	var domainErr *domain.Error
	if err := store.Remove(ctx, "../escape.txt"); !errors.As(err, &domainErr) ||
		domainErr.Reason != "invalid_storage_path" {
		t.Fatalf("Remove traversal error = %v, want invalid_storage_path", err)
	}
}

// failingReader yields one chunk and then fails, simulating a dropped client
// connection during the streaming write.
type failingReader struct {
	data []byte
	sent bool
}

// Read returns the payload once and then an error.
func (r *failingReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("connection reset")
	}
	r.sent = true
	return copy(p, r.data), nil
}
