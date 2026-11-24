package designapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeJudgmentService is the narrow judgment surface the design handler needs
// (T16). It records call arguments so tests can pin that path/query IDs and the
// authenticated actor reach the application service unchanged, and it stands
// in for the real *designapp.JudgmentService (whose own predicate logic is
// pinned by the T6 application tests).
type fakeJudgmentService struct {
	overrideResult design.Override
	overrideErr    error
	overrideCalls  int
	lastVersionID  int64
	lastCaseID     int64
	lastActorID    int64
	lastReason     string

	report    designapp.JudgmentReportView
	reportErr error
	lastReqID *int64
}

func (f *fakeJudgmentService) Override(_ context.Context, versionID, caseID, actorID int64, reason string) (design.Override, error) {
	f.overrideCalls++
	f.lastVersionID = versionID
	f.lastCaseID = caseID
	f.lastActorID = actorID
	f.lastReason = reason
	if f.overrideErr != nil {
		return design.Override{}, f.overrideErr
	}
	return f.overrideResult, nil
}

func (f *fakeJudgmentService) ListJudgments(_ context.Context, versionID int64, requirementID *int64) (designapp.JudgmentReportView, error) {
	f.lastVersionID = versionID
	f.lastReqID = requirementID
	if f.reportErr != nil {
		return designapp.JudgmentReportView{}, f.reportErr
	}
	return f.report, nil
}

// judgmentTestMux mirrors the existing design routes plus the two new
// judgment routes, mounted directly on the handler (no auth middleware), the
// same shape testMux uses for every other design endpoint. The admin guard is
// exercised here at the handler level (defense in depth); server-level route
// mounting behind the admin middleware is pinned separately in
// internal/server's design judgment wiring test.
func judgmentTestMux(handler *Handler) *http.ServeMux {
	mux := testMux(handler)
	mux.Handle("POST /api/versions/{id}/design/judgments/{caseId}/override", http.HandlerFunc(handler.OverrideJudgment))
	mux.Handle("GET /api/versions/{id}/design/judgments", http.HandlerFunc(handler.ListJudgments))
	return mux
}

func judgmentRequest(method, target, role string, body *strings.Reader) *http.Request {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, body)
	}
	return request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: 7, Role: role}))
}

// TestOverrideEndpointAdminOnly pins the T16 admin guard: a non-admin caller
// must never reach JudgmentService.Override, and an admin caller against a
// case whose review is escalated (fail exhausted / judge unavailable) gets the
// override audit view back.
func TestOverrideEndpointAdminOnly(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgments := &fakeJudgmentService{
		overrideResult: design.Override{
			ID: 9001, CaseID: 7001, CaseVersionID: 7002, ContentHash: strings.Repeat("a", 64),
			Condition: designapp.OverrideConditionFailExhausted, ActorID: 7, Reason: "接受风险",
			CreatedAt: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		},
	}
	handler.SetJudgmentService(judgments)
	mux := judgmentTestMux(handler)

	// user -> 403, and the application service is never called.
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, judgmentRequest(http.MethodPost,
		"/api/versions/1001/design/judgments/7001/override", "user", strings.NewReader(`{"reason":"接受风险"}`)))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("user override status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	if judgments.overrideCalls != 0 {
		t.Fatalf("a non-admin override reached JudgmentService.Override %d times", judgments.overrideCalls)
	}

	// admin + escalated_fail -> 200.
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, judgmentRequest(http.MethodPost,
		"/api/versions/1001/design/judgments/7001/override", "admin", strings.NewReader(`{"reason":"接受风险"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin override status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if judgments.lastVersionID != 1001 || judgments.lastCaseID != 7001 || judgments.lastActorID != 7 || judgments.lastReason != "接受风险" {
		t.Fatalf("override call = version=%d case=%d actor=%d reason=%q",
			judgments.lastVersionID, judgments.lastCaseID, judgments.lastActorID, judgments.lastReason)
	}
	data := decodeEnvelope(t, recorder)
	if data["id"] != "9001" || data["case_id"] != "7001" || data["case_version_id"] != "7002" ||
		data["condition"] != "fail_exhausted" || data["actor_id"] != "7" || data["reason"] != "接受风险" {
		t.Fatalf("override envelope = %v", data)
	}
	if _, ok := data["judgment_id"]; ok {
		t.Fatalf("override without a referenced judgment must omit judgment_id: %v", data)
	}
}

// TestOverrideEndpointNotOverridable pins that JudgmentService.Override's own
// precondition failure (a pass verdict, or a fail still within its rework
// budget) propagates as the typed 409 it already is -- the handler adds no
// second predicate of its own.
func TestOverrideEndpointNotOverridable(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgments := &fakeJudgmentService{
		overrideErr: platform.StateConflict("override requires an exhausted fail verdict or a judge-unavailable escalation for the current case version"),
	}
	handler.SetJudgmentService(judgments)
	recorder := httptest.NewRecorder()
	judgmentTestMux(handler).ServeHTTP(recorder, judgmentRequest(http.MethodPost,
		"/api/versions/1001/design/judgments/7001/override", "admin", strings.NewReader(`{"reason":"接受风险"}`)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("not-overridable status = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "state_conflict" {
		t.Fatalf("conflict envelope = %s err=%v", recorder.Body.String(), err)
	}
}

// TestOverrideEndpointReferencesJudgment pins the optional judgment_id shape:
// an override of a fail-exhausted verdict references the exact judgment fact;
// a judge-unavailable override has none.
func TestOverrideEndpointReferencesJudgment(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgmentID := int64(4001)
	judgments := &fakeJudgmentService{
		overrideResult: design.Override{
			ID: 9002, CaseID: 7001, CaseVersionID: 7002, JudgmentID: &judgmentID,
			Condition: designapp.OverrideConditionFailExhausted, ActorID: 7, Reason: "复核后接受",
		},
	}
	handler.SetJudgmentService(judgments)
	recorder := httptest.NewRecorder()
	judgmentTestMux(handler).ServeHTTP(recorder, judgmentRequest(http.MethodPost,
		"/api/versions/1001/design/judgments/7001/override", "admin", strings.NewReader(`{"reason":"复核后接受"}`)))
	data := decodeEnvelope(t, recorder)
	if data["judgment_id"] != "4001" {
		t.Fatalf("judgment_id = %v, want 4001", data["judgment_id"])
	}
}

func TestOverrideEndpointValidation(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgments := &fakeJudgmentService{}
	handler.SetJudgmentService(judgments)
	mux := judgmentTestMux(handler)

	for _, tc := range []struct {
		name, target, body string
	}{
		{"bad version id", "/api/versions/abc/design/judgments/7001/override", `{"reason":"接受风险"}`},
		{"bad case id", "/api/versions/1001/design/judgments/abc/override", `{"reason":"接受风险"}`},
		{"malformed body", "/api/versions/1001/design/judgments/7001/override", `not-json`},
		{"empty reason", "/api/versions/1001/design/judgments/7001/override", `{"reason":"   "}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, judgmentRequest(http.MethodPost, tc.target, "admin", strings.NewReader(tc.body)))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
			}
		})
	}
	if judgments.overrideCalls != 0 {
		t.Fatalf("invalid requests reached JudgmentService.Override %d times", judgments.overrideCalls)
	}
}

// TestOverrideEndpointUnwiredFailsClosed pins constitution VII.6: without the
// judgment service wired, the endpoint answers 503 instead of silently
// pretending an override succeeded (or panicking on a nil dependency).
func TestOverrideEndpointUnwiredFailsClosed(t *testing.T) {
	handler, _, _ := newTestHandler()
	recorder := httptest.NewRecorder()
	judgmentTestMux(handler).ServeHTTP(recorder, judgmentRequest(http.MethodPost,
		"/api/versions/1001/design/judgments/7001/override", "admin", strings.NewReader(`{"reason":"接受风险"}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired override status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
}

// TestJudgmentReadEndpointReturnsReport pins the read-only judgment report
// endpoint: a user (not just an admin) can read it, matching the design
// graph/case read visibility (case_review is user-facing), and the optional
// requirement_id filter reaches the application service unchanged.
func TestJudgmentReadEndpointReturnsReport(t *testing.T) {
	handler, _, _ := newTestHandler()
	createdAt := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	judgments := &fakeJudgmentService{
		report: designapp.JudgmentReportView{Cases: []designapp.JudgmentCaseJudgmentView{{
			CaseID: 7001, CurrentVersionID: 7002, ReviewState: design.ReviewStatePass,
			Judgment: &design.Judgment{
				ID: 4001, CaseID: 7001, CaseVersionID: 7002, Round: 1, Verdict: design.VerdictPass,
				Findings:         []design.JudgmentFinding{{Dimension: "coverage", Severity: design.SeverityMinor, Message: "边界略欠", CaseRef: "7001"}},
				ACCoverageMatrix: map[string]any{"AC-1": []any{"7001"}},
				CreatedAt:        createdAt,
			},
		}}},
	}
	handler.SetJudgmentService(judgments)
	mux := judgmentTestMux(handler)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, judgmentRequest(http.MethodGet, "/api/versions/1001/design/judgments", "user", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("judgment read status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if judgments.lastVersionID != 1001 || judgments.lastReqID != nil {
		t.Fatalf("read call = version=%d requirement=%v, want version=1001 requirement=nil", judgments.lastVersionID, judgments.lastReqID)
	}
	data := decodeEnvelope(t, recorder)
	cases := data["cases"].([]any)
	if len(cases) != 1 {
		t.Fatalf("cases = %v", cases)
	}
	card := cases[0].(map[string]any)
	if card["case_id"] != "7001" || card["current_version_id"] != "7002" || card["review_state"] != "pass" {
		t.Fatalf("case judgment = %v", card)
	}
	judgment := card["judgment"].(map[string]any)
	if judgment["id"] != "4001" || judgment["round"].(float64) != 1 || judgment["verdict"] != "pass" {
		t.Fatalf("judgment = %v", judgment)
	}
	findings := judgment["findings"].([]any)
	if len(findings) != 1 || findings[0].(map[string]any)["severity"] != "minor" {
		t.Fatalf("findings = %v", findings)
	}
	if judgment["ac_coverage_matrix"] == nil {
		t.Fatalf("ac_coverage_matrix missing: %v", judgment)
	}

	// requirement_id filter reaches the service unchanged.
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, judgmentRequest(http.MethodGet, "/api/versions/1001/design/judgments?requirement_id=3001", "admin", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("filtered read status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if judgments.lastReqID == nil || *judgments.lastReqID != 3001 {
		t.Fatalf("requirement filter = %v, want 3001", judgments.lastReqID)
	}
}

// TestJudgmentReadEndpointUnjudgedCase pins that a case with no judgment fact
// yet still appears in the report (review_state pending, judgment null) --
// the report is the case_review/designing view, not a filter over judged cases
// only.
func TestJudgmentReadEndpointUnjudgedCase(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgments := &fakeJudgmentService{
		report: designapp.JudgmentReportView{Cases: []designapp.JudgmentCaseJudgmentView{
			{CaseID: 7003, CurrentVersionID: 7004, ReviewState: design.ReviewStatePending, Judgment: nil},
		}},
	}
	handler.SetJudgmentService(judgments)
	recorder := httptest.NewRecorder()
	judgmentTestMux(handler).ServeHTTP(recorder, judgmentRequest(http.MethodGet, "/api/versions/1001/design/judgments", "user", nil))
	data := decodeEnvelope(t, recorder)
	card := data["cases"].([]any)[0].(map[string]any)
	if card["review_state"] != "pending" || card["judgment"] != nil {
		t.Fatalf("unjudged case = %v, want review_state=pending judgment=null", card)
	}
}

func TestJudgmentReadEndpointValidation(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgments := &fakeJudgmentService{}
	handler.SetJudgmentService(judgments)
	mux := judgmentTestMux(handler)

	for _, target := range []string{
		"/api/versions/abc/design/judgments",
		"/api/versions/1001/design/judgments?requirement_id=abc",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, judgmentRequest(http.MethodGet, target, "admin", nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s, want 400", target, recorder.Code, recorder.Body.String())
		}
	}
}

func TestJudgmentReadEndpointUnwiredFailsClosed(t *testing.T) {
	handler, _, _ := newTestHandler()
	recorder := httptest.NewRecorder()
	judgmentTestMux(handler).ServeHTTP(recorder, judgmentRequest(http.MethodGet, "/api/versions/1001/design/judgments", "admin", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired judgment read status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
}

func TestJudgmentReadEndpointPropagatesServiceError(t *testing.T) {
	handler, _, _ := newTestHandler()
	judgments := &fakeJudgmentService{reportErr: errors.New("boom")}
	handler.SetJudgmentService(judgments)
	recorder := httptest.NewRecorder()
	judgmentTestMux(handler).ServeHTTP(recorder, judgmentRequest(http.MethodGet, "/api/versions/1001/design/judgments", "admin", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("service error status = %d body=%s, want 500", recorder.Code, recorder.Body.String())
	}
}

// TestReviewStateViewInCaseDTOs pins that the review_state projection reaches
// both the graph card DTO and the case detail DTO unchanged from the
// application view (the application-layer derivation itself is pinned by
// TestDesignGraphCarriesReviewState / TestGetCaseCarriesReviewState).
func TestReviewStateViewInCaseDTOs(t *testing.T) {
	handler, graph, _ := newTestHandler()
	graph.graph = designapp.GraphView{
		VersionID: 1001,
		Requirements: []designapp.RequirementGraphView{{
			RequirementID: 3001, RequirementVersionID: 3002,
			Scenarios: []designapp.ScenarioGraphView{{
				ID: 5001,
				Cases: []designapp.CaseCardView{{
					CaseID: 7001, CurrentVersionID: 7002, Title: "登录成功", Status: "draft",
					ReviewState: design.ReviewStateEscalatedFail,
				}},
			}},
		}},
	}
	recorder := httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/versions/1001/design/graph", nil))
	data := decodeEnvelope(t, recorder)
	card := data["requirements"].([]any)[0].(map[string]any)["scenarios"].([]any)[0].(map[string]any)["cases"].([]any)[0].(map[string]any)
	if card["review_state"] != "escalated_fail" {
		t.Fatalf("case card review_state = %v, want escalated_fail", card["review_state"])
	}

	graph.caseDetail = designapp.CaseDetailView{
		CaseID: 7001, Status: "draft", CurrentVersionID: int64Ptr(7002),
		ReviewState: design.ReviewStateJudgeUnavailable,
	}
	recorder = httptest.NewRecorder()
	testMux(handler).ServeHTTP(recorder, authedRequest(http.MethodGet, "/api/versions/1001/design/cases/7001", nil))
	data = decodeEnvelope(t, recorder)
	if data["review_state"] != "judge_unavailable" {
		t.Fatalf("case detail review_state = %v, want judge_unavailable", data["review_state"])
	}
}
