//go:build integration

package executionpg

import (
	"context"
	"errors"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestIntentCreateAndTransitionAtomic pins the T22 replay-atomicity contract at
// the repository that owns the transaction: the intent is created and reaches
// its terminal in ONE transaction (no reader ever sees the intermediate open
// row), and a failed from-guard rolls the INSERT back — no orphan open intent
// the planner could claim into an unintended retest wave.
func TestIntentCreateAndTransitionAtomic(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	intent := execution.ExecutionIntent{
		ID:                     9101,
		VersionID:              100,
		CaseVersionID:          300,
		SUTIDs:                 []int64{5001},
		EnvironmentFingerprint: testFingerprint,
		Purpose:                execution.PurposeManual,
		Status:                 execution.IntentOpen,
		DedupeKey:              "replay:9101",
		AttemptNo:              1,
		CreatedAt:              time.Now().UTC(),
		UpdatedAt:              time.Now().UTC(),
	}
	if err := repo.Intents.CreateAndTransition(ctx, intent, []execution.IntentStatus{execution.IntentOpen}, execution.IntentSuperseded); err != nil {
		t.Fatalf("atomic create+transition: %v", err)
	}
	stored, err := repo.Intents.Get(ctx, 9101)
	if err != nil {
		t.Fatalf("get composite intent: %v", err)
	}
	if stored.Status != execution.IntentSuperseded {
		t.Fatalf("composite intent status = %s, want superseded (never observable as open)", stored.Status)
	}
	open, err := repo.Intents.ListOpen(ctx, 100)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	for _, row := range open {
		if row.ID == 9101 {
			t.Fatal("the composite intent must never appear in the open set the planner claims from")
		}
	}

	// A from-guard that does not match the created status fails the whole
	// composite: the INSERT is rolled back, leaving NO orphan row behind.
	orphan := intent
	orphan.ID = 9102
	orphan.DedupeKey = "replay:9102"
	err = repo.Intents.CreateAndTransition(ctx, orphan, []execution.IntentStatus{execution.IntentClaimed}, execution.IntentSuperseded)
	if !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("mismatched from-guard must fail with ErrStateConflict, got %v", err)
	}
	if _, err := repo.Intents.Get(ctx, 9102); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("failed composite left an orphan intent (get err = %v); the insert must roll back", err)
	}
}

// TestCaseRunCreateAndTransitionAtomic pins the same contract for the replay
// anchor case_run: created and superseded in one transaction, and a failed
// guard/fence rolls the insert back (no orphan pending anchor a scheduler tick
// could dispatch).
func TestCaseRunCreateAndTransitionAtomic(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	// case_run.intent_id is a real FK within the execution context: the anchor
	// intent must exist before the anchor case_run is inserted.
	mustCreateIntent(t, repo, 9101, 100, 300, testFingerprint, execution.PurposeManual)
	mustCreateWave(t, repo, 9200, 100)
	run := execution.CaseRun{
		ID:            9201,
		WaveID:        9200,
		IntentID:      9101,
		CaseVersionID: 300,
		Status:        execution.CaseRunPending,
		Attempts:      1,
		DispatchEpoch: 1,
		DispatchToken: "replay-9201",
		Summary:       execution.CaseRunSummary{},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := repo.CaseRuns.CreateAndTransition(ctx, run, []execution.CaseRunStatus{execution.CaseRunPending}, execution.CaseRunSuperseded, run.DispatchEpoch, run.DispatchToken); err != nil {
		t.Fatalf("atomic create+transition: %v", err)
	}
	stored, err := repo.CaseRuns.Get(ctx, 9201)
	if err != nil {
		t.Fatalf("get composite case_run: %v", err)
	}
	if stored.Status != execution.CaseRunSuperseded {
		t.Fatalf("composite anchor status = %s, want superseded (never observable as pending)", stored.Status)
	}

	// Mismatched from-guard: rollback, no orphan. Each subtest gets its OWN
	// intent — case_run is unique on (wave_id, intent_id), so reusing the
	// success run's intent would hit the unique constraint before the guard.
	mustCreateIntent(t, repo, 9102, 100, 301, testFingerprint, execution.PurposeManual)
	orphan := run
	orphan.ID = 9202
	orphan.IntentID = 9102
	orphan.DispatchToken = "replay-9202"
	err = repo.CaseRuns.CreateAndTransition(ctx, orphan, []execution.CaseRunStatus{execution.CaseRunRunning}, execution.CaseRunSuperseded, orphan.DispatchEpoch, orphan.DispatchToken)
	if !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("mismatched from-guard must fail with ErrStateConflict, got %v", err)
	}
	if _, err := repo.CaseRuns.Get(ctx, 9202); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("failed composite left an orphan case_run (get err = %v); the insert must roll back", err)
	}

	// Mismatched dispatch fence: rollback, no orphan.
	mustCreateIntent(t, repo, 9103, 100, 302, testFingerprint, execution.PurposeManual)
	fenced := run
	fenced.ID = 9203
	fenced.IntentID = 9103
	fenced.DispatchToken = "replay-9203"
	err = repo.CaseRuns.CreateAndTransition(ctx, fenced, []execution.CaseRunStatus{execution.CaseRunPending}, execution.CaseRunSuperseded, fenced.DispatchEpoch+1, fenced.DispatchToken)
	if !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("mismatched fence must fail with ErrStateConflict, got %v", err)
	}
	if _, err := repo.CaseRuns.Get(ctx, 9203); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("fenced-out composite left an orphan case_run (get err = %v); the insert must roll back", err)
	}
}
