package localfs_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/localfs"
)

// sha256Hex is the canonical object id used by the storage abstraction.
func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// objectPath resolves the content-addressed path of an object below root.
func objectPath(root string, obj storage.Object) string {
	return filepath.Join(root, filepath.FromSlash(obj.RelativePath))
}

// TestPutOpenDedupeAndCorrupt is the brief's scenario: put, dedupe by hash,
// open the stored bytes, then tamper with the file and require the corruption
// to be reported instead of silently returning wrong content.
func TestPutOpenDedupeAndCorrupt(t *testing.T) {
	ctx := context.Background()
	s := localfs.New(t.TempDir())

	obj, err := s.Put(ctx, strings.NewReader("hello"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	wantSHA := sha256Hex("hello")
	if obj.SHA256 != wantSHA || obj.Size != 5 || obj.ContentType != "text/plain" {
		t.Fatalf("object=%+v, want sha=%s size=5 content_type=text/plain", obj, wantSHA)
	}
	if obj.RelativePath != filepath.ToSlash(filepath.Join(wantSHA[:2], wantSHA[2:4], wantSHA)) {
		t.Fatalf("relative path=%q", obj.RelativePath)
	}

	obj2, err := s.Put(ctx, strings.NewReader("hello"), "text/plain")
	if err != nil || obj2.SHA256 != obj.SHA256 {
		t.Fatalf("dedupe failed: object=%+v err=%v", obj2, err)
	}

	rc, opened, err := s.Open(ctx, obj.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rc)
	if closeErr := rc.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	if err != nil || string(b) != "hello" {
		t.Fatalf("got=%q err=%v", b, err)
	}
	if opened.SHA256 != obj.SHA256 || opened.Size != 5 {
		t.Fatalf("opened object=%+v", opened)
	}

	// Tampering with the stored file must surface as storage_corrupted, not as
	// a successful read of the wrong bytes.
	if err := os.WriteFile(objectPath(s.Root(), obj), []byte("hella"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, obj.SHA256); !errors.Is(err, storage.ErrCorrupted) {
		t.Fatalf("tampered open error=%v, want storage.ErrCorrupted", err)
	}
}

// TestPutWritesTempThenRenamesIntoContentAddressedPath proves the on-disk
// layout is ab/cd/<sha256> and that no temporary file is left behind.
func TestPutWritesTempThenRenamesIntoContentAddressedPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := localfs.New(root)

	obj, err := s.Put(ctx, strings.NewReader("content-addressed"), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	path := objectPath(root, obj)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("object not stored at %s: %v", path, err)
	}
	if filepath.Base(path) != obj.SHA256 {
		t.Fatalf("object filename=%q want %q", filepath.Base(path), obj.SHA256)
	}
	// The store must not leave its staging file visible below the root.
	var leftovers []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if p != path {
			leftovers = append(leftovers, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("unexpected files left in the store: %v", leftovers)
	}
}

// TestPutDoesNotOverwriteExistingObject proves the append-only contract: when
// the target path already exists, Put leaves it untouched even if its content
// no longer matches the hash.
func TestPutDoesNotOverwriteExistingObject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := localfs.New(root)

	content := "hello"
	sum := sha256Hex(content)
	target := filepath.Join(root, sum[:2], sum[2:4], sum)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Put(ctx, strings.NewReader(content), "text/plain"); err != nil {
		t.Fatalf("put over an existing object must dedupe, got error: %v", err)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "tampered" {
		t.Fatalf("existing object was overwritten: %q", onDisk)
	}
}

// TestOpenStatErrors covers the not-found and validation paths.
func TestOpenStatErrors(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := localfs.New(root)

	if _, _, err := s.Open(ctx, "not-a-sha"); !errors.Is(err, storage.ErrInvalidSHA256) {
		t.Fatalf("open invalid sha error=%v, want ErrInvalidSHA256", err)
	}
	if _, err := s.Stat(ctx, "../../etc/passwd"); !errors.Is(err, storage.ErrInvalidSHA256) {
		t.Fatalf("stat traversal error=%v, want ErrInvalidSHA256", err)
	}
	missing := strings.Repeat("0", 64)
	if _, _, err := s.Open(ctx, missing); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("open missing error=%v, want ErrObjectNotFound", err)
	}
	if _, err := s.Stat(ctx, missing); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("stat missing error=%v, want ErrObjectNotFound", err)
	}

	obj, err := s.Put(ctx, strings.NewReader("size me"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	stat, err := s.Stat(ctx, obj.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if stat.SHA256 != obj.SHA256 || stat.Size != int64(len("size me")) || stat.RelativePath != obj.RelativePath {
		t.Fatalf("stat=%+v object=%+v", stat, obj)
	}
}

// TestPutEmptyContent stores the empty object, which is a valid content hash.
func TestPutEmptyContent(t *testing.T) {
	ctx := context.Background()
	s := localfs.New(t.TempDir())

	obj, err := s.Put(ctx, strings.NewReader(""), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Size != 0 || obj.SHA256 != sha256Hex("") {
		t.Fatalf("empty object=%+v", obj)
	}
	rc, _, err := s.Open(ctx, obj.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if data, err := io.ReadAll(rc); err != nil || len(data) != 0 {
		t.Fatalf("empty read len=%d err=%v", len(data), err)
	}
}

// TestPutPropagatesReaderError proves a failed upload leaves no object behind.
func TestPutPropagatesReaderError(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := localfs.New(root)

	boom := errors.New("reader exploded")
	_, err := s.Put(ctx, io.MultiReader(strings.NewReader("partial"), errorReader{boom}), "text/plain")
	if !errors.Is(err, boom) {
		t.Fatalf("put error=%v, want %v", err, boom)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed put left entries behind: %v", entries)
	}
}

// errorReader fails every read with err.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
