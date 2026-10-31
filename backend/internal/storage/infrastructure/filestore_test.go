package storagepg_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
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
