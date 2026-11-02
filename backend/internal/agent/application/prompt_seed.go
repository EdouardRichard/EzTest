package agentapp

import (
	"context"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// PromptRevisionSeeder is the narrow persistence port of the startup prompt
// backfill. The repository inserts v1 (source=seed) only when the profile has
// no revision yet; it never overwrites an administrator edit.
type PromptRevisionSeeder interface {
	ListProfiles(ctx context.Context) ([]agent.AgentProfile, error)
	EnsurePromptRevision(ctx context.Context, profileKey, prompt string) error
}

// EnsurePromptRevisions backfills the v1 seed revision for every profile that
// has no revision yet, using the current agent_profile.system_prompt as the
// body (spec §8.3). It runs after EnsureProfiles and is idempotent.
func EnsurePromptRevisions(ctx context.Context, repo PromptRevisionSeeder) error {
	if repo == nil {
		return platform.Unavailable("prompt revision repository is not configured")
	}
	profiles, err := repo.ListProfiles(ctx)
	if err != nil {
		return err
	}
	for _, profile := range profiles {
		if err := repo.EnsurePromptRevision(ctx, profile.Key, profile.SystemPrompt); err != nil {
			return err
		}
	}
	return nil
}

// DefaultPrompt returns the built-in seed prompt of a profile. It is the same
// source as Profiles(), never a copied string (spec §8.3).
func DefaultPrompt(key string) (string, bool) {
	for _, profile := range Profiles() {
		if profile.Key == key {
			return profile.SystemPrompt, true
		}
	}
	return "", false
}

// ProfileSeeds returns a fresh copy of the built-in profile seeds. Callers may
// mutate the returned slice without affecting the next seed/backfill pass.
func ProfileSeeds() []agent.AgentProfile {
	profiles := Profiles()
	for i := range profiles {
		profiles[i].AllowedTools = append([]string(nil), profiles[i].AllowedTools...)
	}
	return profiles
}
