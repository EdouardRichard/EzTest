package versionapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// ---------- in-memory requirement repository ----------

type cleanupRepo struct {
	requirements  map[int64]version.Requirement
	versions      map[int64][]version.RequirementVersion // requirement ID -> revisions, newest first
	confirmations map[int64][]version.Confirmation
	relations     []version.Relation
}

func newCleanupRepo() *cleanupRepo {
	return &cleanupRepo{
		requirements:  map[int64]version.Requirement{},
		versions:      map[int64][]version.RequirementVersion{},
		confirmations: map[int64][]version.Confirmation{},
	}
}

func (r *cleanupRepo) seed(requirement version.Requirement, revision version.RequirementVersion) {
	r.requirements[requirement.ID] = requirement
	r.versions[requirement.ID] = []version.RequirementVersion{revision}
}

func (r *cleanupRepo) current(requirement version.Requirement) (version.RequirementVersion, bool) {
	if requirement.CurrentVersionID == nil {
		return version.RequirementVersion{}, false
	}
	for _, revision := range r.versions[requirement.ID] {
		if revision.ID == *requirement.CurrentVersionID {
			return revision, true
		}
	}
	return version.RequirementVersion{}, false
}

func (r *cleanupRepo) Create(_ context.Context, requirement version.Requirement, revision version.RequirementVersion) error {
	r.requirements[requirement.ID] = requirement
	r.versions[requirement.ID] = []version.RequirementVersion{revision}
	return nil
}

func (r *cleanupRepo) Get(_ context.Context, versionID, requirementID int64) (version.RequirementAggregate, error) {
	requirement, ok := r.requirements[requirementID]
	if !ok || requirement.VersionID != versionID {
		return version.RequirementAggregate{}, version.ErrRequirementNotFound
	}
	aggregate := version.RequirementAggregate{
		Requirement:   requirement,
		Versions:      append([]version.RequirementVersion{}, r.versions[requirementID]...),
		Confirmations: append([]version.Confirmation{}, r.confirmations[requirementID]...),
	}
	for _, relation := range r.relations {
		if relation.VersionID == versionID &&
			(relation.FromRequirementID == requirementID || relation.ToRequirementID == requirementID) {
			aggregate.Relations = append(aggregate.Relations, relation)
		}
	}
	return aggregate, nil
}

func (r *cleanupRepo) GetBySourceKey(ctx context.Context, versionID int64, sourceKey string) (version.RequirementAggregate, error) {
	for _, requirement := range r.requirements {
		if requirement.VersionID == versionID && requirement.SourceKey == sourceKey &&
			requirement.Status != version.RequirementObsolete {
			return r.Get(ctx, versionID, requirement.ID)
		}
	}
	return version.RequirementAggregate{}, version.ErrRequirementNotFound
}

func (r *cleanupRepo) GetMany(_ context.Context, versionID int64, ids []int64) ([]version.RequirementWithCurrent, error) {
	items := make([]version.RequirementWithCurrent, 0, len(ids))
	for _, id := range ids {
		requirement, ok := r.requirements[id]
		if !ok || requirement.VersionID != versionID {
			return nil, version.ErrRequirementNotFound
		}
		current, ok := r.current(requirement)
		if !ok {
			return nil, version.ErrRequirementNotFound
		}
		items = append(items, version.RequirementWithCurrent{Requirement: requirement, Current: current})
	}
	return items, nil
}

func (r *cleanupRepo) List(_ context.Context, versionID int64, status version.RequirementStatus, limit int, beforeID *int64) ([]version.RequirementListItem, error) {
	items := make([]version.RequirementListItem, 0)
	for _, requirement := range r.requirements {
		if requirement.VersionID != versionID {
			continue
		}
		if status != "" && requirement.Status != status {
			continue
		}
		if beforeID != nil && requirement.ID >= *beforeID {
			continue
		}
		current, ok := r.current(requirement)
		if !ok {
			continue
		}
		items = append(items, version.RequirementListItem{Requirement: requirement, Current: current})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Requirement.ID > items[j].Requirement.ID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (r *cleanupRepo) AddRevision(_ context.Context, requirement version.Requirement, revision version.RequirementVersion) error {
	stored, ok := r.requirements[requirement.ID]
	if !ok || stored.VersionID != requirement.VersionID {
		return version.ErrRequirementNotFound
	}
	r.requirements[requirement.ID] = requirement
	r.versions[requirement.ID] = append([]version.RequirementVersion{revision}, r.versions[requirement.ID]...)
	return nil
}

func (r *cleanupRepo) Confirm(_ context.Context, requirement version.Requirement, confirmation version.Confirmation) error {
	if _, ok := r.requirements[requirement.ID]; !ok {
		return version.ErrRequirementNotFound
	}
	r.requirements[requirement.ID] = requirement
	r.confirmations[requirement.ID] = append(r.confirmations[requirement.ID], confirmation)
	return nil
}

func (r *cleanupRepo) BatchConfirm(_ context.Context, requirements []version.Requirement, confirmations []version.Confirmation) error {
	for _, requirement := range requirements {
		if _, ok := r.requirements[requirement.ID]; !ok {
			return version.ErrRequirementNotFound
		}
	}
	for _, requirement := range requirements {
		r.requirements[requirement.ID] = requirement
	}
	return nil
}

func (r *cleanupRepo) CreateRelation(_ context.Context, relation version.Relation) error {
	for _, existing := range r.relations {
		if existing.VersionID == relation.VersionID &&
			existing.FromRequirementID == relation.FromRequirementID &&
			existing.ToRequirementID == relation.ToRequirementID &&
			existing.Kind == relation.Kind {
			return version.ErrRelationDuplicate
		}
	}
	r.relations = append(r.relations, relation)
	return nil
}

func (r *cleanupRepo) ListRelations(_ context.Context, versionID, requirementID int64) ([]version.Relation, error) {
	relations := make([]version.Relation, 0)
	for _, relation := range r.relations {
		if relation.VersionID == versionID &&
			(relation.FromRequirementID == requirementID || relation.ToRequirementID == requirementID) {
			relations = append(relations, relation)
		}
	}
	return relations, nil
}

func (r *cleanupRepo) SetStatus(_ context.Context, versionID, requirementID int64, status version.RequirementStatus, updatedAt time.Time) error {
	requirement, ok := r.requirements[requirementID]
	if !ok || requirement.VersionID != versionID {
		return version.ErrRequirementNotFound
	}
	requirement.Status = status
	requirement.UpdatedAt = updatedAt
	r.requirements[requirementID] = requirement
	return nil
}

// ---------- version reader / clock / ids / audit / events ----------

type cleanupVersionReader struct{ id int64 }

func (v cleanupVersionReader) Get(_ context.Context, id int64) (versionapp.VersionView, error) {
	if id != v.id {
		return versionapp.VersionView{}, platform.NotFound("version")
	}
	return versionapp.VersionView{ID: id, Status: "clarifying"}, nil
}

func (cleanupVersionReader) MarkRunning(context.Context, int64) error { return nil }

type cleanupClock struct{ at time.Time }

func (c cleanupClock) Now() time.Time { return c.at }

type cleanupIDs struct{ next int64 }

func (g *cleanupIDs) Next() int64 {
	g.next++
	return g.next
}

type cleanupAudit struct{ events []auditapp.Event }

func (a *cleanupAudit) Record(_ context.Context, event auditapp.Event) error {
	a.events = append(a.events, event)
	return nil
}

func (a *cleanupAudit) actions() []string {
	actions := make([]string, 0, len(a.events))
	for _, event := range a.events {
		actions = append(actions, event.Action)
	}
	return actions
}

func (a *cleanupAudit) find(action string) (auditapp.Event, bool) {
	for index := len(a.events) - 1; index >= 0; index-- {
		if a.events[index].Action == action {
			return a.events[index], true
		}
	}
	return auditapp.Event{}, false
}

func (a *cleanupAudit) count(action string) int {
	count := 0
	for _, event := range a.events {
		if event.Action == action {
			count++
		}
	}
	return count
}

type cleanupEvents struct{ events []eventingapp.Event }

func (p *cleanupEvents) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return nil
}

func (p *cleanupEvents) types() []string {
	types := make([]string, 0, len(p.events))
	for _, event := range p.events {
		types = append(types, event.Type)
	}
	return types
}

func (p *cleanupEvents) count(eventType string) int {
	count := 0
	for _, event := range p.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// ---------- HTTP fixture ----------

const cleanupVersionID int64 = 101

type cleanupRoutes struct {
	mux    http.Handler
	repo   *cleanupRepo
	audit  *cleanupAudit
	events *cleanupEvents
}

func newCleanupRoutes(t *testing.T) *cleanupRoutes {
	t.Helper()
	repo := newCleanupRepo()
	audit := &cleanupAudit{}
	events := &cleanupEvents{}
	service := versionapp.NewRequirementService(
		repo,
		cleanupVersionReader{id: cleanupVersionID},
		nil,
		nil,
		audit,
		events,
		&cleanupIDs{next: 1000},
		cleanupClock{at: testTime},
	)
	handler := versionapi.NewRequirementHandler(service)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/versions/{id}/requirements/dedupe", handler.Dedupe)
	mux.HandleFunc("DELETE /api/versions/{id}/requirements/{rid}", handler.Delete)
	mux.HandleFunc("POST /api/versions/{id}/requirements/merge", handler.Merge)
	return &cleanupRoutes{mux: mux, repo: repo, audit: audit, events: events}
}

// seed inserts one requirement with a current revision and distinct revision ID.
func (f *cleanupRoutes) seed(id int64, status version.RequirementStatus, title, description, acceptance string) version.Requirement {
	return f.seedInVersion(cleanupVersionID, id, status, title, description, acceptance)
}

// seedInVersion inserts a requirement belonging to another version, so
// cross-version (foreign) access can be asserted through the real handlers.
func (f *cleanupRoutes) seedInVersion(versionID, id int64, status version.RequirementStatus, title, description, acceptance string) version.Requirement {
	revisionID := id + 100
	revision := version.RequirementVersion{
		ID: revisionID, RequirementID: id, Revision: 1,
		Title: title, Description: description, AcceptanceCriteria: acceptance,
		Priority: "P1", Constraints: json.RawMessage(`{}`),
		TargetVersionSUTs: []string{}, TargetPages: []string{}, TargetAPIs: []string{},
		ContentHash: strings.Repeat("a", 64), CreatedAt: testTime,
	}
	requirement := version.Requirement{
		ID: id, VersionID: versionID, CurrentVersionID: &revisionID, Status: status,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	f.repo.seed(requirement, revision)
	return requirement
}

func statusOf(t *testing.T, repo *cleanupRepo, id int64) version.RequirementStatus {
	t.Helper()
	requirement, ok := repo.requirements[id]
	if !ok {
		t.Fatalf("requirement %d not seeded", id)
	}
	return requirement.Status
}

func stringList(t *testing.T, raw any) []string {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("value is not a list: %#v", raw)
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			t.Fatalf("item is not a string: %#v", item)
		}
		values = append(values, value)
	}
	return values
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, status, recorder.Body.String())
	}
	body := errorBody(t, recorder)
	if body["code"] != code {
		t.Fatalf("error code = %#v, want %q (body = %s)", body["code"], code, recorder.Body.String())
	}
	return body
}

// TestRequirementCleanupRoutes pins Task 83 end to end through the HTTP
// handlers and the real application service: duplicate draft cleanup is user
// reachable, only drafts may be deleted/merged/deduped, writes are audited and
// published as events, and every rejection is a readable typed error.
func TestRequirementCleanupRoutes(t *testing.T) {
	t.Run("draft delete is reachable, audited and idempotent", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9001, version.RequirementDraft, "Coupon checkout", "desc", "acceptance")

		recorder := call(t, routes.mux, http.MethodDelete,
			"/api/versions/101/requirements/9001", `{"reason":"duplicate draft"}`, actor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		data := dataBody(t, recorder)
		if data["id"] != "9001" || data["status"] != "obsolete" {
			t.Fatalf("deleted requirement = %#v", data)
		}
		if stored := statusOf(t, routes.repo, 9001); stored != version.RequirementObsolete {
			t.Fatalf("stored status = %q, want obsolete", stored)
		}
		deleted, ok := routes.audit.find("requirement.deleted")
		if !ok {
			t.Fatalf("audit actions = %v, want requirement.deleted", routes.audit.actions())
		}
		if deleted.ActorID == nil || *deleted.ActorID != 42 {
			t.Fatalf("delete audit actor = %#v", deleted.ActorID)
		}
		if deleted.TargetID != "9001" || deleted.Metadata["reason"] != "duplicate draft" {
			t.Fatalf("delete audit = %#v", deleted)
		}
		if types := routes.events.types(); !hasString(types, "requirement.obsoleted") {
			t.Fatalf("events = %v, want requirement.obsoleted", types)
		}
		// One delete writes exactly one deleted audit + one obsoleted audit and
		// one requirement.obsoleted event. The obsolete projection must not add
		// a second set of rows of its own.
		if got := routes.audit.count("requirement.deleted"); got != 1 {
			t.Fatalf("requirement.deleted audits = %d, want 1 (%v)", got, routes.audit.actions())
		}
		if got := routes.audit.count("requirement.obsoleted"); got != 1 {
			t.Fatalf("requirement.obsoleted audits = %d, want 1 (%v)", got, routes.audit.actions())
		}
		if got := routes.events.count("requirement.obsoleted"); got != 1 {
			t.Fatalf("requirement.obsoleted events = %d, want 1 (%v)", got, routes.events.types())
		}
		auditTotal := len(routes.audit.events)
		eventTotal := len(routes.events.events)

		// A repeated delete of the already-obsolete draft is idempotent and
		// publishes/audits nothing new: the replay must not duplicate
		// requirement.deleted or requirement.obsoleted.
		again := call(t, routes.mux, http.MethodDelete,
			"/api/versions/101/requirements/9001", "", actor())
		if again.Code != http.StatusOK || dataBody(t, again)["status"] != "obsolete" {
			t.Fatalf("repeat delete = %d %s", again.Code, again.Body.String())
		}
		if len(routes.audit.events) != auditTotal || len(routes.events.events) != eventTotal {
			t.Fatalf("replay wrote audit/events: audits=%v events=%v",
				routes.audit.actions(), routes.events.types())
		}
		if got := routes.audit.count("requirement.deleted"); got != 1 {
			t.Fatalf("replay requirement.deleted audits = %d, want 1", got)
		}
		if got := routes.audit.count("requirement.obsoleted"); got != 1 {
			t.Fatalf("replay requirement.obsoleted audits = %d, want 1", got)
		}
		if got := routes.events.count("requirement.obsoleted"); got != 1 {
			t.Fatalf("replay requirement.obsoleted events = %d, want 1", got)
		}
	})

	t.Run("confirmed delete is rejected with a typed 409", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9002, version.RequirementClarified, "Confirmed requirement", "desc", "acceptance")

		recorder := call(t, routes.mux, http.MethodDelete,
			"/api/versions/101/requirements/9002", "", actor())
		requireError(t, recorder, http.StatusConflict, "state_conflict")
		if stored := statusOf(t, routes.repo, 9002); stored != version.RequirementClarified {
			t.Fatalf("confirmed requirement changed to %q", stored)
		}
		if _, ok := routes.audit.find("requirement.deleted"); ok {
			t.Fatal("rejected delete must not be audited as deleted")
		}
	})

	t.Run("delete validates scope, id and actor", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9003, version.RequirementDraft, "Draft", "desc", "acceptance")

		missing := call(t, routes.mux, http.MethodDelete,
			"/api/versions/101/requirements/9999", "", actor())
		requireError(t, missing, http.StatusNotFound, "not_found")

		foreign := call(t, routes.mux, http.MethodDelete,
			"/api/versions/102/requirements/9003", "", actor())
		requireError(t, foreign, http.StatusNotFound, "not_found")

		badID := call(t, routes.mux, http.MethodDelete,
			"/api/versions/101/requirements/nope", "", actor())
		requireError(t, badID, http.StatusBadRequest, "invalid_argument")

		unauthenticated := call(t, routes.mux, http.MethodDelete,
			"/api/versions/101/requirements/9003", "", nil)
		requireError(t, unauthenticated, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("dedupe collapses duplicate drafts and audits the survivors", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9101, version.RequirementDraft, "Coupon checkout", "first", "first acceptance")
		routes.seed(9102, version.RequirementDraft, "  coupon   CHECKOUT ", "refined", "refined acceptance")
		routes.seed(9103, version.RequirementDraft, "Refund timing", "refund", "refund acceptance")
		// A confirmed requirement with the same title must never be retired.
		routes.seed(9104, version.RequirementClarified, "Coupon checkout", "confirmed", "confirmed acceptance")

		recorder := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/dedupe", "", actor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("dedupe status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		data := dataBody(t, recorder)
		kept := stringList(t, data["kept_ids"])
		obsolete := stringList(t, data["obsolete_ids"])
		if !hasString(obsolete, "9102") || hasString(obsolete, "9101") || hasString(obsolete, "9103") {
			t.Fatalf("obsolete ids = %v", obsolete)
		}
		if !hasString(kept, "9101") || !hasString(kept, "9103") {
			t.Fatalf("kept ids = %v", kept)
		}
		if stored := statusOf(t, routes.repo, 9102); stored != version.RequirementObsolete {
			t.Fatalf("duplicate status = %q, want obsolete", stored)
		}
		if stored := statusOf(t, routes.repo, 9104); stored != version.RequirementClarified {
			t.Fatalf("confirmed status = %q, want clarified", stored)
		}
		if _, ok := routes.audit.find("requirement.deduped"); !ok {
			t.Fatalf("audit actions = %v, want requirement.deduped", routes.audit.actions())
		}
		if types := routes.events.types(); !hasString(types, "requirement.deduped") {
			t.Fatalf("events = %v, want requirement.deduped", types)
		}
		if got := routes.audit.count("requirement.deduped"); got != 1 {
			t.Fatalf("requirement.deduped audits = %d, want 1 (%v)", got, routes.audit.actions())
		}
		if got := routes.events.count("requirement.deduped"); got != 1 {
			t.Fatalf("requirement.deduped events = %d, want 1 (%v)", got, routes.events.types())
		}
		auditTotal := len(routes.audit.events)
		eventTotal := len(routes.events.events)

		// Repeating dedupe is idempotent: no duplicate drafts remain. The no-op
		// must not emit a second requirement.deduped audit/event.
		again := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/dedupe", "", actor())
		if again.Code != http.StatusOK {
			t.Fatalf("repeat dedupe status = %d", again.Code)
		}
		if obsoleteAgain := stringList(t, dataBody(t, again)["obsolete_ids"]); len(obsoleteAgain) != 0 {
			t.Fatalf("repeat dedupe obsolete ids = %v, want empty", obsoleteAgain)
		}
		if len(routes.audit.events) != auditTotal || len(routes.events.events) != eventTotal {
			t.Fatalf("no-op dedupe wrote audit/events: audits=%v events=%v",
				routes.audit.actions(), routes.events.types())
		}
		if got := routes.audit.count("requirement.deduped"); got != 1 {
			t.Fatalf("no-op dedupe requirement.deduped audits = %d, want 1", got)
		}
		if got := routes.events.count("requirement.deduped"); got != 1 {
			t.Fatalf("no-op dedupe requirement.deduped events = %d, want 1", got)
		}

		missingVersion := call(t, routes.mux, http.MethodPost,
			"/api/versions/999/requirements/dedupe", "", actor())
		requireError(t, missingVersion, http.StatusNotFound, "not_found")

		unauthenticated := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/dedupe", "", nil)
		requireError(t, unauthenticated, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("dedupe with nothing to collapse is silent", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9151, version.RequirementDraft, "Coupon checkout", "coupon desc", "coupon acceptance")
		routes.seed(9152, version.RequirementDraft, "Refund timing", "refund desc", "refund acceptance")

		recorder := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/dedupe", "", actor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("dedupe status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		data := dataBody(t, recorder)
		if len(stringList(t, data["obsolete_ids"])) != 0 {
			t.Fatalf("obsolete ids = %v, want empty", data["obsolete_ids"])
		}
		if len(stringList(t, data["kept_ids"])) != 2 {
			t.Fatalf("kept ids = %v, want both unique drafts", data["kept_ids"])
		}
		if got := routes.audit.count("requirement.deduped"); got != 0 {
			t.Fatalf("no-op dedupe audits = %v, want none", routes.audit.actions())
		}
		if got := routes.events.count("requirement.deduped"); got != 0 {
			t.Fatalf("no-op dedupe events = %v, want none", routes.events.types())
		}
	})

	t.Run("merge folds draft sources into the target and records the relation", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9201, version.RequirementDraft,
			"Author can publish", "Author publishes an article.", "The article is publicly visible.")
		routes.seed(9202, version.RequirementDraft,
			"Author can publish articles", "Author publishes an article.", "The public list shows it.")

		recorder := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/merge",
			`{"target_requirement_id":"9201","source_requirement_ids":["9202"]}`, actor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("merge status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		data := dataBody(t, recorder)
		mergedIDs := stringList(t, data["merged_ids"])
		if len(mergedIDs) != 1 || mergedIDs[0] != "9202" {
			t.Fatalf("merged ids = %v", mergedIDs)
		}
		requirement, ok := data["requirement"].(map[string]any)
		if !ok || requirement["id"] != "9201" || requirement["revision"] != float64(2) {
			t.Fatalf("merged requirement = %#v", data["requirement"])
		}
		current := requirement["current_version"].(map[string]any)
		acceptance, _ := current["acceptance_criteria"].(string)
		if !strings.Contains(acceptance, "publicly visible") || !strings.Contains(acceptance, "public list shows it") {
			t.Fatalf("merged acceptance = %q", acceptance)
		}
		if stored := statusOf(t, routes.repo, 9202); stored != version.RequirementObsolete {
			t.Fatalf("merge source status = %q, want obsolete", stored)
		}
		if len(routes.repo.relations) != 1 ||
			routes.repo.relations[0].FromRequirementID != 9201 ||
			routes.repo.relations[0].Kind != version.RelationOverlaps {
			t.Fatalf("merge relations = %#v", routes.repo.relations)
		}
		if _, ok := routes.audit.find("requirement.merged"); !ok {
			t.Fatalf("audit actions = %v, want requirement.merged", routes.audit.actions())
		}
		types := routes.events.types()
		if !hasString(types, "requirement.merged") || !hasString(types, "requirement.obsoleted") {
			t.Fatalf("events = %v, want merged + obsoleted", types)
		}
	})

	t.Run("confirmed requirements cannot be merge targets or sources", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9301, version.RequirementClarified, "Confirmed target", "desc", "acceptance")
		routes.seed(9302, version.RequirementDraft, "Draft source", "desc", "acceptance")
		routes.seed(9303, version.RequirementDraft, "Draft target", "desc", "acceptance")
		routes.seed(9304, version.RequirementClarified, "Confirmed source", "desc", "acceptance")

		confirmedTarget := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/merge",
			`{"target_requirement_id":"9301","source_requirement_ids":["9302"]}`, actor())
		requireError(t, confirmedTarget, http.StatusConflict, "state_conflict")
		if stored := statusOf(t, routes.repo, 9301); stored != version.RequirementClarified {
			t.Fatalf("confirmed target changed to %q", stored)
		}
		if stored := statusOf(t, routes.repo, 9302); stored != version.RequirementDraft {
			t.Fatalf("draft source changed to %q", stored)
		}
		if len(routes.repo.relations) != 0 {
			t.Fatalf("rejected merge wrote relations: %#v", routes.repo.relations)
		}

		confirmedSource := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/merge",
			`{"target_requirement_id":"9303","source_requirement_ids":["9304"]}`, actor())
		requireError(t, confirmedSource, http.StatusConflict, "state_conflict")
		if stored := statusOf(t, routes.repo, 9304); stored != version.RequirementClarified {
			t.Fatalf("confirmed source changed to %q", stored)
		}
		if _, ok := routes.audit.find("requirement.merged"); ok {
			t.Fatal("rejected merge must not be audited as merged")
		}
	})

	t.Run("closed requirements are rejected with a typed 409", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9351, version.RequirementClosed, "Closed target", "desc", "acceptance")
		routes.seed(9352, version.RequirementDraft, "Draft source", "desc", "acceptance")
		routes.seed(9353, version.RequirementDraft, "Draft target", "desc", "acceptance")
		routes.seed(9354, version.RequirementClosed, "Closed source", "desc", "acceptance")

		deleted := call(t, routes.mux, http.MethodDelete,
			"/api/versions/101/requirements/9351", "", actor())
		requireError(t, deleted, http.StatusConflict, "state_conflict")
		if stored := statusOf(t, routes.repo, 9351); stored != version.RequirementClosed {
			t.Fatalf("closed requirement changed to %q", stored)
		}
		if _, ok := routes.audit.find("requirement.deleted"); ok {
			t.Fatal("rejected closed delete must not be audited as deleted")
		}

		closedTarget := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/merge",
			`{"target_requirement_id":"9351","source_requirement_ids":["9352"]}`, actor())
		requireError(t, closedTarget, http.StatusConflict, "state_conflict")
		if stored := statusOf(t, routes.repo, 9351); stored != version.RequirementClosed {
			t.Fatalf("closed merge target changed to %q", stored)
		}
		if stored := statusOf(t, routes.repo, 9352); stored != version.RequirementDraft {
			t.Fatalf("draft source changed after rejected closed-target merge: %q", stored)
		}

		closedSource := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/merge",
			`{"target_requirement_id":"9353","source_requirement_ids":["9354"]}`, actor())
		requireError(t, closedSource, http.StatusConflict, "state_conflict")
		if stored := statusOf(t, routes.repo, 9354); stored != version.RequirementClosed {
			t.Fatalf("closed merge source changed to %q", stored)
		}
		if len(routes.repo.relations) != 0 {
			t.Fatalf("rejected closed merges wrote relations: %#v", routes.repo.relations)
		}
		if got := routes.audit.count("requirement.merged"); got != 0 {
			t.Fatalf("rejected closed merges audited as merged: %v", routes.audit.actions())
		}
		if got := routes.events.count("requirement.merged"); got != 0 {
			t.Fatalf("rejected closed merges published merged: %v", routes.events.types())
		}
	})

	t.Run("merge resolves target and sources inside the version", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9601, version.RequirementDraft, "Target", "target desc", "target acceptance")
		routes.seed(9602, version.RequirementDraft, "Source", "source desc", "source acceptance")
		// Same IDs exist, but under a foreign version: the version-scoped reads
		// must report 404 instead of reaching them.
		routes.seedInVersion(102, 9603, version.RequirementDraft, "Foreign target", "foreign desc", "foreign acceptance")
		routes.seedInVersion(102, 9604, version.RequirementDraft, "Foreign source", "foreign desc", "foreign acceptance")

		cases := []struct {
			name string
			body string
		}{
			{"missing target", `{"target_requirement_id":"9999","source_requirement_ids":["9602"]}`},
			{"foreign target", `{"target_requirement_id":"9603","source_requirement_ids":["9602"]}`},
			{"missing source", `{"target_requirement_id":"9601","source_requirement_ids":["9999"]}`},
			{"foreign source", `{"target_requirement_id":"9601","source_requirement_ids":["9604"]}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				recorder := call(t, routes.mux, http.MethodPost,
					"/api/versions/101/requirements/merge", tc.body, actor())
				requireError(t, recorder, http.StatusNotFound, "not_found")
			})
		}
		if stored := statusOf(t, routes.repo, 9601); stored != version.RequirementDraft {
			t.Fatalf("target status changed to %q", stored)
		}
		if stored := statusOf(t, routes.repo, 9602); stored != version.RequirementDraft {
			t.Fatalf("source status changed to %q", stored)
		}
		if stored := statusOf(t, routes.repo, 9603); stored != version.RequirementDraft {
			t.Fatalf("foreign target status changed to %q", stored)
		}
		if stored := statusOf(t, routes.repo, 9604); stored != version.RequirementDraft {
			t.Fatalf("foreign source status changed to %q", stored)
		}
		if len(routes.repo.relations) != 0 {
			t.Fatalf("rejected cross-version merges wrote relations: %#v", routes.repo.relations)
		}
		if got := routes.audit.count("requirement.merged"); got != 0 {
			t.Fatalf("rejected merges audited as merged: %v", routes.audit.actions())
		}
		if got := routes.events.count("requirement.merged"); got != 0 {
			t.Fatalf("rejected merges published merged: %v", routes.events.types())
		}
	})

	t.Run("merge validates the request with typed errors", func(t *testing.T) {
		routes := newCleanupRoutes(t)
		routes.seed(9401, version.RequirementDraft, "Target", "desc", "acceptance")
		routes.seed(9402, version.RequirementDraft, "Source", "desc", "acceptance")

		cases := []struct {
			name  string
			body  string
			field string
		}{
			{"missing target", `{"source_requirement_ids":["9402"]}`, "target_requirement_id"},
			{"bad target id", `{"target_requirement_id":"nope","source_requirement_ids":["9402"]}`, "target_requirement_id"},
			{"empty sources", `{"target_requirement_id":"9401","source_requirement_ids":[]}`, "source_requirement_ids"},
			{"self merge", `{"target_requirement_id":"9401","source_requirement_ids":["9401"]}`, "source_requirement_ids"},
			{"bad source id", `{"target_requirement_id":"9401","source_requirement_ids":["nope"]}`, "source_requirement_ids[0]"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				recorder := call(t, routes.mux, http.MethodPost,
					"/api/versions/101/requirements/merge", tc.body, actor())
				body := requireError(t, recorder, http.StatusBadRequest, "invalid_argument")
				details, _ := body["details"].(map[string]any)
				if details["field"] != tc.field {
					t.Fatalf("error field = %#v, want %q (body = %s)", details["field"], tc.field, recorder.Body.String())
				}
			})
		}

		unauthenticated := call(t, routes.mux, http.MethodPost,
			"/api/versions/101/requirements/merge",
			`{"target_requirement_id":"9401","source_requirement_ids":["9402"]}`, nil)
		requireError(t, unauthenticated, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("cleanup rejects malformed version and requirement ids", func(t *testing.T) {
		routes := newCleanupRoutes(t)

		requireField := func(t *testing.T, method, path, body, field string) {
			t.Helper()
			recorder := call(t, routes.mux, method, path, body, actor())
			requireError(t, recorder, http.StatusBadRequest, "invalid_argument")
			if details, _ := errorBody(t, recorder)["details"].(map[string]any); details["field"] != field {
				t.Fatalf("error details = %#v, want field %q (body = %s)", details, field, recorder.Body.String())
			}
		}

		requireField(t, http.MethodDelete, "/api/versions/nope/requirements/1", "", "id")
		requireField(t, http.MethodDelete, "/api/versions/101/requirements/nope", "", "rid")
		requireField(t, http.MethodPost, "/api/versions/nope/requirements/merge",
			`{"target_requirement_id":"1","source_requirement_ids":["2"]}`, "id")
		requireField(t, http.MethodPost, "/api/versions/nope/requirements/dedupe", "", "id")
	})
}
