package executionapp

import (
	"context"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// R-G9 (v0.2.4-G T3, SC-G12): a duplicate or conflicting intent must never flip
// a completed version to running. Reopen is decided ONLY inside the createIntent
// loop, after the repository has resolved dedup/conflicts under the version
// advisory lock (dedup before the completed-version reopen signal). These unit
// tests pin the service half of that contract: no service entry point may
// pre-reopen from its own out-of-lock status read. The fake intent store mirrors
// the repository guard order (dedup first, then completed version), so the
// service is exercised against the same conflict ordering it faces in
// production; the lock-atomic half is pinned against real PostgreSQL in
// infrastructure/postgres/intent_integration_test.go.
//
// DISCLOSED FAKE GAP: the fake does NOT model the repository's closed-requirement
// guard (ErrRequirementClosed). The real guard reads the requirement status under
// the version advisory lock (postgres guardIntentScope); the fake has no
// requirement state, so tests that need that terminal conflict inject it with
// failNextCreate(execution.ErrRequirementClosed) (see intent_race_test.go). That
// path pins the service's response to the conflict, not the guard's own SQL —
// the guard's production behavior is pinned by the integration test, not here.

// A duplicate retest on a completed version returns the conflict and leaves the
// version completed: the natural-key slot is already occupied, so there is
// nothing to reopen for.
func TestDuplicateRetestOnCompletedDoesNotReopen(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, versiondomain.StatusRunning, 5001)
	if _, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42); err != nil {
		t.Fatalf("seed retest intent: %v", err)
	}
	// The version projects completed while the retest row still occupies the
	// active natural-key slot. The repository must resolve the duplicate before
	// it reports the completed-version reopen signal (integration test pins the
	// lock-atomic ordering; here the service must not pre-reopen on its own).
	f.versions.bindings[100] = task6Binding(100, versiondomain.StatusCompleted, 5001)

	_, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42)
	requireAPIError(t, err, "state_conflict")
	if len(f.versions.reopened) != 0 {
		t.Fatalf("duplicate retest reopened the completed version: %v", f.versions.reopened)
	}
	binding, err := f.versions.Get(ctx, 100)
	if err != nil || binding.Status != versiondomain.StatusCompleted {
		t.Fatalf("version status = %q err=%v, want completed", binding.Status, err)
	}
}

// The initial-intent scan has the same contract: when a concurrent scan won the
// insert race (ErrDuplicate), the completed version is not reopened.
func TestEnsureInitialIntentsDuplicateOnCompletedDoesNotReopen(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, versiondomain.StatusCompleted, 5001)
	// The count pre-check saw no intent, but a concurrent scan committed the
	// same natural key before this scan's insert.
	f.intents.failNextCreate(execution.ErrDuplicate)

	created, err := f.intent.EnsureInitialIntents(ctx, 100)
	if err != nil || created != 0 {
		t.Fatalf("scan after lost insert race: created=%d err=%v", created, err)
	}
	if len(f.versions.reopened) != 0 {
		t.Fatalf("duplicate initial intent reopened the completed version: %v", f.versions.reopened)
	}
	binding, err := f.versions.Get(ctx, 100)
	if err != nil || binding.Status != versiondomain.StatusCompleted {
		t.Fatalf("version status = %q err=%v, want completed", binding.Status, err)
	}
}

// The supersede re-plan path has the same contract: an already-queued
// replacement (ErrDuplicate) never reopens the completed version.
func TestSupersedeReplacementDuplicateOnCompletedDoesNotReopen(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[301] = task6Snapshot(301, 5001)
	f.versions.bindings[100] = task6Binding(100, versiondomain.StatusRunning, 5001)
	stale, err := f.intent.CreateManual(ctx, 100, 301, execution.PurposeRetest, 42)
	if err != nil {
		t.Fatalf("seed stale retest intent: %v", err)
	}
	// The confirmed revision was superseded and the same logical case is
	// current+confirmed again as version 302.
	logicalCaseID := f.cases.cases[301].CaseID
	delete(f.cases.cases, 301)
	replacement := task6Snapshot(302, 5001)
	replacement.CaseID = logicalCaseID
	f.cases.cases[302] = replacement
	f.cases.replacements[301] = 302
	f.versions.bindings[100] = task6Binding(100, versiondomain.StatusCompleted, 5001)
	// Another tick already enqueued the replacement initial intent.
	f.intents.failNextCreate(execution.ErrDuplicate)

	result, err := f.intent.SupersedeAndReplan(ctx, stale)
	if err != nil {
		t.Fatalf("SupersedeAndReplan: %v", err)
	}
	if !result.Superseded || !result.Replanned {
		t.Fatalf("result = %+v, want superseded with the replacement covered", result)
	}
	if len(f.versions.reopened) != 0 {
		t.Fatalf("duplicate replacement reopened the completed version: %v", f.versions.reopened)
	}
	binding, err := f.versions.Get(ctx, 100)
	if err != nil || binding.Status != versiondomain.StatusCompleted {
		t.Fatalf("version status = %q err=%v, want completed", binding.Status, err)
	}
}

// A genuinely NEW retest on a completed version still reopens and creates:
// fixing the duplicate mis-reopen must not break the incremental retest chain
// (SC-G12 reachability). The reopen happens through the createIntent loop after
// the repository reported the completed version under the lock.
func TestGenuineNewRetestReopensAndCreates(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, versiondomain.StatusCompleted, 5001)

	intent, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42)
	if err != nil {
		t.Fatalf("genuine new retest on completed version: %v", err)
	}
	if len(f.versions.reopened) != 1 || f.versions.reopened[0] != 100 {
		t.Fatalf("version reopen calls = %v, want [100]", f.versions.reopened)
	}
	binding, err := f.versions.Get(ctx, 100)
	if err != nil || binding.Status != versiondomain.StatusRunning {
		t.Fatalf("version status = %q err=%v, want running", binding.Status, err)
	}
	stored, err := f.intents.Get(ctx, intent.ID)
	if err != nil || stored.Status != execution.IntentOpen {
		t.Fatalf("stored intent = %+v err=%v, want open", stored, err)
	}
	if f.events.count(EventIntentCreated) != 1 {
		t.Fatalf("intent_created events = %d, want 1", f.events.count(EventIntentCreated))
	}
}

// The reopen-and-retry loop stays reachable for the concurrent-close race: a
// close that commits between the service pre-check and the insert is answered
// with reopen+retry, and a version that keeps being re-closed ends in a typed
// conflict instead of an orphan open intent.
func TestConcurrentCloseRaceReopenRetry(t *testing.T) {
	t.Run("close commits between pre-check and insert", func(t *testing.T) {
		f := newTask6Fixture(t)
		ctx := context.Background()
		f.cases.cases[300] = task6Snapshot(300, 5001)
		f.versions.bindings[100] = task6Binding(100, versiondomain.StatusRunning, 5001)
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
			t.Fatalf("stored intent = %+v err=%v, want open", stored, err)
		}
	})
	t.Run("gives up when close keeps winning", func(t *testing.T) {
		f := newTask6Fixture(t)
		ctx := context.Background()
		f.cases.cases[300] = task6Snapshot(300, 5001)
		f.versions.bindings[100] = task6Binding(100, versiondomain.StatusRunning, 5001)
		for i := 0; i < intentCreateAttempts; i++ {
			f.intents.failNextCreate(execution.ErrVersionCompleted)
		}

		_, err := f.intent.CreateManual(ctx, 100, 300, execution.PurposeRetest, 42)
		requireAPIError(t, err, "state_conflict")
		if f.intents.createC != 0 {
			t.Fatalf("intents created = %d, want 0", f.intents.createC)
		}
	})
}
