//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// requirementIntegrationSHA is a valid 64-character lowercase sha256.
const requirementIntegrationSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// requirementFixture wires the real requirement/relation services to the real
// PostgreSQL repositories.
type requirementFixture struct {
	pool         *pgxpool.Pool
	requirements *postgres.RequirementRepository
	versions     *postgres.VersionRepository
	events       *integrationEvents
	ids          *integrationIDs
	service      *application.RequirementService
	relations    *application.RelationService
}

// newRequirementFixture opens the migrated test database and wires the services.
func newRequirementFixture(t *testing.T) *requirementFixture {
	t.Helper()
	pool := testutil.OpenTestDB(t)
	requirementRepo := postgres.NewRequirementRepository(pool)
	versionRepo := postgres.NewVersionRepository(pool)
	events := &integrationEvents{}
	ids := &integrationIDs{next: 91001}
	fixture := &requirementFixture{
		pool:         pool,
		requirements: requirementRepo,
		versions:     versionRepo,
		events:       events,
		ids:          ids,
	}
	fixture.service = application.NewRequirementService(
		requirementRepo, versionRepo, events,
		application.WithRequirementIDGenerator(ids),
	)
	fixture.relations = application.NewRelationService(
		requirementRepo,
		application.WithRelationIDGenerator(ids),
		application.WithRelationEventPublisher(events),
	)
	return fixture
}

// seedVersionT inserts a version with the given status and fails the test on
// error. Repository seeding is enough: requirement operations must never depend
// on how the status was reached.
func (f *requirementFixture) seedVersionT(t *testing.T, id int64, status string) domain.Version {
	t.Helper()
	version := domain.Version{
		ID: id, Name: "Order v2", Status: status, Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
	if err := f.versions.Create(context.Background(), &version); err != nil {
		t.Fatalf("seed version %s: %v", status, err)
	}
	return version
}

// assertVersionUnchanged proves INV-2/INV-4: the version status and revision are
// untouched by requirement operations.
func (f *requirementFixture) assertVersionUnchanged(t *testing.T, version domain.Version) {
	t.Helper()
	stored, err := f.versions.Get(context.Background(), version.ID)
	if err != nil {
		t.Fatalf("Get version: %v", err)
	}
	if stored.Status != version.Status || stored.Revision != version.Revision {
		t.Fatalf("requirement operation changed the version: status=%s revision=%d, want %s/%d",
			stored.Status, stored.Revision, version.Status, version.Revision)
	}
}

// createRequirement creates one requirement through the service and fails the
// test on error.
func (f *requirementFixture) createRequirement(t *testing.T, versionID int64, title string) domain.Requirement {
	t.Helper()
	created, err := f.service.Create(context.Background(), versionID,
		application.CreateRequirementCommand{Title: title, Description: title + " description"}, 42)
	if err != nil {
		t.Fatalf("Create requirement %q: %v", title, err)
	}
	return *created
}

// requirementPtr returns a pointer to v.
func requirementPtr[T any](v T) *T { return &v }

// TestRequirementCreateDoesNotChangeVersionStatus drives the real repository and
// service: adding requirements in draft, clarifying and designing never touches
// Version.status (spec §6.2/INV-4).
func TestRequirementCreateDoesNotChangeVersionStatus(t *testing.T) {
	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
	} {
		t.Run(status, func(t *testing.T) {
			f := newRequirementFixture(t)
			version := f.seedVersionT(t, 9001, status)

			created := f.createRequirement(t, version.ID, "Checkout flow")

			if created.VersionID != version.ID || created.Status != domain.RequirementDraft ||
				created.Revision != 1 || created.Source != domain.SourceUser ||
				created.LastUpdateSource != domain.SourceUser {
				t.Fatalf("created requirement = %+v", created)
			}
			stored, err := f.requirements.Get(context.Background(), created.ID)
			if err != nil {
				t.Fatalf("Get requirement: %v", err)
			}
			if stored.Title != "Checkout flow" || stored.Priority != domain.PriorityShould ||
				stored.CreatedBy != 42 || stored.UpdatedBy != 42 {
				t.Fatalf("stored requirement = %+v", stored)
			}
			if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
				t.Fatalf("timestamps not populated: %+v", stored)
			}
			f.assertVersionUnchanged(t, version)

			if len(f.events.events) != 1 {
				t.Fatalf("events = %v, want [requirement.added]", eventTypes(f.events.events))
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

// TestRequirementConfirmGateAndReopen covers the open-question confirmation gate
// and the reopen transition against the real schema (spec §6.2/§6.3).
func TestRequirementConfirmGateAndReopen(t *testing.T) {
	ctx := context.Background()
	f := newRequirementFixture(t)
	version := f.seedVersionT(t, 9001, domain.VersionClarifying)
	requirement := f.createRequirement(t, version.ID, "Checkout flow")
	f.events.events = nil

	// An open question of the requirement blocks confirmation.
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO clarification_question (id, version_id, requirement_id, question, status, created_by)
		VALUES (92001, $1, $2, 'Which coupons?', 'open', 7)`, version.ID, requirement.ID); err != nil {
		t.Fatalf("insert question: %v", err)
	}
	_, err := f.service.Confirm(ctx, version.ID, requirement.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "open_questions_remaining")
	blocked, err := f.requirements.Get(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("Get after blocked confirm: %v", err)
	}
	if blocked.Status != domain.RequirementDraft || blocked.ConfirmedAt != nil {
		t.Fatalf("blocked confirm wrote the requirement: %+v", blocked)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("blocked confirm published events: %v", eventTypes(f.events.events))
	}
	f.assertVersionUnchanged(t, version)

	// Answering it unblocks the confirmation and stamps confirmed_at/by.
	if _, err := f.pool.Exec(ctx, `
		UPDATE clarification_question SET status = 'answered', answer = 'all coupons'
		WHERE id = 92001`); err != nil {
		t.Fatalf("answer question: %v", err)
	}
	confirmed, err := f.service.Confirm(ctx, version.ID, requirement.ID, 42)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if confirmed.Status != domain.RequirementClarified || confirmed.Revision != 2 ||
		confirmed.ConfirmedAt == nil || confirmed.ConfirmedBy == nil || *confirmed.ConfirmedBy != 42 {
		t.Fatalf("confirmed requirement = %+v", confirmed)
	}
	f.assertVersionUnchanged(t, version)

	// A decision linked to the requirement survives the reopen.
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO decision (id, version_id, requirement_id, topic, decision, status, source, proposed_by)
		VALUES (93001, $1, $2, 'Coupons', 'Apply all coupons', 'accepted', 'user', 42)`,
		version.ID, requirement.ID); err != nil {
		t.Fatalf("insert decision: %v", err)
	}
	reopened, err := f.service.Reopen(ctx, version.ID, requirement.ID, 43)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if reopened.Status != domain.RequirementDraft || reopened.Revision != 3 ||
		reopened.ConfirmedAt != nil || reopened.ConfirmedBy != nil {
		t.Fatalf("reopened requirement = %+v", reopened)
	}
	for _, query := range []string{
		`SELECT count(*)::int FROM clarification_question WHERE id = 92001`,
		`SELECT count(*)::int FROM decision WHERE id = 93001`,
	} {
		var count int
		if err := f.pool.QueryRow(ctx, query).Scan(&count); err != nil {
			t.Fatalf("count child rows: %v", err)
		}
		if count != 1 {
			t.Fatalf("reopen dropped a child row (%s = %d)", query, count)
		}
	}
	f.assertVersionUnchanged(t, version)

	// Re-confirmation is possible again, then the status CASes start failing.
	if _, err := f.service.Confirm(ctx, version.ID, requirement.ID, 44); err != nil {
		t.Fatalf("Confirm after reopen: %v", err)
	}
	_, err = f.service.Confirm(ctx, version.ID, requirement.ID, 44)
	assertIntegrationReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
	if _, err := f.service.Reopen(ctx, version.ID, requirement.ID, 44); err != nil {
		t.Fatalf("Reopen confirmed: %v", err)
	}
	_, err = f.service.Reopen(ctx, version.ID, requirement.ID, 44)
	assertIntegrationReason(t, err, domain.KindStateConflict, "requirement_state_conflict")

	wantTypes := []string{
		"requirement.clarified",
		"requirement.reopened",
		"requirement.clarified",
		"requirement.reopened",
	}
	if len(f.events.events) != len(wantTypes) {
		t.Fatalf("events = %v, want %v", eventTypes(f.events.events), wantTypes)
	}
	for i, want := range wantTypes {
		if f.events.events[i].Type != want {
			t.Fatalf("event %d = %s, want %s (all: %v)", i, f.events.events[i].Type, want, eventTypes(f.events.events))
		}
		if f.events.events[i].RefRequirementID == nil || *f.events.events[i].RefRequirementID != requirement.ID {
			t.Fatalf("event %d = %+v", i, f.events.events[i])
		}
	}
}

// TestRequirementReadOnlyAndRevisionCAS covers the revision CAS, clarified
// read-only rule, source tracking and the draft-only delete against the real
// schema.
func TestRequirementReadOnlyAndRevisionCAS(t *testing.T) {
	ctx := context.Background()
	f := newRequirementFixture(t)
	version := f.seedVersionT(t, 9001, domain.VersionDraft)
	requirement := f.createRequirement(t, version.ID, "Checkout flow")
	f.events.events = nil

	// A stale revision is a revision_conflict and never writes.
	_, err := f.service.Update(ctx, version.ID, requirement.ID, application.UpdateRequirementCommand{
		Revision: requirement.Revision + 1, Title: requirementPtr("Stale"),
	}, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "revision_conflict")
	unchanged, err := f.requirements.Get(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("Get after stale update: %v", err)
	}
	if unchanged.Title != requirement.Title || unchanged.Revision != requirement.Revision {
		t.Fatalf("stale update wrote the requirement: %+v", unchanged)
	}

	// A matching revision applies a partial update; an AI caller stamps
	// last_update_source but never rewrites source.
	updated, err := f.service.Update(ctx, version.ID, requirement.ID, application.UpdateRequirementCommand{
		Revision: requirement.Revision,
		Title:    requirementPtr("Coupons"),
		Priority: requirementPtr(domain.PriorityMust),
		Source:   domain.SourceAI,
	}, 42)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Coupons" || updated.Priority != domain.PriorityMust ||
		updated.Description != requirement.Description || updated.Revision != 2 {
		t.Fatalf("updated requirement = %+v", updated)
	}
	if updated.Source != domain.SourceUser || updated.LastUpdateSource != domain.SourceAI {
		t.Fatalf("source/lastUpdateSource = %s/%s, want user/ai", updated.Source, updated.LastUpdateSource)
	}

	// Clarified requirements reject edits and deletes until reopened.
	if _, err := f.service.Confirm(ctx, version.ID, requirement.ID, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	_, err = f.service.Update(ctx, version.ID, requirement.ID, application.UpdateRequirementCommand{
		Revision: 2, Title: requirementPtr("After confirm"),
	}, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
	err = f.service.Delete(ctx, version.ID, requirement.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "requirement_state_conflict")
	if _, err := f.requirements.Get(ctx, requirement.ID); err != nil {
		t.Fatalf("clarified requirement was deleted: %v", err)
	}

	// Reopening restores editability; deleting a draft is a hard delete.
	if _, err := f.service.Reopen(ctx, version.ID, requirement.ID, 42); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := f.service.Delete(ctx, version.ID, requirement.ID, 42); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.requirements.Get(ctx, requirement.ID); err == nil {
		t.Fatal("deleted requirement still reads back")
	}

	f.assertVersionUnchanged(t, version)
	wantTypes := []string{
		"requirement.updated",
		"requirement.clarified",
		"requirement.reopened",
		"requirement.deleted",
	}
	if len(f.events.events) != len(wantTypes) {
		t.Fatalf("events = %v, want %v", eventTypes(f.events.events), wantTypes)
	}
	for i, want := range wantTypes {
		if f.events.events[i].Type != want {
			t.Fatalf("event %d = %s, want %s (all: %v)", i, f.events.events[i].Type, want, eventTypes(f.events.events))
		}
	}
	if f.events.events[0].Source != domain.SourceAI {
		t.Fatalf("update event source = %s, want ai", f.events.events[0].Source)
	}
}

// TestRequirementDeleteCascades covers the requirement-level cascade of spec
// §6.2/INV-10 against the real foreign keys.
func TestRequirementDeleteCascades(t *testing.T) {
	ctx := context.Background()
	f := newRequirementFixture(t)
	version := f.seedVersionT(t, 9001, domain.VersionClarifying)
	deleted := f.createRequirement(t, version.ID, "Delete me")
	kept := f.createRequirement(t, version.ID, "Keep me")

	if _, err := f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: deleted.ID, ToRequirementID: kept.ID,
		RelationType: domain.RelationDependsOn,
	}, 42); err != nil {
		t.Fatalf("Create relation: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO clarification_question (id, version_id, requirement_id, question, status, created_by)
		VALUES (92001, $1, $2, 'Requirement question?', 'open', 7),
		       (92002, $1, NULL, 'Version question?', 'open', 7)`, version.ID, deleted.ID); err != nil {
		t.Fatalf("insert questions: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO decision (id, version_id, requirement_id, topic, decision, status, source, proposed_by)
		VALUES (93001, $1, $2, 'Topic', 'Decision', 'proposed', 'ai', 7)`, version.ID, deleted.ID); err != nil {
		t.Fatalf("insert decision: %v", err)
	}
	attachment := &domain.Attachment{
		ID: 94001, VersionID: version.ID, RequirementID: deleted.ID,
		Kind: domain.AttachmentText, Filename: "notes.txt", MIMEType: "text/plain",
		SizeBytes: 12, SHA256: requirementIntegrationSHA,
		StoragePath: "9001/94001_notes.txt", CreatedBy: 42,
	}
	if err := f.requirements.CreateAttachment(ctx, attachment); err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}

	if err := f.service.Delete(ctx, version.ID, deleted.ID, 42); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, query := range []string{
		`SELECT count(*)::int FROM requirement_relation WHERE from_requirement_id = $1 OR to_requirement_id = $1`,
		`SELECT count(*)::int FROM clarification_question WHERE requirement_id = $1`,
		`SELECT count(*)::int FROM decision WHERE requirement_id = $1`,
		`SELECT count(*)::int FROM requirement_attachment WHERE requirement_id = $1`,
	} {
		var count int
		if err := f.pool.QueryRow(ctx, query, deleted.ID).Scan(&count); err != nil {
			t.Fatalf("count cascade rows (%s): %v", query, err)
		}
		if count != 0 {
			t.Fatalf("cascade left rows (%s = %d)", query, count)
		}
	}
	// The version-level question and the other requirement survive.
	var versionQuestions int
	if err := f.pool.QueryRow(ctx, `SELECT count(*)::int FROM clarification_question WHERE id = 92002`).Scan(&versionQuestions); err != nil {
		t.Fatalf("count version question: %v", err)
	}
	if versionQuestions != 1 {
		t.Fatal("delete removed the version-level question")
	}
	if _, err := f.requirements.Get(ctx, kept.ID); err != nil {
		t.Fatalf("Get kept requirement: %v", err)
	}
	f.assertVersionUnchanged(t, version)
	if len(f.events.events) == 0 || f.events.events[len(f.events.events)-1].Type != "requirement.deleted" {
		t.Fatalf("events = %v, want a trailing requirement.deleted", eventTypes(f.events.events))
	}
}

// TestRequirementRelationsNormalizationAndEvents covers the relation invariants of
// spec §6.4/INV-7 end to end plus the relation event stream.
func TestRequirementRelationsNormalizationAndEvents(t *testing.T) {
	ctx := context.Background()
	f := newRequirementFixture(t)
	version := f.seedVersionT(t, 9001, domain.VersionDraft)
	first := f.createRequirement(t, version.ID, "First")
	second := f.createRequirement(t, version.ID, "Second")
	otherVersion := f.seedVersionT(t, 9002, domain.VersionDraft)
	foreign := f.createRequirement(t, otherVersion.ID, "Foreign")
	f.events.events = nil

	overlaps, err := f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: first.ID, ToRequirementID: second.ID,
		RelationType: domain.RelationOverlaps, Description: "same scope",
	}, 42)
	if err != nil {
		t.Fatalf("Create overlaps: %v", err)
	}
	if overlaps.Source != domain.SourceUser || overlaps.CreatedBy != 42 {
		t.Fatalf("created relation = %+v", overlaps)
	}

	// The reversed undirected pair is a duplicate.
	_, err = f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: second.ID, ToRequirementID: first.ID, RelationType: domain.RelationOverlaps,
	}, 42)
	assertIntegrationReason(t, err, domain.KindConflict, "duplicate_relation")

	// depends_on is directed: both directions are distinct relations.
	if _, err := f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: first.ID, ToRequirementID: second.ID, RelationType: domain.RelationDependsOn,
	}, 42); err != nil {
		t.Fatalf("Create depends_on forward: %v", err)
	}
	reverse, err := f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: second.ID, ToRequirementID: first.ID, RelationType: domain.RelationDependsOn,
	}, 42)
	if err != nil {
		t.Fatalf("Create depends_on reverse: %v", err)
	}

	// Self-loops and cross-version endpoints are 422s.
	_, err = f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: first.ID, ToRequirementID: first.ID, RelationType: domain.RelationConflictsWith,
	}, 42)
	assertIntegrationReason(t, err, domain.KindInvalid, "invalid_relation")
	_, err = f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: first.ID, ToRequirementID: foreign.ID, RelationType: domain.RelationConflictsWith,
	}, 42)
	assertIntegrationReason(t, err, domain.KindInvalid, "relation_requirement_mismatch")

	// Relations remain editable for clarified requirements.
	if _, err := f.service.Confirm(ctx, version.ID, second.ID, 42); err != nil {
		t.Fatalf("Confirm second: %v", err)
	}
	if _, err := f.relations.Create(ctx, version.ID, application.CreateRelationCommand{
		FromRequirementID: first.ID, ToRequirementID: second.ID, RelationType: domain.RelationConflictsWith,
	}, 42); err != nil {
		t.Fatalf("Create relation on a clarified requirement: %v", err)
	}

	// Update keeps the type when only the description changes, and delete works.
	updated, err := f.relations.Update(ctx, version.ID, overlaps.ID, nil, requirementPtr("still the same scope"), 42)
	if err != nil {
		t.Fatalf("Update relation: %v", err)
	}
	if updated.RelationType != domain.RelationOverlaps || updated.Description != "still the same scope" {
		t.Fatalf("updated relation = %+v", updated)
	}
	if err := f.relations.Delete(ctx, version.ID, reverse.ID, 42); err != nil {
		t.Fatalf("Delete relation: %v", err)
	}

	relations, err := f.relations.List(ctx, version.ID)
	if err != nil {
		t.Fatalf("List relations: %v", err)
	}
	if len(relations) != 3 {
		t.Fatalf("relations = %d, want 3 after one delete", len(relations))
	}
	f.assertVersionUnchanged(t, version)

	wantTypes := []string{
		"requirement.relation_added",   // overlaps
		"requirement.relation_added",   // depends_on forward
		"requirement.relation_added",   // depends_on reverse
		"requirement.clarified",        // second requirement confirmed
		"requirement.relation_added",   // conflicts_with on clarified
		"requirement.relation_updated", // overlaps description
		"requirement.relation_removed", // depends_on reverse
	}
	if len(f.events.events) != len(wantTypes) {
		t.Fatalf("events = %v, want %v", eventTypes(f.events.events), wantTypes)
	}
	for i, want := range wantTypes {
		if f.events.events[i].Type != want {
			t.Fatalf("event %d = %s, want %s (all: %v)", i, f.events.events[i].Type, want, eventTypes(f.events.events))
		}
		if f.events.events[i].VersionID != version.ID {
			t.Fatalf("event %d version = %d, want %d", i, f.events.events[i].VersionID, version.ID)
		}
	}
}

// TestRequirementSourceTracking covers the source/last_update_source contract:
// HTTP callers are user, agent tools are ai (spec §6.2/§6.9).
func TestRequirementSourceTracking(t *testing.T) {
	ctx := context.Background()
	f := newRequirementFixture(t)
	version := f.seedVersionT(t, 9001, domain.VersionDraft)

	created, err := f.service.Create(ctx, version.ID, application.CreateRequirementCommand{
		Title: "AI draft", Source: domain.SourceAI,
	}, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Source != domain.SourceAI || created.LastUpdateSource != domain.SourceAI {
		t.Fatalf("source/lastUpdateSource = %s/%s, want ai/ai", created.Source, created.LastUpdateSource)
	}
	if len(f.events.events) != 1 || f.events.events[0].Source != domain.SourceAI {
		t.Fatalf("events = %v", eventTypes(f.events.events))
	}

	updated, err := f.service.Update(ctx, version.ID, created.ID, application.UpdateRequirementCommand{
		Revision: created.Revision, Description: requirementPtr("human edit"),
	}, 43)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Source != domain.SourceAI || updated.LastUpdateSource != domain.SourceUser {
		t.Fatalf("source/lastUpdateSource = %s/%s, want ai/user", updated.Source, updated.LastUpdateSource)
	}
	if len(f.events.events) != 2 || f.events.events[1].Source != domain.SourceUser {
		t.Fatalf("events = %v", eventTypes(f.events.events))
	}
}
