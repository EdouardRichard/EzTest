package agentapp_test

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

type fakePromptSeeder struct {
	profiles []agent.AgentProfile
	listErr  error
	seen     map[string]string
	order    []string
	ensureErr error
	ensureCalls int
}

func (f *fakePromptSeeder) ListProfiles(_ context.Context) ([]agent.AgentProfile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.profiles, nil
}

func (f *fakePromptSeeder) EnsurePromptRevision(_ context.Context, profileKey, prompt string) error {
	f.ensureCalls++
	if f.ensureErr != nil {
		return f.ensureErr
	}
	if f.seen == nil {
		f.seen = map[string]string{}
	}
	if _, exists := f.seen[profileKey]; !exists {
		f.seen[profileKey] = prompt
		f.order = append(f.order, profileKey)
	}
	return nil
}

func TestEnsurePromptRevisionsBackfillsCurrentBodyOnce(t *testing.T) {
	seeder := &fakePromptSeeder{profiles: []agent.AgentProfile{
		{Key: agentapp.ProfileKeyClarificationHost, SystemPrompt: "[profile:clarification_host] v1"},
		{Key: agentapp.ProfileKeyCaseExecutor, SystemPrompt: "[profile:case_executor] v1"},
	}}
	ctx := context.Background()

	if err := agentapp.EnsurePromptRevisions(ctx, seeder); err != nil {
		t.Fatalf("EnsurePromptRevisions: %v", err)
	}
	if seeder.ensureCalls != 2 {
		t.Fatalf("ensure calls = %d, want 2", seeder.ensureCalls)
	}
	if seeder.seen[agentapp.ProfileKeyClarificationHost] != "[profile:clarification_host] v1" {
		t.Fatalf("backfilled prompt = %q", seeder.seen[agentapp.ProfileKeyClarificationHost])
	}

	// The repository is responsible for the "already has a revision" check; the
	// application backfill must be idempotent when the repository no-ops.
	seeder.ensureErr = nil
	if err := agentapp.EnsurePromptRevisions(ctx, seeder); err != nil {
		t.Fatalf("second EnsurePromptRevisions: %v", err)
	}
	if len(seeder.seen) != 2 {
		t.Fatalf("seen profiles = %d, want 2", len(seeder.seen))
	}
}

func TestEnsurePromptRevisionsPropagatesErrors(t *testing.T) {
	wantList := errors.New("list boom")
	if err := agentapp.EnsurePromptRevisions(context.Background(), &fakePromptSeeder{listErr: wantList}); !errors.Is(err, wantList) {
		t.Fatalf("list err = %v", err)
	}

	wantEnsure := errors.New("ensure boom")
	seeder := &fakePromptSeeder{
		profiles:  []agent.AgentProfile{{Key: "clarification_host", SystemPrompt: "x"}},
		ensureErr: wantEnsure,
	}
	if err := agentapp.EnsurePromptRevisions(context.Background(), seeder); !errors.Is(err, wantEnsure) {
		t.Fatalf("ensure err = %v", err)
	}
}

func TestEnsurePromptRevisionsWithoutRepository(t *testing.T) {
	if err := agentapp.EnsurePromptRevisions(context.Background(), nil); err == nil {
		t.Fatal("expected an unavailable error")
	}
}

func TestDefaultPromptMatchesSeedProfiles(t *testing.T) {
	seeds := agentapp.ProfileSeeds()
	if len(seeds) != 14 {
		t.Fatalf("ProfileSeeds = %d, want 14", len(seeds))
	}
	for _, profile := range seeds {
		got, ok := agentapp.DefaultPrompt(profile.Key)
		if !ok {
			t.Fatalf("DefaultPrompt(%q) not found", profile.Key)
		}
		if got != profile.SystemPrompt {
			t.Fatalf("DefaultPrompt(%q) != Profiles() seed", profile.Key)
		}
	}
	if _, ok := agentapp.DefaultPrompt("does_not_exist"); ok {
		t.Fatal("unknown key returned a default prompt")
	}
}
