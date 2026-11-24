package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// codeIntelExpectedProfileTools is the spec §4.4 / contracts §8.1 P5 increment after
// the v0.22 D-B1 convergence (the Scout/Planner entries were removed and the
// author seed is now design_author). It is a union with the P1–P4 tools, not a
// replacement.
var codeIntelExpectedProfileTools = map[string][]string{
	agentapp.ProfileKeyDesignAuthor: {
		tools.ToolCodeFacts, tools.ToolCodeRoutes, tools.ToolCodeAPIs,
		tools.ToolCodeDiff, tools.ToolCodeImpact, tools.ToolCodeAnchors,
	},
	agentapp.ProfileKeyFailureAnalyst: {
		tools.ToolCodeDiff, tools.ToolCodeFacts, tools.ToolCodeAnchors,
	},
	agentapp.ProfileKeyDesignHost: {
		tools.ToolCodeFacts, tools.ToolCodeRoutes, tools.ToolCodeAPIs,
	},
}

func TestCodeIntelProfileToolAdditionsMatchContract(t *testing.T) {
	got := agentapp.CodeIntelProfileTools()
	if len(got) != len(codeIntelExpectedProfileTools) {
		t.Fatalf("CodeIntelProfileTools() has %d profiles, want %d: %v", len(got), len(codeIntelExpectedProfileTools), got)
	}
	for key, expected := range codeIntelExpectedProfileTools {
		actual, ok := got[key]
		if !ok {
			t.Fatalf("CodeIntelProfileTools() is missing %s", key)
		}
		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			t.Fatalf("%s codeintel tools = %v, want %v", key, actual, expected)
		}
	}
}

func TestEnsureProfilesSeedsCodeIntelToolAdditions(t *testing.T) {
	repo := newFakeRuntimeRepo()
	seeded, err := agentapp.EnsureProfiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}

	for key, additions := range codeIntelExpectedProfileTools {
		profile, ok := profileByKey(seeded, key)
		if !ok {
			t.Fatalf("seeded profiles are missing %s", key)
		}
		for _, name := range additions {
			if !hasTool(profile, name) {
				t.Fatalf("%s is missing codeintel tool %q: %v", key, name, profile.AllowedTools)
			}
		}
	}

	// P5 code tools and candidate writers are not exposed to any other profile.
	// The design judge (v0.2.4-E) is the one exemption for the code.* negative
	// gate: its seed carries the full read-only code whitelist by design (E
	// spec SC-E3, pinned by TestDesignJudgeProfileAndRole). The candidate
	// writer gate still applies to it.
	writeTools := []string{tools.ToolSUTPagePropose}
	for _, profile := range seeded {
		if _, adjusted := codeIntelExpectedProfileTools[profile.Key]; adjusted {
			continue
		}
		for _, name := range profile.AllowedTools {
			if strings.HasPrefix(name, "code.") && profile.Key != agentapp.ProfileKeyDesignJudge {
				t.Fatalf("%s must not expose code tool %q", profile.Key, name)
			}
			for _, write := range writeTools {
				if name == write {
					t.Fatalf("%s must not expose %q", profile.Key, name)
				}
			}
		}
	}

	// The merge is a set: a tool already present in the base seed must not be
	// duplicated by the P5 increment.
	executor, ok := profileByKey(seeded, agentapp.ProfileKeyCaseExecutor)
	if !ok {
		t.Fatal("case_executor profile missing")
	}
	seen := map[string]int{}
	for _, name := range executor.AllowedTools {
		seen[name]++
	}
	for _, name := range executor.AllowedTools {
		if seen[name] > 1 {
			t.Fatalf("case_executor exposes duplicate tool %q", name)
		}
	}
}
