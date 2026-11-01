package storageapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

type fakeGCService struct {
	run        storage.GCRun
	runErr     error
	runs       []storage.GCRun
	listErr    error
	detail     storageapp.GCRunDetail
	getErr     error
	gotMode    storage.GCMode
	gotActorID int64
	gotLimit   int
	gotRunID   int64
	runCalls   int
	listCalls  int
	getCalls   int
}

func (f *fakeGCService) Run(_ context.Context, mode storage.GCMode, actorID int64) (storage.GCRun, error) {
	f.runCalls++
	f.gotMode = mode
	f.gotActorID = actorID
	if f.runErr != nil {
		return storage.GCRun{}, f.runErr
	}
	return f.run, nil
}

func (f *fakeGCService) ListRuns(_ context.Context, limit int) ([]storage.GCRun, error) {
	f.listCalls++
	f.gotLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.runs, nil
}

func (f *fakeGCService) GetRun(_ context.Context, runID int64) (storageapp.GCRunDetail, error) {
	f.getCalls++
	f.gotRunID = runID
	if f.getErr != nil {
		return storageapp.GCRunDetail{}, f.getErr
	}
	return f.detail, nil
}

func gcAdminRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 42, Role: "admin"}))
}

func sampleGCRun() storage.GCRun {
	return storage.GCRun{
		ID:             900,
		Mode:           storage.GCModeDryRun,
		Status:         storage.GCRunCompleted,
		ActorKind:      storage.GCActorUser,
		StartedAt:      time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Report:         json.RawMessage(`{"candidates":{"temp":1}}`),
		ScannedObjects: 3,
		ScannedRefs:    4,
	}
}

func TestGCCreateRunAdminOnly(t *testing.T) {
	handler := NewGC(&fakeGCService{})

	anon := httptest.NewRequest(http.MethodPost, "/api/admin/gc/runs", strings.NewReader(`{"mode":"dry_run"}`))
	anonRec := httptest.NewRecorder()
	handler.CreateRun(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonRec.Code)
	}

	user := httptest.NewRequest(http.MethodPost, "/api/admin/gc/runs", strings.NewReader(`{"mode":"dry_run"}`))
	user = user.WithContext(platform.WithActor(user.Context(), platform.Actor{ID: 7, Role: "user"}))
	userRec := httptest.NewRecorder()
	handler.CreateRun(userRec, user)
	if userRec.Code != http.StatusForbidden {
		t.Fatalf("user status = %d", userRec.Code)
	}
	if strings.Contains(userRec.Body.String(), "candidates") {
		t.Fatalf("forbidden response leaked body: %s", userRec.Body.String())
	}
}

func TestGCCreateRunApplyRequiresConfirmation(t *testing.T) {
	service := &fakeGCService{run: sampleGCRun()}
	handler := NewGC(service)
	req := gcAdminRequest(http.MethodPost, "/api/admin/gc/runs", `{"mode":"apply"}`)
	rec := httptest.NewRecorder()

	handler.CreateRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code != "confirmation_required" {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
	if service.runCalls != 0 {
		t.Fatalf("service was called without confirmation")
	}
}

func TestGCCreateRunRejectsInvalidMode(t *testing.T) {
	service := &fakeGCService{}
	handler := NewGC(service)
	for _, body := range []string{`{"mode":"destroy"}`, `{}`, `not json`} {
		req := gcAdminRequest(http.MethodPost, "/api/admin/gc/runs", body)
		rec := httptest.NewRecorder()
		handler.CreateRun(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d", body, rec.Code)
		}
	}
	if service.runCalls != 0 {
		t.Fatalf("service calls = %d", service.runCalls)
	}
}

func TestGCCreateRunMapsRunningConflict(t *testing.T) {
	service := &fakeGCService{runErr: platform.NewError(http.StatusConflict, "gc_running", "already running")}
	handler := NewGC(service)
	req := gcAdminRequest(http.MethodPost, "/api/admin/gc/runs", `{"mode":"apply","confirm":true}`)
	rec := httptest.NewRecorder()

	handler.CreateRun(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gc_running") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if service.gotMode != storage.GCModeApply || service.gotActorID != 42 {
		t.Fatalf("service got mode/actor = %q/%d", service.gotMode, service.gotActorID)
	}
}

func TestGCCreateRunReturnsRunView(t *testing.T) {
	service := &fakeGCService{run: sampleGCRun()}
	handler := NewGC(service)
	req := gcAdminRequest(http.MethodPost, "/api/admin/gc/runs", `{"mode":"dry_run"}`)
	rec := httptest.NewRecorder()

	handler.CreateRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data gcRunDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.ID != "900" || envelope.Data.Mode != "dry_run" || envelope.Data.Status != "completed" {
		t.Fatalf("data = %#v", envelope.Data)
	}
	if envelope.Data.Report == nil || !strings.Contains(string(envelope.Data.Report), `"temp":1`) {
		t.Fatalf("report = %s", string(envelope.Data.Report))
	}
	if envelope.Data.ActorKind != "user" {
		t.Fatalf("actor kind = %q", envelope.Data.ActorKind)
	}
	if service.gotMode != storage.GCModeDryRun {
		t.Fatalf("mode = %q", service.gotMode)
	}
}

func TestGCListRuns(t *testing.T) {
	service := &fakeGCService{runs: []storage.GCRun{sampleGCRun()}}
	handler := NewGC(service)

	req := gcAdminRequest(http.MethodGet, "/api/admin/gc/runs?limit=5", "")
	rec := httptest.NewRecorder()
	handler.ListRuns(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if service.gotLimit != 5 {
		t.Fatalf("limit = %d", service.gotLimit)
	}
	var envelope struct {
		Data []gcRunDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != "900" {
		t.Fatalf("data = %#v", envelope.Data)
	}

	for _, raw := range []string{"limit=0", "limit=-1", "limit=101", "limit=abc"} {
		req := gcAdminRequest(http.MethodGet, "/api/admin/gc/runs?"+raw, "")
		rec := httptest.NewRecorder()
		handler.ListRuns(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", raw, rec.Code)
		}
	}

	user := httptest.NewRequest(http.MethodGet, "/api/admin/gc/runs", nil)
	user = user.WithContext(platform.WithActor(user.Context(), platform.Actor{ID: 7, Role: "user"}))
	userRec := httptest.NewRecorder()
	handler.ListRuns(userRec, user)
	if userRec.Code != http.StatusForbidden {
		t.Fatalf("user status = %d", userRec.Code)
	}
}

func TestGCGetRun(t *testing.T) {
	run := sampleGCRun()
	tombstone := storage.GCTombstone{
		ID: 5, RunID: run.ID, ObjectSHA: strings.Repeat("ab", 32), Size: 12,
		MIME: "text/plain", RelativePath: "objects/ab/ab/" + strings.Repeat("ab", 32),
		Reason: storage.GCCandidateTemp, DeletedAt: time.Date(2026, 6, 1, 0, 1, 0, 0, time.UTC),
	}
	service := &fakeGCService{detail: storageapp.GCRunDetail{Run: run, Tombstones: []storage.GCTombstone{tombstone}}}
	handler := NewGC(service)

	req := gcAdminRequest(http.MethodGet, "/api/admin/gc/runs/900", "")
	req.SetPathValue("gcRunId", "900")
	rec := httptest.NewRecorder()
	handler.GetRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if service.gotRunID != 900 {
		t.Fatalf("run id = %d", service.gotRunID)
	}
	var envelope struct {
		Data struct {
			gcRunDTO
			Tombstones []gcTombstoneDTO `json:"tombstones"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.ID != "900" || len(envelope.Data.Tombstones) != 1 {
		t.Fatalf("data = %#v", envelope.Data)
	}
	if envelope.Data.Tombstones[0].ID != "5" || envelope.Data.Tombstones[0].RunID != "900" {
		t.Fatalf("tombstone = %#v", envelope.Data.Tombstones[0])
	}
	if envelope.Data.Tombstones[0].Reason != storage.GCCandidateTemp {
		t.Fatalf("reason = %q", envelope.Data.Tombstones[0].Reason)
	}

	for _, raw := range []string{"abc", "0", "-3"} {
		req := gcAdminRequest(http.MethodGet, "/api/admin/gc/runs/"+raw, "")
		req.SetPathValue("gcRunId", raw)
		rec := httptest.NewRecorder()
		handler.GetRun(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q status = %d", raw, rec.Code)
		}
	}

	service.getErr = platform.NotFound("gc_run")
	req = gcAdminRequest(http.MethodGet, "/api/admin/gc/runs/901", "")
	req.SetPathValue("gcRunId", "901")
	rec = httptest.NewRecorder()
	handler.GetRun(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", rec.Code)
	}

	user := httptest.NewRequest(http.MethodGet, "/api/admin/gc/runs/900", nil)
	user.SetPathValue("gcRunId", "900")
	user = user.WithContext(platform.WithActor(user.Context(), platform.Actor{ID: 7, Role: "user"}))
	userRec := httptest.NewRecorder()
	handler.GetRun(userRec, user)
	if userRec.Code != http.StatusForbidden {
		t.Fatalf("user status = %d", userRec.Code)
	}
}
