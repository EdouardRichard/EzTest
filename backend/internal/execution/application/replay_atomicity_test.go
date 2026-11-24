package executionapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// replaySourceSetup drives one source case_run to a terminal PASSED state with
// one settled command-evidence record, and aligns the source intent's
// fingerprint with the fixture version binding so the replay same-fingerprint
// gate passes. It returns the command record JSON the content store serves.
func replaySourceSetup(t *testing.T, fixture *caseRunnerFixture) []byte {
	t.Helper()
	ctx := context.Background()

	binding := fixture.versions.binding
	fp, err := executionapp.EnvironmentFingerprint(binding)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	intent := fixture.intents.rows[1001]
	intent.EnvironmentFingerprint = fp
	baseline, err := executionapp.CodeBaseline(binding)
	if err != nil {
		t.Fatalf("code baseline: %v", err)
	}
	intent.CodeBaseline = baseline
	fixture.intents.rows[1001] = intent

	prepareCaseRun(t, fixture)

	record := map[string]any{
		"command_id":    "cmd_1",
		"argv":          []string{"true"},
		"shell":         false,
		"exit":          0,
		"outcome":       "exited",
		"stdout_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"stderr_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("record marshal: %v", err)
	}
	sum := sha256.Sum256(recordJSON)
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_1", Outcome: "exited", Exit: intptr(0)}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{{
			Kind: "command", CommandID: "cmd_1", Filename: "command-cmd_1.json",
			Base64: base64.StdEncoding.EncodeToString(recordJSON),
			SHA256: hex.EncodeToString(sum[:]),
		}},
		NextCursor: 1,
	}}
	if _, err := fixture.runner.Exec(ctx, executionScope(), executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command:     executionapp.ExecCommand{Argv: []string{"true"}},
	}); err != nil {
		t.Fatalf("source Exec: %v", err)
	}
	// The content store serves the command record for every sha (the replay
	// loader and the settled-record reader both open command evidence).
	fixture.content.open = recordJSON

	if _, err := fixture.runner.StepRecord(ctx, executionScope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(ctx, executionScope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "ok",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	finished, err := fixture.runner.Finish(ctx, executionScope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunPassed {
		t.Fatalf("source verdict = %s, want passed", finished.Status)
	}
	return recordJSON
}

// TestReplayIntentTransitionFailureLeavesNoOrphan pins the T20 routed debt
// (v0.2.4-C T22): the replay anchor intent must reach superseded ATOMICALLY
// with its creation. Before the fix, Create(open) committed first and a
// failing Transition left a persistently OPEN replay intent the planner could
// claim into an unintended retest wave. A failed create+transition must leave
// NO intent and NO case_run behind.
func TestReplayIntentTransitionFailureLeavesNoOrphan(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	replaySourceSetup(t, fixture)

	fixture.intents.mu.Lock()
	fixture.intents.transitionErr = errors.New("simulated intent transition failure")
	fixture.intents.mu.Unlock()

	_, err := fixture.runner.ReplayCaseRun(context.Background(), 10, 3001, 99)
	if err == nil {
		t.Fatal("a failing intent transition must fail the replay")
	}

	fixture.intents.mu.Lock()
	intentCount := len(fixture.intents.rows)
	var orphanOpen []int64
	for id, row := range fixture.intents.rows {
		if row.Status == execution.IntentOpen {
			orphanOpen = append(orphanOpen, id)
		}
	}
	fixture.intents.mu.Unlock()
	if intentCount != 1 || len(orphanOpen) != 0 {
		t.Fatalf("failed replay left %d intents (open orphans %v); the create+transition must be atomic — no orphan intent", intentCount, orphanOpen)
	}
	fixture.caseRuns.mu.Lock()
	runCount := len(fixture.caseRuns.rows)
	fixture.caseRuns.mu.Unlock()
	if runCount != 1 {
		t.Fatalf("failed replay left %d case_runs; no anchor may survive a failed intent creation", runCount)
	}
}

// TestReplayAnchorNeverVisibleAsOpen pins the planner-visible half of the same
// debt: after a successful replay the anchor intent and case_run are superseded
// and the intent is NOT in the open set the planner/scheduler claim from. The
// atomic create+transition means no tick can ever observe it open.
func TestReplayAnchorNeverVisibleAsOpen(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	replaySourceSetup(t, fixture)

	if _, err := fixture.runner.ReplayCaseRun(context.Background(), 10, 3001, 99); err != nil {
		t.Fatalf("ReplayCaseRun: %v", err)
	}

	fixture.intents.mu.Lock()
	var replayIntent *execution.ExecutionIntent
	for id, row := range fixture.intents.rows {
		if row.DedupeKey == "replay:3001" {
			copied := row
			replayIntent = &copied
			_ = id
		}
	}
	fixture.intents.mu.Unlock()
	if replayIntent == nil {
		t.Fatal("the replay anchor intent was not created")
	}
	if replayIntent.Status != execution.IntentSuperseded {
		t.Fatalf("replay intent status = %s, want superseded", replayIntent.Status)
	}
	open, err := fixture.intents.ListOpen(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	for _, row := range open {
		if row.DedupeKey == "replay:3001" {
			t.Fatal("the replay intent must never be visible to the planner as open work")
		}
	}
	fixture.caseRuns.mu.Lock()
	var anchor *execution.CaseRun
	for _, row := range fixture.caseRuns.rows {
		if row.IntentID == replayIntent.ID {
			copied := row
			anchor = &copied
		}
	}
	fixture.caseRuns.mu.Unlock()
	if anchor == nil || anchor.Status != execution.CaseRunSuperseded {
		t.Fatalf("replay anchor = %+v, want a superseded process row", anchor)
	}
}
