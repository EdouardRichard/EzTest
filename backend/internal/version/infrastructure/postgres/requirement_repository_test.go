//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// requirementTestSHA is a valid 64-character lowercase sha256 for attachments.
const requirementTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// openRequirementTestDB opens the migrated test database and the requirement and
// version repositories on it.
func openRequirementTestDB(t *testing.T) (*pgxpool.Pool, *postgres.RequirementRepository, *postgres.VersionRepository) {
	t.Helper()
	pool := testutil.OpenTestDB(t)
	return pool, postgres.NewRequirementRepository(pool), postgres.NewVersionRepository(pool)
}

// insertRequirement creates one requirement through the repository and returns
// the stored row.
func insertRequirement(t *testing.T, repo *postgres.RequirementRepository, requirement domain.Requirement) domain.Requirement {
	t.Helper()
	if requirement.Status == "" {
		requirement.Status = domain.RequirementDraft
	}
	if requirement.Priority == "" {
		requirement.Priority = domain.PriorityShould
	}
	if requirement.Source == "" {
		requirement.Source = domain.SourceUser
	}
	if requirement.LastUpdateSource == "" {
		requirement.LastUpdateSource = domain.SourceUser
	}
	if requirement.Revision == 0 {
		requirement.Revision = 1
	}
	if err := repo.Create(context.Background(), &requirement); err != nil {
		t.Fatalf("create requirement %d: %v", requirement.ID, err)
	}
	return requirement
}

// assertRequirementKind asserts err is a *domain.Error with kind and reason.
func assertRequirementKind(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want *domain.Error kind=%s reason=%s, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("error kind/reason = %s/%s, want %s/%s (%v)",
			domainErr.Kind, domainErr.Reason, kind, reason, err)
	}
	return domainErr
}

// requirementIDs projects requirement ids for readable failures.
func requirementIDs(requirements []domain.Requirement) []int64 {
	ids := make([]int64, len(requirements))
	for i, requirement := range requirements {
		ids[i] = requirement.ID
	}
	return ids
}

// assertRequirementStored compares a stored requirement with the expected one,
// ignoring server timestamps.
func assertRequirementStored(t *testing.T, got, want domain.Requirement) {
	t.Helper()
	if got.ID != want.ID || got.VersionID != want.VersionID ||
		got.Title != want.Title || got.Description != want.Description ||
		got.AcceptanceCriteria != want.AcceptanceCriteria || got.Priority != want.Priority ||
		got.Status != want.Status || got.Source != want.Source ||
		got.LastUpdateSource != want.LastUpdateSource || got.Revision != want.Revision ||
		got.CreatedBy != want.CreatedBy || got.UpdatedBy != want.UpdatedBy {
		t.Fatalf("requirement = %+v\nwant %+v", got, want)
	}
	if !sameVersionInt64(got.ConfirmedBy, want.ConfirmedBy) {
		t.Fatalf("confirmedBy = %v, want %v", got.ConfirmedBy, want.ConfirmedBy)
	}
	if (got.ConfirmedAt == nil) != (want.ConfirmedAt == nil) {
		t.Fatalf("confirmedAt = %v, want %v", got.ConfirmedAt, want.ConfirmedAt)
	}
}

// TestRequirementRepositoryLifecycle covers create/get/list ordering, the revision
// CAS, confirmation gating and the reopen transition against the real schema.
func TestRequirementRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	pool, repo, versions := openRequirementTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Order v2", CreatedBy: 7, UpdatedBy: 7})

	created := insertRequirement(t, repo, domain.Requirement{
		ID: 2001, VersionID: version.ID,
		Title: "Checkout flow", Description: "checkout", AcceptanceCriteria: "coupons apply",
		Priority: domain.PriorityMust, Status: domain.RequirementDraft,
		Source: domain.SourceAI, LastUpdateSource: domain.SourceAI,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created timestamps not populated: %+v", created)
	}
	loaded, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertRequirementStored(t, *loaded, created)
	if !loaded.CreatedAt.Equal(created.CreatedAt) || !loaded.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("timestamps did not round-trip: %v/%v vs %v/%v",
			loaded.CreatedAt, loaded.UpdatedAt, created.CreatedAt, created.UpdatedAt)
	}

	// Missing rows are not-found reads.
	_, err = repo.Get(ctx, 999999)
	assertRequirementKind(t, err, domain.KindNotFound, "not_found")

	// Duplicate explicit Snowflake ids are a uniqueness conflict.
	duplicate := &domain.Requirement{
		ID: 2001, VersionID: version.ID, Title: "Duplicate", Priority: domain.PriorityShould,
		Status: domain.RequirementDraft, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
	assertRequirementKind(t, repo.Create(ctx, duplicate), domain.KindConflict, "duplicate_key")

	// A requirement of a missing version is a foreign-key failure reported as a
	// missing version.
	orphan := &domain.Requirement{
		ID: 2099, VersionID: 999999, Title: "Orphan", Priority: domain.PriorityShould,
		Status: domain.RequirementDraft, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
	assertRequirementKind(t, repo.Create(ctx, orphan), domain.KindNotFound, "not_found")

	// Update is a revision CAS on a draft row.
	candidate := *loaded
	candidate.Title = "Coupons"
	candidate.Description = "coupon flow"
	candidate.AcceptanceCriteria = "coupons apply at checkout"
	candidate.Priority = domain.PriorityCould
	candidate.LastUpdateSource = domain.SourceAI
	candidate.UpdatedBy = 42
	if err := repo.Update(ctx, &candidate, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if candidate.Revision != 2 || candidate.Source != domain.SourceAI ||
		candidate.LastUpdateSource != domain.SourceAI || candidate.UpdatedBy != 42 {
		t.Fatalf("updated requirement = %+v", candidate)
	}
	reloaded, err := repo.Get(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	assertRequirementStored(t, *reloaded, candidate)

	// A stale revision never writes.
	stale := *reloaded
	stale.Title = "Stale"
	assertRequirementKind(t, repo.Update(ctx, &stale, 1), domain.KindStateConflict, "revision_conflict")
	afterStale, err := repo.Get(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("Get after stale update: %v", err)
	}
	if afterStale.Title != "Coupons" || afterStale.Revision != 2 {
		t.Fatalf("stale update wrote the row: %+v", afterStale)
	}

	// List is ordered by created_at, id ascending and supports a keyset cursor.
	insertRequirement(t, repo, domain.Requirement{
		ID: 2002, VersionID: version.ID, Title: "Second", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	insertRequirement(t, repo, domain.Requirement{
		ID: 2003, VersionID: version.ID, Title: "Third", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	base := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	for id, at := range map[int64]time.Time{
		2001: base.Add(3 * time.Minute),
		2002: base.Add(time.Minute),
		2003: base.Add(2 * time.Minute),
	} {
		if _, err := pool.Exec(ctx, `UPDATE requirement SET created_at = $2 WHERE id = $1`, id, at); err != nil {
			t.Fatalf("set created_at for %d: %v", id, err)
		}
	}
	page, more, err := repo.List(ctx, version.ID, "", domain.Page{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := requirementIDs(page); len(got) != 2 || got[0] != 2002 || got[1] != 2003 || !more {
		t.Fatalf("first page ids=%v more=%v, want [2002 2003] true", requirementIDs(page), more)
	}
	next, more, err := repo.List(ctx, version.ID, "", domain.Page{
		Limit: 2, Cursor: platform.EncodeCursor(page[len(page)-1].ID),
	})
	if err != nil {
		t.Fatalf("List second page: %v", err)
	}
	if got := requirementIDs(next); len(got) != 1 || got[0] != 2001 || more {
		t.Fatalf("second page ids=%v more=%v, want [2001] false", requirementIDs(next), more)
	}

	// Status filtering happens before pagination.
	clarified, more, err := repo.List(ctx, version.ID, domain.RequirementClarified, domain.Page{Limit: 10})
	if err != nil {
		t.Fatalf("List clarified: %v", err)
	}
	if len(clarified) != 0 || more {
		t.Fatalf("clarified page = %v more=%v", requirementIDs(clarified), more)
	}
	if _, _, err := repo.List(ctx, version.ID, "", domain.Page{Limit: 0}); err == nil {
		t.Fatal("List accepted a zero limit")
	}
	if other, _, err := repo.List(ctx, 999999, "", domain.Page{Limit: 10}); err != nil || len(other) != 0 {
		t.Fatalf("list on a missing version = %v/%v", other, err)
	}

	// Confirmation requires this requirement's open questions to be zero.
	if _, err := pool.Exec(ctx, `
		INSERT INTO clarification_question (id, version_id, requirement_id, question, status, created_by)
		VALUES (3001, $1, 2001, 'Which coupons?', 'open', 7)`, version.ID); err != nil {
		t.Fatalf("insert question: %v", err)
	}
	open, err := repo.CountOpenQuestions(ctx, 2001)
	if err != nil {
		t.Fatalf("CountOpenQuestions: %v", err)
	}
	if open != 1 {
		t.Fatalf("open questions = %d, want 1", open)
	}
	assertRequirementKind(t, mustUpdateStatus(repo, ctx, 2001, domain.RequirementDraft, domain.RequirementClarified, 42),
		domain.KindStateConflict, "open_questions_remaining")

	// Answering the question unblocks the confirmation and stamps the audit
	// fields.
	if _, err := pool.Exec(ctx, `UPDATE clarification_question SET status = 'answered', answer = 'all' WHERE id = 3001`); err != nil {
		t.Fatalf("answer question: %v", err)
	}
	confirmed, err := repo.UpdateStatus(ctx, 2001, domain.RequirementDraft, domain.RequirementClarified, 42)
	if err != nil {
		t.Fatalf("UpdateStatus closed: %v", err)
	}
	if confirmed.Status != domain.RequirementClarified || confirmed.Revision != 3 ||
		confirmed.ConfirmedAt == nil || confirmed.ConfirmedBy == nil || *confirmed.ConfirmedBy != 42 ||
		confirmed.UpdatedBy != 42 || confirmed.LastUpdateSource != domain.SourceUser {
		t.Fatalf("confirmed requirement = %+v", confirmed)
	}

	// A confirmed requirement is read-only for the revision CAS: the update is
	// rejected as a state conflict even with the matching revision.
	readOnly := *confirmed
	readOnly.Title = "Edited after confirm"
	assertRequirementKind(t, repo.Update(ctx, &readOnly, confirmed.Revision),
		domain.KindStateConflict, "requirement_state_conflict")
	afterReadOnly, err := repo.Get(ctx, 2001)
	if err != nil {
		t.Fatalf("Get after blocked update: %v", err)
	}
	if afterReadOnly.Title != "Coupons" {
		t.Fatalf("blocked update wrote the row: %+v", afterReadOnly)
	}

	// Reopen is the reversed status CAS and clears the confirmation fields.
	reopened, err := repo.UpdateStatus(ctx, 2001, domain.RequirementClarified, domain.RequirementDraft, 43)
	if err != nil {
		t.Fatalf("UpdateStatus reopen: %v", err)
	}
	if reopened.Status != domain.RequirementDraft || reopened.Revision != 4 ||
		reopened.ConfirmedAt != nil || reopened.ConfirmedBy != nil || reopened.UpdatedBy != 43 {
		t.Fatalf("reopened requirement = %+v", reopened)
	}
	assertRequirementKind(t, mustUpdateStatus(repo, ctx, 2001, domain.RequirementClarified, domain.RequirementDraft, 43),
		domain.KindStateConflict, "requirement_state_conflict")

	// A status CAS on a missing row is a state conflict, matching the port.
	assertRequirementKind(t, mustUpdateStatus(repo, ctx, 999999, domain.RequirementDraft, domain.RequirementClarified, 43),
		domain.KindStateConflict, "requirement_state_conflict")

	// The version row is untouched by every requirement operation.
	storedVersion, err := versions.Get(ctx, version.ID)
	if err != nil {
		t.Fatalf("Get version: %v", err)
	}
	if storedVersion.Status != domain.VersionDraft || storedVersion.Revision != 1 {
		t.Fatalf("requirement operations changed the version: %+v", storedVersion)
	}
}

// mustUpdateStatus runs a requirement status CAS and returns its error.
func mustUpdateStatus(repo *postgres.RequirementRepository, ctx context.Context, id int64, from, to string, actor int64) error {
	_, err := repo.UpdateStatus(ctx, id, from, to, actor)
	return err
}

// TestRequirementRepositoryDeleteCascades covers the draft-only delete guard and
// the ON DELETE CASCADE of relations, questions, decisions and attachments.
func TestRequirementRepositoryDeleteCascades(t *testing.T) {
	ctx := context.Background()
	pool, repo, versions := openRequirementTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Order v2", CreatedBy: 7, UpdatedBy: 7})
	insertRequirement(t, repo, domain.Requirement{
		ID: 2101, VersionID: version.ID, Title: "Delete me", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	keep := insertRequirement(t, repo, domain.Requirement{
		ID: 2102, VersionID: version.ID, Title: "Keep me", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_relation
			(id, version_id, from_requirement_id, to_requirement_id, relation_type, created_by)
		VALUES (4001, $1, 2101, 2102, 'depends_on', 7),
		       (4002, $1, 2102, 2101, 'overlaps', 7)`, version.ID); err != nil {
		t.Fatalf("insert relations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO clarification_question
			(id, version_id, requirement_id, question, status, created_by)
		VALUES (4101, $1, 2101, 'Requirement question?', 'open', 7),
		       (4102, $1, NULL, 'Version question?', 'open', 7)`, version.ID); err != nil {
		t.Fatalf("insert questions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO decision
			(id, version_id, requirement_id, topic, decision, status, source, proposed_by)
		VALUES (4201, $1, 2101, 'Topic', 'Decision', 'proposed', 'ai', 7)`, version.ID); err != nil {
		t.Fatalf("insert decision: %v", err)
	}
	attachment := &domain.Attachment{
		ID: 5001, VersionID: version.ID, RequirementID: 2101,
		Kind: domain.AttachmentText, Filename: "notes.txt", MIMEType: "text/plain",
		SizeBytes: 12, SHA256: requirementTestSHA, StoragePath: "1001/5001_notes.txt", CreatedBy: 7,
	}
	if err := repo.CreateAttachment(ctx, attachment); err != nil {
		t.Fatalf("create attachment: %v", err)
	}

	if err := repo.Delete(ctx, 2101); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, table := range []string{
		"requirement_relation WHERE from_requirement_id = 2101 OR to_requirement_id = 2101",
		"clarification_question WHERE requirement_id = 2101",
		"decision WHERE requirement_id = 2101",
		"requirement_attachment WHERE requirement_id = 2101",
	} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s = %d, want 0 after the cascade", table, count)
		}
	}
	// The version-level question and the other requirement survive.
	var survivors int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM clarification_question WHERE id = 4102`).Scan(&survivors); err != nil {
		t.Fatalf("count surviving question: %v", err)
	}
	if survivors != 1 {
		t.Fatal("delete removed the version-level question")
	}
	if _, err := repo.Get(ctx, keep.ID); err != nil {
		t.Fatalf("Get surviving requirement: %v", err)
	}

	// A clarified requirement cannot be deleted through the repository.
	if _, err := repo.UpdateStatus(ctx, keep.ID, domain.RequirementDraft, domain.RequirementClarified, 42); err != nil {
		t.Fatalf("confirm keep: %v", err)
	}
	assertRequirementKind(t, repo.Delete(ctx, keep.ID), domain.KindStateConflict, "requirement_state_conflict")
	if _, err := repo.Get(ctx, keep.ID); err != nil {
		t.Fatalf("blocked delete removed the requirement: %v", err)
	}

	// A missing row is not-found.
	assertRequirementKind(t, repo.Delete(ctx, 999999), domain.KindNotFound, "not_found")
}

// TestRequirementRelationRepository covers relation insert/list/update/delete and
// the SQL uniqueness backstops (normalized undirected and directed depends_on).
func TestRequirementRelationRepository(t *testing.T) {
	ctx := context.Background()
	_, repo, versions := openRequirementTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Order v2", CreatedBy: 7, UpdatedBy: 7})
	first := insertRequirement(t, repo, domain.Requirement{
		ID: 2201, VersionID: version.ID, Title: "First", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	second := insertRequirement(t, repo, domain.Requirement{
		ID: 2202, VersionID: version.ID, Title: "Second", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	otherVersion := insertVersion(t, versions, domain.Version{ID: 1002, Name: "Other", CreatedBy: 7, UpdatedBy: 7})
	foreign := insertRequirement(t, repo, domain.Requirement{
		ID: 2203, VersionID: otherVersion.ID, Title: "Foreign", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})

	overlaps := &domain.Relation{
		ID: 6001, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: second.ID,
		RelationType: domain.RelationOverlaps, Description: "same scope", Source: domain.SourceAI, CreatedBy: 42,
	}
	if err := repo.CreateRelation(ctx, overlaps); err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	if overlaps.CreatedAt.IsZero() || overlaps.UpdatedAt.IsZero() {
		t.Fatalf("relation timestamps not populated: %+v", overlaps)
	}

	// The normalized undirected unique index rejects the reversed pair.
	reversed := &domain.Relation{
		ID: 6002, VersionID: version.ID, FromRequirementID: second.ID, ToRequirementID: first.ID,
		RelationType: domain.RelationOverlaps, Source: domain.SourceUser, CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateRelation(ctx, reversed), domain.KindConflict, "duplicate_relation")

	// depends_on is directed: both directions coexist, the same direction cannot.
	directed := &domain.Relation{
		ID: 6003, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: second.ID,
		RelationType: domain.RelationDependsOn, Source: domain.SourceUser, CreatedBy: 42,
	}
	if err := repo.CreateRelation(ctx, directed); err != nil {
		t.Fatalf("CreateRelation directed: %v", err)
	}
	reverseDirected := &domain.Relation{
		ID: 6004, VersionID: version.ID, FromRequirementID: second.ID, ToRequirementID: first.ID,
		RelationType: domain.RelationDependsOn, Source: domain.SourceUser, CreatedBy: 42,
	}
	if err := repo.CreateRelation(ctx, reverseDirected); err != nil {
		t.Fatalf("CreateRelation reverse directed: %v", err)
	}
	sameDirected := &domain.Relation{
		ID: 6005, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: second.ID,
		RelationType: domain.RelationDependsOn, Source: domain.SourceUser, CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateRelation(ctx, sameDirected), domain.KindConflict, "duplicate_relation")

	// A self-loop violates the CHECK constraint.
	selfLoop := &domain.Relation{
		ID: 6006, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: first.ID,
		RelationType: domain.RelationConflictsWith, Source: domain.SourceUser, CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateRelation(ctx, selfLoop), domain.KindInvalid, "invalid_relation")

	// Cross-version endpoints are rejected by the guarded insert.
	crossVersion := &domain.Relation{
		ID: 6007, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: foreign.ID,
		RelationType: domain.RelationConflictsWith, Source: domain.SourceUser, CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateRelation(ctx, crossVersion), domain.KindInvalid, "relation_requirement_mismatch")

	// Missing endpoints are reported the same way.
	missing := &domain.Relation{
		ID: 6008, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: 999999,
		RelationType: domain.RelationConflictsWith, Source: domain.SourceUser, CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateRelation(ctx, missing), domain.KindInvalid, "relation_requirement_mismatch")

	// An unknown relation type violates the CHECK constraint.
	unknown := &domain.Relation{
		ID: 6009, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: second.ID,
		RelationType: "blocks", Source: domain.SourceUser, CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateRelation(ctx, unknown), domain.KindInvalid, "invalid_relation_type")

	// List returns the version's relations ordered by id.
	relations, err := repo.ListRelations(ctx, version.ID)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	relationIDs := make([]int64, len(relations))
	for i, relation := range relations {
		relationIDs[i] = relation.ID
	}
	if len(relationIDs) != 3 || relationIDs[0] != 6001 || relationIDs[1] != 6003 || relationIDs[2] != 6004 {
		t.Fatalf("relation ids = %v, want [6001 6003 6004]", relationIDs)
	}
	empty, err := repo.ListRelations(ctx, 999999)
	if err != nil {
		t.Fatalf("ListRelations empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty relations = %+v", empty)
	}

	// Update rewrites type/description; a duplicate type is rejected by the
	// unique index.
	shares := &domain.Relation{
		ID: 6010, VersionID: version.ID, FromRequirementID: first.ID, ToRequirementID: second.ID,
		RelationType: domain.RelationSharesScope, Description: "shares", Source: domain.SourceUser, CreatedBy: 42,
	}
	if err := repo.CreateRelation(ctx, shares); err != nil {
		t.Fatalf("CreateRelation shares_scope: %v", err)
	}
	// 6001 is still overlaps here, so turning the directed 6003 into overlaps
	// collides with it.
	assertRequirementKind(t,
		mustUpdateRelation(repo, ctx, 6003, domain.RelationOverlaps, ""),
		domain.KindConflict, "duplicate_relation")
	updated, err := repo.UpdateRelation(ctx, 6001, domain.RelationConflictsWith, "conflicts")
	if err != nil {
		t.Fatalf("UpdateRelation: %v", err)
	}
	if updated.RelationType != domain.RelationConflictsWith || updated.Description != "conflicts" {
		t.Fatalf("updated relation = %+v", updated)
	}
	assertRequirementKind(t,
		mustUpdateRelation(repo, ctx, 6003, "blocks", ""),
		domain.KindInvalid, "invalid_relation_type")
	assertRequirementKind(t, mustUpdateRelation(repo, ctx, 999999, domain.RelationOverlaps, ""),
		domain.KindNotFound, "not_found")

	// Delete removes one relation; a missing row is not-found.
	if err := repo.DeleteRelation(ctx, 6010); err != nil {
		t.Fatalf("DeleteRelation: %v", err)
	}
	assertRequirementKind(t, repo.DeleteRelation(ctx, 6010), domain.KindNotFound, "not_found")
}

// mustUpdateRelation runs a relation update and returns its error.
func mustUpdateRelation(repo *postgres.RequirementRepository, ctx context.Context, id int64, relType, desc string) error {
	_, err := repo.UpdateRelation(ctx, id, relType, desc)
	return err
}

// TestRequirementRelationListCapsAt500 covers spec §7.4: the relation list is
// unpaginated but bounded to 500 rows per version. Rows are seeded through SQL
// so the test stays fast with more than 500 relations.
func TestRequirementRelationListCapsAt500(t *testing.T) {
	ctx := context.Background()
	pool, repo, versions := openRequirementTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Capped v1", CreatedBy: 7, UpdatedBy: 7})

	// Two disjoint requirement ranges give 50x11 distinct non-self pairs; 521
	// relations are seeded so only the lowest 500 ids may be returned.
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, title, created_by, updated_by)
		SELECT n, $1::bigint, 'requirement ' || n, 7, 7 FROM generate_series(3001, 3050) AS n
		UNION ALL
		SELECT n, $1::bigint, 'requirement ' || n, 7, 7 FROM generate_series(4001, 4011) AS n`, version.ID); err != nil {
		t.Fatalf("seed requirements: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_relation
			(id, version_id, from_requirement_id, to_requirement_id, relation_type, created_by)
		SELECT 800000 + n, $1, 3001 + (n / 11), 4001 + (n % 11), 'overlaps', 7
		FROM generate_series(0, 520) AS n`, version.ID); err != nil {
		t.Fatalf("seed relations: %v", err)
	}

	relations, err := repo.ListRelations(ctx, version.ID)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(relations) != 500 {
		t.Fatalf("relations = %d, want the 500 cap", len(relations))
	}
	if relations[0].ID != 800000 || relations[499].ID != 800499 {
		t.Fatalf("first/last relation = %d/%d, want 800000/800499", relations[0].ID, relations[499].ID)
	}
}

// TestRequirementAttachmentRepository covers the attachment metadata methods that
// the attachment service (T11) consumes.
func TestRequirementAttachmentRepository(t *testing.T) {
	ctx := context.Background()
	_, repo, versions := openRequirementTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Order v2", CreatedBy: 7, UpdatedBy: 7})
	first := insertRequirement(t, repo, domain.Requirement{
		ID: 2301, VersionID: version.ID, Title: "First", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	second := insertRequirement(t, repo, domain.Requirement{
		ID: 2302, VersionID: version.ID, Title: "Second", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})

	attachment := &domain.Attachment{
		ID: 7001, VersionID: version.ID, RequirementID: first.ID,
		Kind: domain.AttachmentText, Filename: "notes.txt", MIMEType: "text/plain",
		SizeBytes: 12, SHA256: requirementTestSHA, StoragePath: "1001/7001_notes.txt", CreatedBy: 42,
	}
	if err := repo.CreateAttachment(ctx, attachment); err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}
	if attachment.CreatedAt.IsZero() {
		t.Fatalf("attachment timestamp not populated: %+v", attachment)
	}
	loaded, err := repo.GetAttachment(ctx, attachment.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if loaded.Filename != "notes.txt" || loaded.SHA256 != requirementTestSHA ||
		loaded.StoragePath != "1001/7001_notes.txt" || loaded.CreatedBy != 42 {
		t.Fatalf("loaded attachment = %+v", loaded)
	}

	// Same requirement + same sha256 is the duplicate_attachment 409.
	duplicate := &domain.Attachment{
		ID: 7002, VersionID: version.ID, RequirementID: first.ID,
		Kind: domain.AttachmentText, Filename: "notes-copy.txt", MIMEType: "text/plain",
		SizeBytes: 12, SHA256: requirementTestSHA, StoragePath: "1001/7002_notes-copy.txt", CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateAttachment(ctx, duplicate), domain.KindConflict, "duplicate_attachment")

	// Another requirement may attach the same content.
	secondAttachment := &domain.Attachment{
		ID: 7003, VersionID: version.ID, RequirementID: second.ID,
		Kind: domain.AttachmentImage, Filename: "shot.png", MIMEType: "image/png",
		SizeBytes: 12, SHA256: requirementTestSHA, StoragePath: "1001/7003_shot.png", CreatedBy: 42,
	}
	if err := repo.CreateAttachment(ctx, secondAttachment); err != nil {
		t.Fatalf("CreateAttachment second requirement: %v", err)
	}

	requirementCount, versionCount, err := repo.CountAttachments(ctx, first.ID, version.ID)
	if err != nil {
		t.Fatalf("CountAttachments: %v", err)
	}
	if requirementCount != 1 || versionCount != 2 {
		t.Fatalf("attachment counts = %d/%d, want 1/2", requirementCount, versionCount)
	}

	listed, err := repo.ListAttachments(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != 7001 {
		t.Fatalf("listed attachments = %+v", listed)
	}

	// An attachment of a missing requirement is a missing-version style FK error.
	orphan := &domain.Attachment{
		ID: 7004, VersionID: version.ID, RequirementID: 999999,
		Kind: domain.AttachmentText, Filename: "orphan.txt", MIMEType: "text/plain",
		SizeBytes: 12, SHA256: strings.Repeat("b", 64), StoragePath: "1001/7004_orphan.txt", CreatedBy: 42,
	}
	assertRequirementKind(t, repo.CreateAttachment(ctx, orphan), domain.KindNotFound, "not_found")

	if err := repo.DeleteAttachment(ctx, 7001); err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	assertRequirementKind(t, repo.DeleteAttachment(ctx, 7001), domain.KindNotFound, "not_found")
	_, err = repo.GetAttachment(ctx, 7001)
	assertRequirementKind(t, err, domain.KindNotFound, "not_found")
}
