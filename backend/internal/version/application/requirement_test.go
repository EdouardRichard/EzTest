package versionapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- fake requirement repository ----------

type fakeRequirementRepo struct {
	requirements  map[int64]version.Requirement
	versions      map[int64][]version.RequirementVersion // requirement ID -> revisions, newest first
	confirmations map[int64][]version.Confirmation       // requirement ID -> confirmations
	relations     []version.Relation
	attachments   []version.Attachment
	addErr        error
}

func newFakeRequirementRepo() *fakeRequirementRepo {
	return &fakeRequirementRepo{
		requirements:  map[int64]version.Requirement{},
		versions:      map[int64][]version.RequirementVersion{},
		confirmations: map[int64][]version.Confirmation{},
	}
}

func (f *fakeRequirementRepo) Create(_ context.Context, requirement version.Requirement, revision version.RequirementVersion) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.requirements[requirement.ID] = requirement
	f.versions[requirement.ID] = []version.RequirementVersion{revision}
	return nil
}

func (f *fakeRequirementRepo) Get(_ context.Context, versionID, requirementID int64) (version.RequirementAggregate, error) {
	requirement, ok := f.requirements[requirementID]
	if !ok || requirement.VersionID != versionID {
		return version.RequirementAggregate{}, version.ErrRequirementNotFound
	}
	aggregate := version.RequirementAggregate{
		Requirement:   requirement,
		Versions:      append([]version.RequirementVersion{}, f.versions[requirementID]...),
		Confirmations: append([]version.Confirmation{}, f.confirmations[requirementID]...),
	}
	for _, relation := range f.relations {
		if relation.VersionID == versionID && (relation.FromRequirementID == requirementID || relation.ToRequirementID == requirementID) {
			aggregate.Relations = append(aggregate.Relations, relation)
		}
	}
	for _, attachment := range f.attachments {
		if attachment.VersionID == versionID && attachment.RequirementID != nil && *attachment.RequirementID == requirementID {
			aggregate.Attachments = append(aggregate.Attachments, attachment)
		}
	}
	return aggregate, nil
}

func (f *fakeRequirementRepo) GetMany(_ context.Context, versionID int64, ids []int64) ([]version.RequirementWithCurrent, error) {
	items := make([]version.RequirementWithCurrent, 0, len(ids))
	for _, id := range ids {
		requirement, ok := f.requirements[id]
		if !ok || requirement.VersionID != versionID {
			return nil, version.ErrRequirementNotFound
		}
		current, ok := f.currentVersion(requirement)
		if !ok {
			return nil, version.ErrRequirementNotFound
		}
		items = append(items, version.RequirementWithCurrent{Requirement: requirement, Current: current})
	}
	return items, nil
}

func (f *fakeRequirementRepo) List(_ context.Context, versionID int64, status version.RequirementStatus, limit int, beforeID *int64) ([]version.RequirementListItem, error) {
	items := make([]version.RequirementListItem, 0)
	for _, requirement := range f.requirements {
		if requirement.VersionID != versionID {
			continue
		}
		if status != "" && requirement.Status != status {
			continue
		}
		if beforeID != nil && requirement.ID >= *beforeID {
			continue
		}
		current, ok := f.currentVersion(requirement)
		if !ok {
			continue
		}
		item := version.RequirementListItem{Requirement: requirement, Current: current}
		for _, confirmation := range f.confirmations[requirement.ID] {
			if confirmation.RequirementVersionID == current.ID {
				confirmedAt := confirmation.ConfirmedAt
				item.ConfirmedAt = &confirmedAt
				break
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Requirement.ID > items[j].Requirement.ID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (f *fakeRequirementRepo) AddRevision(_ context.Context, requirement version.Requirement, revision version.RequirementVersion) error {
	if f.addErr != nil {
		return f.addErr
	}
	stored, ok := f.requirements[requirement.ID]
	if !ok || stored.VersionID != requirement.VersionID {
		return version.ErrRequirementNotFound
	}
	if stored.Status == version.RequirementClosed || stored.Status == version.RequirementObsolete {
		return version.ErrStateConflict
	}
	f.requirements[requirement.ID] = requirement
	f.versions[requirement.ID] = append([]version.RequirementVersion{revision}, f.versions[requirement.ID]...)
	return nil
}

func (f *fakeRequirementRepo) Confirm(_ context.Context, requirement version.Requirement, confirmation version.Confirmation) error {
	stored, ok := f.requirements[requirement.ID]
	if !ok || stored.VersionID != requirement.VersionID {
		return version.ErrRequirementNotFound
	}
	if stored.Status != version.RequirementDraft {
		return version.ErrStateConflict
	}
	f.requirements[requirement.ID] = requirement
	f.confirmations[requirement.ID] = append(f.confirmations[requirement.ID], confirmation)
	return nil
}

func (f *fakeRequirementRepo) BatchConfirm(_ context.Context, requirements []version.Requirement, confirmations []version.Confirmation) error {
	for _, requirement := range requirements {
		stored, ok := f.requirements[requirement.ID]
		if !ok || stored.Status != version.RequirementDraft {
			return version.ErrStateConflict
		}
	}
	for _, requirement := range requirements {
		f.requirements[requirement.ID] = requirement
	}
	for _, confirmation := range confirmations {
		for _, requirement := range requirements {
			if requirement.CurrentVersionID != nil && *requirement.CurrentVersionID == confirmation.RequirementVersionID {
				f.confirmations[requirement.ID] = append(f.confirmations[requirement.ID], confirmation)
			}
		}
	}
	return nil
}

func (f *fakeRequirementRepo) CreateRelation(_ context.Context, relation version.Relation) error {
	for _, existing := range f.relations {
		if existing.VersionID == relation.VersionID && existing.FromRequirementID == relation.FromRequirementID &&
			existing.ToRequirementID == relation.ToRequirementID && existing.Kind == relation.Kind {
			return version.ErrRelationDuplicate
		}
	}
	f.relations = append(f.relations, relation)
	return nil
}

func (f *fakeRequirementRepo) ListRelations(_ context.Context, versionID, requirementID int64) ([]version.Relation, error) {
	relations := make([]version.Relation, 0)
	for _, relation := range f.relations {
		if relation.VersionID == versionID && (relation.FromRequirementID == requirementID || relation.ToRequirementID == requirementID) {
			relations = append(relations, relation)
		}
	}
	return relations, nil
}

func (f *fakeRequirementRepo) currentVersion(requirement version.Requirement) (version.RequirementVersion, bool) {
	if requirement.CurrentVersionID == nil {
		return version.RequirementVersion{}, false
	}
	for _, revision := range f.versions[requirement.ID] {
		if revision.ID == *requirement.CurrentVersionID {
			return revision, true
		}
	}
	return version.RequirementVersion{}, false
}

// seedRequirement inserts a raw requirement aggregate, used to build invalid
// drafts that the service would normally reject.
func (f *fakeRequirementRepo) seedRequirement(requirement version.Requirement, revision version.RequirementVersion) {
	f.requirements[requirement.ID] = requirement
	f.versions[requirement.ID] = append([]version.RequirementVersion{revision}, f.versions[requirement.ID]...)
}

type staleCall struct {
	versionID     int64
	requirementID int64
	reason        string
	revision      int
}

type fakeStaleHook struct {
	calls    []staleCall
	affected []string
	err      error
}

func (f *fakeStaleHook) MarkRequirementChanged(_ context.Context, versionID, requirementID int64, reason string, revision int) ([]string, error) {
	f.calls = append(f.calls, staleCall{versionID: versionID, requirementID: requirementID, reason: reason, revision: revision})
	if f.err != nil {
		return nil, f.err
	}
	if f.affected == nil {
		return []string{}, nil
	}
	return f.affected, nil
}

// ---------- requirement test environment ----------

type requirementEnv struct {
	svc          *versionapp.RequirementService
	repo         *fakeRequirementRepo
	versionEnv   *testEnv
	versionID    int64
	versionSUTID string
	hook         *fakeStaleHook
	audit        *fakeAudit
	events       *fakePublisher
}

func newRequirementEnv(t *testing.T) *requirementEnv {
	t.Helper()
	base := newTestEnv(t)
	created := createVersion(t, base, "Requirement version")
	repo := newFakeRequirementRepo()
	hook := &fakeStaleHook{}
	audit := &fakeAudit{}
	events := &fakePublisher{}
	svc := versionapp.NewRequirementService(
		repo, base.svc, base.catalog, hook, audit, events, &seqIDs{}, fixedClock{at: testTime},
	)
	return &requirementEnv{
		svc: svc, repo: repo, versionEnv: base, versionID: created.ID,
		versionSUTID: idString(created.Bindings[0].ID),
		hook:         hook, audit: audit, events: events,
	}
}

func validRequirementDraft(env *requirementEnv) versionapp.RequirementDraft {
	return versionapp.RequirementDraft{
		Title:              "Checkout supports coupons",
		Description:        "Users can apply a coupon code at checkout.",
		AcceptanceCriteria: "A valid coupon reduces the order total.",
		Priority:           "P1",
		Constraints:        json.RawMessage(`{"business_rules":["one coupon per order"]}`),
		TargetVersionSUTs:  []string{env.versionSUTID},
		TargetPages:        []string{"1301"},
		TargetAPIs:         []string{"1401"},
	}
}

func createRequirement(t *testing.T, env *requirementEnv, title string) versionapp.RequirementDetail {
	t.Helper()
	draft := validRequirementDraft(env)
	draft.Title = title
	detail, err := env.svc.Create(requestContext(), env.versionID, draft, 42)
	if err != nil {
		t.Fatalf("Create(%q): %v", title, err)
	}
	return detail
}

func requirementEventTypes(events []eventingapp.Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func auditActions(events []auditapp.Event) []string {
	actions := make([]string, 0, len(events))
	for _, event := range events {
		actions = append(actions, event.Action)
	}
	return actions
}

func TestRequirementCreatePersistsImmutableRevisionAndProjectsRunning(t *testing.T) {
	env := newRequirementEnv(t)
	draft := validRequirementDraft(env)
	draft.Title = "  Checkout supports coupons  "

	detail, err := env.svc.Create(requestContext(), env.versionID, draft, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if detail.ID <= 0 || detail.CurrentVersionID <= 0 {
		t.Fatalf("ids = %d/%d", detail.ID, detail.CurrentVersionID)
	}
	if detail.Status != "draft" || detail.Revision != 1 {
		t.Fatalf("status/revision = %q/%d", detail.Status, detail.Revision)
	}
	if detail.Title != "Checkout supports coupons" || detail.Priority != "P1" {
		t.Fatalf("title/priority = %q/%q", detail.Title, detail.Priority)
	}
	if detail.CurrentVersion.Title != "Checkout supports coupons" || detail.CurrentVersion.Revision != 1 {
		t.Fatalf("current version = %#v", detail.CurrentVersion)
	}
	if len(detail.CurrentVersion.ContentHash) != 64 {
		t.Fatalf("content hash = %q", detail.CurrentVersion.ContentHash)
	}
	if len(detail.History) != 0 || len(detail.Confirmations) != 0 || len(detail.Relations) != 0 || len(detail.Attachments) != 0 {
		t.Fatalf("detail should start empty: %#v", detail)
	}
	if detail.ConfirmedAt != nil {
		t.Fatalf("confirmed_at = %v", detail.ConfirmedAt)
	}
	if len(detail.TargetVersionSUTs) != 1 || detail.TargetVersionSUTs[0] != env.versionSUTID {
		t.Fatalf("targets = %#v", detail.TargetVersionSUTs)
	}

	// The version is projected to running (best effort, idempotent).
	versionView, err := env.versionEnv.svc.Get(requestContext(), env.versionID)
	if err != nil {
		t.Fatalf("version Get: %v", err)
	}
	if versionView.Status != version.StatusRunning {
		t.Fatalf("version status = %q, want running", versionView.Status)
	}
	runningEvents := 0
	for _, event := range env.versionEnv.publisher.events {
		if event.Type == "version.started" {
			runningEvents++
		}
	}
	if runningEvents != 2 {
		t.Fatalf("version.started events = %d, want 2 channels", runningEvents)
	}

	if got := requirementEventTypes(env.events.events); !containsAll(got, "requirement.created", "requirement.version_created") {
		t.Fatalf("events = %#v", got)
	}
	for _, event := range env.events.events {
		if event.Channel != "version:"+idString(env.versionID) {
			t.Fatalf("event channel = %q", event.Channel)
		}
		if event.Scope["version_id"] != idString(env.versionID) || event.Scope["requirement_id"] != idString(detail.ID) {
			t.Fatalf("event scope = %#v", event.Scope)
		}
	}
	created := env.events.events[0]
	if created.Type != "requirement.created" || created.Payload["status"] != "draft" || created.Payload["title"] != "Checkout supports coupons" {
		t.Fatalf("created event = %#v", created)
	}
	versionCreated := env.events.events[1]
	if versionCreated.Type != "requirement.version_created" {
		t.Fatalf("version event = %#v", versionCreated)
	}
	if versionCreated.Payload["revision"] != 1 || versionCreated.Payload["requirement_version_id"] != idString(detail.CurrentVersionID) {
		t.Fatalf("version payload = %#v", versionCreated.Payload)
	}
	if !containsAll(auditActions(env.audit.events), "requirement.created") {
		t.Fatalf("audit = %#v", auditActions(env.audit.events))
	}
}

func containsAll(values []string, wanted ...string) bool {
	for _, want := range wanted {
		found := false
		for _, value := range values {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestRequirementCreateValidatesContentAndTargets(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*versionapp.RequirementDraft)
		wantField string
		wantCode  int
	}{
		{"blank title", func(d *versionapp.RequirementDraft) { d.Title = "  " }, "title", 400},
		{"bad priority", func(d *versionapp.RequirementDraft) { d.Priority = "P9" }, "priority", 400},
		{"bad constraints", func(d *versionapp.RequirementDraft) { d.Constraints = json.RawMessage(`[1]`) }, "constraints", 400},
		{"unknown version sut", func(d *versionapp.RequirementDraft) { d.TargetVersionSUTs = []string{"9999"} }, "targets.version_suts", 400},
		{"page outside version scope", func(d *versionapp.RequirementDraft) { d.TargetPages = []string{"1302"} }, "targets.pages", 400},
		{"unknown api", func(d *versionapp.RequirementDraft) { d.TargetAPIs = []string{"9999"} }, "targets.apis", 400},
		{"non decimal target", func(d *versionapp.RequirementDraft) { d.TargetPages = []string{"page-a"} }, "targets.pages", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRequirementEnv(t)
			draft := validRequirementDraft(env)
			tc.mutate(&draft)
			_, err := env.svc.Create(requestContext(), env.versionID, draft, 42)
			if err == nil {
				t.Fatal("Create accepted invalid input")
			}
			apiErr := apiError(t, err)
			if apiErr.Status != tc.wantCode {
				t.Fatalf("status = %d, want %d (%v)", apiErr.Status, tc.wantCode, err)
			}
			if field, _ := apiErr.Details["field"].(string); field != tc.wantField {
				t.Fatalf("field = %q, want %q", field, tc.wantField)
			}
			if len(env.repo.requirements) != 0 || len(env.events.events) != 0 || len(env.audit.events) != 0 {
				t.Fatal("invalid input persisted side effects")
			}
		})
	}

	env := newRequirementEnv(t)
	if _, err := env.svc.Create(requestContext(), 9999, validRequirementDraft(env), 42); err == nil {
		t.Fatal("missing version accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestRequirementUpdateCreatesRevisionAndReopensClarified(t *testing.T) {
	env := newRequirementEnv(t)
	created := createRequirement(t, env, "Original title")

	confirmed, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !confirmed.Confirmed || confirmed.Requirement.Status != "clarified" || len(confirmed.Requirement.Confirmations) != 1 {
		t.Fatalf("confirm result = %#v", confirmed)
	}

	draft := validRequirementDraft(env)
	draft.Title = "Edited title"
	draft.Priority = "P0"
	updated, err := env.svc.Update(requestContext(), env.versionID, created.ID, draft, "scope clarified", 7)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != "draft" || updated.Revision != 2 || updated.CurrentVersionID == created.CurrentVersionID {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.Title != "Edited title" || updated.Priority != "P0" {
		t.Fatalf("content = %#v", updated)
	}
	if len(updated.History) != 1 || updated.History[0].ID != created.CurrentVersionID || updated.History[0].Title != "Original title" {
		t.Fatalf("history = %#v", updated.History)
	}
	if len(updated.Confirmations) != 1 || updated.Confirmations[0].RequirementVersionID != created.CurrentVersionID {
		t.Fatalf("confirmations should stay on the old revision: %#v", updated.Confirmations)
	}
	if updated.ConfirmedAt != nil {
		t.Fatalf("reopened requirement still reports confirmed_at = %v", updated.ConfirmedAt)
	}

	types := requirementEventTypes(env.events.events)
	if countOf(types, "requirement.version_created") != 2 {
		t.Fatalf("version_created events = %d", countOf(types, "requirement.version_created"))
	}
	if !containsAll(auditActions(env.audit.events), "requirement.confirmed", "requirement.updated") {
		t.Fatalf("audit = %#v", auditActions(env.audit.events))
	}
	updatedAudit := env.audit.events[len(env.audit.events)-1]
	if updatedAudit.ActorID == nil || *updatedAudit.ActorID != 7 || updatedAudit.Metadata["reason"] != "scope clarified" {
		t.Fatalf("audit = %#v", updatedAudit)
	}

	// Editing a plain draft keeps it a draft and increments the revision.
	draft.Title = "Second edit"
	second, err := env.svc.Update(requestContext(), env.versionID, created.ID, draft, "", 7)
	if err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if second.Status != "draft" || second.Revision != 3 || len(second.History) != 2 {
		t.Fatalf("second revision = %#v", second)
	}

	if _, err := env.svc.Update(requestContext(), env.versionID+1, created.ID, draft, "", 7); err == nil {
		t.Fatal("cross-version update accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("cross-version status = %d", apiError(t, err).Status)
	}
}

func countOf(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func TestRequirementConfirmIsIdempotentAndFrozen(t *testing.T) {
	env := newRequirementEnv(t)
	created := createRequirement(t, env, "Confirm me")

	first, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !first.Confirmed {
		t.Fatal("first confirmation reported confirmed=false")
	}
	if first.Requirement.Status != "clarified" || first.Requirement.ConfirmedAt == nil {
		t.Fatalf("requirement = %#v", first.Requirement)
	}
	if len(first.Requirement.Confirmations) != 1 {
		t.Fatalf("confirmations = %#v", first.Requirement.Confirmations)
	}
	confirmation := first.Requirement.Confirmations[0]
	if confirmation.Scope != "requirement" || confirmation.ActorID != 42 || confirmation.BatchID != nil {
		t.Fatalf("confirmation = %#v", confirmation)
	}
	if confirmation.RequirementVersionID != created.CurrentVersionID {
		t.Fatalf("confirmation version = %d", confirmation.RequirementVersionID)
	}

	eventsBefore := len(env.events.events)
	auditBefore := len(env.audit.events)
	second, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 43)
	if err != nil {
		t.Fatalf("second Confirm: %v", err)
	}
	if second.Confirmed {
		t.Fatal("second confirmation reported confirmed=true")
	}
	if len(second.Requirement.Confirmations) != 1 {
		t.Fatalf("second confirmation duplicated the record: %#v", second.Requirement.Confirmations)
	}
	if len(env.events.events) != eventsBefore || len(env.audit.events) != auditBefore {
		t.Fatal("idempotent confirmation emitted events/audit")
	}

	// The frozen revision cannot be modified; editing produces a new draft.
	current := created.CurrentVersion
	draft := validRequirementDraft(env)
	draft.Title = "After confirmation"
	updated, err := env.svc.Update(requestContext(), env.versionID, created.ID, draft, "", 42)
	if err != nil {
		t.Fatalf("Update after confirm: %v", err)
	}
	if updated.Revision != 2 || updated.Status != "draft" {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.History[0].Title != current.Title {
		t.Fatalf("old revision changed: %#v", updated.History[0])
	}

	// A missing requirement is a 404.
	if _, err := env.svc.Confirm(requestContext(), env.versionID, 9999, 42); err == nil {
		t.Fatal("missing requirement confirmed")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

func TestRequirementBatchConfirmAllOrNothing(t *testing.T) {
	env := newRequirementEnv(t)
	first := createRequirement(t, env, "First")
	second := createRequirement(t, env, "Second")
	third := createRequirement(t, env, "Third")

	if _, err := env.svc.Confirm(requestContext(), env.versionID, third.ID, 42); err != nil {
		t.Fatalf("Confirm third: %v", err)
	}

	result, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{first.ID, second.ID, third.ID, first.ID}, 42)
	if err != nil {
		t.Fatalf("BatchConfirm: %v", err)
	}
	if result.BatchID == "" {
		t.Fatal("batch id is empty")
	}
	if len(result.Confirmed) != 2 || result.Confirmed[0] != first.ID || result.Confirmed[1] != second.ID {
		t.Fatalf("confirmed = %#v", result.Confirmed)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != third.ID {
		t.Fatalf("skipped = %#v", result.Skipped)
	}
	for _, id := range []int64{first.ID, second.ID} {
		requirement := env.repo.requirements[id]
		if requirement.Status != version.RequirementClarified {
			t.Fatalf("requirement %d status = %q", id, requirement.Status)
		}
		for _, confirmation := range env.repo.confirmations[id] {
			if confirmation.Scope != "batch" || confirmation.BatchID == nil || *confirmation.BatchID != result.BatchID {
				t.Fatalf("confirmation = %#v", confirmation)
			}
			if len(confirmation.AffectedRequirementIDs) != 2 {
				t.Fatalf("affected ids = %#v", confirmation.AffectedRequirementIDs)
			}
		}
	}
	clarifiedEvents := 0
	for _, event := range env.events.events {
		if event.Type == "requirement.clarified" && event.Payload["scope"] == "batch" {
			clarifiedEvents++
			if event.Payload["scope"] != "batch" || event.Payload["batch_id"] != result.BatchID {
				t.Fatalf("clarified payload = %#v", event.Payload)
			}
		}
	}
	if clarifiedEvents != 2 {
		t.Fatalf("clarified events = %d", clarifiedEvents)
	}

	// A second batch with only already-confirmed requirements skips everything.
	allSkipped, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{first.ID, third.ID}, 42)
	if err != nil {
		t.Fatalf("BatchConfirm all skipped: %v", err)
	}
	if len(allSkipped.Confirmed) != 0 || len(allSkipped.Skipped) != 2 {
		t.Fatalf("result = %#v", allSkipped)
	}
}

func TestRequirementBatchConfirmRejectsInvalidDraftAtomically(t *testing.T) {
	env := newRequirementEnv(t)
	good := createRequirement(t, env, "Good")
	badID := int64(9001)
	badVersionID := int64(9002)
	env.repo.seedRequirement(version.Requirement{
		ID: badID, VersionID: env.versionID, CurrentVersionID: &badVersionID,
		Status: version.RequirementDraft, CreatedAt: testTime, UpdatedAt: testTime,
		TargetVersionSUTIDs: []string{},
	}, version.RequirementVersion{
		ID: badVersionID, RequirementID: badID, Revision: 1,
		Title: "", Description: "", AcceptanceCriteria: "", Priority: "P2",
		Constraints: json.RawMessage(`{}`), ContentHash: strings.Repeat("0", 64),
		CreatedAt: testTime,
	})

	_, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{good.ID, badID}, 42)
	if err == nil {
		t.Fatal("batch with an invalid draft succeeded")
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusBadRequest {
		t.Fatalf("status = %d", apiErr.Status)
	}
	if apiErr.Details["requirement_id"] != idString(badID) {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	if env.repo.requirements[good.ID].Status != version.RequirementDraft {
		t.Fatal("valid requirement was confirmed despite the failure")
	}

	if _, err := env.svc.BatchConfirm(requestContext(), env.versionID, nil, 42); err == nil {
		t.Fatal("empty batch accepted")
	}
	if _, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{good.ID, 9999}, 42); err == nil {
		t.Fatal("batch with a missing requirement accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("missing requirement status = %d", apiError(t, err).Status)
	}
}

func TestRequirementRollbackCreatesDraftRevisionAndCallsStaleHook(t *testing.T) {
	env := newRequirementEnv(t)
	env.hook.affected = []string{"casev_1"}
	created := createRequirement(t, env, "Rollback me")
	if _, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	rolled, err := env.svc.Rollback(requestContext(), env.versionID, created.ID, "AI proposal accepted", int64Ptr(555), 42)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.Status != "draft" || rolled.Revision != 2 || rolled.CurrentVersionID == created.CurrentVersionID {
		t.Fatalf("rolled = %#v", rolled)
	}
	if rolled.Title != created.Title || rolled.CurrentVersion.ContentHash != created.CurrentVersion.ContentHash {
		t.Fatalf("rollback did not copy the confirmed content: %#v", rolled)
	}
	if len(rolled.History) != 1 || rolled.History[0].ID != created.CurrentVersionID {
		t.Fatalf("history = %#v", rolled.History)
	}
	if len(rolled.Confirmations) != 1 {
		t.Fatalf("confirmations = %#v", rolled.Confirmations)
	}

	if len(env.hook.calls) != 1 {
		t.Fatalf("stale hook calls = %#v", env.hook.calls)
	}
	call := env.hook.calls[0]
	if call.versionID != env.versionID || call.requirementID != created.ID || call.reason != "AI proposal accepted" || call.revision != 2 {
		t.Fatalf("stale call = %#v", call)
	}

	var rolledEvent *eventingapp.Event
	var versionEvent *eventingapp.Event
	for i := range env.events.events {
		event := &env.events.events[i]
		switch event.Type {
		case "requirement.rolled_back":
			rolledEvent = event
		case "requirement.version_created":
			if event.Payload["revision"] == 2 {
				versionEvent = event
			}
		}
	}
	if rolledEvent == nil || versionEvent == nil {
		t.Fatalf("events = %#v", requirementEventTypes(env.events.events))
	}
	if rolledEvent.Payload["from_version_id"] != idString(created.CurrentVersionID) ||
		rolledEvent.Payload["new_version_id"] != idString(rolled.CurrentVersionID) ||
		rolledEvent.Payload["reason"] != "AI proposal accepted" {
		t.Fatalf("rolled event payload = %#v", rolledEvent.Payload)
	}
	affected, ok := rolledEvent.Payload["affected_case_version_ids"].([]string)
	if !ok || len(affected) != 1 || affected[0] != "casev_1" {
		t.Fatalf("affected = %#v", rolledEvent.Payload["affected_case_version_ids"])
	}
	if !containsAll(auditActions(env.audit.events), "requirement.rolled_back") {
		t.Fatalf("audit = %#v", auditActions(env.audit.events))
	}

	// Rolling back a draft is a state conflict.
	if _, err := env.svc.Rollback(requestContext(), env.versionID, created.ID, "", nil, 42); err == nil {
		t.Fatal("draft rollback accepted")
	} else if apiError(t, err).Status != http.StatusConflict {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
	if code := apiError(t, errOrNil(env.svc.Rollback(requestContext(), env.versionID, 9999, "", nil, 42))).Code; code != "not_found" {
		t.Fatalf("missing rollback code = %q", code)
	}
}

func errOrNil(_ versionapp.RequirementDetail, err error) error { return err }

func TestRequirementUpdateCallsStaleHookAndEventCarriesAffected(t *testing.T) {
	env := newRequirementEnv(t)
	env.hook.affected = []string{"7002", "7004"}
	created := createRequirement(t, env, "Edit me")
	if _, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	draft := validRequirementDraft(env)
	draft.Title = "Edited after confirmation"
	updated, err := env.svc.Update(requestContext(), env.versionID, created.ID, draft, "需求变化", 42)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Revision != 2 || updated.Status != "draft" {
		t.Fatalf("updated = %#v", updated)
	}

	if len(env.hook.calls) != 1 {
		t.Fatalf("stale hook calls = %#v", env.hook.calls)
	}
	call := env.hook.calls[0]
	if call.versionID != env.versionID || call.requirementID != created.ID || call.reason != "需求变化" || call.revision != 2 {
		t.Fatalf("stale call = %#v", call)
	}

	var versionEvent *eventingapp.Event
	for i := range env.events.events {
		event := &env.events.events[i]
		if event.Type != "requirement.version_created" || event.Payload["revision"] != 2 {
			continue
		}
		versionEvent = event
	}
	if versionEvent == nil {
		t.Fatalf("events = %#v", requirementEventTypes(env.events.events))
	}
	affected, ok := versionEvent.Payload["affected_case_version_ids"].([]string)
	if !ok || len(affected) != 2 || affected[0] != "7002" || affected[1] != "7004" {
		t.Fatalf("affected = %#v", versionEvent.Payload["affected_case_version_ids"])
	}
}

func TestRequirementUpdateStaleHookFailureIsReported(t *testing.T) {
	env := newRequirementEnv(t)
	created := createRequirement(t, env, "Edit fails")
	draft := validRequirementDraft(env)
	draft.Title = "Edited"
	env.hook.err = errors.New("design store down")

	if _, err := env.svc.Update(requestContext(), env.versionID, created.ID, draft, "需求变化", 42); err == nil {
		t.Fatal("stale hook failure was ignored")
	}
	if env.repo.requirements[created.ID].CurrentVersionID == nil ||
		*env.repo.requirements[created.ID].CurrentVersionID == created.CurrentVersionID {
		t.Fatal("revision was not persisted before the hook failure")
	}
}

func TestRequirementRelationsValidateAndEmit(t *testing.T) {
	env := newRequirementEnv(t)
	from := createRequirement(t, env, "From")
	to := createRequirement(t, env, "To")

	relation, err := env.svc.CreateRelation(requestContext(), env.versionID, versionapp.RelationInput{
		FromRequirementID: from.ID, ToRequirementID: to.ID, Kind: "depends_on", Description: "needs coupons",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	if relation.ID <= 0 || relation.FromRequirementID != from.ID || relation.Kind != "depends_on" {
		t.Fatalf("relation = %#v", relation)
	}
	if !containsAll(requirementEventTypes(env.events.events), "requirement.relation_created") {
		t.Fatalf("events = %#v", requirementEventTypes(env.events.events))
	}
	lastEvent := env.events.events[len(env.events.events)-1]
	if lastEvent.Payload["from_requirement_id"] != idString(from.ID) || lastEvent.Payload["kind"] != "depends_on" {
		t.Fatalf("payload = %#v", lastEvent.Payload)
	}

	if _, err := env.svc.CreateRelation(requestContext(), env.versionID, versionapp.RelationInput{
		FromRequirementID: from.ID, ToRequirementID: to.ID, Kind: "depends_on",
	}, 42); err == nil {
		t.Fatal("duplicate relation accepted")
	} else if apiError(t, err).Status != http.StatusConflict {
		t.Fatalf("duplicate status = %d", apiError(t, err).Status)
	}
	if _, err := env.svc.CreateRelation(requestContext(), env.versionID, versionapp.RelationInput{
		FromRequirementID: from.ID, ToRequirementID: from.ID, Kind: "overlaps",
	}, 42); err == nil {
		t.Fatal("self relation accepted")
	} else if field, _ := apiError(t, err).Details["field"].(string); field != "to_requirement_id" {
		t.Fatalf("self relation field = %q", field)
	}
	if _, err := env.svc.CreateRelation(requestContext(), env.versionID, versionapp.RelationInput{
		FromRequirementID: from.ID, ToRequirementID: to.ID, Kind: "blocks",
	}, 42); err == nil {
		t.Fatal("invalid relation kind accepted")
	}
	if _, err := env.svc.CreateRelation(requestContext(), env.versionID, versionapp.RelationInput{
		FromRequirementID: from.ID, ToRequirementID: 9999, Kind: "overlaps",
	}, 42); err == nil {
		t.Fatal("missing relation target accepted")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("missing target status = %d", apiError(t, err).Status)
	}
}

func TestRequirementListFiltersAndPaginates(t *testing.T) {
	env := newRequirementEnv(t)
	first := createRequirement(t, env, "First")
	second := createRequirement(t, env, "Second")
	third := createRequirement(t, env, "Third")
	if _, err := env.svc.Confirm(requestContext(), env.versionID, second.ID, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	all, hasMore, err := env.svc.List(requestContext(), env.versionID, "", 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if hasMore || len(all) != 3 {
		t.Fatalf("all = %#v, hasMore = %v", all, hasMore)
	}
	if all[0].ID != third.ID || all[1].ID != second.ID || all[2].ID != first.ID {
		t.Fatalf("order = %#v", all)
	}
	if all[1].ConfirmedAt == nil || all[0].ConfirmedAt != nil {
		t.Fatalf("confirmed_at = %#v/%#v", all[1].ConfirmedAt, all[0].ConfirmedAt)
	}
	if all[1].Revision != 1 || all[1].CurrentVersionID != second.CurrentVersionID || all[1].Title != "Second" {
		t.Fatalf("list item = %#v", all[1])
	}

	page, hasMore, err := env.svc.List(requestContext(), env.versionID, "", 2, nil)
	if err != nil {
		t.Fatalf("List page: %v", err)
	}
	if !hasMore || len(page) != 2 || page[0].ID != third.ID {
		t.Fatalf("page = %#v, hasMore = %v", page, hasMore)
	}
	next, hasMore, err := env.svc.List(requestContext(), env.versionID, "", 2, &page[1].ID)
	if err != nil {
		t.Fatalf("List next: %v", err)
	}
	if hasMore || len(next) != 1 || next[0].ID != first.ID {
		t.Fatalf("next = %#v, hasMore = %v", next, hasMore)
	}

	clarified, _, err := env.svc.List(requestContext(), env.versionID, "clarified", 10, nil)
	if err != nil || len(clarified) != 1 || clarified[0].ID != second.ID {
		t.Fatalf("clarified = %#v, %v", clarified, err)
	}
	if _, _, err := env.svc.List(requestContext(), env.versionID, "bogus", 10, nil); err == nil {
		t.Fatal("invalid status filter accepted")
	}
}

func TestRequirementGetDetailIncludesHistoryRelationsAndAttachments(t *testing.T) {
	env := newRequirementEnv(t)
	created := createRequirement(t, env, "First revision")
	if _, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	draft := validRequirementDraft(env)
	draft.Title = "Second revision"
	updated, err := env.svc.Update(requestContext(), env.versionID, created.ID, draft, "", 42)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	other := createRequirement(t, env, "Other")
	if _, err := env.svc.CreateRelation(requestContext(), env.versionID, versionapp.RelationInput{
		FromRequirementID: updated.ID, ToRequirementID: other.ID, Kind: "overlaps",
	}, 42); err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	requirementID := updated.ID
	attachmentID := int64(8001)
	env.repo.attachments = append(env.repo.attachments, version.Attachment{
		ID: attachmentID, VersionID: env.versionID, RequirementID: &requirementID,
		Name: "spec.md", MIME: "text/markdown", Size: 12, ExtractionStatus: "done", CreatedAt: testTime,
	})

	detail, err := env.svc.Get(requestContext(), env.versionID, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.Revision != 2 || detail.CurrentVersion.Title != "Second revision" {
		t.Fatalf("detail = %#v", detail)
	}
	if len(detail.History) != 1 || detail.History[0].Title != "First revision" {
		t.Fatalf("history = %#v", detail.History)
	}
	if len(detail.Confirmations) != 1 || len(detail.Relations) != 1 || len(detail.Attachments) != 1 {
		t.Fatalf("detail collections = %#v", detail)
	}
	if detail.Attachments[0].ID != attachmentID || detail.Attachments[0].Name != "spec.md" {
		t.Fatalf("attachments = %#v", detail.Attachments)
	}

	if _, err := env.svc.Get(requestContext(), env.versionID, 9999); err == nil {
		t.Fatal("missing requirement returned")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d", apiError(t, err).Status)
	}
}

// keep imports used while iterating
var _ = errors.Is
var _ = platform.Invalid
var _ = time.Time{}
