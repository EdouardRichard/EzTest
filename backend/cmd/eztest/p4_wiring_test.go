package main

import (
	"context"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
)

// fakeSettings is the narrow settings reader of the composition helpers.
type fakeSettings struct {
	ints  map[string]int64
	bools map[string]bool
}

func (f *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	value, ok := f.ints[key]
	if !ok {
		return 0, context.Canceled
	}
	return value, nil
}

func (f *fakeSettings) Bool(_ context.Context, key string) (bool, error) {
	value, ok := f.bools[key]
	if !ok {
		return false, context.Canceled
	}
	return value, nil
}

// TestP4WiringRegistryContainsP4Tools pins that the composition-root
// registration exposes every P4 tool used by the three profiles (the seven new
// tools plus the reused requirement.read = the 8 task-scoped names).
func TestP4WiringRegistryContainsP4Tools(t *testing.T) {
	registerP4Plugins(nil, nil, nil, nil, nil)

	registry := tools.NewP4Registry(tools.Deps{})
	for _, name := range []string{
		tools.ToolIssueRead,
		tools.ToolIssuePropose,
		tools.ToolReportDataRead,
		tools.ToolReportNarrativePropose,
		tools.ToolRequirementRead,
		tools.ToolVersionCloseRead,
		tools.ToolRequirementGoalMet,
		tools.ToolVersionClosePropose,
	} {
		if tool, ok := registry.Get(name); !ok || tool == nil {
			t.Fatalf("P4 registry is missing tool %q (names=%v)", name, registry.Names())
		}
	}
}

func TestP4WiringProfilesAndMapping(t *testing.T) {
	profiles := agentapp.P4Profiles()
	if len(profiles) != 3 {
		t.Fatalf("P4 profiles = %d, want 3", len(profiles))
	}
	seen := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		seen[profile.Key] = true
		if !profile.Enabled || profile.Budgets.MaxTurns != 6 || profile.Budgets.SoftSeconds != 180 || profile.Budgets.HardSeconds != 300 {
			t.Fatalf("profile %s = %+v, want enabled with the P4 budget", profile.Key, profile)
		}
	}
	for _, key := range []string{agentapp.ProfileKeyIssueTriager, agentapp.ProfileKeyReportWriter, agentapp.ProfileKeyVersionSteward} {
		if !seen[key] {
			t.Fatalf("missing P4 profile %q (got %v)", key, seen)
		}
	}
	mapping := map[string]string{
		agentapp.WorkTypeIssueTriage:      agentapp.ProfileKeyIssueTriager,
		agentapp.WorkTypeReportWave:       agentapp.ProfileKeyReportWriter,
		agentapp.WorkTypeReportVersion:    agentapp.ProfileKeyReportWriter,
		agentapp.WorkTypeRequirementClose: agentapp.ProfileKeyVersionSteward,
		agentapp.WorkTypeVersionClose:     agentapp.ProfileKeyVersionSteward,
	}
	for workType, wantProfile := range mapping {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != wantProfile {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q/%v, want %q", workType, got, ok, wantProfile)
		}
	}
}

func TestP4WiringChainBudgetUsesSettings(t *testing.T) {
	settings := &fakeSettings{ints: map[string]int64{
		"chain.max_nodes":        42,
		"chain.max_edges":        84,
		"chain.max_requirements": 12,
	}}
	budget := p4ChainBudget(context.Background(), settings)
	want := chain.Budget{MaxNodes: 42, MaxEdges: 84, MaxRequirements: 12}
	if budget != want {
		t.Fatalf("budget = %+v, want %+v", budget, want)
	}

	// Missing and out-of-range values fall back to the spec defaults.
	fallback := p4ChainBudget(context.Background(), &fakeSettings{ints: map[string]int64{
		"chain.max_nodes": 0, "chain.max_edges": 999999, "chain.max_requirements": -1,
	}})
	defaults := chain.ClampBudget(chain.Budget{})
	if fallback != defaults {
		t.Fatalf("fallback budget = %+v, want %+v", fallback, defaults)
	}
}

func TestP4WiringReportSectionOptionsUseSettings(t *testing.T) {
	settings := &fakeSettings{bools: map[string]bool{
		"report.section_summary":  false,
		"report.section_results":  true,
		"report.section_issues":   false,
		"report.section_coverage": true,
		"report.section_evidence": false,
	}}
	options := p4SectionOptions(context.Background(), settings)
	if options.Summary || !options.Results || options.Issues || !options.Coverage || options.Evidence {
		t.Fatalf("section options = %+v", options)
	}
	// Missing reads keep every section visible.
	fallback := p4SectionOptions(context.Background(), &fakeSettings{})
	if !fallback.Summary || !fallback.Results || !fallback.Issues || !fallback.Coverage || !fallback.Evidence {
		t.Fatalf("fallback section options = %+v, want all enabled", fallback)
	}
}
