package application

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

const (
	testSHA  = "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4"
	testPath = "8f/43/8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4"
)

// fakeStore is a storage.Store whose results the test controls.
type fakeStore struct {
	putObject    storage.Object
	putErr       error
	openReader   io.ReadCloser
	openObject   storage.Object
	openErr      error
	statObject   storage.Object
	statErr      error
	putCalls     int
	lastType     string
	lastPutBytes string
}

func (f *fakeStore) Put(_ context.Context, r io.Reader, contentType string) (storage.Object, error) {
	f.putCalls++
	f.lastType = contentType
	if r != nil {
		data, _ := io.ReadAll(r)
		f.lastPutBytes = string(data)
	}
	obj := f.putObject
	obj.ContentType = contentType
	return obj, f.putErr
}

func (f *fakeStore) Open(context.Context, string) (io.ReadCloser, storage.Object, error) {
	return f.openReader, f.openObject, f.openErr
}

func (f *fakeStore) Stat(context.Context, string) (storage.Object, error) {
	return f.statObject, f.statErr
}

// fakeMetadata is an in-memory MetadataRepository with insert-only semantics.
type fakeMetadata struct {
	mu        sync.Mutex
	rows      map[string]ObjectMetadata
	insertErr error
	findErr   error
}

func newFakeMetadata() *fakeMetadata {
	return &fakeMetadata{rows: make(map[string]ObjectMetadata)}
}

func (m *fakeMetadata) Insert(_ context.Context, metadata ObjectMetadata) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.insertErr != nil {
		return false, m.insertErr
	}
	if _, ok := m.rows[metadata.SHA256]; ok {
		return false, nil
	}
	m.rows[metadata.SHA256] = metadata
	return true, nil
}

func (m *fakeMetadata) Find(_ context.Context, sha256 string) (ObjectMetadata, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findErr != nil {
		return ObjectMetadata{}, false, m.findErr
	}
	metadata, ok := m.rows[sha256]
	return metadata, ok, nil
}

// capturingRecorder keeps the audit entries recorded during a test.
type capturingRecorder struct {
	entries []auditdomain.Entry
}

func (r *capturingRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func (r *capturingRecorder) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return r.entries, false, nil
}

// newTestService wires a fake store and the in-memory metadata repository.
func newTestService(store *fakeStore) (*Service, *fakeMetadata, *capturingRecorder) {
	metadata := newFakeMetadata()
	recorder := &capturingRecorder{}
	return NewService(store, metadata, recorder), metadata, recorder
}

// TestPutAuditsOnlyNewObjects proves storage_objects is the source of truth for
// the create audit: the first upload inserts the metadata row and records
// storage.object_created, while a later re-upload of the same bytes dedupes,
// records nothing, and reports the persisted content type.
func TestPutAuditsOnlyNewObjects(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{putObject: storage.Object{
		SHA256:       testSHA,
		Size:         5,
		RelativePath: testPath,
	}}
	svc, metadata, recorder := newTestService(store)

	first, err := svc.Put(ctx, strings.NewReader("hello"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != testSHA || first.Size != 5 || first.ContentType != "text/plain" {
		t.Fatalf("first object=%+v", first)
	}
	row, ok := metadata.rows[testSHA]
	if !ok {
		t.Fatalf("metadata row was not inserted: %+v", metadata.rows)
	}
	if row.SHA256 != testSHA || row.Size != 5 || row.ContentType != "text/plain" || row.RelativePath != testPath {
		t.Fatalf("metadata row=%+v", row)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("audit entries after first put=%d want 1: %+v", len(recorder.entries), recorder.entries)
	}
	entry := recorder.entries[0]
	if entry.Action != auditapp.ActionStorageObjectCreated || entry.TargetType != "storage_object" || entry.TargetID != testSHA {
		t.Fatalf("audit entry=%+v", entry)
	}
	if entry.Metadata["sha256"] != testSHA || entry.Metadata["size"] != int64(5) || entry.Metadata["content_type"] != "text/plain" {
		t.Fatalf("audit metadata=%v", entry.Metadata)
	}

	// A second upload of the same bytes deduplicates to the existing row: it
	// must not claim a new object was created, and the durable content type
	// (not the newly requested one) is reported.
	second, err := svc.Put(ctx, strings.NewReader("hello"), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != testSHA || second.Size != 5 {
		t.Fatalf("deduped object=%+v", second)
	}
	if second.ContentType != "text/plain" {
		t.Fatalf("deduped content type=%q want persisted text/plain", second.ContentType)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("deduped upload recorded an audit entry: %+v", recorder.entries)
	}
	if len(metadata.rows) != 1 {
		t.Fatalf("metadata rows=%d want 1", len(metadata.rows))
	}
}

// TestPutRequiresMetadataInsert proves an object is not reported as stored when
// its durable metadata could not be persisted; the request fails instead of
// claiming a creation that the database does not know about.
func TestPutRequiresMetadataInsert(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("metadata database is down")
	store := &fakeStore{putObject: storage.Object{SHA256: testSHA, Size: 5, RelativePath: testPath}}
	svc, metadata, recorder := newTestService(store)
	metadata.insertErr = boom

	if _, err := svc.Put(ctx, strings.NewReader("hello"), "text/plain"); !errors.Is(err, boom) {
		t.Fatalf("put error=%v, want %v", err, boom)
	}
	if len(recorder.entries) != 0 {
		t.Fatalf("failed put recorded audit entries: %+v", recorder.entries)
	}
}

// TestOpenAndStatFillContentTypeFromMetadata proves read paths take the media
// type from the durable metadata row instead of returning an empty type.
func TestOpenAndStatFillContentTypeFromMetadata(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		openReader: io.NopCloser(strings.NewReader("hello")),
		openObject: storage.Object{SHA256: testSHA, Size: 5, RelativePath: testPath},
		statObject: storage.Object{SHA256: testSHA, Size: 5, RelativePath: testPath},
	}
	svc, metadata, _ := newTestService(store)
	if _, err := metadata.Insert(ctx, ObjectMetadata{
		SHA256:       testSHA,
		Size:         5,
		ContentType:  "text/plain; charset=utf-8",
		RelativePath: testPath,
	}); err != nil {
		t.Fatal(err)
	}

	rc, opened, err := svc.Open(ctx, testSHA)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read=%q err=%v", data, err)
	}
	if opened.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("open content type=%q", opened.ContentType)
	}

	stat, err := svc.Stat(ctx, testSHA)
	if err != nil {
		t.Fatal(err)
	}
	if stat.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("stat content type=%q", stat.ContentType)
	}
}
