package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var handlerTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type stubTeamRoomService struct {
	view     agentapp.TeamRoomView
	err      error
	calls    int
	lastVer  int64
	lastTeam string
	lastLim  int
}

func (s *stubTeamRoomService) Read(_ context.Context, versionID int64, team string, limit int) (agentapp.TeamRoomView, error) {
	s.calls++
	s.lastVer = versionID
	s.lastTeam = team
	s.lastLim = limit
	if s.err != nil {
		return agentapp.TeamRoomView{}, s.err
	}
	return s.view, nil
}

var _ agentapi.TeamRoomService = (*stubTeamRoomService)(nil)

func newHandlerServer(t *testing.T) (http.Handler, *stubTeamRoomService) {
	t.Helper()
	stub := &stubTeamRoomService{}
	handler := agentapi.NewTeamRoom(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/team-rooms/{team}", handler.Read)
	return mux, stub
}

func teamRoomRequest(t *testing.T, handler http.Handler, path string, actor *platform.Actor) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if actor != nil {
		request = request.WithContext(platform.WithActor(request.Context(), *actor))
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodePayload(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func sampleRoomView() agentapp.TeamRoomView {
	runID := int64(900)
	return agentapp.TeamRoomView{
		Team: "clarification",
		Digest: &agent.TeamRoomDigest{
			ID:            400,
			VersionID:     10,
			Team:          agent.TeamClarification,
			Summary:       "团队摘要",
			PinnedPostIDs: []int64{301, 302},
			TokenEstimate: 42,
			Version:       3,
			UpdatedAt:     handlerTime,
		},
		Posts: []agent.TeamRoomPost{
			{
				ID:         302,
				VersionID:  10,
				Team:       agent.TeamClarification,
				Kind:       agent.PostKindDecision,
				Content:    "决定使用方案 A",
				Refs:       []agent.PostRef{{Kind: "requirement", ID: "501"}},
				AgentRunID: &runID,
				CreatedAt:  handlerTime,
			},
			{
				ID:        301,
				VersionID: 10,
				Team:      agent.TeamClarification,
				Kind:      agent.PostKindFinding,
				Content:   "接口已就绪",
				Refs:      []agent.PostRef{},
				CreatedAt: handlerTime.Add(-time.Minute),
			},
		},
	}
}

func TestTeamRoomReadReturnsDigestAndPosts(t *testing.T) {
	handler, stub := newHandlerServer(t)
	stub.view = sampleRoomView()

	recorder := teamRoomRequest(t, handler, "/api/versions/10/team-rooms/clarification", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 1 || stub.lastVer != 10 || stub.lastTeam != "clarification" || stub.lastLim != 20 {
		t.Fatalf("service call = %#v", stub)
	}

	payload := decodePayload(t, recorder)
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v", payload)
	}
	if data["team"] != "clarification" {
		t.Fatalf("team = %#v", data["team"])
	}
	digest, ok := data["digest"].(map[string]any)
	if !ok {
		t.Fatalf("digest = %#v", data["digest"])
	}
	if digest["summary"] != "团队摘要" || digest["version"] != float64(3) || digest["token_estimate"] != float64(42) {
		t.Fatalf("digest = %#v", digest)
	}
	if digest["updated_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("updated_at = %#v", digest["updated_at"])
	}
	pinned := digest["pinned_post_ids"].([]any)
	if len(pinned) != 2 || pinned[0] != "301" || pinned[1] != "302" {
		t.Fatalf("pinned = %#v", pinned)
	}

	posts := data["posts"].([]any)
	if len(posts) != 2 {
		t.Fatalf("posts = %#v", posts)
	}
	first := posts[0].(map[string]any)
	if first["id"] != "302" || first["kind"] != "decision" || first["content"] != "决定使用方案 A" {
		t.Fatalf("first post = %#v", first)
	}
	if first["agent_run_id"] != "900" {
		t.Fatalf("agent_run_id = %#v", first["agent_run_id"])
	}
	refs := first["refs"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["id"] != "501" {
		t.Fatalf("refs = %#v", refs)
	}
	second := posts[1].(map[string]any)
	if second["agent_run_id"] != nil {
		t.Fatalf("agent_run_id = %#v, want null", second["agent_run_id"])
	}
}

func TestTeamRoomReadParsesLimit(t *testing.T) {
	handler, stub := newHandlerServer(t)
	stub.view = sampleRoomView()

	recorder := teamRoomRequest(t, handler, "/api/versions/10/team-rooms/design?limit=5", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if stub.lastLim != 5 || stub.lastTeam != "design" {
		t.Fatalf("args = %d/%q", stub.lastLim, stub.lastTeam)
	}

	for _, limit := range []string{"0", "abc", "101", "-1"} {
		recorder := teamRoomRequest(t, handler, "/api/versions/10/team-rooms/clarification?limit="+limit, &platform.Actor{ID: 42})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s status = %d", limit, recorder.Code)
		}
		errorBody := decodePayload(t, recorder)["error"].(map[string]any)
		if field := errorBody["details"].(map[string]any)["field"]; field != "limit" {
			t.Fatalf("limit=%s field = %#v", limit, field)
		}
	}
}

func TestTeamRoomReadValidationAndErrors(t *testing.T) {
	handler, stub := newHandlerServer(t)
	stub.view = sampleRoomView()

	t.Run("invalid team", func(t *testing.T) {
		recorder := teamRoomRequest(t, handler, "/api/versions/10/team-rooms/ops", &platform.Actor{ID: 42})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
		errorBody := decodePayload(t, recorder)["error"].(map[string]any)
		if field := errorBody["details"].(map[string]any)["field"]; field != "team" {
			t.Fatalf("field = %#v", field)
		}
		if stub.calls != 0 {
			t.Fatalf("service called for invalid team")
		}
	})

	t.Run("invalid version id", func(t *testing.T) {
		recorder := teamRoomRequest(t, handler, "/api/versions/not-an-id/team-rooms/clarification", &platform.Actor{ID: 42})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
		if stub.calls != 0 {
			t.Fatalf("service called for invalid id")
		}
	})

	t.Run("unauthenticated", func(t *testing.T) {
		recorder := teamRoomRequest(t, handler, "/api/versions/10/team-rooms/clarification", nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", recorder.Code)
		}
		if stub.calls != 0 {
			t.Fatalf("service called without actor")
		}
	})

	t.Run("service not found", func(t *testing.T) {
		stub.err = platform.NotFound("version")
		recorder := teamRoomRequest(t, handler, "/api/versions/10/team-rooms/clarification", &platform.Actor{ID: 42})
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d", recorder.Code)
		}
		if decodePayload(t, recorder)["error"].(map[string]any)["code"] != "not_found" {
			t.Fatalf("body = %s", recorder.Body.String())
		}
		stub.err = nil
	})
}

func TestTeamRoomReadEmptyDigestAndPosts(t *testing.T) {
	handler, stub := newHandlerServer(t)
	stub.view = agentapp.TeamRoomView{Team: "clarification", Digest: nil, Posts: []agent.TeamRoomPost{}}

	recorder := teamRoomRequest(t, handler, "/api/versions/10/team-rooms/clarification", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	data := decodePayload(t, recorder)["data"].(map[string]any)
	if data["digest"] != nil {
		t.Fatalf("digest = %#v, want null", data["digest"])
	}
	posts, ok := data["posts"].([]any)
	if !ok || len(posts) != 0 {
		t.Fatalf("posts = %#v", data["posts"])
	}
}
