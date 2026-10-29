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
	"sync"
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

// TestPutWritesTempThenCommitsIntoContentAddressedPath proves the on-disk
// layout is ab/cd/<sha256> and that no temporary file is left behind.
func TestPutWritesTempThenCommitsIntoContentAddressedPath(t *testing.T) {
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

// TestPutRejectsCorruptExistingObject proves the append-only contract: when
// the target path already exists but its bytes no longer hash to their id, Put
// reports corruption and never overwrites the existing file.
func TestPutRejectsCorruptExistingObject(t *testing.T) {
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

	if _, err := s.Put(ctx, strings.NewReader(content), "text/plain"); !errors.Is(err, storage.ErrCorrupted) {
		t.Fatalf("put over a corrupt object error=%v, want storage.ErrCorrupted", err)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "tampered" {
		t.Fatalf("corrupt existing object was overwritten: %q", onDisk)
	}
	// The rejected upload must not leave its staging file behind either.
	var files []string
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != target {
		t.Fatalf("files after rejected put=%v, want only %s", files, target)
	}
}

// TestPutDedupesValidExistingObject keeps the positive dedupe case: when the
// target path already exists with the matching bytes, Put reuses it and reports
// its verified size instead of writing or overwriting anything.
func TestPutDedupesValidExistingObject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := localfs.New(root)

	content := "hello"
	sum := sha256Hex(content)
	target := filepath.Join(root, sum[:2], sum[2:4], sum)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	obj, err := s.Put(ctx, strings.NewReader(content), "application/json")
	if err != nil {
		t.Fatalf("put over a matching object must dedupe: %v", err)
	}
	if obj.SHA256 != sum || obj.Size != int64(len(content)) || obj.RelativePath != filepath.ToSlash(filepath.Join(sum[:2], sum[2:4], sum)) {
		t.Fatalf("deduped object=%+v", obj)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != content {
		t.Fatalf("existing object changed: %q", onDisk)
	}
}

// TestConcurrentPutDedupesWithoutOverwrite runs many simultaneous uploads of
// the same bytes. The commit must be no-replace, so every caller sees the same
// verified object and exactly one file is left behind.
func TestConcurrentPutDedupesWithoutOverwrite(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := localfs.New(root)

	content := strings.Repeat("concurrent content-addressed upload;", 64)
	wantSHA := sha256Hex(content)
	const writers = 16

	objects := make([]storage.Object, writers)
	errs := make([]error, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			objects[i], errs[i] = s.Put(ctx, strings.NewReader(content), "text/plain")
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range objects {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		if objects[i].SHA256 != wantSHA || objects[i].Size != int64(len(content)) || objects[i].ContentType != "text/plain" {
			t.Fatalf("writer %d object=%+v, want sha=%s size=%d content_type=text/plain",
				i, objects[i], wantSHA, len(content))
		}
	}

	target := filepath.Join(root, wantSHA[:2], wantSHA[2:4], wantSHA)
	var files []string
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != target {
		t.Fatalf("files after concurrent puts=%v, want only %s", files, target)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != content {
		t.Fatalf("stored object has %d bytes that do not match the uploaded content", len(onDisk))
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
