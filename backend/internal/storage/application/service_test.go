package storageapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

var fixedTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fakeRepository struct {
	mu       sync.Mutex
	objects  map[string]storage.Object
	refs     map[int64]storage.Ref
	nextRef  int64
	putCalls int
	getErr   error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		objects: make(map[string]storage.Object),
		refs:    make(map[int64]storage.Ref),
	}
}

func (f *fakeRepository) PutObject(_ context.Context, object storage.Object) (storage.Object, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls++
	if existing, ok := f.objects[object.SHA256]; ok {
		return existing, false, nil
	}
	if object.CreatedAt.IsZero() {
		object.CreatedAt = fixedTime
	}
	f.objects[object.SHA256] = object
	return object, true, nil
}

func (f *fakeRepository) GetObject(_ context.Context, sha string) (storage.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return storage.Object{}, f.getErr
	}
	object, ok := f.objects[sha]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return object, nil
}

func (f *fakeRepository) AddRef(_ context.Context, ref storage.Ref) (storage.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRef++
	ref.ID = f.nextRef
	if ref.CreatedAt.IsZero() {
		ref.CreatedAt = fixedTime
	}
	f.refs[ref.ID] = ref
	return ref, nil
}

func (f *fakeRepository) ListRefs(_ context.Context, ownerKind string, ownerID int64) ([]storage.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]storage.Ref, 0)
	for id := int64(1); id <= f.nextRef; id++ {
		ref, ok := f.refs[id]
		if !ok || ref.OwnerKind != ownerKind || ref.OwnerID != ownerID || ref.DeletedAt != nil {
			continue
		}
		result = append(result, ref)
	}
	return result, nil
}

func (f *fakeRepository) SoftDeleteRef(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref, ok := f.refs[id]
	if !ok || ref.DeletedAt != nil {
		return storage.ErrNotFound
	}
	at := fixedTime
	ref.DeletedAt = &at
	f.refs[id] = ref
	return nil
}

type putCall struct {
	root string
	sha  string
	data []byte
}

type fakeFileStore struct {
	mu      sync.Mutex
	puts    []putCall
	objects map[string][]byte
	putErr  error
	openErr error
}

func newFakeFileStore() *fakeFileStore {
	return &fakeFileStore{objects: make(map[string][]byte)}
}

func relativePathFor(sha string) string {
	return "objects/" + sha[0:2] + "/" + sha[2:4] + "/" + sha
}

func (f *fakeFileStore) Put(_ context.Context, root, sha string, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return "", f.putErr
	}
	f.puts = append(f.puts, putCall{root: root, sha: sha, data: append([]byte(nil), data...)})
	rel := relativePathFor(sha)
	f.objects[rel] = append([]byte(nil), data...)
	return rel, nil
}

func (f *fakeFileStore) Open(_ context.Context, _, rel string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return nil, f.openErr
	}
	data, ok := f.objects[rel]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeFileStore) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

func (f *fakeFileStore) tamper(rel string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[rel] = append([]byte(nil), data...)
}

type fakeSettings struct {
	root string
	err  error
}

func (f *fakeSettings) String(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.root, nil
}

type fakeIDs struct {
	next int64
}

func (f *fakeIDs) Next() int64 {
	f.next++
	return f.next
}

type fakePublisher struct {
	mu     sync.Mutex
	events []eventingapp.Event
}

func (f *fakePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type fakeAuditor struct {
	mu     sync.Mutex
	events []auditapp.Event
}

func (f *fakeAuditor) Record(_ context.Context, event auditapp.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *fakeAuditor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type fixture struct {
	svc       *Service
	repo      *fakeRepository
	files     *fakeFileStore
	settings  *fakeSettings
	ids       *fakeIDs
	events    *fakePublisher
	auditor   *fakeAuditor
	root      string
	imageData []byte
}

func newFixture() *fixture {
	repo := newFakeRepository()
	files := newFakeFileStore()
	settings := &fakeSettings{root: "/var/lib/eztest"}
	ids := &fakeIDs{}
	events := &fakePublisher{}
	auditor := &fakeAuditor{}
	return &fixture{
		svc:      New(repo, files, settings, ids, auditor, events),
		repo:     repo,
		files:    files,
		settings: settings,
		ids:      ids,
		events:   events,
		auditor:  auditor,
		root:     settings.root,
	}
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestUploadStoresContentAddressedObject(t *testing.T) {
	f := newFixture()
	data := []byte("hello content")
	wantSHA := shaHex(data)

	object, err := f.svc.Upload(context.Background(), "note.txt", "application/octet-stream", bytes.NewReader(data), MaxUploadBytes, 42)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if object.SHA256 != wantSHA {
		t.Fatalf("sha = %q, want %q", object.SHA256, wantSHA)
	}
	if object.Size != int64(len(data)) {
		t.Fatalf("size = %d", object.Size)
	}
	if object.MIME != "text/plain; charset=utf-8" {
		t.Fatalf("mime = %q, want detected text/plain", object.MIME)
	}
	if object.RelativePath != relativePathFor(wantSHA) {
		t.Fatalf("relative path = %q", object.RelativePath)
	}
	if f.files.putCount() != 1 {
		t.Fatalf("file put calls = %d", f.files.putCount())
	}
	stored := f.repo.objects[wantSHA]
	if stored.Size != int64(len(data)) || stored.MIME != object.MIME || stored.RelativePath != object.RelativePath {
		t.Fatalf("stored object = %#v", stored)
	}

	if f.events.count() != 1 {
		t.Fatalf("event count = %d", f.events.count())
	}
	event := f.events.events[0]
	if event.Channel != "system" || event.Type != "content.uploaded" {
		t.Fatalf("event = %#v", event)
	}
	if event.ActorID == nil || *event.ActorID != 42 {
		t.Fatalf("event actor = %v", event.ActorID)
	}
	if event.Payload["sha256"] != wantSHA {
		t.Fatalf("event payload = %#v", event.Payload)
	}
	if f.auditor.count() != 1 || f.auditor.events[0].Action != "content.uploaded" {
		t.Fatalf("audit = %#v", f.auditor.events)
	}
}

func TestUploadUsesDataRootSetting(t *testing.T) {
	f := newFixture()
	f.settings.root = t.TempDir()
	if _, err := f.svc.Upload(context.Background(), "a.bin", "", strings.NewReader("abc"), MaxUploadBytes, 1); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(f.files.puts) != 1 || f.files.puts[0].root != f.settings.root {
		t.Fatalf("root used = %#v", f.files.puts)
	}
}

func TestUploadDeduplicatesExistingContent(t *testing.T) {
	f := newFixture()
	data := []byte("same content twice")
	first, err := f.svc.Upload(context.Background(), "one.txt", "", bytes.NewReader(data), MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	second, err := f.svc.Upload(context.Background(), "two.txt", "", bytes.NewReader(data), MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("second Upload: %v", err)
	}
	if first != second {
		t.Fatalf("dedupe object mismatch: %#v vs %#v", first, second)
	}
	if f.files.putCount() != 1 {
		t.Fatalf("file put calls = %d, want 1", f.files.putCount())
	}
	if f.repo.putCalls != 1 {
		t.Fatalf("object put calls = %d, want 1", f.repo.putCalls)
	}
}

func TestUploadRejectsOversizeContent(t *testing.T) {
	f := newFixture()
	data := make([]byte, MaxUploadBytes+1)
	_, err := f.svc.Upload(context.Background(), "big.bin", "", bytes.NewReader(data), MaxUploadBytes, 1)
	if err == nil {
		t.Fatal("expected oversize error")
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != http.StatusRequestEntityTooLarge || apiErr.Code != "payload_too_large" {
		t.Fatalf("error = %#v", apiErr)
	}
	if f.files.putCount() != 0 || f.repo.putCalls != 0 {
		t.Fatalf("oversize content was persisted")
	}
}

func TestUploadPropagatesSettingsFailure(t *testing.T) {
	f := newFixture()
	wantErr := errors.New("settings unavailable")
	f.settings.err = wantErr
	_, err := f.svc.Upload(context.Background(), "a.txt", "", strings.NewReader("abc"), MaxUploadBytes, 1)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenReturnsVerifiedContent(t *testing.T) {
	f := newFixture()
	data := []byte("download me")
	object, err := f.svc.Upload(context.Background(), "a.txt", "", bytes.NewReader(data), MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	opened, reader, err := f.svc.Open(context.Background(), object.SHA256)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	if opened.SHA256 != object.SHA256 || opened.MIME != object.MIME {
		t.Fatalf("opened = %#v", opened)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q", got)
	}
}

func TestOpenDetectsHashMismatch(t *testing.T) {
	f := newFixture()
	data := []byte("original bytes")
	object, err := f.svc.Upload(context.Background(), "a.txt", "", bytes.NewReader(data), MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	f.files.tamper(object.RelativePath, []byte("tampered bytes"))

	_, reader, err := f.svc.Open(context.Background(), object.SHA256)
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
	if reader != nil {
		t.Fatal("reader should be nil on mismatch")
	}
	if !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestOpenMissingObjectIsNotFound(t *testing.T) {
	f := newFixture()
	_, _, err := f.svc.Open(context.Background(), strings.Repeat("a", 64))
	apiErr := platform.AsError(err)
	if apiErr.Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiErr.Status)
	}
}

func TestAddRefListAndSoftDelete(t *testing.T) {
	f := newFixture()
	object, err := f.svc.Upload(context.Background(), "a.txt", "", strings.NewReader("ref me"), MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	ref, err := f.svc.AddRef(context.Background(), object.SHA256, "version", 1001, "")
	if err != nil {
		t.Fatalf("AddRef: %v", err)
	}
	if ref.ID == 0 || ref.Role != "attachment" {
		t.Fatalf("ref = %#v", ref)
	}
	if ref.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	refs, err := f.svc.ListRefs(context.Background(), "version", 1001)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != ref.ID {
		t.Fatalf("refs = %#v", refs)
	}

	if err := f.svc.SoftDeleteRef(context.Background(), ref.ID); err != nil {
		t.Fatalf("SoftDeleteRef: %v", err)
	}
	refs, err = f.svc.ListRefs(context.Background(), "version", 1001)
	if err != nil {
		t.Fatalf("ListRefs after delete: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("deleted ref still listed: %#v", refs)
	}
	if err := f.svc.SoftDeleteRef(context.Background(), ref.ID); platform.AsError(err).Status != http.StatusNotFound {
		t.Fatalf("second delete status = %d", platform.AsError(err).Status)
	}
}

func TestAddRefValidatesInput(t *testing.T) {
	f := newFixture()
	object, err := f.svc.Upload(context.Background(), "a.txt", "", strings.NewReader("ref me"), MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	cases := []struct {
		name     string
		objectID string
		owner    string
		ownerID  int64
		role     string
		status   int
	}{
		{"bad sha", "not-a-sha", "version", 1, "", http.StatusBadRequest},
		{"empty owner kind", object.SHA256, " ", 1, "", http.StatusBadRequest},
		{"zero owner id", object.SHA256, "version", 0, "", http.StatusBadRequest},
		{"missing object", strings.Repeat("b", 64), "version", 1, "", http.StatusNotFound},
		{"role too long", object.SHA256, "version", 1, strings.Repeat("r", 65), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.AddRef(context.Background(), tc.objectID, tc.owner, tc.ownerID, tc.role)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := platform.AsError(err).Status; got != tc.status {
				t.Fatalf("status = %d, want %d", got, tc.status)
			}
		})
	}
}

func TestListRefsAndSoftDeleteValidateInput(t *testing.T) {
	f := newFixture()
	if _, err := f.svc.ListRefs(context.Background(), "", 1); err == nil {
		t.Fatal("empty owner kind: expected error")
	}
	if _, err := f.svc.ListRefs(context.Background(), "version", 0); err == nil {
		t.Fatal("zero owner id: expected error")
	}
	if err := f.svc.SoftDeleteRef(context.Background(), 0); err == nil {
		t.Fatal("zero ref id: expected error")
	}
	if err := f.svc.SoftDeleteRef(context.Background(), 999); platform.AsError(err).Status != http.StatusNotFound {
		t.Fatalf("missing ref status = %d", platform.AsError(err).Status)
	}
}

func TestUploadDetectsImageMIME(t *testing.T) {
	f := newFixture()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	object, err := f.svc.Upload(context.Background(), "image.bin", "text/plain", bytes.NewReader(png), MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if object.MIME != "image/png" {
		t.Fatalf("mime = %q", object.MIME)
	}
}
