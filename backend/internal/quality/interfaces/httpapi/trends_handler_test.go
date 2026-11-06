package httpapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

func snapshotRunFixture() qualityapp.EvalRun {
	versionID := int64(7)
	run := fixtureRun()
	run.Mode = quality.EvalModeSnapshot
	run.CorpusVersion = "snapshot:7"
	run.Model = ""
	run.VersionID = &versionID
	run.Metrics = []quality.Metric{
		{Name: quality.MetricDesignCoverage, Value: 0.9, Threshold: 0.8, Passed: true},
		{Name: quality.MetricExecutionPassRate, Value: 0.5, Threshold: 0.8, Passed: false},
		{Name: quality.MetricCaseCoverage, Value: 1, Threshold: 0.8, Passed: true},
		{Name: quality.MetricVisualClassificationAccuracy, Value: 0.9, Threshold: 0.8, Passed: true},
	}
	return run
}

// TestCreateEvalRunSnapshotTriggersServerDerivedRun covers the W2-14 trigger
// contract: POST /api/quality/eval-runs {mode:snapshot, version_id} calls the
// snapshot use case (not the client-sample recorder) and returns 201.
func TestCreateEvalRunSnapshotTriggersServerDerivedRun(t *testing.T) {
	fake := &fakeService{snapshotRun: snapshotRunFixture()}
	mux := newQualityMux(fake, "admin")

	recorder, body := performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs",
		`{"mode":"snapshot","version_id":"7","status":"passed","sample":{"confirmed_requirements":1}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.snapshotVersion != 7 {
		t.Fatalf("snapshot version = %d", fake.snapshotVersion)
	}
	if fake.recordInput.Mode != "" {
		t.Fatalf("snapshot request reached the recorder: %+v", fake.recordInput)
	}
	data := body["data"].(map[string]any)
	if data["mode"] != quality.EvalModeSnapshot || data["version_id"] != "7" {
		t.Fatalf("view = %+v", data)
	}
}

func TestCreateEvalRunSnapshotRequiresVersion(t *testing.T) {
	fake := &fakeService{snapshotRun: snapshotRunFixture()}
	mux := newQualityMux(fake, "admin")
	for _, body := range []string{
		`{"mode":"snapshot"}`,
		`{"mode":"snapshot","version_id":"abc"}`,
		`{"mode":"snapshot","version_id":"0"}`,
	} {
		recorder, decoded := performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d (%s)", body, recorder.Code, recorder.Body.String())
		}
		envelope := decoded["error"].(map[string]any)
		if envelope["code"] != "invalid_argument" {
			t.Fatalf("body %s code = %v", body, envelope["code"])
		}
	}
	if fake.snapshotVersion != 0 {
		t.Fatalf("invalid request reached the service: version=%d", fake.snapshotVersion)
	}
}

func TestCreateEvalRunSnapshotConflictMapsTo409(t *testing.T) {
	fake := &fakeService{snapshotErr: platform.Conflict("eval snapshot already running")}
	mux := newQualityMux(fake, "admin")
	recorder, decoded := performQuality(t, mux, http.MethodPost, "/api/quality/eval-runs",
		`{"mode":"snapshot","version_id":"7"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if decoded["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// TestQualityTrendsEndpoint covers the new W2-14 read: 200 with >=2 points,
// exact version/metric/bound parsing, and 400/403/404 mapping.
func TestQualityTrendsEndpoint(t *testing.T) {
	baseline := 0.7
	versionID := int64(7)
	fake := &fakeService{trends: []qualityapp.TrendPoint{
		{RunID: 101, VersionID: &versionID, Metric: quality.MetricExecutionPassRate, Value: 0.9, Threshold: 0.8, Passed: true, CreatedAt: handlerStart},
		{RunID: 102, VersionID: &versionID, Metric: quality.MetricExecutionPassRate, Value: 0.6, Threshold: 0.8, Passed: false, Baseline: &baseline, Regressed: true, CreatedAt: handlerNow},
	}}
	mux := newQualityMux(fake, "admin")

	recorder, body := performQuality(t, mux, http.MethodGet,
		"/api/quality/trends?version_id=7&metric=execution_pass_rate&from=2026-11-01T10:00:00Z&to=2026-11-01T13:00:00Z", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.trendVersion != 7 || fake.trendMetric != quality.MetricExecutionPassRate {
		t.Fatalf("scope = %d/%s", fake.trendVersion, fake.trendMetric)
	}
	if fake.trendFrom.UTC().Format(time.RFC3339) != "2026-11-01T10:00:00Z" ||
		fake.trendTo.UTC().Format(time.RFC3339) != "2026-11-01T13:00:00Z" {
		t.Fatalf("bounds = %v/%v", fake.trendFrom, fake.trendTo)
	}
	points, _ := body["data"].([]any)
	if len(points) != 2 {
		t.Fatalf("data = %s", recorder.Body.String())
	}
	second := points[1].(map[string]any)
	if second["regressed"] != true || second["metric"] != quality.MetricExecutionPassRate {
		t.Fatalf("point = %+v", second)
	}

	for _, query := range []string{
		"/api/quality/trends?metric=execution_pass_rate",
		"/api/quality/trends?version_id=7",
		"/api/quality/trends?version_id=7&metric=nope",
		"/api/quality/trends?version_id=7&metric=execution_pass_rate&from=yesterday",
	} {
		recorder, _ := performQuality(t, mux, http.MethodGet, query, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d (%s)", query, recorder.Code, recorder.Body.String())
		}
	}
}

func TestQualityTrendsRequiresAdminAndMapsNotFound(t *testing.T) {
	userMux := newQualityMux(&fakeService{}, "user")
	recorder, _ := performQuality(t, userMux, http.MethodGet,
		"/api/quality/trends?version_id=7&metric=execution_pass_rate", "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}

	adminMux := newQualityMux(&fakeService{trendsErr: platform.NotFound("version")}, "admin")
	recorder, _ = performQuality(t, adminMux, http.MethodGet,
		"/api/quality/trends?version_id=7&metric=execution_pass_rate", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
}
