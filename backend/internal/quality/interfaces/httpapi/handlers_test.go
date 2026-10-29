package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	qualityhttp "github.com/EdouardRichard/EzTest/backend/internal/quality/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
)

var handlerTestTime = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// fakeEvalRunService records the forwarded calls and serves canned results.
type fakeEvalRunService struct {
	run     domain.Run
	runs    []domain.Run
	err     error
	record  []quality.RecordInput
	listLim []int
	getIDs  []int64
}

func (f *fakeEvalRunService) Record(_ context.Context, input quality.RecordInput) (domain.Run, error) {
	f.record = append(f.record, input)
	return f.run, f.err
}

func (f *fakeEvalRunService) Get(_ context.Context, id int64) (domain.Run, error) {
	f.getIDs = append(f.getIDs, id)
	return f.run, f.err
}

func (f *fakeEvalRunService) List(_ context.Context, limit int) ([]domain.Run, error) {
	f.listLim = append(f.listLim, limit)
	return f.runs, f.err
}

// tokenAuth is the minimal identity Authenticator used by the auth middleware.
type tokenAuth map[string]identity.User

func (a tokenAuth) Authenticate(_ context.Context, token string) (identity.User, error) {
	user, ok := a[token]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

func (tokenAuth) Login(context.Context, string, string) (identityapp.LoginResult, error) {
	return identityapp.LoginResult{}, errors.New("login is not used by the quality tests")
}

func (tokenAuth) Logout(context.Context, string) error { return nil }

// newServer mounts the quality handlers behind the real bearer middleware and a
// RequireAdmin guard, exactly like server.New does for the /api routes.
func newServer(t *testing.T, service *fakeEvalRunService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	handler := qualityhttp.NewHandlers(service)
	if handler == nil {
		t.Fatal("NewHandlers returned nil for a wired service")
	}
	handler.RegisterRoutes(mux, identityhttp.RequireAdmin)
	return identityhttp.AuthMiddleware(tokenAuth{
		"ezt_admin": {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
		"ezt_user":  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	})(mux)
}

func doRequest(t *testing.T, handler http.Handler, method, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type runEnvelope struct {
	Data struct {
		ID            string `json:"id"`
		Mode          string `json:"mode"`
		CorpusVersion string `json:"corpusVersion"`
		Model         string `json:"model"`
		Status        string `json:"status"`
		Summary       struct {
			Scenario string `json:"scenario"`
		} `json:"summary"`
		Metrics []struct {
			Name      string         `json:"name"`
			Value     float64        `json:"value"`
			Threshold float64        `json:"threshold"`
			Passed    bool           `json:"passed"`
			Detail    map[string]any `json:"detail"`
		} `json:"metrics"`
		StartedAt  string  `json:"startedAt"`
		FinishedAt *string `json:"finishedAt"`
	} `json:"data"`
}

type errorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// sampleRun is a persisted deterministic run with two metrics.
func sampleRun() domain.Run {
	finished := handlerTestTime.Add(90 * time.Second)
	return domain.Run{
		ID:            1844674407370955161,
		Mode:          domain.ModeDeterministic,
		CorpusVersion: "2026-10-29.1",
		Model:         "fake-llm",
		Status:        domain.StatusFailed,
		Summary:       map[string]any{"scenario": "deterministic"},
		StartedAt:     handlerTestTime,
		FinishedAt:    &finished,
		Metrics: []domain.Metric{{
			Name:      domain.MetricDesignCoverage,
			Value:     0.8,
			Threshold: 0.9,
			Passed:    false,
			Detail: map[string]any{
				"numerator": 4, "denominator": 5, "reason": domain.MetricReasonBelowThreshold,
			},
		}, {
			Name:      domain.MetricClassificationAccuracy,
			Value:     1,
			Threshold: 1,
			Passed:    true,
			Detail:    map[string]any{"numerator": 2, "denominator": 2},
		}},
	}
}

// TestRecordRunRendersTheAdminContract pins POST /api/quality/eval-runs: the
// request fields are forwarded to the service and the 201 body carries decimal
// string ids, camelCase fields, metric thresholds/verdicts and the detail map.
func TestRecordRunRendersTheAdminContract(t *testing.T) {
	service := &fakeEvalRunService{run: sampleRun()}
	body := `{
		"mode": "deterministic",
		"corpusVersion": "2026-10-29.1",
		"model": "fake-llm",
		"baseline": {"design_coverage": 0.90},
		"summary": {"scenario": "deterministic"},
		"startedAt": "2026-10-29T12:00:00Z",
		"finishedAt": "2026-10-29T12:01:30Z",
		"sample": {
			"confirmedRequirements": 5, "coveredRequirements": 4,
			"impactExpected": 4, "impactHit": 3, "impactSelected": 4,
			"injectedFailures": 2, "correctClassifications": 2,
			"aiIssues": 2, "truePositiveIssues": 2,
			"supplementCases": 2, "supplementHits": 1
		}
	}`
	recorder := doRequest(t, newServer(t, service), http.MethodPost, "/api/quality/eval-runs", "ezt_admin", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201", recorder.Code, recorder.Body)
	}
	if len(service.record) != 1 {
		t.Fatalf("service calls = %d, want 1", len(service.record))
	}
	input := service.record[0]
	if input.Mode != domain.ModeDeterministic || input.CorpusVersion != "2026-10-29.1" || input.Model != "fake-llm" {
		t.Fatalf("forwarded input = %+v", input)
	}
	if input.Sample == nil || input.Sample.ConfirmedRequirements != 5 || input.Sample.SupplementHits != 1 {
		t.Fatalf("forwarded sample = %+v", input.Sample)
	}
	if input.Baseline[domain.MetricDesignCoverage] != 0.90 {
		t.Fatalf("forwarded baseline = %+v", input.Baseline)
	}
	if got := input.StartedAt.UTC(); !got.Equal(handlerTestTime) {
		t.Errorf("forwarded startedAt = %v, want %v", got, handlerTestTime)
	}
	if input.FinishedAt.UTC().Sub(input.StartedAt.UTC()) != 90*time.Second {
		t.Errorf("forwarded finishedAt = %v", input.FinishedAt)
	}

	var envelope runEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body)
	}
	data := envelope.Data
	if data.ID != "1844674407370955161" {
		t.Errorf("id = %q, want the decimal string", data.ID)
	}
	if data.Mode != "deterministic" || data.Status != "failed" || data.CorpusVersion != "2026-10-29.1" ||
		data.Model != "fake-llm" || data.Summary.Scenario != "deterministic" {
		t.Errorf("run = %+v", data)
	}
	if len(data.Metrics) != 2 {
		t.Fatalf("metrics = %d, want 2", len(data.Metrics))
	}
	design := data.Metrics[0]
	if design.Name != domain.MetricDesignCoverage || design.Value != 0.8 || design.Threshold != 0.9 || design.Passed {
		t.Errorf("design metric = %+v", design)
	}
	if design.Detail["reason"] != domain.MetricReasonBelowThreshold {
		t.Errorf("design detail = %+v", design.Detail)
	}
	if data.StartedAt == "" || data.FinishedAt == nil {
		t.Errorf("times = %q/%v, want both rendered", data.StartedAt, data.FinishedAt)
	}
}

// TestRecordRejectsMalformedBodyAndValidationErrors pins the shared 400 envelope
// for a typo and for an application validation verdict.
func TestRecordRejectsMalformedBodyAndValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		err     error
		field   string
		service *fakeEvalRunService
	}{
		{
			name: "unknown field", body: `{"mode":"deterministic","corpusVersion":"c","sample":{},"bogus":1}`,
			field: "body", service: &fakeEvalRunService{},
		},
		{
			name: "invalid json", body: `{"mode":`, field: "body", service: &fakeEvalRunService{},
		},
		{
			name: "trailing object", body: `{"mode":"deterministic","corpusVersion":"c","sample":{}}{}`,
			field: "body", service: &fakeEvalRunService{},
		},
		{
			name: "service validation", body: `{"mode":"manual","corpusVersion":"c","sample":{}}`,
			field: "mode", service: &fakeEvalRunService{err: &quality.ValidationError{Field: "mode", Message: "mode must be deterministic or real_llm"}},
		},
	}
	for _, tc := range cases {
		recorder := doRequest(t, newServer(t, tc.service), http.MethodPost, "/api/quality/eval-runs", "ezt_admin", tc.body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s, want 400", tc.name, recorder.Code, recorder.Body)
		}
		var envelope errorEnvelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s: decode: %v body=%s", tc.name, err, recorder.Body)
		}
		if envelope.Error.Code != "invalid_argument" {
			t.Errorf("%s: code = %q, want invalid_argument", tc.name, envelope.Error.Code)
		}
		if envelope.Error.Details["field"] != tc.field {
			t.Errorf("%s: details = %+v, want field=%s", tc.name, envelope.Error.Details, tc.field)
		}
	}
}

// TestListRunsRendersHistoryAndValidatesLimit pins GET /api/quality/eval-runs.
func TestListRunsRendersHistoryAndValidatesLimit(t *testing.T) {
	service := &fakeEvalRunService{runs: []domain.Run{sampleRun()}}
	server := newServer(t, service)

	recorder := doRequest(t, server, http.MethodGet, "/api/quality/eval-runs", "ezt_admin", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if len(service.listLim) != 1 || service.listLim[0] != quality.DefaultListLimit {
		t.Errorf("forwarded limits = %v, want the default %d", service.listLim, quality.DefaultListLimit)
	}
	var list struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "1844674407370955161" || list.Data[0].Status != "failed" {
		t.Fatalf("list = %+v", list.Data)
	}

	recorder = doRequest(t, server, http.MethodGet, "/api/quality/eval-runs?limit=7", "ezt_admin", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("limit 7 status = %d body=%s", recorder.Code, recorder.Body)
	}
	if service.listLim[len(service.listLim)-1] != 7 {
		t.Errorf("forwarded limit = %d, want 7", service.listLim[len(service.listLim)-1])
	}

	for _, target := range []string{
		"/api/quality/eval-runs?limit=0",
		"/api/quality/eval-runs?limit=51",
		"/api/quality/eval-runs?limit=abc",
	} {
		recorder = doRequest(t, server, http.MethodGet, target, "ezt_admin", "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s, want 400", target, recorder.Code, recorder.Body)
		}
	}
}

// TestGetRunRendersDetailAndNotFound pins GET /api/quality/eval-runs/{id}: the
// decimal path id is forwarded and an unknown run is the shared 404.
func TestGetRunRendersDetailAndNotFound(t *testing.T) {
	service := &fakeEvalRunService{run: sampleRun()}
	recorder := doRequest(t, newServer(t, service), http.MethodGet, "/api/quality/eval-runs/42", "ezt_admin", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if len(service.getIDs) != 1 || service.getIDs[0] != 42 {
		t.Fatalf("forwarded ids = %v, want [42]", service.getIDs)
	}

	for _, target := range []string{
		"/api/quality/eval-runs/not-a-number",
		"/api/quality/eval-runs/0",
		"/api/quality/eval-runs/-3",
	} {
		before := len(service.getIDs)
		recorder = doRequest(t, newServer(t, service), http.MethodGet, target, "ezt_admin", "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s, want 400", target, recorder.Code, recorder.Body)
		}
		if len(service.getIDs) != before {
			t.Fatalf("%s: reached the service with a malformed id", target)
		}
	}

	notFound := &fakeEvalRunService{err: quality.ErrRunNotFound}
	recorder = doRequest(t, newServer(t, notFound), http.MethodGet, "/api/quality/eval-runs/77", "ezt_admin", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown run status = %d body=%s, want 404", recorder.Code, recorder.Body)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", envelope.Error.Code)
	}
}

// TestQualityRoutesAreAdminOnly proves the routes sit behind the bearer
// middleware and the RequireAdmin guard: anonymous is 401, a normal user is 403
// and neither reaches the service (spec §11.5: evaluation is admin-only).
func TestQualityRoutesAreAdminOnly(t *testing.T) {
	service := &fakeEvalRunService{run: sampleRun()}
	handler := newServer(t, service)
	requests := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, "/api/quality/eval-runs", ""},
		{http.MethodGet, "/api/quality/eval-runs/1", ""},
		{http.MethodPost, "/api/quality/eval-runs", `{"mode":"deterministic","corpusVersion":"c","sample":{}}`},
	}
	for _, request := range requests {
		recorder := doRequest(t, handler, request.method, request.target, "", request.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous status = %d, want 401", request.method, request.target, recorder.Code)
		}
		recorder = doRequest(t, handler, request.method, request.target, "ezt_user", request.body)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s %s normal-user status = %d body=%s, want 403",
				request.method, request.target, recorder.Code, recorder.Body)
		}
	}
	if len(service.record)+len(service.getIDs)+len(service.listLim) != 0 {
		t.Errorf("a rejected request reached the service: %+v", service)
	}
}

// TestServerMountsQualityRoutes proves the composition root's
// Dependencies.Quality wiring mounts the admin-only group behind the global auth.
func TestServerMountsQualityRoutes(t *testing.T) {
	service := &fakeEvalRunService{runs: []domain.Run{sampleRun()}}
	handler := server.New(
		config.Config{Env: "test"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		server.Dependencies{
			Auth: tokenAuth{
				"ezt_admin": {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
				"ezt_user":  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
			},
			Quality: qualityhttp.NewHandlers(service),
		},
	)

	anonymous := doRequest(t, handler, http.MethodGet, "/api/quality/eval-runs", "", "")
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d body=%s, want 401", anonymous.Code, anonymous.Body)
	}
	user := doRequest(t, handler, http.MethodGet, "/api/quality/eval-runs", "ezt_user", "")
	if user.Code != http.StatusForbidden {
		t.Fatalf("normal-user status = %d body=%s, want 403", user.Code, user.Body)
	}
	admin := doRequest(t, handler, http.MethodGet, "/api/quality/eval-runs", "ezt_admin", "")
	if admin.Code != http.StatusOK {
		t.Fatalf("admin status = %d body=%s, want 200", admin.Code, admin.Body)
	}
	if len(service.listLim) != 1 {
		t.Fatalf("service calls = %d, want 1 (anonymous and normal user must not reach it)", len(service.listLim))
	}
}

// TestNewHandlersNil proves an unwired process mounts no quality route.
func TestNewHandlersNil(t *testing.T) {
	if qualityhttp.NewHandlers(nil) != nil {
		t.Fatal("NewHandlers(nil) must return nil")
	}
}
