package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// p7ExpectedProfileTools is the contract §10.1 P7 whitelist increment: the
// knowledge tool is a union with the P1–P6 tools, not a replacement.
var p7ExpectedProfileTools = map[string][]string{
	agentapp.ProfileKeyClarificationHost: {tools.ToolKnowledgePropose},
	agentapp.ProfileKeyContextScout:      {tools.ToolKnowledgePropose},
	agentapp.ProfileKeyDesignHost:        {tools.ToolKnowledgePropose},
	agentapp.ProfileKeyDesignPlanner:     {tools.ToolKnowledgePropose},
	agentapp.ProfileKeyCaseAuthor:        {tools.ToolKnowledgePropose},
}

func TestP7ProfileToolAdditionsMatchContract(t *testing.T) {
	got := agentapp.P7ProfileTools()
	if len(got) != len(p7ExpectedProfileTools) {
		t.Fatalf("P7ProfileTools() has %d profiles, want %d: %v", len(got), len(p7ExpectedProfileTools), got)
	}
	for key, expected := range p7ExpectedProfileTools {
		actual, ok := got[key]
		if !ok {
			t.Fatalf("P7ProfileTools() is missing %s", key)
		}
		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			t.Fatalf("%s P7 tools = %v, want %v", key, actual, expected)
		}
	}
}

func TestEnsureProfilesSeedsP7ToolAdditions(t *testing.T) {
	repo := newFakeRuntimeRepo()
	seeded, err := agentapp.EnsureProfiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	if len(seeded) != 14 {
		t.Fatalf("seeded profiles = %d, want 14", len(seeded))
	}

	for key, additions := range p7ExpectedProfileTools {
		profile, ok := profileByKey(seeded, key)
		if !ok {
			t.Fatalf("seeded profiles are missing %s", key)
		}
		for _, name := range additions {
			if !hasTool(profile, name) {
				t.Fatalf("%s is missing P7 tool %q: %v", key, name, profile.AllowedTools)
			}
		}
	}

	// The knowledge tool is a proposal surface only: teams that execute,
	// analyse or deliver must not expose it (contracts §10.1).
	excluded := []string{
		agentapp.ProfileKeyRequirementCurator,
		agentapp.ProfileKeyDesignCritic,
		agentapp.ProfileKeyDataPlanner,
		agentapp.ProfileKeyCaseExecutor,
		agentapp.ProfileKeyFailureAnalyst,
		agentapp.ProfileKeyCoverageAnalyst,
		agentapp.ProfileKeyIssueTriager,
		agentapp.ProfileKeyReportWriter,
		agentapp.ProfileKeyVersionSteward,
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

func TestApplyP7ProfileToolsIsASetUnion(t *testing.T) {
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
