package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// evidenceTestRepo is an in-memory EvidenceRepository. It records rows in
// creation order so ListByCaseRun keeps the repository's stable ordering.
type evidenceTestRepo struct {
	mu        sync.Mutex
	rows      map[int64]domain.Evidence
	order     []int64
	createErr error
	getErr    error
	listErr   error
}

func newEvidenceTestRepo() *evidenceTestRepo {
	return &evidenceTestRepo{rows: make(map[int64]domain.Evidence)}
}

func (r *evidenceTestRepo) Create(_ context.Context, evidence domain.Evidence) (domain.Evidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return domain.Evidence{}, r.createErr
	}
	if _, exists := r.rows[evidence.ID]; exists {
		return domain.Evidence{}, fmt.Errorf("%w: evidence %d", ErrConflict, evidence.ID)
	}
	evidence.CreatedAt = time.Unix(0, 0).UTC()
	r.rows[evidence.ID] = evidence
	r.order = append(r.order, evidence.ID)
	return evidence, nil
}

func (r *evidenceTestRepo) Get(_ context.Context, evidenceID int64) (domain.Evidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return domain.Evidence{}, r.getErr
	}
	evidence, ok := r.rows[evidenceID]
	if !ok {
		return domain.Evidence{}, fmt.Errorf("%w: evidence %d", ErrEvidenceNotFound, evidenceID)
	}
	return evidence, nil
}

func (r *evidenceTestRepo) ListByCaseRun(_ context.Context, caseRunID int64) ([]domain.Evidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	items := make([]domain.Evidence, 0, len(r.order))
	for _, id := range r.order {
		evidence := r.rows[id]
		if evidence.CaseRunID != nil && *evidence.CaseRunID == caseRunID {
			items = append(items, evidence)
		}
	}
	return items, nil
}

// seed inserts a stored row directly, bypassing the service.
func (r *evidenceTestRepo) seed(evidence domain.Evidence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if evidence.CreatedAt.IsZero() {
		evidence.CreatedAt = time.Unix(0, 0).UTC()
	}
	r.rows[evidence.ID] = evidence
	r.order = append(r.order, evidence.ID)
}

func (r *evidenceTestRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

// evidenceTestRefs records evidence_ref inserts and can simulate a failure.
type evidenceTestRefs struct {
	mu        sync.Mutex
	refs      []domain.EvidenceRef
	createErr error
}

func (r *evidenceTestRefs) Create(_ context.Context, ref domain.EvidenceRef) (domain.EvidenceRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return domain.EvidenceRef{}, r.createErr
	}
	r.refs = append(r.refs, ref)
	return ref, nil
}

func (r *evidenceTestRefs) has(kind string, id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ref := range r.refs {
		if ref.RefKind == kind && ref.RefID == id {
			return true
		}
	}
	return false
}

func (r *evidenceTestRefs) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.refs)
}

// evidenceTestBatches is the batch ownership reader used by Open. Batch 1
// belongs to version 7 and batch 2 to version 8; every other pair is foreign.
type evidenceTestBatches struct {
	versions map[int64]int64
}

func (b *evidenceTestBatches) GetBatch(_ context.Context, versionID, batchID int64) (domain.RunBatch, error) {
	version, ok := b.versions[batchID]
	if !ok || version != versionID {
		return domain.RunBatch{}, fmt.Errorf("%w: batch %d of version %d", ErrBatchNotFound, batchID, versionID)
	}
	return domain.RunBatch{ID: batchID, VersionID: version}, nil
}

// evidenceTestStore is an in-memory EvidenceStore implementation. It records
// written and removed paths so the service's orphan cleanup is observable.
type evidenceTestStore struct {
	mu        sync.Mutex
	files     map[string][]byte
	written   []string
	removed   []string
	writeErr  error
	openErr   error
	removeErr error
}

func newEvidenceTestStore() *evidenceTestStore {
	return &evidenceTestStore{files: make(map[string][]byte)}
}

func (s *evidenceTestStore) Write(_ context.Context, rel string, data []byte) (string, string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return "", "", 0, s.writeErr
	}
	copied := append([]byte(nil), data...)
	s.files[rel] = copied
	s.written = append(s.written, rel)
	sum := sha256.Sum256(copied)
	return rel, hex.EncodeToString(sum[:]), int64(len(copied)), nil
}

func (s *evidenceTestStore) Open(_ context.Context, rel string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		return nil, s.openErr
	}
	data, ok := s.files[rel]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (s *evidenceTestStore) Remove(_ context.Context, rel string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removeErr != nil {
		return s.removeErr
	}
	s.removed = append(s.removed, rel)
	delete(s.files, rel)
	return nil
}

func (s *evidenceTestStore) exists(rel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[rel]
	return ok
}

// evidenceTestIDs mints sequential ids starting at 1000.
type evidenceTestIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *evidenceTestIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next == 0 {
		g.next = 1000
	}
	id := g.next
	g.next++
	return id
}

type evidenceTestDeps struct {
	evidence *evidenceTestRepo
	refs     *evidenceTestRefs
	batches  *evidenceTestBatches
	store    *evidenceTestStore
	ids      *evidenceTestIDs
}

// newEvidenceServiceTest wires the evidence service to in-memory doubles. Batch
// 1 belongs to version 7 and batch 2 to version 8.
func newEvidenceServiceTest(t *testing.T, opts ...EvidenceServiceOption) (*EvidenceService, *evidenceTestDeps) {
	t.Helper()
	deps := &evidenceTestDeps{
		evidence: newEvidenceTestRepo(),
		refs:     &evidenceTestRefs{},
		batches:  &evidenceTestBatches{versions: map[int64]int64{1: 7, 2: 8}},
		store:    newEvidenceTestStore(),
		ids:      &evidenceTestIDs{},
	}
	service := NewEvidenceService(deps.evidence, deps.refs, deps.batches, deps.store, deps.ids, opts...)
	return service, deps
}

// evidenceInt64Ptr returns a pointer to id for optional fields.
func evidenceInt64Ptr(id int64) *int64 { return &id }

// assertEvidenceReason asserts err is a *domain.ReasonError with code and reason.
func assertEvidenceReason(t *testing.T, err error, code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s/%s, got nil", code, reason)
	}
	var reasonErr *domain.ReasonError
	if !errors.As(err, &reasonErr) {
		t.Fatalf("want *domain.ReasonError, got %T (%v)", err, err)
	}
	if reasonErr.Code != code || reasonErr.Reason != reason {
		t.Fatalf("code/reason = %s/%s, want %s/%s (%v)",
			reasonErr.Code, reasonErr.Reason, code, reason, err)
	}
}

// TestCaptureHashesAndReadsBack pins the capture contract: the slug path is
// {batch}/{caseRun}/{id}.{ext}, the row carries the content hash and size, the
// case_run ref is written, and Open verifies the hash before serving the bytes.
func TestCaptureHashesAndReadsBack(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	data := []byte("png")
	stepResultID := int64(55)

	evidence, err := service.Capture(context.Background(), CaptureInput{
		BatchID:      1,
		CaseRunID:    2,
		StepResultID: &stepResultID,
		Kind:         "screenshot",
		Mime:         "image/png",
		Data:         data,
		Metadata:     map[string]any{"step": "1"},
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	wantSHA := sha256.Sum256(data)
	wantSHAHex := hex.EncodeToString(wantSHA[:])
	if evidence.SHA256 != wantSHAHex {
		t.Fatalf("sha256 = %q, want %q", evidence.SHA256, wantSHAHex)
	}
	if evidence.SizeBytes != int64(len(data)) {
		t.Fatalf("size = %d, want %d", evidence.SizeBytes, len(data))
	}
	if evidence.Kind != "screenshot" || evidence.Mime != "image/png" {
		t.Fatalf("kind/mime = %s/%s", evidence.Kind, evidence.Mime)
	}
	if evidence.BatchID != 1 || evidence.CaseRunID == nil || *evidence.CaseRunID != 2 ||
		evidence.StepResultID == nil || *evidence.StepResultID != 55 {
		t.Fatalf("scoped ids not stored: %+v", evidence)
	}
	wantRel := fmt.Sprintf("1/2/%d.png", evidence.ID)
	if evidence.Path != wantRel {
		t.Fatalf("path = %q, want %q", evidence.Path, wantRel)
	}
	if !deps.store.exists(wantRel) {
		t.Fatalf("stored file %q missing", wantRel)
	}
	if !deps.refs.has("case_run", 2) || !deps.refs.has("step_result", 55) {
		t.Fatalf("refs = %+v, want case_run/2 and step_result/55", deps.refs.refs)
	}

	reader, stored, err := service.Open(context.Background(), 7, evidence.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	readBack, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(readBack, data) {
		t.Fatalf("read back = %q, want %q", readBack, data)
	}
	if stored.SHA256 != wantSHAHex || stored.SizeBytes != int64(len(data)) {
		t.Fatalf("open metadata = %+v, want hash %s size %d", stored, wantSHAHex, len(data))
	}
	if stored.Path != "" {
		t.Fatalf("Open exposed the filesystem path %q", stored.Path)
	}
}

// TestCaptureStoresKindExtension covers every valid kind and its canonical file
// extension.
func TestCaptureStoresKindExtension(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	cases := []struct {
		kind    string
		mime    string
		wantExt string
	}{
		{"screenshot", "image/png", "png"},
		{"screenshot", "image/jpeg", "jpg"},
		{"http_exchange", "application/json", "json"},
		{"console_log", "text/plain; charset=utf-8", "txt"},
		{"network_log", "application/json", "json"},
		{"download", "application/octet-stream", "bin"},
		{"download", "video/mp4", "bin"},
		{"db_result", "application/json", "json"},
	}
	for i, tc := range cases {
		evidence, err := service.Capture(context.Background(), CaptureInput{
			BatchID:   1,
			CaseRunID: int64(10 + i),
			Kind:      tc.kind,
			Mime:      tc.mime,
			Data:      []byte("artifact"),
		})
		if err != nil {
			t.Fatalf("%s/%s: Capture: %v", tc.kind, tc.mime, err)
		}
		wantRel := fmt.Sprintf("1/%d/%d.%s", 10+i, evidence.ID, tc.wantExt)
		if evidence.Path != wantRel {
			t.Fatalf("%s/%s: path = %q, want %q", tc.kind, tc.mime, evidence.Path, wantRel)
		}
		if !deps.store.exists(wantRel) {
			t.Fatalf("%s/%s: stored file %q missing", tc.kind, tc.mime, wantRel)
		}
	}
}

// TestCaptureRejectsInvalidInput covers kind validation, empty content and
// missing scope ids. Nothing is written for a rejected capture.
func TestCaptureRejectsInvalidInput(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)

	kinds := []string{"", "video", "Screenshot", "screenshot "}
	for _, kind := range kinds {
		_, err := service.Capture(context.Background(), CaptureInput{
			BatchID: 1, CaseRunID: 2, Kind: kind, Mime: "image/png", Data: []byte("x"),
		})
		assertEvidenceReason(t, err, "invalid_argument", "invalid_evidence_kind")
	}

	if _, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png",
	}); err == nil {
		t.Fatal("Capture accepted empty evidence data")
	}

	badScope := []CaptureInput{
		{BatchID: 0, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("x")},
		{BatchID: 1, CaseRunID: 0, Kind: "screenshot", Mime: "image/png", Data: []byte("x")},
		{BatchID: 1, CaseRunID: 2, StepResultID: evidenceInt64Ptr(0), Kind: "screenshot", Mime: "image/png", Data: []byte("x")},
	}
	for _, input := range badScope {
		if _, err := service.Capture(context.Background(), input); err == nil {
			t.Fatalf("Capture accepted invalid scope %+v", input)
		}
	}

	if got := deps.store.written; len(got) != 0 {
		t.Fatalf("rejected captures wrote files: %v", got)
	}
	if got := deps.evidence.count(); got != 0 {
		t.Fatalf("rejected captures created %d rows", got)
	}
}

// TestCaptureEnforcesPerEvidenceSizeLimit covers the single artifact cap: the
// boundary is inclusive and an oversized payload never reaches the store.
func TestCaptureEnforcesPerEvidenceSizeLimit(t *testing.T) {
	if DefaultEvidenceMaxBytes != 10<<20 {
		t.Fatalf("DefaultEvidenceMaxBytes = %d, want %d", DefaultEvidenceMaxBytes, 10<<20)
	}
	service, deps := newEvidenceServiceTest(t, WithEvidenceMaxBytes(4))

	if _, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "download", Mime: "application/octet-stream",
		Data: []byte("abcd"),
	}); err != nil {
		t.Fatalf("Capture at the limit: %v", err)
	}
	_, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "download", Mime: "application/octet-stream",
		Data: []byte("abcde"),
	})
	assertEvidenceReason(t, err, "invalid_argument", "evidence_too_large")
	if got := deps.evidence.count(); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
	if got := len(deps.store.written); got != 1 {
		t.Fatalf("written files = %d, want 1", got)
	}
}

// TestCaptureEnforcesPerCaseLimit covers the 100-artifacts-per-case-run cap.
func TestCaptureEnforcesPerCaseLimit(t *testing.T) {
	if MaxEvidencePerCaseRun != 100 {
		t.Fatalf("MaxEvidencePerCaseRun = %d, want 100", MaxEvidencePerCaseRun)
	}
	service, deps := newEvidenceServiceTest(t)
	for i := 0; i < MaxEvidencePerCaseRun; i++ {
		deps.evidence.seed(domain.Evidence{
			ID:        int64(9000 + i),
			BatchID:   1,
			CaseRunID: evidenceInt64Ptr(2),
			Kind:      "screenshot",
			Path:      fmt.Sprintf("1/2/%d.png", 9000+i),
			SHA256:    strings.Repeat("a", 64),
			SizeBytes: 1,
			Mime:      "image/png",
		})
	}

	_, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("x"),
	})
	assertEvidenceReason(t, err, "invalid_argument", "evidence_limit_exceeded")
	if got := len(deps.store.written); got != 0 {
		t.Fatalf("over-limit capture wrote files: %v", deps.store.written)
	}
	if got := deps.evidence.count(); got != MaxEvidencePerCaseRun {
		t.Fatalf("rows = %d, want %d", got, MaxEvidencePerCaseRun)
	}
}

// TestCaptureCleansUpFileWhenRowInsertFails pins the best-effort orphan cleanup:
// a failed metadata insert removes the just-written file (D30).
func TestCaptureCleansUpFileWhenRowInsertFails(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	deps.evidence.createErr = errors.New("db down")

	_, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("png"),
	})
	if err == nil {
		t.Fatal("Capture succeeded despite the row insert failure")
	}
	wantRel := "1/2/1000.png"
	if deps.store.exists(wantRel) {
		t.Fatalf("orphan file %q was not removed", wantRel)
	}
	if len(deps.store.removed) != 1 || deps.store.removed[0] != wantRel {
		t.Fatalf("removed = %v, want [%s]", deps.store.removed, wantRel)
	}
}

// TestCaptureRefsAreBestEffort pins the D30 ordering: the evidence row is the
// commit point, so a failed ref insert must not fail the capture.
func TestCaptureRefsAreBestEffort(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	deps.refs.createErr = errors.New("refs down")

	evidence, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("png"),
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if deps.evidence.count() != 1 {
		t.Fatalf("evidence row not committed: %+v", deps.evidence.rows)
	}
	if !deps.store.exists(evidence.Path) {
		t.Fatalf("file %q missing", evidence.Path)
	}
	if got := deps.refs.len(); got != 0 {
		t.Fatalf("refs = %d, want 0", got)
	}
}

// TestOpenRejectsCrossVersionEvidence pins the ownership check: an evidence id
// from another version is indistinguishable from a missing one (404).
func TestOpenRejectsCrossVersionEvidence(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	evidence, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("png"),
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	if _, _, err := service.Open(context.Background(), 7, evidence.ID); err != nil {
		t.Fatalf("Open own version: %v", err)
	}
	_, _, err = service.Open(context.Background(), 8, evidence.ID)
	assertEvidenceReason(t, err, "not_found", "not_found")

	// An evidence row owned by batch 2 opens under version 8.
	other, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 2, CaseRunID: 3, Kind: "screenshot", Mime: "image/png", Data: []byte("png"),
	})
	if err != nil {
		t.Fatalf("Capture batch 2: %v", err)
	}
	if _, _, err := service.Open(context.Background(), 8, other.ID); err != nil {
		t.Fatalf("Open foreign batch own version: %v", err)
	}
	if !deps.store.exists(other.Path) {
		t.Fatalf("batch 2 file missing")
	}
}

// TestOpenRejectsUnknownEvidence covers a missing row.
func TestOpenRejectsUnknownEvidence(t *testing.T) {
	service, _ := newEvidenceServiceTest(t)
	_, _, err := service.Open(context.Background(), 7, 12345)
	assertEvidenceReason(t, err, "not_found", "not_found")
}

// TestOpenRejectsHashMismatch pins the read-time integrity check (spec §5.7,
// §5.12 invariant 3).
func TestOpenRejectsHashMismatch(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	evidence, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("png"),
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	deps.store.files[evidence.Path] = []byte("tampered")

	_, _, err = service.Open(context.Background(), 7, evidence.ID)
	assertEvidenceReason(t, err, "internal", "evidence_corrupt")
}

// TestOpenRejectsMissingFile covers a metadata row whose file was lost.
func TestOpenRejectsMissingFile(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	evidence, err := service.Capture(context.Background(), CaptureInput{
		BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("png"),
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	delete(deps.store.files, evidence.Path)

	if _, _, err := service.Open(context.Background(), 7, evidence.ID); err == nil {
		t.Fatal("Open served evidence whose file is missing")
	}
}

// TestListByCaseRun pins the append-only listing order and error propagation.
func TestListByCaseRun(t *testing.T) {
	service, deps := newEvidenceServiceTest(t)
	for _, caseRunID := range []int64{2, 3, 2} {
		if _, err := service.Capture(context.Background(), CaptureInput{
			BatchID: 1, CaseRunID: caseRunID, Kind: "screenshot", Mime: "image/png", Data: []byte("png"),
		}); err != nil {
			t.Fatalf("Capture case run %d: %v", caseRunID, err)
		}
	}

	items, err := service.ListByCaseRun(context.Background(), 2)
	if err != nil {
		t.Fatalf("ListByCaseRun: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].ID > items[1].ID {
		t.Fatalf("items not in capture order: %+v", items)
	}

	deps.evidence.listErr = errors.New("query failed")
	if _, err := service.ListByCaseRun(context.Background(), 2); err == nil {
		t.Fatal("ListByCaseRun swallowed the repository error")
	}
}

// TestRedactHTTPStripsSecrets covers the header and body redaction of spec §5.7.
func TestRedactHTTPStripsSecrets(t *testing.T) {
	headers := map[string][]string{
		"Authorization": {"Bearer secret-token"},
		"Cookie":        {"session=abc"},
		"set-cookie":    {"sid=1; HttpOnly"},
		"X-Api-Key":     {"api-key-123"},
		"Content-Type":  {"application/json"},
		"X-Trace-Id":    {"trace-1", "trace-2"},
	}
	body := []byte(`{"username":"alice","password":"p@ss","nested":{"access_token":"tok-123","client_secret":"shh","safe":1},"items":[{"passwd":"pw"},{"ok":"yes"}],"number":1.50}`)

	redactedHeaders, redactedBody := RedactHTTP(headers, body)

	for _, name := range []string{"Authorization", "Cookie", "set-cookie", "X-Api-Key"} {
		values, ok := redactedHeaders[name]
		if !ok {
			t.Fatalf("sensitive header %q was dropped; the header name must be retained", name)
		}
		if len(values) != len(headers[name]) {
			t.Fatalf("header %q values = %v, want same cardinality", name, values)
		}
		for _, value := range values {
			if value != "***" {
				t.Fatalf("header %q value = %q, want masked", name, value)
			}
		}
	}
	if got := redactedHeaders["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
		t.Fatalf("non-sensitive header changed: %v", got)
	}
	if got := redactedHeaders["X-Trace-Id"]; len(got) != 2 || got[0] != "trace-1" || got[1] != "trace-2" {
		t.Fatalf("non-sensitive header changed: %v", got)
	}

	text := string(redactedBody)
	for _, leaked := range []string{"secret-token", "p@ss", "tok-123", "shh", "pw"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("redacted body leaked %q: %s", leaked, text)
		}
	}
	for _, kept := range []string{`"username":"alice"`, `"safe":1`, `"ok":"yes"`, `"number":1.50`} {
		if !strings.Contains(text, kept) {
			t.Fatalf("redacted body dropped %q: %s", kept, text)
		}
	}
	if !strings.Contains(text, `"***"`) {
		t.Fatalf("redacted body carries no mask: %s", text)
	}
}

// TestRedactHTTPNonJSONBodyUnchanged pins that a body the redactor cannot parse
// as JSON is returned byte-for-byte (the caller decides how to treat it).
func TestRedactHTTPNonJSONBodyUnchanged(t *testing.T) {
	body := []byte("password=p@ss&user=alice")
	headers, redacted := RedactHTTP(nil, body)
	if headers == nil {
		t.Fatal("RedactHTTP returned a nil header map")
	}
	if !bytes.Equal(redacted, body) {
		t.Fatalf("non-JSON body changed: %q", redacted)
	}
}

// TestRedactJSONMasksSensitiveKeysRecursively covers the recursive JSON walk and
// the sensitive key-name matching (case-insensitive, separators ignored).
func TestRedactJSONMasksSensitiveKeysRecursively(t *testing.T) {
	body := []byte(`{"password":"a","PASSWORD":"b","x-api-key":"c","api_key":"d","access_token":"e","client_secret":"f","user":{"name":"n","items":[{"secret":"g"},{"keep":"h"}]}}`)
	redacted := string(RedactJSON(body))

	for _, leaked := range []string{`"password":"a"`, `"PASSWORD":"b"`, `"x-api-key":"c"`, `"api_key":"d"`, `"access_token":"e"`, `"client_secret":"f"`, `"secret":"g"`} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("RedactJSON leaked %s: %s", leaked, redacted)
		}
	}
	for _, kept := range []string{`"name":"n"`, `"keep":"h"`} {
		if !strings.Contains(redacted, kept) {
			t.Fatalf("RedactJSON dropped %s: %s", kept, redacted)
		}
	}
	if strings.Count(redacted, `"***"`) != 7 {
		t.Fatalf("redacted = %s, want seven masks", redacted)
	}
}

// TestRedactJSONLeavesInvalidInputAlone covers a nil and a malformed body.
func TestRedactJSONLeavesInvalidInputAlone(t *testing.T) {
	if got := RedactJSON(nil); got != nil {
		t.Fatalf("RedactJSON(nil) = %q, want nil", got)
	}
	invalid := []byte("{not json")
	if got := RedactJSON(invalid); !bytes.Equal(got, invalid) {
		t.Fatalf("RedactJSON(invalid) = %q, want unchanged", got)
	}
}
