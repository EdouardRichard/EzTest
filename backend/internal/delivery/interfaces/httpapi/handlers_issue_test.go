package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// issueServer wires the issue handlers with a fake service and refs reader.
func issueServer(t *testing.T, issues *fakeIssueService, refs *fakeIssueRefs) http.Handler {
	t.Helper()
	return newTestServer(t, HandlerDeps{Issues: issues, IssueRefs: refs})
}

// TestIssueRoutesRequireAuthentication proves every issue route is behind the
// global bearer auth (spec §11.1: 401 for anonymous).
func TestIssueRoutesRequireAuthentication(t *testing.T) {
	h := issueServer(t, &fakeIssueService{}, &fakeIssueRefs{})
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/versions/101/issues", ""},
		{http.MethodPost, "/api/versions/101/issues", `{"title":"x","severity":"major"}`},
		{http.MethodGet, "/api/versions/101/issues/9001", ""},
		{http.MethodPatch, "/api/versions/101/issues/9001", `{"status":"confirmed","expectedRevision":1}`},
	} {
		rec := doRequest(h, route.method, route.path, "", route.body)
		requireStatus(t, rec, http.StatusUnauthorized)
	}
}

// TestIssueListPassesFiltersAndReturnsCursor proves the list endpoint forwards
// every §7.3 filter, decodes the opaque cursor and renders string ids.
func TestIssueListPassesFiltersAndReturnsCursor(t *testing.T) {
	issueA := sampleIssue()
	issueB := sampleIssue()
	issueB.ID = 9002
	var gotVersionID int64
	var gotFilter deliveryapp.IssueFilter
	issues := &fakeIssueService{list: func(_ context.Context, versionID int64, filter deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
		gotVersionID, gotFilter = versionID, filter
		return []domain.Issue{issueA, issueB}, true, nil
	}}
	refs := &fakeIssueRefs{refs: map[int64][]domain.IssueRef{
		issueA.ID: sampleIssueRefs(issueA.ID),
		issueB.ID: {},
	}}
	codeRefs := &fakeIssueCodeRefs{refs: map[int64][]IssueCodeRef{
		issueA.ID: {{AnchorID: 8001, Path: "src/x.ts", Line: 42, Kind: "file_line", Label: "checkout"}},
	}}
	h := newTestServer(t, HandlerDeps{Issues: issues, IssueRefs: refs, IssueCodeRefs: codeRefs})

	cursor := encodeCursor(9009)
	rec := doRequest(h, http.MethodGet,
		"/api/versions/101/issues?status=open&severity=major&category=defect"+
			"&batchId=7001&caseRunId=7101&caseNodeId=7102&q=login&cursor="+cursor+"&limit=2",
		"ezt_user", "")
	requireStatus(t, rec, http.StatusOK)

	if gotVersionID != testVersionID {
		t.Fatalf("versionID=%d want %d", gotVersionID, testVersionID)
	}
	if gotFilter.Status != domain.StatusOpen || gotFilter.Severity != domain.SeverityMajor ||
		gotFilter.Category != domain.CategoryDefect || gotFilter.Query != "login" ||
		gotFilter.Limit != 2 || gotFilter.Cursor != 9009 {
		t.Fatalf("filter=%+v", gotFilter)
	}
	if gotFilter.BatchID == nil || *gotFilter.BatchID != 7001 ||
		gotFilter.CaseRunID == nil || *gotFilter.CaseRunID != 7101 ||
		gotFilter.CaseNodeID == nil || *gotFilter.CaseNodeID != 7102 {
		t.Fatalf("filter ids=%+v", gotFilter)
	}
	if codeRefs.calls != 2 || codeRefs.gotVersion != testVersionID || codeRefs.gotIssue != issueB.ID {
		t.Fatalf("code refs reader calls=%d version=%d issue=%d want 2/%d/%d",
			codeRefs.calls, codeRefs.gotVersion, codeRefs.gotIssue, testVersionID, issueB.ID)
	}

	items, next, hasMore := decodePage(t, rec)
	if len(items) != 2 || next != encodeCursor(9002) || !hasMore {
		t.Fatalf("items=%d next=%q hasMore=%v want 2 %q true", len(items), next, hasMore, encodeCursor(9002))
	}
	if got := requireStringField(t, items[0], "id"); got != "9001" {
		t.Fatalf("issue id=%q want 9001", got)
	}
	requireStringField(t, items[0], "versionId")
	requireStringField(t, items[0], "batchId")
	requireStringField(t, items[0], "caseRunId")
	requireStringField(t, items[0], "caseNodeId")
	if _, ok := items[0]["refs"].([]any); !ok {
		t.Fatalf("list item refs=%v want an array", items[0]["refs"])
	}
	codeRefItems, ok := items[0]["codeRefs"].([]any)
	if !ok || len(codeRefItems) != 1 {
		t.Fatalf("list item codeRefs=%v want one entry", items[0]["codeRefs"])
	}
	codeRef, _ := codeRefItems[0].(map[string]any)
	if requireStringField(t, codeRef, "anchorId") != "8001" ||
		codeRef["path"] != "src/x.ts" || codeRef["line"] != float64(42) ||
		codeRef["kind"] != "file_line" || codeRef["label"] != "checkout" {
		t.Fatalf("codeRef=%v", codeRef)
	}
	if second, ok := items[1]["codeRefs"].([]any); !ok || len(second) != 0 {
		t.Fatalf("issue without anchors codeRefs=%v want []", items[1]["codeRefs"])
	}
}

// TestIssueListRejectsInvalidPagination proves limit/cursor validation happens
// at the HTTP boundary (spec §7.1).
func TestIssueListRejectsInvalidPagination(t *testing.T) {
	issues := &fakeIssueService{list: func(context.Context, int64, deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
		t.Fatal("list must not be called for an invalid query")
		return nil, false, nil
	}}
	h := issueServer(t, issues, &fakeIssueRefs{})
	for name, path := range map[string]string{
		"limit zero":       "/api/versions/101/issues?limit=0",
		"limit too large":  "/api/versions/101/issues?limit=101",
		"limit not a num":  "/api/versions/101/issues?limit=ten",
		"cursor malformed": "/api/versions/101/issues?cursor=not-a-cursor",
		"batch id invalid": "/api/versions/101/issues?batchId=abc",
	} {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
			requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
		})
	}
}

// TestIssueListLimitContract pins the delivery side of the shared limit
// contract (spec §5.6 C6): 100 is accepted, and 200 keeps the existing 422
// invalid_argument mapping while carrying the clarified message and
// details{field,min,max}.
func TestIssueListLimitContract(t *testing.T) {
	var gotLimit int
	issues := &fakeIssueService{list: func(_ context.Context, _ int64, filter deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
		gotLimit = filter.Limit
		return nil, false, nil
	}}
	h := issueServer(t, issues, &fakeIssueRefs{})

	rec := doRequest(h, http.MethodGet, "/api/versions/101/issues?limit=100", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if gotLimit != platform.MaxLimit {
		t.Fatalf("list limit=%d want %d", gotLimit, platform.MaxLimit)
	}

	for _, testCase := range []struct{ name, limit, message string }{
		{"non-integer", "ten", "limit must be an integer"},
		{"too large", "200", "limit must be between 1 and 100"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rec := doRequest(h, http.MethodGet,
				"/api/versions/101/issues?limit="+testCase.limit, "ezt_user", "")
			requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
			envelope := decodeError(t, rec)
			if envelope.Error.Message != testCase.message {
				t.Fatalf("message=%q want %q", envelope.Error.Message, testCase.message)
			}
			details := envelope.Error.Details
			if details["field"] != "limit" || details["min"] != float64(1) ||
				details["max"] != float64(platform.MaxLimit) {
				t.Fatalf("details=%v want field=limit min=1 max=%d", details, platform.MaxLimit)
			}
		})
	}
}

// TestIssueListMapsServiceErrors proves a domain validation error keeps its
// stable reason and maps to 422.
func TestIssueListMapsServiceErrors(t *testing.T) {
	issues := &fakeIssueService{list: func(context.Context, int64, deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
		return nil, false, domain.NewInvalid("status", domain.ReasonInvalidArgument, "status is invalid")
	}}
	h := issueServer(t, issues, &fakeIssueRefs{})
	rec := doRequest(h, http.MethodGet, "/api/versions/101/issues?status=nope", "ezt_user", "")
	requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
}

// TestCreateHumanIssue proves POST creates an open human issue and forwards the
// body as decimal-string ids.
func TestCreateHumanIssue(t *testing.T) {
	issue := sampleIssue()
	issue.Source = domain.SourceHuman
	issue.DedupeKey = ""
	var gotVersionID, gotActor int64
	var gotCmd deliveryapp.CreateIssueCommand
	issues := &fakeIssueService{createHuman: func(_ context.Context, versionID, actor int64, cmd deliveryapp.CreateIssueCommand) (domain.Issue, error) {
		gotVersionID, gotActor, gotCmd = versionID, actor, cmd
		return issue, nil
	}}
	h := issueServer(t, issues, &fakeIssueRefs{refs: map[int64][]domain.IssueRef{issue.ID: sampleIssueRefs(issue.ID)}})

	rec := doRequest(h, http.MethodPost, "/api/versions/101/issues",
		"ezt_user",
		`{"title":"checkout fails","severity":"major","description":"500",`+
			`"expected":"order","actual":"500","reproSteps":"submit",`+
			`"caseRunId":"7101","caseNodeId":"7102","requirementId":"7301"}`)
	requireStatus(t, rec, http.StatusCreated)

	if gotVersionID != testVersionID || gotActor != testActorID {
		t.Fatalf("version=%d actor=%d", gotVersionID, gotActor)
	}
	if gotCmd.Title != "checkout fails" || gotCmd.Severity != domain.SeverityMajor ||
		gotCmd.Description != "500" || gotCmd.Expected != "order" || gotCmd.Actual != "500" ||
		gotCmd.ReproSteps != "submit" {
		t.Fatalf("command=%+v", gotCmd)
	}
	if gotCmd.CaseRunID == nil || *gotCmd.CaseRunID != 7101 ||
		gotCmd.CaseNodeID == nil || *gotCmd.CaseNodeID != 7102 ||
		gotCmd.RequirementID == nil || *gotCmd.RequirementID != 7301 {
		t.Fatalf("command ids=%+v", gotCmd)
	}

	data := decodeData(t, rec)
	if data["source"] != domain.SourceHuman {
		t.Fatalf("source=%v want human", data["source"])
	}
	if got := requireStringField(t, data, "id"); got != "9001" {
		t.Fatalf("id=%q", got)
	}
	if _, ok := data["refs"].([]any); !ok {
		t.Fatalf("refs=%v want an array", data["refs"])
	}
}

// TestCreateIssueRejectsInvalidBody proves malformed JSON, numeric ids and bad
// path ids are 422.
func TestCreateIssueRejectsInvalidBody(t *testing.T) {
	issues := &fakeIssueService{}
	h := issueServer(t, issues, &fakeIssueRefs{})
	for name, request := range map[string]struct{ method, path, body string }{
		"malformed json": {http.MethodPost, "/api/versions/101/issues", `{"title":`},
		"numeric id":     {http.MethodPost, "/api/versions/101/issues", `{"title":"x","severity":"major","caseRunId":7101}`},
		"bad id string":  {http.MethodPost, "/api/versions/101/issues", `{"title":"x","severity":"major","caseRunId":"x"}`},
		"bad version":    {http.MethodPost, "/api/versions/abc/issues", `{"title":"x","severity":"major"}`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(h, request.method, request.path, "ezt_user", request.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			envelope := decodeError(t, rec)
			if envelope.Error.Code != "invalid_argument" {
				t.Fatalf("code=%q body=%s", envelope.Error.Code, rec.Body)
			}
		})
	}
}

// TestGetIssueDetail proves the detail view carries refs, string ids and the
// derived case/batch summary without leaking sensitive fields.
func TestGetIssueDetail(t *testing.T) {
	issue := sampleIssue()
	issue.ConfirmedBy = nil
	issues := &fakeIssueService{get: func(context.Context, int64, int64) (domain.Issue, error) {
		return issue, nil
	}}
	refs := &fakeIssueRefs{refs: map[int64][]domain.IssueRef{issue.ID: sampleIssueRefs(issue.ID)}}
	codeRefs := &fakeIssueCodeRefs{refs: map[int64][]IssueCodeRef{
		issue.ID: {{AnchorID: 8001, Path: "src/x.ts", Line: 42, Kind: "file_line", Label: "checkout"}},
	}}
	h := newTestServer(t, HandlerDeps{Issues: issues, IssueRefs: refs, IssueCodeRefs: codeRefs})

	rec := doRequest(h, http.MethodGet, "/api/versions/101/issues/9001", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	data := decodeData(t, rec)
	requireStringField(t, data, "id")
	requireStringField(t, data, "versionId")
	requireStringField(t, data, "caseRunId")
	requireStringField(t, data, "caseNodeId")
	if got := requireStringField(t, data, "createdBy"); got != "1" {
		t.Fatalf("createdBy=%q", got)
	}
	if data["description"] != "checkout request returns 500" {
		t.Fatalf("description=%v", data["description"])
	}
	if data["failureClass"] != domain.FailureClassApplication {
		t.Fatalf("failureClass=%v", data["failureClass"])
	}
	if codeRefs.calls != 1 || codeRefs.gotVersion != testVersionID || codeRefs.gotIssue != issue.ID {
		t.Fatalf("code refs reader calls=%d version=%d issue=%d",
			codeRefs.calls, codeRefs.gotVersion, codeRefs.gotIssue)
	}
	detailCodeRefs, ok := data["codeRefs"].([]any)
	if !ok || len(detailCodeRefs) != 1 {
		t.Fatalf("detail codeRefs=%v want one entry", data["codeRefs"])
	}
	detailCodeRef, _ := detailCodeRefs[0].(map[string]any)
	if requireStringField(t, detailCodeRef, "anchorId") != "8001" ||
		detailCodeRef["path"] != "src/x.ts" || detailCodeRef["line"] != float64(42) ||
		detailCodeRef["kind"] != "file_line" || detailCodeRef["label"] != "checkout" {
		t.Fatalf("detail codeRef=%v", detailCodeRef)
	}
	refsJSON, ok := data["refs"].([]any)
	if !ok || len(refsJSON) != 2 {
		t.Fatalf("refs=%v want 2 entries", data["refs"])
	}
	first, _ := refsJSON[0].(map[string]any)
	if requireStringField(t, first, "id") != "7102" || first["kind"] != domain.RefKindCaseNode {
		t.Fatalf("ref=%v", first)
	}
	caseSummary, ok := data["case"].(map[string]any)
	if !ok {
		t.Fatalf("case summary=%v", data["case"])
	}
	requireStringField(t, caseSummary, "caseNodeId")
	batchSummary, ok := data["batch"].(map[string]any)
	if !ok {
		t.Fatalf("batch summary=%v", data["batch"])
	}
	requireStringField(t, batchSummary, "id")
	for _, forbidden := range []string{"password", "secret", "evidence", "prompt", "markdownPath"} {
		requireNoJSONField(t, data, forbidden)
	}
}

// TestGetIssueMissingIs404 proves a missing/foreign issue is 404 issue_not_found.
func TestGetIssueMissingIs404(t *testing.T) {
	issues := &fakeIssueService{get: func(context.Context, int64, int64) (domain.Issue, error) {
		return domain.Issue{}, domain.NewNotFound("issue", testIssueID)
	}}
	h := issueServer(t, issues, &fakeIssueRefs{})
	rec := doRequest(h, http.MethodGet, "/api/versions/101/issues/9001", "ezt_user", "")
	requireError(t, rec, http.StatusNotFound, "not_found", "issue_not_found", false)
}

// TestIssueCodeRefsNilOrFailureIsEmpty proves the optional codeintel seam never
// breaks an issue response: an unwired or failing reader yields codeRefs: []
// (spec §7.3).
func TestIssueCodeRefsNilOrFailureIsEmpty(t *testing.T) {
	issue := sampleIssue()
	issues := &fakeIssueService{
		list: func(context.Context, int64, deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
			return []domain.Issue{issue}, false, nil
		},
		get: func(context.Context, int64, int64) (domain.Issue, error) { return issue, nil },
	}
	// Failing codeintel reader: both list and detail stay 200 with an empty array.
	h := newTestServer(t, HandlerDeps{
		Issues:        issues,
		IssueRefs:     &fakeIssueRefs{},
		IssueCodeRefs: &fakeIssueCodeRefs{err: errors.New("codeintel unavailable")},
	})
	rec := doRequest(h, http.MethodGet, "/api/versions/101/issues", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	items, _, _ := decodePage(t, rec)
	if refs, ok := items[0]["codeRefs"].([]any); !ok || len(refs) != 0 {
		t.Fatalf("list codeRefs=%v want []", items[0]["codeRefs"])
	}
	rec = doRequest(h, http.MethodGet, "/api/versions/101/issues/9001", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if refs, ok := decodeData(t, rec)["codeRefs"].([]any); !ok || len(refs) != 0 {
		t.Fatalf("detail codeRefs=%v want []", decodeData(t, rec)["codeRefs"])
	}

	// Unwired reader: the codeRefs field is still an empty array.
	h = newTestServer(t, HandlerDeps{Issues: issues, IssueRefs: &fakeIssueRefs{}})
	rec = doRequest(h, http.MethodGet, "/api/versions/101/issues/9001", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if refs, ok := decodeData(t, rec)["codeRefs"].([]any); !ok || len(refs) != 0 {
		t.Fatalf("unwired detail codeRefs=%v want []", decodeData(t, rec)["codeRefs"])
	}
}

// TestPatchIssueConfirm proves PATCH confirmed forwards the OCC revision and
// actor and returns the transitioned issue.
func TestPatchIssueConfirm(t *testing.T) {
	confirmed := sampleIssue()
	confirmed.Status = domain.StatusConfirmed
	confirmed.Revision = 4
	actor := testActorID
	confirmed.ConfirmedBy = &actor
	var gotVersionID, gotIssueID, gotActor int64
	var gotRevision int
	var gotComment string
	issues := &fakeIssueService{confirm: func(_ context.Context, versionID, issueID, actorID int64, expectedRevision int, comment string) (domain.Issue, error) {
		gotVersionID, gotIssueID, gotActor = versionID, issueID, actorID
		gotRevision, gotComment = expectedRevision, comment
		return confirmed, nil
	}}
	h := issueServer(t, issues, &fakeIssueRefs{refs: map[int64][]domain.IssueRef{confirmed.ID: sampleIssueRefs(confirmed.ID)}})

	rec := doRequest(h, http.MethodPatch, "/api/versions/101/issues/9001",
		"ezt_user", `{"status":"confirmed","expectedRevision":3,"comment":"verified"}`)
	requireStatus(t, rec, http.StatusOK)
	if gotVersionID != testVersionID || gotIssueID != testIssueID || gotActor != testActorID ||
		gotRevision != 3 || gotComment != "verified" {
		t.Fatalf("args version=%d issue=%d actor=%d revision=%d comment=%q",
			gotVersionID, gotIssueID, gotActor, gotRevision, gotComment)
	}
	data := decodeData(t, rec)
	if data["status"] != domain.StatusConfirmed {
		t.Fatalf("status=%v", data["status"])
	}
	if got := requireStringField(t, data, "confirmedBy"); got != strconv.FormatInt(testActorID, 10) {
		t.Fatalf("confirmedBy=%q", got)
	}
}

// TestPatchIssueDismiss proves PATCH dismissed forwards the required reason.
func TestPatchIssueDismiss(t *testing.T) {
	dismissed := sampleIssue()
	dismissed.Status = domain.StatusDismissed
	dismissed.Revision = 4
	dismissed.DismissedReason = "not a product bug"
	var gotReason string
	issues := &fakeIssueService{dismiss: func(_ context.Context, _, _, _ int64, _ int, reason string) (domain.Issue, error) {
		gotReason = reason
		return dismissed, nil
	}}
	h := issueServer(t, issues, &fakeIssueRefs{})

	rec := doRequest(h, http.MethodPatch, "/api/versions/101/issues/9001",
		"ezt_user", `{"status":"dismissed","expectedRevision":3,"comment":"not a product bug"}`)
	requireStatus(t, rec, http.StatusOK)
	if gotReason != "not a product bug" {
		t.Fatalf("reason=%q", gotReason)
	}
	if data := decodeData(t, rec); data["dismissedReason"] != "not a product bug" {
		t.Fatalf("dismissedReason=%v", data["dismissedReason"])
	}
}

// TestPatchIssueErrors proves the 409/422 mappings of the issue transition.
func TestPatchIssueErrors(t *testing.T) {
	conflict := &fakeIssueService{confirm: func(context.Context, int64, int64, int64, int, string) (domain.Issue, error) {
		return domain.Issue{}, domain.NewStateConflict(domain.ReasonIssueStateConflict, "revision mismatch")
	}}
	dismissNoReason := &fakeIssueService{dismiss: func(context.Context, int64, int64, int64, int, string) (domain.Issue, error) {
		return domain.Issue{}, domain.NewInvalid("comment", domain.ReasonIssueDismissReasonRequired, "reason required")
	}}

	t.Run("state conflict 409", func(t *testing.T) {
		h := issueServer(t, conflict, &fakeIssueRefs{})
		rec := doRequest(h, http.MethodPatch, "/api/versions/101/issues/9001",
			"ezt_user", `{"status":"confirmed","expectedRevision":2}`)
		requireError(t, rec, http.StatusConflict, "conflict", "issue_state_conflict", false)
	})
	t.Run("dismiss reason 422", func(t *testing.T) {
		h := issueServer(t, dismissNoReason, &fakeIssueRefs{})
		rec := doRequest(h, http.MethodPatch, "/api/versions/101/issues/9001",
			"ezt_user", `{"status":"dismissed","expectedRevision":3}`)
		requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument",
			"issue_dismiss_reason_required", false)
	})
	t.Run("invalid status 422", func(t *testing.T) {
		h := issueServer(t, &fakeIssueService{}, &fakeIssueRefs{})
		rec := doRequest(h, http.MethodPatch, "/api/versions/101/issues/9001",
			"ezt_user", `{"status":"closed","expectedRevision":3}`)
		requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
	})
	t.Run("missing revision 422", func(t *testing.T) {
		h := issueServer(t, &fakeIssueService{}, &fakeIssueRefs{})
		rec := doRequest(h, http.MethodPatch, "/api/versions/101/issues/9001",
			"ezt_user", `{"status":"confirmed"}`)
		requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
	})
	t.Run("unknown issue 404", func(t *testing.T) {
		missing := &fakeIssueService{confirm: func(context.Context, int64, int64, int64, int, string) (domain.Issue, error) {
			return domain.Issue{}, domain.NewNotFound("issue", 9001)
		}}
		h := issueServer(t, missing, &fakeIssueRefs{})
		rec := doRequest(h, http.MethodPatch, "/api/versions/101/issues/9001",
			"ezt_user", `{"status":"confirmed","expectedRevision":3}`)
		requireError(t, rec, http.StatusNotFound, "not_found", "issue_not_found", false)
	})
}

// TestIssueRoutesAllowRegularUsers proves delivery has no admin guard: a regular
// authenticated user must never receive 403 (spec §11.1).
func TestIssueRoutesAllowRegularUsers(t *testing.T) {
	issue := sampleIssue()
	issues := &fakeIssueService{
		list: func(context.Context, int64, deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
			return []domain.Issue{issue}, false, nil
		},
		get: func(context.Context, int64, int64) (domain.Issue, error) { return issue, nil },
		confirm: func(context.Context, int64, int64, int64, int, string) (domain.Issue, error) {
			return issue, nil
		},
	}
	h := issueServer(t, issues, &fakeIssueRefs{})
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/versions/101/issues", ""},
		{http.MethodGet, "/api/versions/101/issues/9001", ""},
		{http.MethodPatch, "/api/versions/101/issues/9001", `{"status":"confirmed","expectedRevision":3}`},
	} {
		rec := doRequest(h, request.method, request.path, "ezt_user", request.body)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s %s returned 403 for a regular user: %s", request.method, request.path, rec.Body)
		}
	}
}

// TestIssueErrorMappingKeepsDomainErrorsAs500 proves an unexpected error is a
// 500 without leaking internals.
func TestIssueErrorMappingKeepsDomainErrorsAs500(t *testing.T) {
	issues := &fakeIssueService{get: func(context.Context, int64, int64) (domain.Issue, error) {
		return domain.Issue{}, errors.New("store exploded")
	}}
	h := issueServer(t, issues, &fakeIssueRefs{})
	rec := doRequest(h, http.MethodGet, "/api/versions/101/issues/9001", "ezt_user", "")
	requireStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "store exploded") {
		t.Fatalf("internal error leaked: %s", rec.Body)
	}
}
