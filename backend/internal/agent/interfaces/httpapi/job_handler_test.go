package agentapi_test

// Task 46 (P2-T6): HTTP wiring of the job cancel/retry control API. The
// handler only parses IDs, enforces the logged-in actor and renders the shared
// {data: ...} envelope; every lifecycle rule lives in the application service.
// Both the plan's canonical /api/jobs/{id}/{action} route and the
// version-scoped /api/versions/{id}/jobs/{jobId}/{action} route used by the
// JobCard are mounted.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type stubJobControlService struct {
	view agentapp.JobView
	err  error

	cancelCalls int
	retryCalls  int
	lastActorID int64
	lastVersion int64
	lastJobID   int64
}

func (s *stubJobControlService) Cancel(_ context.Context, actorID, versionID, jobID int64) (agentapp.JobView, error) {
	s.cancelCalls++
	s.lastActorID, s.lastVersion, s.lastJobID = actorID, versionID, jobID
	return s.view, s.err
}

func (s *stubJobControlService) Retry(_ context.Context, actorID, versionID, jobID int64) (agentapp.JobView, error) {
	s.retryCalls++
	s.lastActorID, s.lastVersion, s.lastJobID = actorID, versionID, jobID
	return s.view, s.err
}

var _ agentapi.JobControlService = (*stubJobControlService)(nil)

func newJobControlHandlerServer(service agentapi.JobControlService, actor *platform.Actor) http.Handler {
	handler := agentapi.NewJobControlHandler(service)
	mux := http.NewServeMux()
	handler.Register(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if actor != nil {
				r = r.WithContext(platform.WithActor(r.Context(), *actor))
			}
			next.ServeHTTP(w, r)
		})
	})
	return mux
}

func postJobControl(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeJobControlData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("response is not a {data: ...} envelope: %#v", payload)
	}
	return data
}

func assertJobControlError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, status, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	envelope, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("response is not an error envelope: %#v", payload)
	}
	if envelope["code"] != code {
		t.Fatalf("error code = %#v, want %q", envelope["code"], code)
	}
}

func TestJobControlHandler(t *testing.T) {
	author := &platform.Actor{ID: 71, Role: "author"}
	// Task 46 fix round 2: a canceled 1/3 job reports the truthful pre-cancel
	// attempt position (1), not the attempts=max_attempts storage marker.
	cancelled := agentapp.JobView{
		JobID: 501, VersionID: 10, Kind: "execute.batch",
		State: agentapp.JobStateCancelled, Attempts: 1, MaxAttempts: 3, UpdatedAt: handlerTime,
	}
	requeued := cancelled
	requeued.State = "open"
	requeued.Attempts = 0

	t.Run("canonical cancel route returns the job view", func(t *testing.T) {
		stub := &stubJobControlService{view: cancelled}
		recorder := postJobControl(t, newJobControlHandlerServer(stub, author), "/api/jobs/501/cancel")

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
			t.Fatalf("content type = %q", contentType)
		}
		data := decodeJobControlData(t, recorder)
		want := map[string]any{
			"job_id":       "501",
			"version_id":   "10",
			"kind":         "execute.batch",
			"state":        "cancelled",
			"attempts":     float64(1),
			"max_attempts": float64(3),
			"updated_at":   "2026-10-31T12:00:00Z",
		}
		for key, value := range want {
			if data[key] != value {
				t.Fatalf("data[%q] = %#v, want %#v (data %#v)", key, data[key], value, data)
			}
		}
		if stub.cancelCalls != 1 || stub.retryCalls != 0 {
			t.Fatalf("service calls = cancel %d retry %d", stub.cancelCalls, stub.retryCalls)
		}
		if stub.lastActorID != 71 || stub.lastVersion != 0 || stub.lastJobID != 501 {
			t.Fatalf("service args = actor %d version %d job %d", stub.lastActorID, stub.lastVersion, stub.lastJobID)
		}
	})

	t.Run("version-scoped cancel route forwards the path version", func(t *testing.T) {
		stub := &stubJobControlService{view: cancelled}
		recorder := postJobControl(t, newJobControlHandlerServer(stub, author), "/api/versions/10/jobs/501/cancel")

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		if stub.lastActorID != 71 || stub.lastVersion != 10 || stub.lastJobID != 501 {
			t.Fatalf("service args = actor %d version %d job %d", stub.lastActorID, stub.lastVersion, stub.lastJobID)
		}
	})

	t.Run("retry routes return the open job", func(t *testing.T) {
		for _, path := range []string{
			"/api/jobs/501/retry",
			"/api/versions/10/jobs/501/retry",
		} {
			stub := &stubJobControlService{view: requeued}
			recorder := postJobControl(t, newJobControlHandlerServer(stub, author), path)
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200 (body %s)", path, recorder.Code, recorder.Body.String())
			}
			data := decodeJobControlData(t, recorder)
			if data["state"] != "open" {
				t.Fatalf("%s: state = %#v, want open", path, data["state"])
			}
			if stub.retryCalls != 1 || stub.cancelCalls != 0 {
				t.Fatalf("%s: service calls = cancel %d retry %d", path, stub.cancelCalls, stub.retryCalls)
			}
		}
	})

	t.Run("invalid IDs are 400 and never reach the service", func(t *testing.T) {
		stub := &stubJobControlService{}
		handler := newJobControlHandlerServer(stub, author)
		for _, path := range []string{
			"/api/jobs/abc/cancel",
			"/api/jobs/0/retry",
			"/api/versions/abc/jobs/501/cancel",
			"/api/versions/10/jobs/abc/retry",
		} {
			recorder := postJobControl(t, handler, path)
			assertJobControlError(t, recorder, http.StatusBadRequest, "invalid_argument")
		}
		if stub.cancelCalls != 0 || stub.retryCalls != 0 {
			t.Fatalf("invalid IDs reached the service")
		}
	})

	t.Run("missing actor is 401", func(t *testing.T) {
		stub := &stubJobControlService{view: cancelled}
		recorder := postJobControl(t, newJobControlHandlerServer(stub, nil), "/api/jobs/501/cancel")
		assertJobControlError(t, recorder, http.StatusUnauthorized, "unauthenticated")
		if stub.cancelCalls != 0 {
			t.Fatalf("unauthenticated request reached the service")
		}
	})

	t.Run("application errors keep their typed status", func(t *testing.T) {
		cases := []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{"missing", platform.NotFound("job"), http.StatusNotFound, "not_found"},
			{"state", platform.StateConflict("job is not running"), http.StatusConflict, "state_conflict"},
			{"unavailable", platform.Unavailable("job control is not configured"), http.StatusServiceUnavailable, "unavailable"},
		}
		for _, testCase := range cases {
			stub := &stubJobControlService{err: testCase.err}
			recorder := postJobControl(t, newJobControlHandlerServer(stub, author), "/api/versions/10/jobs/501/retry")
			assertJobControlError(t, recorder, testCase.status, testCase.code)
		}
	})
}
