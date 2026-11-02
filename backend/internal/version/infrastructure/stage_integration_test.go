//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

// TestStageRepositoryGetApply pins the PostgreSQL stage repository against the
// real schema: the creation default, the compare-and-swap source-stage guard,
// suspension bookkeeping and the clearing performed by a resume.
func TestStageRepositoryGetApply(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := repo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}

	// A freshly created version starts at the schema default `created`.
	state, err := repo.GetStage(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetStage new version: %v", err)
	}
	if state.VersionID != v.ID || state.Stage != version.StageCreated {
		t.Fatalf("new stage = %#v", state)
	}
	if state.StageEnteredAt.IsZero() {
		t.Fatal("new stage has a zero stage_entered_at")
	}
	if state.Blocker != "" || state.SuspendedFrom != nil {
		t.Fatalf("new stage has suspension fields: %#v", state)
	}

	// A matching expected stage writes the transition.
	clarifyingAt := testTime.Add(time.Hour)
	if err := repo.ApplyStage(ctx, version.StageCreated, version.StageState{
		VersionID: v.ID, Stage: version.StageClarifying, StageEnteredAt: clarifyingAt,
	}); err != nil {
		t.Fatalf("ApplyStage created -> clarifying: %v", err)
	}
	state, err = repo.GetStage(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetStage after apply: %v", err)
	}
	if state.Stage != version.StageClarifying || !state.StageEnteredAt.Equal(clarifyingAt) {
		t.Fatalf("clarifying stage = %#v", state)
	}

	// A stale expected stage is a conflict and must not overwrite the row.
	staleAt := clarifyingAt.Add(time.Hour)
	err = repo.ApplyStage(ctx, version.StageCreated, version.StageState{
		VersionID: v.ID, Stage: version.StageRequirementReview, StageEnteredAt: staleAt,
	})
	if !errors.Is(err, version.ErrStageConflict) {
		t.Fatalf("stale ApplyStage err = %v, want ErrStageConflict", err)
	}
	state, err = repo.GetStage(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetStage after stale apply: %v", err)
	}
	if state.Stage != version.StageClarifying || !state.StageEnteredAt.Equal(clarifyingAt) {
		t.Fatalf("stale apply rewrote the stage: %#v", state)
	}

	// Suspension records the source stage and the blocker.
	suspendedFrom := version.StageClarifying
	blockedAt := clarifyingAt.Add(2 * time.Hour)
	if err := repo.ApplyStage(ctx, version.StageClarifying, version.StageState{
		VersionID: v.ID, Stage: version.StageBlocked, StageEnteredAt: blockedAt,
		Blocker: "executor is offline", SuspendedFrom: &suspendedFrom,
	}); err != nil {
		t.Fatalf("ApplyStage clarifying -> blocked: %v", err)
	}
	state, err = repo.GetStage(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetStage blocked: %v", err)
	}
	if state.Stage != version.StageBlocked || state.Blocker != "executor is offline" {
		t.Fatalf("blocked stage = %#v", state)
	}
	if state.SuspendedFrom == nil || *state.SuspendedFrom != version.StageClarifying {
		t.Fatalf("suspended_from = %#v", state.SuspendedFrom)
	}

	// Resume restores the recorded stage and clears suspended_from/blocker.
	resumedAt := blockedAt.Add(time.Hour)
	if err := repo.ApplyStage(ctx, version.StageBlocked, version.StageState{
		VersionID: v.ID, Stage: version.StageClarifying, StageEnteredAt: resumedAt,
	}); err != nil {
		t.Fatalf("ApplyStage blocked -> clarifying: %v", err)
	}
	state, err = repo.GetStage(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetStage resumed: %v", err)
	}
	if state.Stage != version.StageClarifying || !state.StageEnteredAt.Equal(resumedAt) {
		t.Fatalf("resumed stage = %#v", state)
	}
	if state.SuspendedFrom != nil || state.Blocker != "" {
		t.Fatalf("resume did not clear suspension fields: %#v", state)
	}

	// A missing version is distinguishable from a stale stage.
	missingID := ids.Next()
	err = repo.ApplyStage(ctx, version.StageCreated, version.StageState{
		VersionID: missingID, Stage: version.StageClarifying, StageEnteredAt: resumedAt,
	})
	if !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing ApplyStage err = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetStage(ctx, missingID); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing GetStage err = %v, want ErrNotFound", err)
	}
}
