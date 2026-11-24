package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* --------------------------------- stubs ---------------------------------- */

type stubSkillService struct {
	skills   []agent.Skill
	skill    agent.Skill
	changed  bool
	versions []agent.SkillVersion
	hasMore  bool
	err      error

	listCalls   int
	getCalls    int
	createCalls int
	updateCalls int
	deleteCalls int
	revCalls    int

	lastActor   platform.Actor
	lastInput   agent.Skill
	lastUpdate  agent.SkillUpdate
	lastNote    string
	lastLimit   int
	lastBefore  *int
	createdNote string
}

func (s *stubSkillService) List(context.Context) ([]agent.Skill, error) {
	s.listCalls++
	return s.skills, s.err
}
func (s *stubSkillService) Get(_ context.Context, _ string) (agent.Skill, error) {
	s.getCalls++
	return s.skill, s.err
}
func (s *stubSkillService) Create(_ context.Context, actor platform.Actor, input agent.Skill, note string) (agent.Skill, error) {
	s.createCalls++
	s.lastActor = actor
	s.lastInput = input
	s.createdNote = note
	return s.skill, s.err
}
func (s *stubSkillService) Update(_ context.Context, actor platform.Actor, _ string, update agent.SkillUpdate, note string) (agent.Skill, bool, error) {
	s.updateCalls++
	s.lastActor = actor
	s.lastUpdate = update
	s.lastNote = note
	return s.skill, s.changed, s.err
}
func (s *stubSkillService) Delete(_ context.Context, actor platform.Actor, _ string) error {
	s.deleteCalls++
	s.lastActor = actor
	return s.err
}
func (s *stubSkillService) Versions(_ context.Context, _ string, limit int, before *int) ([]agent.SkillVersion, bool, error) {
	s.revCalls++
	s.lastLimit = limit
	s.lastBefore = before
	return s.versions, s.hasMore, s.err
}

var _ agentapi.SkillService = (*stubSkillService)(nil)
var _ agentapi.SkillService = (*skillapp.Service)(nil)

func skillTestActor(role string) *http.Request {
	return func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
		if role != "" {
			r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: role}))
		}
		return r
	}()
}

func sampleSkill() agent.Skill {
	return agent.Skill{
		ID:          42,
		Name:        "api-recon",
		Description: "Probe an HTTP API surface",
		Body:        "# API recon\n\nProbe.",
		Metadata:    json.RawMessage(`{"tags":["http"]}`),
		Version:     3,
		Enabled:     true,
		CreatedAt:   time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC),
	}
}

func serveSkill(t *testing.T, svc agentapi.SkillService, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	identity := func(h http.Handler) http.Handler { return h }
	agentapi.RegisterSkillRoutes(mux, identity, agentapi.NewSkillHandler(svc))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

/* ------------------------------- admin gate ------------------------------- */

func TestSkillRoutesRejectNonAdmin(t *testing.T) {
	stub := &stubSkillService{skill: sampleSkill()}
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/skills", ""},
		{http.MethodPost, "/api/skills", `{"name":"x","description":"d","body":"b"}`},
		{http.MethodGet, "/api/skills/api-recon", ""},
		{http.MethodPatch, "/api/skills/api-recon", `{"body":"b"}`},
		{http.MethodDelete, "/api/skills/api-recon", ""},
		{http.MethodGet, "/api/skills/api-recon/versions", ""},
	}
	for _, route := range routes {
		for _, role := range []string{"", "user"} {
			r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			if role != "" {
				r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: role}))
			}
			rec := serveSkill(t, stub, r)
			want := http.StatusUnauthorized
			if role == "user" {
				want = http.StatusForbidden
			}
			if rec.Code != want {
				t.Fatalf("%s %s role=%q: status = %d, want %d", route.method, route.path, role, rec.Code, want)
			}
		}
	}
	if stub.listCalls+stub.getCalls+stub.createCalls+stub.updateCalls+stub.deleteCalls+stub.revCalls != 0 {
		t.Fatal("a rejected request reached the service")
	}
}

/* --------------------------------- reads ---------------------------------- */

func TestSkillListDTOShape(t *testing.T) {
	stub := &stubSkillService{skills: []agent.Skill{sampleSkill()}}
	r := skillTestActor("admin")
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("data = %#v", payload.Data)
	}
	item := payload.Data[0]
	if item["id"] != "42" {
		t.Fatalf("id = %#v, want string \"42\"", item["id"])
	}
	if item["name"] != "api-recon" || item["version"] != float64(3) || item["enabled"] != true {
		t.Fatalf("item = %#v", item)
	}
	if item["created_at"] != "2026-11-16T10:00:00Z" {
		t.Fatalf("created_at = %#v", item["created_at"])
	}
	meta, ok := item["metadata"].(map[string]any)
	if !ok || meta["tags"] == nil {
		t.Fatalf("metadata = %#v, want a JSON object", item["metadata"])
	}
}

func TestSkillGetIncludesBody(t *testing.T) {
	stub := &stubSkillService{skill: sampleSkill()}
	r := httptest.NewRequest(http.MethodGet, "/api/skills/api-recon", nil)
	r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Data["body"] != "# API recon\n\nProbe." {
		t.Fatalf("body = %#v", payload.Data["body"])
	}
}

func TestSkillGetRejectsInvalidName(t *testing.T) {
	stub := &stubSkillService{skill: sampleSkill()}
	r := httptest.NewRequest(http.MethodGet, "/api/skills/Bad%20Name", nil)
	r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if stub.getCalls != 0 {
		t.Fatal("invalid name reached the service")
	}
}

/* --------------------------------- writes --------------------------------- */

func TestSkillCreateDecodesAndDefaultsEnabled(t *testing.T) {
	stub := &stubSkillService{skill: sampleSkill()}
	body := `{"name":"api-recon","description":"d","body":"# b","note":"initial"}`
	r := httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(body))
	r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if stub.createCalls != 1 || stub.lastInput.Name != "api-recon" || !stub.lastInput.Enabled {
		t.Fatalf("create input = %#v calls=%d, want enabled default true", stub.lastInput, stub.createCalls)
	}
	if stub.createdNote != "initial" {
		t.Fatalf("note = %q", stub.createdNote)
	}
	if stub.lastActor.ID != 5 {
		t.Fatalf("actor = %#v", stub.lastActor)
	}
}

func TestSkillCreateRequiresFields(t *testing.T) {
	stub := &stubSkillService{skill: sampleSkill()}
	for _, body := range []string{`{}`, `{"name":"x"}`, `{"name":"x","body":"b","description":"d","extra":1}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(body))
		r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
		rec := serveSkill(t, stub, r)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
	if stub.createCalls != 0 {
		t.Fatal("invalid create reached the service")
	}
}

func TestSkillPatchDecodesPartialFields(t *testing.T) {
	stub := &stubSkillService{skill: sampleSkill(), changed: true}
	body := `{"body":"new body","enabled":false,"note":"runtime edit","expected_version":3}`
	r := httptest.NewRequest(http.MethodPatch, "/api/skills/api-recon", strings.NewReader(body))
	r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if stub.updateCalls != 1 {
		t.Fatalf("update calls = %d", stub.updateCalls)
	}
	u := stub.lastUpdate
	if u.Body == nil || *u.Body != "new body" || u.Enabled == nil || *u.Enabled {
		t.Fatalf("update = %#v", u)
	}
	if u.Description != nil || u.Metadata != nil {
		t.Fatalf("absent fields must stay nil: %#v", u)
	}
	if u.ExpectedVersion == nil || *u.ExpectedVersion != 3 || stub.lastNote != "runtime edit" {
		t.Fatalf("expected_version/note = %#v / %q", u.ExpectedVersion, stub.lastNote)
	}
	var payload struct {
		Data    map[string]any `json:"data"`
		Changed bool           `json:"changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.Changed {
		t.Fatal("changed sibling missing")
	}
}

func TestSkillDeleteReturns204(t *testing.T) {
	stub := &stubSkillService{}
	r := httptest.NewRequest(http.MethodDelete, "/api/skills/api-recon", nil)
	r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if stub.deleteCalls != 1 || stub.lastActor.ID != 5 {
		t.Fatalf("delete calls = %d actor=%#v", stub.deleteCalls, stub.lastActor)
	}
}

func TestSkillVersionsPagination(t *testing.T) {
	stub := &stubSkillService{
		versions: []agent.SkillVersion{{Version: 3, Source: agent.SkillSourceAdmin}, {Version: 2, Source: agent.SkillSourceSeed}},
		hasMore:  true,
	}
	r := httptest.NewRequest(http.MethodGet, "/api/skills/api-recon/versions?limit=2&before_version=4", nil)
	r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if stub.lastLimit != 2 || stub.lastBefore == nil || *stub.lastBefore != 4 {
		t.Fatalf("limit/before = %d / %v", stub.lastLimit, stub.lastBefore)
	}
	var payload struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.HasMore || payload.NextCursor != "2" {
		t.Fatalf("cursor payload = %#v", payload)
	}
	if payload.Data[0]["source"] != "admin" {
		t.Fatalf("version dto = %#v", payload.Data[0])
	}
}

func TestSkillRoutesNilHandlerUnmounted(t *testing.T) {
	mux := http.NewServeMux()
	identity := func(h http.Handler) http.Handler { return h }
	agentapi.RegisterSkillRoutes(mux, identity, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("nil handler mounted routes: status = %d", rec.Code)
	}
}
