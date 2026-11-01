package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// p5ExpectedProfileTools is the spec §4.4 / contracts §8.1 P5 increment. It is
// a union with the P1–P4 tools, not a replacement.
var p5ExpectedProfileTools = map[string][]string{
	agentapp.ProfileKeyContextScout: {
		tools.ToolCodeTree, tools.ToolCodeRead, tools.ToolCodeSearch,
		tools.ToolCodeFacts, tools.ToolCodeRoutes, tools.ToolCodeAPIs,
		tools.ToolCodeDiff, tools.ToolCodeImpact, tools.ToolSUTPagePropose,
	},
	agentapp.ProfileKeyCaseAuthor: {
		tools.ToolCodeFacts, tools.ToolCodeRoutes, tools.ToolCodeAPIs,
		tools.ToolCodeDiff, tools.ToolCodeImpact, tools.ToolCodeAnchors,
	},
	agentapp.ProfileKeyFailureAnalyst: {
		tools.ToolCodeDiff, tools.ToolCodeFacts, tools.ToolCodeAnchors,
	},
	agentapp.ProfileKeyDesignPlanner: {
		tools.ToolCodeImpact,
	},
	agentapp.ProfileKeyCaseExecutor: {
		tools.ToolExecutionVisualAssert,
	},
}

func TestP5ProfileToolAdditionsMatchContract(t *testing.T) {
	got := agentapp.P5ProfileTools()
	if len(got) != len(p5ExpectedProfileTools) {
		t.Fatalf("P5ProfileTools() has %d profiles, want %d: %v", len(got), len(p5ExpectedProfileTools), got)
	}
	for key, expected := range p5ExpectedProfileTools {
		actual, ok := got[key]
		if !ok {
			t.Fatalf("P5ProfileTools() is missing %s", key)
		}
		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			t.Fatalf("%s P5 tools = %v, want %v", key, actual, expected)
		}
	}
}

func TestEnsureProfilesSeedsP5ToolAdditions(t *testing.T) {
	repo := newFakeRuntimeRepo()
	seeded, err := agentapp.EnsureProfiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}

	for key, additions := range p5ExpectedProfileTools {
		profile, ok := profileByKey(seeded, key)
		if !ok {
			t.Fatalf("seeded profiles are missing %s", key)
		}
		for _, name := range additions {
			if !hasTool(profile, name) {
				t.Fatalf("%s is missing P5 tool %q: %v", key, name, profile.AllowedTools)
			}
		}
	}

	// P5 code tools and candidate writers are not exposed to any other profile.
	writeTools := []string{tools.ToolSUTPagePropose, tools.ToolExecutionVisualAssert}
	for _, profile := range seeded {
		if _, adjusted := p5ExpectedProfileTools[profile.Key]; adjusted {
			continue
		}
		for _, name := range profile.AllowedTools {
			if strings.HasPrefix(name, "code.") {
				t.Fatalf("%s must not expose code tool %q", profile.Key, name)
			}
			for _, write := range writeTools {
				if name == write {
					t.Fatalf("%s must not expose %q", profile.Key, name)
				}
			}
		}
	}

	// The merge is a set: execution.visual_assert already exists in the P3 seed
	// and must not be duplicated by the P5 increment.
	executor, ok := profileByKey(seeded, agentapp.ProfileKeyCaseExecutor)
	if !ok {
		t.Fatal("case_executor profile missing")
	}
	seen := map[string]int{}
	for _, name := range executor.AllowedTools {
		seen[name]++
	}
	if seen[tools.ToolExecutionVisualAssert] != 1 {
		t.Fatalf("case_executor execution.visual_assert count = %d: %v", seen[tools.ToolExecutionVisualAssert], executor.AllowedTools)
	}
	for _, name := range executor.AllowedTools {
		if seen[name] > 1 {
			t.Fatalf("case_executor exposes duplicate tool %q", name)
		}
	}
}
