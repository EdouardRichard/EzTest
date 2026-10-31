package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

func TestP1ProfilesMatchContract(t *testing.T) {
	profiles := agentapp.P1Profiles()
	if len(profiles) != 3 {
		t.Fatalf("profiles = %d, want 3", len(profiles))
	}
	registry := tools.NewP1Registry(tools.Deps{})

	host := profiles[0]
	if host.Key != agentapp.ProfileKeyClarificationHost || host.Name != "澄清 Host" {
		t.Fatalf("host profile = %#v", host)
	}
	if !strings.Contains(host.SystemPrompt, "[profile:clarification_host]") {
		t.Fatalf("host prompt missing fake marker: %s", host.SystemPrompt)
	}
	if host.Budgets.MaxTurns != 12 || host.Budgets.SoftSeconds != 300 || host.Budgets.HardSeconds != 600 {
		t.Fatalf("host budgets = %#v", host.Budgets)
	}
	if len(host.AllowedTools) != 9 {
		t.Fatalf("host tools = %v", host.AllowedTools)
	}

	curator := profiles[1]
	if curator.Key != agentapp.ProfileKeyRequirementCurator || len(curator.AllowedTools) != 12 {
		t.Fatalf("curator profile = %#v", curator)
	}
	if !strings.Contains(curator.SystemPrompt, "[profile:requirement_curator]") {
		t.Fatalf("curator prompt missing fake marker")
	}

	scout := profiles[2]
	if scout.Key != agentapp.ProfileKeyContextScout || len(scout.AllowedTools) != 6 {
		t.Fatalf("scout profile = %#v", scout)
	}
	if !strings.Contains(scout.SystemPrompt, "[profile:context_scout]") {
		t.Fatalf("scout prompt missing fake marker")
	}

	for _, profile := range profiles {
		if !profile.Enabled {
			t.Fatalf("profile %s is not enabled by default", profile.Key)
		}
		for _, name := range profile.AllowedTools {
			if _, ok := registry.Get(name); !ok {
				t.Fatalf("profile %s lists unregistered tool %q", profile.Key, name)
			}
		}
	}
}

func TestEnsureProfilesIsIdempotentAndPreservesAdminEdits(t *testing.T) {
	repo := newFakeRuntimeRepo()
	ctx := context.Background()

	first, err := agentapp.EnsureProfiles(ctx, repo)
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("seeded = %d", len(first))
	}
	ids := map[string]int64{}
	for _, profile := range first {
		ids[profile.Key] = profile.ID
	}

	seed := repo.profiles[agentapp.ProfileKeyClarificationHost]
	seed.Name = "管理员改过的 Host"
	repo.profiles[agentapp.ProfileKeyClarificationHost] = seed

	second, err := agentapp.EnsureProfiles(ctx, repo)
	if err != nil {
		t.Fatalf("second EnsureProfiles: %v", err)
	}
	if len(second) != 3 {
		t.Fatalf("second seed = %d", len(second))
	}
	for _, profile := range second {
		if profile.ID != ids[profile.Key] {
			t.Fatalf("profile %s id changed: %d != %d", profile.Key, profile.ID, ids[profile.Key])
		}
	}
	if second[0].Name != "管理员改过的 Host" {
		t.Fatalf("admin edit overwritten: %q", second[0].Name)
	}
}
