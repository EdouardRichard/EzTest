package designapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func int64Ptr(value int64) *int64 { return &value }

type fakeGraphService struct {
	graph         designapp.GraphView
	overview      designapp.OverviewView
	caseDetail    designapp.CaseDetailView
	confirmResult designapp.ConfirmResult
	err           error
	lastConfirmIn designapp.ConfirmInput
	lastActorID   int64
	lastCaseID    int64
	lastCaseVerID *int64
	lastVersionID int64
	rollbackView  designapp.CaseVersionRollbackView
	lastReason    string
	lastRolledID  int64
}

func (f *fakeGraphService) ReadGraph(_ context.Context, versionID int64) (designapp.GraphView, error) {
	f.lastVersionID = versionID
	if f.err != nil {
		return designapp.GraphView{}, f.err
	}
	return f.graph, nil
}

func (f *fakeGraphService) Overview(_ context.Context, versionID int64) (designapp.OverviewView, error) {
	if f.err != nil {
		return designapp.OverviewView{}, f.err
	}
	return f.overview, nil
}

func (f *fakeGraphService) GetCase(_ context.Context, versionID, caseID int64, caseVersionID *int64) (designapp.CaseDetailView, error) {
	f.lastVersionID = versionID
	f.lastCaseID = caseID
	f.lastCaseVerID = caseVersionID
	if f.err != nil {
		return designapp.CaseDetailView{}, f.err
	}
	return f.caseDetail, nil
}

func (f *fakeGraphService) Confirm(_ context.Context, versionID int64, in designapp.ConfirmInput, actorID int64) (designapp.ConfirmResult, error) {
	f.lastVersionID = versionID
	f.lastConfirmIn = in
	f.lastActorID = actorID
	if f.err != nil {
		return designapp.ConfirmResult{}, f.err
	}
	return f.confirmResult, nil
}

func (f *fakeGraphService) RollbackCaseVersion(_ context.Context, versionID, caseVersionID int64, reason string, actorID int64) (designapp.CaseVersionRollbackView, error) {
	f.lastVersionID = versionID
	f.lastRolledID = caseVersionID
	f.lastReason = reason
	f.lastActorID = actorID
	if f.err != nil {
		return designapp.CaseVersionRollbackView{}, f.err
	}
	return f.rollbackView, nil
}

type fakeExportService struct {
	createResult designapp.ExportView
	listResult   []designapp.ExportView
	listHasMore  bool
	getResult    designapp.ExportView
	openResult   designapp.ExportView
	openReader   io.ReadCloser
	err          error
	createCalls  int
	lastCreateIn designapp.ExportInput
	lastActorID  int64
	lastFormat   string
	lastLimit    int
	lastBeforeID *int64
}

func (f *fakeExportService) Create(_ context.Context, versionID int64, in designapp.ExportInput, actorID int64) (designapp.ExportView, error) {
	f.createCalls++
	f.lastCreateIn = in
	f.lastActorID = actorID
	if f.err != nil {
		return designapp.ExportView{}, f.err
	}
	return f.createResult, nil
}

func (f *fakeExportService) List(_ context.Context, versionID int64, format string, limit int, beforeID *int64) ([]designapp.ExportView, bool, error) {
	f.lastFormat = format
	f.lastLimit = limit
	f.lastBeforeID = beforeID
	if f.err != nil {
		return nil, false, f.err
	}
	return f.listResult, f.listHasMore, nil
}

func (f *fakeExportService) Get(_ context.Context, exportID int64) (designapp.ExportView, error) {
	if f.err != nil {
		return designapp.ExportView{}, f.err
	}
	return f.getResult, nil
}

func (f *fakeExportService) Open(_ context.Context, exportID int64) (designapp.ExportView, io.ReadCloser, error) {
	if f.err != nil {
		return designapp.ExportView{}, nil, f.err
	}
	return f.openResult, f.openReader, nil
}

func newTestHandler() (*Handler, *fakeGraphService, *fakeExportService) {
	graph := &fakeGraphService{}
	exports := &fakeExportService{}
	return New(graph, exports), graph, exports
}

func testMux(handler *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /api/versions/{id}/design/graph", http.HandlerFunc(handler.ReadGraph))
	mux.Handle("GET /api/versions/{id}/design/overview", http.HandlerFunc(handler.Overview))
	mux.Handle("GET /api/versions/{id}/design/cases/{caseId}", http.HandlerFunc(handler.GetCase))
	mux.Handle("POST /api/versions/{id}/design/confirmations", http.HandlerFunc(handler.Confirm))
	mux.Handle("POST /api/versions/{id}/case-versions/{caseVersionId}/rollback", http.HandlerFunc(handler.RollbackCaseVersion))
	mux.Handle("POST /api/versions/{id}/exports", http.HandlerFunc(handler.CreateExport))
	mux.Handle("GET /api/versions/{id}/exports", http.HandlerFunc(handler.ListExports))
	mux.Handle("GET /api/exports/{exportId}", http.HandlerFunc(handler.GetExport))
	mux.Handle("GET /api/exports/{exportId}/download", http.HandlerFunc(handler.DownloadExport))
	return mux
}

func authedRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	return request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: 7, Role: "admin"}))
}

func decodeEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code < 200 || recorder.Code >= 300 {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, recorder.Body.String())
	}
	return envelope.Data
}

// fixedDesignStageReader is the deterministic stage projection stub for the
// case-gate guard.
type fixedDesignStageReader struct {
	stage version.Stage
	err   error
}

func (r fixedDesignStageReader) Stage(_ context.Context, versionID int64) (versionapp.StageView, error) {
	if r.err != nil {
		return versionapp.StageView{}, r.err
	}
	return versionapp.StageView{VersionID: versionID, Stage: r.stage}, nil
}

// TestCaseConfirmationRequiresCaseReview pins the Task 78 guard on the single
// case confirmation path: the design API must not freeze cases while the
// version is clarifying/designing, and the typed 409 must carry the stage and
// next action. At case_review the same request reaches the graph service.
func TestCaseConfirmationRequiresCaseReview(t *testing.T) {
	handler, graph, _ := newTestHandler()
	handler.SetStageReader(fixedDesignStageReader{stage: version.StageClarifying})
	mux := testMux(handler)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, authedRequest(http.MethodPost,
		"/api/versions/1001/design/confirmations", strings.NewReader(`{"scope":"case","target_id":"7001"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errorData, _ := payload["error"].(map[string]any)
	if errorData["code"] != "invalid_stage_for_action" {
		t.Fatalf("error = %#v, want invalid_stage_for_action", errorData)
	}
	details, _ := errorData["details"].(map[string]any)
	if details["required_stage"] != "case_review" || details["current_stage"] != "clarifying" {
		t.Fatalf("details = %#v", details)
	}
	if next, _ := details["next_action"].(string); next == "" {
		t.Fatalf("next_action = %#v, want actionable guidance", details["next_action"])
	}
	if graph.lastVersionID != 0 {
		t.Fatalf("wrong-stage confirmation reached the graph service: %+v", graph)
	}

	handler.SetStageReader(fixedDesignStageReader{stage: version.StageCaseReview})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, authedRequest(http.MethodPost,
		"/api/versions/1001/design/confirmations", strings.NewReader(`{"scope":"case","target_id":"7001"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("case_review status = %d, body = %s", response.Code, response.Body.String())
	}
	if graph.lastVersionID != 1001 || graph.lastConfirmIn.TargetID != 7001 {
		t.Fatalf("confirm input = %+v version=%d", graph.lastConfirmIn, graph.lastVersionID)
	}
}

// TestConfirmationStageGuidanceCoversEveryScope pins the Task 82 guidance fix:
// the confirmation endpoint serves case, scenario and requirement scopes, so
// its wrong-stage 409 must echo the requested scope and carry scope-appropriate
// next-action guidance instead of a case-only message.
func TestConfirmationStageGuidanceCoversEveryScope(t *testing.T) {
	for _, scope := range []string{"case", "scenario", "requirement"} {
		t.Run(scope, func(t *testing.T) {
			handler, graph, _ := newTestHandler()
			handler.SetStageReader(fixedDesignStageReader{stage: version.StageDesigning})
			mux := testMux(handler)

			response := httptest.NewRecorder()
			body := `{"scope":"` + scope + `","target_id":"7001"}`
			mux.ServeHTTP(response, authedRequest(http.MethodPost,
				"/api/versions/1001/design/confirmations", strings.NewReader(body)))
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409, body = %s", response.Code, response.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			errorData, _ := payload["error"].(map[string]any)
			if errorData["code"] != "invalid_stage_for_action" {
				t.Fatalf("error = %#v, want invalid_stage_for_action", errorData)
			}
			details, _ := errorData["details"].(map[string]any)
			if details["scope"] != scope {
				t.Fatalf("details.scope = %#v, want %q", details["scope"], scope)
			}
			if next, _ := details["next_action"].(string); next == "" {
				t.Fatalf("next_action = %#v, want actionable guidance", details["next_action"])
			}
			if graph.lastVersionID != 0 {
				t.Fatalf("wrong-stage confirmation reached the graph service: %+v", graph)
			}
		})
	}
}

func TestGraphHandlerReturnsEnvelope(t *testing.T) {
	handler, graph, _ := newTestHandler()
	generatedAt := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	graph.graph = designapp.GraphView{
		VersionID: 1001, GeneratedAt: generatedAt,
		GraphHash: strings.Repeat("a", 64), PendingDesignCount: 2,
		Stats: designapp.GraphStatsView{Requirements: 1, Scenarios: 1, Cases: 1, Draft: 1},
		Requirements: []designapp.RequirementGraphView{{
			RequirementID: 3001, RequirementVersionID: 3002, Revision: 2, Title: "登录",
			Status: "clarified", DesignStatus: "draft", TargetVersionSUTIDs: []int64{9001},
			Scenarios: []designapp.ScenarioGraphView{{
				ID: 5001, RequirementVersionID: 3002, Name: "正常登录",
				VersionSUTIDs: []int64{9001}, Cases: []designapp.CaseCardView{{
					CaseID: 7001, CurrentVersionID: 7002, Revision: 1, Title: "登录成功",
					Priority: "P1", Status: "draft", DefaultVersionSUTID: int64Ptr(9001),
					VersionSUTIDs: []int64{9001}, StepCount: 2, AssertionCount: 1,
					ExecutionVerdict: "failed",
				}},
			}},
		}},
	}
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/versions/1001/design/graph", nil))
	data := decodeEnvelope(t, recorder)
	if data["version_id"] != "1001" || data["graph_hash"] != strings.Repeat("a", 64) {
		t.Fatalf("graph envelope = %v", data)
	}
	if data["generated_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("generated_at = %v", data["generated_at"])
	}
	stats := data["stats"].(map[string]any)
	if stats["requirements"].(float64) != 1 || stats["draft"].(float64) != 1 {
		t.Fatalf("stats = %v", stats)
	}
	requirements := data["requirements"].([]any)
	requirement := requirements[0].(map[string]any)
	if requirement["requirement_id"] != "3001" || requirement["requirement_version_id"] != "3002" {
		t.Fatalf("requirement = %v", requirement)
	}
	scenario := requirement["scenarios"].([]any)[0].(map[string]any)
	card := scenario["cases"].([]any)[0].(map[string]any)
	if card["case_id"] != "7001" || card["current_version_id"] != "7002" || card["default_version_sut_id"] != "9001" {
		t.Fatalf("case card = %v", card)
	}
	if card["execution_verdict"] != "failed" {
		t.Fatalf("case execution_verdict = %v", card["execution_verdict"])
	}
}

func TestOverviewHandler(t *testing.T) {
	handler, graph, _ := newTestHandler()
	graph.overview = designapp.OverviewView{
		PendingDesignCount: 2, RoundCount: 1,
		ActiveRound: &designapp.RoundView{
			ID: 8001, VersionID: 1001, ConversationID: 9001, RoundNo: 1,
			Activation: "manual", RequirementIDs: []int64{3001},
		},
	}
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/versions/1001/design/overview", nil))
	data := decodeEnvelope(t, recorder)
	if data["pending_design_count"].(float64) != 2 || data["round_count"].(float64) != 1 {
		t.Fatalf("overview = %v", data)
	}
	active := data["active_round"].(map[string]any)
	if active["id"] != "8001" || active["round_no"].(float64) != 1 || active["conversation_id"] != "9001" {
		t.Fatalf("active round = %v", active)
	}
}

func TestCaseHandlerUsesVersionQuery(t *testing.T) {
	handler, graph, _ := newTestHandler()
	graph.caseDetail = designapp.CaseDetailView{
		CaseID: 7001, Status: "draft", CurrentVersionID: int64Ptr(7002),
		RequirementID: 3001, RequirementVersionID: 3002, ScenarioID: 5001, ScenarioName: "正常登录",
		Versions: []designapp.CaseVersionSummaryView{{
			CaseVersionID: 7002, Revision: 1, Status: "draft", Title: "登录成功",
			CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		}},
		Selected: designapp.CaseVersionDetailView{
			CaseVersionID: 7002, Revision: 1, Title: "登录成功", Priority: "P2", Status: "draft",
			VersionSUTIDs: []int64{9001}, DefaultVersionSUTID: int64Ptr(9001),
			TestData: json.RawMessage(`{}`),
			Steps:    []designapp.CaseStepView{{Seq: 1, Action: "打开登录页", Data: json.RawMessage(`{}`)}},
		},
		Confirmable: true,
	}
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/versions/1001/design/cases/7001?version_id=7002", nil))
	data := decodeEnvelope(t, recorder)
	if graph.lastCaseID != 7001 || graph.lastCaseVerID == nil || *graph.lastCaseVerID != 7002 {
		t.Fatalf("handler resolution: caseID=%d versionID=%v", graph.lastCaseID, graph.lastCaseVerID)
	}
	if data["case_id"] != "7001" || data["confirmable"] != true {
		t.Fatalf("case detail = %v", data)
	}
	selected := data["selected"].(map[string]any)
	if selected["case_version_id"] != "7002" || selected["default_version_sut_id"] != "9001" {
		t.Fatalf("selected = %v", selected)
	}
	step := selected["steps"].([]any)[0].(map[string]any)
	if step["seq"].(float64) != 1 {
		t.Fatalf("step = %v", step)
	}

	bad := httptest.NewRecorder()
	testMux(handler).ServeHTTP(bad, authedRequest(http.MethodGet, "/api/versions/1001/design/cases/7001?version_id=abc", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad version_id status = %d", bad.Code)
	}
}

func TestConfirmHandlerValidationAndEnvelope(t *testing.T) {
	handler, graph, _ := newTestHandler()
	graph.confirmResult = designapp.ConfirmResult{
		Scope: "case", TargetID: 7001, BatchID: "batch_abc",
		Confirmed:   []designapp.ConfirmedCaseView{{CaseID: 7001, CaseVersionID: 7002}},
		ConfirmedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
	body := `{"scope":"case","target_id":"7001","batch_id":"batch_abc"}`
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodPost, "/api/versions/1001/design/confirmations", strings.NewReader(body)))
	data := decodeEnvelope(t, recorder)
	if graph.lastConfirmIn.Scope != "case" || graph.lastConfirmIn.TargetID != 7001 || graph.lastActorID != 7 {
		t.Fatalf("confirm input = %+v actor=%d", graph.lastConfirmIn, graph.lastActorID)
	}
	if data["scope"] != "case" || data["target_id"] != "7001" || data["batch_id"] != "batch_abc" {
		t.Fatalf("confirm envelope = %v", data)
	}
	confirmed := data["confirmed"].([]any)[0].(map[string]any)
	if confirmed["case_id"] != "7001" || confirmed["case_version_id"] != "7002" {
		t.Fatalf("confirmed = %v", confirmed)
	}

	for _, badBody := range []string{`not-json`, `{"scope":"batch","target_id":"7001"}`, `{"scope":"case","target_id":"abc"}`} {
		bad := httptest.NewRecorder()
		testMux(handler).ServeHTTP(bad, authedRequest(http.MethodPost, "/api/versions/1001/design/confirmations", strings.NewReader(badBody)))
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d body=%s", badBody, bad.Code, bad.Body.String())
		}
	}

	graph.err = platform.StateConflict("no draft case version")
	conflict := httptest.NewRecorder()
	testMux(handler).ServeHTTP(conflict, authedRequest(http.MethodPost, "/api/versions/1001/design/confirmations", strings.NewReader(body)))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d", conflict.Code)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(conflict.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "state_conflict" {
		t.Fatalf("conflict envelope = %s", conflict.Body.String())
	}
}

func TestExportHandlerRejectsUnsupportedFormat(t *testing.T) {
	handler, _, exports := newTestHandler()
	for _, badBody := range []string{
		`{"owner_kind":"case","owner_id":"1001","format":"html"}`,
		`{"owner_kind":"case","owner_id":"1001","format":"excel"}`,
		`{"owner_kind":"report","owner_id":"1001","format":"markdown"}`,
		`{"owner_kind":"case","owner_id":"9999","format":"markdown"}`,
		`{"owner_kind":"case","owner_id":"1001"}`,
	} {
		recorder := httptest.NewRecorder()
		testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodPost, "/api/versions/1001/exports", strings.NewReader(badBody)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d", badBody, recorder.Code)
		}
	}
	if exports.createCalls != 0 {
		t.Fatalf("invalid export reached the service %d times", exports.createCalls)
	}
}

func TestCreateExportHandlerEnvelope(t *testing.T) {
	handler, _, exports := newTestHandler()
	exports.createResult = designapp.ExportView{
		ID: 9001, VersionID: 1001, OwnerKind: "case", OwnerID: 1001, Revision: 1,
		Format: "markdown", Size: 42, MIME: "text/markdown; charset=utf-8",
		Filename: "case-export-9001.md", SHA256: strings.Repeat("b", 64), Status: "ready",
		CreatedAt:   time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		DownloadURL: "/api/exports/9001/download",
	}
	body := `{"owner_kind":"case","owner_id":"1001","format":"markdown"}`
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodPost, "/api/versions/1001/exports", strings.NewReader(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := decodeEnvelope(t, recorder)
	if data["id"] != "9001" || data["sha256"] != strings.Repeat("b", 64) || data["download_url"] != "/api/exports/9001/download" {
		t.Fatalf("export envelope = %v", data)
	}
	if exports.lastCreateIn.OwnerKind != "case" || exports.lastCreateIn.OwnerID != 1001 || exports.lastActorID != 7 {
		t.Fatalf("create input = %+v actor=%d", exports.lastCreateIn, exports.lastActorID)
	}
}

func TestListExportsEnvelope(t *testing.T) {
	handler, _, exports := newTestHandler()
	exports.listResult = []designapp.ExportView{
		{ID: 9002, VersionID: 1001, Revision: 2, Format: "csv"},
		{ID: 9001, VersionID: 1001, Revision: 1, Format: "markdown"},
	}
	exports.listHasMore = true
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/versions/1001/exports?format=markdown&limit=2&before=9005", nil))
	var envelope struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(envelope.Data) != 2 || !envelope.HasMore || envelope.NextCursor != "9001" {
		t.Fatalf("list envelope = %s", recorder.Body.String())
	}
	if exports.lastFormat != "markdown" || exports.lastLimit != 2 || exports.lastBeforeID == nil || *exports.lastBeforeID != 9005 {
		t.Fatalf("list filters = %+v", exports)
	}
	if envelope.Data[0]["id"] != "9002" || envelope.Data[0]["version_id"] != "1001" {
		t.Fatalf("list data = %v", envelope.Data[0])
	}
}

func TestDownloadSetsContentDispositionAndVerifiesHash(t *testing.T) {
	handler, _, exports := newTestHandler()
	exports.openResult = designapp.ExportView{
		ID: 9001, VersionID: 1001, Format: "markdown", Size: 5,
		MIME: "text/markdown; charset=utf-8", Filename: "case-export-9001.md",
		SHA256: strings.Repeat("c", 64),
	}
	exports.openReader = io.NopCloser(strings.NewReader("hello"))
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/exports/9001/download", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename="case-export-9001.md"`) {
		t.Fatalf("content disposition = %q", got)
	}
	if got := recorder.Header().Get("X-Content-SHA256"); got != strings.Repeat("c", 64) {
		t.Fatalf("sha header = %q", got)
	}
	if recorder.Body.String() != "hello" {
		t.Fatalf("body = %q", recorder.Body.String())
	}

	exports.err = platform.NotFound("export")
	missing := httptest.NewRecorder()
	testMux(handler).ServeHTTP(missing, authedRequest(http.MethodGet, "/api/exports/9999/download", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing download status = %d", missing.Code)
	}

	exports.err = errors.New("boom")
	failed := httptest.NewRecorder()
	testMux(handler).ServeHTTP(failed, authedRequest(http.MethodGet, "/api/exports/9999/download", nil))
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("failed download status = %d", failed.Code)
	}
}

func TestNoCloseDesignRoute(t *testing.T) {
	handler, _, _ := newTestHandler()
	for _, target := range []string{"/api/versions/1001/design/close", "/api/versions/1001/design/conversations/close"} {
		recorder := httptest.NewRecorder()
		testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodPost, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", target, recorder.Code)
		}
	}
}

func TestRollbackCaseVersionHandler(t *testing.T) {
	handler, graph, _ := newTestHandler()
	graph.rollbackView = designapp.CaseVersionRollbackView{
		CaseID: 7001, CaseVersionID: 7009, RolledBackFrom: 7008,
		Revision: 3, Status: "confirmed", Trigger: "rollback",
	}
	body := `{"reason":"探索误判"}`
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodPost,
		"/api/versions/1001/case-versions/7008/rollback", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if graph.lastVersionID != 1001 || graph.lastRolledID != 7008 || graph.lastReason != "探索误判" || graph.lastActorID != 7 {
		t.Fatalf("rollback input version=%d rolled=%d reason=%q actor=%d",
			graph.lastVersionID, graph.lastRolledID, graph.lastReason, graph.lastActorID)
	}
	data := decodeEnvelope(t, recorder)
	if data["case_id"] != "7001" || data["case_version_id"] != "7009" ||
		data["rolled_back_from"] != "7008" || data["trigger"] != "rollback" {
		t.Fatalf("rollback envelope = %v", data)
	}

	// Invalid path id and malformed JSON are 400s and never reach the service.
	invalid := httptest.NewRecorder()
	testMux(handler).ServeHTTP(invalid, authedRequest(http.MethodPost,
		"/api/versions/1001/case-versions/not-a-number/rollback", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid path status = %d", invalid.Code)
	}
	malformed := httptest.NewRecorder()
	testMux(handler).ServeHTTP(malformed, authedRequest(http.MethodPost,
		"/api/versions/1001/case-versions/7008/rollback", strings.NewReader("not-json")))
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d body=%s", malformed.Code, malformed.Body.String())
	}
}

func TestRollbackCaseVersionHandlerErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"not_found", platform.NotFound("case version"), http.StatusNotFound},
		{"conflict", platform.StateConflict("only a confirmed case version can be rolled back"), http.StatusConflict},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler, graph, _ := newTestHandler()
			graph.err = testCase.err
			recorder := httptest.NewRecorder()
			testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodPost,
				"/api/versions/1001/case-versions/7008/rollback", nil))
			if recorder.Code != testCase.status {
				t.Fatalf("status = %d, want %d body=%s", recorder.Code, testCase.status, recorder.Body.String())
			}
		})
	}
}

func TestGetExportHandler(t *testing.T) {
	handler, _, exports := newTestHandler()
	exports.getResult = designapp.ExportView{ID: 9001, VersionID: 1001, Format: "opml", Revision: 1}
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/exports/9001", nil))
	data := decodeEnvelope(t, recorder)
	if data["id"] != "9001" || data["format"] != "opml" {
		t.Fatalf("get export = %v", data)
	}
}
