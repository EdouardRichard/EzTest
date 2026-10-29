package httpapi

import (
	"context"
	"net/http"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeKnowledgeService records the knowledge calls the HTTP layer makes and
// serves a scripted entry list. Its error fields exercise the shared error
// envelope mapping.
type fakeKnowledgeService struct {
	entries []versionapp.KnowledgeEntry
	nextID  int64

	createErr error
	updateErr error
	reviewErr error
	listErr   error

	lastCreate struct {
		versionID int64
		input     versionapp.CreateKnowledgeInput
		actor     int64
	}
	lastUpdate struct {
		versionID int64
		id        int64
		input     versionapp.UpdateKnowledgeInput
		actor     int64
		admin     bool
	}
	lastReview struct {
		versionID int64
		id        int64
		input     versionapp.ReviewKnowledgeInput
		actor     int64
	}
	lastList struct {
		versionID int64
		status    string
		kind      string
		page      domain.Page
	}
	lastGlobal struct {
		versionID *int64
		status    string
		kind      string
		page      domain.Page
	}
}

// newFakeKnowledgeService returns an empty fake.
func newFakeKnowledgeService() *fakeKnowledgeService {
	return &fakeKnowledgeService{nextID: 6001}
}

// Create records the call and returns a draft entry.
func (f *fakeKnowledgeService) Create(_ context.Context, versionID int64, input versionapp.CreateKnowledgeInput, actor int64) (*versionapp.KnowledgeEntry, error) {
	f.lastCreate.versionID = versionID
	f.lastCreate.input = input
	f.lastCreate.actor = actor
	if f.createErr != nil {
		return nil, f.createErr
	}
	entry := versionapp.KnowledgeEntry{
		ID: f.nextID, VersionID: versionID, Kind: input.Kind, Title: input.Title,
		Body: input.Body, Tags: input.Tags, SourceRefs: input.SourceRefs,
		Status: domain.KnowledgeDraft, Confidence: domain.KnowledgeConfidenceMedium,
		CreatedBy: actor, Revision: 1, CreatedAt: testNow, UpdatedAt: testNow,
	}
	f.nextID++
	return &entry, nil
}

// Update records the call and returns the revised entry.
func (f *fakeKnowledgeService) Update(_ context.Context, versionID, kid int64, input versionapp.UpdateKnowledgeInput, actor int64, admin bool) (*versionapp.KnowledgeEntry, error) {
	f.lastUpdate.versionID = versionID
	f.lastUpdate.id = kid
	f.lastUpdate.input = input
	f.lastUpdate.actor = actor
	f.lastUpdate.admin = admin
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	entry := versionapp.KnowledgeEntry{
		ID: kid, VersionID: versionID, Kind: domain.KnowledgeKindPattern,
		Title: "updated", Body: "updated body", Status: domain.KnowledgeDraft,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: actor,
		Revision: input.Revision + 1, CreatedAt: testNow, UpdatedAt: testNow,
	}
	return &entry, nil
}

// Review records the call and returns the reviewed entry.
func (f *fakeKnowledgeService) Review(_ context.Context, versionID, kid int64, input versionapp.ReviewKnowledgeInput, actor int64) (*versionapp.KnowledgeEntry, error) {
	f.lastReview.versionID = versionID
	f.lastReview.id = kid
	f.lastReview.input = input
	f.lastReview.actor = actor
	if f.reviewErr != nil {
		return nil, f.reviewErr
	}
	status := domain.KnowledgeApproved
	if !input.Approve {
		status = domain.KnowledgeRejected
	}
	entry := versionapp.KnowledgeEntry{
		ID: kid, VersionID: versionID, Kind: domain.KnowledgeKindPattern,
		Title: "reviewed", Body: "reviewed body", Status: status,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: 7,
		ReviewedBy: &actor, ReviewNote: input.Reason, Revision: input.Revision + 1,
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	return &entry, nil
}

// List records the version-scoped call.
func (f *fakeKnowledgeService) List(_ context.Context, versionID int64, status, kind string, page domain.Page) ([]versionapp.KnowledgeEntry, bool, error) {
	f.lastList.versionID = versionID
	f.lastList.status = status
	f.lastList.kind = kind
	f.lastList.page = page
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	return f.entries, false, nil
}

// ListGlobal records the global call.
func (f *fakeKnowledgeService) ListGlobal(_ context.Context, versionID *int64, status, kind string, page domain.Page) ([]versionapp.KnowledgeEntry, bool, error) {
	f.lastGlobal.versionID = versionID
	f.lastGlobal.status = status
	f.lastGlobal.kind = kind
	f.lastGlobal.page = page
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	return f.entries, false, nil
}

// knowledgeEntry is one DTO fixture.
func knowledgeEntry(overrides versionapp.KnowledgeEntry) versionapp.KnowledgeEntry {
	entry := versionapp.KnowledgeEntry{
		ID: 6000, VersionID: versionDetailID, VersionName: "Order v2",
		Kind: domain.KnowledgeKindPattern, Title: "Retry policy",
		Body: "Retry twice before failing the case.", Tags: []string{"payment"},
		SourceRefs: []domain.KnowledgeSourceRef{{Kind: domain.KnowledgeRefRequirement, ID: 200}},
		Status:     domain.KnowledgeApproved, Confidence: domain.KnowledgeConfidenceHigh,
		CreatedBy: testUserID, Revision: 2, CreatedAt: testNow, UpdatedAt: testNow,
	}
	if overrides.ID != 0 {
		entry.ID = overrides.ID
	}
	if overrides.VersionID != 0 {
		entry.VersionID = overrides.VersionID
	}
	if overrides.Kind != "" {
		entry.Kind = overrides.Kind
	}
	if overrides.Title != "" {
		entry.Title = overrides.Title
	}
	if overrides.Body != "" {
		entry.Body = overrides.Body
	}
	if overrides.Status != "" {
		entry.Status = overrides.Status
	}
	if !overrides.CreatedAt.IsZero() {
		entry.CreatedAt = overrides.CreatedAt
		entry.UpdatedAt = overrides.CreatedAt
	}
	return entry
}

// TestKnowledgeCreateEndpoint covers POST /api/versions/{id}/knowledge.
func TestKnowledgeCreateEndpoint(t *testing.T) {
	handler, deps := newTestServer(t)
	recorder := doRequest(handler, http.MethodPost, "/api/versions/100/knowledge", testUserToken,
		`{"kind":"pitfall","title":"Payment timeout","body":"Use the fake clock.",`+
			`"confidence":"high","tags":["payment"],"sourceRefs":[{"kind":"requirement","id":"200"}]}`)
	requireStatus(t, recorder, http.StatusCreated)
	data := decodeDataMap(t, recorder)
	requireJSONString(t, data, "id", "6001")
	requireJSONString(t, data, "versionId", "100")
	requireStringField(t, data, "status", domain.KnowledgeDraft)
	requireStringField(t, data, "kind", domain.KnowledgeKindPitfall)
	requireJSONNumber(t, data, "createdBy", testUserID)
	if deps.knowledge.lastCreate.versionID != versionDetailID ||
		deps.knowledge.lastCreate.actor != testUserID ||
		deps.knowledge.lastCreate.input.Title != "Payment timeout" {
		t.Fatalf("create call = %+v", deps.knowledge.lastCreate)
	}
	if len(deps.knowledge.lastCreate.input.SourceRefs) != 1 ||
		deps.knowledge.lastCreate.input.SourceRefs[0].ID != 200 {
		t.Fatalf("source refs = %+v", deps.knowledge.lastCreate.input.SourceRefs)
	}

	// A version-scoped service error keeps its envelope.
	deps.knowledge.createErr = domain.NewNotFound("version", versionDetailID)
	recorder = doRequest(handler, http.MethodPost, "/api/versions/100/knowledge", testUserToken,
		`{"kind":"pattern","title":"t","body":"b"}`)
	requireError(t, recorder, http.StatusNotFound, "not_found", "not_found")

	deps.knowledge.createErr = domain.NewInvalid("title", "invalid_knowledge_title", "title is required")
	recorder = doRequest(handler, http.MethodPost, "/api/versions/100/knowledge", testUserToken,
		`{"kind":"pattern","body":"b"}`)
	requireError(t, recorder, http.StatusUnprocessableEntity, "invalid_argument", "invalid_knowledge_title")
}

// TestKnowledgeListEndpoints covers the version list and the global approved
// read including its admin-only status filter.
func TestKnowledgeListEndpoints(t *testing.T) {
	handler, deps := newTestServer(t)
	deps.knowledge.entries = []versionapp.KnowledgeEntry{knowledgeEntry(versionapp.KnowledgeEntry{})}

	recorder := doRequest(handler, http.MethodGet,
		"/api/versions/100/knowledge?status=approved&kind=pattern&limit=5", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	items, _, _ := decodePage(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	requireJSONString(t, items[0], "id", "6000")
	requireJSONString(t, items[0], "versionId", "100")
	requireJSONString(t, items[0], "versionName", "Order v2")
	requireJSONNumber(t, items[0], "createdBy", testUserID)
	requireNullField(t, items[0], "reviewedBy")
	requireStringField(t, items[0], "reviewNote", "")
	requireJSONNumber(t, items[0], "revision", 2)
	refs := requireArrayField(t, items[0], "sourceRefs")
	if len(refs) != 1 {
		t.Fatalf("source refs = %+v", refs)
	}
	ref, _ := refs[0].(map[string]any)
	requireStringField(t, ref, "kind", domain.KnowledgeRefRequirement)
	requireJSONString(t, ref, "id", "200")
	if deps.knowledge.lastList.status != "approved" || deps.knowledge.lastList.kind != "pattern" ||
		deps.knowledge.lastList.page.Limit != 5 {
		t.Fatalf("list call = %+v", deps.knowledge.lastList)
	}

	// The global read defaults to approved.
	recorder = doRequest(handler, http.MethodGet, "/api/knowledge", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	if deps.knowledge.lastGlobal.status != domain.KnowledgeApproved {
		t.Fatalf("global status = %q, want approved", deps.knowledge.lastGlobal.status)
	}

	// A regular user may not ask for the draft queue.
	recorder = doRequest(handler, http.MethodGet, "/api/knowledge?status=draft", testUserToken, "")
	requireError(t, recorder, http.StatusForbidden, "forbidden", "")

	// An administrator may.
	recorder = doRequest(handler, http.MethodGet, "/api/knowledge?status=draft&versionId=100", testAdminToken, "")
	requireStatus(t, recorder, http.StatusOK)
	if deps.knowledge.lastGlobal.status != domain.KnowledgeDraft ||
		deps.knowledge.lastGlobal.versionID == nil || *deps.knowledge.lastGlobal.versionID != 100 {
		t.Fatalf("global call = %+v", deps.knowledge.lastGlobal)
	}

	// A malformed versionId is a 400.
	recorder = doRequest(handler, http.MethodGet, "/api/knowledge?versionId=abc", testAdminToken, "")
	requireError(t, recorder, http.StatusBadRequest, "invalid_argument", "invalid_version_id")
}

// TestKnowledgePatchEndpoint covers the draft edit endpoint: the creator/admin
// rule is a service decision mapped to 403, and the revision is mandatory.
func TestKnowledgePatchEndpoint(t *testing.T) {
	handler, deps := newTestServer(t)
	recorder := doRequest(handler, http.MethodPatch, "/api/versions/100/knowledge/6000", testUserToken,
		`{"revision":2,"title":"Updated","tags":["a"],"confidence":"low"}`)
	requireStatus(t, recorder, http.StatusOK)
	data := decodeDataMap(t, recorder)
	requireJSONString(t, data, "id", "6000")
	if deps.knowledge.lastUpdate.id != 6000 || deps.knowledge.lastUpdate.admin ||
		deps.knowledge.lastUpdate.input.Revision != 2 ||
		deps.knowledge.lastUpdate.input.Title == nil || *deps.knowledge.lastUpdate.input.Title != "Updated" {
		t.Fatalf("update call = %+v", deps.knowledge.lastUpdate)
	}

	// The admin flag comes from the authenticated identity, not the body.
	recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/knowledge/6000", testAdminToken,
		`{"revision":3,"body":"admin edit"}`)
	requireStatus(t, recorder, http.StatusOK)
	if !deps.knowledge.lastUpdate.admin {
		t.Fatal("admin token did not set the admin flag")
	}

	// A missing revision is rejected before the service call.
	recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/knowledge/6000", testUserToken,
		`{"title":"no revision"}`)
	requireError(t, recorder, http.StatusUnprocessableEntity, "invalid_argument", "invalid_revision")

	// A service-side permission error maps to 403.
	deps.knowledge.updateErr = domain.NewForbidden("knowledge_forbidden", "only the creator or an admin may edit")
	recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/knowledge/6000", testUserToken,
		`{"revision":2,"title":"x"}`)
	requireError(t, recorder, http.StatusForbidden, "forbidden", "knowledge_forbidden")
}

// TestKnowledgeReviewEndpoint covers the admin-only review route and the
// approve/reject request contract.
func TestKnowledgeReviewEndpoint(t *testing.T) {
	handler, deps := newTestServer(t)

	// A regular user is rejected by the per-route admin guard.
	recorder := doRequest(handler, http.MethodPost, "/api/versions/100/knowledge/6000/review", testUserToken,
		`{"revision":1,"status":"approved"}`)
	requireError(t, recorder, http.StatusForbidden, "forbidden", "")
	if deps.knowledge.lastReview.id != 0 {
		t.Fatal("a rejected review reached the service")
	}

	recorder = doRequest(handler, http.MethodPost, "/api/versions/100/knowledge/6000/review", testAdminToken,
		`{"revision":1,"status":"approved","reason":"matches the sandbox"}`)
	requireStatus(t, recorder, http.StatusOK)
	data := decodeDataMap(t, recorder)
	requireStringField(t, data, "status", domain.KnowledgeApproved)
	if !deps.knowledge.lastReview.input.Approve || deps.knowledge.lastReview.actor != testAdminID ||
		deps.knowledge.lastReview.input.Revision != 1 {
		t.Fatalf("review call = %+v", deps.knowledge.lastReview)
	}

	// Rejecting is modelled as status=rejected; an invalid status is a 422.
	recorder = doRequest(handler, http.MethodPost, "/api/versions/100/knowledge/6000/review", testAdminToken,
		`{"revision":1,"status":"maybe"}`)
	requireError(t, recorder, http.StatusUnprocessableEntity, "invalid_argument", "invalid_status")

	// The missing rejection reason is the service's invalid_review_note.
	deps.knowledge.reviewErr = domain.NewInvalid("reason", "invalid_review_note", "reason is required to reject")
	recorder = doRequest(handler, http.MethodPost, "/api/versions/100/knowledge/6000/review", testAdminToken,
		`{"revision":1,"status":"rejected"}`)
	requireError(t, recorder, http.StatusUnprocessableEntity, "invalid_argument", "invalid_review_note")

	// A stale revision surfaces the CAS conflict.
	deps.knowledge.reviewErr = domain.NewStateConflict("knowledge_revision_conflict", "revision changed")
	recorder = doRequest(handler, http.MethodPost, "/api/versions/100/knowledge/6000/review", testAdminToken,
		`{"revision":1,"status":"rejected","reason":"duplicate"}`)
	requireError(t, recorder, http.StatusConflict, "conflict", "knowledge_revision_conflict")
}

// TestKnowledgeRoutesRequireAuthentication pins the global bearer middleware for
// every knowledge route.
func TestKnowledgeRoutesRequireAuthentication(t *testing.T) {
	handler, _ := newTestServer(t)
	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/versions/100/knowledge"},
		{http.MethodPost, "/api/versions/100/knowledge"},
		{http.MethodPatch, "/api/versions/100/knowledge/6000"},
		{http.MethodPost, "/api/versions/100/knowledge/6000/review"},
		{http.MethodGet, "/api/knowledge"},
	} {
		recorder := doRequest(handler, request.method, request.path, "", "{}")
		requireStatus(t, recorder, http.StatusUnauthorized)
	}
}

// compile-time proof the fake implements the consumer port.
var _ KnowledgeService = (*fakeKnowledgeService)(nil)
