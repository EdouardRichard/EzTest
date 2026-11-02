package settingsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

type fakeService struct {
	list      []settings.Setting
	get       map[string]settings.Setting
	revisions map[string][]settings.Revision
	updated   settings.Setting
	updateErr error
	lastActor int64
}

func (f *fakeService) List(context.Context) ([]settings.Setting, error) {
	return f.list, nil
}

func (f *fakeService) Get(_ context.Context, key string) (settings.Setting, error) {
	item, ok := f.get[key]
	if !ok {
		return settings.Setting{}, platform.NotFound("setting")
	}
	return item, nil
}

func (f *fakeService) Update(_ context.Context, key string, _ any, actorID int64) (settings.Setting, error) {
	f.lastActor = actorID
	if f.updateErr != nil {
		return settings.Setting{}, f.updateErr
	}
	return f.updated, nil
}

func (f *fakeService) Revisions(_ context.Context, key string, _ int, _ *int64) ([]settings.Revision, error) {
	return f.revisions[key], nil
}

func newHandler() (*Handler, *fakeService) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	svc := &fakeService{
		list: []settings.Setting{
			{Key: "execution.global_concurrency", Category: "execution", Type: settings.TypeNumber, Value: json.RawMessage("3"), Version: 1, UpdatedAt: now},
			{Key: "llm.api_key", Category: "llm", Type: settings.TypeString, Value: json.RawMessage(`"super-secret"`), Secret: true, Version: 1, UpdatedAt: now},
		},
		get: map[string]settings.Setting{
			"execution.global_concurrency": {Key: "execution.global_concurrency", Category: "execution", Type: settings.TypeNumber, Value: json.RawMessage("3"), Version: 1, UpdatedAt: now},
			"llm.api_key":                  {Key: "llm.api_key", Category: "llm", Type: settings.TypeString, Value: json.RawMessage(`"super-secret"`), Secret: true, Version: 1, UpdatedAt: now},
			"retention.gc_dry_run":         {Key: "retention.gc_dry_run", Category: "retention", Type: settings.TypeBool, Value: json.RawMessage("true"), Version: 1, UpdatedAt: now},
		},
		revisions: map[string][]settings.Revision{
			"execution.global_concurrency": {
				{ID: 11, Key: "execution.global_concurrency", OldValue: json.RawMessage("3"), NewValue: json.RawMessage("5"), CreatedAt: now},
				{ID: 10, Key: "execution.global_concurrency", OldValue: json.RawMessage("2"), NewValue: json.RawMessage("3"), CreatedAt: now},
				{ID: 9, Key: "execution.global_concurrency", OldValue: json.RawMessage("1"), NewValue: json.RawMessage("2"), CreatedAt: now},
			},
			"llm.api_key": {
				{ID: 12, Key: "llm.api_key", OldValue: json.RawMessage(`"old-secret"`), NewValue: json.RawMessage(`"new-secret"`), CreatedAt: now},
			},
		},
		updated: settings.Setting{
			Key: "execution.global_concurrency", Category: "execution", Type: settings.TypeNumber,
			Value: json.RawMessage("5"), Version: 2, UpdatedAt: now,
		},
	}
	return New(svc), svc
}

func TestListMasksSecretValues(t *testing.T) {
	handler, _ := newHandler()
	rec := httptest.NewRecorder()
	handler.List(rec, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Data []settingDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data = %#v", body.Data)
	}
	if body.Data[0].Value == nil || body.Data[0].Value.(float64) != 3 {
		t.Fatalf("number value = %#v", body.Data[0].Value)
	}
	if body.Data[1].Value != nil || !body.Data[1].IsSecret {
		t.Fatalf("secret leaked: %#v", body.Data[1])
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	handler, _ := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings/nope", nil)
	req.SetPathValue("key", "nope")
	rec := httptest.NewRecorder()
	handler.Get(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUpdateRequiresActorBodyAndValue(t *testing.T) {
	handler, svc := newHandler()

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings/execution.global_concurrency", strings.NewReader(`{"value":5}`))
	req.SetPathValue("key", "execution.global_concurrency")
	rec := httptest.NewRecorder()
	handler.Update(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing actor status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/settings/execution.global_concurrency", strings.NewReader(`{"value":`))
	req = req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 42, Role: "admin"}))
	req.SetPathValue("key", "execution.global_concurrency")
	rec = httptest.NewRecorder()
	handler.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/settings/execution.global_concurrency", strings.NewReader(`{"value":null}`))
	req = req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 42, Role: "admin"}))
	req.SetPathValue("key", "execution.global_concurrency")
	rec = httptest.NewRecorder()
	handler.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("null value status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/settings/execution.global_concurrency", strings.NewReader(`{"value":5}`))
	req = req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 42, Role: "admin"}))
	req.SetPathValue("key", "execution.global_concurrency")
	rec = httptest.NewRecorder()
	handler.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid update status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastActor != 42 {
		t.Fatalf("actor = %d", svc.lastActor)
	}
}

func TestRevisionsMasksSecretAndPaginates(t *testing.T) {
	handler, _ := newHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings/revisions?key=execution.global_concurrency&limit=2", nil)
	rec := httptest.NewRecorder()
	handler.Revisions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data       []revisionDTO `json:"data"`
		NextCursor string        `json:"next_cursor"`
		HasMore    bool          `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 || !body.HasMore || body.NextCursor != "10" {
		t.Fatalf("page = %#v", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/settings/revisions?key=llm.api_key", nil)
	rec = httptest.NewRecorder()
	handler.Revisions(rec, req)
	var secretBody struct {
		Data []revisionDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &secretBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(secretBody.Data) != 1 || secretBody.Data[0].OldValue != nil || secretBody.Data[0].NewValue != nil {
		t.Fatalf("secret revision leaked: %#v", secretBody.Data)
	}
}

func TestRevisionsRequiresKey(t *testing.T) {
	handler, _ := newHandler()
	rec := httptest.NewRecorder()
	handler.Revisions(rec, httptest.NewRequest(http.MethodGet, "/api/admin/settings/revisions", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUpdateNotFoundMapsToError(t *testing.T) {
	handler, svc := newHandler()
	svc.updateErr = platform.NotFound("setting")
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings/nope", strings.NewReader(`{"value":1}`))
	req = req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 1, Role: "admin"}))
	req.SetPathValue("key", "nope")
	rec := httptest.NewRecorder()
	handler.Update(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}
