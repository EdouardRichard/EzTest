package knowledgeapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

/* ---------------------------------- fakes --------------------------------- */

type fakeRepo struct {
	items     map[int64]knowledge.Item
	revisions map[int64][]knowledge.Revision

	getErr error

	listItems  []knowledge.Item
	listFilter knowledge.Filter
	listErr    error

	insertedItem     knowledge.Item
	insertedRevision knowledge.Revision
	insertCalls      int
	insertErr        error

	updatedItem     knowledge.Item
	updatedRevision knowledge.Revision
	updateCalls     int
	updateErr       error

	statusItem knowledge.Item
	statusFrom knowledge.Status
	statusErr  error

	revisionItems []knowledge.Revision
	revisionErr   error

	injectionItems []knowledge.Item
	injectionQuery knowledge.InjectionQuery
	injectionCalls int
	injectionErr   error
}

func (r *fakeRepo) Insert(_ context.Context, item knowledge.Item, revision knowledge.Revision) error {
	r.insertCalls++
	r.insertedItem = item
	r.insertedRevision = revision
	if r.insertErr != nil {
		return r.insertErr
	}
	if r.items == nil {
		r.items = map[int64]knowledge.Item{}
	}
	r.items[item.ID] = item
	return nil
}

func (r *fakeRepo) Get(_ context.Context, id int64) (knowledge.Item, error) {
	if r.getErr != nil {
		return knowledge.Item{}, r.getErr
	}
	item, ok := r.items[id]
	if !ok {
		return knowledge.Item{}, knowledge.ErrNotFound
	}
	return item, nil
}

func (r *fakeRepo) List(_ context.Context, filter knowledge.Filter) ([]knowledge.Item, error) {
	r.listFilter = filter
	return r.listItems, r.listErr
}

func (r *fakeRepo) Update(_ context.Context, item knowledge.Item, revision knowledge.Revision) error {
	r.updateCalls++
	r.updatedItem = item
	r.updatedRevision = revision
	return r.updateErr
}

func (r *fakeRepo) UpdateStatus(_ context.Context, item knowledge.Item, from knowledge.Status) error {
	r.statusItem = item
	r.statusFrom = from
	return r.statusErr
}

func (r *fakeRepo) ListRevisions(_ context.Context, _ int64) ([]knowledge.Revision, error) {
	return r.revisionItems, r.revisionErr
}

func (r *fakeRepo) ListForInjection(_ context.Context, query knowledge.InjectionQuery) ([]knowledge.Item, error) {
	r.injectionCalls++
	r.injectionQuery = query
	return r.injectionItems, r.injectionErr
}

type fakeUsage struct {
	batches [][]knowledge.Usage
	err     error
}

func (u *fakeUsage) Insert(_ context.Context, usages []knowledge.Usage) error {
	u.batches = append(u.batches, usages)
	return u.err
}

type fakeSettings struct {
	ints    map[string]int64
	bools   map[string]bool
	intErr  error
	boolErr error
}

func (s *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	if s.intErr != nil {
		return 0, s.intErr
	}
	value, ok := s.ints[key]
	if !ok {
		return 0, errors.New("setting not found: " + key)
	}
	return value, nil
}

func (s *fakeSettings) Bool(_ context.Context, key string) (bool, error) {
	if s.boolErr != nil {
		return false, s.boolErr
	}
	value, ok := s.bools[key]
	if !ok {
		return false, errors.New("setting not found: " + key)
	}
	return value, nil
}

type auditCall struct {
	action   string
	targetID int64
	metadata map[string]any
}

type fakeAudit struct {
	calls []auditCall
	err   error
}

func (a *fakeAudit) Record(_ context.Context, action string, targetID int64, metadata map[string]any) error {
	a.calls = append(a.calls, auditCall{action: action, targetID: targetID, metadata: metadata})
	return a.err
}

type fakeEvents struct {
	events []eventingapp.Event
	err    error
}

func (e *fakeEvents) Publish(_ context.Context, event eventingapp.Event) error {
	e.events = append(e.events, event)
	return e.err
}

type fakeSources struct {
	missing map[int64]bool
	err     error
	calls   []int64
}

func (s *fakeSources) SourceVersionExists(_ context.Context, versionID int64) (bool, error) {
	s.calls = append(s.calls, versionID)
	if s.err != nil {
		return false, s.err
	}
	return !s.missing[versionID], nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type seqIDs struct{ next int64 }

func (i *seqIDs) Next() int64 {
	i.next++
	return i.next
}

/* -------------------------------- fixture --------------------------------- */

type fixture struct {
	svc      *knowledgeapp.Service
	repo     *fakeRepo
	usage    *fakeUsage
	settings *fakeSettings
	audit    *fakeAudit
	events   *fakeEvents
	sources  *fakeSources
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		repo:     &fakeRepo{items: map[int64]knowledge.Item{}, revisions: map[int64][]knowledge.Revision{}},
		usage:    &fakeUsage{},
		settings: &fakeSettings{ints: map[string]int64{}, bools: map[string]bool{}},
		audit:    &fakeAudit{},
		events:   &fakeEvents{},
		sources:  &fakeSources{missing: map[int64]bool{}},
	}
	f.svc = knowledgeapp.NewService(f.repo, f.usage, f.settings, f.audit, f.events, f.sources,
		fixedClock{now: testNow}, &seqIDs{next: 1000})
	return f
}

func (f *fixture) useAdminOneStep(t *testing.T) {
	t.Helper()
	f.settings.bools[knowledgeapp.SettingReviewRequired] = false
}

func ptr[T any](value T) *T { return &value }

func draftItem(id int64, createdBy int64) knowledge.Item {
	return knowledge.Item{
		ID:        id,
		Scope:     knowledge.ScopeGlobal,
		Title:     "Retry contract",
		Content:   "Callback must be idempotent.",
		Tags:      []string{"payments"},
		Status:    knowledge.StatusDraft,
		Revision:  1,
		CreatedBy: ptr(createdBy),
		CreatedAt: testNow.Add(-time.Hour),
		UpdatedAt: testNow.Add(-time.Hour),
	}
}

func approvedItem(id int64, scope knowledge.Scope, systemKey string) knowledge.Item {
	item := knowledge.Item{
		ID:           id,
		Scope:        scope,
		Title:        "Retry contract",
		Content:      "Callback must be idempotent.",
		Tags:         []string{"payments"},
		Status:       knowledge.StatusApproved,
		Revision:     1,
		ReviewReason: nil,
		CreatedAt:    testNow.Add(-time.Hour),
		UpdatedAt:    testNow.Add(-time.Hour),
	}
	if scope == knowledge.ScopeSystem {
		item.SystemKey = ptr(systemKey)
	}
	return item
}

func assertAPIError(t *testing.T, err error, status int, code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %d %s, got nil", status, code)
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %+v, want %d %s", apiErr, status, code)
	}
	if reason != "" {
		if apiErr.Details == nil || apiErr.Details["reason"] != reason {
			t.Fatalf("error details = %#v, want reason %q", apiErr.Details, reason)
		}
	}
}

/* --------------------------------- create --------------------------------- */

func TestCreateDraftPublishesProposedEventAndAudit(t *testing.T) {
	f := newFixture(t)
	item, err := f.svc.Create(context.Background(), platform.Actor{ID: 7, Role: "user"},
		knowledgeapp.CreateInput{
			Title:   "  Retry contract  ",
			Content: "  Callback must be idempotent.  ",
			Tags:    []string{" payments ", "payments", "callback"},
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.Status != knowledge.StatusDraft || item.Revision != 1 || item.Scope != knowledge.ScopeGlobal {
		t.Fatalf("item = %+v", item)
	}
	if item.Title != "Retry contract" || item.Content != "Callback must be idempotent." {
		t.Fatalf("trimmed content = %q / %q", item.Title, item.Content)
	}
	if len(item.Tags) != 2 || item.Tags[0] != "payments" || item.Tags[1] != "callback" {
		t.Fatalf("tags = %#v", item.Tags)
	}
	if item.CreatedBy == nil || *item.CreatedBy != 7 || !item.CreatedAt.Equal(testNow) {
		t.Fatalf("audit fields = %+v", item)
	}
	if f.repo.insertCalls != 1 {
		t.Fatalf("insert calls = %d", f.repo.insertCalls)
	}
	if f.repo.insertedRevision.Revision != 1 || f.repo.insertedRevision.KnowledgeID != item.ID ||
		f.repo.insertedRevision.ChangedBy == nil || *f.repo.insertedRevision.ChangedBy != 7 {
		t.Fatalf("initial revision = %+v", f.repo.insertedRevision)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "knowledge.proposed" ||
		f.events.events[0].Channel != "system" {
		t.Fatalf("events = %+v", f.events.events)
	}
	published := f.events.events[0]
	if published.Payload["knowledge_id"] != knowledgeIDString(item.ID) || published.Payload["scope"] != "global" {
		t.Fatalf("proposed payload = %#v", published.Payload)
	}
	if _, leaked := published.Payload["content"]; leaked {
		t.Fatalf("payload must not contain the content: %#v", published.Payload)
	}
	if len(f.audit.calls) != 1 || f.audit.calls[0].action != "knowledge.create" || f.audit.calls[0].targetID != item.ID {
		t.Fatalf("audit = %+v", f.audit.calls)
	}
	if f.audit.calls[0].metadata["knowledge_id"] != knowledgeIDString(item.ID) {
		t.Fatalf("audit metadata = %#v", f.audit.calls[0].metadata)
	}
}

func TestCreateAdminOneStepApprovesWhenReviewNotRequired(t *testing.T) {
	f := newFixture(t)
	f.useAdminOneStep(t)
	item, err := f.svc.Create(context.Background(), platform.Actor{ID: 88, Role: "admin"},
		knowledgeapp.CreateInput{Title: "Approved knowledge", Content: "Body"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.Status != knowledge.StatusApproved || item.ReviewedBy == nil || *item.ReviewedBy != 88 ||
		item.ReviewedAt == nil {
		t.Fatalf("item = %+v", item)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "knowledge.approved" {
		t.Fatalf("events = %+v", f.events.events)
	}
	if f.events.events[0].Payload["reviewer_id"] != "88" {
		t.Fatalf("approved payload = %#v", f.events.events[0].Payload)
	}
}

func TestCreateNonAdminStaysDraftWhenReviewNotRequired(t *testing.T) {
	f := newFixture(t)
	f.useAdminOneStep(t)
	item, err := f.svc.Create(context.Background(), platform.Actor{ID: 7, Role: "user"},
		knowledgeapp.CreateInput{Title: "AI proposal", Content: "Body"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.Status != knowledge.StatusDraft {
		t.Fatalf("status = %s, want draft", item.Status)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "knowledge.proposed" {
		t.Fatalf("events = %+v", f.events.events)
	}
}

func TestCreateReviewSettingErrorFailsClosedToDraft(t *testing.T) {
	f := newFixture(t)
	f.settings.boolErr = errors.New("settings down")
	item, err := f.svc.Create(context.Background(), platform.Actor{ID: 88, Role: "admin"},
		knowledgeapp.CreateInput{Title: "Knowledge", Content: "Body"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.Status != knowledge.StatusDraft {
		t.Fatalf("status = %s, want draft (fail closed)", item.Status)
	}
}

func TestCreateDerivesSystemScopeAndPromotedVersion(t *testing.T) {
	f := newFixture(t)
	item, err := f.svc.Create(context.Background(), platform.Actor{ID: 7, Role: "user"},
		knowledgeapp.CreateInput{
			Title:           "Payments retry",
			Content:         "Body",
			SystemKey:       ptr(" payments "),
			SourceVersionID: ptr[int64](500),
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.Scope != knowledge.ScopeSystem || item.SystemKey == nil || *item.SystemKey != "payments" {
		t.Fatalf("scope = %s key = %v", item.Scope, item.SystemKey)
	}
	if item.SourceVersionID == nil || *item.SourceVersionID != 500 {
		t.Fatalf("source version = %v", item.SourceVersionID)
	}
	if item.PromotedFromVersionID == nil || *item.PromotedFromVersionID != 500 {
		t.Fatalf("promoted version = %v", item.PromotedFromVersionID)
	}
	if f.events.events[0].Payload["source_version_id"] != "500" {
		t.Fatalf("payload = %#v", f.events.events[0].Payload)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input knowledgeapp.CreateInput
		field string
	}{
		{name: "missing title", input: knowledgeapp.CreateInput{Content: "Body"}, field: "title"},
		{name: "long title", input: knowledgeapp.CreateInput{Title: strings.Repeat("a", 201), Content: "Body"}, field: "title"},
		{name: "missing content", input: knowledgeapp.CreateInput{Title: "Title"}, field: "content"},
		{name: "long content", input: knowledgeapp.CreateInput{Title: "Title", Content: strings.Repeat("a", 20001)}, field: "content"},
		{name: "21 tags", input: knowledgeapp.CreateInput{Title: "Title", Content: "Body", Tags: manyTags(21)}, field: "tags"},
		{name: "system without key", input: knowledgeapp.CreateInput{Title: "Title", Content: "Body", Scope: knowledge.ScopeSystem}, field: "system_key"},
		{name: "global with key", input: knowledgeapp.CreateInput{Title: "Title", Content: "Body", Scope: knowledge.ScopeGlobal, SystemKey: ptr("payments")}, field: "system_key"},
		{name: "invalid scope", input: knowledgeapp.CreateInput{Title: "Title", Content: "Body", Scope: knowledge.Scope("tenant")}, field: "scope"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.svc.Create(context.Background(), platform.Actor{ID: 7, Role: "user"}, test.input)
			if apiErr := platform.AsError(err); apiErr.Status != 400 || apiErr.Details["field"] != test.field {
				t.Fatalf("error = %+v, want invalid %s", apiErr, test.field)
			}
			if f.repo.insertCalls != 0 || len(f.events.events) != 0 || len(f.audit.calls) != 0 {
				t.Fatalf("rejected create must not write: insert=%d events=%d audit=%d",
					f.repo.insertCalls, len(f.events.events), len(f.audit.calls))
			}
		})
	}
}

func TestCreateSurvivesBestEffortAuditAndEventFailures(t *testing.T) {
	f := newFixture(t)
	f.audit.err = errors.New("audit down")
	f.events.err = errors.New("event down")
	if _, err := f.svc.Create(context.Background(), platform.Actor{ID: 7, Role: "user"},
		knowledgeapp.CreateInput{Title: "Title", Content: "Body"}); err != nil {
		t.Fatalf("create must not fail on audit/event errors: %v", err)
	}
}

/* --------------------------------- update --------------------------------- */

func TestUpdateAuthorEditsDraftAndAppendsRevision(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	updated, err := f.svc.Update(context.Background(), platform.Actor{ID: 7, Role: "user"}, 42,
		knowledgeapp.UpdateInput{Title: ptr("New title")})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Revision != 2 || updated.Title != "New title" || updated.Status != knowledge.StatusDraft {
		t.Fatalf("item = %+v", updated)
	}
	if f.repo.updateCalls != 1 {
		t.Fatalf("update calls = %d", f.repo.updateCalls)
	}
	if f.repo.updatedRevision.Revision != 2 || f.repo.updatedRevision.Title != "New title" ||
		f.repo.updatedRevision.KnowledgeID != 42 {
		t.Fatalf("revision = %+v", f.repo.updatedRevision)
	}
	if f.repo.updatedItem.Revision != 2 {
		t.Fatalf("stored revision = %d", f.repo.updatedItem.Revision)
	}
	if len(f.audit.calls) != 1 || f.audit.calls[0].action != "knowledge.edit" {
		t.Fatalf("audit = %+v", f.audit.calls)
	}
	changed, ok := f.audit.calls[0].metadata["changed_fields"].([]string)
	if !ok || len(changed) != 1 || changed[0] != "title" {
		t.Fatalf("changed fields = %#v", f.audit.calls[0].metadata["changed_fields"])
	}
	if len(f.events.events) != 0 {
		t.Fatalf("update must not publish an event: %+v", f.events.events)
	}
}

func TestUpdateAdminCanEditOthersDraft(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	if _, err := f.svc.Update(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42,
		knowledgeapp.UpdateInput{Content: ptr("Admin edited")}); err != nil {
		t.Fatalf("admin update: %v", err)
	}
}

func TestUpdateNonAuthorForbidden(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	_, err := f.svc.Update(context.Background(), platform.Actor{ID: 9, Role: "user"}, 42,
		knowledgeapp.UpdateInput{Title: ptr("Hijack")})
	assertAPIError(t, err, 403, "forbidden", "")
	if f.repo.updateCalls != 0 {
		t.Fatalf("update calls = %d", f.repo.updateCalls)
	}
}

func TestUpdateRejectedReturnsToDraftAndClearsReview(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.Status = knowledge.StatusRejected
	item.ReviewReason = ptr("needs sources")
	item.ReviewedBy = ptr[int64](88)
	item.ReviewedAt = ptr(testNow)
	f.repo.items[42] = item
	updated, err := f.svc.Update(context.Background(), platform.Actor{ID: 7, Role: "user"}, 42,
		knowledgeapp.UpdateInput{Content: ptr("New body")})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != knowledge.StatusDraft || updated.ReviewReason != nil ||
		updated.ReviewedBy != nil || updated.ReviewedAt != nil {
		t.Fatalf("item = %+v", updated)
	}
}

func TestUpdateApprovedIsAStateConflict(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.Status = knowledge.StatusApproved
	f.repo.items[42] = item
	_, err := f.svc.Update(context.Background(), platform.Actor{ID: 7, Role: "user"}, 42,
		knowledgeapp.UpdateInput{Title: ptr("Nope")})
	assertAPIError(t, err, 409, "state_conflict", "invalid_transition")
}

func TestUpdateNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Update(context.Background(), platform.Actor{ID: 7, Role: "user"}, 404,
		knowledgeapp.UpdateInput{Title: ptr("Missing")})
	assertAPIError(t, err, 404, "not_found", "")
}

func TestUpdateInvalidCombinationLeavesItemUntouched(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	_, err := f.svc.Update(context.Background(), platform.Actor{ID: 7, Role: "user"}, 42,
		knowledgeapp.UpdateInput{Scope: ptr(knowledge.ScopeSystem)})
	if apiErr := platform.AsError(err); apiErr.Status != 400 || apiErr.Details["field"] != "system_key" {
		t.Fatalf("error = %+v", apiErr)
	}
	if f.repo.updateCalls != 0 {
		t.Fatalf("update calls = %d", f.repo.updateCalls)
	}
}

func TestUpdateSwitchingToGlobalClearsSystemKey(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.Scope = knowledge.ScopeSystem
	item.SystemKey = ptr("payments")
	f.repo.items[42] = item
	updated, err := f.svc.Update(context.Background(), platform.Actor{ID: 7, Role: "user"}, 42,
		knowledgeapp.UpdateInput{Scope: ptr(knowledge.ScopeGlobal)})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Scope != knowledge.ScopeGlobal || updated.SystemKey != nil {
		t.Fatalf("item = %+v", updated)
	}
}

/* -------------------------------- review ---------------------------------- */

func TestApproveRequiresAdmin(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	_, err := f.svc.Approve(context.Background(), platform.Actor{ID: 7, Role: "user"}, 42, "")
	assertAPIError(t, err, 403, "forbidden", "")
	if len(f.events.events) != 0 {
		t.Fatalf("events = %+v", f.events.events)
	}
}

func TestApproveDraftPublishesApproved(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	item, err := f.svc.Approve(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if item.Status != knowledge.StatusApproved || item.ReviewedBy == nil || *item.ReviewedBy != 88 ||
		item.ReviewedAt == nil {
		t.Fatalf("item = %+v", item)
	}
	if f.repo.statusFrom != knowledge.StatusDraft {
		t.Fatalf("from status = %s", f.repo.statusFrom)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "knowledge.approved" {
		t.Fatalf("events = %+v", f.events.events)
	}
	if len(f.audit.calls) != 1 || f.audit.calls[0].action != "knowledge.approve" {
		t.Fatalf("audit = %+v", f.audit.calls)
	}
	if f.audit.calls[0].metadata["from_status"] != "draft" || f.audit.calls[0].metadata["to_status"] != "approved" {
		t.Fatalf("audit metadata = %#v", f.audit.calls[0].metadata)
	}
}

func TestApproveRevokedRestoresApproved(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.Status = knowledge.StatusRevoked
	item.RevokedAt = ptr(testNow.Add(-time.Hour))
	item.ReviewReason = ptr("outdated")
	f.repo.items[42] = item
	approved, err := f.svc.Approve(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != knowledge.StatusApproved || approved.RevokedAt != nil || approved.ReviewReason != nil {
		t.Fatalf("item = %+v", approved)
	}
}

func TestApproveRejectedIsAStateConflict(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.Status = knowledge.StatusRejected
	f.repo.items[42] = item
	_, err := f.svc.Approve(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, "")
	assertAPIError(t, err, 409, "state_conflict", "invalid_transition")
}

func TestRejectRequiresAdminAndReason(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	if _, err := f.svc.Reject(context.Background(), platform.Actor{ID: 7, Role: "user"}, 42, "no"); err == nil {
		t.Fatal("non-admin reject must fail")
	}
	_, err := f.svc.Reject(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, "   ")
	if apiErr := platform.AsError(err); apiErr.Status != 400 || apiErr.Details["field"] != "reason" {
		t.Fatalf("error = %+v", apiErr)
	}
	rejected, err := f.svc.Reject(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, " duplicated")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != knowledge.StatusRejected || rejected.ReviewReason == nil ||
		*rejected.ReviewReason != "duplicated" || rejected.ReviewedAt == nil {
		t.Fatalf("item = %+v", rejected)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "knowledge.rejected" ||
		f.events.events[0].Payload["reason"] != "duplicated" {
		t.Fatalf("events = %+v", f.events.events)
	}
	if len(f.audit.calls) != 1 || f.audit.calls[0].action != "knowledge.reject" {
		t.Fatalf("audit = %+v", f.audit.calls)
	}
}

func TestRejectApprovedIsAStateConflict(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.Status = knowledge.StatusApproved
	f.repo.items[42] = item
	_, err := f.svc.Reject(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, "reason")
	assertAPIError(t, err, 409, "state_conflict", "invalid_transition")
}

func TestRevokeApproved(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.Status = knowledge.StatusApproved
	f.repo.items[42] = item
	revoked, err := f.svc.Revoke(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, " obsolete ")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked.Status != knowledge.StatusRevoked || revoked.RevokedAt == nil ||
		revoked.ReviewReason == nil || *revoked.ReviewReason != "obsolete" {
		t.Fatalf("item = %+v", revoked)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "knowledge.revoked" {
		t.Fatalf("events = %+v", f.events.events)
	}
	if len(f.audit.calls) != 1 || f.audit.calls[0].action != "knowledge.revoke" {
		t.Fatalf("audit = %+v", f.audit.calls)
	}
}

func TestRevokeDraftIsAStateConflict(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	_, err := f.svc.Revoke(context.Background(), platform.Actor{ID: 88, Role: "admin"}, 42, "reason")
	assertAPIError(t, err, 409, "state_conflict", "invalid_transition")
}

/* ---------------------------------- list ---------------------------------- */

func TestListNormalizesLimitAndPaginates(t *testing.T) {
	f := newFixture(t)
	f.repo.listItems = []knowledge.Item{
		{ID: 10, Status: knowledge.StatusDraft},
		{ID: 9, Status: knowledge.StatusDraft},
		{ID: 8, Status: knowledge.StatusDraft},
	}
	page, err := f.svc.List(context.Background(), knowledgeapp.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if f.repo.listFilter.Limit != 3 {
		t.Fatalf("repository limit = %d, want 3", f.repo.listFilter.Limit)
	}
	if len(page.Items) != 2 || !page.HasMore || page.NextCursor != 9 {
		t.Fatalf("page = %+v", page)
	}
}

func TestListDefaultsToTwenty(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.List(context.Background(), knowledgeapp.ListFilter{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if f.repo.listFilter.Limit != 21 {
		t.Fatalf("repository limit = %d, want 21", f.repo.listFilter.Limit)
	}
	if f.repo.listFilter.Scope != "" || f.repo.listFilter.Status != "" {
		t.Fatalf("filter = %+v", f.repo.listFilter)
	}
}

func TestListPassesFiltersAndCapsLimit(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.List(context.Background(), knowledgeapp.ListFilter{
		Scope: knowledge.ScopeSystem, Status: knowledge.StatusApproved, SystemKey: " payments ",
		Tag: " callback ", Query: " retry ", Cursor: 50, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	filter := f.repo.listFilter
	if filter.Scope != knowledge.ScopeSystem || filter.Status != knowledge.StatusApproved ||
		filter.SystemKey != "payments" || filter.Tag != "callback" || filter.Query != "retry" ||
		filter.BeforeID != 50 || filter.Limit != 101 {
		t.Fatalf("filter = %+v", filter)
	}
}

func TestListRejectsInvalidFilters(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.List(context.Background(), knowledgeapp.ListFilter{Scope: knowledge.Scope("tenant")}); err == nil {
		t.Fatal("invalid scope must fail")
	}
	if _, err := f.svc.List(context.Background(), knowledgeapp.ListFilter{Status: knowledge.Status("pending")}); err == nil {
		t.Fatal("invalid status must fail")
	}
	if _, err := f.svc.List(context.Background(), knowledgeapp.ListFilter{Scope: knowledge.ScopeGlobal, SystemKey: "payments"}); err == nil {
		t.Fatal("global scope with system_key filter must fail")
	}
	if _, err := f.svc.List(context.Background(), knowledgeapp.ListFilter{Cursor: -1}); err == nil {
		t.Fatal("negative cursor must fail")
	}
}

/* ----------------------------------- get ---------------------------------- */

func TestGetMarksSourceStale(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.SourceVersionID = ptr[int64](500)
	f.repo.items[42] = item
	f.sources.missing[500] = true
	loaded, err := f.svc.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !loaded.SourceStale {
		t.Fatal("missing source version must mark source_stale")
	}
	f.sources.missing = map[int64]bool{}
	loaded, err = f.svc.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.SourceStale {
		t.Fatal("existing source version must not mark source_stale")
	}
}

func TestGetIgnoresSourceReaderErrorsAndNotFound(t *testing.T) {
	f := newFixture(t)
	item := draftItem(42, 7)
	item.SourceVersionID = ptr[int64](500)
	f.repo.items[42] = item
	f.sources.err = errors.New("version db down")
	loaded, err := f.svc.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.SourceStale {
		t.Fatal("undeterminable staleness must not be reported as stale")
	}
	_, err = f.svc.Get(context.Background(), 404)
	assertAPIError(t, err, 404, "not_found", "")
}

/* --------------------------------- revisions ------------------------------ */

func TestRevisionsChecksExistence(t *testing.T) {
	f := newFixture(t)
	f.repo.items[42] = draftItem(42, 7)
	f.repo.revisionItems = []knowledge.Revision{
		{ID: 2, KnowledgeID: 42, Revision: 2},
		{ID: 1, KnowledgeID: 42, Revision: 1},
	}
	revisions, err := f.svc.Revisions(context.Background(), 42)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revisions) != 2 || revisions[0].Revision != 2 {
		t.Fatalf("revisions = %+v", revisions)
	}
	_, err = f.svc.Revisions(context.Background(), 404)
	assertAPIError(t, err, 404, "not_found", "")
}

/* ---------------------------------- inject -------------------------------- */

func TestInjectFiltersStatusScopeAndStaleKeepingRankOrder(t *testing.T) {
	f := newFixture(t)
	keepGlobal := approvedItem(10, knowledge.ScopeGlobal, "")
	keepSystem := approvedItem(11, knowledge.ScopeSystem, "payments")
	otherSystem := approvedItem(12, knowledge.ScopeSystem, "design")
	rejected := approvedItem(13, knowledge.ScopeGlobal, "")
	rejected.Status = knowledge.StatusRejected
	stale := approvedItem(14, knowledge.ScopeGlobal, "")
	stale.SourceVersionID = ptr[int64](999)
	f.repo.injectionItems = []knowledge.Item{keepGlobal, keepSystem, otherSystem, rejected, stale}
	f.sources.missing[999] = true

	injected, err := f.svc.Inject(context.Background(), knowledgeapp.InjectInput{
		VersionID: 1, SystemKeys: []string{" Payments ", "payments"}, Query: " retry ",
	})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if len(injected) != 2 || injected[0].ID != 10 || injected[1].ID != 11 {
		t.Fatalf("injected = %+v", injected)
	}
	if injected[0].Tokens < 1 || injected[0].Snippet == "" || injected[0].Scope != knowledge.ScopeGlobal {
		t.Fatalf("injected[0] = %+v", injected[0])
	}
	query := f.repo.injectionQuery
	if query.Query != "retry" || query.Limit != 20 || len(query.SystemKeys) != 1 || query.SystemKeys[0] != "payments" {
		t.Fatalf("injection query = %+v", query)
	}
}

func TestInjectHonorsMaxItemsAndBudget(t *testing.T) {
	f := newFixture(t)
	f.settings.ints[knowledgeapp.SettingInjectionMaxItems] = 2
	f.settings.ints[knowledgeapp.SettingInjectionBudgetTokens] = 4
	first := approvedItem(10, knowledge.ScopeGlobal, "")
	first.Title = "t"
	first.Tags = nil
	first.Content = "xxxxxxxx" // (1 + 8) / 3 = 3 tokens
	second := approvedItem(11, knowledge.ScopeGlobal, "")
	second.Title = "t"
	second.Tags = nil
	second.Content = "xxxxxx" // (1 + 6) / 3 = 2 tokens -> exceeds the 4-token budget
	third := approvedItem(12, knowledge.ScopeGlobal, "")
	f.repo.injectionItems = []knowledge.Item{first, second, third}
	injected, err := f.svc.Inject(context.Background(), knowledgeapp.InjectInput{VersionID: 1})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if len(injected) != 1 || injected[0].ID != 10 {
		t.Fatalf("injected = %+v", injected)
	}
	if f.repo.injectionQuery.Limit != 20 {
		t.Fatalf("candidate limit = %d", f.repo.injectionQuery.Limit)
	}

	f2 := newFixture(t)
	f2.settings.ints[knowledgeapp.SettingInjectionMaxItems] = 1
	f2.repo.injectionItems = []knowledge.Item{approvedItem(10, knowledge.ScopeGlobal, ""), approvedItem(11, knowledge.ScopeGlobal, "")}
	injected, err = f2.svc.Inject(context.Background(), knowledgeapp.InjectInput{VersionID: 1})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if len(injected) != 1 || injected[0].ID != 10 {
		t.Fatalf("max items result = %+v", injected)
	}
}

func TestInjectDisabledByZeroMaxItems(t *testing.T) {
	f := newFixture(t)
	f.settings.ints[knowledgeapp.SettingInjectionMaxItems] = 0
	injected, err := f.svc.Inject(context.Background(), knowledgeapp.InjectInput{VersionID: 1})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if len(injected) != 0 {
		t.Fatalf("injected = %+v", injected)
	}
	if f.repo.injectionCalls != 0 {
		t.Fatalf("repository calls = %d", f.repo.injectionCalls)
	}
}

func TestInjectRejectsInvalidVersionID(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Inject(context.Background(), knowledgeapp.InjectInput{VersionID: 0})
	if apiErr := platform.AsError(err); apiErr.Status != 400 || apiErr.Details["field"] != "version_id" {
		t.Fatalf("error = %+v", apiErr)
	}
}

/* -------------------------------- usage ----------------------------------- */

func TestRecordUsageWritesOneRowPerInjectedItem(t *testing.T) {
	f := newFixture(t)
	err := f.svc.RecordUsage(context.Background(), 900, 901, []knowledgeapp.Injected{
		{ID: 10, Tokens: 3},
		{ID: 11, Tokens: 7},
	})
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if len(f.usage.batches) != 1 || len(f.usage.batches[0]) != 2 {
		t.Fatalf("batches = %+v", f.usage.batches)
	}
	first := f.usage.batches[0][0]
	if first.KnowledgeID != 10 || first.AgentRunID == nil || *first.AgentRunID != 900 ||
		first.WorkItemID == nil || *first.WorkItemID != 901 || first.TokenEstimate != 3 ||
		!first.InjectedAt.Equal(testNow) {
		t.Fatalf("usage row = %+v", first)
	}
	if first.ID == 0 || f.usage.batches[0][0].ID == f.usage.batches[0][1].ID {
		t.Fatalf("usage ids = %d / %d", f.usage.batches[0][0].ID, f.usage.batches[0][1].ID)
	}
}

func TestRecordUsageEmptyIsANoop(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordUsage(context.Background(), 900, 0, nil); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if len(f.usage.batches) != 0 {
		t.Fatalf("batches = %+v", f.usage.batches)
	}
}

func TestRecordUsageWithoutRepositoryIsUnavailable(t *testing.T) {
	svc := knowledgeapp.NewService(nil, nil, nil, nil, nil, nil, fixedClock{now: testNow}, &seqIDs{next: 1000})
	if err := svc.RecordUsage(context.Background(), 900, 0, []knowledgeapp.Injected{{ID: 10, Tokens: 1}}); err == nil {
		t.Fatal("nil usage repository must fail")
	}
}

func manyTags(count int) []string {
	tags := make([]string, 0, count)
	for i := 0; i < count; i++ {
		tags = append(tags, "tag-"+string(rune('a'+i)))
	}
	return tags
}

func knowledgeIDString(id int64) string {
	return platformid.Format(id)
}
