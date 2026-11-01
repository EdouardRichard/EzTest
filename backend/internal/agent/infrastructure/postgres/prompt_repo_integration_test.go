//go:build integration

package agentpg_test

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newPromptRepo(t *testing.T) (*agentpg.RuntimeRepository, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	if _, err := agentapp.EnsureProfiles(context.Background(), repo); err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	return repo, pool
}

func TestEnsurePromptRevisionsBackfillsSeedOnce(t *testing.T) {
	repo, _ := newPromptRepo(t)
	ctx := context.Background()

	if err := agentapp.EnsurePromptRevisions(ctx, repo); err != nil {
		t.Fatalf("EnsurePromptRevisions: %v", err)
	}
	if err := agentapp.EnsurePromptRevisions(ctx, repo); err != nil {
		t.Fatalf("EnsurePromptRevisions again: %v", err)
	}

	revisions, hasMore, err := repo.ListPromptRevisions(ctx, agentapp.ProfileKeyClarificationHost, 10, nil)
	if err != nil {
		t.Fatalf("ListPromptRevisions: %v", err)
	}
	if hasMore || len(revisions) != 1 {
		t.Fatalf("revisions = %#v hasMore=%v", revisions, hasMore)
	}
	revision := revisions[0]
	if revision.Version != 1 || revision.Source != agent.PromptSourceSeed || revision.ActorID != nil {
		t.Fatalf("seed revision = %#v", revision)
	}
	defaultPrompt, ok := agentapp.DefaultPrompt(agentapp.ProfileKeyClarificationHost)
	if !ok || revision.SystemPrompt != defaultPrompt {
		t.Fatalf("seed body != default prompt")
	}

	profile, err := repo.GetProfile(ctx, agentapp.ProfileKeyClarificationHost)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.SystemPrompt != defaultPrompt {
		t.Fatalf("backfill changed current prompt")
	}
}

func TestEnsurePromptRevisionDoesNotOverwriteAdminEdit(t *testing.T) {
	repo, _ := newPromptRepo(t)
	ctx := context.Background()
	if err := agentapp.EnsurePromptRevisions(ctx, repo); err != nil {
		t.Fatalf("EnsurePromptRevisions: %v", err)
	}

	edited := "[profile:clarification_host] 管理员编辑。"
	if _, err := repo.AppendPromptRevision(ctx, agentapp.ProfileKeyClarificationHost, edited, "edit", agent.PromptSourceAdmin, nil); err != nil {
		t.Fatalf("AppendPromptRevision: %v", err)
	}
	if err := agentapp.EnsurePromptRevisions(ctx, repo); err != nil {
		t.Fatalf("EnsurePromptRevisions after edit: %v", err)
	}
	profile, err := repo.GetProfile(ctx, agentapp.ProfileKeyClarificationHost)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.SystemPrompt != edited {
		t.Fatalf("backfill overwrote admin edit: %q", profile.SystemPrompt)
	}
	revisions, _, err := repo.ListPromptRevisions(ctx, agentapp.ProfileKeyClarificationHost, 10, nil)
	if err != nil {
		t.Fatalf("ListPromptRevisions: %v", err)
	}
	if len(revisions) != 2 || revisions[0].Version != 2 {
		t.Fatalf("revisions = %#v", revisions)
	}
}

func TestAppendPromptRevisionIncrementsAndSyncsCurrentPrompt(t *testing.T) {
	repo, _ := newPromptRepo(t)
	ctx := context.Background()
	if err := agentapp.EnsurePromptRevisions(ctx, repo); err != nil {
		t.Fatalf("EnsurePromptRevisions: %v", err)
	}
	actorID := int64(77)
	first := "[profile:clarification_host] 第一版编辑。"
	version, err := repo.AppendPromptRevision(ctx, agentapp.ProfileKeyClarificationHost, first, "first", agent.PromptSourceAdmin, &actorID)
	if err != nil {
		t.Fatalf("AppendPromptRevision: %v", err)
	}
	if version != 2 {
		t.Fatalf("version = %d, want 2", version)
	}
	second := "[profile:clarification_host] 第二版编辑。"
	version, err = repo.AppendPromptRevision(ctx, agentapp.ProfileKeyClarificationHost, second, "second", agent.PromptSourceAdmin, &actorID)
	if err != nil {
		t.Fatalf("AppendPromptRevision second: %v", err)
	}
	if version != 3 {
		t.Fatalf("version = %d, want 3", version)
	}

	profile, err := repo.GetProfile(ctx, agentapp.ProfileKeyClarificationHost)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.SystemPrompt != second {
		t.Fatalf("current prompt = %q", profile.SystemPrompt)
	}

	revisions, hasMore, err := repo.ListPromptRevisions(ctx, agentapp.ProfileKeyClarificationHost, 2, nil)
	if err != nil {
		t.Fatalf("ListPromptRevisions: %v", err)
	}
	if !hasMore || len(revisions) != 2 || revisions[0].Version != 3 || revisions[1].Version != 2 {
		t.Fatalf("page = %#v hasMore=%v", revisions, hasMore)
	}
	if revisions[0].Note != "second" || revisions[0].ActorID == nil || *revisions[0].ActorID != actorID {
		t.Fatalf("revision = %#v", revisions[0])
	}

	before := 2
	page2, hasMore, err := repo.ListPromptRevisions(ctx, agentapp.ProfileKeyClarificationHost, 1, &before)
	if err != nil {
		t.Fatalf("ListPromptRevisions page2: %v", err)
	}
	if hasMore || len(page2) != 1 || page2[0].Version != 1 {
		t.Fatalf("page2 = %#v hasMore=%v", page2, hasMore)
	}

	revisionV1, err := repo.GetPromptRevision(ctx, agentapp.ProfileKeyClarificationHost, 1)
	if err != nil {
		t.Fatalf("GetPromptRevision: %v", err)
	}
	if revisionV1.Source != agent.PromptSourceSeed {
		t.Fatalf("v1 source = %q", revisionV1.Source)
	}
	if _, err := repo.GetPromptRevision(ctx, agentapp.ProfileKeyClarificationHost, 99); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("missing revision err = %v", err)
	}
}

func TestSavePromptRevisionNoOpAndExpectedVersion(t *testing.T) {
	repo, _ := newPromptRepo(t)
	ctx := context.Background()
	if err := agentapp.EnsurePromptRevisions(ctx, repo); err != nil {
		t.Fatalf("EnsurePromptRevisions: %v", err)
	}
	current, err := repo.GetProfile(ctx, agentapp.ProfileKeyClarificationHost)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}

	noop, err := repo.SavePromptRevision(ctx, agent.PromptWrite{
		ProfileKey:   agentapp.ProfileKeyClarificationHost,
		SystemPrompt: current.SystemPrompt,
		Source:       agent.PromptSourceAdmin,
		NoOpOnSame:   true,
	})
	if err != nil {
		t.Fatalf("SavePromptRevision noop: %v", err)
	}
	if noop.Changed || noop.Version != 1 {
		t.Fatalf("noop = %#v", noop)
	}
	revisions, _, err := repo.ListPromptRevisions(ctx, agentapp.ProfileKeyClarificationHost, 10, nil)
	if err != nil {
		t.Fatalf("ListPromptRevisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("no-op inserted a revision: %#v", revisions)
	}

	expected := 9
	if _, err := repo.SavePromptRevision(ctx, agent.PromptWrite{
		ProfileKey:      agentapp.ProfileKeyClarificationHost,
		SystemPrompt:    "[profile:clarification_host] changed",
		Source:          agent.PromptSourceAdmin,
		NoOpOnSame:      true,
		ExpectedVersion: &expected,
	}); !errors.Is(err, agent.ErrPromptVersionConflict) {
		t.Fatalf("expected_version err = %v", err)
	}

	expected = 1
	result, err := repo.SavePromptRevision(ctx, agent.PromptWrite{
		ProfileKey:      agentapp.ProfileKeyClarificationHost,
		SystemPrompt:    "[profile:clarification_host] changed",
		Source:          agent.PromptSourceAdmin,
		NoOpOnSame:      true,
		ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("SavePromptRevision: %v", err)
	}
	if !result.Changed || result.Version != 2 {
		t.Fatalf("result = %#v", result)
	}
}
