package filestore_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/filestore"
)

// newExportStore returns a store rooted in a fresh subdirectory of the test
// temporary directory, plus the absolute root.
func newExportStore(t *testing.T) (*filestore.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "exports")
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, root
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

// exportRel is the canonical export relative path of the spec §6.10 layout.
const exportRel = "versions/100/draft/markdown-5001.md"

// TestStoreWriteOpenRoundTrip pins the stored layout and metadata: the returned
// path is the root-relative slash path, sha256/size describe the bytes, and Open
// reads the same content back.
func TestStoreWriteOpenRoundTrip(t *testing.T) {
	store, root := newExportStore(t)
	content := []byte("# 用例脑图\n\n- 版本：订单\n")

	path, digest, size, err := store.Write(exportRel, content)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != exportRel {
		t.Fatalf("path = %q, want %q", path, exportRel)
	}
	wantSHA := sha256.Sum256(content)
	if digest != hex.EncodeToString(wantSHA[:]) {
		t.Fatalf("sha256 = %q, want %q", digest, hex.EncodeToString(wantSHA[:]))
	}
	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}

	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(exportRel)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("stored bytes = %q, want %q", stored, content)
	}

	reader, err := store.Open(exportRel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	readBack, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(readBack, content) {
		t.Fatalf("read back = %q, want %q", readBack, content)
	}
}

// TestStoreWriteRejectsTraversal covers the root confinement of D30: no relative
// or absolute traversal, drive letters, stream separators or empty segments.
func TestStoreWriteRejectsTraversal(t *testing.T) {
	store, root := newExportStore(t)
	content := []byte("export")

	for _, rel := range []string{
		"",
		"../escape.md",
		`..\escape.md`,
		"/etc/passwd",
		`C:\windows\evil.md`,
		"versions/../../escape.md",
		"versions/100/draft/../../../escape.md",
		"./versions/100/draft/export.md",
		"versions//export.md",
		"versions/100/draft/export.md:stream",
		"versions/100/draft/",
		"versions/100/draft/.",
		"versions/100/draft/..",
		"versions/100/draft/export\x00.md",
	} {
		t.Run(rel, func(t *testing.T) {
			_, _, _, err := store.Write(rel, content)
			assertInvalidReason(t, err, "invalid_storage_path")
		})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Write escaped the root: %v", err)
	}
}

// TestStoreWriteAtomicNoTempLeak covers the temporary-file + rename contract:
// after a successful write the directory holds exactly the committed file, and a
// rejected oversize write leaves nothing behind.
func TestStoreWriteAtomicNoTempLeak(t *testing.T) {
	store, root := newExportStore(t)

	if _, _, _, err := store.Write(exportRel, []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if names := dirNames(t, filepath.Join(root, "versions", "100", "draft")); len(names) != 1 ||
		names[0] != "markdown-5001.md" {
		t.Fatalf("export dir entries = %v, want [markdown-5001.md]", names)
	}
}

// TestStoreWriteEnforcesSizeLimit covers the 20 MiB per-file cap: the boundary
// is inclusive and an oversized buffer is rejected before anything is written.
func TestStoreWriteEnforcesSizeLimit(t *testing.T) {
	if filestore.MaxFileBytes != 20<<20 {
		t.Fatalf("MaxFileBytes = %d, want %d", filestore.MaxFileBytes, 20<<20)
	}

	root := filepath.Join(t.TempDir(), "exports")
	store, err := filestore.New(root, filestore.WithMaxBytes(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, _, size, err := store.Write("versions/1/draft/opml-1.opml", []byte("abcd")); err != nil {
		t.Fatalf("Write at the injected limit: %v", err)
	} else if size != 4 {
		t.Fatalf("size = %d, want 4", size)
	}
	if _, _, _, err := store.Write("versions/1/draft/opml-2.opml", []byte("abcde")); err == nil {
		t.Fatal("Write accepted a payload over the injected limit")
	}
	// The rejected write left no file and no temporary file behind.
	if names := dirNames(t, filepath.Join(root, "versions", "1", "draft")); len(names) != 1 ||
		names[0] != "opml-1.opml" {
		t.Fatalf("export dir entries = %v, want only the accepted file", names)
	}
}

// TestStoreWriteDirectoryModes covers the 0600 file / 0700 directory rule on
// platforms that expose Unix permission bits.
func TestStoreWriteDirectoryModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits through os.FileMode")
	}
	store, root := newExportStore(t)
	if _, _, _, err := store.Write(exportRel, []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fileInfo, err := os.Stat(filepath.Join(root, filepath.FromSlash(exportRel)))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %o, want 600", mode)
	}
	for _, dir := range []string{
		root,
		filepath.Join(root, "versions"),
		filepath.Join(root, "versions", "100"),
		filepath.Join(root, "versions", "100", "draft"),
	} {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat dir %s: %v", dir, err)
		}
		if mode := dirInfo.Mode().Perm(); mode != 0o700 {
			t.Fatalf("dir %s mode = %o, want 700", dir, mode)
		}
	}
}

// TestStoreWriteRejectsSymlinkedDirectory proves a pre-created symlink cannot
// redirect a write outside the root.
func TestStoreWriteRejectsSymlinkedDirectory(t *testing.T) {
	store, root := newExportStore(t)
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o700); err != nil {
		t.Fatalf("mkdir versions: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "versions", "100")); err != nil {
		t.Skipf("symlink creation is not permitted on this host: %v", err)
	}

	if _, _, _, err := store.Write(exportRel, []byte("x")); err == nil {
		t.Fatal("Write followed a symlinked export directory")
	}
	if names := dirNames(t, outside); len(names) != 0 {
		t.Fatalf("Write escaped the root: %v", names)
	}
}

// TestStoreOpenRejectsSymlinkedFile proves a symlink pointing outside the root
// cannot be read through Open.
func TestStoreOpenRejectsSymlinkedFile(t *testing.T) {
	store, root := newExportStore(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("outside the root"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	dir := filepath.Join(root, "versions", "100", "draft")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir export dir: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "markdown-1.md")); err != nil {
		t.Skipf("symlink creation is not permitted on this host: %v", err)
	}

	if _, err := store.Open("versions/100/draft/markdown-1.md"); err == nil {
		t.Fatal("Open followed a symlink outside the export root")
	}
}

// TestStoreWriteRejectsWindowsJunction covers the Windows directory-junction
// variant of the symlink escape (a junction can be created without elevation,
// unlike a symlink). The write must be rejected and nothing may land outside the
// root.
func TestStoreWriteRejectsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions only exist on Windows")
	}
	store, root := newExportStore(t)
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o700); err != nil {
		t.Fatalf("mkdir versions: %v", err)
	}
	outside := t.TempDir()
	junction := filepath.Join(root, "versions", "100")
	command := exec.Command("cmd", "/c", "mklink", "/J", junction, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("junction creation is not permitted on this host: %v (%s)", err, output)
	}

	if _, _, _, err := store.Write(exportRel, []byte("x")); err == nil {
		t.Fatal("Write followed a directory junction outside the export root")
	}
	if names := dirNames(t, outside); len(names) != 0 {
		t.Fatalf("Write escaped the root through a junction: %v", names)
	}
}

// TestStoreOpenRejectsWindowsJunction covers the read half of the junction
// escape: a file that lives outside the root but is reachable through a
// junction inside the root must not be served.
func TestStoreOpenRejectsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions only exist on Windows")
	}
	store, root := newExportStore(t)
	if err := os.MkdirAll(filepath.Join(root, "versions", "100"), 0o700); err != nil {
		t.Fatalf("mkdir export dirs: %v", err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "draft"), 0o700); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "draft", "markdown-1.md"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	junction := filepath.Join(root, "versions", "100", "draft")
	command := exec.Command("cmd", "/c", "mklink", "/J", junction, filepath.Join(outside, "draft"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("junction creation is not permitted on this host: %v (%s)", err, output)
	}

	if reader, err := store.Open("versions/100/draft/markdown-1.md"); err == nil {
		reader.Close()
		t.Fatal("Open served a file reached through a junction outside the export root")
	}
}

// TestStoreOpenMissingAndRemove covers the read/remove half of the port: a
// missing file is fs.ErrNotExist, Remove deletes and is idempotent, and an
// unsafe path is rejected.
func TestStoreOpenMissingAndRemove(t *testing.T) {
	store, root := newExportStore(t)
	if _, _, _, err := store.Write(exportRel, []byte("content")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := store.Remove(exportRel); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(exportRel))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still exists after Remove: %v", err)
	}
	if _, err := store.Open(exportRel); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open missing file error = %v, want fs.ErrNotExist", err)
	}
	if err := store.Remove(exportRel); err != nil {
		t.Fatalf("Remove missing file must be a no-op, got %v", err)
	}
	assertInvalidReason(t, store.Remove("../escape.md"), "invalid_storage_path")
	if _, err := store.Open("../escape.md"); err == nil {
		t.Fatal("Open accepted a traversal path")
	} else {
		assertInvalidReason(t, err, "invalid_storage_path")
	}
}

// TestStoreNewRequiresRoot covers configuration validation.
func TestStoreNewRequiresRoot(t *testing.T) {
	for _, root := range []string{"", "   "} {
		if _, err := filestore.New(root); err == nil {
			t.Fatalf("New(%q) accepted an empty root", root)
		}
	}
	if _, err := filestore.New(filepath.Join(t.TempDir(), "exports")); err != nil {
		t.Fatalf("New(valid root): %v", err)
	}
}

// TestStoreWriteOverwritesAtomically covers re-writing the same relative path:
// the old content is fully replaced, never partially merged.
func TestStoreWriteOverwritesAtomically(t *testing.T) {
	store, root := newExportStore(t)
	if _, _, _, err := store.Write(exportRel, bytes.Repeat([]byte("a"), 64)); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if _, _, _, err := store.Write(exportRel, []byte("b")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(exportRel)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(stored) != "b" {
		t.Fatalf("stored = %q, want \"b\"", stored)
	}
	if names := dirNames(t, filepath.Join(root, "versions", "100", "draft")); len(names) != 1 {
		t.Fatalf("export dir entries = %v, want a single file", names)
	}
}
