package versionapi_test

// Task 27 (v0.22 B §8.1/spec R-B5-1): GET /api/versions/{id}/handoff.
//
// The handler only parses the path/query and renders the handoff document. The
// six blocks, revision and generated_at are owned by versionapp.HandoffService;
// route registration is wired by the server task (Task 37).

import (
	"context"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

type fakeHandoffReadService struct {
	document  versionapp.Handoff
	err       error
	calls     int
	versionID int64
	revision  int
}

func (f *fakeHandoffReadService) Generate(_ context.Context, _ int64) (versionapp.Handoff, error) {
	return versionapp.Handoff{}, nil
}

func (f *fakeHandoffReadService) Latest(_ context.Context, versionID int64, revision int) (versionapp.Handoff, error) {
	f.calls++
	f.versionID = versionID
	f.revision = revision
	if f.err != nil {
		return versionapp.Handoff{}, f.err
	}
	return f.document, nil
}

var _ versionapi.HandoffService = (*fakeHandoffReadService)(nil)

func newHandoffHandlerTestServer(t *testing.T, svc versionapi.HandoffService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/handoff", versionapi.NewHandoffHandler(svc).Get)
	return mux
}

func handoffReadDocument() versionapp.Handoff {
	return versionapp.Handoff{
		VersionID:   7,
		Revision:    2,
		GeneratedAt: testTime,
		Requirements: []versionapp.HandoffRequirement{{
			ID: 101, CurrentVersionID: 201, Revision: 1,
			Title: "退款到账时效", Description: "退款后约定时效到账",
			AcceptanceCriteria: "T+1 到账", Priority: "P1", Status: "clarified",
			SourceQuote: "退款到账时间按 T+1 验收",
		}},
		QA: []versionapp.HandoffQAItem{{
			RequestID: 501, MessageID: 9001, QuestionKey: "q1",
			Question: "T+1 还是 T+3？", Status: "answered", AskedAt: testTime,
			AnswerSelected: []string{"a"},
		}},
		Decisions: []versionapp.HandoffDecision{{
			FactID: 601, Kind: "decision", Key: "refund.sla",
			Summary: "退款 SLA 采用 T+1", CreatedAt: testTime,
		}},
		Open: []versionapp.HandoffOpenItem{{
			Kind: "question", Summary: "是否需要覆盖退款失败场景？", Status: "pending",
			Source: "human_request:502", ReferenceID: 502,
		}},
		Plan: versionapp.HandoffPlan{
			Stage:   "designing",
			Current: &versionapp.HandoffPlanItem{ID: 701, Phase: "designing", Title: "输出退款用例设计", Status: "in_progress"},
			Next:    &versionapp.HandoffPlanItem{ID: 702, Phase: "designing", Title: "评审退款用例", Status: "pending"},
		},
		Resources: versionapp.HandoffResources{
			SUTs: []versionapp.HandoffSUT{{
				VersionSUTID: 801, SUTID: 901, Name: "订单中心",
				EnvironmentID: 1001, Environment: "staging", BaseURL: "https://orders.example.test",
			}},
			Pages:    []versionapp.HandoffPage{{ID: 1101, VersionSUTID: 801, Name: "退款页", URL: "https://orders.example.test/refund"}},
			APIs:     []versionapp.HandoffAPI{{ID: 1201, VersionSUTID: 801, Name: "创建退款", Method: "POST", Path: "/api/refunds"}},
			Accounts: []versionapp.HandoffAccount{{ID: 1301, Username: "qa-refund", Roles: []string{"admin"}}},
			Databases: []versionapp.HandoffDatabase{
				{ID: 1401, Name: "orders", Kind: "postgres", ReadOnly: true},
			},
		},
	}
}

// TestHandoffHandlerGet pins the 200 contract: the document carries the
// version/revision/generated_at and the six blocks, with published ID strings.
func TestHandoffHandlerGet(t *testing.T) {
	svc := &fakeHandoffReadService{document: handoffReadDocument()}
	recorder := call(t, newHandoffHandlerTestServer(t, svc), http.MethodGet, "/api/versions/7/handoff", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.calls != 1 || svc.versionID != 7 || svc.revision != 0 {
		t.Fatalf("service call = %+v, want version 7 latest", svc)
	}
	data := dataBody(t, recorder)
	if data["version_id"] != "7" || data["revision"] != float64(2) || data["generated_at"] == "" {
		t.Fatalf("handoff header = %#v", data)
	}
	for _, block := range []string{"requirements", "qa", "decisions", "open"} {
		if _, ok := data[block].([]any); !ok {
			t.Fatalf("%s block = %#v, want a JSON array", block, data[block])
		}
	}
	plan, ok := data["plan"].(map[string]any)
	if !ok || plan["stage"] != "designing" {
		t.Fatalf("plan block = %#v", data["plan"])
	}
	requirement := data["requirements"].([]any)[0].(map[string]any)
	if requirement["id"] != "101" || requirement["current_version_id"] != "201" ||
		requirement["acceptance_criteria"] != "T+1 到账" || requirement["source_quote"] == "" {
		t.Fatalf("requirement DTO = %#v", requirement)
	}
	qa := data["qa"].([]any)[0].(map[string]any)
	if qa["question_id"] != "501" || qa["message_id"] != "9001" || qa["status"] != "answered" {
		t.Fatalf("qa DTO = %#v", qa)
	}
	decision := data["decisions"].([]any)[0].(map[string]any)
	if decision["fact_id"] != "601" || decision["kind"] != "decision" {
		t.Fatalf("decision DTO = %#v", decision)
	}
	openItem := data["open"].([]any)[0].(map[string]any)
	if openItem["kind"] != "question" || openItem["source"] != "human_request:502" {
		t.Fatalf("open DTO = %#v", openItem)
	}
	resources := data["resources"].(map[string]any)
	if len(resources["pages"].([]any)) != 1 || len(resources["apis"].([]any)) != 1 ||
		len(resources["suts"].([]any)) != 1 || len(resources["accounts"].([]any)) != 1 ||
		len(resources["databases"].([]any)) != 1 {
		t.Fatalf("resources DTO = %#v", resources)
	}
}

// TestHandoffHandlerRevisionQuery pins ?revision= selection.
func TestHandoffHandlerRevisionQuery(t *testing.T) {
	svc := &fakeHandoffReadService{document: handoffReadDocument()}
	recorder := call(t, newHandoffHandlerTestServer(t, svc), http.MethodGet, "/api/versions/7/handoff?revision=2", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.revision != 2 {
		t.Fatalf("service revision = %d, want 2", svc.revision)
	}
}

// TestHandoffHandlerNotFound pins the 404 contract when no revision exists.
func TestHandoffHandlerNotFound(t *testing.T) {
	svc := &fakeHandoffReadService{err: platform.NotFound("clarification handoff")}
	recorder := call(t, newHandoffHandlerTestServer(t, svc), http.MethodGet, "/api/versions/7/handoff", "", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", recorder.Code, recorder.Body.String())
	}
	errorData := errorBody(t, recorder)
	if errorData["code"] != "not_found" {
		t.Fatalf("error = %#v", errorData)
	}
}

// TestHandoffHandlerInvalidRevision pins the input contract: an invalid revision
// is a 400 and the service is never called.
func TestHandoffHandlerInvalidRevision(t *testing.T) {
	for _, raw := range []string{"abc", "0", "-2"} {
		svc := &fakeHandoffReadService{document: handoffReadDocument()}
		recorder := call(t, newHandoffHandlerTestServer(t, svc), http.MethodGet,
			"/api/versions/7/handoff?revision="+raw, "", nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("revision %q status = %d, want 400, body = %s", raw, recorder.Code, recorder.Body.String())
		}
		if svc.calls != 0 {
			t.Fatalf("revision %q called the service %d times", raw, svc.calls)
		}
	}
}
