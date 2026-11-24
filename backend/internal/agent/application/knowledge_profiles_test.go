package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// knowledgeExpectedProfileTools is the contract §10.1 P7 whitelist increment after the
// v0.22 D-B1 convergence: the knowledge tool is a union with the remaining P1–P6
// tools, not a replacement.
var knowledgeExpectedProfileTools = map[string][]string{
	agentapp.ProfileKeyClarificationHost: {tools.ToolKnowledgePropose},
	agentapp.ProfileKeyDesignHost:        {tools.ToolKnowledgePropose},
	agentapp.ProfileKeyDesignAuthor:      {tools.ToolKnowledgePropose},
}

func TestKnowledgeProfileToolAdditionsMatchContract(t *testing.T) {
	got := agentapp.KnowledgeProfileTools()
	if len(got) != len(knowledgeExpectedProfileTools) {
		t.Fatalf("KnowledgeProfileTools() has %d profiles, want %d: %v", len(got), len(knowledgeExpectedProfileTools), got)
	}
	for key, expected := range knowledgeExpectedProfileTools {
		actual, ok := got[key]
		if !ok {
			t.Fatalf("KnowledgeProfileTools() is missing %s", key)
		}
		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			t.Fatalf("%s knowledge tools = %v, want %v", key, actual, expected)
		}
	}
}

func TestEnsureProfilesSeedsKnowledgeToolAdditions(t *testing.T) {
	repo := newFakeRuntimeRepo()
	seeded, err := agentapp.EnsureProfiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	if len(seeded) != 11 {
		t.Fatalf("seeded profiles = %d, want 11 (1 clarification Host + 3 design profiles + 3 execution + 2 delivery + 1 knowledge_curator + 1 design_judge; GD-17 removed version_steward)", len(seeded))
	}

	for key, additions := range knowledgeExpectedProfileTools {
		profile, ok := profileByKey(seeded, key)
		if !ok {
			t.Fatalf("seeded profiles are missing %s", key)
		}
		for _, name := range additions {
			if !hasTool(profile, name) {
				t.Fatalf("%s is missing knowledge tool %q: %v", key, name, profile.AllowedTools)
			}
		}
	}

	// The knowledge tool is a proposal surface only: teams that execute,
	// analyse or deliver must not expose it (contracts §10.1).
	excluded := []string{
		agentapp.ProfileKeyDesignCritic,
		agentapp.ProfileKeyCaseExecutor,
		agentapp.ProfileKeyFailureAnalyst,
		agentapp.ProfileKeyCoverageAnalyst,
		agentapp.ProfileKeyIssueTriager,
		agentapp.ProfileKeyReportWriter,
	}
	for _, key := range excluded {
		profile, ok := profileByKey(seeded, key)
		if !ok {
			t.Fatalf("seeded profiles are missing %s", key)
		}
		if hasTool(profile, tools.ToolKnowledgePropose) {
			t.Fatalf("%s must not expose %q: %v", key, tools.ToolKnowledgePropose, profile.AllowedTools)
		}
	}
}

func TestEnsureProfilesSeedsKnowledgeCuratorProfile(t *testing.T) {
	seeded, err := agentapp.EnsureProfiles(context.Background(), newFakeRuntimeRepo())
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	profile, ok := profileByKey(seeded, agentapp.ProfileKeyKnowledgeCurator)
	if !ok {
		t.Fatalf("seeded profiles are missing %s", agentapp.ProfileKeyKnowledgeCurator)
	}
	if !profile.Enabled {
		t.Fatal("knowledge_curator seed is disabled")
	}
	want := []string{tools.ToolKnowledgeSearch, tools.ToolKnowledgeRead, tools.ToolKnowledgePropose}
	if strings.Join(profile.AllowedTools, ",") != strings.Join(want, ",") {
		t.Fatalf("knowledge_curator tools = %v, want %v", profile.AllowedTools, want)
	}
}

func TestApplyKnowledgeProfileToolsIsASetUnion(t *testing.T) {
	// The merge must not duplicate a tool an earlier seed (or an administrator
	// edit) already whitelisted.
	repo := newFakeRuntimeRepo()
	seeded, err := agentapp.EnsureProfiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	for _, profile := range seeded {
		seen := make(map[string]int, len(profile.AllowedTools))
		for _, name := range profile.AllowedTools {
			seen[name]++
		}
		for name, count := range seen {
			if count != 1 {
				t.Fatalf("%s exposes duplicate tool %q (%d times)", profile.Key, name, count)
			}
		}
	}
}
