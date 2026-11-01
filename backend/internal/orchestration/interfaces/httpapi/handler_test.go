package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	"github.com/EdouardRichard/EzTest/backend/internal/orchestration/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var testTime = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type fakeService struct {
	instances []orchestration.Instance
	listErr   error
	listCalls int

	drainID         int64
	activateID      int64
	transition      orchestration.Status
	actor           platform.Actor
	transitionErr   error
	transitionCalls int

	readiness orchestrationapp.Readiness
}

func (f *fakeService) List(context.Context) ([]orchestration.Instance, error) {
	f.listCalls++
	return f.instances, f.listErr
}

func (f *fakeService) Drain(_ context.Context, id int64, actor platform.Actor) (orchestration.Instance, error) {
	f.transitionCalls++
	f.drainID, f.actor, f.transition = id, actor, orchestration.StatusDraining
	if f.transitionErr != nil {
		return orchestration.Instance{}, f.transitionErr
	}
	return fixtureInstance(id, orchestration.StatusDraining), nil
}

func (f *fakeService) Activate(_ context.Context, id int64, actor platform.Actor) (orchestration.Instance, error) {
	f.transitionCalls++
	f.activateID, f.actor, f.transition = id, actor, orchestration.StatusActive
	if f.transitionErr != nil {
		return orchestration.Instance{}, f.transitionErr
	}
	return fixtureInstance(id, orchestration.StatusActive), nil
}

func (f *fakeService) Readiness(context.Context) orchestrationapp.Readiness {
	return f.readiness
}

func fixtureInstance(id int64, status orchestration.Status) orchestration.Instance {
	drainingAt := testTime
	return orchestration.Instance{
		ID:             id,
		InstanceKey:    "host-a:42",
		Hostname:       "host-a",
		PID:            42,
		APIPort:        8080,
		NodeID:         3,
		Status:         status,
		Version:        "0.2.0",
		ActiveRequests: 5,
		ActiveWork:     2,
		StartedAt:      testTime,
		HeartbeatAt:    testTime,
		DrainingAt:     &drainingAt,
		CreatedAt:      testTime,
		UpdatedAt:      testTime,
	}
}

func actorRequest(method, target string, role string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	if role != "" {
		request = request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: 9, Role: role}))
	}
	return request
}

func decodeData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, recorder.Body.String())
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("body has no data object: %s", recorder.Body.String())
	}
	return data
}

/* ----------------------------------- list --------------------------------- */

func TestListRequiresAdmin(t *testing.T) {
	service := &fakeService{}
	handler := New(service)
	for _, role := range []string{"", "user"} {
		recorder := httptest.NewRecorder()
		handler.ListInstances(recorder, actorRequest(http.MethodGet, "/api/admin/instances", role))
		want := http.StatusForbidden
		if role == "" {
			want = http.StatusUnauthorized
		}
		if recorder.Code != want {
			t.Fatalf("role %q: status = %d, want %d", role, recorder.Code, want)
		}
	}
	if service.listCalls != 0 {
		t.Fatalf("list called %d times", service.listCalls)
	}
}

func TestListReturnsInstanceViews(t *testing.T) {
	service := &fakeService{instances: []orchestration.Instance{
		fixtureInstance(1, orchestration.StatusActive),
		fixtureInstance(2, orchestration.StatusDraining),
	}}
	handler := New(service)
	recorder := httptest.NewRecorder()
	handler.ListInstances(recorder, actorRequest(http.MethodGet, "/api/admin/instances", "admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body struct {
		Data []instanceView `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("views = %d", len(body.Data))
	}
	first := body.Data[0]
	if first.ID != "1" || first.InstanceKey != "host-a:42" || first.NodeID != 3 ||
		first.Status != "active" || first.PID != 42 || first.APIPort != 8080 ||
		first.StartedAt == "" || first.HeartbeatAt == "" ||
		first.ActiveRequests != 5 || first.ActiveWork != 2 {
		t.Fatalf("first view = %+v", first)
	}
	if body.Data[1].Status != "draining" || body.Data[1].DrainingAt == nil {
		t.Fatalf("second view = %+v", body.Data[1])
	}
}

/* -------------------------------- transitions ------------------------------ */

func TestDrainAndActivateUsePathID(t *testing.T) {
	service := &fakeService{}
	handler := New(service)

	recorder := httptest.NewRecorder()
	request := actorRequest(http.MethodPost, "/api/admin/instances/42/drain", "admin")
	request.SetPathValue("id", "42")
	handler.DrainInstance(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("drain status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if service.drainID != 42 || service.transition != orchestration.StatusDraining {
		t.Fatalf("drain call = %d/%q", service.drainID, service.transition)
	}
	if service.actor.ID != 9 || !service.actor.IsAdmin() {
		t.Fatalf("actor = %+v", service.actor)
	}
	view := decodeData(t, recorder)
	if view["status"] != "draining" || view["id"] != "42" {
		t.Fatalf("drain view = %#v", view)
	}

	recorder = httptest.NewRecorder()
	request = actorRequest(http.MethodPost, "/api/admin/instances/42/activate", "admin")
	request.SetPathValue("id", "42")
	handler.ActivateInstance(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("activate status = %d", recorder.Code)
	}
	if service.activateID != 42 || service.transition != orchestration.StatusActive {
		t.Fatalf("activate call = %d/%q", service.activateID, service.transition)
	}
}

func TestTransitionsRejectInvalidIDAndMissingAdmin(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "abc"} {
		service := &fakeService{}
		handler := New(service)
		recorder := httptest.NewRecorder()
		request := actorRequest(http.MethodPost, "/api/admin/instances/x/drain", "admin")
		request.SetPathValue("id", raw)
		handler.DrainInstance(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("id %q: status = %d, want 400", raw, recorder.Code)
		}
		if service.transitionCalls != 0 {
			t.Fatalf("id %q reached the service", raw)
		}
	}

	service := &fakeService{}
	handler := New(service)
	recorder := httptest.NewRecorder()
	request := actorRequest(http.MethodPost, "/api/admin/instances/42/drain", "user")
	request.SetPathValue("id", "42")
	handler.DrainInstance(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", recorder.Code)
	}
}

func TestTransitionErrorsAreEnveloped(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{platform.NotFound("instance"), http.StatusNotFound},
		{platform.StateConflict("already stopped"), http.StatusConflict},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		service := &fakeService{transitionErr: tc.err}
		handler := New(service)
		recorder := httptest.NewRecorder()
		request := actorRequest(http.MethodPost, "/api/admin/instances/42/drain", "admin")
		request.SetPathValue("id", "42")
		handler.DrainInstance(recorder, request)
		if recorder.Code != tc.want {
			t.Fatalf("error %v: status = %d, want %d", tc.err, recorder.Code, tc.want)
		}
	}
}

/* --------------------------------- readiness ------------------------------- */

func TestReadyReturns200WhenAllConditionsPass(t *testing.T) {
	service := &fakeService{readiness: orchestrationapp.Readiness{
		Database: true, Storage: true, InstanceID: 42,
		InflightRequests: 3, InflightWork: 1,
	}}
	handler := New(service)
	recorder := httptest.NewRecorder()
	handler.Ready(recorder, httptest.NewRequest(http.MethodGet, "/api/health/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	data := decodeData(t, recorder)
	if data["status"] != "ok" || data["database"] != "ok" || data["storage"] != "ok" ||
		data["draining"] != false || data["instance_id"] != "42" ||
		data["inflight_requests"] != float64(3) || data["inflight_work"] != float64(1) {
		t.Fatalf("ready body = %#v", data)
	}
}

func TestReadyReturns503ForEachFailedCondition(t *testing.T) {
	cases := []struct {
		name      string
		readiness orchestrationapp.Readiness
		field     string
	}{
		{name: "database", readiness: orchestrationapp.Readiness{Storage: true, InstanceID: 42}, field: "database"},
		{name: "storage", readiness: orchestrationapp.Readiness{Database: true, InstanceID: 42}, field: "storage"},
		{name: "draining", readiness: orchestrationapp.Readiness{Database: true, Storage: true, Draining: true, InstanceID: 42}, field: "draining"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(&fakeService{readiness: tc.readiness})
			recorder := httptest.NewRecorder()
			handler.Ready(recorder, httptest.NewRequest(http.MethodGet, "/api/health/ready", nil))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", recorder.Code)
			}
			data := decodeData(t, recorder)
			if data["status"] != "degraded" {
				t.Fatalf("status field = %#v", data["status"])
			}
			switch tc.field {
			case "database":
				if data["database"] != "down" {
					t.Fatalf("database = %#v", data["database"])
				}
			case "storage":
				if data["storage"] != "down" {
					t.Fatalf("storage = %#v", data["storage"])
				}
			case "draining":
				if data["draining"] != true {
					t.Fatalf("draining = %#v", data["draining"])
				}
			}
		})
	}
}

func TestReadinessWithoutServiceFailsClosed(t *testing.T) {
	handler := New(nil)
	recorder := httptest.NewRecorder()
	handler.Ready(recorder, httptest.NewRequest(http.MethodGet, "/api/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

/* ---------------------------------- routes --------------------------------- */

func TestRegisterRoutesMountsAdminAndPublicEndpoints(t *testing.T) {
	service := &fakeService{
		instances: []orchestration.Instance{fixtureInstance(1, orchestration.StatusActive)},
		readiness: orchestrationapp.Readiness{Database: true, Storage: true, InstanceID: 42},
	}
	handler := New(service)
	mux := http.NewServeMux()

	withAdmin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := platform.WithActor(r.Context(), platform.Actor{ID: 9, Role: "admin"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	authenticated := func(next http.Handler) http.Handler { return next }
	RegisterRoutes(mux, authenticated, withAdmin, handler)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/instances", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET instances = %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/admin/instances/42/drain", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST drain = %d", recorder.Code)
	}

	// The readiness probe is public: no middleware and no actor.
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET ready = %d", recorder.Code)
	}
}
