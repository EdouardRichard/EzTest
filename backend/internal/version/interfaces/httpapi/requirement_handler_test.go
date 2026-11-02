package versionapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

type stubRequirementService struct {
	detail    versionapp.RequirementDetail
	getQueue  []versionapp.RequirementDetail
	list      []versionapp.RequirementView
	hasMore   bool
	confirm   versionapp.ConfirmResult
	batch     versionapp.BatchConfirmResult
	relations []versionapp.RelationView
	err       error

	lastVersionID int64
	lastRequestID int64
	lastActor     int64
	lastDraft     versionapp.RequirementDraft
	lastReason    string
	lastProposal  *int64
	lastStatus    string
	lastLimit     int
	lastBefore    *int64
	lastBatchIDs  []int64
	lastRelation  int64
	calls         []string
}

func (s *stubRequirementService) Create(_ context.Context, versionID int64, in versionapp.RequirementDraft, actorID int64) (versionapp.RequirementDetail, error) {
	s.calls = append(s.calls, "create")
	s.lastVersionID = versionID
	s.lastDraft = in
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.RequirementDetail{}, s.err
	}
	return s.detail, nil
}

func (s *stubRequirementService) Update(_ context.Context, versionID, requirementID int64, in versionapp.RequirementDraft, reason string, actorID int64) (versionapp.RequirementDetail, error) {
	s.calls = append(s.calls, "update")
	s.lastVersionID = versionID
	s.lastRequestID = requirementID
	s.lastDraft = in
	s.lastReason = reason
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.RequirementDetail{}, s.err
	}
	return s.detail, nil
}

func (s *stubRequirementService) Get(_ context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error) {
	s.calls = append(s.calls, "get")
	s.lastVersionID = versionID
	s.lastRequestID = requirementID
	if s.err != nil {
		return versionapp.RequirementDetail{}, s.err
	}
	if len(s.getQueue) > 0 {
		detail := s.getQueue[0]
		s.getQueue = s.getQueue[1:]
		return detail, nil
	}
	return s.detail, nil
}

func (s *stubRequirementService) List(_ context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error) {
	s.calls = append(s.calls, "list")
	s.lastVersionID = versionID
	s.lastStatus = status
	s.lastLimit = limit
	s.lastBefore = beforeID
	if s.err != nil {
		return nil, false, s.err
	}
	return s.list, s.hasMore, nil
}

func (s *stubRequirementService) Confirm(_ context.Context, versionID, requirementID, actorID int64) (versionapp.ConfirmResult, error) {
	s.calls = append(s.calls, "confirm")
	s.lastVersionID = versionID
	s.lastRequestID = requirementID
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.ConfirmResult{}, s.err
	}
	return s.confirm, nil
}

func (s *stubRequirementService) BatchConfirm(_ context.Context, versionID int64, requirementIDs []int64, actorID int64) (versionapp.BatchConfirmResult, error) {
	s.calls = append(s.calls, "batch")
	s.lastVersionID = versionID
	s.lastBatchIDs = requirementIDs
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.BatchConfirmResult{}, s.err
	}
	return s.batch, nil
}

func (s *stubRequirementService) Rollback(_ context.Context, versionID, requirementID int64, reason string, proposalMessageID *int64, actorID int64) (versionapp.RequirementDetail, error) {
	s.calls = append(s.calls, "rollback")
	s.lastVersionID = versionID
	s.lastRequestID = requirementID
	s.lastReason = reason
	s.lastProposal = proposalMessageID
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.RequirementDetail{}, s.err
	}
	return s.detail, nil
}

func (s *stubRequirementService) ListRelations(_ context.Context, versionID, requirementID int64) ([]versionapp.RelationView, error) {
	s.calls = append(s.calls, "relations")
	s.lastVersionID = versionID
	s.lastRelation = requirementID
	if s.err != nil {
		return nil, s.err
	}
	return s.relations, nil
}

var _ versionapi.RequirementService = (*stubRequirementService)(nil)

func newRequirementServer(t *testing.T) (http.Handler, *stubRequirementService) {
	t.Helper()
	mux, stub, _ := newRequirementServerWithClosures(t)
	return mux, stub
}

func newRequirementServerWithClosures(t *testing.T) (http.Handler, *stubRequirementService, *stubRequirementClosureReader) {
	t.Helper()
	stub := &stubRequirementService{}
	closures := &stubRequirementClosureReader{closures: map[int64]versionapp.RequirementClosureView{}}
	handler := versionapi.NewRequirementHandler(stub, versionapi.WithRequirementClosures(closures))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/requirements", handler.List)
	mux.HandleFunc("POST /api/versions/{id}/requirements", handler.Create)
	mux.HandleFunc("POST /api/versions/{id}/requirements/confirmations", handler.BatchConfirm)
	mux.HandleFunc("GET /api/versions/{id}/requirements/{rid}", handler.Get)
	mux.HandleFunc("PATCH /api/versions/{id}/requirements/{rid}", handler.Update)
	mux.HandleFunc("POST /api/versions/{id}/requirements/{rid}/confirmations", handler.Confirm)
	mux.HandleFunc("POST /api/versions/{id}/requirements/{rid}/rollback", handler.Rollback)
	mux.HandleFunc("GET /api/versions/{id}/requirements/{rid}/relations", handler.ListRelations)
	return mux, stub, closures
}

func sampleRequirementVersionView() versionapp.RequirementVersionView {
	return versionapp.RequirementVersionView{
		ID: 9002, RequirementID: 9001, Revision: 2,
		Title: "Checkout coupons", Description: "desc", AcceptanceCriteria: "ac",
		Priority: "P1", Constraints: json.RawMessage(`{"business_rules":[]}`),
		TargetVersionSUTs: []string{"5001"}, TargetPages: []string{"6001"}, TargetAPIs: []string{"7001"},
		ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt:   testTime,
	}
}

func sampleRequirementView() versionapp.RequirementView {
	return versionapp.RequirementView{
		ID: 9001, CurrentVersionID: 9002, Revision: 2, Title: "Checkout coupons",
		Status: "clarified", Priority: "P1",
		TargetVersionSUTs: []string{"5001"}, TargetPages: []string{"6001"}, TargetAPIs: []string{"7001"},
		UpdatedAt: testTime, ConfirmedAt: &testTime,
	}
}

func sampleRequirementDetail() versionapp.RequirementDetail {
	return versionapp.RequirementDetail{
		RequirementView: sampleRequirementView(),
		CurrentVersion:  sampleRequirementVersionView(),
		History: []versionapp.RequirementVersionView{{
			ID: 9000, RequirementID: 9001, Revision: 1, Title: "Old title", Priority: "P2",
			Constraints: json.RawMessage(`{}`), TargetVersionSUTs: []string{}, TargetPages: []string{}, TargetAPIs: []string{},
			ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CreatedAt: testTime,
		}},
		Confirmations: []versionapp.ConfirmationView{{
			ID: 9100, RequirementVersionID: 9002, Scope: "requirement", ActorID: 42,
			ConfirmedAt: testTime, AffectedRequirementIDs: []string{},
		}},
		Relations: []versionapp.RelationView{{
			ID: 9200, FromRequirementID: 9001, ToRequirementID: 9003, Kind: "depends_on",
			Description: "needs coupons", CreatedAt: testTime,
		}},
		Attachments: []versionapp.AttachmentView{{
			ID: 9300, Name: "spec.md", MIME: "text/markdown", Size: 12,
			ExtractionStatus: "failed", ExtractionErrorCode: "unsupported_attachment_format",
			ExtractionErrorText: "unsupported", CreatedAt: testTime,
		}},
	}
}

func TestCreateRequirementParsesTargetsAndReturnsDetailShape(t *testing.T) {
	handler, stub := newRequirementServer(t)
	stub.detail = sampleRequirementDetail()

	body := `{
		"title": "Checkout coupons",
		"description": "desc",
		"acceptance_criteria": "ac",
		"priority": "P1",
		"constraints": {"business_rules": ["one per order"]},
		"targets": {"version_suts": ["5001"], "pages": ["6001"], "apis": ["7001"]}
	}`
	recorder := call(t, handler, http.MethodPost, "/api/versions/101/requirements", body, actor())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastActor != 42 {
		t.Fatalf("args = %d/%d", stub.lastVersionID, stub.lastActor)
	}
	if stub.lastDraft.Title != "Checkout coupons" || stub.lastDraft.Priority != "P1" {
		t.Fatalf("draft = %#v", stub.lastDraft)
	}
	if len(stub.lastDraft.TargetVersionSUTs) != 1 || stub.lastDraft.TargetVersionSUTs[0] != "5001" ||
		stub.lastDraft.TargetPages[0] != "6001" || stub.lastDraft.TargetAPIs[0] != "7001" {
		t.Fatalf("targets = %#v", stub.lastDraft)
	}
	if string(stub.lastDraft.Constraints) != `{"business_rules": ["one per order"]}` {
		t.Fatalf("constraints = %s", stub.lastDraft.Constraints)
	}

	data := dataBody(t, recorder)
	if data["id"] != "9001" || data["current_version_id"] != "9002" || data["revision"] != float64(2) {
		t.Fatalf("data = %#v", data)
	}
	if data["status"] != "clarified" || data["priority"] != "P1" || data["title"] != "Checkout coupons" {
		t.Fatalf("data = %#v", data)
	}
	if _, hasVersionID := data["version_id"]; hasVersionID {
		t.Fatalf("summary must not expose version_id: %#v", data)
	}
	if data["confirmed_at"] != "2026-10-31T12:00:00Z" || data["updated_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("times = %#v/%#v", data["confirmed_at"], data["updated_at"])
	}
	targets := data["targets"].(map[string]any)
	if targets["version_suts"].([]any)[0] != "5001" || targets["pages"].([]any)[0] != "6001" || targets["apis"].([]any)[0] != "7001" {
		t.Fatalf("targets = %#v", targets)
	}

	current := data["current_version"].(map[string]any)
	if current["id"] != "9002" || current["requirement_id"] != "9001" || current["content_hash"] == "" {
		t.Fatalf("current_version = %#v", current)
	}
	if _, ok := current["created_by"]; ok {
		t.Fatalf("current_version must not expose internal audit ids: %#v", current)
	}
	history := data["history"].([]any)
	if len(history) != 1 || history[0].(map[string]any)["revision"] != float64(1) {
		t.Fatalf("history = %#v", history)
	}
	confirmations := data["confirmations"].([]any)
	if len(confirmations) != 1 || confirmations[0].(map[string]any)["requirement_version_id"] != "9002" {
		t.Fatalf("confirmations = %#v", confirmations)
	}
	relations := data["relations"].([]any)
	if len(relations) != 1 || relations[0].(map[string]any)["from_requirement_id"] != "9001" {
		t.Fatalf("relations = %#v", relations)
	}
	if _, ok := relations[0].(map[string]any)["version_id"]; ok {
		t.Fatalf("relation must not expose version_id: %#v", relations[0])
	}
	attachments := data["attachments"].([]any)
	attachment := attachments[0].(map[string]any)
	if attachment["name"] != "spec.md" || attachment["extraction_error"].(map[string]any)["code"] != "unsupported_attachment_format" {
		t.Fatalf("attachment = %#v", attachment)
	}
}

func TestRequirementListParsesFiltersAndReturnsSummaryPage(t *testing.T) {
	handler, stub := newRequirementServer(t)
	stub.list = []versionapp.RequirementView{sampleRequirementView()}
	stub.hasMore = true

	recorder := call(t, handler, http.MethodGet, "/api/versions/101/requirements?status=draft&limit=5&before=99", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastStatus != "draft" || stub.lastLimit != 5 {
		t.Fatalf("args = %#v", stub)
	}
	if stub.lastBefore == nil || *stub.lastBefore != 99 {
		t.Fatalf("before = %#v", stub.lastBefore)
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0].(map[string]any)
	if item["id"] != "9001" || item["current_version_id"] != "9002" || item["targets"].(map[string]any)["pages"].([]any)[0] != "6001" {
		t.Fatalf("item = %#v", item)
	}
	payload := decodeBody(t, recorder)
	if payload["next_cursor"] != "9001" || payload["has_more"] != true {
		t.Fatalf("envelope = %#v", payload)
	}

	stub.lastLimit = 0
	stub.lastBefore = nil
	stub.list = nil
	stub.hasMore = false
	empty := call(t, handler, http.MethodGet, "/api/versions/101/requirements", "", actor())
	if empty.Code != http.StatusOK {
		t.Fatalf("empty status = %d", empty.Code)
	}
	if stub.lastLimit != 20 {
		t.Fatalf("default limit = %d", stub.lastLimit)
	}
	if payload := decodeBody(t, empty); payload["next_cursor"] != "" || payload["has_more"] != false {
		t.Fatalf("empty envelope = %#v", payload)
	}
}

func TestRequirementGetUpdateRollbackAndRelations(t *testing.T) {
	handler, stub := newRequirementServer(t)
	stub.detail = sampleRequirementDetail()
	stub.relations = []versionapp.RelationView{{ID: 9200, FromRequirementID: 9001, ToRequirementID: 9003, Kind: "overlaps", CreatedAt: testTime}}

	get := call(t, handler, http.MethodGet, "/api/versions/101/requirements/9001", "", actor())
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", get.Code, get.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastRequestID != 9001 {
		t.Fatalf("get args = %d/%d", stub.lastVersionID, stub.lastRequestID)
	}

	updateBody := `{"title":"New","description":"d","acceptance_criteria":"a","priority":"P0","constraints":{},"targets":{"version_suts":[],"pages":[],"apis":[]},"reason":"clarified scope"}`
	patched := call(t, handler, http.MethodPatch, "/api/versions/101/requirements/9001", updateBody, actor())
	if patched.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patched.Code, patched.Body.String())
	}
	if stub.lastReason != "clarified scope" || stub.lastDraft.Title != "New" || stub.lastDraft.Priority != "P0" {
		t.Fatalf("update = %#v", stub)
	}

	rollback := call(t, handler, http.MethodPost, "/api/versions/101/requirements/9001/rollback", `{"reason":"proposal","proposal_message_id":"777"}`, actor())
	if rollback.Code != http.StatusOK {
		t.Fatalf("rollback status = %d, body = %s", rollback.Code, rollback.Body.String())
	}
	if stub.lastReason != "proposal" || stub.lastProposal == nil || *stub.lastProposal != 777 {
		t.Fatalf("rollback args = %#v", stub)
	}

	emptyRollback := call(t, handler, http.MethodPost, "/api/versions/101/requirements/9001/rollback", "", actor())
	if emptyRollback.Code != http.StatusOK {
		t.Fatalf("empty rollback status = %d, body = %s", emptyRollback.Code, emptyRollback.Body.String())
	}
	if stub.lastProposal != nil || stub.lastReason != "" {
		t.Fatalf("empty rollback args = %#v", stub)
	}

	relations := call(t, handler, http.MethodGet, "/api/versions/101/requirements/9001/relations", "", actor())
	if relations.Code != http.StatusOK {
		t.Fatalf("relations status = %d", relations.Code)
	}
	if stub.lastVersionID != 101 || stub.lastRelation != 9001 {
		t.Fatalf("relations args = %d/%d", stub.lastVersionID, stub.lastRelation)
	}
	items := dataList(t, relations)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "overlaps" {
		t.Fatalf("relations = %#v", items)
	}
	if payload := decodeBody(t, relations); payload["next_cursor"] != "" || payload["has_more"] != false {
		t.Fatalf("relations envelope = %#v", payload)
	}
}

func TestRequirementConfirmAndBatchShapes(t *testing.T) {
	handler, stub := newRequirementServer(t)
	stub.confirm = versionapp.ConfirmResult{Confirmed: true, Requirement: sampleRequirementDetail()}
	stub.batch = versionapp.BatchConfirmResult{BatchID: "b-1", Confirmed: []int64{1, 2}, Skipped: []int64{3}}

	single := call(t, handler, http.MethodPost, "/api/versions/101/requirements/9001/confirmations", "", actor())
	if single.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", single.Code, single.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastRequestID != 9001 || stub.lastActor != 42 {
		t.Fatalf("confirm args = %#v", stub)
	}
	data := dataBody(t, single)
	if data["confirmed"] != true || data["requirement"].(map[string]any)["id"] != "9001" {
		t.Fatalf("confirm data = %#v", data)
	}

	batch := call(t, handler, http.MethodPost, "/api/versions/101/requirements/confirmations", `{"requirement_ids":["1","2","3"]}`, actor())
	if batch.Code != http.StatusOK {
		t.Fatalf("batch status = %d, body = %s", batch.Code, batch.Body.String())
	}
	if len(stub.lastBatchIDs) != 3 || stub.lastBatchIDs[0] != 1 || stub.lastBatchIDs[2] != 3 {
		t.Fatalf("batch ids = %#v", stub.lastBatchIDs)
	}
	batchData := dataBody(t, batch)
	if batchData["batch_id"] != "b-1" {
		t.Fatalf("batch data = %#v", batchData)
	}
	confirmed := batchData["confirmed"].([]any)
	skipped := batchData["skipped"].([]any)
	if confirmed[0] != "1" || confirmed[1] != "2" || skipped[0] != "3" {
		t.Fatalf("batch result = %#v", batchData)
	}

	emptyBatch := call(t, handler, http.MethodPost, "/api/versions/101/requirements/confirmations", `{"requirement_ids":[]}`, actor())
	if emptyBatch.Code != http.StatusOK {
		t.Fatalf("empty batch status = %d", emptyBatch.Code)
	}
	if len(stub.lastBatchIDs) != 0 {
		t.Fatalf("empty batch ids = %#v", stub.lastBatchIDs)
	}
}

func TestRequirementHandlerValidationAndServiceErrors(t *testing.T) {
	handler, stub := newRequirementServer(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		actor  *platform.Actor
		want   int
		field  string
	}{
		{"malformed create", http.MethodPost, "/api/versions/101/requirements", `{"title":`, actor(), 400, "body"},
		{"bad ids", http.MethodGet, "/api/versions/abc/requirements", "", actor(), 400, "id"},
		{"bad rid", http.MethodGet, "/api/versions/101/requirements/xyz", "", actor(), 400, "rid"},
		{"bad batch id", http.MethodPost, "/api/versions/101/requirements/confirmations", `{"requirement_ids":["nope"]}`, actor(), 400, "requirement_ids[0]"},
		{"bad proposal id", http.MethodPost, "/api/versions/101/requirements/9001/rollback", `{"proposal_message_id":"nope"}`, actor(), 400, "proposal_message_id"},
		{"bad before", http.MethodGet, "/api/versions/101/requirements?before=abc", "", actor(), 400, "before"},
		{"missing actor create", http.MethodPost, "/api/versions/101/requirements", `{"title":"T"}`, nil, 401, ""},
		{"missing actor update", http.MethodPatch, "/api/versions/101/requirements/9001", `{}`, nil, 401, ""},
		{"missing actor confirm", http.MethodPost, "/api/versions/101/requirements/9001/confirmations", "", nil, 401, ""},
		{"missing actor batch", http.MethodPost, "/api/versions/101/requirements/confirmations", `{"requirement_ids":["1"]}`, nil, 401, ""},
		{"missing actor rollback", http.MethodPost, "/api/versions/101/requirements/9001/rollback", "", nil, 401, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := call(t, handler, tc.method, tc.path, tc.body, tc.actor)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.field != "" {
				if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != tc.field {
					t.Fatalf("field = %#v, want %q", field, tc.field)
				}
			}
		})
	}

	stub.err = platform.NotFound("requirement")
	if recorder := call(t, handler, http.MethodGet, "/api/versions/101/requirements/9001", "", actor()); recorder.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d", recorder.Code)
	}
	stub.err = platform.StateConflict("frozen")
	if recorder := call(t, handler, http.MethodPatch, "/api/versions/101/requirements/9001", `{"title":"T"}`, actor()); recorder.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d", recorder.Code)
	}
}

type requirementReopenCall struct {
	requirementID int64
	reason        string
}

type stubRequirementReopener struct {
	err   error
	calls []requirementReopenCall
}

func (s *stubRequirementReopener) Reopen(_ context.Context, requirementID int64, reason string) error {
	s.calls = append(s.calls, requirementReopenCall{requirementID: requirementID, reason: reason})
	return s.err
}

var _ versionapi.RequirementReopener = (*stubRequirementReopener)(nil)

type stubRequirementClosureReader struct {
	closures map[int64]versionapp.RequirementClosureView
	err      error
	calls    []int64
}

func (s *stubRequirementClosureReader) LatestClosure(_ context.Context, requirementID int64) (versionapp.RequirementClosureView, bool, error) {
	s.calls = append(s.calls, requirementID)
	if s.err != nil {
		return versionapp.RequirementClosureView{}, false, s.err
	}
	closure, found := s.closures[requirementID]
	return closure, found, nil
}

var _ versionapi.RequirementClosureReader = (*stubRequirementClosureReader)(nil)

func newRequirementReopenServer(t *testing.T) (http.Handler, *stubRequirementService, *stubRequirementReopener, *stubRequirementClosureReader) {
	t.Helper()
	stub := &stubRequirementService{}
	reopen := &stubRequirementReopener{}
	closures := &stubRequirementClosureReader{closures: map[int64]versionapp.RequirementClosureView{}}
	handler := versionapi.NewRequirementHandler(
		stub,
		versionapi.WithRequirementReopener(reopen),
		versionapi.WithRequirementClosures(closures),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/versions/{versionId}/requirements/{requirementId}/reopen", handler.Reopen)
	return mux, stub, reopen, closures
}

func TestRequirementReopenReturnsDraftWithClosure(t *testing.T) {
	handler, stub, reopen, closures := newRequirementReopenServer(t)
	closedAt := testTime
	closed := sampleRequirementDetail()
	closed.Status = "closed"
	closed.ClosedAt = &closedAt
	draft := sampleRequirementDetail()
	draft.Status = "draft"
	draft.ClosedAt = nil
	stub.getQueue = []versionapp.RequirementDetail{closed, draft}
	closures.closures[9001] = versionapp.RequirementClosureView{
		ID: 3001, CloseRound: 1, ClosedAt: testTime, ReopenCount: 2,
	}

	recorder := call(t, handler, http.MethodPost, "/api/versions/101/requirements/9001/reopen",
		`{"reason":"reopen after review"}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(reopen.calls) != 1 {
		t.Fatalf("reopen calls = %#v", reopen.calls)
	}
	reopenCall := reopen.calls[0]
	if reopenCall.requirementID != 9001 || reopenCall.reason != "reopen after review" {
		t.Fatalf("reopen call = %#v", reopenCall)
	}
	if len(stub.calls) != 2 || stub.lastVersionID != 101 || stub.lastRequestID != 9001 {
		t.Fatalf("service calls = %#v", stub.calls)
	}
	data := dataBody(t, recorder)
	if data["id"] != "9001" || data["status"] != "draft" {
		t.Fatalf("data = %#v", data)
	}
	if data["closed_at"] != nil {
		t.Fatalf("reopened requirement keeps closed_at: %#v", data)
	}
	closure, ok := data["closure"].(map[string]any)
	if !ok {
		t.Fatalf("closure = %#v", data["closure"])
	}
	if closure["closure_id"] != "3001" || closure["close_round"] != float64(1) ||
		closure["closed_at"] != "2026-10-31T12:00:00Z" || closure["reopen_count"] != float64(2) {
		t.Fatalf("closure = %#v", closure)
	}
	if _, ok := closure["checks"]; ok {
		t.Fatalf("closure exposes internal checks: %#v", closure)
	}
}

func TestRequirementReopenRejectsNonClosedRequirement(t *testing.T) {
	handler, stub, reopen, _ := newRequirementReopenServer(t)
	stub.detail = sampleRequirementDetail()

	recorder := call(t, handler, http.MethodPost, "/api/versions/101/requirements/9001/reopen",
		`{"reason":"reopen"}`, actor())
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if code := errorBody(t, recorder)["code"]; code != "state_conflict" {
		t.Fatalf("error = %#v", errorBody(t, recorder))
	}
	if len(reopen.calls) != 0 {
		t.Fatalf("reopen called for a non-closed requirement: %#v", reopen.calls)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("service calls = %#v", stub.calls)
	}
}

func TestRequirementReopenValidatesInputAndMapsErrors(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		body  string
		actor *platform.Actor
		want  int
		field string
	}{
		{"missing reason", "/api/versions/101/requirements/9001/reopen", `{}`, actor(), 400, "reason"},
		{"blank reason", "/api/versions/101/requirements/9001/reopen", `{"reason":"  "}`, actor(), 400, "reason"},
		{"malformed body", "/api/versions/101/requirements/9001/reopen", `{"reason":`, actor(), 400, "body"},
		{"invalid version id", "/api/versions/abc/requirements/9001/reopen", `{"reason":"x"}`, actor(), 400, "versionId"},
		{"invalid requirement id", "/api/versions/101/requirements/abc/reopen", `{"reason":"x"}`, actor(), 400, "requirementId"},
		{"missing actor", "/api/versions/101/requirements/9001/reopen", `{"reason":"x"}`, nil, 401, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, stub, reopen, _ := newRequirementReopenServer(t)
			recorder := call(t, handler, http.MethodPost, tc.path, tc.body, tc.actor)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.field != "" {
				if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != tc.field {
					t.Fatalf("field = %#v, want %q", field, tc.field)
				}
			}
			if len(stub.calls) != 0 || len(reopen.calls) != 0 {
				t.Fatalf("service called for invalid input: stub=%#v reopen=%#v", stub.calls, reopen.calls)
			}
		})
	}

	missingServer, missingStub, missingReopen, _ := newRequirementReopenServer(t)
	missingStub.err = platform.NotFound("requirement")
	if recorder := call(t, missingServer, http.MethodPost, "/api/versions/101/requirements/999/reopen", `{"reason":"x"}`, actor()); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(missingReopen.calls) != 0 {
		t.Fatalf("reopen called for a missing requirement: %#v", missingReopen.calls)
	}

	conflictServer, conflictStub, conflictReopen, _ := newRequirementReopenServer(t)
	closedAt := testTime
	closed := sampleRequirementDetail()
	closed.Status = "closed"
	closed.ClosedAt = &closedAt
	conflictStub.detail = closed
	conflictReopen.err = platform.StateConflict("requirement is not closed")
	if recorder := call(t, conflictServer, http.MethodPost, "/api/versions/101/requirements/9001/reopen", `{"reason":"x"}`, actor()); recorder.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRequirementViewsExposeCloseProjection(t *testing.T) {
	handler, stub, closures := newRequirementServerWithClosures(t)
	closedAt := testTime
	closed := sampleRequirementView()
	closed.Status = "closed"
	closed.ClosedAt = &closedAt
	stub.list = []versionapp.RequirementView{closed}
	stub.detail = versionapp.RequirementDetail{RequirementView: closed}
	closures.closures[9001] = versionapp.RequirementClosureView{
		ID: 3001, CloseRound: 2, ClosedAt: testTime, ReopenCount: 1,
	}

	list := call(t, handler, http.MethodGet, "/api/versions/101/requirements", "", actor())
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	items := dataList(t, list)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0].(map[string]any)
	if item["status"] != "closed" || item["closed_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("list item = %#v", item)
	}
	listClosure := item["closure"].(map[string]any)
	if listClosure["closure_id"] != "3001" || listClosure["close_round"] != float64(2) ||
		listClosure["reopen_count"] != float64(1) {
		t.Fatalf("list closure = %#v", listClosure)
	}

	get := call(t, handler, http.MethodGet, "/api/versions/101/requirements/9001", "", actor())
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", get.Code, get.Body.String())
	}
	data := dataBody(t, get)
	if data["closed_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("detail closed_at = %#v", data["closed_at"])
	}
	detailClosure := data["closure"].(map[string]any)
	if detailClosure["closure_id"] != "3001" || detailClosure["close_round"] != float64(2) {
		t.Fatalf("detail closure = %#v", detailClosure)
	}
}

func TestVersionDetailIncludesP1Counts(t *testing.T) {
	stub := &stubService{}
	view := sampleView()
	view.RequirementCounts = version.RequirementCounts{Total: 5, Draft: 2, Clarified: 3}
	view.ConversationCount = 4
	view.PendingDesignCount = 3
	stub.view = view
	handler := versionapi.New(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}", handler.Get)

	recorder := call(t, mux, http.MethodGet, "/api/versions/101", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := dataBody(t, recorder)
	counts := data["requirement_counts"].(map[string]any)
	if counts["total"] != float64(5) || counts["draft"] != float64(2) || counts["clarified"] != float64(3) ||
		counts["closed"] != float64(0) || counts["obsolete"] != float64(0) {
		t.Fatalf("counts = %#v", counts)
	}
	if data["conversation_count"] != float64(4) || data["pending_design_count"] != float64(3) {
		t.Fatalf("counts = %#v", data)
	}
}

var _ = time.Time{}
