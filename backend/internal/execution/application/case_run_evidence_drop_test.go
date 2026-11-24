package executionapp_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// recordPassedResults drives the fixture case to a deterministic passed verdict.
func recordPassedResults(t *testing.T, fixture *caseRunnerFixture) {
	t.Helper()
	if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "Login 按钮可见",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
}

// Constitution art.4.5 (ruling T6-A): a case_run whose evidence was dropped must
// settle inconclusive. T6 landed the count + explicit evidence.skipped event;
// this is the judgment-layer wiring on the settlement path.
func TestFinishDegradesToInconclusiveWhenEvidenceWasDropped(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.settings.ints["execution.evidence_max_per_case_run"] = 1
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)

	first, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "first",
	})
	if err != nil {
		t.Fatalf("first EvidenceText: %v", err)
	}
	if _, ok := first.(map[string]any)["evidence_id"]; !ok {
		t.Fatalf("first evidence row was not stored: %+v", first)
	}
	second, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "second",
	})
	if err != nil {
		t.Fatalf("second EvidenceText: %v", err)
	}
	// Unified drop shape (v0.2.4-C T11): every tool result reports the dropped
	// COUNT as evidence_skipped plus evidence_skipped_reason — one contract.
	if skipped, _ := second.(map[string]any)["evidence_skipped"].(int); skipped != 1 {
		t.Fatalf("second evidence row was not dropped by the cap: %+v", second)
	}

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive after an evidence drop", finished.Status)
	}
	if finished.FailureClass != "" {
		t.Fatalf("failure class = %q, want empty", finished.FailureClass)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", finished.Summary.VerdictReason, execution.VerdictReasonEvidenceDropped)
	}
	if finished.Summary.EvidenceDropped != 1 {
		t.Fatalf("summary evidence_dropped = %d, want 1", finished.Summary.EvidenceDropped)
	}
	// The drop is a persisted fact, not only an event (constitution art.5.7).
	row, err := fixture.caseRuns.Get(context.Background(), 3001)
	if err != nil {
		t.Fatalf("reload case_run: %v", err)
	}
	if row.EvidenceDropped != 1 {
		t.Fatalf("persisted evidence_dropped = %d, want 1", row.EvidenceDropped)
	}
}

func TestFinishWithoutEvidenceDropStaysPassed(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)
	if _, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "only",
	}); err != nil {
		t.Fatalf("EvidenceText: %v", err)
	}
	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunPassed {
		t.Fatalf("status = %q, want passed", finished.Status)
	}
	if finished.Summary.EvidenceDropped != 0 {
		t.Fatalf("evidence_dropped = %d, want 0", finished.Summary.EvidenceDropped)
	}
}

// A drop that happens while the terminal evidence is captured must degrade the
// verdict too: the settlement re-reads the fact after the captures.
func TestFinalizeDegradesWhenTerminalCaptureDropsEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.settings.ints["execution.evidence_max_per_case_run"] = 1
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)
	if _, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "fills the cap",
	}); err != nil {
		t.Fatalf("EvidenceText: %v", err)
	}
	// The terminal CLI screenshot of a failed run is dropped by the cap. The
	// capture drains the recording ledger (T15), so the screenshot is a ledger
	// artifact, not a bridge action result.
	fixture.sutContext.mu.Lock()
	fixture.sutContext.rows[9101] = execution.CaseRunSUTContext{
		ID: 9101, CaseRunID: 3001, SUTID: 7, AccountRole: "admin",
		SessionID: "ctx_active", Status: execution.SUTContextActive,
	}
	fixture.sutContext.mu.Unlock()
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{{
			Kind: "screenshot", Base64: base64.StdEncoding.EncodeToString([]byte("png")),
			Path: ".playwright-cli/page-terminal.png", Filename: "page-terminal.png",
			Size: 3, MIME: "image/png",
		}},
		NextCursor: 1,
	}}
	if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "Login 按钮不存在",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: the terminal capture dropped evidence", finished.Status)
	}
	if finished.Summary.EvidenceDropped != 1 {
		t.Fatalf("summary evidence_dropped = %d, want 1", finished.Summary.EvidenceDropped)
	}
}

// TestV2EvidenceDropDegradesVerdictEndToEnd proves ruling T6-A on the real
// settlement path: a configured cap drops the command recording, the drop is a
// persisted fact, and the terminal verdict degrades to inconclusive.
func TestV2EvidenceDropDegradesVerdictEndToEnd(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.settings.ints["execution.evidence_max_per_case_run"] = 1
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)

	// One text row fills the cap; the command recording is then dropped.
	if _, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "fills the cap",
	}); err != nil {
		t.Fatalf("EvidenceText: %v", err)
	}
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_1", Outcome: "exited"}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{{
			Kind: "command", CommandID: "cmd_1", Filename: "command-cmd_1.json",
			Base64: "e30=",
		}},
		NextCursor: 1,
	}}
	outcome, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if outcome.EvidenceSkipped != 1 || len(outcome.EvidenceIDs) != 0 {
		t.Fatalf("Exec outcome = %+v, want one explicit drop and no evidence row", outcome)
	}

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("verdict = %q, want inconclusive after the recording was dropped", finished.Status)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", finished.Summary.VerdictReason, execution.VerdictReasonEvidenceDropped)
	}
	if finished.Summary.EvidenceDropped != 1 {
		t.Fatalf("summary evidence_dropped = %d, want 1", finished.Summary.EvidenceDropped)
	}
}

// TestV2ArtifactCursorGapDegradesVerdict: a settlement cursor that fell behind
// the executor's bounded buffer is an explicit evidence loss, never a silent one.
func TestV2ArtifactCursorGapDegradesVerdict(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_2", Outcome: "exited"}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts:  []executionapp.ExecutionArtifact{},
		NextCursor: 9,
		BaseCursor: 7,
		Evicted:    7,
		Gap:        true,
		Missed:     2,
	}}
	outcome, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if outcome.EvidenceSkipped != 2 {
		t.Fatalf("EvidenceSkipped = %d, want the 2 missed artifacts counted", outcome.EvidenceSkipped)
	}
	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive || finished.Summary.EvidenceDropped != 2 {
		t.Fatalf("finished = %+v, want inconclusive with 2 dropped artifacts", finished)
	}
	if got := countEvents(fixture.events, "evidence.skipped"); got != 2 {
		t.Fatalf("evidence.skipped events = %d, want one per missed artifact", got)
	}
}
