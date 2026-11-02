//go:build integration

package versionpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

func sampleRequirement(ids interface{ Next() int64 }, versionID int64) (version.Requirement, version.RequirementVersion) {
	requirementID := ids.Next()
	revisionID := ids.Next()
	createdBy := int64(42)
	requirement := version.Requirement{
		ID:                  requirementID,
		VersionID:           versionID,
		CurrentVersionID:    &revisionID,
		Status:              version.RequirementDraft,
		TargetVersionSUTIDs: []string{"5001"},
		CreatedBy:           &createdBy,
		CreatedAt:           testTime,
		UpdatedAt:           testTime,
	}
	revision := version.RequirementVersion{
		ID:                 revisionID,
		RequirementID:      requirementID,
		Revision:           1,
		Title:              "Checkout coupons",
		Description:        "Users can apply coupons.",
		AcceptanceCriteria: "A valid coupon reduces the total.",
		Priority:           "P1",
		Constraints:        json.RawMessage(`{"business_rules":["one coupon per order"]}`),
		TargetVersionSUTs:  []string{"5001"},
		TargetPages:        []string{"6001", "6002"},
		TargetAPIs:         []string{"7001"},
		ContentHash:        strings.Repeat("a", 64),
		CreatedBy:          &createdBy,
		CreatedAt:          testTime,
	}
	return requirement, revision
}

func TestRequirementRepositoryLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewRequirementRepository(pool)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}

	requirement, revision := sampleRequirement(ids, v.ID)
	if err := repo.Create(ctx, requirement, revision); err != nil {
		t.Fatalf("Create: %v", err)
	}

	aggregate, err := repo.Get(ctx, v.ID, requirement.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if aggregate.Requirement.ID != requirement.ID || aggregate.Requirement.Status != version.RequirementDraft {
		t.Fatalf("requirement = %#v", aggregate.Requirement)
	}
	if len(aggregate.Versions) != 1 || aggregate.Versions[0].Title != "Checkout coupons" {
		t.Fatalf("versions = %#v", aggregate.Versions)
	}
	if got := aggregate.Versions[0].TargetPages; len(got) != 2 || got[0] != "6001" || got[1] != "6002" {
		t.Fatalf("target pages = %#v", got)
	}
	if string(aggregate.Versions[0].Constraints) != `{"business_rules": ["one coupon per order"]}` {
		t.Fatalf("constraints = %s", aggregate.Versions[0].Constraints)
	}
	if len(aggregate.Confirmations) != 0 || len(aggregate.Relations) != 0 || len(aggregate.Attachments) != 0 {
		t.Fatalf("aggregate = %#v", aggregate)
	}

	list, err := repo.List(ctx, v.ID, "", 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Requirement.ID != requirement.ID || list[0].Current.Revision != 1 || list[0].ConfirmedAt != nil {
		t.Fatalf("list = %#v", list)
	}
	clarified, err := repo.List(ctx, v.ID, version.RequirementClarified, 10, nil)
	if err != nil || len(clarified) != 0 {
		t.Fatalf("clarified list = %#v, %v", clarified, err)
	}

	// A second revision becomes current and the old revision stays queryable.
	secondRevision := revision
	secondRevision.ID = ids.Next()
	secondRevision.Revision = 2
	secondRevision.Title = "Checkout coupons v2"
	secondRevision.ContentHash = strings.Repeat("b", 64)
	secondRevision.CreatedAt = testTime.Add(time.Hour)
	requirement.CurrentVersionID = &secondRevision.ID
	requirement.UpdatedAt = testTime.Add(time.Hour)
	if err := repo.AddRevision(ctx, requirement, secondRevision); err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	aggregate, err = repo.Get(ctx, v.ID, requirement.ID)
	if err != nil {
		t.Fatalf("Get after revision: %v", err)
	}
	if len(aggregate.Versions) != 2 || aggregate.Versions[0].ID != secondRevision.ID || aggregate.Versions[1].ID != revision.ID {
		t.Fatalf("versions = %#v", aggregate.Versions)
	}
	*aggregate.Requirement.CurrentVersionID = secondRevision.ID
	if aggregate.Requirement.Status != version.RequirementDraft {
		t.Fatalf("status = %q", aggregate.Requirement.Status)
	}

	// Confirm freezes the current revision.
	confirmedAt := testTime.Add(2 * time.Hour)
	confirmation := version.Confirmation{
		ID:                     ids.Next(),
		RequirementVersionID:   secondRevision.ID,
		Scope:                  version.ConfirmationScopeRequirement,
		ActorID:                42,
		ConfirmedAt:            confirmedAt,
		AffectedRequirementIDs: []string{},
	}
	confirmedRequirement := aggregate.Requirement
	confirmedRequirement.Status = version.RequirementClarified
	confirmedRequirement.UpdatedAt = confirmedAt
	if err := repo.Confirm(ctx, confirmedRequirement, confirmation); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	aggregate, err = repo.Get(ctx, v.ID, requirement.ID)
	if err != nil {
		t.Fatalf("Get after confirm: %v", err)
	}
	if aggregate.Requirement.Status != version.RequirementClarified || len(aggregate.Confirmations) != 1 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
	if aggregate.Confirmations[0].Scope != "requirement" || aggregate.Confirmations[0].ActorID != 42 {
		t.Fatalf("confirmation = %#v", aggregate.Confirmations[0])
	}
	if err := repo.Confirm(ctx, confirmedRequirement, confirmation); !errors.Is(err, version.ErrStateConflict) {
		t.Fatalf("duplicate confirm error = %v", err)
	}
	list, err = repo.List(ctx, v.ID, "", 10, nil)
	if err != nil || len(list) != 1 || list[0].ConfirmedAt == nil || !list[0].ConfirmedAt.Equal(confirmedAt) {
		t.Fatalf("list = %#v, %v", list, err)
	}

	if _, err := repo.Get(ctx, v.ID+1, requirement.ID); !errors.Is(err, version.ErrRequirementNotFound) {
		t.Fatalf("cross-version Get error = %v", err)
	}
}

func TestRequirementRepositoryBatchConfirmIsAllOrNothing(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewRequirementRepository(pool)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	first, firstRevision := sampleRequirement(ids, v.ID)
	second, secondRevision := sampleRequirement(ids, v.ID)
	for _, item := range []struct {
		requirement version.Requirement
		revision    version.RequirementVersion
	}{{first, firstRevision}, {second, secondRevision}} {
		if err := repo.Create(ctx, item.requirement, item.revision); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	confirmTime := testTime.Add(time.Hour)
	firstConfirmed := first
	firstConfirmed.Status = version.RequirementClarified
	firstConfirmed.UpdatedAt = confirmTime
	batchID := "batch-1"
	firstConfirmation := version.Confirmation{
		ID: ids.Next(), RequirementVersionID: firstRevision.ID, Scope: version.ConfirmationScopeBatch,
		BatchID: &batchID, ActorID: 42, ConfirmedAt: confirmTime, AffectedRequirementIDs: []string{"1", "2"},
	}
	secondConfirmed := second
	secondConfirmed.Status = version.RequirementClarified
	secondConfirmed.UpdatedAt = confirmTime
	secondConfirmation := version.Confirmation{
		ID: ids.Next(), RequirementVersionID: secondRevision.ID, Scope: version.ConfirmationScopeBatch,
		BatchID: &batchID, ActorID: 42, ConfirmedAt: confirmTime, AffectedRequirementIDs: []string{"1", "2"},
	}

	// Confirm the second requirement first so the batch is stale for it.
	secondConfirmedEarly := second
	secondConfirmedEarly.Status = version.RequirementClarified
	secondConfirmedEarly.UpdatedAt = confirmTime
	earlyBatchID := "batch-early"
	if err := repo.Confirm(ctx, secondConfirmedEarly, version.Confirmation{
		ID: ids.Next(), RequirementVersionID: secondRevision.ID, Scope: version.ConfirmationScopeBatch,
		BatchID: &earlyBatchID, ActorID: 42, ConfirmedAt: confirmTime, AffectedRequirementIDs: []string{},
	}); err != nil {
		t.Fatalf("early Confirm: %v", err)
	}

	// A stale requirement status makes the whole batch roll back: the first
	// requirement must stay draft and get no confirmation.
	if err := repo.BatchConfirm(ctx, []version.Requirement{firstConfirmed, secondConfirmed},
		[]version.Confirmation{firstConfirmation, secondConfirmation}); !errors.Is(err, version.ErrStateConflict) {
		t.Fatalf("stale batch error = %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM requirement_confirmation WHERE requirement_version_id = $1`,
		firstRevision.ID).Scan(&count); err != nil {
		t.Fatalf("count confirmations: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back batch confirmed the first requirement")
	}
	if stored, err := repo.Get(ctx, v.ID, first.ID); err != nil || stored.Requirement.Status != version.RequirementDraft {
		t.Fatalf("first status = %#v, %v", stored.Requirement, err)
	}

	// The valid batch confirms the remaining draft.
	if err := repo.BatchConfirm(ctx, []version.Requirement{firstConfirmed},
		[]version.Confirmation{firstConfirmation}); err != nil {
		t.Fatalf("BatchConfirm: %v", err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		aggregate, err := repo.Get(ctx, v.ID, id)
		if err != nil {
			t.Fatalf("Get %d: %v", id, err)
		}
		if aggregate.Requirement.Status != version.RequirementClarified || len(aggregate.Confirmations) != 1 {
			t.Fatalf("aggregate %d = %#v", id, aggregate)
		}
		if aggregate.Confirmations[0].BatchID == nil {
			t.Fatalf("confirmation = %#v", aggregate.Confirmations[0])
		}
	}
}

func TestRequirementRepositoryRelations(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewRequirementRepository(pool)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	from, fromRevision := sampleRequirement(ids, v.ID)
	to, toRevision := sampleRequirement(ids, v.ID)
	if err := repo.Create(ctx, from, fromRevision); err != nil {
		t.Fatalf("Create from: %v", err)
	}
	if err := repo.Create(ctx, to, toRevision); err != nil {
		t.Fatalf("Create to: %v", err)
	}

	createdBy := int64(42)
	relation := version.Relation{
		ID: ids.Next(), VersionID: v.ID, FromRequirementID: from.ID, ToRequirementID: to.ID,
		Kind: "depends_on", Description: "needs coupons", CreatedBy: &createdBy, CreatedAt: testTime,
	}
	if err := repo.CreateRelation(ctx, relation); err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	if err := repo.CreateRelation(ctx, relation); !errors.Is(err, version.ErrRelationDuplicate) {
		t.Fatalf("duplicate relation error = %v", err)
	}
	aggregate, err := repo.Get(ctx, v.ID, from.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(aggregate.Relations) != 1 || aggregate.Relations[0].Kind != "depends_on" {
		t.Fatalf("relations = %#v", aggregate.Relations)
	}
	relations, err := repo.ListRelations(ctx, v.ID, from.ID)
	if err != nil || len(relations) != 1 || relations[0].ID != relation.ID {
		t.Fatalf("list relations = %#v, %v", relations, err)
	}
	other, err := repo.ListRelations(ctx, v.ID, 999999)
	if err != nil || len(other) != 0 {
		t.Fatalf("missing relations = %#v, %v", other, err)
	}
}

func TestRepositoryMarkRunningAndStats(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	if err := repo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	stats, err := repo.Stats(ctx, v.ID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Requirements.Total != 0 || stats.ConversationCount != 0 {
		t.Fatalf("empty stats = %#v", stats)
	}

	first, firstRevision := sampleRequirement(ids, v.ID)
	if err := requirementRepo.Create(ctx, first, firstRevision); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, secondRevision := sampleRequirement(ids, v.ID)
	if err := requirementRepo.Create(ctx, second, secondRevision); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	confirmed := second
	confirmed.Status = version.RequirementClarified
	if err := requirementRepo.Confirm(ctx, confirmed, version.Confirmation{
		ID: ids.Next(), RequirementVersionID: secondRevision.ID, Scope: "requirement",
		ActorID: 42, ConfirmedAt: testTime, AffectedRequirementIDs: []string{},
	}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversation (id, version_id, kind, title) VALUES ($1, $2, 'clarification', 'chat')
	`, ids.Next(), v.ID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}

	stats, err = repo.Stats(ctx, v.ID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Requirements.Total != 2 || stats.Requirements.Draft != 1 || stats.Requirements.Clarified != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if stats.ConversationCount != 1 {
		t.Fatalf("conversation count = %d", stats.ConversationCount)
	}

	changed, err := repo.MarkRunning(ctx, v.ID, testTime.Add(time.Minute))
	if err != nil || !changed {
		t.Fatalf("MarkRunning = %v/%v", changed, err)
	}
	changed, err = repo.MarkRunning(ctx, v.ID, testTime.Add(2*time.Minute))
	if err != nil || changed {
		t.Fatalf("second MarkRunning = %v/%v", changed, err)
	}
	stored, _, err := repo.Get(ctx, v.ID)
	if err != nil || stored.Status != version.StatusRunning {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
}
