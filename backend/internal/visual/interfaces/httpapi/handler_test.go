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
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
	visualapi "github.com/EdouardRichard/EzTest/backend/internal/visual/interfaces/httpapi"
)

type fakeConfig struct {
	assertion  visual.Assertion
	list       []visual.Assertion
	err        error
	gotInput   visual.ConfigInput
	gotActor   int64
	gotVersion int64
}

func (f *fakeConfig) Upsert(_ context.Context, versionID, caseAssertionID, actorID int64, in visual.ConfigInput) (visual.Assertion, error) {
	f.gotInput, f.gotActor, f.gotVersion = in, actorID, versionID
	if f.err != nil {
		return visual.Assertion{}, f.err
	}
	assertion := f.assertion
	assertion.VersionID = versionID
	assertion.CaseAssertionID = caseAssertionID
	return assertion, nil
}

func (f *fakeConfig) List(context.Context, int64, *int64, *int64) ([]visual.Assertion, error) {
	return f.list, f.err
}

func (f *fakeConfig) Get(context.Context, int64) (visual.Assertion, error) {
	return f.assertion, f.err
}

type fakeBaselines struct {
	active      visual.Baseline
	hasActive   bool
	history     []visual.Baseline
	approved    visual.Baseline
	err         error
	gotEvidence int64
}

func (f *fakeBaselines) Approve(_ context.Context, _, _, evidenceID, _ int64) (visual.Baseline, error) {
	f.gotEvidence = evidenceID
	return f.approved, f.err
}

func (f *fakeBaselines) List(context.Context, int64, int64) ([]visual.Baseline, error) {
	return f.history, f.err
}

func (f *fakeBaselines) Active(context.Context, int64) (visual.Baseline, bool, error) {
	return f.active, f.hasActive, f.err
}

type fakeDiffs struct {
	list    []visual.Diff
	hasMore bool
	detail  visual.Diff
	err     error
	filter  visual.DiffFilter
}

func (f *fakeDiffs) List(_ context.Context, _ int64, filter visual.DiffFilter) ([]visual.Diff, bool, error) {
	f.filter = filter
	return f.list, f.hasMore, f.err
}

func (f *fakeDiffs) Get(context.Context, int64, int64) (visual.Diff, error) {
	return f.detail, f.err
}

type fakeOverview struct {
	overview visualapp.Overview
	metrics  visualapp.Metrics
	err      error
}

func (f *fakeOverview) Version(context.Context, int64) (visualapp.Overview, error) {
	return f.overview, f.err
}

func (f *fakeOverview) Global(context.Context, *int64) (visualapp.Metrics, error) {
	return f.metrics, f.err
}

func fixtureAssertion() visual.Assertion {
	return visual.Assertion{
		ID: 6001, VersionID: 10, CaseVersionID: 41, CaseAssertionID: 9001,
		StepSeq: 1, AssertionSeq: 2, VersionSUTID: 7, Selector: "#chart",
		Region: json.RawMessage(`{}`), Threshold: 0.005,
		ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1,
		WaitPolicy: json.RawMessage(`{}`), ConfigRevision: 3, Status: visual.AssertionActive,
		CreatedAt: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC),
	}
}

func newHandler(config *fakeConfig, baselines *fakeBaselines, diffs *fakeDiffs, overview *fakeOverview) *visualapi.Handler {
	return visualapi.New(visualapi.Deps{
		Configs: config, Baselines: baselines, Diffs: diffs, Overview: overview,
	})
}

func newMux(handler *visualapi.Handler, role string) *http.ServeMux {
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 77, Role: role})))
		})
	}
	admin := authenticated
	if role == "admin" {
		admin = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 88, Role: "admin"})))
			})
		}
	}
	handler.Register(mux, authenticated, admin)
	return mux
}

func perform(t *testing.T, mux *http.ServeMux, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
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

func TestListAssertionsRendersStringsAndActiveBaseline(t *testing.T) {
	config := &fakeConfig{list: []visual.Assertion{fixtureAssertion()}}
	baselines := &fakeBaselines{
		active: visual.Baseline{
			ID: 5001, VisualAssertionID: 6001, SHA256: strings.Repeat("a", 64),
			Width: 1280, Height: 720, ConfigRevision: 3,
			EnvironmentFingerprint: strings.Repeat("b", 64),
			ApprovedAt:             time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC),
		},
		hasActive: true,
	}
	mux := newMux(newHandler(config, baselines, &fakeDiffs{}, &fakeOverview{}), "user")
	recorder, body := perform(t, mux, http.MethodGet, "/api/versions/10/visual/assertions?caseVersionId=41", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := body["data"].([]any)
	first := data[0].(map[string]any)
	if first["id"] != "6001" || first["version_id"] != "10" || first["case_assertion_id"] != "9001" {
		t.Fatalf("view = %+v", first)
	}
	baseline := first["baseline"].(map[string]any)
	if baseline["id"] != "5001" {
		t.Fatalf("baseline = %+v", baseline)
	}
	serialized, _ := json.Marshal(body)
	for _, forbidden := range []string{"content_ref", "base64", "filesystem", "C:\\\\"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("view leaks %q: %s", forbidden, serialized)
		}
	}
}

func TestUpsertAssertionParsesBodyAndActor(t *testing.T) {
	config := &fakeConfig{assertion: fixtureAssertion()}
	mux := newMux(newHandler(config, &fakeBaselines{}, &fakeDiffs{}, &fakeOverview{}), "user")
	body := `{"versionSutId":"7","selector":"#chart","region":{"x":0.1,"y":0.2,"width":0.5,"height":0.3},"threshold":0.02,"viewport":{"width":800,"height":600},"deviceScaleFactor":2}`
	recorder, decoded := perform(t, mux, http.MethodPut, "/api/versions/10/visual/assertions/9001", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if config.gotActor != 77 || config.gotVersion != 10 {
		t.Fatalf("service got actor=%d version=%d", config.gotActor, config.gotVersion)
	}
	if config.gotInput.Threshold == nil || *config.gotInput.Threshold != 0.02 {
		t.Fatalf("input threshold = %v", config.gotInput.Threshold)
	}
	if config.gotInput.Viewport == nil || config.gotInput.Viewport.Width != 800 {
		t.Fatalf("input viewport = %+v", config.gotInput.Viewport)
	}
	if config.gotInput.VersionSUTID == nil || *config.gotInput.VersionSUTID != 7 {
		t.Fatalf("input version_sut = %v", config.gotInput.VersionSUTID)
	}
	if decoded["data"].(map[string]any)["config_revision"].(float64) != 3 {
		t.Fatalf("response = %+v", decoded)
	}
}

func TestUpsertAssertionRejectsInvalidBody(t *testing.T) {
	config := &fakeConfig{assertion: fixtureAssertion()}
	mux := newMux(newHandler(config, &fakeBaselines{}, &fakeDiffs{}, &fakeOverview{}), "user")
	for _, body := range []string{`{"versionSutId":"abc"}`, `{"unknown":true}`, `not-json`} {
		recorder, decoded := perform(t, mux, http.MethodPut, "/api/versions/10/visual/assertions/9001", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d", body, recorder.Code)
		}
		if decoded["error"].(map[string]any)["code"] != "invalid_argument" {
			t.Fatalf("error = %+v", decoded)
		}
	}
}

func TestApproveBaselineIsAdminOnly(t *testing.T) {
	baselines := &fakeBaselines{approved: visual.Baseline{
		ID: 5001, VisualAssertionID: 6001, EvidenceID: 7001,
		SHA256: strings.Repeat("a", 64), SizeBytes: 10, Width: 1280, Height: 720,
		ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1, ConfigRevision: 3,
		EnvironmentFingerprint: strings.Repeat("b", 64),
		Status:                 visual.BaselineApproved, ApprovedBy: 88,
		ApprovedAt: time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),
		CreatedAt:  time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),
	}}
	userMux := newMux(newHandler(&fakeConfig{}, baselines, &fakeDiffs{}, &fakeOverview{}), "user")
	recorder, _ := perform(t, userMux, http.MethodPost, "/api/versions/10/visual/assertions/6001/baselines", `{"evidenceId":"7001"}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", recorder.Code)
	}

	adminMux := newMux(newHandler(&fakeConfig{}, baselines, &fakeDiffs{}, &fakeOverview{}), "admin")
	recorder, decoded := perform(t, adminMux, http.MethodPost, "/api/versions/10/visual/assertions/6001/baselines", `{"evidenceId":"7001"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("admin status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if baselines.gotEvidence != 7001 {
		t.Fatalf("evidence id = %d", baselines.gotEvidence)
	}
	if decoded["data"].(map[string]any)["evidence_id"] != "7001" {
		t.Fatalf("response = %+v", decoded)
	}
}

func TestListDiffsPassesFiltersAndCursor(t *testing.T) {
	diffs := &fakeDiffs{
		list: []visual.Diff{{
			ID: 8001, VisualAssertionID: 6001, CaseRunID: 55, AssertionResultID: 66,
			ActualEvidenceID: 77, DiffRatio: 0.02, Threshold: 0.005,
			Classification: visual.ClassificationFailed, Reason: "visual_mismatch",
			EnvironmentMatch: true, Width: 10, Height: 10,
			CreatedAt: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC),
		}},
		hasMore: true,
	}
	mux := newMux(newHandler(&fakeConfig{}, &fakeBaselines{}, diffs, &fakeOverview{}), "user")
	recorder, decoded := perform(t, mux, http.MethodGet,
		"/api/versions/10/visual/diffs?caseRunId=55&classification=failed&limit=1&cursor=9000", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if diffs.filter.Classification != visual.ClassificationFailed || diffs.filter.Limit != 1 ||
		diffs.filter.CaseRunID == nil || *diffs.filter.CaseRunID != 55 ||
		diffs.filter.BeforeID == nil || *diffs.filter.BeforeID != 9000 {
		t.Fatalf("filter = %+v", diffs.filter)
	}
	if decoded["has_more"] != true || decoded["next_cursor"] != "8001" {
		t.Fatalf("envelope = %+v", decoded)
	}
}

func TestListDiffsRejectsInvalidFilters(t *testing.T) {
	mux := newMux(newHandler(&fakeConfig{}, &fakeBaselines{}, &fakeDiffs{}, &fakeOverview{}), "user")
	for _, path := range []string{
		"/api/versions/10/visual/diffs?classification=bogus",
		"/api/versions/10/visual/diffs?limit=0",
		"/api/versions/10/visual/diffs?cursor=abc",
		"/api/versions/10/visual/diffs?caseRunId=-1",
	} {
		recorder, _ := perform(t, mux, http.MethodGet, path, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("path %s: status = %d", path, recorder.Code)
		}
	}
}

func TestGetDiffDetailRendersMismatchBoxes(t *testing.T) {
	diffs := &fakeDiffs{detail: visual.Diff{
		ID: 8001, VisualAssertionID: 6001, CaseRunID: 55, AssertionResultID: 66,
		ActualEvidenceID: 77, DiffEvidenceID: int64Ptr(78), DiffRatio: 0.02, Threshold: 0.005,
		Classification: visual.ClassificationFailed, Reason: "visual_mismatch",
		EnvironmentMatch: true, Width: 100, Height: 100,
		MismatchBoxes: json.RawMessage(`[{"x":0,"y":0,"width":32,"height":32}]`),
		CreatedAt:     time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC),
	}}
	mux := newMux(newHandler(&fakeConfig{}, &fakeBaselines{}, diffs, &fakeOverview{}), "user")
	recorder, decoded := perform(t, mux, http.MethodGet, "/api/versions/10/visual/diffs/8001", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := decoded["data"].(map[string]any)
	boxes := data["mismatch_boxes"].([]any)
	if len(boxes) != 1 || data["diff_evidence_id"] != "78" {
		t.Fatalf("detail = %+v", data)
	}
	if data["classification"] != "failed" || data["environment"] != nil {
		t.Fatalf("detail = %+v", data)
	}
}

func TestGetDiffMapsNotFound(t *testing.T) {
	diffs := &fakeDiffs{err: platform.NotFound("visual_diff")}
	mux := newMux(newHandler(&fakeConfig{}, &fakeBaselines{}, diffs, &fakeOverview{}), "user")
	recorder, decoded := perform(t, mux, http.MethodGet, "/api/versions/10/visual/diffs/8001", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	if decoded["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("error = %+v", decoded)
	}
}

func TestOverviewAndMetrics(t *testing.T) {
	ratio := 0.75
	p50 := 0.002
	p95 := 0.004
	overview := &fakeOverview{
		overview: visualapp.Overview{
			VersionID: 10, AssertionTotal: 4, BaselineCovered: 3, BaselineCoverageRatio: &ratio,
			Passed: 6, Failed: 2, Inconclusive: 1, Blocked: 1, PassRatio: &ratio,
			InconclusiveReasons: []visualapp.ReasonCount{{Reason: "missing_visual_baseline", Count: 1}},
			DiffRatioP50:        &p50, DiffRatioP95: &p95,
		},
		metrics: visualapp.Metrics{
			VersionID: int64Ptr(10), Assertions: 4, Baselines: 3, Diffs: 10,
			Passed: 6, Failed: 2, Inconclusive: 1, Blocked: 1,
			BaselineCoverage: &ratio, PassRate: &ratio, InconclusiveRate: &ratio,
			GeneratedAt: time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC),
		},
	}
	mux := newMux(newHandler(&fakeConfig{}, &fakeBaselines{}, &fakeDiffs{}, overview), "user")
	recorder, decoded := perform(t, mux, http.MethodGet, "/api/versions/10/visual/overview", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := decoded["data"].(map[string]any)
	if data["version_id"] != "10" || data["pass_ratio"] != 0.75 {
		t.Fatalf("overview = %+v", data)
	}
	if data["diff_ratio_p50"] != 0.002 || data["diff_ratio_p95"] != 0.004 {
		t.Fatalf("overview percentiles = %+v", data)
	}
	if data["diff_latency_ms"] != nil {
		t.Fatalf("diff latency must be null (not stored)")
	}
	if len(data["inconclusive_reasons"].([]any)) != 1 {
		t.Fatalf("reasons = %+v", data)
	}

	recorder, decoded = perform(t, mux, http.MethodGet, "/api/quality/visual-metrics?versionId=10", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	metrics := decoded["data"].(map[string]any)
	if metrics["scope"].(map[string]any)["version_id"] != "10" {
		t.Fatalf("metrics = %+v", metrics)
	}
	if metrics["totals"].(map[string]any)["diffs"].(float64) != 10 {
		t.Fatalf("metrics totals = %+v", metrics)
	}
	if metrics["generated_at"] == "" {
		t.Fatalf("generated_at missing")
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	mux := http.NewServeMux()
	unauthorized := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
	visualapi.New(visualapi.Deps{}).Register(mux, unauthorized, unauthorized)
	recorder, _ := perform(t, mux, http.MethodGet, "/api/versions/10/visual/assertions", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestErrorEnvelopeCarriesStableCode(t *testing.T) {
	config := &fakeConfig{err: &platform.Error{
		Status: 422, Code: "config_invalid", Message: "threshold must be between 0 and 0.1",
		Details: map[string]any{"field": "threshold"},
	}}
	mux := newMux(newHandler(config, &fakeBaselines{}, &fakeDiffs{}, &fakeOverview{}), "user")
	recorder, decoded := perform(t, mux, http.MethodPut, "/api/versions/10/visual/assertions/9001", `{"threshold":0.5}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", recorder.Code)
	}
	envelope := decoded["error"].(map[string]any)
	if envelope["code"] != "config_invalid" || envelope["details"].(map[string]any)["field"] != "threshold" {
		t.Fatalf("error = %+v", envelope)
	}
}

func int64Ptr(value int64) *int64 { return &value }
