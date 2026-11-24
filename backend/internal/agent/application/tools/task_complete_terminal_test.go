package tools

import (
	"context"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestPlanItemCompletionAcceptsAllTerminalStatuses closes invariant 15④
// (v0.2.4-C T22): planItemDone used to accept only done/completed while the
// version domain authority (version.PlanItemTerminal) classifies SEVEN statuses
// as finished. A cancelled/skipped/closed/failed plan item was therefore
// rejected by task.complete — a LIVE divergence: the model could never complete
// work it had legitimately cancelled, skipped, closed or failed. The validator
// now derives from the authority; every terminal status with a recorded
// decision completes (regression pin, driven from the authority's value set so
// a future added terminal status is exercised automatically).
func TestPlanItemCompletionAcceptsAllTerminalStatuses(t *testing.T) {
	for _, status := range version.PlanItemTerminalStatuses() {
		t.Run(status, func(t *testing.T) {
			reader := &fakePlanItemReader{item: PlanItemSnapshot{ID: 9, VersionID: 7, Status: status, Decision: "recorded"}}
			validator := NewPlanItemCompletionValidator(reader, 9)
			verification, err := validator.Validate(context.Background(), CompletionRequest{
				Scope:   Scope{VersionID: 7},
				Summary: "plan item finished",
				Status:  "completed",
			})
			if err != nil {
				t.Fatalf("terminal plan item %q rejected by task.complete: %v", status, err)
			}
			if verification.PlanItemID != 9 {
				t.Fatalf("verification target = %d, want 9", verification.PlanItemID)
			}
		})
	}
}

// TestPlanItemCompletionTerminalNormalization pins the authority's trim +
// lower-case normalization at the tool boundary: a stored " CANCELLED " or
// "Failed" classifies exactly as version.PlanItemTerminal does.
func TestPlanItemCompletionTerminalNormalization(t *testing.T) {
	for _, status := range []string{" CANCELLED ", "Failed", "CLOSED", "Skipped", "Canceled"} {
		reader := &fakePlanItemReader{item: PlanItemSnapshot{ID: 9, VersionID: 7, Status: status, Decision: "recorded"}}
		validator := NewPlanItemCompletionValidator(reader, 9)
		if _, err := validator.Validate(context.Background(), CompletionRequest{
			Scope: Scope{VersionID: 7}, Summary: "done", Status: "completed",
		}); err != nil {
			t.Fatalf("status %q: %v (authority classifies it terminal: %v)", status, err, version.PlanItemTerminal(status))
		}
	}
}

// TestPlanItemCompletionStillRejectsOpenStatuses keeps the non-terminal side:
// pending/in_progress/unknown items are still open work and must not complete.
func TestPlanItemCompletionStillRejectsOpenStatuses(t *testing.T) {
	for _, status := range []string{"pending", "in_progress", "", "mystery"} {
		if version.PlanItemTerminal(status) {
			t.Fatalf("test premise broken: authority classifies %q terminal", status)
		}
		reader := &fakePlanItemReader{item: PlanItemSnapshot{ID: 9, VersionID: 7, Status: status, Decision: "recorded"}}
		validator := NewPlanItemCompletionValidator(reader, 9)
		if _, err := validator.Validate(context.Background(), CompletionRequest{
			Scope: Scope{VersionID: 7}, Summary: "done", Status: "completed",
		}); err == nil {
			t.Fatalf("open plan item %q must not complete", status)
		}
	}
}
