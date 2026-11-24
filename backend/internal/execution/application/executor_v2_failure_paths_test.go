package executionapp_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// seedActiveSession reserves one live executor session for the fixture run, the
// state a real run is in after its first execution call.
func seedActiveSession(fixture *caseRunnerFixture, sessionID string) {
	fixture.sutContext.mu.Lock()
	defer fixture.sutContext.mu.Unlock()
	fixture.sutContext.rows[9101] = execution.CaseRunSUTContext{
		ID: 9101, CaseRunID: 3001, SUTID: 7, AccountRole: "admin",
		SessionID: sessionID, Status: execution.SUTContextActive, LeaseEpoch: 3,
	}
}

// TestTerminalSettlementFailureCountsDropAndDegrades is review finding 1: the
// terminal ledger drain used to discard the error, so a run whose executor
// restarted / whose session was reclaimed settled PASSED with its command/file/
// trace evidence permanently lost and evidence_dropped=0 — the exact art.4.5
// hole this task closes. A settlement failure must count a drop and degrade.
func TestTerminalSettlementFailureCountsDropAndDegrades(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)
	seedActiveSession(fixture, "sess_live")
	fixture.execution.mu.Lock()
	fixture.execution.artifactsErr = errors.New("executor restarted; ledger unreachable")
	fixture.execution.mu.Unlock()

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: the recording could not be settled", finished.Status)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", finished.Summary.VerdictReason, execution.VerdictReasonEvidenceDropped)
	}
	if finished.Summary.EvidenceDropped < 1 {
		t.Fatalf("summary evidence_dropped = %d, want at least the failed settlement counted", finished.Summary.EvidenceDropped)
	}
	row, err := fixture.caseRuns.Get(context.Background(), 3001)
	if err != nil {
		t.Fatalf("reload case_run: %v", err)
	}
	if row.EvidenceDropped < 1 {
		t.Fatalf("persisted evidence_dropped = %d, want the loss recorded as a fact", row.EvidenceDropped)
	}
	if got := countEvents(fixture.events, "evidence.skipped"); got < 1 {
		t.Fatalf("evidence.skipped events = %d, want the loss evented", got)
	}
	payloads := eventPayloads(fixture.events, "evidence.skipped")
	if payloads[0]["reason"] != "evidence_settlement_failed" {
		t.Fatalf("skip reason = %v, want evidence_settlement_failed", payloads[0]["reason"])
	}
}

// TestTerminalSessionListFailureCountsDropAndDegrades is final-review finding 1:
// settleSessionEvidence used to return silently when ListByCaseRun failed, so a
// PASSED run — whose ONLY ledger drain is this terminal one — abandoned its whole
// command/file/trace recording with evidence_dropped=0 and no event. The loss
// must be counted on a fresh context (the settle ctx may be expired) and degrade
// the verdict (constitution art.4.5 / art.7.6).
func TestTerminalSessionListFailureCountsDropAndDegrades(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)
	seedActiveSession(fixture, "sess_live")
	fixture.sutContext.mu.Lock()
	fixture.sutContext.listErr = errors.New("session ledger briefly unreachable")
	fixture.sutContext.mu.Unlock()

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: the terminal drain never ran", finished.Status)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", finished.Summary.VerdictReason, execution.VerdictReasonEvidenceDropped)
	}
	if finished.Summary.EvidenceDropped < 1 {
		t.Fatalf("summary evidence_dropped = %d, want the abandoned drain counted", finished.Summary.EvidenceDropped)
	}
	row, err := fixture.caseRuns.Get(context.Background(), 3001)
	if err != nil {
		t.Fatalf("reload case_run: %v", err)
	}
	if row.EvidenceDropped < 1 {
		t.Fatalf("persisted evidence_dropped = %d, want the loss recorded as a fact", row.EvidenceDropped)
	}
	payloads := eventPayloads(fixture.events, "evidence.skipped")
	if len(payloads) < 1 {
		t.Fatal("evidence.skipped events = 0, want the loss evented")
	}
	if payloads[0]["reason"] != "evidence_settlement_failed" {
		t.Fatalf("skip reason = %v, want evidence_settlement_failed", payloads[0]["reason"])
	}
}

// TestSettlementFailureCountsTheUnsettledRemainder: when the third artifact fails
// its digest check, that artifact AND everything after it stayed un-settled — the
// whole remainder must be counted, not just one item.
func TestSettlementFailureCountsTheUnsettledRemainder(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	recordPassedResults(t, fixture)
	seedActiveSession(fixture, "sess_live")
	fixture.execution.mu.Lock()
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{
			{Kind: "command", CommandID: "cmd_1", Filename: "command-cmd_1.json", Base64: "e30="},
			{Kind: "file", Path: "a.txt", Filename: "a.txt", Base64: "YWJj"},
			{
				Kind: "file", Path: "b.txt", Filename: "b.txt", Base64: "YWJj",
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			},
			{Kind: "file", Path: "c.txt", Filename: "c.txt", Base64: "YWJj"},
		},
		NextCursor: 4,
	}}
	fixture.execution.mu.Unlock()

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive", finished.Status)
	}
	// Artifacts 3 and 4 never settled; the first two did.
	if finished.Summary.EvidenceDropped != 2 {
		t.Fatalf("evidence_dropped = %d, want the 2 un-settled artifacts", finished.Summary.EvidenceDropped)
	}
	rows, err := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if err != nil {
		t.Fatalf("ListByCaseRun: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("evidence rows = %d, want the 2 that settled before the failure", len(rows))
	}
	// The cursor must NOT advance past the un-settled remainder.
	row, ok := fixture.sutContext.row(7, "admin")
	if !ok || row.ArtifactCursor != 0 {
		t.Fatalf("artifact cursor = %+v (found %v), want 0 so the remainder is retryable", row, ok)
	}
}

// TestTerminalScreenshotSettlementFailureCountsDrop: the same silent-loss hole
// one level up — the terminal screenshot was CAPTURED and then lost by the
// content store. A capture failure is best effort; a settlement failure is lost
// evidence and must be counted.
func TestTerminalScreenshotSettlementFailureCountsDrop(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "Login 按钮不存在",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	seedActiveSession(fixture, "sess_live")
	// The terminal CLI screenshot is a recording-ledger artifact (T15); the
	// content store fails when the capture drains it into evidence.
	fixture.execution.mu.Lock()
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{{
			Kind: "screenshot", Base64: base64.StdEncoding.EncodeToString([]byte("png")),
			Path: ".playwright-cli/page-terminal.png", Filename: "page-terminal.png",
			Size: 3, MIME: "image/png",
		}},
		NextCursor: 1,
	}}
	fixture.execution.mu.Unlock()
	fixture.content.mu.Lock()
	fixture.content.putErr = errors.New("content store unavailable")
	fixture.content.mu.Unlock()

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: the captured screenshot was lost", finished.Status)
	}
	if finished.Summary.EvidenceDropped < 1 {
		t.Fatalf("evidence_dropped = %d, want the lost screenshot counted", finished.Summary.EvidenceDropped)
	}
}

// TestExecRecoversFromVanishedSession is review finding 2: a reclaimed/crashed
// session returns 404 session_not_found, and without marking the reservation
// failed every later command reused the dead id and failed identically for the
// rest of the run. The next call must re-create a session and succeed.
func TestExecRecoversFromVanishedSession(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	first, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	// The executor reclaims the session mid-run.
	fixture.execution.mu.Lock()
	fixture.execution.execErrBySession = map[string]error{
		first.SessionID: platform.NewError(404, "session_not_found", "session does not exist"),
	}
	fixture.execution.mu.Unlock()

	if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	}); err == nil {
		t.Fatal("the command against the vanished session must surface its typed error")
	}

	// The NEXT call must create a fresh session and succeed — the run is not stuck.
	third, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("Exec after the session vanished: %v (the run must recover, not stay stuck)", err)
	}
	if third.SessionID == first.SessionID {
		t.Fatalf("session id = %q, want a NEW session (the dead one was reused)", third.SessionID)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.sessions != 2 {
		t.Fatalf("sessions created = %d, want 2 (the dead reservation was replaced)", fixture.execution.sessions)
	}
}

// TestExecKeepsReservationOnLeaseLost: a stale epoch means a NEWER owner holds the
// session. Clearing the reservation there would let the stale dispatch re-create
// a session it no longer owns, so lease_lost must NOT trigger recovery.
func TestExecKeepsReservationOnLeaseLost(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	first, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	fixture.execution.mu.Lock()
	fixture.execution.execErrBySession = map[string]error{
		first.SessionID: platform.NewError(409, "lease_lost", "stale epoch"),
	}
	fixture.execution.mu.Unlock()
	if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	}); err == nil {
		t.Fatal("a fenced write must surface lease_lost")
	}
	row, ok := fixture.sutContext.row(7, "")
	if !ok || row.SessionID != first.SessionID || row.Status != execution.SUTContextActive {
		t.Fatalf("reservation = %+v (found %v), want the newer owner's session untouched", row, ok)
	}
}

// TestExecSettlementFailureIsExplicit: the tool path must surface a ledger
// failure AND count the loss (the same accounting as the terminal drain).
func TestExecSettlementFailureIsExplicit(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.mu.Lock()
	fixture.execution.artifactsErr = errors.New("ledger unreachable")
	fixture.execution.mu.Unlock()
	outcome, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err == nil {
		t.Fatal("a ledger failure must be returned, never swallowed")
	}
	if outcome.EvidenceSkipped < 1 {
		t.Fatalf("outcome = %+v, want the loss reported to the caller", outcome)
	}
	row, getErr := fixture.caseRuns.Get(context.Background(), 3001)
	if getErr != nil || row.EvidenceDropped < 1 {
		t.Fatalf("persisted evidence_dropped = %d (err %v), want the loss recorded", row.EvidenceDropped, getErr)
	}
}

// TestExecCommandFailureStillSettlesEvidence: a timed-out command's recording is
// evidence of what actually ran; the failure must not skip settlement.
func TestExecCommandFailureStillSettlesEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.mu.Lock()
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_slow", Outcome: "timeout", TimedOut: true}
	fixture.execution.execErr = platform.NewError(504, "executor_timeout", "command timed out")
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{
			{Kind: "command", CommandID: "cmd_slow", Filename: "command-cmd_slow.json", Base64: "e30="},
		},
		NextCursor: 1,
	}}
	fixture.execution.mu.Unlock()
	outcome, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"sleep"}},
	})
	if err == nil {
		t.Fatal("the command timeout must be returned")
	}
	if len(outcome.EvidenceIDs) != 1 {
		t.Fatalf("outcome = %+v, want the timed-out command's recording settled", outcome)
	}
	if !outcome.TimedOut {
		t.Fatalf("outcome = %+v, want timed_out preserved", outcome)
	}
}

// TestSessionLeaseRenewalFailureDoesNotBreakTheRun: renewal is best effort — a
// transient failure is logged and retried on the next tick, never fatal.
func TestSessionLeaseRenewalFailureDoesNotBreakTheRun(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	runner := executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:           fixture.intents,
		CaseRuns:          fixture.caseRuns,
		SUTContexts:       fixture.sutContext,
		Results:           fixture.results,
		Evidence:          fixture.evidence,
		Cases:             fixture.cases,
		Bindings:          fixture.bindings,
		Execution:         fixture.execution,
		Content:           fixture.content,
		Settings:          fixture.settings,
		Clock:             fixture.clock,
		IDs:               fixture.ids,
		Events:            fixture.events,
		InstanceID:        "api-1",
		HeartbeatInterval: 5 * time.Millisecond,
	})
	if _, err := runner.Prepare(fixture.runCtx, executionWorkItem()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	seedActiveSession(fixture, "sess_live")
	fixture.execution.mu.Lock()
	fixture.execution.renewErr = errors.New("executor briefly unreachable")
	fixture.execution.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for {
		fixture.execution.mu.Lock()
		attempts := len(fixture.execution.renewedEpochs)
		fixture.execution.mu.Unlock()
		if attempts >= 3 {
			// Renewal kept being attempted despite the failures: the timer survived.
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("renewal attempts = %d, want the heartbeat to keep retrying after a failure", attempts)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
