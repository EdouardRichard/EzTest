package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const internalTestToken = "executor-internal-secret"

type internalRepo struct {
	mu     sync.Mutex
	rows   map[int64]execution.ExecutorInstance
	nextID int64
	upsert int
}

func newInternalRepo() *internalRepo {
	return &internalRepo{rows: map[int64]execution.ExecutorInstance{}}
}

func (r *internalRepo) UpsertHeartbeat(_ context.Context, instance execution.ExecutorInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upsert++
	if instance.ID == 0 {
		r.nextID++
		instance.ID = r.nextID
	}
	for id, existing := range r.rows {
		if existing.Endpoint == instance.Endpoint && id != instance.ID {
			delete(r.rows, id)
		}
	}
	r.rows[instance.ID] = instance
	return nil
}

func (r *internalRepo) GetByEndpoint(_ context.Context, endpoint string) (execution.ExecutorInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.Endpoint == endpoint {
			return row, nil
		}
	}
	return execution.ExecutorInstance{}, execution.ErrNotFound
}

func (r *internalRepo) Get(_ context.Context, id int64) (execution.ExecutorInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	if !ok {
		return execution.ExecutorInstance{}, execution.ErrNotFound
	}
	return row, nil
}

func (r *internalRepo) List(context.Context) ([]execution.ExecutorInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := make([]execution.ExecutorInstance, 0, len(r.rows))
	for _, row := range r.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (r *internalRepo) SetStatus(_ context.Context, id int64, status execution.ExecutorStatus) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	if !ok {
		return false, nil
	}
	row.Status = status
	r.rows[id] = row
	return true, nil
}

func (r *internalRepo) byEndpoint(t *testing.T, endpoint string) execution.ExecutorInstance {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.Endpoint == endpoint {
			return row
		}
	}
	t.Fatalf("endpoint %q is not registered", endpoint)
	return execution.ExecutorInstance{}
}

type internalSettings struct {
	token string
}

func (s *internalSettings) String(_ context.Context, key string) (string, error) {
	switch key {
	case executionapp.SettingExecutorToken:
		return s.token, nil
	case executionapp.SettingExecutorURL:
		return "http://127.0.0.1:13336", nil
	case executionapp.SettingExecutorURLs:
		return "", nil
	default:
		return "", errors.New("setting not found: " + key)
	}
}

func (s *internalSettings) Int(_ context.Context, key string) (int64, error) {
	switch key {
	case executionapp.SettingExecutorOfflineSeconds:
		return 30, nil
	default:
		return 0, errors.New("setting not found: " + key)
	}
}

func (s *internalSettings) Bool(context.Context, string) (bool, error) { return false, nil }

type internalClock struct{ now time.Time }

func (c *internalClock) Now() time.Time { return c.now }

type internalIDs struct{ next int64 }

func (g *internalIDs) Next() int64 { g.next++; return g.next }

type internalFixture struct {
	mux      *http.ServeMux
	repo     *internalRepo
	registry *executionapp.Registry
	clock    *internalClock
	token    string
}

func newInternalFixture(t *testing.T) *internalFixture {
	t.Helper()
	repo := newInternalRepo()
	settings := &internalSettings{token: internalTestToken}
	clock := &internalClock{now: time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)}
	registry := executionapp.NewRegistry(executionapp.RegistryDeps{
		Repo:     repo,
		Settings: settings,
		Clock:    clock,
		IDs:      &internalIDs{},
	})
	handler := New(Deps{Executors: repo, Settings: settings, Registry: registry})
	mux := http.NewServeMux()
	RegisterInternalRoutes(mux, InternalTokenAuth(settings), handler)
	return &internalFixture{mux: mux, repo: repo, registry: registry, clock: clock, token: internalTestToken}
}

func (f *internalFixture) send(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, request)
	return recorder
}

func decodeData[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return envelope.Data
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestInternalRegisterReturnsInstanceView(t *testing.T) {
	f := newInternalFixture(t)
	recorder := f.send(t, http.MethodPost, "/api/internal/executors/register", f.token,
		`{"endpoint":"http://127.0.0.1:14000/","name":"executor-push","capacity":6,"active_contexts":2,"version":"0.2.0-p7"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	view := decodeData[InstanceView](t, recorder)
	if view.InstanceID == "" || view.Endpoint != "http://127.0.0.1:14000" || view.Name != "executor-push" {
		t.Fatalf("view = %+v", view)
	}
	if view.Status != "active" || view.Capacity != 6 || view.ActiveContexts != 2 || view.Version != "0.2.0-p7" {
		t.Fatalf("view = %+v", view)
	}
	if view.LastHeartbeat == nil {
		t.Fatal("last_heartbeat is missing")
	}
	if _, err := time.Parse(time.RFC3339, *view.LastHeartbeat); err != nil {
		t.Fatalf("last_heartbeat = %q: %v", *view.LastHeartbeat, err)
	}
	row := f.repo.byEndpoint(t, "http://127.0.0.1:14000")
	if row.TokenHash != tokenHash(f.token) {
		t.Fatalf("token hash = %q", row.TokenHash)
	}
	if row.LastHeartbeat == nil || !row.LastHeartbeat.Equal(f.clock.now) {
		t.Fatalf("stored last heartbeat = %v", row.LastHeartbeat)
	}
}

func TestInternalRegisterAcceptsMaxContextsAlias(t *testing.T) {
	f := newInternalFixture(t)
	recorder := f.send(t, http.MethodPost, "/api/internal/executors/register", f.token,
		`{"endpoint":"http://127.0.0.1:14000","name":"executor-push","max_contexts":9,"version":"0.2.0-p7"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if row := f.repo.byEndpoint(t, "http://127.0.0.1:14000"); row.Capacity != 9 {
		t.Fatalf("capacity = %d, want 9 from max_contexts alias", row.Capacity)
	}
	// Conflicting alias values are rejected.
	recorder = f.send(t, http.MethodPost, "/api/internal/executors/register", f.token,
		`{"endpoint":"http://127.0.0.1:14001","name":"executor-push","capacity":4,"max_contexts":9,"version":"0.2.0-p7"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("conflicting alias status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	assertErrorCode(t, recorder, "invalid_argument")
}

func TestInternalRoutesRequireExecutorToken(t *testing.T) {
	f := newInternalFixture(t)
	cases := []struct{ name, token string }{
		{"missing", ""},
		{"wrong", "not-the-token"},
		{"user token", "ezt_session"},
	}
	for _, tc := range cases {
		recorder := f.send(t, http.MethodPost, "/api/internal/executors/register", tc.token,
			`{"endpoint":"http://127.0.0.1:14000","name":"executor-push","capacity":4,"version":"p7"}`)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d body = %s", tc.name, recorder.Code, recorder.Body.String())
		}
		assertErrorCode(t, recorder, "unauthorized")
		if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Fatalf("%s WWW-Authenticate = %q", tc.name, got)
		}
	}
	if got := len(f.repo.rows); got != 0 {
		t.Fatalf("unauthorized register created %d rows", got)
	}
}

func TestInternalHeartbeatUnknownEndpointIs404(t *testing.T) {
	f := newInternalFixture(t)
	recorder := f.send(t, http.MethodPost, "/api/internal/executors/heartbeat", f.token,
		`{"endpoint":"http://127.0.0.1:14999","active_contexts":1}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	assertErrorCode(t, recorder, "executor_unknown")
}

func TestInternalHeartbeatUpdatesCapacityAndPreservesDraining(t *testing.T) {
	f := newInternalFixture(t)
	recorder := f.send(t, http.MethodPost, "/api/internal/executors/register", f.token,
		`{"endpoint":"http://127.0.0.1:14000","name":"executor-push","capacity":6,"version":"p7"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("register status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	registered := decodeData[InstanceView](t, recorder)

	f.clock.now = f.clock.now.Add(10 * time.Second)
	recorder = f.send(t, http.MethodPost, "/api/internal/executors/heartbeat", f.token,
		`{"endpoint":"http://127.0.0.1:14000/","active_contexts":4,"capacity":10,"version":"p7.1","status":"degraded"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	view := decodeData[InstanceView](t, recorder)
	if view.InstanceID != registered.InstanceID {
		t.Fatalf("instance id = %q, want %q", view.InstanceID, registered.InstanceID)
	}
	if view.Status != "active" || view.Capacity != 10 || view.ActiveContexts != 4 || view.Version != "p7.1" {
		t.Fatalf("view = %+v", view)
	}
	row := f.repo.byEndpoint(t, "http://127.0.0.1:14000")
	if row.LastError != "degraded" {
		t.Fatalf("last error = %q", row.LastError)
	}

	instanceID, err := strconv.ParseInt(registered.InstanceID, 10, 64)
	if err != nil {
		t.Fatalf("instance id: %v", err)
	}
	if ok, err := f.registry.Drain(context.Background(), instanceID); err != nil || !ok {
		t.Fatalf("Drain = %v, %v", ok, err)
	}
	recorder = f.send(t, http.MethodPost, "/api/internal/executors/heartbeat", f.token,
		`{"endpoint":"http://127.0.0.1:14000","active_contexts":0,"status":"active"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("draining heartbeat status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if view := decodeData[InstanceView](t, recorder); view.Status != "draining" {
		t.Fatalf("status = %q, want draining", view.Status)
	}
	row = f.repo.byEndpoint(t, "http://127.0.0.1:14000")
	if row.Status != execution.ExecutorDraining || row.LastError != "" || row.Capacity != 10 {
		t.Fatalf("row = %+v", row)
	}
}

func TestInternalRegisterPreservesDraining(t *testing.T) {
	f := newInternalFixture(t)
	recorder := f.send(t, http.MethodPost, "/api/internal/executors/register", f.token,
		`{"endpoint":"http://127.0.0.1:14000","name":"executor-push","capacity":6,"version":"p7"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("register status = %d", recorder.Code)
	}
	view := decodeData[InstanceView](t, recorder)
	instanceID, _ := strconv.ParseInt(view.InstanceID, 10, 64)
	if _, err := f.registry.Drain(context.Background(), instanceID); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	recorder = f.send(t, http.MethodPost, "/api/internal/executors/register", f.token,
		`{"endpoint":"http://127.0.0.1:14000","name":"executor-push","capacity":8,"version":"p7"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("second register status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if got := decodeData[InstanceView](t, recorder).Status; got != "draining" {
		t.Fatalf("status = %q, want draining", got)
	}
}

func TestInternalPushValidation(t *testing.T) {
	f := newInternalFixture(t)
	cases := []struct{ name, body string }{
		{"malformed json", `{`},
		{"array body", `[]`},
		{"missing endpoint", `{"name":"executor","capacity":4,"version":"p7"}`},
		{"invalid endpoint", `{"endpoint":"ftp://host:1","name":"executor","capacity":4,"version":"p7"}`},
		{"missing name", `{"endpoint":"http://127.0.0.1:14000","capacity":4,"version":"p7"}`},
		{"zero capacity", `{"endpoint":"http://127.0.0.1:14000","name":"executor","capacity":0,"version":"p7"}`},
		{"missing version", `{"endpoint":"http://127.0.0.1:14000","name":"executor","capacity":4}`},
		{"unsupported status", `{"endpoint":"http://127.0.0.1:14000","name":"executor","capacity":4,"version":"p7","status":"offline"}`},
	}
	for _, tc := range cases {
		recorder := f.send(t, http.MethodPost, "/api/internal/executors/register", f.token, tc.body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body = %s", tc.name, recorder.Code, recorder.Body.String())
		}
		assertErrorCode(t, recorder, "invalid_argument")
	}
	// Heartbeat requires active_contexts and validates its status enum.
	for _, body := range []string{
		`{"endpoint":"http://127.0.0.1:14000"}`,
		`{"endpoint":"http://127.0.0.1:14000","active_contexts":-1}`,
		`{"endpoint":"http://127.0.0.1:14000","active_contexts":0,"status":"offline"}`,
	} {
		recorder := f.send(t, http.MethodPost, "/api/internal/executors/heartbeat", f.token, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("heartbeat %s: status = %d body = %s", body, recorder.Code, recorder.Body.String())
		}
		assertErrorCode(t, recorder, "invalid_argument")
	}
}

func TestInternalRoutesRejectUnexpectedMethods(t *testing.T) {
	f := newInternalFixture(t)
	recorder := f.send(t, http.MethodGet, "/api/internal/executors/register", f.token, "")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", recorder.Code)
	}
}

func assertErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", recorder.Body.String(), err)
	}
	if envelope.Error.Code != want {
		t.Fatalf("error code = %q, want %q (body %s)", envelope.Error.Code, want, recorder.Body.String())
	}
}
