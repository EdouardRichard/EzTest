package application_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// createRelation is a test helper that creates a relation and fails the test on
// an unexpected error.
func createRelation(t *testing.T, f *requirementFixture, versionID int64, from, to int64, relType string) *domain.Relation {
	t.Helper()
	relation, err := f.relations.Create(context.Background(), versionID, application.CreateRelationCommand{
		FromRequirementID: from, ToRequirementID: to, RelationType: relType,
	}, 42)
	if err != nil {
		t.Fatalf("Create relation %s %d->%d: %v", relType, from, to, err)
	}
	return relation
}

// TestRelationNormalizationAndDuplicate covers spec §6.4/INV-7: undirected
// normalization, depends_on direction, no self-loop and cross-version rejection.
func TestRelationNormalizationAndDuplicate(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionDraft)
	first := f.seedRequirement(version.ID, domain.RequirementDraft)
	second := f.seedRequirement(version.ID, domain.RequirementDraft)

	created := createRelation(t, f, version.ID, first.ID, second.ID, domain.RelationOverlaps)
	if created.VersionID != version.ID || created.FromRequirementID != first.ID ||
		created.ToRequirementID != second.ID || created.Source != domain.SourceUser ||
		created.CreatedBy != 42 {
		t.Fatalf("created relation = %+v", created)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %d, want 1", len(f.events.events))
	}
	event := f.events.events[0]
	if event.Type != "requirement.relation_added" || event.VersionID != version.ID ||
		event.RefRequirementID == nil || *event.RefRequirementID != first.ID ||
		event.ActorID != 42 {
		t.Fatalf("event = %+v", event)
	}
	if event.Detail["relationType"] != domain.RelationOverlaps ||
		event.Detail["fromRequirementId"] != strconv.FormatInt(first.ID, 10) ||
		event.Detail["toRequirementId"] != strconv.FormatInt(second.ID, 10) {
		t.Fatalf("event detail = %+v", event.Detail)
	}
	if event.Detail["relationId"] != strconv.FormatInt(created.ID, 10) {
		t.Fatalf("event detail relationId = %#v, want decimal string", event.Detail["relationId"])
	}

	t.Run("undirected duplicate in reverse order is a 409", func(t *testing.T) {
		_, err := f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
			FromRequirementID: second.ID, ToRequirementID: first.ID, RelationType: domain.RelationOverlaps,
		}, 42)
		assertDomainReason(t, err, domain.KindConflict, "duplicate_relation")
	})

	t.Run("a different undirected type on the same pair is allowed", func(t *testing.T) {
		createRelation(t, f, version.ID, first.ID, second.ID, domain.RelationSharesScope)
	})

	t.Run("depends_on is directed", func(t *testing.T) {
		createRelation(t, f, version.ID, first.ID, second.ID, domain.RelationDependsOn)
		createRelation(t, f, version.ID, second.ID, first.ID, domain.RelationDependsOn)

		_, err := f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
			FromRequirementID: first.ID, ToRequirementID: second.ID, RelationType: domain.RelationDependsOn,
		}, 42)
		assertDomainReason(t, err, domain.KindConflict, "duplicate_relation")
	})

	t.Run("self loop is a 422", func(t *testing.T) {
		_, err := f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
			FromRequirementID: first.ID, ToRequirementID: first.ID, RelationType: domain.RelationConflictsWith,
		}, 42)
		assertDomainReason(t, err, domain.KindInvalid, "invalid_relation")
	})

	t.Run("cross-version endpoint is a 422", func(t *testing.T) {
		otherVersion := f.versions.seed(domain.Version{ID: 200, Name: "Other", CreatedBy: 1, UpdatedBy: 1})
		foreign := f.seedRequirement(otherVersion.ID, domain.RequirementDraft)

		_, err := f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
			FromRequirementID: first.ID, ToRequirementID: foreign.ID, RelationType: domain.RelationConflictsWith,
		}, 42)
		assertDomainReason(t, err, domain.KindInvalid, "relation_requirement_mismatch")

		_, err = f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
			FromRequirementID: foreign.ID, ToRequirementID: first.ID, RelationType: domain.RelationConflictsWith,
		}, 42)
		assertDomainReason(t, err, domain.KindInvalid, "relation_requirement_mismatch")
	})

	t.Run("missing requirement is 404", func(t *testing.T) {
		_, err := f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
			FromRequirementID: first.ID, ToRequirementID: 999, RelationType: domain.RelationConflictsWith,
		}, 42)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})

	t.Run("unknown relation type is a 422", func(t *testing.T) {
		_, err := f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
			FromRequirementID: first.ID, ToRequirementID: second.ID, RelationType: "blocks",
		}, 42)
		assertDomainReason(t, err, domain.KindInvalid, "invalid_relation_type")
	})
}

// TestRelationAllowedOnClarifiedRequirements covers spec §6.4: relation changes
// are not limited by the requirement status.
func TestRelationAllowedOnClarifiedRequirements(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionDesigning)
	first := f.seedRequirement(version.ID, domain.RequirementClarified)
	second := f.seedRequirement(version.ID, domain.RequirementClarified)

	created, err := f.relations.Create(context.Background(), version.ID, application.CreateRelationCommand{
		FromRequirementID: first.ID, ToRequirementID: second.ID,
		RelationType: domain.RelationDependsOn, Source: domain.SourceAI,
	}, 42)
	if err != nil {
		t.Fatalf("Create relation on clarified requirements: %v", err)
	}
	if created.Source != domain.SourceAI {
		t.Fatalf("source = %s, want ai", created.Source)
	}
	if len(f.events.events) != 1 || f.events.events[0].Source != domain.SourceAI {
		t.Fatalf("events = %+v", f.events.events)
	}
}

// TestRelationUpdateAndDelete covers relation edits, duplicate detection and
// version scoping of the relation endpoints.
func TestRelationUpdateAndDelete(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionDraft)
	first := f.seedRequirement(version.ID, domain.RequirementDraft)
	second := f.seedRequirement(version.ID, domain.RequirementDraft)

	overlapping := createRelation(t, f, version.ID, first.ID, second.ID, domain.RelationOverlaps)
	shares := createRelation(t, f, version.ID, first.ID, second.ID, domain.RelationSharesScope)
	f.events.events = nil

	t.Run("changing into a duplicate type is a 409", func(t *testing.T) {
		_, err := f.relations.Update(context.Background(), version.ID, overlapping.ID, domain.RelationSharesScope, "")
		assertDomainReason(t, err, domain.KindConflict, "duplicate_relation")
		if got := f.repo.relations[overlapping.ID]; got.RelationType != domain.RelationOverlaps {
			t.Fatalf("failed update wrote the relation: %+v", got)
		}
	})

	t.Run("empty relation type keeps the current type and rewrites the description", func(t *testing.T) {
		updated, err := f.relations.Update(context.Background(), version.ID, overlapping.ID, "", "same checkout scope")
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if updated.RelationType != domain.RelationOverlaps || updated.Description != "same checkout scope" {
			t.Fatalf("updated = %+v", updated)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "requirement.relation_updated" {
			t.Fatalf("events = %+v", f.events.events)
		}
	})

	t.Run("relation type can change", func(t *testing.T) {
		updated, err := f.relations.Update(context.Background(), version.ID, overlapping.ID, domain.RelationConflictsWith, "conflict")
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if updated.RelationType != domain.RelationConflictsWith || updated.Description != "conflict" {
			t.Fatalf("updated = %+v", updated)
		}
	})

	t.Run("unknown relation type is a 422", func(t *testing.T) {
		_, err := f.relations.Update(context.Background(), version.ID, overlapping.ID, "blocks", "")
		assertDomainReason(t, err, domain.KindInvalid, "invalid_relation_type")
	})

	t.Run("missing relation is 404", func(t *testing.T) {
		_, err := f.relations.Update(context.Background(), version.ID, 999, domain.RelationOverlaps, "")
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})

	t.Run("delete removes the relation and publishes an event", func(t *testing.T) {
		f.events.events = nil
		if err := f.relations.Delete(context.Background(), version.ID, shares.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, ok := f.repo.relations[shares.ID]; ok {
			t.Fatal("relation was not deleted")
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "requirement.relation_removed" {
			t.Fatalf("events = %+v", f.events.events)
		}
		if err := f.relations.Delete(context.Background(), version.ID, shares.ID); err == nil {
			t.Fatal("deleting a missing relation succeeded")
		}
	})

	t.Run("relations of another version are 404", func(t *testing.T) {
		otherVersion := f.versions.seed(domain.Version{ID: 200, Name: "Other", CreatedBy: 1, UpdatedBy: 1})
		foreignFirst := f.seedRequirement(otherVersion.ID, domain.RequirementDraft)
		foreignSecond := f.seedRequirement(otherVersion.ID, domain.RequirementDraft)
		foreign := createRelation(t, f, otherVersion.ID, foreignFirst.ID, foreignSecond.ID, domain.RelationOverlaps)

		_, err := f.relations.Update(context.Background(), version.ID, foreign.ID, domain.RelationConflictsWith, "")
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
		assertDomainReason(t, f.relations.Delete(context.Background(), version.ID, foreign.ID),
			domain.KindNotFound, "not_found")
	})
}

// TestRelationList covers the read passthrough used by the HTTP layer.
func TestRelationList(t *testing.T) {
	f := newRequirementFixture()
	version := f.seedVersion(domain.VersionDraft)
	first := f.seedRequirement(version.ID, domain.RequirementDraft)
	second := f.seedRequirement(version.ID, domain.RequirementDraft)
	created := createRelation(t, f, version.ID, first.ID, second.ID, domain.RelationOverlaps)

	relations, err := f.relations.List(context.Background(), version.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(relations) != 1 || relations[0].ID != created.ID {
		t.Fatalf("relations = %+v", relations)
	}
	empty, err := f.relations.List(context.Background(), 999)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty relations = %+v", empty)
	}
}
