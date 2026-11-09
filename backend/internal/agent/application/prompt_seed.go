package agentapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

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

/* ------------------------- v0.2.3 prompt upgrade ------------------------- */

// profileBackfillPromptNote is the note of the append-only seed prompt revision
// written when an old built-in prompt is detected.
const profileBackfillPromptNote = "upgrade 0.2.3"

// legacySeedPromptHashes is the allow-list of historical built-in prompt bodies
// (normalized to LF before hashing) that the backfill may replace with the
// current seed prompt. Comparing against known seed bodies is what separates an
// untouched old seed from an administrator edit whose revision source is not
// trustworthy (for example a pre-revision database row). Hashes keep the
// policy compact; line endings are normalized so a Windows checkout and a Linux
// build agree.
var legacySeedPromptHashes = map[string][]string{
	ProfileKeyClarificationHost: {
		// v0.2.1 (the UAT database) and v0.2.2.
		"1faa0108cf679bf2ff64dda1202a563dc20c1ecb9ca6b736cd29d61a1744fc7d",
		"c3e8e3119ec214c4b8bba5cbbd3aa237cb2cd2ae0d971edfe1eaf671ce9419e3",
	},
	ProfileKeyDesignHost: {
		// v0.2.1 and v0.2.2 bodies are identical; Task 8 changes this prompt.
		"d7018dc9ded63969ee8a1af336c2ce0f10f7cea6dd3d227c8a7e2f28ccdbc2f4",
	},
	ProfileKeyDesignCritic: {
		"834b1b2b9bac7239955425fa44249adb7f1b423d5c81224d56a28128a989f80d",
		"c6f078567d8a0746c1aeb1aab64fc461c90fe39413e3389ed6019e4955fe153d",
	},
	ProfileKeyCaseExecutor: {
		"7a38ab53bdc38d98df9fd719f0bbf167081946291b895b876130b6da12f310ee",
		"952ba40510416c335da5701fbf3659bd8c9c1ea9af4b5e4f6c72049fe829be99",
	},
}

// backfillSeedPrompt appends one current seed revision when the latest revision
// (or, for a pre-revision database, the stored profile body) is an untouched
// historical seed body. An admin/reset revision or an unknown body always wins.
func backfillSeedPrompt(
	ctx context.Context,
	repo ProfileBackfillRepository,
	key, storedPrompt string,
) (bool, error) {
	current, ok := DefaultPrompt(key)
	if !ok {
		return false, nil
	}
	revisions, _, err := repo.ListPromptRevisions(ctx, key, 1, nil)
	if err != nil {
		return false, err
	}
	candidate := storedPrompt
	write := agent.PromptWrite{
		ProfileKey:   key,
		SystemPrompt: current,
		Note:         profileBackfillPromptNote,
		Source:       agent.PromptSourceSeed,
	}
	if len(revisions) > 0 {
		latest := revisions[0]
		if latest.Source != agent.PromptSourceSeed {
			return false, nil
		}
		candidate = latest.SystemPrompt
		version := latest.Version
		write.ExpectedVersion = &version
	}
	if normalizePromptBody(candidate) == normalizePromptBody(current) {
		return false, nil
	}
	if !isLegacySeedPrompt(key, candidate) {
		return false, nil
	}
	result, err := repo.SavePromptRevision(ctx, write)
	if err != nil {
		// A concurrent startup/admin writer already moved the revision on; the
		// marker step still runs and the next boot observes the new state.
		if errors.Is(err, agent.ErrPromptVersionConflict) {
			return false, nil
		}
		return false, err
	}
	return result.Changed, nil
}

func isLegacySeedPrompt(key, body string) bool {
	digest := seedPromptDigest(body)
	for _, candidate := range legacySeedPromptHashes[key] {
		if candidate == digest {
			return true
		}
	}
	return false
}

func seedPromptDigest(body string) string {
	sum := sha256.Sum256([]byte(normalizePromptBody(body)))
	return hex.EncodeToString(sum[:])
}

// normalizePromptBody makes the legacy hash independent of the checkout line
// ending (the same source file is CRLF on a Windows worktree and LF on CI).
func normalizePromptBody(body string) string {
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	return strings.ReplaceAll(normalized, "\r", "\n")
}
