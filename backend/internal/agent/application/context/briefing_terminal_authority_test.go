package agentcontext

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestBriefingPlanTerminalDerivesFromAuthority closes invariant 15②
// (v0.2.4-C T22): the briefing's plan-terminal classification is derived from
// version.PlanItemTerminalStatuses()/PlanItemTerminal, not a hand-written map.
// The loop is driven from the authority's VALUE SET, so a terminal status added
// to the domain is exercised here automatically (a hand-copied map would fail).
func TestBriefingPlanTerminalDerivesFromAuthority(t *testing.T) {
	for _, status := range version.PlanItemTerminalStatuses() {
		if !planItemTerminal(status) {
			t.Fatalf("authority terminal status %q classified open by the briefing reader", status)
		}
		if !planItemTerminal("  " + status + "  ") {
			t.Fatalf("authority terminal status %q (padded) classified open by the briefing reader", status)
		}
	}
	for _, status := range []string{"pending", "in_progress", "", "mystery"} {
		if planItemTerminal(status) {
			t.Fatalf("non-terminal status %q classified terminal by the briefing reader", status)
		}
	}
}
