package agentapp

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestSummaryPlanTerminalDerivesFromAuthority closes invariant 15③
// (v0.2.4-C T22): the rolling-summary plan-terminal classification is derived
// from version.PlanItemTerminalStatuses()/PlanItemTerminal, not a hand-written
// map. Driven from the authority's value set so a newly added terminal status
// is exercised automatically.
func TestSummaryPlanTerminalDerivesFromAuthority(t *testing.T) {
	for _, status := range version.PlanItemTerminalStatuses() {
		if !summaryPlanTerminal(status) {
			t.Fatalf("authority terminal status %q classified open by the summary reader", status)
		}
		if !summaryPlanTerminal("  " + status + "  ") {
			t.Fatalf("authority terminal status %q (padded) classified open by the summary reader", status)
		}
	}
	for _, status := range []string{"pending", "in_progress", "", "mystery"} {
		if summaryPlanTerminal(status) {
			t.Fatalf("non-terminal status %q classified terminal by the summary reader", status)
		}
	}
}
