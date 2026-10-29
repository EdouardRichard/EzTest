package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// fakeCenterReader is a programmable CenterReader.
type fakeCenterReader struct {
	catalog     application.CenterCatalog
	catalogErr  error
	sessionPage application.CenterSessionPage
	sessionErr  error
	quality     application.CenterQuality
	qualityErr  error

	catalogCalls int
	sessionCalls int
	qualityCalls int
	lastQuery    application.CenterSessionQuery
}

// Catalog implements CenterReader.
func (f *fakeCenterReader) Catalog(context.Context) (application.CenterCatalog, error) {
	f.catalogCalls++
	return f.catalog, f.catalogErr
}

// Sessions implements CenterReader.
func (f *fakeCenterReader) Sessions(_ context.Context, query application.CenterSessionQuery) (application.CenterSessionPage, error) {
	f.sessionCalls++
	f.lastQuery = query
	return f.sessionPage, f.sessionErr
}

// Quality implements CenterReader.
func (f *fakeCenterReader) Quality(context.Context) (application.CenterQuality, error) {
	f.qualityCalls++
	return f.quality, f.qualityErr
}

// centerTestHandler mounts the center routes on a fresh mux.
func centerTestHandler(reader CenterReader) http.Handler {
	mux := http.NewServeMux()
	RegisterCenterRoutes(mux, NewCenterHandler(reader))
	return mux
}

// centerGet performs one GET request against handler.
func centerGet(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestCenterCatalogHandlerReturnsThirteenRolesWithoutSensitiveFields proves the
// catalog DTO carries the directory only: no prompt body and no secret-shaped
// key ever leaves the read endpoint (spec §8.1/D60).
func TestCenterCatalogHandlerReturnsThirteenRolesWithoutSensitiveFields(t *testing.T) {
	roles := make([]application.CenterRoleView, 13)
	for index := range roles {
		key := fmt.Sprintf("role-%d", index+1)
		roles[index] = application.CenterRoleView{
			Order:        index + 1,
			Role:         key,
			Name:         key,
			Phase:        "P8",
			Mode:         application.CenterModeAgent,
			ProfileKey:   &key,
			ProfileID:    func() *int64 { id := int64(index + 1); return &id }(),
			Enabled:      true,
			Model:        "deepseek-chat",
			AllowedTools: []string{"tool.one"},
			Stats:        application.CenterRoleStats{},
		}
	}
	reader := &fakeCenterReader{catalog: application.CenterCatalog{Roles: roles}}
	rec := centerGet(t, centerTestHandler(reader), "/api/agent-center/catalog")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET catalog = %d body=%s, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			Roles      []map[string]any `json:"roles"`
			LatestEval any              `json:"latest_eval"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, rec.Body)
	}
	if got := len(body.Data.Roles); got != 13 {
		t.Fatalf("len(roles) = %d, want 13", got)
	}
	for _, forbidden := range []string{"system_prompt", "api_key", "apiKey", "sk-", "secret"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("catalog response leaks %q: %s", forbidden, rec.Body)
		}
	}
}

// TestCenterCatalogHandlerHidesInternalErrors proves a failing read returns the
// shared opaque 500 envelope, never the underlying database message.
func TestCenterCatalogHandlerHidesInternalErrors(t *testing.T) {
	reader := &fakeCenterReader{catalogErr: errors.New("pq: relation agent_secret does not exist")}
	rec := centerGet(t, centerTestHandler(reader), "/api/agent-center/catalog")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET catalog error = %d body=%s, want 500", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "agent_secret") {
		t.Errorf("500 body leaks the internal error: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Errorf("500 body = %s, want the shared internal envelope", rec.Body)
	}
}

// TestCenterQualityHandlerKeepsLargeRunIDAsString proves the eval run id keeps
// the D29 decimal-string contract end-to-end: a Snowflake-sized id above
// JavaScript's 2^53 safe-integer range must serialize as a JSON string and lose
// no digits.
func TestCenterQualityHandlerKeepsLargeRunIDAsString(t *testing.T) {
	reader := &fakeCenterReader{quality: application.CenterQuality{Latest: &application.CenterEval{
		RunID:      "9007199254740993", // 2^53+1, not exactly representable as a float64
		Passed:     true,
		Metrics:    map[string]float64{"design_coverage": 1},
		Thresholds: map[string]float64{"design_coverage": 0.9},
		CreatedAt:  time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
	}}}
	rec := centerGet(t, centerTestHandler(reader), "/api/agent-center/quality")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET quality = %d body=%s, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			Latest struct {
				RunID json.RawMessage `json:"run_id"`
			} `json:"latest"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode quality: %v body=%s", err, rec.Body)
	}
	if got := strings.TrimSpace(string(body.Data.Latest.RunID)); got != `"9007199254740993"` {
		t.Fatalf("run_id JSON = %s, want the decimal string \"9007199254740993\" (body=%s)", got, rec.Body)
	}
}

// TestCenterSessionsHandlerPassesFiltersAndLimit pins the read-only query
// projection and the items envelope.
func TestCenterSessionsHandlerPassesFiltersAndLimit(t *testing.T) {
	updated := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	reader := &fakeCenterReader{sessionPage: application.CenterSessionPage{Items: []application.CenterSessionItem{{
		ID:             9,
		Role:           "case-design",
		Title:          "设计会话",
		Status:         "running",
		Running:        true,
		CreatedAt:      updated.Add(-time.Hour),
		UpdatedAt:      updated,
		LastActivityAt: updated,
	}}}}
	rec := centerGet(t, centerTestHandler(reader),
		"/api/agent-center/sessions?role=case-design&status=running&limit=5")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET sessions = %d body=%s, want 200", rec.Code, rec.Body)
	}
	want := application.CenterSessionQuery{Role: "case-design", Status: "running", Limit: 5}
	if reader.lastQuery != want {
		t.Errorf("service query = %+v, want %+v", reader.lastQuery, want)
	}
	if !strings.Contains(rec.Body.String(), `"id":9`) || !strings.Contains(rec.Body.String(), `"running":true`) {
		t.Errorf("sessions body = %s, want the mapped item", rec.Body)
	}
}

// TestCenterSessionsHandlerRejectsBadLimits pins the request validation.
func TestCenterSessionsHandlerRejectsBadLimits(t *testing.T) {
	for _, raw := range []string{"abc", "0", "-1", "101"} {
		reader := &fakeCenterReader{}
		rec := centerGet(t, centerTestHandler(reader), "/api/agent-center/sessions?limit="+raw)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%q status = %d body=%s, want 400", raw, rec.Code, rec.Body)
		}
		if reader.sessionCalls != 0 {
			t.Errorf("limit=%q called the service, want request validation first", raw)
		}
		if !strings.Contains(rec.Body.String(), `"field":"limit"`) {
			t.Errorf("limit=%q body = %s, want details.field=limit", raw, rec.Body)
		}
	}
}

// TestCenterSessionsHandlerMapsUnknownFilters pins 422 details.field for an
// unknown role or status.
func TestCenterSessionsHandlerMapsUnknownFilters(t *testing.T) {
	cases := []struct {
		query string
		err   error
		field string
	}{
		{"role=not-a-role", application.ErrUnknownCenterRole, "role"},
		{"status=exploded", application.ErrUnknownCenterStatus, "status"},
	}
	for _, test := range cases {
		reader := &fakeCenterReader{sessionErr: test.err}
		rec := centerGet(t, centerTestHandler(reader), "/api/agent-center/sessions?"+test.query)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s status = %d body=%s, want 422", test.query, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `"field":"`+test.field+`"`) {
			t.Errorf("%s body = %s, want details.field=%s", test.query, rec.Body, test.field)
		}
	}
}

// TestCenterSessionsHandlerTrimsEmptyFilters documents that blank query values
// mean "all", not an empty-string role/status.
func TestCenterSessionsHandlerTrimsEmptyFilters(t *testing.T) {
	reader := &fakeCenterReader{sessionPage: application.CenterSessionPage{Items: []application.CenterSessionItem{}}}
	rec := centerGet(t, centerTestHandler(reader), "/api/agent-center/sessions?role=+&status=+")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET sessions = %d body=%s, want 200", rec.Code, rec.Body)
	}
	if want := (application.CenterSessionQuery{Limit: application.CenterDefaultSessionLimit}); reader.lastQuery != want {
		t.Errorf("query = %+v, want %+v", reader.lastQuery, want)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty page body = %s, want items:[]", rec.Body)
	}
}

// TestCenterQualityHandlerReturnsNullLatest pins the pre-Task-23 contract: the
// endpoint is mounted and returns an explicit null instead of fake metrics.
func TestCenterQualityHandlerReturnsNullLatest(t *testing.T) {
	reader := &fakeCenterReader{quality: application.CenterQuality{}}
	rec := centerGet(t, centerTestHandler(reader), "/api/agent-center/quality")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET quality = %d body=%s, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			Latest any `json:"latest"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode quality: %v body=%s", err, rec.Body)
	}
	if body.Data.Latest != nil {
		t.Errorf("latest = %v, want null", body.Data.Latest)
	}
}

// TestCenterRoutesAbsentWithoutReader proves a nil reader mounts nothing, so a
// partially wired process never exposes a route backed by nil dependencies.
func TestCenterRoutesAbsentWithoutReader(t *testing.T) {
	if handler := NewCenterHandler(nil); handler != nil {
		t.Fatalf("NewCenterHandler(nil) = %v, want nil", handler)
	}
	mux := http.NewServeMux()
	RegisterCenterRoutes(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent-center/catalog", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unmounted catalog = %d, want 404", rec.Code)
	}
}
