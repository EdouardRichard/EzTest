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

// TestDeliveryWiringRegistryContainsDeliveryTools pins that the composition-root
// registration exposes the P4 delivery tools (issue/report). GD-17 removed the
// version-steward tool group (requirement.goal_met / version.close_read /
// version.close_propose) with the Steward profile.
func TestDeliveryWiringRegistryContainsDeliveryTools(t *testing.T) {
	registerDeliveryPlugins(nil, nil, nil)

	registry := tools.NewToolGroupRegistry(tools.Deps{})
	for _, name := range []string{
		tools.ToolIssueRead,
		tools.ToolIssuePropose,
		tools.ToolReportDataRead,
		tools.ToolReportNarrativePropose,
	} {
		if tool, ok := registry.Get(name); !ok || tool == nil {
			t.Fatalf("delivery registry is missing tool %q (names=%v)", name, registry.Names())
		}
	}
	for _, name := range []string{
		"version.close_read",
		"requirement.goal_met",
		"version.close_propose",
	} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("delivery registry still exposes the retired steward tool %q", name)
		}
	}
}

func TestDeliveryWiringProfilesAndMapping(t *testing.T) {
	profiles := agentapp.DeliveryProfiles()
	if len(profiles) != 2 {
		t.Fatalf("delivery profiles = %d, want 2 (GD-17 removed version_steward)", len(profiles))
	}
	seen := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		seen[profile.Key] = true
		if !profile.Enabled || profile.Budgets.MaxTurns != 6 || profile.Budgets.SoftSeconds != 180 || profile.Budgets.HardSeconds != 300 {
			t.Fatalf("profile %s = %+v, want enabled with the delivery budget", profile.Key, profile)
		}
	}
	for _, key := range []string{agentapp.ProfileKeyIssueTriager, agentapp.ProfileKeyReportWriter} {
		if !seen[key] {
			t.Fatalf("missing delivery profile %q (got %v)", key, seen)
		}
	}
	mapping := map[string]string{
		agentapp.WorkTypeIssueTriage:   agentapp.ProfileKeyIssueTriager,
		agentapp.WorkTypeReportWave:    agentapp.ProfileKeyReportWriter,
		agentapp.WorkTypeReportVersion: agentapp.ProfileKeyReportWriter,
	}
	for workType, wantProfile := range mapping {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != wantProfile {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q/%v, want %q", workType, got, ok, wantProfile)
		}
	}
}

func TestDeliveryWiringChainBudgetUsesSettings(t *testing.T) {
	settings := &fakeSettings{ints: map[string]int64{
		"chain.max_nodes":        42,
		"chain.max_edges":        84,
		"chain.max_requirements": 12,
	}}
	budget := deliveryChainBudget(context.Background(), settings)
	want := chain.Budget{MaxNodes: 42, MaxEdges: 84, MaxRequirements: 12}
	if budget != want {
		t.Fatalf("budget = %+v, want %+v", budget, want)
	}

	// Missing and out-of-range values fall back to the spec defaults.
	fallback := deliveryChainBudget(context.Background(), &fakeSettings{ints: map[string]int64{
		"chain.max_nodes": 0, "chain.max_edges": 999999, "chain.max_requirements": -1,
	}})
	defaults := chain.ClampBudget(chain.Budget{})
	if fallback != defaults {
		t.Fatalf("fallback budget = %+v, want %+v", fallback, defaults)
	}
}

func TestDeliveryWiringReportSectionOptionsUseSettings(t *testing.T) {
	settings := &fakeSettings{bools: map[string]bool{
		"report.section_summary":  false,
		"report.section_results":  true,
		"report.section_issues":   false,
		"report.section_coverage": true,
		"report.section_evidence": false,
	}}
	options := deliverySectionOptions(context.Background(), settings)
	if options.Summary || !options.Results || options.Issues || !options.Coverage || options.Evidence {
		t.Fatalf("section options = %+v", options)
	}
	// Missing reads keep every section visible.
	fallback := deliverySectionOptions(context.Background(), &fakeSettings{})
	if !fallback.Summary || !fallback.Results || !fallback.Issues || !fallback.Coverage || !fallback.Evidence {
		t.Fatalf("fallback section options = %+v, want all enabled", fallback)
	}
}
