package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	qualityapi "github.com/EdouardRichard/EzTest/backend/internal/quality/interfaces/httpapi"
)

var (
	handlerNow   = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	handlerStart = handlerNow.Add(-time.Minute)
)

type fakeService struct {
	listRuns  []qualityapp.EvalRun
	listErr   error
	listLimit int

	recordRun   qualityapp.EvalRun
	recordInput qualityapp.RecordInput
	recordActor platform.Actor
	recordErr   error

	getRun qualityapp.EvalRun
	getID  int64
	getErr error

	overview     qualityapp.Overview
	overviewErr  error
	overviewScope *int64
}

func (f *fakeService) List(_ context.Context, limit int) ([]qualityapp.EvalRun, error) {
	f.listLimit = limit
	return f.listRuns, f.listErr
}

func (f *fakeService) Record(_ context.Context, actor platform.Actor, input qualityapp.RecordInput) (qualityapp.EvalRun, error) {
	f.recordActor = actor
	f.recordInput = input
	return f.recordRun, f.recordErr
}

func (f *fakeService) Get(_ context.Context, id int64) (qualityapp.EvalRun, error) {
	f.getID = id
	return f.getRun, f.getErr
}

func (f *fakeService) Overview(_ context.Context, versionID *int64) (qualityapp.Overview, error) {
	f.overviewScope = versionID
	return f.overview, f.overviewErr
}

type handlerRepo struct {
	insertedRun     qualityapp.EvalRun
	insertedMetrics []quality.Metric
	insertCalls     int
	insertErr       error

	getRun qualityapp.EvalRun
	getErr error

	listRuns  []qualityapp.EvalRun
	listLimit int
	latest    *qualityapp.EvalRun
	latestErr error
}

func (r *handlerRepo) Insert(_ context.Context, run qualityapp.EvalRun, metrics []quality.Metric) error {
	r.insertCalls++
	r.insertedRun = run
	r.insertedMetrics = metrics
	return r.insertErr
}

func (r *handlerRepo) Get(context.Context, int64) (qualityapp.EvalRun, error) {
	return r.getRun, r.getErr
}

func (r *handlerRepo) List(_ context.Context, limit int) ([]qualityapp.EvalRun, error) {
	r.listLimit = limit
	return r.listRuns, nil
}

func (r *handlerRepo) Latest(context.Context) (*qualityapp.EvalRun, error) {
	return r.latest, r.latestErr
}

type handlerClock struct{}

func (handlerClock) Now() time.Time { return handlerNow }

type handlerIDs struct{ next int64 }

func (i *handlerIDs) Next() int64 {
	i.next++
	return 1000 + i.next
}

func newQualityMux(service qualityapi.Service, role string) *http.ServeMux {
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if role == "" {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 77, Role: role})))
		})
	}
	admin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if role == "" {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 88, Role: role})))
		})
	}
	qualityapi.New(service).Register(mux, authenticated, admin)
	return mux
}

func performQuality(t *testing.T, mux *http.ServeMux, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v (%s)", err, recorder.Body.String())
		}
	}
	return recorder, decoded
}

func fixtureMetric() quality.Metric {
	baseline := 0.75
	return quality.Metric{
		Name:      quality.MetricDesignCoverage,
		Value:     0.8,
		Threshold: 0.80,
		Passed:    true,
		Detail: quality.MetricDetail{
			Numerator:       8,
			Denominator:     10,
			ThresholdPassed: true,
			Baseline:        &baseline,
			Reason:          quality.ReasonOK,
		},
	}
}

func fixtureRun() qualityapp.EvalRun {
	finished := handlerNow
	return qualityapp.EvalRun{
		ID:            42,
		Mode:          quality.EvalModeRealLLM,
		CorpusVersion: "v02-2026-11-01.1",
		Model:         "deepseek-flash",
		Status:        quality.StatusPassed,
		Summary:       map[string]any{"version_id": "10"},
		StartedAt:     handlerStart,
		FinishedAt:    &finished,
		CreatedAt:     handlerNow,
		Metrics:       []quality.Metric{fixtureMetric()},
	}
}

func TestEvalEndpointsRequireAdmin(t *testing.T) {
	fake := &fakeService{listRuns: []qualityapp.EvalRun{}, recordRun: fixtureRun(), getRun: fixtureRun()}

	userMux := newQualityMux(fake, "user")
	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/quality/eval-runs", ""},
		{http.MethodPost, "/api/quality/eval-runs", "{}"},
		{http.MethodGet, "/api/quality/eval-runs/42", ""},
	} {
		recorder, body := performQuality(t, userMux, route.method, route.path, route.body)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d body=%s", route.method, route.path, recorder.Code, recorder.Body.String())
		}
		envelope := body["error"].(map[string]any)
		if envelope["code"] != "forbidden" {
			t.Fatalf("%s %s code = %v", route.method, route.path, envelope["code"])
		}
	}

	anonymousMux := newQualityMux(fake, "")
	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/quality/eval-runs", ""},
		{http.MethodPost, "/api/quality/eval-runs", "{}"},
		{http.MethodGet, "/api/quality/eval-runs/42", ""},
		{http.MethodGet, "/api/quality/overview", ""},
	} {
		recorder, _ := performQuality(t, anonymousMux, route.method, route.path, route.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", route.method, route.path, recorder.Code)
		}
	}
}

func TestOverviewIsAvailableToEveryLoggedInUser(t *testing.T) {
	fake := &fakeService{overview: qualityapp.Overview{
		GeneratedAt: handlerNow,
		Operational: quality.UnavailableOperationalMetrics(),
	}}
	mux := newQualityMux(fake, "user")
	recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/overview", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := body["data"].(map[string]any)
	if data["scope"].(map[string]any)["version_id"] != nil {
		t.Fatalf("scope = %v", data["scope"])
	}
	if count := len(data["operational"].([]any)); count != 7 {
		t.Fatalf("operational count = %d", count)
	}
	first := data["operational"].([]any)[0].(map[string]any)
	if first["available"] != false || first["value"] != nil {
		t.Fatalf("unavailable metric = %+v", first)
	}
	if first["definition"] == "" {
		t.Fatalf("definition = %+v", first)
	}
	if data["latest_eval"] != nil {
		t.Fatalf("latest_eval = %+v", data["latest_eval"])
	}
}

func TestOverviewAcceptsVersionFilterAndLatestEval(t *testing.T) {
	fake := &fakeService{overview: qualityapp.Overview{
		GeneratedAt:    handlerNow,
		Operational:    quality.UnavailableOperationalMetrics(),
		LatestEval:     &qualityapp.EvalSummary{ID: 9, Mode: quality.EvalModeRealLLM, Status: quality.StatusPassed, CreatedAt: handlerNow, MetricsCount: 7, MetricsPassed: 6},
		ScopeVersionID: func() *int64 { value := int64(10); return &value }(),
	}}
	mux := newQualityMux(fake, "user")
	recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/overview?version_id=10", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.overviewScope == nil || *fake.overviewScope != 10 {
		t.Fatalf("scope passed to service = %v", fake.overviewScope)
	}
	data := body["data"].(map[string]any)
	scope := data["scope"].(map[string]any)
	if scope["version_id"] != "10" {
		t.Fatalf("scope version_id = %v", scope["version_id"])
	}
	summary := data["latest_eval"].(map[string]any)
	if summary["id"] != "9" || summary["metrics_count"] != float64(7) || summary["metrics_passed"] != float64(6) {
		t.Fatalf("latest_eval = %+v", summary)
	}
	if summary["created_at"] != handlerNow.Format(time.RFC3339) {
		t.Fatalf("created_at = %v", summary["created_at"])
	}
}

func TestOverviewRejectsInvalidVersionFilter(t *testing.T) {
	mux := newQualityMux(&fakeService{overview: qualityapp.Overview{GeneratedAt: handlerNow}}, "user")
	for _, value := range []string{"abc", "0", "-1"} {
		recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/overview?version_id="+value, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("version_id=%s status = %d", value, recorder.Code)
		}
		field := body["error"].(map[string]any)["details"].(map[string]any)["field"]
		if field != "version_id" {
			t.Fatalf("version_id=%s field = %v", value, field)
		}
	}
}

func TestListEvalRunsEnvelopeAndLimit(t *testing.T) {
	fake := &fakeService{listRuns: []qualityapp.EvalRun{fixtureRun()}}
	mux := newQualityMux(fake, "admin")

	recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/eval-runs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.listLimit != qualityapp.DefaultListLimit {
		t.Fatalf("default limit = %d", fake.listLimit)
	}
	if body["next_cursor"] != "" || body["has_more"] != false {
		t.Fatalf("envelope = %+v", body)
	}
	runs := body["data"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["id"] != "42" {
		t.Fatalf("data = %+v", runs)
	}

	recorder, _ = performQuality(t, mux, http.MethodGet, "/api/quality/eval-runs?limit=3", "")
	if recorder.Code != http.StatusOK || fake.listLimit != 3 {
		t.Fatalf("limit=3 status=%d captured=%d", recorder.Code, fake.listLimit)
	}

	for _, value := range []string{"0", "51", "abc"} {
		recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/eval-runs?limit="+value, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s status = %d", value, recorder.Code)
		}
		field := body["error"].(map[string]any)["details"].(map[string]any)["field"]
		if field != "limit" {
			t.Fatalf("limit=%s field = %v", value, field)
		}
	}
}

func TestGetEvalRunRendersContractShape(t *testing.T) {
	fake := &fakeService{getRun: fixtureRun()}
	mux := newQualityMux(fake, "admin")
	recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/eval-runs/42", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := body["data"].(map[string]any)
	if data["id"] != "42" || data["mode"] != "real_llm" || data["model"] != "deepseek-flash" {
		t.Fatalf("run = %+v", data)
	}
	if data["status"] != "passed" || data["corpus_version"] != "v02-2026-11-01.1" {
		t.Fatalf("run = %+v", data)
	}
	if data["started_at"] != handlerStart.Format(time.RFC3339) || data["finished_at"] != handlerNow.Format(time.RFC3339) {
		t.Fatalf("times = %+v", data)
	}
	if data["created_at"] != handlerNow.Format(time.RFC3339) {
		t.Fatalf("created_at = %+v", data)
	}
	metrics := data["metrics"].([]any)
	metric := metrics[0].(map[string]any)
	if metric["name"] != quality.MetricDesignCoverage || metric["passed"] != true {
		t.Fatalf("metric = %+v", metric)
	}
	detail := metric["detail"].(map[string]any)
	if detail["numerator"] != float64(8) || detail["denominator"] != float64(10) ||
		detail["threshold_passed"] != true || detail["regressed"] != false || detail["reason"] != quality.ReasonOK {
		t.Fatalf("detail = %+v", detail)
	}
	if detail["baseline"] != 0.75 {
		t.Fatalf("baseline = %+v", detail["baseline"])
	}
}

func TestGetEvalRunMapsNotFound(t *testing.T) {
	repo := &handlerRepo{getErr: quality.ErrNotFound}
	service := qualityapp.NewService(repo, &handlerIDs{}, handlerClock{}, nil, nil, nil)
	mux := newQualityMux(service, "admin")
	recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/eval-runs/42", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	envelope := body["error"].(map[string]any)
	if envelope["code"] != "not_found" {
		t.Fatalf("code = %v", envelope["code"])
	}
	if envelope["details"].(map[string]any)["kind"] != "eval_run" {
		t.Fatalf("details = %v", envelope["details"])
	}
}

func TestCreateEvalRunUsesServerDerivedVerdict(t *testing.T) {
	repo := &handlerRepo{}
	service := qualityapp.NewService(repo, &handlerIDs{next: 500}, handlerClock{}, nil, nil, nil)
	mux := newQualityMux(service, "admin")

	body := `{
		"mode":"real_llm",
		"corpus_version":"v02-2026-11-01.1",
		"model":"deepseek-flash",
		"status":"passed",
		"verdict":true,
		"sample":{
			"confirmed_requirements":10,"covered_requirements":5,
			"impact_expected":10,"impact_hit":8,"impact_selected":10,
			"injected_failures":10,"correct_classifications":9,
			"ai_issues":10,"true_positive_issues":9,
			"supplement_cases":10,"supplement_hits":8
		},
		"baseline":{},
		"summary":{"version_id":"10"},
		"started_at":"2026-11-01T11:58:00Z",
		"finished_at":"2026-11-01T12:00:00Z"
	}`
	recorder, decoded := performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if repo.insertCalls != 1 {
		t.Fatalf("insert calls = %d", repo.insertCalls)
	}
	if repo.insertedRun.Status != quality.StatusFailed {
		t.Fatalf("persisted status = %q, want failed (client passed ignored)", repo.insertedRun.Status)
	}
	data := decoded["data"].(map[string]any)
	if data["status"] != quality.StatusFailed {
		t.Fatalf("response status = %v", data["status"])
	}
	if len(data["metrics"].([]any)) != 6 {
		t.Fatalf("metrics = %+v", data["metrics"])
	}
	if data["started_at"] != "2026-11-01T11:58:00Z" || data["finished_at"] != "2026-11-01T12:00:00Z" {
		t.Fatalf("times = %v/%v", data["started_at"], data["finished_at"])
	}
}

func TestCreateEvalRunRejectsInvalidSampleAndMode(t *testing.T) {
	repo := &handlerRepo{}
	service := qualityapp.NewService(repo, &handlerIDs{next: 500}, handlerClock{}, nil, nil, nil)
	mux := newQualityMux(service, "admin")

	invalid := `{"mode":"real_llm","corpus_version":"v1","sample":{"confirmed_requirements":1,"covered_requirements":2}}`
	recorder, body := performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs", invalid)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid sample status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	field := body["error"].(map[string]any)["details"].(map[string]any)["field"]
	if field != "covered_requirements" {
		t.Fatalf("invalid sample field = %v", field)
	}

	badMode := `{"mode":"deterministic","corpus_version":"v1"}`
	recorder, body = performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs", badMode)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d", recorder.Code)
	}
	field = body["error"].(map[string]any)["details"].(map[string]any)["field"]
	if field != "mode" {
		t.Fatalf("bad mode field = %v", field)
	}

	malformed := `{"mode":`
	recorder, _ = performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs", malformed)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d", recorder.Code)
	}
	if repo.insertCalls != 0 {
		t.Fatalf("invalid requests reached the repository")
	}
}

func TestCreateEvalRunErrorStatusAllowsNoSample(t *testing.T) {
	repo := &handlerRepo{}
	service := qualityapp.NewService(repo, &handlerIDs{next: 500}, handlerClock{}, nil, nil, nil)
	mux := newQualityMux(service, "admin")
	recorder, body := performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs",
		`{"mode":"real_llm","corpus_version":"v1","status":"error"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := body["data"].(map[string]any)
	if data["status"] != quality.StatusError {
		t.Fatalf("status = %v", data["status"])
	}
	if metrics, ok := data["metrics"].([]any); !ok || len(metrics) != 0 {
		t.Fatalf("metrics = %#v", data["metrics"])
	}
}

func TestGetEvalRunRejectsInvalidPathID(t *testing.T) {
	mux := newQualityMux(&fakeService{}, "admin")
	for _, value := range []string{"abc", "0", "-1"} {
		recorder, body := performQuality(t, mux, http.MethodGet, "/api/quality/eval-runs/"+value, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("id=%s status = %d", value, recorder.Code)
		}
		field := body["error"].(map[string]any)["details"].(map[string]any)["field"]
		if field != "id" {
			t.Fatalf("id=%s field = %v", value, field)
		}
	}
}
