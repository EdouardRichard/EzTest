package main

// The clarification converger is the production adapter between the agent
// Runner (assistant turn) and the version stage machine. It only converges a
// clarifying version that already has a draft requirement, so an assistant
// confirmation phrase can never open an empty requirement gate.

import (
	"context"
	"testing"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type fakeConvergeStageRepo struct {
	stage   versiondomain.Stage
	applied []versiondomain.StageState
}

func (f *fakeConvergeStageRepo) GetStage(_ context.Context, versionID int64) (versiondomain.StageState, error) {
	return versiondomain.StageState{VersionID: versionID, Stage: f.stage, StageEnteredAt: time.Unix(0, 0)}, nil
}

func (f *fakeConvergeStageRepo) ApplyStage(_ context.Context, expected versiondomain.Stage, next versiondomain.StageState) error {
	if f.stage != expected {
		return versiondomain.ErrStageConflict
	}
	f.stage = next.Stage
	f.applied = append(f.applied, next)
	return nil
}

type fakeConvergeRequirements struct {
	drafts int
}

func (f *fakeConvergeRequirements) List(_ context.Context, _ int64, _ string, _ int, _ *int64) ([]versionapp.RequirementView, bool, error) {
	return make([]versionapp.RequirementView, f.drafts), false, nil
}

func (f *fakeConvergeRequirements) Get(_ context.Context, _, _ int64) (versionapp.RequirementDetail, error) {
	return versionapp.RequirementDetail{}, nil
}

func newStageClarificationConvergerForTest(stage versiondomain.Stage, drafts int) (stageClarificationConverger, *fakeConvergeStageRepo) {
	repo := &fakeConvergeStageRepo{stage: stage}
	// The v0.22 stage service is the single writer (R-B4-1); the confirming Host
	// edge only needs the requirement-count gate.
	service := versionapp.NewStageService(versionapp.StageServiceDeps{
		Repo: repo,
		Gates: versionapp.StageGates{
			RequirementCount: func(context.Context, int64) (int, error) {
				return drafts, nil
			},
		},
	})
	return stageClarificationConverger{
		service:      service,
		requirements: &fakeConvergeRequirements{drafts: drafts},
	}, repo
}

func TestStageClarificationConvergerRequiresDraftRequirement(t *testing.T) {
	converger, repo := newStageClarificationConvergerForTest(versiondomain.StageClarifying, 0)
	if err := converger.Converge(context.Background(), 101); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if len(repo.applied) != 0 || repo.stage != versiondomain.StageClarifying {
		t.Fatalf("no-draft convergence wrote stage: stage=%q applied=%d", repo.stage, len(repo.applied))
	}
}

func TestStageClarificationConvergerMovesClarifyingVersion(t *testing.T) {
	converger, repo := newStageClarificationConvergerForTest(versiondomain.StageClarifying, 2)
	if err := converger.Converge(context.Background(), 101); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if repo.stage != versiondomain.StageRequirementReview || len(repo.applied) != 1 {
		t.Fatalf("stage = %q applied = %d, want requirement_review once", repo.stage, len(repo.applied))
	}
}

func TestStageClarificationConvergerIgnoresCreatedVersion(t *testing.T) {
	converger, repo := newStageClarificationConvergerForTest(versiondomain.StageCreated, 2)
	// A created version is not at the requirement-review edge: the explicit
	// Request(requirement_review) is rejected and writes nothing. The automatic
	// created -> clarifying edge is driven by version.created instead.
	_ = converger.Converge(context.Background(), 101)
	if len(repo.applied) != 0 {
		t.Fatalf("created version converged without clarification: %d writes", len(repo.applied))
	}
}
