package storagepg_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
)

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestFileStorePutShardsAtomicallyWithRestrictedModes(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()
	data := []byte("stored content")
	sha := shaOf(data)

	rel, err := store.Put(context.Background(), root, sha, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := "objects/" + sha[0:2] + "/" + sha[2:4] + "/" + sha
	if rel != want {
		t.Fatalf("relative path = %q, want %q", rel, want)
	}

	full := filepath.Join(root, filepath.FromSlash(rel))
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("stored content = %q", got)
	}

	entries, err := os.ReadDir(filepath.Dir(full))
	if err != nil {
		t.Fatalf("read shard dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != sha {
		t.Fatalf("shard dir entries = %#v", entries)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(full)
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("file mode = %o, want 600", mode)
		}
		dirInfo, err := os.Stat(filepath.Dir(full))
		if err != nil {
			t.Fatalf("stat shard dir: %v", err)
		}
		if mode := dirInfo.Mode().Perm(); mode != 0o700 {
			t.Fatalf("shard dir mode = %o, want 700", mode)
		}
	}
}

func TestFileStoreOpenRoundTrip(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()
	data := []byte("open me")
	sha := shaOf(data)
	rel, err := store.Put(context.Background(), root, sha, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	reader, err := store.Open(context.Background(), root, rel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q", got)
	}
}

func TestFileStoreOpenRejectsMissingAndTraversal(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()

	if _, err := store.Open(context.Background(), root, "objects/aa/bb/missing"); err == nil {
		t.Fatal("missing file: expected error")
	}
	if _, err := store.Open(context.Background(), root, "../../etc/passwd"); err == nil {
		t.Fatal("path traversal: expected error")
	}
}

func TestFileStoreOpenRootFile(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()
	file := filepath.Join(root, "report.html")
	if err := os.WriteFile(file, []byte("<html>ok</html>"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	reader, err := store.Open(context.Background(), root, "report.html")
	if err != nil {
		t.Fatalf("Open root file: %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read root file: %v", err)
	}
	if string(got) != "<html>ok</html>" {
		t.Fatalf("root file content = %q", got)
	}
}

func TestFileStoreOpenRejectsAbsoluteAndEscapingPaths(t *testing.T) {
	store := storagepg.NewFileStore()
	ctx := context.Background()
	root := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(absolute, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	paths := []string{
		"..",
		"../outside.txt",
		"objects/../../outside.txt",
		absolute,
	}
	if runtime.GOOS == "windows" {
		// Windows separators are valid in file names for path.Clean but become
		// traversal separators in filepath.Join; both must be rejected.
		paths = append(paths, `..\..\outside.txt`)
	}
	for _, relativePath := range paths {
		reader, err := store.Open(ctx, root, relativePath)
		if err == nil {
			_ = reader.Close()
			t.Fatalf("Open(%q) escaped the data root", relativePath)
		}
		if !errors.Is(err, storagepg.ErrInvalidPath) {
			t.Fatalf("Open(%q) error = %v, want ErrInvalidPath", relativePath, err)
		}
	}
}

func TestFileStoreOpenRejectsSymlinkEscape(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	checked := 0
	if err := os.Symlink(secret, filepath.Join(root, "escape.txt")); err == nil {
		checked++
		assertOpenRejectsLink(t, store, root, "escape.txt", "symlinked file")
	} else if runtime.GOOS != "windows" {
		t.Fatalf("create file symlink: %v", err)
	}

	// Directory links cover the intermediate-component escape. On Windows
	// without Developer Mode they are created as unprivileged junctions, which
	// Go reports as irregular reparse points instead of symlinks.
	if err := createDirLink(filepath.Join(root, "linked-dir"), outside); err == nil {
		checked++
		assertOpenRejectsLink(t, store, root, "linked-dir/secret.txt", "symlinked directory")
	}
	if err := createDirLink(filepath.Join(root, "escape-dir"), outside); err == nil {
		checked++
		assertOpenRejectsLink(t, store, root, "escape-dir", "linked final component")
	}
	if checked == 0 {
		t.Skip("host cannot create symlinks or junctions")
	}
}

func assertOpenRejectsLink(t *testing.T, store *storagepg.FileStore, root, relativePath, kind string) {
	t.Helper()
	reader, err := store.Open(context.Background(), root, relativePath)
	if err == nil {
		_ = reader.Close()
		t.Fatalf("Open followed a %s outside the data root", kind)
	}
	if !errors.Is(err, storagepg.ErrInvalidPath) {
		t.Fatalf("Open(%s) error = %v, want ErrInvalidPath", kind, err)
	}
}

// createDirLink creates a directory symlink where supported and falls back to
// an unprivileged Windows junction when symbol-link creation needs privileges.
func createDirLink(link, target string) error {
	if err := os.Symlink(target, link); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J: %w: %s", err, output)
	}
	return nil
}

func TestFileStoreRejectsMalformedSHA(t *testing.T) {
	store := storagepg.NewFileStore()
	if _, err := store.Put(context.Background(), t.TempDir(), "not-a-sha", []byte("x")); err == nil {
		t.Fatal("expected error for malformed sha")
	}
	if _, err := store.Put(context.Background(), t.TempDir(), "../../escape", []byte("x")); err == nil {
		t.Fatal("expected error for traversal sha")
	}
}

func TestFileStoreConcurrentSameContent(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()
	data := []byte("same content")
	sha := shaOf(data)

	var wg sync.WaitGroup
	results := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := store.Put(context.Background(), root, sha, data)
			if err != nil {
				errs <- err
				return
			}
			results <- rel
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Put: %v", err)
	}
	for rel := range results {
		if rel != "objects/"+sha[0:2]+"/"+sha[2:4]+"/"+sha {
			t.Fatalf("rel = %q", rel)
		}
	}
}
