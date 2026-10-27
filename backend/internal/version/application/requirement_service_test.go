package application_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeRequirementRepo is an in-memory domain.RequirementRepository with the
// semantics the services rely on: revision CAS, status CAS, status-guarded
// updates/deletes and cascaded deletes. It deliberately does NOT enforce relation
// uniqueness: the service pre-check must catch normalized duplicates (the SQL
// unique indexes are the race backstop, tested in the postgres package).
type fakeRequirementRepo struct {
	requirements  map[int64]domain.Requirement
	relations     map[int64]domain.Relation
	attachments   map[int64]domain.Attachment
	openQuestions map[int64]int

	createErr error
	writes    int
}

// newFakeRequirementRepo returns an empty repository.
func newFakeRequirementRepo() *fakeRequirementRepo {
	return &fakeRequirementRepo{
		requirements:  map[int64]domain.Requirement{},
		relations:     map[int64]domain.Relation{},
		attachments:   map[int64]domain.Attachment{},
		openQuestions: map[int64]int{},
	}
}

// stamp returns a deterministic timestamp for one row version.
func (r *fakeRequirementRepo) stamp(id int64, revision int) time.Time {
	return testNow.Add(time.Duration(id)*time.Millisecond + time.Duration(revision)*time.Second)
}

// Create stores requirement and writes server timestamps back to it.
func (r *fakeRequirementRepo) Create(_ context.Context, requirement *domain.Requirement) error {
	if r.createErr != nil {
		return r.createErr
	}
	if _, exists := r.requirements[requirement.ID]; exists {
		return domain.NewConflict("duplicate_key", "requirement already exists")
	}
	requirement.CreatedAt = r.stamp(requirement.ID, 0)
	requirement.UpdatedAt = requirement.CreatedAt
	r.requirements[requirement.ID] = *requirement
	r.writes++
	return nil
}

// Get returns one stored requirement.
func (r *fakeRequirementRepo) Get(_ context.Context, id int64) (*domain.Requirement, error) {
	stored, ok := r.requirements[id]
	if !ok {
		return nil, domain.NewNotFound("requirement", id)
	}
	return &stored, nil
}

// List filters by version and status and applies the requested limit in
// created_at, id ascending order.
func (r *fakeRequirementRepo) List(_ context.Context, versionID int64, status string, p domain.Page) ([]domain.Requirement, bool, error) {
	matches := make([]domain.Requirement, 0, len(r.requirements))
	for _, stored := range r.requirements {
		if stored.VersionID != versionID {
			continue
		}
		if status != "" && stored.Status != status {
			continue
		}
		matches = append(matches, stored)
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
			return matches[i].CreatedAt.Before(matches[j].CreatedAt)
		}
		return matches[i].ID < matches[j].ID
	})
	if p.Cursor != "" {
		for len(matches) > 0 && fmt.Sprintf("%d", matches[0].ID) <= p.Cursor {
			matches = matches[1:]
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

// Update applies the revision- and status-guarded update.
func (r *fakeRequirementRepo) Update(_ context.Context, requirement *domain.Requirement, expected int) error {
	stored, ok := r.requirements[requirement.ID]
	if !ok {
		return domain.NewStateConflict("revision_conflict", "requirement revision does not match")
	}
	if stored.Status != domain.RequirementDraft {
		return domain.NewStateConflict("requirement_state_conflict", "requirement is not draft")
	}
	if stored.Revision != expected {
		return domain.NewStateConflict("revision_conflict", "requirement revision does not match")
	}
	stored.Title = requirement.Title
	stored.Description = requirement.Description
	stored.AcceptanceCriteria = requirement.AcceptanceCriteria
	stored.Priority = requirement.Priority
	stored.LastUpdateSource = requirement.LastUpdateSource
	stored.Revision++
	stored.UpdatedBy = requirement.UpdatedBy
	stored.UpdatedAt = r.stamp(stored.ID, stored.Revision)
	r.requirements[stored.ID] = stored
	*requirement = stored
	r.writes++
	return nil
}

// Delete removes one draft requirement and cascades its children.
func (r *fakeRequirementRepo) Delete(_ context.Context, id int64) error {
	stored, ok := r.requirements[id]
	if !ok {
		return domain.NewNotFound("requirement", id)
	}
	if stored.Status != domain.RequirementDraft {
		return domain.NewStateConflict("requirement_state_conflict", "requirement is not draft")
	}
	delete(r.requirements, id)
	for relationID, relation := range r.relations {
		if relation.FromRequirementID == id || relation.ToRequirementID == id {
			delete(r.relations, relationID)
		}
	}
	for attachmentID, attachment := range r.attachments {
		if attachment.RequirementID == id {
			delete(r.attachments, attachmentID)
		}
	}
	delete(r.openQuestions, id)
	r.writes++
	return nil
}

// UpdateStatus applies a status compare-and-swap and maintains confirmed_at/by.
func (r *fakeRequirementRepo) UpdateStatus(_ context.Context, id int64, from, to string, actor int64) (*domain.Requirement, error) {
	stored, ok := r.requirements[id]
	if !ok || stored.Status != from {
		return nil, domain.NewStateConflict("requirement_state_conflict", "requirement status does not allow this action")
	}
	if to == domain.RequirementClarified && r.openQuestions[id] > 0 {
		return nil, domain.NewStateConflict("open_questions_remaining", "requirement still has open questions")
	}
	stored.Status = to
	if to == domain.RequirementClarified {
		confirmedAt := r.stamp(id, stored.Revision+1)
		confirmedBy := actor
		stored.ConfirmedAt = &confirmedAt
		stored.ConfirmedBy = &confirmedBy
	} else {
		stored.ConfirmedAt = nil
		stored.ConfirmedBy = nil
	}
	stored.LastUpdateSource = domain.SourceUser
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.requirements[id] = stored
	r.writes++
	return &stored, nil
}

// CountOpenQuestions returns the requirement's open question count.
func (r *fakeRequirementRepo) CountOpenQuestions(_ context.Context, requirementID int64) (int, error) {
	return r.openQuestions[requirementID], nil
}

// ListRelations returns the version's relations ordered by id.
func (r *fakeRequirementRepo) ListRelations(_ context.Context, versionID int64) ([]domain.Relation, error) {
	relations := make([]domain.Relation, 0, len(r.relations))
	for _, relation := range r.relations {
		if relation.VersionID == versionID {
			relations = append(relations, relation)
		}
	}
	sort.Slice(relations, func(i, j int) bool { return relations[i].ID < relations[j].ID })
	return relations, nil
}

// CreateRelation stores relation and assigns timestamps.
func (r *fakeRequirementRepo) CreateRelation(_ context.Context, relation *domain.Relation) error {
	relation.CreatedAt = r.stamp(relation.ID, 0)
	relation.UpdatedAt = relation.CreatedAt
	r.relations[relation.ID] = *relation
	r.writes++
	return nil
}

// UpdateRelation rewrites relation_type/description.
func (r *fakeRequirementRepo) UpdateRelation(_ context.Context, id int64, relType, desc string) (*domain.Relation, error) {
	stored, ok := r.relations[id]
	if !ok {
		return nil, domain.NewNotFound("relation", id)
	}
	stored.RelationType = relType
	stored.Description = desc
	stored.UpdatedAt = r.stamp(id, 1)
	r.relations[id] = stored
	r.writes++
	return &stored, nil
}

// DeleteRelation removes one relation.
func (r *fakeRequirementRepo) DeleteRelation(_ context.Context, id int64) error {
	if _, ok := r.relations[id]; !ok {
		return domain.NewNotFound("relation", id)
	}
	delete(r.relations, id)
	r.writes++
	return nil
}

// CreateAttachment stores attachment metadata.
func (r *fakeRequirementRepo) CreateAttachment(_ context.Context, attachment *domain.Attachment) error {
	attachment.CreatedAt = r.stamp(attachment.ID, 0)
	r.attachments[attachment.ID] = *attachment
	r.writes++
	return nil
}

// ListAttachments returns the requirement's attachments ordered by id.
func (r *fakeRequirementRepo) ListAttachments(_ context.Context, requirementID int64) ([]domain.Attachment, error) {
	attachments := make([]domain.Attachment, 0, len(r.attachments))
	for _, attachment := range r.attachments {
		if attachment.RequirementID == requirementID {
			attachments = append(attachments, attachment)
		}
	}
	sort.Slice(attachments, func(i, j int) bool { return attachments[i].ID < attachments[j].ID })
	return attachments, nil
}

// GetAttachment returns one stored attachment.
func (r *fakeRequirementRepo) GetAttachment(_ context.Context, id int64) (*domain.Attachment, error) {
	stored, ok := r.attachments[id]
	if !ok {
		return nil, domain.NewNotFound("attachment", id)
	}
	return &stored, nil
}

// DeleteAttachment removes one attachment.
func (r *fakeRequirementRepo) DeleteAttachment(_ context.Context, id int64) error {
	if _, ok := r.attachments[id]; !ok {
		return domain.NewNotFound("attachment", id)
	}
	delete(r.attachments, id)
	r.writes++
	return nil
}

// CountAttachments returns the requirement and version attachment counts.
func (r *fakeRequirementRepo) CountAttachments(_ context.Context, requirementID, versionID int64) (int, int, error) {
	requirementCount, versionCount := 0, 0
	for _, attachment := range r.attachments {
		if attachment.RequirementID == requirementID {
			requirementCount++
		}
		if attachment.VersionID == versionID {
			versionCount++
		}
	}
	return requirementCount, versionCount, nil
}

// requirementFixture wires the real requirement/relation services to in-memory
// fakes.
type requirementFixture struct {
	service   *application.RequirementService
	relations *application.RelationService
	repo      *fakeRequirementRepo
	versions  *fakeVersionRepo
	events    *fakeEvents
	ids       *fixedIDs
}

// newRequirementFixture returns a fixture whose first generated id is 5000.
func newRequirementFixture() *requirementFixture {
	f := &requirementFixture{
		repo:     newFakeRequirementRepo(),
		versions: newFakeVersionRepo(),
		events:   &fakeEvents{},
		ids:      &fixedIDs{next: 5000},
	}
	f.service = application.NewRequirementService(
		f.repo, f.versions, f.events,
		application.WithRequirementIDGenerator(f.ids),
	)
	f.relations = application.NewRelationService(
		f.repo,
		application.WithRelationIDGenerator(f.ids),
		application.WithRelationEventPublisher(f.events),
	)
	return f
}

// seedVersion stores a version with the given status.
func (f *requirementFixture) seedVersion(status string) domain.Version {
	return f.versions.seed(domain.Version{
		ID: 100, Name: "Order v2", Status: status, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
	})
}

// seedRequirement stores a requirement in the given version and status.
func (f *requirementFixture) seedRequirement(versionID int64, status string) domain.Requirement {
	requirement := domain.Requirement{
		ID:                 f.ids.Next(),
		VersionID:          versionID,
		Title:              "Checkout flow",
		Description:        "checkout",
		AcceptanceCriteria: "coupons apply",
		Priority:           domain.PriorityShould,
		Status:             status,
		Source:             domain.SourceUser,
		LastUpdateSource:   domain.SourceUser,
		Revision:           1,
		CreatedBy:          7,
		UpdatedBy:          7,
		CreatedAt:          testNow,
		UpdatedAt:          testNow,
	}
	if status == domain.RequirementClarified {
		confirmedAt := testNow
		confirmedBy := int64(7)
		requirement.ConfirmedAt = &confirmedAt
		requirement.ConfirmedBy = &confirmedBy
	}
	f.repo.requirements[requirement.ID] = requirement
	return requirement
}

// TestCreateRequirementDoesNotChangeVersionStatus covers spec §6.2/INV-4: adding
// a requirement leaves Version.status and revision untouched in every P3 status.
func TestCreateRequirementDoesNotChangeVersionStatus(t *testing.T) {
	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
	} {
		t.Run(status, func(t *testing.T) {
			f := newRequirementFixture()
			version := f.seedVersion(status)

			created, err := f.service.Create(context.Background(), version.ID,
				application.CreateRequirementCommand{
					Title: "Checkout flow", Description: "checkout",
					AcceptanceCriteria: "coupons apply",
				}, 42)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if created.ID != 5000 || created.VersionID != version.ID {
				t.Fatalf("created id/version = %d/%d, want 5000/%d", created.ID, created.VersionID, version.ID)
			}
			if created.Status != domain.RequirementDraft || created.Revision != 1 {
				t.Fatalf("status/revision = %s/%d, want draft/1", created.Status, created.Revision)
			}
			if created.Source != domain.SourceUser || created.LastUpdateSource != domain.SourceUser {
				t.Fatalf("source/lastUpdateSource = %s/%s, want user/user", created.Source, created.LastUpdateSource)
			}
			if created.Priority != domain.PriorityShould {
				t.Fatalf("priority = %q, want the should default", created.Priority)
			}
			if created.ConfirmedAt != nil || created.ConfirmedBy != nil {
				t.Fatalf("new requirement already confirmed: %+v", created)
			}
			if created.CreatedBy != 42 || created.UpdatedBy != 42 {
				t.Fatalf("createdBy/updatedBy = %d/%d, want 42/42", created.CreatedBy, created.UpdatedBy)
			}
			if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
				t.Fatalf("timestamps not populated: %+v", created)
			}

			storedVersion, err := f.versions.Get(context.Background(), version.ID)
			if err != nil {
				t.Fatalf("Get version: %v", err)
			}
			if storedVersion.Status != status || storedVersion.Revision != 1 {
				t.Fatalf("requirement create changed the version: status=%s revision=%d, want %s/1",
					storedVersion.Status, storedVersion.Revision, status)
			}

			if len(f.events.events) != 1 {
				t.Fatalf("events = %d, want 1", len(f.events.events))
			}
			event := f.events.events[0]
			if event.Type != "requirement.added" || event.VersionID != version.ID ||
				event.RefRequirementID == nil || *event.RefRequirementID != created.ID ||
				event.ActorID != 42 || event.Source != domain.SourceUser {
				t.Fatalf("event = %+v", event)
			}
		})
	}
}

// TestCreateRequirementValidatesAndScopesVersion covers version existence,
// domain validation and the default/priority rules.
func TestCreateRequirementValidatesAndScopesVersion(t *testing.T) {
	t.Run("missing version is 404", func(t *testing.T) {
		f := newRequirementFixture()

		_, err := f.service.Create(context.Background(), 999,
			application.CreateRequirementCommand{Title: "Checkout flow"}, 42)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
		if len(f.repo.requirements) != 0 || len(f.events.events) != 0 {
			t.Fatalf("failed create wrote rows=%d events=%d", len(f.repo.requirements), len(f.events.events))
		}
	})

	t.Run("empty title is 422", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDraft)

		_, err := f.service.Create(context.Background(), version.ID,
			application.CreateRequirementCommand{Title: "   "}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "invalid_title")
		if len(f.repo.requirements) != 0 || len(f.events.events) != 0 {
			t.Fatalf("invalid create wrote rows=%d events=%d", len(f.repo.requirements), len(f.events.events))
		}
	})

	t.Run("invalid priority is 422", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDraft)

		_, err := f.service.Create(context.Background(), version.ID,
			application.CreateRequirementCommand{Title: "Checkout flow", Priority: "critical"}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "invalid_priority")
	})

	t.Run("explicit priority and ai source are kept", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionClarifying)

		created, err := f.service.Create(context.Background(), version.ID,
			application.CreateRequirementCommand{
				Title: "Checkout flow", Priority: domain.PriorityMust, Source: domain.SourceAI,
			}, 42)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created.Priority != domain.PriorityMust {
			t.Fatalf("priority = %q, want must", created.Priority)
		}
		if created.Source != domain.SourceAI || created.LastUpdateSource != domain.SourceAI {
			t.Fatalf("source/lastUpdateSource = %s/%s, want ai/ai", created.Source, created.LastUpdateSource)
		}
		if len(f.events.events) != 1 || f.events.events[0].Source != domain.SourceAI {
			t.Fatalf("events = %+v", f.events.events)
		}
	})
}

// TestConfirmRequirementRequiresZeroOpenQuestions covers the confirmation gate of
// spec §6.3/INV-3: only the requirement's own open questions block it.
func TestConfirmRequirementRequiresZeroOpenQuestions(t *testing.T) {
	t.Run("open questions are a 409 gate with no write", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionClarifying)
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)
		f.repo.openQuestions[requirement.ID] = 2

		_, err := f.service.Confirm(context.Background(), version.ID, requirement.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "open_questions_remaining")
		if got := f.repo.requirements[requirement.ID]; got.Status != domain.RequirementDraft ||
			got.Revision != 1 || got.ConfirmedAt != nil {
			t.Fatalf("blocked confirm wrote the requirement: %+v", got)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("blocked confirm published events: %+v", f.events.events)
		}
		storedVersion, err := f.versions.Get(context.Background(), version.ID)
		if err != nil {
			t.Fatalf("Get version: %v", err)
		}
		if storedVersion.Status != domain.VersionClarifying || storedVersion.Revision != 1 {
			t.Fatalf("blocked confirm changed the version: %+v", storedVersion)
		}
	})

	t.Run("zero open questions confirms the requirement", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDesigning)
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)
		// Version-level open questions never block a single requirement.
		f.repo.openQuestions[999] = 3

		confirmed, err := f.service.Confirm(context.Background(), version.ID, requirement.ID, 42)
		if err != nil {
			t.Fatalf("Confirm: %v", err)
		}

		if confirmed.Status != domain.RequirementClarified || confirmed.Revision != 2 {
			t.Fatalf("status/revision = %s/%d, want clarified/2", confirmed.Status, confirmed.Revision)
		}
		if confirmed.ConfirmedAt == nil || confirmed.ConfirmedBy == nil || *confirmed.ConfirmedBy != 42 {
			t.Fatalf("confirmation fields = %v/%v", confirmed.ConfirmedAt, confirmed.ConfirmedBy)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		event := f.events.events[0]
		if event.Type != "requirement.clarified" || event.RefRequirementID == nil ||
			*event.RefRequirementID != requirement.ID || event.ActorID != 42 {
			t.Fatalf("event = %+v", event)
		}
		storedVersion, err := f.versions.Get(context.Background(), version.ID)
		if err != nil {
			t.Fatalf("Get version: %v", err)
		}
		if storedVersion.Status != domain.VersionDesigning || storedVersion.Revision != 1 {
			t.Fatalf("confirm changed the version: %+v", storedVersion)
		}
	})
}

// TestRequirementConfirmReopenStateConflicts covers the status CAS of confirm and
// reopen and the cleared confirmation fields of a reopen.
func TestRequirementConfirmReopenStateConflicts(t *testing.T) {
	t.Run("confirming a clarified requirement is a state conflict", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionClarifying)
		requirement := f.seedRequirement(version.ID, domain.RequirementClarified)

		_, err := f.service.Confirm(context.Background(), version.ID, requirement.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
		if len(f.events.events) != 0 {
			t.Fatalf("failed confirm published events: %+v", f.events.events)
		}
	})

	t.Run("reopening a draft requirement is a state conflict", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDraft)
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)

		_, err := f.service.Reopen(context.Background(), version.ID, requirement.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
	})

	t.Run("reopening a clarified requirement clears the confirmation", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDesigning)
		requirement := f.seedRequirement(version.ID, domain.RequirementClarified)
		f.repo.openQuestions[requirement.ID] = 1

		reopened, err := f.service.Reopen(context.Background(), version.ID, requirement.ID, 42)
		if err != nil {
			t.Fatalf("Reopen: %v", err)
		}

		if reopened.Status != domain.RequirementDraft || reopened.Revision != 2 {
			t.Fatalf("status/revision = %s/%d, want draft/2", reopened.Status, reopened.Revision)
		}
		if reopened.ConfirmedAt != nil || reopened.ConfirmedBy != nil {
			t.Fatalf("reopen kept confirmation fields: %v/%v", reopened.ConfirmedAt, reopened.ConfirmedBy)
		}
		if got := f.repo.openQuestions[requirement.ID]; got != 1 {
			t.Fatalf("reopen dropped questions: %d, want 1", got)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "requirement.reopened" {
			t.Fatalf("events = %+v", f.events.events)
		}
		storedVersion, err := f.versions.Get(context.Background(), version.ID)
		if err != nil {
			t.Fatalf("Get version: %v", err)
		}
		if storedVersion.Status != domain.VersionDesigning || storedVersion.Revision != 1 {
			t.Fatalf("reopen changed the version: %+v", storedVersion)
		}
	})
}

// TestClarifiedRequirementReadOnlyUntilReopen covers INV-9: a clarified
// requirement rejects edits and deletes until it is reopened.
func TestClarifiedRequirementReadOnlyUntilReopen(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionClarifying)
	requirement := f.seedRequirement(version.ID, domain.RequirementClarified)

	_, err := f.service.Update(context.Background(), version.ID, requirement.ID,
		application.UpdateRequirementCommand{Revision: 1, Title: ptr("Renamed")}, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
	err = f.service.Delete(context.Background(), version.ID, requirement.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
	if _, ok := f.repo.requirements[requirement.ID]; !ok {
		t.Fatal("blocked delete removed the clarified requirement")
	}
	if len(f.events.events) != 0 {
		t.Fatalf("blocked writes published events: %+v", f.events.events)
	}

	if _, err := f.service.Reopen(context.Background(), version.ID, requirement.ID, 42); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	updated, err := f.service.Update(context.Background(), version.ID, requirement.ID,
		application.UpdateRequirementCommand{Revision: 2, Title: ptr("Renamed")}, 42)
	if err != nil {
		t.Fatalf("Update after reopen: %v", err)
	}
	if updated.Title != "Renamed" || updated.Status != domain.RequirementDraft || updated.Revision != 3 {
		t.Fatalf("updated = %+v", updated)
	}
}

// TestDeleteRequirementOnlyDraftAndCascades covers spec §6.2/INV-10: only draft
// requirements are deletable and the delete cascades children.
func TestDeleteRequirementOnlyDraftAndCascades(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionClarifying)
	requirement := f.seedRequirement(version.ID, domain.RequirementDraft)
	other := f.seedRequirement(version.ID, domain.RequirementDraft)
	f.repo.relations[9001] = domain.Relation{
		ID: 9001, VersionID: version.ID,
		FromRequirementID: requirement.ID, ToRequirementID: other.ID,
		RelationType: domain.RelationDependsOn, Source: domain.SourceUser, CreatedBy: 7,
	}
	f.repo.relations[9002] = domain.Relation{
		ID: 9002, VersionID: version.ID,
		FromRequirementID: other.ID, ToRequirementID: requirement.ID,
		RelationType: domain.RelationOverlaps, Source: domain.SourceUser, CreatedBy: 7,
	}
	f.repo.attachments[9101] = domain.Attachment{
		ID: 9101, VersionID: version.ID, RequirementID: requirement.ID,
		Kind: domain.AttachmentText, Filename: "notes.txt", MIMEType: "text/plain",
		SizeBytes: 3, SHA256: "abc", StoragePath: "100/9101_notes.txt", CreatedBy: 7,
	}
	f.repo.openQuestions[requirement.ID] = 1

	if err := f.service.Delete(context.Background(), version.ID, requirement.ID, 42); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, ok := f.repo.requirements[requirement.ID]; ok {
		t.Fatal("draft requirement was not deleted")
	}
	for relationID := range f.repo.relations {
		if relationID == 9001 || relationID == 9002 {
			t.Fatalf("relation %d survived the requirement delete", relationID)
		}
	}
	if len(f.repo.attachments) != 0 {
		t.Fatalf("attachments survived the requirement delete: %+v", f.repo.attachments)
	}
	if len(f.repo.openQuestions) != 0 {
		t.Fatalf("open question counters survived the requirement delete: %+v", f.repo.openQuestions)
	}
	if _, ok := f.repo.requirements[other.ID]; !ok {
		t.Fatal("delete removed a different requirement")
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %d, want 1", len(f.events.events))
	}
	event := f.events.events[0]
	if event.Type != "requirement.deleted" || event.RefRequirementID == nil ||
		*event.RefRequirementID != requirement.ID {
		t.Fatalf("event = %+v", event)
	}
	storedVersion, err := f.versions.Get(context.Background(), version.ID)
	if err != nil {
		t.Fatalf("Get version: %v", err)
	}
	if storedVersion.Status != domain.VersionClarifying || storedVersion.Revision != 1 {
		t.Fatalf("requirement delete changed the version: %+v", storedVersion)
	}
}

// TestRequirementRevisionConflict covers the optimistic lock of spec §6.10.
func TestRequirementRevisionConflict(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionDraft)
	requirement := f.seedRequirement(version.ID, domain.RequirementDraft)
	f.repo.requirements[requirement.ID] = withRequirementRevision(f.repo.requirements[requirement.ID], 2)

	_, err := f.service.Update(context.Background(), version.ID, requirement.ID,
		application.UpdateRequirementCommand{Revision: 1, Title: ptr("Stale")}, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "revision_conflict")
	if got := f.repo.requirements[requirement.ID]; got.Title != "Checkout flow" || got.Revision != 2 {
		t.Fatalf("stale update was applied: %+v", got)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("failed update published events: %+v", f.events.events)
	}

	updated, err := f.service.Update(context.Background(), version.ID, requirement.ID,
		application.UpdateRequirementCommand{
			Revision: 2, Title: ptr("Coupons"), Description: ptr("new"), Source: domain.SourceAI,
		}, 42)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Coupons" || updated.Description != "new" ||
		updated.AcceptanceCriteria != "coupons apply" || updated.Priority != domain.PriorityShould {
		t.Fatalf("updated fields = %+v", updated)
	}
	if updated.Revision != 3 || updated.UpdatedBy != 42 ||
		updated.Source != domain.SourceUser || updated.LastUpdateSource != domain.SourceAI {
		t.Fatalf("revision/updatedBy/source/lastUpdateSource = %d/%d/%s/%s",
			updated.Revision, updated.UpdatedBy, updated.Source, updated.LastUpdateSource)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %d, want 1", len(f.events.events))
	}
	event := f.events.events[0]
	if event.Type != "requirement.updated" || event.Source != domain.SourceAI || event.ActorID != 42 {
		t.Fatalf("event = %+v", event)
	}
	changed, ok := event.Detail["changed"].([]string)
	if !ok || len(changed) != 2 || changed[0] != "title" || changed[1] != "description" {
		t.Fatalf("event changed = %#v, want [title description]", event.Detail["changed"])
	}
}

// TestRequirementUpdateValidationAndScoping covers validation, partial updates
// and version scoping of the update path.
func TestRequirementUpdateValidationAndScoping(t *testing.T) {
	t.Run("missing requirement is 404", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDraft)

		_, err := f.service.Update(context.Background(), version.ID, 999,
			application.UpdateRequirementCommand{Revision: 1, Title: ptr("X")}, 42)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})

	t.Run("requirement of another version is 404", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDraft)
		otherVersion := f.versions.seed(domain.Version{ID: 200, Name: "Other", CreatedBy: 1, UpdatedBy: 1})
		requirement := f.seedRequirement(otherVersion.ID, domain.RequirementDraft)

		_, err := f.service.Update(context.Background(), version.ID, requirement.ID,
			application.UpdateRequirementCommand{Revision: 1, Title: ptr("X")}, 42)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})

	t.Run("empty title and invalid priority are 422", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDraft)
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)

		_, err := f.service.Update(context.Background(), version.ID, requirement.ID,
			application.UpdateRequirementCommand{Revision: 1, Title: ptr("  ")}, 42)
		assertDomainReason(t, err, domain.KindInvalid, "invalid_title")

		_, err = f.service.Update(context.Background(), version.ID, requirement.ID,
			application.UpdateRequirementCommand{Revision: 1, Priority: ptr("critical")}, 42)
		assertDomainReason(t, err, domain.KindInvalid, "invalid_priority")
		if len(f.events.events) != 0 {
			t.Fatalf("invalid updates published events: %+v", f.events.events)
		}
	})

	t.Run("a partial update keeps the other fields", func(t *testing.T) {
		f := newRequirementFixture()
		version := f.seedVersion(domain.VersionDraft)
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)

		updated, err := f.service.Update(context.Background(), version.ID, requirement.ID,
			application.UpdateRequirementCommand{Revision: 1, Priority: ptr(domain.PriorityCould)}, 42)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if updated.Title != requirement.Title || updated.Description != requirement.Description ||
			updated.AcceptanceCriteria != requirement.AcceptanceCriteria {
			t.Fatalf("partial update changed unrelated fields: %+v", updated)
		}
		if updated.Priority != domain.PriorityCould {
			t.Fatalf("priority = %q, want could", updated.Priority)
		}
	})
}

// TestRequirementReads covers the read passthroughs used by the HTTP layer.
func TestRequirementReads(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionDraft)
	first := f.seedRequirement(version.ID, domain.RequirementDraft)
	second := f.seedRequirement(version.ID, domain.RequirementClarified)
	otherVersion := f.versions.seed(domain.Version{ID: 200, Name: "Other", CreatedBy: 1, UpdatedBy: 1})

	loaded, err := f.service.Get(context.Background(), version.ID, first.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.ID != first.ID {
		t.Fatalf("loaded id = %d, want %d", loaded.ID, first.ID)
	}
	if _, err := f.service.Get(context.Background(), otherVersion.ID, first.ID); err == nil {
		t.Fatal("Get returned a requirement of another version")
	}

	page, more, err := f.service.List(context.Background(), version.ID, "", domain.Page{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 2 || page[0].ID != first.ID || page[1].ID != second.ID || more {
		t.Fatalf("page = %+v more=%v", page, more)
	}

	clarifiedPage, more, err := f.service.List(context.Background(), version.ID,
		domain.RequirementClarified, domain.Page{Limit: 10})
	if err != nil {
		t.Fatalf("List clarified: %v", err)
	}
	if len(clarifiedPage) != 1 || clarifiedPage[0].ID != second.ID || more {
		t.Fatalf("clarified page = %+v more=%v", clarifiedPage, more)
	}
}

// TestRequirementEventPublishFailureIsNonFatal mirrors the version service: a
// failed event publication never fails a committed write (spec §6.10).
func TestRequirementEventPublishFailureIsNonFatal(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionDraft)
	f.events.publishErr = fmt.Errorf("event hub unavailable")

	if _, err := f.service.Create(context.Background(), version.ID,
		application.CreateRequirementCommand{Title: "Checkout flow"}, 42); err != nil {
		t.Fatalf("Create must succeed when event publication fails: %v", err)
	}
	if len(f.repo.requirements) != 1 {
		t.Fatalf("stored requirements = %d, want 1", len(f.repo.requirements))
	}
	if len(f.events.events) != 1 {
		t.Fatalf("publish attempts = %d, want 1", len(f.events.events))
	}
}

// withRequirementRevision returns a copy of requirement with the given revision.
func withRequirementRevision(requirement domain.Requirement, revision int) domain.Requirement {
	requirement.Revision = revision
	return requirement
}
