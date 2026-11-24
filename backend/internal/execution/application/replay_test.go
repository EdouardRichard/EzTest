package executionapp

import (
	"context"
	"errors"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestReplayInputFingerprintIncludesEvidenceVersions pins constitution art.4.6:
// the replay input fingerprint folds the evidence versions (id+kind+sha256) the
// conclusion depends on, so an evidence change makes a prior replay input stale.
func TestReplayInputFingerprintIncludesEvidenceVersions(t *testing.T) {
	intent := execution.ExecutionIntent{
		EnvironmentFingerprint: "fp", CodeBaseline: "baseline",
	}
	base := []execution.Evidence{
		{ID: 1, Kind: execution.EvidenceCommand, SHA256: "aaa"},
		{ID: 2, Kind: execution.EvidenceFile, SHA256: "bbb"},
	}
	fp1, err := replayInputFingerprint(100, 200, intent, base)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if fp1 == "" {
		t.Fatal("fingerprint must not be empty")
	}
	// Same ids/kinds, different evidence content (sha256) → different fingerprint.
	changed := []execution.Evidence{
		{ID: 1, Kind: execution.EvidenceCommand, SHA256: "aaa"},
		{ID: 2, Kind: execution.EvidenceFile, SHA256: "ccc"},
	}
	fp2, err := replayInputFingerprint(100, 200, intent, changed)
	if err != nil {
		t.Fatalf("fingerprint changed: %v", err)
	}
	if fp1 == fp2 {
		t.Fatal("evidence version change must change the replay input fingerprint (art.4.6)")
	}
	// A different environment fingerprint also changes the input fingerprint.
	otherIntent := intent
	otherIntent.EnvironmentFingerprint = "fp2"
	fp3, err := replayInputFingerprint(100, 200, otherIntent, base)
	if err != nil {
		t.Fatalf("fingerprint env: %v", err)
	}
	if fp1 == fp3 {
		t.Fatal("environment fingerprint change must change the replay input fingerprint")
	}
	// Non-command/file evidence (e.g. text) is not a replay input version.
	withText := append(base, execution.Evidence{ID: 3, Kind: execution.EvidenceText, SHA256: "ddd"})
	fp4, err := replayInputFingerprint(100, 200, intent, withText)
	if err != nil {
		t.Fatalf("fingerprint text: %v", err)
	}
	if fp1 != fp4 {
		t.Fatal("text evidence is not a command/file replay input; fingerprint must ignore it")
	}
}

// TestReplayFingerprintGate proves the same-fingerprint gate recomputes the
// current environment fingerprint and rejects a change with a typed 409.
func TestReplayFingerprintGate(t *testing.T) {
	binding := VersionBinding{
		VersionID: 10, Status: "running", Stage: "executing",
		SUTs: []SUTBinding{{SUTID: 7, EnvironmentID: 3, BaseURL: "http://sut", AccountRoles: []string{"admin"}}},
	}
	fp, err := EnvironmentFingerprint(binding)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	baseline, err := CodeBaseline(binding)
	if err != nil {
		t.Fatalf("code baseline: %v", err)
	}
	intent := execution.ExecutionIntent{EnvironmentFingerprint: fp, CodeBaseline: baseline}
	versions := &fakeVersionReader{bindings: map[int64]VersionBinding{10: binding}}
	s := &CaseRunner{versions: versions}

	if err := s.requireReplayFingerprint(context.Background(), 10, intent); err != nil {
		t.Fatalf("same fingerprint must pass the gate: %v", err)
	}

	// Change the environment binding → recomputed fingerprint diverges → typed reject.
	changed := binding
	changed.SUTs = []SUTBinding{{SUTID: 7, EnvironmentID: 3, BaseURL: "http://other", AccountRoles: []string{"admin"}}}
	versions.bindings[10] = changed
	err = s.requireReplayFingerprint(context.Background(), 10, intent)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "replay_fingerprint_changed" || apiErr.Status != 409 {
		t.Fatalf("changed fingerprint must reject typed 409 replay_fingerprint_changed, got %v", err)
	}
}

// TestDeriveReplayResultsServerSide proves the assertion re-validation is
// server-derived from the command-log reproduction (expected from the frozen
// case, never a model report): a faithful reproduction re-validates passed, a
// divergence is inconclusive (the replay cannot confirm the original observation).
func TestDeriveReplayResultsServerSide(t *testing.T) {
	s := &CaseRunner{}
	anchor := execution.CaseRun{ID: 1000}
	snapshot := execution.CaseSnapshot{
		Steps:      []execution.CaseStep{{Seq: 1, Action: "run", Expected: "ok"}},
		Assertions: []execution.CaseAssertion{{StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindText, Expected: "hello eztest"}},
	}

	passedSteps := []execution.StepResult{{Seq: 1, Status: execution.StepPassed}}
	passedAssertions := []execution.AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed}}

	assertions, steps := s.deriveReplayResults(anchor, snapshot, true, 2, 2, passedSteps, passedAssertions)
	if len(assertions) != 1 || assertions[0].Verdict != execution.AssertionPassed {
		t.Fatalf("faithful reproduction of a passed source must confirm passed, got %+v", assertions)
	}
	if assertions[0].Expected != "hello eztest" {
		t.Fatalf("expected must be the frozen case expected (server-owned), got %q", assertions[0].Expected)
	}
	if len(steps) != 1 || steps[0].Status != execution.StepPassed {
		t.Fatalf("replay steps must be recorded passed, got %+v", steps)
	}

	// A faithfully reproduced FAILED source confirms the failure (consistency),
	// never a guessed pass (constitution art.3.4/7.6).
	failedAssertions := []execution.AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionFailed}}
	failed, failedSteps := s.deriveReplayResults(anchor, snapshot, true, 2, 2, passedSteps, failedAssertions)
	if failed[0].Verdict != execution.AssertionFailed {
		t.Fatalf("faithful reproduction of a failed source must confirm failed, got %s", failed[0].Verdict)
	}
	failedStatus, _, _, err := execution.Aggregate(execution.VerdictInput{
		Case: snapshot, Steps: failedSteps, Assertions: failed,
		Finish:        &execution.FinishCandidate{Verdict: execution.CaseRunPassed},
		EvidenceCount: 1,
	})
	if err != nil {
		t.Fatalf("aggregate failed: %v", err)
	}
	if failedStatus != execution.CaseRunFailed {
		t.Fatalf("reproduced source failure must aggregate failed, got %s", failedStatus)
	}

	// Divergence: the assertion is inconclusive, and the deterministic aggregation
	// settles the replay inconclusive (the replay cannot confirm the source).
	diverged, _ := s.deriveReplayResults(anchor, snapshot, false, 1, 2, passedSteps, passedAssertions)
	if diverged[0].Verdict != execution.AssertionInconclusive {
		t.Fatalf("divergence must be inconclusive, got %s", diverged[0].Verdict)
	}
	status, _, _, err := execution.Aggregate(execution.VerdictInput{
		Case: snapshot, Steps: steps, Assertions: diverged,
		Finish: &execution.FinishCandidate{Verdict: execution.CaseRunPassed},
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if status != execution.CaseRunInconclusive {
		t.Fatalf("diverged replay must aggregate inconclusive, got %s", status)
	}
}
