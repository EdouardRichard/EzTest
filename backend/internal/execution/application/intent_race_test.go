package executionapp

import (
	"context"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// The intent repository rejects an insert when a version close committed first
// under the shared version advisory lock. The service must reopen the version
// through the version port (P4 version.reopened) and retry.
func TestCreateManualReopensWhenCloseWinsVersionLock(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	f.intents.failNextCreate(execution.ErrVersionCompleted)

	intent, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42)
	if err != nil {
		t.Fatalf("CreateManual after a lost close race: %v", err)
	}
	if len(f.versions.reopened) != 1 || f.versions.reopened[0] != 100 {
		t.Fatalf("version reopen calls = %v, want [100]", f.versions.reopened)
	}
	stored, err := f.intents.Get(ctx, intent.ID)
	if err != nil || stored.Status != execution.IntentOpen {
		t.Fatalf("stored intent = %+v err=%v", stored, err)
	}
	if f.events.count(EventIntentCreated) != 1 {
		t.Fatalf("intent_created events = %d, want 1", f.events.count(EventIntentCreated))
	}
}

// The same recovery applies to the reconciler scan.
func TestEnsureInitialIntentsReopensWhenCloseWinsVersionLock(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	f.intents.failNextCreate(execution.ErrVersionCompleted)

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil || created != 1 {
		t.Fatalf("EnsureInitialIntents after a lost close race: created=%d err=%v", created, err)
	}
	if len(f.versions.reopened) != 1 || f.versions.reopened[0] != 100 {
		t.Fatalf("version reopen calls = %v, want [100]", f.versions.reopened)
	}
	active, err := f.intents.ListActive(ctx, 100)
	if err != nil || len(active) != 1 || active[0].Status != execution.IntentOpen {
		t.Fatalf("active intents = %+v err=%v", active, err)
	}
}

// A closed requirement is terminal for intent creation: the manual API wins a
// state conflict, never an open intent.
func TestCreateManualRejectsClosedRequirement(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	f.intents.failNextCreate(execution.ErrRequirementClosed)

	_, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeManual, 42)
	apiErr := requireAPIError(t, err, "state_conflict")
	if !strings.Contains(strings.ToLower(apiErr.Message), "requirement") {
		t.Fatalf("message = %q, want a requirement hint", apiErr.Message)
	}
	// Task 112: the retry 409 must be actionable in the UI: which requirement is
	// closed and what the user should do next (open it, then retry).
	if apiErr.Details["reason"] != "requirement_closed" {
		t.Fatalf("details.reason = %#v, want requirement_closed", apiErr.Details["reason"])
	}
	if apiErr.Details["requirement_id"] != "2300" {
		t.Fatalf("details.requirement_id = %#v, want the closed requirement 2300", apiErr.Details["requirement_id"])
	}
	if next, _ := apiErr.Details["next_action"].(string); !strings.Contains(next, "需求") {
		t.Fatalf("details.next_action = %#v, want reopen guidance", apiErr.Details["next_action"])
	}
	if f.intents.createC != 0 {
		t.Fatalf("intents created = %d, want 0", f.intents.createC)
	}
	if f.events.count(EventIntentCreated) != 0 {
		t.Fatalf("intent_created events = %d, want 0", f.events.count(EventIntentCreated))
	}
}

// The reconciler skips (instead of failing the whole scan) when a case belongs
// to a requirement that was closed before the initial intent was created.
func TestEnsureInitialIntentsSkipsClosedRequirement(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	f.intents.failNextCreate(execution.ErrRequirementClosed)

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil || created != 0 {
		t.Fatalf("EnsureInitialIntents for a closed requirement: created=%d err=%v", created, err)
	}
	if f.intents.createC != 0 {
		t.Fatalf("intents created = %d, want 0", f.intents.createC)
	}
}

// A version that keeps being re-closed while the intent retries cannot leave an
// open intent behind: the service gives up with a state conflict.
func TestCreateManualGivesUpWhenCloseKeepsWinning(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	for i := 0; i < 4; i++ {
		f.intents.failNextCreate(execution.ErrVersionCompleted)
	}

	_, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42)
	requireAPIError(t, err, "state_conflict")
	if f.intents.createC != 0 {
		t.Fatalf("intents created = %d, want 0", f.intents.createC)
	}
}
