package storageapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

type fakeLedger struct {
	objects   map[string]storage.Object
	contents  map[string][]byte
	refs      []storage.Ref
	nextRefID int64
	openErr   error
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{objects: map[string]storage.Object{}, contents: map[string][]byte{}}
}

func (l *fakeLedger) Upload(_ context.Context, filename, declaredMIME string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return storage.Object{}, err
	}
	if int64(len(data)) > maxBytes {
		return storage.Object{}, platform.NewError(413, "payload_too_large", "too large")
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	object := storage.Object{SHA256: sha, Size: int64(len(data)), MIME: "text/plain; charset=utf-8"}
	l.objects[sha] = object
	l.contents[sha] = data
	return object, nil
}

func (l *fakeLedger) AddRef(_ context.Context, objectID, ownerKind string, ownerID int64, role string) (storage.Ref, error) {
	l.nextRefID++
	ref := storage.Ref{ID: l.nextRefID, ObjectID: objectID, OwnerKind: ownerKind, OwnerID: ownerID, Role: role}
	l.refs = append(l.refs, ref)
	return ref, nil
}

func (l *fakeLedger) Open(_ context.Context, sha string) (storage.Object, io.ReadCloser, error) {
	if l.openErr != nil {
		return storage.Object{}, nil, l.openErr
	}
	object, ok := l.objects[sha]
	if !ok {
		return storage.Object{}, nil, errors.New("missing content")
	}
	return object, io.NopCloser(strings.NewReader(string(l.contents[sha]))), nil
}

type fakeExportRepo struct {
	ExportRepository
	artifacts []storage.ExportArtifact
}

func (r *fakeExportRepo) Create(_ context.Context, artifact storage.ExportArtifact) (storage.ExportArtifact, error) {
	for _, existing := range r.artifacts {
		if existing.VersionID == artifact.VersionID && existing.OwnerKind == artifact.OwnerKind &&
			existing.OwnerID == artifact.OwnerID && existing.Format == artifact.Format &&
			existing.Revision == artifact.Revision {
			return storage.ExportArtifact{}, storage.ErrDuplicate
		}
	}
	r.artifacts = append(r.artifacts, artifact)
	return artifact, nil
}

func (r *fakeExportRepo) Get(_ context.Context, exportID int64) (storage.ExportArtifact, error) {
	for _, artifact := range r.artifacts {
		if artifact.ID == exportID {
			return artifact, nil
		}
	}
	return storage.ExportArtifact{}, storage.ErrNotFound
}

func (r *fakeExportRepo) MaxRevision(_ context.Context, versionID int64, ownerKind string, ownerID int64, format string) (int, error) {
	maxRevision := 0
	for _, artifact := range r.artifacts {
		if artifact.VersionID == versionID && artifact.OwnerKind == ownerKind &&
			artifact.OwnerID == ownerID && artifact.Format == format && artifact.Revision > maxRevision {
			maxRevision = artifact.Revision
		}
	}
	return maxRevision, nil
}

func (r *fakeExportRepo) List(_ context.Context, versionID int64, format string, limit int, beforeID *int64) ([]storage.ExportArtifact, error) {
	matches := make([]storage.ExportArtifact, 0)
	for index := len(r.artifacts) - 1; index >= 0; index-- {
		artifact := r.artifacts[index]
		if artifact.VersionID != versionID {
			continue
		}
		if format != "" && artifact.Format != format {
			continue
		}
		if beforeID != nil && artifact.ID >= *beforeID {
			continue
		}
		matches = append(matches, artifact)
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func newExportFixture() (*ExportService, *fakeLedger, *fakeExportRepo) {
	ledger := newFakeLedger()
	repo := &fakeExportRepo{}
	service := NewExportService(repo, ledger, &exportIDs{}, exportClock{})
	return service, ledger, repo
}

type exportIDs struct{ next int64 }

func (g *exportIDs) Next() int64 {
	g.next++
	if g.next == 0 {
		g.next = 1
	}
	return g.next
}

type exportClock struct{}

func (exportClock) Now() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }

func validCreateInput() CreateExportInput {
	return CreateExportInput{
		VersionID: 1001, OwnerID: 1001, OwnerKind: storage.OwnerKindCase,
		Format: storage.FormatMarkdown, MIME: "text/markdown; charset=utf-8",
		Filename: "case-export-1.md", Data: []byte("# 用例设计\n"),
	}
}

func TestExportServiceWritesContentAndRef(t *testing.T) {
	service, ledger, repo := newExportFixture()
	artifact, err := service.Create(context.Background(), validCreateInput(), 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if artifact.ID <= 0 || artifact.Revision != 1 || artifact.Status != storage.ExportStatusReady {
		t.Fatalf("artifact = %+v", artifact)
	}
	if len(artifact.ContentObjectID) != 64 {
		t.Fatalf("content object id = %q", artifact.ContentObjectID)
	}
	if artifact.Size != int64(len(validCreateInput().Data)) {
		t.Fatalf("size = %d", artifact.Size)
	}
	if artifact.CreatedBy == nil || *artifact.CreatedBy != 42 {
		t.Fatalf("created by = %v", artifact.CreatedBy)
	}
	if len(ledger.refs) != 1 {
		t.Fatalf("refs = %d", len(ledger.refs))
	}
	ref := ledger.refs[0]
	if ref.OwnerKind != storage.OwnerKindExport || ref.OwnerID != artifact.ID || ref.Role != "content" {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.ObjectID != artifact.ContentObjectID {
		t.Fatalf("ref object = %q", ref.ObjectID)
	}
	if len(repo.artifacts) != 1 {
		t.Fatalf("artifacts = %d", len(repo.artifacts))
	}
}

func TestExportRevisionIncrements(t *testing.T) {
	service, _, _ := newExportFixture()
	first, err := service.Create(context.Background(), validCreateInput(), 1)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := service.Create(context.Background(), validCreateInput(), 1)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("revisions = %d, %d", first.Revision, second.Revision)
	}
	if second.ID == first.ID {
		t.Fatalf("export id reused: %d", first.ID)
	}
}

func TestExportFilenameAndMIME(t *testing.T) {
	service, _, _ := newExportFixture()
	artifact, err := service.Create(context.Background(), validCreateInput(), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if artifact.Filename != "case-export-1.md" || !strings.HasPrefix(artifact.MIME, "text/markdown") {
		t.Fatalf("filename/mime = %q %q", artifact.Filename, artifact.MIME)
	}
	for _, mutate := range []func(*CreateExportInput){
		func(in *CreateExportInput) { in.OwnerKind = "unknown" },
		func(in *CreateExportInput) { in.Format = "excel" },
		func(in *CreateExportInput) { in.MIME = "" },
		func(in *CreateExportInput) { in.Filename = "" },
		func(in *CreateExportInput) { in.VersionID = 0 },
		func(in *CreateExportInput) { in.OwnerID = 0 },
	} {
		in := validCreateInput()
		mutate(&in)
		if _, err := service.Create(context.Background(), in, 1); err == nil {
			t.Fatalf("input was accepted: %+v", in)
		}
	}
	// The storage layer accepts the frozen schema enum for future owners; P2's
	// case-only restriction belongs to the design export service/API.
	pdf := validCreateInput()
	pdf.Format = storage.FormatPDF
	pdf.OwnerKind = storage.OwnerKindReport
	pdf.MIME = "application/pdf"
	pdf.Filename = "case-export.pdf"
	if _, err := service.Create(context.Background(), pdf, 1); err != nil {
		t.Fatalf("schema-enum owner/format rejected: %v", err)
	}
}

func TestExportOpenVerifiesHash(t *testing.T) {
	service, ledger, _ := newExportFixture()
	artifact, err := service.Create(context.Background(), validCreateInput(), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	opened, reader, err := service.Open(context.Background(), artifact.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != string(validCreateInput().Data) {
		t.Fatalf("data = %q", data)
	}
	if opened.ContentObjectID != artifact.ContentObjectID {
		t.Fatalf("opened artifact = %+v", opened)
	}
	ledger.openErr = storage.ErrCorrupt
	if _, _, err := service.Open(context.Background(), artifact.ID); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("corrupt open err = %v", err)
	}
	if _, _, err := service.Open(context.Background(), 999999); err == nil {
		t.Fatal("missing export accepted")
	}
}

func TestExportListCursor(t *testing.T) {
	service, _, _ := newExportFixture()
	for index := 0; index < 3; index++ {
		if _, err := service.Create(context.Background(), validCreateInput(), 1); err != nil {
			t.Fatalf("Create %d: %v", index, err)
		}
	}
	items, hasMore, err := service.List(context.Background(), 1001, "", 2, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || !hasMore {
		t.Fatalf("items=%d hasMore=%v", len(items), hasMore)
	}
	before := items[len(items)-1].ID
	next, hasMore, err := service.List(context.Background(), 1001, "", 2, &before)
	if err != nil {
		t.Fatalf("List cursor: %v", err)
	}
	if len(next) != 1 || hasMore {
		t.Fatalf("cursor items=%d hasMore=%v", len(next), hasMore)
	}
	filtered, _, err := service.List(context.Background(), 1001, storage.FormatOPML, 20, nil)
	if err != nil || len(filtered) != 0 {
		t.Fatalf("format filter = %v, %v", filtered, err)
	}
}
