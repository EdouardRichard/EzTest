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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/filestore"
)

// newEvidenceStore returns a store rooted in a fresh subdirectory of the test
// temporary directory, plus the absolute root.
func newEvidenceStore(t *testing.T) (*filestore.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "evidence")
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, root
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

// evidenceRel is the canonical evidence relative path of spec §5.7:
// {batch_id}/{case_run_id}/{evidence_id}.{ext}.
const evidenceRel = "100/200/3001.png"

// TestStoreWriteOpenRoundTrip pins the stored layout and metadata: the returned
// path is the root-relative slash path, sha256/size describe the bytes, and Open
// reads the same bytes back.
func TestStoreWriteOpenRoundTrip(t *testing.T) {
	store, root := newEvidenceStore(t)
	content := []byte("\x89PNG\r\n\x1a\nevidence-bytes")

	path, digest, size, err := store.Write(context.Background(), evidenceRel, content)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != evidenceRel {
		t.Fatalf("path = %q, want %q", path, evidenceRel)
	}
	wantSHA := sha256.Sum256(content)
	if digest != hex.EncodeToString(wantSHA[:]) {
		t.Fatalf("sha256 = %q, want %q", digest, hex.EncodeToString(wantSHA[:]))
	}
	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}

	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(evidenceRel)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("stored bytes = %q, want %q", stored, content)
	}

	reader, err := store.Open(context.Background(), evidenceRel)
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
	store, root := newEvidenceStore(t)
	content := []byte("evidence")

	for _, rel := range []string{
		"",
		"../escape.png",
		`..\escape.png`,
		"/etc/passwd",
		`C:\windows\evil.png`,
		"100/../../escape.png",
		"100/200/../../../escape.png",
		"./100/200/3001.png",
		"100//3001.png",
		"100/200/3001.png:stream",
		"100/200/",
		"100/200/.",
		"100/200/..",
		"100/200/3001\x00.png",
	} {
		t.Run(rel, func(t *testing.T) {
			_, _, _, err := store.Write(context.Background(), rel, content)
			if !errors.Is(err, filestore.ErrInvalidPath) {
				t.Fatalf("Write(%q) error = %v, want ErrInvalidPath", rel, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.png")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Write escaped the root: %v", err)
	}
}

// TestStoreWriteEnforcesSizeLimit covers the per-evidence size cap: the
// boundary is inclusive and an oversized buffer is rejected before anything is
// written, leaving no committed file and no temporary file behind.
func TestStoreWriteEnforcesSizeLimit(t *testing.T) {
	if filestore.MaxFileBytes != 10<<20 {
		t.Fatalf("MaxFileBytes = %d, want %d", filestore.MaxFileBytes, 10<<20)
	}

	root := filepath.Join(t.TempDir(), "evidence")
	store, err := filestore.New(root, filestore.WithMaxBytes(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, _, size, err := store.Write(context.Background(), "1/2/1.png", []byte("abcd")); err != nil {
		t.Fatalf("Write at the injected limit: %v", err)
	} else if size != 4 {
		t.Fatalf("size = %d, want 4", size)
	}
	if _, _, _, err := store.Write(context.Background(), "1/2/2.png", []byte("abcde")); !errors.Is(err, filestore.ErrFileTooLarge) {
		t.Fatalf("Write over the limit error = %v, want ErrFileTooLarge", err)
	}
	// The rejected write left no file and no temporary file behind.
	if names := dirNames(t, filepath.Join(root, "1", "2")); len(names) != 1 || names[0] != "1.png" {
		t.Fatalf("evidence dir entries = %v, want only the accepted file", names)
	}
}

// TestStoreWriteAtomicNoTempLeak pins the temporary-file + atomic rename
// contract: after a successful write the destination directory holds exactly the
// committed file, never the in-flight temporary file.
func TestStoreWriteAtomicNoTempLeak(t *testing.T) {
	store, root := newEvidenceStore(t)
	if _, _, _, err := store.Write(context.Background(), evidenceRel, []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	names := dirNames(t, filepath.Join(root, "100", "200"))
	if len(names) != 1 || names[0] != "3001.png" {
		t.Fatalf("evidence dir entries = %v, want [3001.png]", names)
	}
}

// TestStoreConcurrentFirstWritesSameDirectory pins the ensureDir race: two
// goroutines writing different evidence files into the same not-yet-existing
// case-run directory must both succeed.
func TestStoreConcurrentFirstWritesSameDirectory(t *testing.T) {
	store, root := newEvidenceStore(t)
	for round := 0; round < 20; round++ {
		dir := "1/" + strconv.Itoa(round+1) + "/"
		rels := []string{dir + "1.png", dir + "2.json"}
		start := make(chan struct{})
		errs := make(chan error, len(rels))
		var wait sync.WaitGroup
		for i, rel := range rels {
			wait.Add(1)
			go func(content, rel string) {
				defer wait.Done()
				<-start
				if _, _, _, err := store.Write(context.Background(), rel, []byte(content)); err != nil {
					errs <- err
				}
			}("content "+strconv.Itoa(i), rel)
		}
		close(start)
		wait.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: concurrent first Write: %v", round, err)
		}
		for _, rel := range rels {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatalf("round %d: stored file %q: %v", round, rel, err)
			}
		}
	}
}

// TestStoreWriteDirectoryModes covers the 0600 file / 0700 directory rule on
// platforms that expose Unix permission bits.
func TestStoreWriteDirectoryModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits through os.FileMode")
	}
	store, root := newEvidenceStore(t)
	if _, _, _, err := store.Write(context.Background(), evidenceRel, []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fileInfo, err := os.Stat(filepath.Join(root, filepath.FromSlash(evidenceRel)))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %o, want 600", mode)
	}
	for _, dir := range []string{
		root,
		filepath.Join(root, "100"),
		filepath.Join(root, "100", "200"),
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
	store, root := newEvidenceStore(t)
	if err := os.MkdirAll(filepath.Join(root, "100"), 0o700); err != nil {
		t.Fatalf("mkdir 100: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "100", "200")); err != nil {
		t.Skipf("symlink creation is not permitted on this host: %v", err)
	}

	if _, _, _, err := store.Write(context.Background(), evidenceRel, []byte("x")); err == nil {
		t.Fatal("Write followed a symlinked evidence directory")
	}
	if names := dirNames(t, outside); len(names) != 0 {
		t.Fatalf("Write escaped the root: %v", names)
	}
}

// TestStoreOpenRejectsSymlinkedFile proves a symlink pointing outside the root
// cannot be read through Open.
func TestStoreOpenRejectsSymlinkedFile(t *testing.T) {
	store, root := newEvidenceStore(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.png")
	if err := os.WriteFile(secret, []byte("outside the root"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	dir := filepath.Join(root, "100", "200")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "3001.png")); err != nil {
		t.Skipf("symlink creation is not permitted on this host: %v", err)
	}

	if _, err := store.Open(context.Background(), evidenceRel); err == nil {
		t.Fatal("Open followed a symlink outside the evidence root")
	}
}

// TestStoreWriteRejectsWindowsJunction covers the Windows directory-junction
// variant of the symlink escape. The write must be rejected and nothing may land
// outside the root.
func TestStoreWriteRejectsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions only exist on Windows")
	}
	store, root := newEvidenceStore(t)
	if err := os.MkdirAll(filepath.Join(root, "100"), 0o700); err != nil {
		t.Fatalf("mkdir 100: %v", err)
	}
	outside := t.TempDir()
	junction := filepath.Join(root, "100", "200")
	command := exec.Command("cmd", "/c", "mklink", "/J", junction, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("junction creation is not permitted on this host: %v (%s)", err, output)
	}

	if _, _, _, err := store.Write(context.Background(), evidenceRel, []byte("x")); err == nil {
		t.Fatal("Write followed a directory junction outside the evidence root")
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
	store, root := newEvidenceStore(t)
	if err := os.MkdirAll(filepath.Join(root, "100", "200"), 0o700); err != nil {
		t.Fatalf("mkdir evidence dirs: %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "3001.png"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	junction := filepath.Join(root, "100", "200")
	command := exec.Command("cmd", "/c", "mklink", "/J", junction, outside)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("junction creation is not permitted on this host: %v (%s)", err, output)
	}

	if reader, err := store.Open(context.Background(), evidenceRel); err == nil {
		reader.Close()
		t.Fatal("Open served a file reached through a junction outside the evidence root")
	}
}

// TestStoreOpenMissingAndRemove covers the read/remove half of the store: a
// missing file is fs.ErrNotExist, Remove deletes and is idempotent, and an
// unsafe path is rejected by both.
func TestStoreOpenMissingAndRemove(t *testing.T) {
	store, root := newEvidenceStore(t)
	if _, _, _, err := store.Write(context.Background(), evidenceRel, []byte("content")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := store.Remove(context.Background(), evidenceRel); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(evidenceRel))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still exists after Remove: %v", err)
	}
	if _, err := store.Open(context.Background(), evidenceRel); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open missing file error = %v, want fs.ErrNotExist", err)
	}
	if err := store.Remove(context.Background(), evidenceRel); err != nil {
		t.Fatalf("Remove missing file must be a no-op, got %v", err)
	}
	if err := store.Remove(context.Background(), "../escape.png"); !errors.Is(err, filestore.ErrInvalidPath) {
		t.Fatalf("Remove traversal error = %v, want ErrInvalidPath", err)
	}
	if _, err := store.Open(context.Background(), "../escape.png"); !errors.Is(err, filestore.ErrInvalidPath) {
		t.Fatalf("Open traversal error = %v, want ErrInvalidPath", err)
	}
}

// TestStoreNewRequiresRoot covers configuration validation.
func TestStoreNewRequiresRoot(t *testing.T) {
	for _, root := range []string{"", "   "} {
		if _, err := filestore.New(root); err == nil {
			t.Fatalf("New(%q) accepted an empty root", root)
		}
	}
	if _, err := filestore.New(filepath.Join(t.TempDir(), "evidence")); err != nil {
		t.Fatalf("New(valid root): %v", err)
	}
}

// TestStoreWriteOverwritesAtomically covers re-writing the same relative path:
// the old content is fully replaced, never partially merged.
func TestStoreWriteOverwritesAtomically(t *testing.T) {
	store, root := newEvidenceStore(t)
	if _, _, _, err := store.Write(context.Background(), evidenceRel, bytes.Repeat([]byte("a"), 64)); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if _, _, _, err := store.Write(context.Background(), evidenceRel, []byte("b")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(evidenceRel)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(stored) != "b" {
		t.Fatalf("stored = %q, want \"b\"", stored)
	}
	if names := dirNames(t, filepath.Join(root, "100", "200")); len(names) != 1 {
		t.Fatalf("evidence dir entries = %v, want a single file", names)
	}
}
