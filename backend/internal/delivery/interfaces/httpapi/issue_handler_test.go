package deliveryapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var deliveryTestTime = time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

func int64Ptr(value int64) *int64 { return &value }

func int32Ptr(value int32) *int32 { return &value }

/* --------------------------------- fakes ---------------------------------- */

type issueGetCall struct {
	versionID int64
	issueID   int64
}

type issueTransitionCall struct {
	action    string
	versionID int64
	issueID   int64
	actorID   int64
	reason    string
	expected  *int32
}

type issueMergeCall struct {
	versionID int64
	sourceID  int64
	targetID  int64
	actorID   int64
	reason    string
	expected  *int32
}

// fakeIssueService is an in-memory Service. It mimics the application service
// page contract (descending ID, exclusive before cursor, bounded limit) so the
// handler's limit+1/has_more logic is exercised against realistic data.
type fakeIssueService struct {
	listResult []delivery.Issue
	listErr    error
	listCalls  int
	versionID  int64
	filter     deliveryapp.IssueFilter

	details  map[int64]deliveryapp.IssueDetail
	getErr   error
	getCalls []issueGetCall

	actionIssue delivery.Issue
	confirmErr  error
	dismissErr  error
	reopenErr   error
	mergeErr    error

	confirmCalls []issueTransitionCall
	dismissCalls []issueTransitionCall
	reopenCalls  []issueTransitionCall
	mergeCalls   []issueMergeCall
}

func (f *fakeIssueService) List(_ context.Context, versionID int64, filter deliveryapp.IssueFilter) ([]delivery.Issue, error) {
	f.listCalls++
	f.versionID = versionID
	f.filter = filter
	if f.listErr != nil {
		return nil, f.listErr
	}
	items := make([]delivery.Issue, 0, len(f.listResult))
	for _, issue := range f.listResult {
		if filter.Before > 0 && issue.ID >= filter.Before {
			continue
		}
		if filter.Limit > 0 && len(items) >= filter.Limit {
			break
		}
		items = append(items, issue)
	}
	return items, nil
}

func (f *fakeIssueService) Get(_ context.Context, versionID, issueID int64) (deliveryapp.IssueDetail, error) {
	f.getCalls = append(f.getCalls, issueGetCall{versionID: versionID, issueID: issueID})
	if f.getErr != nil {
		return deliveryapp.IssueDetail{}, f.getErr
	}
	detail, ok := f.details[issueID]
	if !ok || (detail.Issue.VersionID != 0 && detail.Issue.VersionID != versionID) {
		return deliveryapp.IssueDetail{}, deliveryapp.ErrNotFound
	}
	return detail, nil
}

func (f *fakeIssueService) Confirm(_ context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	f.confirmCalls = append(f.confirmCalls, issueTransitionCall{
		action: "confirm", versionID: versionID, issueID: issueID, actorID: actorID, reason: reason, expected: expected,
	})
	return f.actionIssue, f.confirmErr
}

func (f *fakeIssueService) Dismiss(_ context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	f.dismissCalls = append(f.dismissCalls, issueTransitionCall{
		action: "dismiss", versionID: versionID, issueID: issueID, actorID: actorID, reason: reason, expected: expected,
	})
	return f.actionIssue, f.dismissErr
}

func (f *fakeIssueService) Reopen(_ context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	f.reopenCalls = append(f.reopenCalls, issueTransitionCall{
		action: "reopen", versionID: versionID, issueID: issueID, actorID: actorID, reason: reason, expected: expected,
	})
	return f.actionIssue, f.reopenErr
}

func (f *fakeIssueService) Merge(_ context.Context, versionID, sourceID, targetID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	f.mergeCalls = append(f.mergeCalls, issueMergeCall{
		versionID: versionID, sourceID: sourceID, targetID: targetID, actorID: actorID, reason: reason, expected: expected,
	})
	return f.actionIssue, f.mergeErr
}

var _ Service = (*fakeIssueService)(nil)

/* -------------------------------- fixtures -------------------------------- */

func sampleIssue(id int64, status delivery.IssueStatus) delivery.Issue {
	issue := delivery.Issue{
		ID:               id,
		VersionID:        42,
		Title:            "Checkout · step 2: api failure",
		Summary:          "expected 200, got 500",
		Status:           status,
		Category:         "api",
		Severity:         "major",
		DedupeKey:        strings.Repeat("a", 32),
		Fingerprint:      strings.Repeat("b", 64),
		Source:           delivery.IssueCandidateSource,
		ExpectedRevision: 3,
		ReopenCount:      1,
		CreatedAt:        deliveryTestTime,
		UpdatedAt:        deliveryTestTime.Add(time.Minute),
	}
	if status == delivery.IssueStatusMerged {
		issue.MergedIntoID = int64Ptr(2002)
		mergedAt := deliveryTestTime.Add(2 * time.Minute)
		issue.MergedAt = &mergedAt
	}
	return issue
}

func sampleIssueRefs(issueID int64) []delivery.IssueRef {
	return []delivery.IssueRef{
		{
			ID: 1, IssueID: issueID, Kind: delivery.IssueRefKindCaseRun, RefID: 900,
			VersionSUTID: int64Ptr(9), Role: delivery.IssueRefRoleSource,
			CreatedAt: deliveryTestTime, UpdatedAt: deliveryTestTime,
		},
		{
			ID: 2, IssueID: issueID, Kind: delivery.IssueRefKindEvidence, RefID: 55,
			Role: "evidence", CreatedAt: deliveryTestTime, UpdatedAt: deliveryTestTime,
		},
	}
}

func sampleAnalysisRun() delivery.AnalysisRun {
	return delivery.AnalysisRun{
		ID:        77,
		VersionID: 42,
		Kind:      "triage",
		Status:    delivery.AnalysisStatusDone,
		Output:    json.RawMessage(`{"classification":"flaky","duplicate_of_issue_id":"2002","merge_reason":"same failure","confidence":0.8}`),
		CreatedAt: deliveryTestTime,
		UpdatedAt: deliveryTestTime,
	}
}

func sampleIssueHistory(issueID int64) []delivery.IssueStatusChange {
	from := delivery.IssueStatusConfirmed
	return []delivery.IssueStatusChange{
		{
			ID: 1, IssueID: issueID, From: nil, To: delivery.IssueStatusOpen,
			ActorKind: "system", CreatedAt: deliveryTestTime, UpdatedAt: deliveryTestTime,
		},
		{
			ID: 2, IssueID: issueID, From: &from, To: delivery.IssueStatusOpen,
			Reason: "reopened manually", ActorID: int64Ptr(7), ActorKind: "user",
			CreatedAt: deliveryTestTime.Add(time.Minute), UpdatedAt: deliveryTestTime.Add(time.Minute),
		},
	}
}

func sampleIssueDetail(issue delivery.Issue) deliveryapp.IssueDetail {
	run := sampleAnalysisRun()
	return deliveryapp.IssueDetail{
		Issue:   issue,
		Refs:    sampleIssueRefs(issue.ID),
		History: sampleIssueHistory(issue.ID),
		Triage:  &run,
	}
}

// issueFixture returns a service seeded with descending-ID issues. Every issue
// has a detail so list refs/triage enrichment succeeds.
func issueFixture(count int) (*fakeIssueService, []delivery.Issue) {
	issues := make([]delivery.Issue, 0, count)
	details := make(map[int64]deliveryapp.IssueDetail, count)
	for index := 0; index < count; index++ {
		issue := sampleIssue(int64(1011-index), delivery.IssueStatusOpen)
		issues = append(issues, issue)
		details[issue.ID] = sampleIssueDetail(issue)
	}
	return &fakeIssueService{listResult: issues, details: details}, issues
}

/* -------------------------------- helpers --------------------------------- */

func newDeliveryMux(t *testing.T, handler *Handler) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, handler)
	return mux
}

func deliveryCall(t *testing.T, handler http.Handler, method, target, body string, actor *platform.Actor) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if actor != nil {
		request = request.WithContext(platform.WithActor(request.Context(), *actor))
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func userActor() *platform.Actor { return &platform.Actor{ID: 7, Role: "user"} }

func decodeDeliveryObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	if envelope.Data == nil {
		t.Fatalf("response has no data object: %s", recorder.Body.String())
	}
	return envelope.Data
}

func decodeDeliveryList(t *testing.T, recorder *httptest.ResponseRecorder) ([]any, string, bool) {
	t.Helper()
	var envelope struct {
		Data       []any  `json:"data"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode list response %q: %v", recorder.Body.String(), err)
	}
	return envelope.Data, envelope.NextCursor, envelope.HasMore
}

func decodeDeliveryError(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response %q: %v", recorder.Body.String(), err)
	}
	if envelope.Error == nil {
		t.Fatalf("response has no error object: %s", recorder.Body.String())
	}
	return envelope.Error
}

func errorDetails(t *testing.T, apiError map[string]any) map[string]any {
	t.Helper()
	details, ok := apiError["details"].(map[string]any)
	if !ok {
		t.Fatalf("error details = %#v, want an object", apiError["details"])
	}
	return details
}

func requireStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, want, recorder.Body.String())
	}
}

/* ---------------------------------- tests --------------------------------- */

func TestIssueHandlerListParsesFiltersAndProjectsViews(t *testing.T) {
	service, issues := issueFixture(12)
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodGet,
		"/api/versions/42/issues?status=open&category=api&severity=major&version_sut_id=9&before=5000&limit=10",
		"", nil)
	requireStatus(t, recorder, http.StatusOK)

	if service.listCalls != 1 {
		t.Fatalf("List calls = %d, want 1", service.listCalls)
	}
	if service.versionID != 42 {
		t.Errorf("List version = %d, want 42", service.versionID)
	}
	wantFilter := deliveryapp.IssueFilter{
		Status:       "open",
		Category:     "api",
		Severity:     "major",
		VersionSUTID: int64Ptr(9),
		Before:       5000,
		Limit:        11, // requested limit+1 so has_more is exact
	}
	if service.filter.Status != wantFilter.Status ||
		service.filter.Category != wantFilter.Category ||
		service.filter.Severity != wantFilter.Severity ||
		service.filter.Before != wantFilter.Before ||
		service.filter.Limit != wantFilter.Limit ||
		service.filter.VersionSUTID == nil || *service.filter.VersionSUTID != 9 {
		t.Errorf("List filter = %+v, want %+v", service.filter, wantFilter)
	}

	items, nextCursor, hasMore := decodeDeliveryList(t, recorder)
	if len(items) != 10 {
		t.Fatalf("items = %d, want 10; body = %s", len(items), recorder.Body.String())
	}
	if !hasMore || nextCursor != "1002" {
		t.Errorf("pagination = (%q, %v), want (1002, true)", nextCursor, hasMore)
	}

	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first item is not an object: %#v", items[0])
	}
	expectedID := fmt.Sprintf("%d", issues[0].ID)
	if first["id"] != expectedID || first["version_id"] != "42" {
		t.Errorf("ids = %v/%v, want %s/42", first["id"], first["version_id"], expectedID)
	}
	if first["title"] != issues[0].Title || first["summary"] != issues[0].Summary {
		t.Errorf("text fields = %v / %v", first["title"], first["summary"])
	}
	if first["status"] != "open" || first["category"] != "api" || first["severity"] != "major" {
		t.Errorf("classification = %v/%v/%v", first["status"], first["category"], first["severity"])
	}
	if first["source"] != "failure_candidate" {
		t.Errorf("source = %v, want failure_candidate", first["source"])
	}
	if first["expected_revision"] != float64(3) || first["reopen_count"] != float64(1) {
		t.Errorf("revision/count = %v/%v", first["expected_revision"], first["reopen_count"])
	}
	if first["merged_into_id"] != nil || first["merged_at"] != nil {
		t.Errorf("unmerged issue must emit null merged fields: %v/%v", first["merged_into_id"], first["merged_at"])
	}
	if first["created_at"] != "2026-11-01T06:00:00Z" || first["updated_at"] != "2026-11-01T06:01:00Z" {
		t.Errorf("timestamps = %v/%v, want RFC3339 UTC", first["created_at"], first["updated_at"])
	}

	refs, ok := first["refs"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("refs = %#v, want 2", first["refs"])
	}
	ref, _ := refs[0].(map[string]any)
	if ref["kind"] != "case_run" || ref["ref_id"] != "900" || ref["version_sut_id"] != "9" || ref["role"] != "source" {
		t.Errorf("ref = %v", ref)
	}
	evidenceRef, _ := refs[1].(map[string]any)
	if evidenceRef["version_sut_id"] != nil {
		t.Errorf("evidence ref version_sut_id = %v, want null", evidenceRef["version_sut_id"])
	}

	triage, ok := first["triage"].(map[string]any)
	if !ok {
		t.Fatalf("triage = %#v, want an object", first["triage"])
	}
	if triage["analysis_run_id"] != "77" || triage["status"] != "done" {
		t.Errorf("triage identity = %v/%v", triage["analysis_run_id"], triage["status"])
	}
	if triage["classification"] != "flaky" || triage["duplicate_of_issue_id"] != "2002" {
		t.Errorf("triage proposal = %v/%v", triage["classification"], triage["duplicate_of_issue_id"])
	}
	if triage["summary"] != "same failure" {
		t.Errorf("triage summary = %v, want the merge reason fallback", triage["summary"])
	}
}

func TestIssueHandlerListWithoutMore(t *testing.T) {
	service, _ := issueFixture(3)
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/issues", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	if service.filter.Limit != 21 {
		t.Errorf("default limit = %d, want 21 (20+1)", service.filter.Limit)
	}
	items, nextCursor, hasMore := decodeDeliveryList(t, recorder)
	if len(items) != 3 || hasMore || nextCursor != "" {
		t.Errorf("pagination = (%d, %q, %v), want (3, \"\", false)", len(items), nextCursor, hasMore)
	}
}

func TestIssueHandlerListRejectsInvalidQueries(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "status", query: "?status=broken", field: "status"},
		{name: "category", query: "?category=unknown", field: "category"},
		{name: "severity", query: "?severity=urgent", field: "severity"},
		{name: "version_sut_id zero", query: "?version_sut_id=0", field: "version_sut_id"},
		{name: "version_sut_id text", query: "?version_sut_id=abc", field: "version_sut_id"},
		{name: "before zero", query: "?before=0", field: "before"},
		{name: "before text", query: "?before=soon", field: "before"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := issueFixture(1)
			mux := newDeliveryMux(t, New(service))
			recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/issues"+test.query, "", nil)
			requireStatus(t, recorder, http.StatusBadRequest)
			if service.listCalls != 0 {
				t.Errorf("List calls = %d, want none for an invalid query", service.listCalls)
			}
			details := errorDetails(t, decodeDeliveryError(t, recorder))
			if details["field"] != test.field {
				t.Errorf("field = %v, want %s", details["field"], test.field)
			}
		})
	}
}

func TestIssueHandlerListRejectsInvalidVersion(t *testing.T) {
	service, _ := issueFixture(1)
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/not-a-number/issues", "", nil)
	requireStatus(t, recorder, http.StatusBadRequest)
	if service.listCalls != 0 {
		t.Errorf("List calls = %d, want none", service.listCalls)
	}
}

func TestIssueHandlerGetProjectsDetail(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusConfirmed)
	service := &fakeIssueService{details: map[int64]deliveryapp.IssueDetail{1001: sampleIssueDetail(issue)}}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/issues/1001", "", nil)
	requireStatus(t, recorder, http.StatusOK)

	if len(service.getCalls) != 1 || service.getCalls[0].versionID != 42 || service.getCalls[0].issueID != 1001 {
		t.Fatalf("Get calls = %#v", service.getCalls)
	}
	data := decodeDeliveryObject(t, recorder)
	if data["id"] != "1001" || data["status"] != "confirmed" {
		t.Errorf("issue identity = %v/%v", data["id"], data["status"])
	}

	history, ok := data["history"].([]any)
	if !ok || len(history) != 2 {
		t.Fatalf("history = %#v, want 2 entries", data["history"])
	}
	first, _ := history[0].(map[string]any)
	if first["from_status"] != nil || first["to_status"] != "open" || first["actor_kind"] != "system" {
		t.Errorf("creation history = %v", first)
	}
	if first["request_id"] != nil {
		t.Errorf("history must not leak request id: %v", first)
	}
	second, _ := history[1].(map[string]any)
	if second["from_status"] != "confirmed" || second["to_status"] != "open" ||
		second["reason"] != "reopened manually" || second["actor_id"] != "7" {
		t.Errorf("reopen history = %v", second)
	}
	if _, ok := second["created_at"].(string); !ok {
		t.Errorf("history created_at = %v, want RFC3339 string", second["created_at"])
	}

	related, ok := data["related_refs"].(map[string]any)
	if !ok {
		t.Fatalf("related_refs = %#v, want an object", data["related_refs"])
	}
	if related["case_run"] != float64(1) || related["evidence"] != float64(1) {
		t.Errorf("related_refs = %v, want per-kind counts", related)
	}

	triage, ok := data["triage"].(map[string]any)
	if !ok || triage["analysis_run_id"] != "77" {
		t.Errorf("triage = %#v", data["triage"])
	}
}

func TestIssueHandlerGetWithoutTriage(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusOpen)
	detail := sampleIssueDetail(issue)
	detail.Triage = nil
	service := &fakeIssueService{details: map[int64]deliveryapp.IssueDetail{1001: detail}}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/issues/1001", "", nil)
	requireStatus(t, recorder, http.StatusOK)
	data := decodeDeliveryObject(t, recorder)
	if value, present := data["triage"]; !present || value != nil {
		t.Errorf("triage = %v (present=%v), want null", value, present)
	}
}

func TestIssueHandlerGetMapsErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: deliveryapp.ErrNotFound, status: http.StatusNotFound, code: "not_found"},
		{name: "invalid argument", err: fmt.Errorf("%w: issue id is invalid", deliveryapp.ErrInvalidArgument), status: http.StatusBadRequest, code: "invalid_argument"},
		{name: "unknown", err: errors.New("boom"), status: http.StatusInternalServerError, code: "internal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeIssueService{getErr: test.err}
			mux := newDeliveryMux(t, New(service))
			recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/42/issues/1001", "", nil)
			requireStatus(t, recorder, test.status)
			if code := decodeDeliveryError(t, recorder)["code"]; code != test.code {
				t.Errorf("error code = %v, want %s", code, test.code)
			}
		})
	}
}

func TestIssueHandlerGetRejectsWrongVersionAsNotFound(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusOpen)
	service := &fakeIssueService{details: map[int64]deliveryapp.IssueDetail{1001: sampleIssueDetail(issue)}}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodGet, "/api/versions/99/issues/1001", "", nil)
	requireStatus(t, recorder, http.StatusNotFound)
}

func TestIssueHandlerConfirmAndDismiss(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusConfirmed)
	service := &fakeIssueService{
		actionIssue: issue,
		details:     map[int64]deliveryapp.IssueDetail{1001: sampleIssueDetail(issue)},
	}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/confirm",
		`{"reason":"  looks bad  ","expected_revision":3}`, userActor())
	requireStatus(t, recorder, http.StatusOK)

	if len(service.confirmCalls) != 1 {
		t.Fatalf("Confirm calls = %#v", service.confirmCalls)
	}
	call := service.confirmCalls[0]
	if call.versionID != 42 || call.issueID != 1001 || call.actorID != 7 {
		t.Errorf("Confirm identity = %+v", call)
	}
	if call.reason != "looks bad" {
		t.Errorf("Confirm reason = %q, want trimmed", call.reason)
	}
	if call.expected == nil || *call.expected != 3 {
		t.Errorf("Confirm expected = %v, want 3", call.expected)
	}
	data := decodeDeliveryObject(t, recorder)
	if data["id"] != "1001" || data["status"] != "confirmed" {
		t.Errorf("Confirm response = %v", data)
	}
	if refs, ok := data["refs"].([]any); !ok || len(refs) != 2 {
		t.Errorf("Confirm response refs = %#v, want the projected refs", data["refs"])
	}

	recorder = deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/dismiss", `{}`, userActor())
	requireStatus(t, recorder, http.StatusOK)
	if len(service.dismissCalls) != 1 {
		t.Fatalf("Dismiss calls = %#v", service.dismissCalls)
	}
	if service.dismissCalls[0].reason != "" || service.dismissCalls[0].expected != nil {
		t.Errorf("Dismiss defaults = %+v", service.dismissCalls[0])
	}
}

func TestIssueHandlerConfirmBodyIsOptional(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusConfirmed)
	service := &fakeIssueService{
		actionIssue: issue,
		details:     map[int64]deliveryapp.IssueDetail{1001: sampleIssueDetail(issue)},
	}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/confirm", "", userActor())
	requireStatus(t, recorder, http.StatusOK)
	if len(service.confirmCalls) != 1 {
		t.Fatalf("Confirm calls = %d, want 1", len(service.confirmCalls))
	}
	if service.confirmCalls[0].reason != "" || service.confirmCalls[0].expected != nil {
		t.Errorf("defaults = %+v", service.confirmCalls[0])
	}
}

func TestIssueHandlerReopenRequiresReason(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusConfirmed)
	service := &fakeIssueService{actionIssue: issue, details: map[int64]deliveryapp.IssueDetail{1001: sampleIssueDetail(issue)}}
	mux := newDeliveryMux(t, New(service))

	for _, body := range []string{`{}`, `{"reason":"   "}`, `{"reason":""}`} {
		recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/reopen", body, userActor())
		requireStatus(t, recorder, http.StatusBadRequest)
		details := errorDetails(t, decodeDeliveryError(t, recorder))
		if details["field"] != "reason" {
			t.Errorf("body %s: field = %v, want reason", body, details["field"])
		}
	}
	if len(service.reopenCalls) != 0 {
		t.Errorf("Reopen calls = %d, want none", len(service.reopenCalls))
	}

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/reopen",
		`{"reason":"fixed","expected_revision":2}`, userActor())
	requireStatus(t, recorder, http.StatusOK)
	if len(service.reopenCalls) != 1 {
		t.Fatalf("Reopen calls = %#v", service.reopenCalls)
	}
	call := service.reopenCalls[0]
	if call.reason != "fixed" || call.expected == nil || *call.expected != 2 || call.actorID != 7 {
		t.Errorf("Reopen call = %+v", call)
	}
}

func TestIssueHandlerMerge(t *testing.T) {
	issue := sampleIssue(1001, delivery.IssueStatusMerged)
	service := &fakeIssueService{actionIssue: issue, details: map[int64]deliveryapp.IssueDetail{1001: sampleIssueDetail(issue)}}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/merge",
		`{"target_issue_id":"2002","reason":"duplicate","expected_revision":4}`, userActor())
	requireStatus(t, recorder, http.StatusOK)
	if len(service.mergeCalls) != 1 {
		t.Fatalf("Merge calls = %#v", service.mergeCalls)
	}
	call := service.mergeCalls[0]
	if call.versionID != 42 || call.sourceID != 1001 || call.targetID != 2002 || call.actorID != 7 {
		t.Errorf("Merge call = %+v", call)
	}
	if call.reason != "duplicate" || call.expected == nil || *call.expected != 4 {
		t.Errorf("Merge reason/expected = %+v", call)
	}
	data := decodeDeliveryObject(t, recorder)
	if data["status"] != "merged" || data["merged_into_id"] != "2002" {
		t.Errorf("merged response = %v", data)
	}
}

func TestIssueHandlerMergeRejectsInvalidTarget(t *testing.T) {
	service := &fakeIssueService{}
	mux := newDeliveryMux(t, New(service))

	for _, body := range []string{`{}`, `{"target_issue_id":""}`, `{"target_issue_id":"abc"}`, `{"target_issue_id":"-1"}`, `{"target_issue_id":"0"}`} {
		recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/merge", body, userActor())
		requireStatus(t, recorder, http.StatusBadRequest)
		details := errorDetails(t, decodeDeliveryError(t, recorder))
		if details["field"] != "target_issue_id" {
			t.Errorf("body %s: field = %v, want target_issue_id", body, details["field"])
		}
	}
	if len(service.mergeCalls) != 0 {
		t.Errorf("Merge calls = %d, want none", len(service.mergeCalls))
	}
}

func TestIssueHandlerRejectsInvalidBodiesAndRevisions(t *testing.T) {
	service := &fakeIssueService{}
	mux := newDeliveryMux(t, New(service))
	longReason := strings.Repeat("x", 2001)

	tests := []struct {
		name   string
		target string
		body   string
		field  string
	}{
		{name: "invalid json", target: "/confirm", body: `{"reason":`, field: "body"},
		{name: "reason too long", target: "/confirm", body: `{"reason":"` + longReason + `"}`, field: "reason"},
		{name: "zero expected", target: "/confirm", body: `{"expected_revision":0}`, field: "expected_revision"},
		{name: "negative expected", target: "/confirm", body: `{"expected_revision":-2}`, field: "expected_revision"},
		{name: "huge expected", target: "/confirm", body: `{"expected_revision":99999999999}`, field: "expected_revision"},
		{name: "reopen empty body", target: "/reopen", body: "", field: "body"},
		{name: "merge empty body", target: "/merge", body: "", field: "body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001"+test.target, test.body, userActor())
			requireStatus(t, recorder, http.StatusBadRequest)
			details := errorDetails(t, decodeDeliveryError(t, recorder))
			if details["field"] != test.field {
				t.Errorf("field = %v, want %s", details["field"], test.field)
			}
		})
	}
	if len(service.confirmCalls)+len(service.reopenCalls)+len(service.mergeCalls) != 0 {
		t.Errorf("invalid requests reached the service: %#v", service)
	}
}

func TestIssueHandlerActionsRequireActor(t *testing.T) {
	service := &fakeIssueService{}
	mux := newDeliveryMux(t, New(service))

	recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001/confirm", `{}`, nil)
	requireStatus(t, recorder, http.StatusUnauthorized)
	if len(service.confirmCalls) != 0 {
		t.Errorf("Confirm calls = %d, want none", len(service.confirmCalls))
	}
}

func TestIssueHandlerActionErrorMapping(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		body           string
		setErr         func(*fakeIssueService, error)
		err            error
		status         int
		code           string
		reason         string
		messageContain string
	}{
		{
			name: "confirm not open", path: "/confirm",
			setErr: func(service *fakeIssueService, err error) { service.confirmErr = err },
			err:    &deliveryapp.IssueStateConflictError{Reason: deliveryapp.IssueConflictNotOpen},
			status: http.StatusConflict, code: "state_conflict", reason: deliveryapp.IssueConflictNotOpen,
		},
		{
			name: "dismiss not open", path: "/dismiss",
			setErr: func(service *fakeIssueService, err error) { service.dismissErr = err },
			err:    &deliveryapp.IssueStateConflictError{Reason: deliveryapp.IssueConflictNotOpen},
			status: http.StatusConflict, code: "state_conflict", reason: deliveryapp.IssueConflictNotOpen,
		},
		{
			name: "reopen not reopenable", path: "/reopen",
			setErr: func(service *fakeIssueService, err error) { service.reopenErr = err },
			err:    &deliveryapp.IssueStateConflictError{Reason: deliveryapp.IssueConflictNotReopenable},
			status: http.StatusConflict, code: "state_conflict", reason: deliveryapp.IssueConflictNotReopenable,
		},
		{
			name: "merge cycle", path: "/merge", body: `{"target_issue_id":"2002"}`,
			setErr: func(service *fakeIssueService, err error) { service.mergeErr = err },
			err:    &delivery.MergeError{Reason: delivery.MergeReasonCycle},
			status: http.StatusConflict, code: "state_conflict", reason: delivery.MergeReasonCycle,
		},
		{
			name: "merge target invalid", path: "/merge", body: `{"target_issue_id":"2002"}`,
			setErr: func(service *fakeIssueService, err error) { service.mergeErr = err },
			err:    &delivery.MergeError{Reason: delivery.MergeReasonTargetInvalid},
			status: http.StatusConflict, code: "state_conflict", reason: delivery.MergeReasonTargetInvalid,
		},
		{
			name: "missing issue", path: "/confirm",
			setErr: func(service *fakeIssueService, err error) { service.confirmErr = err },
			err:    deliveryapp.ErrNotFound,
			status: http.StatusNotFound, code: "not_found",
		},
		{
			name: "invalid reason", path: "/reopen",
			setErr:         func(service *fakeIssueService, err error) { service.reopenErr = err },
			err:            fmt.Errorf("%w: reason must not be empty", deliveryapp.ErrInvalidArgument),
			status:         http.StatusBadRequest,
			code:           "invalid_argument",
			messageContain: "reason must not be empty",
		},
		{
			name: "unknown", path: "/confirm",
			setErr: func(service *fakeIssueService, err error) { service.confirmErr = err },
			err:    errors.New("boom"),
			status: http.StatusInternalServerError, code: "internal",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeIssueService{}
			test.setErr(service, test.err)
			mux := newDeliveryMux(t, New(service))
			body := test.body
			if body == "" {
				body = `{"reason":"valid"}`
			}
			recorder := deliveryCall(t, mux, http.MethodPost, "/api/versions/42/issues/1001"+test.path, body, userActor())
			requireStatus(t, recorder, test.status)
			apiError := decodeDeliveryError(t, recorder)
			if apiError["code"] != test.code {
				t.Errorf("code = %v, want %s", apiError["code"], test.code)
			}
			if test.reason != "" {
				if reason := errorDetails(t, apiError)["reason"]; reason != test.reason {
					t.Errorf("details.reason = %v, want %s", reason, test.reason)
				}
			}
			if test.messageContain != "" {
				message, _ := apiError["message"].(string)
				if !strings.Contains(message, test.messageContain) {
					t.Errorf("message = %q, want it to contain %q", message, test.messageContain)
				}
			}
		})
	}
}
