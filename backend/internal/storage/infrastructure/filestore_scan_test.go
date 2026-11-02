package storagepg_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
)

func TestFileStoreScanObjectsReturnsLedgerFilesAndStagingTemps(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()
	ctx := context.Background()

	first := []byte("first scanned object")
	firstSHA := shaOf(first)
	if _, err := store.Put(ctx, root, firstSHA, first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	second := []byte("second scanned object")
	secondSHA := shaOf(second)
	if _, err := store.Put(ctx, root, secondSHA, second); err != nil {
		t.Fatalf("Put second: %v", err)
	}

	// A crashed Put leaves a ".<sha>.tmp-*" staging file inside the shard.
	stagingDir := filepath.Join(root, "objects", firstSHA[0:2], firstSHA[2:4])
	stagingPath := filepath.Join(stagingDir, "."+firstSHA+".tmp-123")
	if err := os.WriteFile(stagingPath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write staging file: %v", err)
	}
	// A non-ledger file is still returned but without a SHA, so GC ignores it.
	notesPath := filepath.Join(root, "objects", "notes.txt")
	if err := os.WriteFile(notesPath, []byte("notes"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	// Files outside objects/ are not part of the content tree.
	if err := os.WriteFile(filepath.Join(root, "outside.bin"), []byte("nope"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	files, err := store.ScanObjects(ctx, root)
	if err != nil {
		t.Fatalf("ScanObjects: %v", err)
	}
	byPath := map[string]storageapp.FileInfo{}
	for _, file := range files {
		byPath[file.RelativePath] = file
	}
	if len(files) != 4 {
		t.Fatalf("files = %#v", files)
	}
	firstPath := "objects/" + firstSHA[0:2] + "/" + firstSHA[2:4] + "/" + firstSHA
	if info, ok := byPath[firstPath]; !ok || info.SHA256 != firstSHA || info.Size != int64(len(first)) || info.ModTime.IsZero() {
		t.Fatalf("first = %#v", byPath[firstPath])
	}
	stagingRelative := "objects/" + firstSHA[0:2] + "/" + firstSHA[2:4] + "/." + firstSHA + ".tmp-123"
	if info, ok := byPath[stagingRelative]; !ok || info.SHA256 != firstSHA || info.Size != int64(len("partial")) {
		t.Fatalf("staging = %#v", byPath[stagingRelative])
	}
	if info, ok := byPath["objects/notes.txt"]; !ok || info.SHA256 != "" {
		t.Fatalf("notes = %#v", byPath["objects/notes.txt"])
	}
	if _, ok := byPath["outside.bin"]; ok {
		t.Fatal("scan leaked a file outside objects/")
	}
	if runtime.GOOS != "windows" {
		for _, file := range files {
			if strings.HasSuffix(file.RelativePath, firstSHA) {
				info, err := os.Stat(filepath.Join(root, filepath.FromSlash(file.RelativePath)))
				if err != nil {
					t.Fatalf("stat: %v", err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("file mode = %o", info.Mode().Perm())
				}
			}
		}
	}
}

func TestFileStoreScanObjectsMissingObjectsDirIsEmpty(t *testing.T) {
	store := storagepg.NewFileStore()
	files, err := store.ScanObjects(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("ScanObjects: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %#v", files)
	}
	if _, err := store.ScanObjects(context.Background(), ""); err == nil {
		t.Fatal("empty root should fail")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ScanObjects(cancelled, t.TempDir()); err == nil {
		t.Fatal("cancelled ctx should fail")
	}
}

func TestFileStoreDeleteObjectRemovesOnlyLedgerPaths(t *testing.T) {
	store := storagepg.NewFileStore()
	root := t.TempDir()
	ctx := context.Background()
	data := []byte("delete me")
	sha := shaOf(data)
	relativePath, err := store.Put(ctx, root, sha, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.DeleteObject(ctx, root, relativePath); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relativePath))); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
	if err := store.DeleteObject(ctx, root, relativePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second delete err = %v", err)
	}

	outside := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "../keep.txt", "objects/../../keep.txt", outside} {
		if err := store.DeleteObject(ctx, root, bad); !errors.Is(err, storagepg.ErrInvalidPath) {
			t.Fatalf("DeleteObject(%q) err = %v", bad, err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was touched: %v", err)
	}
}

func TestFileStoreProbeWritableWritesAndCleansUp(t *testing.T) {
	store := storagepg.NewFileStore()
	root := filepath.Join(t.TempDir(), "data")
	if err := store.ProbeWritable(context.Background(), root, "host:1234"); err != nil {
		t.Fatalf("ProbeWritable: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe was not removed: %#v", entries)
	}

	if err := store.ProbeWritable(context.Background(), "", "host:1234"); err == nil {
		t.Fatal("empty root should fail")
	}
	fileRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file root: %v", err)
	}
	if err := store.ProbeWritable(context.Background(), fileRoot, "host:1234"); err == nil {
		t.Fatal("file root should fail")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.ProbeWritable(cancelled, t.TempDir(), "host:1234"); err == nil {
		t.Fatal("cancelled ctx should fail")
	}
}

func TestFileStoreDeleteObjectRejectsSymlinkedShard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	store := storagepg.NewFileStore()
	root := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "victim")
	if err := os.WriteFile(target, []byte("victim"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	objectsDir := filepath.Join(root, "objects")
	if err := os.MkdirAll(objectsDir, 0o700); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(objectsDir, "aa")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := store.DeleteObject(context.Background(), root, "objects/aa/victim"); !errors.Is(err, storagepg.ErrInvalidPath) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("victim was deleted: %v", err)
	}
}

func TestFileStoreScanObjectsIgnoresSymlinkedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	store := storagepg.NewFileStore()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "linked")
	if err := os.WriteFile(target, []byte("linked"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "objects", strings.Repeat("ab", 32))); err != nil {
		t.Skipf("symlink: %v", err)
	}
	files, err := store.ScanObjects(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanObjects: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("scan returned symlinks: %#v", files)
	}
}
