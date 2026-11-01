//go:build integration

package agentpg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestUpdateProfilePersistsPartialFieldsAndDetectsNoChange(t *testing.T) {
	repo, _ := newPromptRepo(t)
	ctx := context.Background()
	before, err := repo.GetProfile(ctx, agentapp.ProfileKeyClarificationHost)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}

	model := "deepseek-flash"
	budgets := agent.ProfileBudgets{MaxTurns: 20, SoftSeconds: 120, HardSeconds: 240}
	allowed := []string{"requirement.read", "context.task_pack"}
	stored, changed, err := repo.UpdateProfile(ctx, agentapp.ProfileKeyClarificationHost, agent.ProfileUpdate{
		SetModel: true, Model: &model,
		Budgets:      &budgets,
		AllowedTools: &allowed,
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if !changed {
		t.Fatal("changed = false")
	}
	if stored.Model == nil || *stored.Model != model {
		t.Fatalf("model = %v", stored.Model)
	}
	if stored.Budgets != budgets || len(stored.AllowedTools) != 2 {
		t.Fatalf("stored = %#v", stored)
	}
	if !stored.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("updated_at not bumped: %v <= %v", stored.UpdatedAt, before.UpdatedAt)
	}
	if stored.SystemPrompt != before.SystemPrompt {
		t.Fatal("PATCH must not change the prompt")
	}

	storedAgain, changed, err := repo.UpdateProfile(ctx, agentapp.ProfileKeyClarificationHost, agent.ProfileUpdate{
		SetModel: true, Model: &model,
		Budgets:      &budgets,
		AllowedTools: &allowed,
	})
	if err != nil {
		t.Fatalf("UpdateProfile again: %v", err)
	}
	if changed {
		t.Fatal("changed = true for identical values")
	}
	if !storedAgain.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("no-op bumped updated_at: %v -> %v", stored.UpdatedAt, storedAgain.UpdatedAt)
	}

	// A partial update leaves the untouched fields alone.
	cleared, changed, err := repo.UpdateProfile(ctx, agentapp.ProfileKeyClarificationHost, agent.ProfileUpdate{SetModel: true})
	if err != nil {
		t.Fatalf("UpdateProfile clear model: %v", err)
	}
	if !changed || cleared.Model != nil {
		t.Fatalf("cleared = %#v changed=%v", cleared, changed)
	}
	if cleared.Budgets != budgets || len(cleared.AllowedTools) != 2 {
		t.Fatalf("partial update reset other fields: %#v", cleared)
	}
	if _, _, err := repo.UpdateProfile(ctx, "missing_profile", agent.ProfileUpdate{SetModel: true}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("missing profile err = %v", err)
	}
}

func TestLatestPromptVersionsAfterBackfill(t *testing.T) {
	repo, _ := newPromptRepo(t)
	ctx := context.Background()
	if err := agentapp.EnsurePromptRevisions(ctx, repo); err != nil {
		t.Fatalf("EnsurePromptRevisions: %v", err)
	}
	versions, err := repo.LatestPromptVersions(ctx)
	if err != nil {
		t.Fatalf("LatestPromptVersions: %v", err)
	}
	if len(versions) != 14 {
		t.Fatalf("versions = %d, want 14", len(versions))
	}
	for key, info := range versions {
		if info.Version != 1 {
			t.Fatalf("%s version = %d", key, info.Version)
		}
		if info.CreatedAt.IsZero() || time.Since(info.CreatedAt) > time.Hour {
			t.Fatalf("%s created_at = %v", key, info.CreatedAt)
		}
	}

	if _, err := repo.AppendPromptRevision(ctx, agentapp.ProfileKeyClarificationHost, "[profile:clarification_host] v2", "", agent.PromptSourceAdmin, nil); err != nil {
		t.Fatalf("AppendPromptRevision: %v", err)
	}
	versions, err = repo.LatestPromptVersions(ctx)
	if err != nil {
		t.Fatalf("LatestPromptVersions: %v", err)
	}
	if versions[agentapp.ProfileKeyClarificationHost].Version != 2 {
		t.Fatalf("host version = %d", versions[agentapp.ProfileKeyClarificationHost].Version)
	}
}
