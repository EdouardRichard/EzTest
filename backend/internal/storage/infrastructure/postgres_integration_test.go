//go:build integration

package storagepg_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

type staticSettings struct {
	root string
	err  error
}

func (s staticSettings) String(context.Context, string) (string, error) {
	return s.root, s.err
}

func TestRepositoryObjectDedupeAndGet(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := storagepg.New(pool)
	ctx := context.Background()

	object := storage.Object{SHA256: shaOf([]byte("db content")), Size: 10, MIME: "text/plain", RelativePath: "objects/aa/bb/cc"}
	created, isNew, err := repo.PutObject(ctx, object)
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if !isNew || created.SHA256 != object.SHA256 || created.CreatedAt.IsZero() {
		t.Fatalf("created = %#v isNew=%v", created, isNew)
	}

	dupe, isNew, err := repo.PutObject(ctx, storage.Object{SHA256: object.SHA256, Size: 99, MIME: "application/x-other", RelativePath: "objects/other"})
	if err != nil {
		t.Fatalf("PutObject duplicate: %v", err)
	}
	if isNew {
		t.Fatal("duplicate reported as new")
	}
	if dupe.MIME != "text/plain" || dupe.RelativePath != object.RelativePath {
		t.Fatalf("duplicate returned %#v", dupe)
	}

	fetched, err := repo.GetObject(ctx, object.SHA256)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if fetched.Size != object.Size || fetched.MIME != object.MIME || fetched.RelativePath != object.RelativePath {
		t.Fatalf("fetched = %#v", fetched)
	}

	if _, err := repo.GetObject(ctx, shaOf([]byte("missing"))); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestRepositoryRefLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := storagepg.New(pool)
	ctx := context.Background()

	sha := shaOf([]byte("ref content"))
	if _, _, err := repo.PutObject(ctx, storage.Object{SHA256: sha, Size: 4, MIME: "text/plain", RelativePath: "objects/aa/bb/cc"}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	ref, err := repo.AddRef(ctx, storage.Ref{ID: 9001, ObjectID: sha, OwnerKind: "version", OwnerID: 1001, Role: "attachment"})
	if err != nil {
		t.Fatalf("AddRef: %v", err)
	}
	if ref.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	refs, err := repo.ListRefs(ctx, "version", 1001)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != 9001 || refs[0].DeletedAt != nil {
		t.Fatalf("refs = %#v", refs)
	}

	if err := repo.SoftDeleteRef(ctx, 9001); err != nil {
		t.Fatalf("SoftDeleteRef: %v", err)
	}
	refs, err = repo.ListRefs(ctx, "version", 1001)
	if err != nil {
		t.Fatalf("ListRefs after delete: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("deleted refs = %#v", refs)
	}
	if err := repo.SoftDeleteRef(ctx, 9001); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestServiceUploadDownloadDedupeAndRefs(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := storagepg.New(pool)
	files := storagepg.NewFileStore()
	root := t.TempDir()
	ids, err := id.NewSnowflake(7)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	svc := storageapp.New(repo, files, staticSettings{root: root}, ids, nil, nil)
	ctx := context.Background()

	data := []byte("integration upload content")
	wantSHA := shaOf(data)
	object, err := svc.Upload(ctx, "file.txt", "text/plain", bytes.NewReader(data), storageapp.MaxUploadBytes, 55)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if object.SHA256 != wantSHA || object.Size != int64(len(data)) {
		t.Fatalf("object = %#v", object)
	}
	onDisk := filepath.Join(root, filepath.FromSlash(object.RelativePath))
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("stored file missing: %v", err)
	}

	opened, reader, err := svc.Open(ctx, wantSHA)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q", got)
	}
	if opened.SHA256 != wantSHA {
		t.Fatalf("opened = %#v", opened)
	}

	dupe, err := svc.Upload(ctx, "again.txt", "text/plain", bytes.NewReader(data), storageapp.MaxUploadBytes, 55)
	if err != nil {
		t.Fatalf("duplicate Upload: %v", err)
	}
	if dupe != object {
		t.Fatalf("dedupe mismatch: %#v vs %#v", dupe, object)
	}

	ref, err := svc.AddRef(ctx, wantSHA, "version", 1001, "evidence")
	if err != nil {
		t.Fatalf("AddRef: %v", err)
	}
	refs, err := svc.ListRefs(ctx, "version", 1001)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if len(refs) != 1 || refs[0].Role != "evidence" {
		t.Fatalf("refs = %#v", refs)
	}
	if err := svc.SoftDeleteRef(ctx, ref.ID); err != nil {
		t.Fatalf("SoftDeleteRef: %v", err)
	}

	// Tampering with the stored bytes must be detected on read.
	if err := os.WriteFile(onDisk, []byte("corrupted"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, _, err := svc.Open(ctx, wantSHA); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("tampered Open err = %v", err)
	}
}
