package auditapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
)

type fakeService struct {
	entries   []audit.Entry
	hasMore   bool
	err       error
	lastLimit int
	lastBefore *int64
	lastFilter audit.Filter
}

func (f *fakeService) List(_ context.Context, filter audit.Filter, limit int, beforeID *int64) ([]audit.Entry, bool, error) {
	f.lastFilter = filter
	f.lastLimit = limit
	f.lastBefore = beforeID
	if f.err != nil {
		return nil, false, f.err
	}
	return f.entries, f.hasMore, nil
}

func newHandler() (*Handler, *fakeService) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	actor := int64(42)
	svc := &fakeService{
		entries: []audit.Entry{
			{ID: 9002, ActorID: &actor, Action: "auth.login", TargetKind: "user", TargetID: "42", RequestID: "req_2", IP: "192.0.2.1", UserAgent: "ua", Metadata: map[string]any{"username": "admin"}, CreatedAt: now},
			{ID: 9001, Action: "auth.login_failed", CreatedAt: now},
		},
		hasMore: true,
	}
	return New(svc), svc
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) ([]entryDTO, string, bool) {
	t.Helper()
	var body struct {
		Data       []entryDTO `json:"data"`
		NextCursor string     `json:"next_cursor"`
		HasMore    bool       `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return body.Data, body.NextCursor, body.HasMore
}

func TestListFiltersAndPaginates(t *testing.T) {
	handler, svc := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs?action=auth.login&actor=42&limit=2&cursor=9003", nil)
	rec := httptest.NewRecorder()
	handler.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	items, nextCursor, hasMore := decodeList(t, rec)
	if len(items) != 2 || !hasMore || nextCursor != "9001" {
		t.Fatalf("items = %#v, cursor = %q, hasMore = %v", items, nextCursor, hasMore)
	}
	if items[0].ID != "9002" || items[0].ActorID != "42" || items[0].Action != "auth.login" {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[0].Metadata["username"] != "admin" {
		t.Fatalf("metadata = %#v", items[0].Metadata)
	}
	if items[0].CreatedAt != "2026-10-31T12:00:00Z" {
		t.Fatalf("created_at = %q", items[0].CreatedAt)
	}
	if items[1].ActorID != "" || items[1].Metadata == nil {
		t.Fatalf("second item = %#v", items[1])
	}

	if svc.lastFilter.Action != "auth.login" || svc.lastFilter.ActorID == nil || *svc.lastFilter.ActorID != 42 {
		t.Fatalf("filter = %#v", svc.lastFilter)
	}
	if svc.lastLimit != 2 {
		t.Fatalf("limit = %d", svc.lastLimit)
	}
	if svc.lastBefore == nil || *svc.lastBefore != 9003 {
		t.Fatalf("before = %v", svc.lastBefore)
	}
}

func TestListRejectsInvalidActorAndCursor(t *testing.T) {
	handler, _ := newHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs?actor=abc", nil)
	rec := httptest.NewRecorder()
	handler.List(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid actor status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs?cursor=abc", nil)
	rec = httptest.NewRecorder()
	handler.List(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", rec.Code)
	}
}

func TestListDefaultsLimitAndCursor(t *testing.T) {
	handler, svc := newHandler()
	svc.hasMore = false
	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil)
	rec := httptest.NewRecorder()
	handler.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if svc.lastLimit != auditapp.DefaultListLimit {
		t.Fatalf("limit = %d, want %d", svc.lastLimit, auditapp.DefaultListLimit)
	}
	if svc.lastBefore != nil {
		t.Fatalf("before = %v, want nil", svc.lastBefore)
	}
	if svc.lastFilter != (audit.Filter{}) {
		t.Fatalf("filter = %#v", svc.lastFilter)
	}
	_, nextCursor, hasMore := decodeList(t, rec)
	if nextCursor != "" || hasMore {
		t.Fatalf("cursor = %q, hasMore = %v", nextCursor, hasMore)
	}
}

func TestListReturnsEmptyArrayNotNull(t *testing.T) {
	handler, svc := newHandler()
	svc.entries = nil
	svc.hasMore = false
	rec := httptest.NewRecorder()
	handler.List(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil))

	items, _, _ := decodeList(t, rec)
	if items == nil || len(items) != 0 {
		t.Fatalf("data = %#v, want []", items)
	}
}

func TestListMapsRepositoryErrorTo500(t *testing.T) {
	handler, svc := newHandler()
	svc.err = errors.New("boom")
	rec := httptest.NewRecorder()
	handler.List(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs", nil))

	if rec.Code != http.StatusInternalServerError {
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
	if body.Error.Code != "internal" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}
