package executionapp_test

import (
	"context"
	"encoding/json"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestCaseRunPersistsStepsEvidence pins QA-F1: a real execution must persist the
// step/assertion results and the evidence the agent produced. The case snapshot
// is shaped like the design data that actually killed the QA run: no step or
// assertion carries a target SUT and the logged-out step uses an unauthenticated
// account role (`anonymous`) that is not a bound test account.
//
// v0.2.4-B (T9 design contract): the inherited value is now the snapshot
// DefaultSUTID that executionSUTBindings resolves from the version's single
// binding, not a design-authored case_version default. Prepare must still never
// reject the case, and the resolved default must still stamp an assertion that
// carries no SUT of its own.
func TestCaseRunPersistsStepsEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	snapshot := caseRunSnapshot()
	snapshot.DefaultSUTID = 0
	snapshot.SUTIDs = nil
	snapshot.Steps[0].AccountRole = "anonymous"
	snapshot.Assertions[0].SUTID = 0
	fixture.cases.snapshot = snapshot

	prepared, err := fixture.runner.Prepare(fixture.runCtx, executionWorkItem())
	if err != nil {
		t.Fatalf("Prepare must resolve the single bound SUT instead of rejecting the case: %v", err)
	}
	if got := prepared.Case.DefaultSUTID; got != 7 {
		t.Fatalf("prepared default SUT = %d, want the sole binding 7", got)
	}
	if got := prepared.Case.Assertions[0].SUTID; got != 7 {
		t.Fatalf("prepared assertion target = %d, want the resolved default 7", got)
	}
	if _, ok := fixture.sutContext.row(7, "anonymous"); !ok {
		t.Fatalf("unauthenticated (anonymous) step context was not prepared")
	}

	if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "登录表单可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "Login 按钮可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := fixture.runner.AddEvidence(context.Background(), executionScope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: []byte("snapshot: 登录按钮可见"), SUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}

	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunPassed || finished.FailureClass != "" {
		t.Fatalf("verdict = %s/%q, want passed", finished.Status, finished.FailureClass)
	}
	if finished.Summary.StepTotal != 1 || finished.Summary.AssertionTotal != 1 ||
		finished.Summary.EvidenceCount != 1 {
		t.Fatalf("summary = %+v, want 1 step / 1 assertion / 1 evidence", finished.Summary)
	}

	steps, err := fixture.results.ListSteps(context.Background(), 3001)
	if err != nil || len(steps) != 1 || steps[0].Status != execution.StepPassed {
		t.Fatalf("persisted steps = %+v (err %v)", steps, err)
	}
	assertions, err := fixture.results.ListAssertions(context.Background(), 3001)
	if err != nil || len(assertions) != 1 || assertions[0].Verdict != execution.AssertionPassed {
		t.Fatalf("persisted assertions = %+v (err %v)", assertions, err)
	}
	evidence, err := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if err != nil || len(evidence) != 1 || evidence[0].Kind != execution.EvidenceText {
		t.Fatalf("persisted evidence = %+v (err %v)", evidence, err)
	}
}

// TestFinalizePersistsBlockedReasonAndEvidence pins the second half of QA-F1: a
// case that genuinely cannot start must surface a typed blocked verdict with the
// concrete reason persisted as evidence, never a silent failed/agent_failure row
// with no steps, assertions or evidence.
func TestFinalizePersistsBlockedReasonAndEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	runErr := &agentapp.RunError{
		Reason:    "case_blocked",
		Retryable: false,
		Cause:     platform.StateConflict("step 1 has no resolvable target SUT"),
	}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, agentapp.RunResult{}, runErr, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunBlocked || stored.FailureClass != execution.FailureClassBlocked {
		t.Fatalf("finalized case_run = %s/%q, want blocked/blocked", stored.Status, stored.FailureClass)
	}
	if stored.Summary.VerdictReason != execution.VerdictReasonBlocked {
		t.Fatalf("verdict reason = %q, want blocked", stored.Summary.VerdictReason)
	}
	if stored.Summary.LastError == "" {
		t.Fatalf("the blocker reason was not persisted on the case_run")
	}
	evidence, err := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if err != nil || len(evidence) != 1 {
		t.Fatalf("blocker evidence = %+v (err %v), want one text evidence row", evidence, err)
	}
	if evidence[0].Kind != execution.EvidenceText {
		t.Fatalf("blocker evidence kind = %q, want text", evidence[0].Kind)
	}
}

// TestEvidenceCapUnlimitedByDefault pins the recording-layer cap contract:
// execution.evidence_max_per_case_run defaults to 0 = unlimited, so 101 command
// evidence items injected directly into the settlement path all persist (the
// legacy hard cap of 100 is gone). Producers arrive with the executor v2
// recording layer; the settlement chain is exercised by direct injection.
func TestEvidenceCapUnlimitedByDefault(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	for i := 0; i < 101; i++ {
		stored, err := fixture.runner.AddEvidence(context.Background(), executionScope(), executionapp.EvidenceInput{
			Kind: execution.EvidenceCommand, Data: []byte(`{"argv":["echo","ok"],"exit":0}`), Auto: true,
		})
		if err != nil {
			t.Fatalf("command evidence %d: %v", i, err)
		}
		if stored.ID == 0 {
			t.Fatalf("command evidence %d was dropped although the cap defaults to unlimited", i)
		}
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 101 {
		t.Fatalf("evidence rows = %d, want all 101 (cap 0 = unlimited)", len(rows))
	}
	for _, row := range rows {
		if row.Kind != execution.EvidenceCommand {
			t.Fatalf("evidence kind = %q, want command", row.Kind)
		}
	}
	if got := countEvents(fixture.events, "evidence.skipped"); got != 0 {
		t.Fatalf("evidence.skipped events = %d, want 0 under an unlimited cap", got)
	}
}

// TestEvidenceCapConfiguredDropsExplicitly pins the configured-cap contract:
// with execution.evidence_max_per_case_run=5 the sixth item is dropped without
// failing the run, but the drop is explicit — a zero Evidence return, one
// evidence.skipped event carrying reason/kind/cap, and the execution.evidence
// tool reports skipped instead of a fake success.
func TestEvidenceCapConfiguredDropsExplicitly(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.settings.ints["execution.evidence_max_per_case_run"] = 5

	for i := 0; i < 5; i++ {
		stored, err := fixture.runner.AddEvidence(context.Background(), executionScope(), executionapp.EvidenceInput{
			Kind: execution.EvidenceCommand, Data: []byte(`{"exit":0}`), Auto: true,
		})
		if err != nil || stored.ID == 0 {
			t.Fatalf("command evidence %d = %+v (err %v), want persisted below the cap", i, stored, err)
		}
	}
	overflow, err := fixture.runner.AddEvidence(context.Background(), executionScope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceCommand, Data: []byte(`{"exit":1}`), Auto: true,
	})
	if err != nil {
		t.Fatalf("overflow must not fail the run: %v", err)
	}
	if overflow.ID != 0 {
		t.Fatalf("overflow evidence = %+v, want the zero dropped signal", overflow)
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 5 {
		t.Fatalf("evidence rows = %d, want the configured cap 5", len(rows))
	}
	payloads := eventPayloads(fixture.events, "evidence.skipped")
	if len(payloads) != 1 {
		t.Fatalf("evidence.skipped events = %d, want exactly 1 for the dropped item", len(payloads))
	}
	payload := payloads[0]
	if payload["reason"] != "evidence_cap_reached" {
		t.Fatalf("skip reason = %v, want evidence_cap_reached", payload["reason"])
	}
	if payload["kind"] != "command" {
		t.Fatalf("skip kind = %v, want command", payload["kind"])
	}
	if payload["cap"] != int64(5) {
		t.Fatalf("skip cap = %v (%T), want int64(5)", payload["cap"], payload["cap"])
	}
	// T22 (T6 routing): cap and evidence_count are the SAME Go type in the
	// evidence.skipped payload — both int64 — so downstream consumers never see
	// one number typed int and the other int64 for the same event shape.
	if payload["evidence_count"] != int64(5) {
		t.Fatalf("skip evidence_count = %v (%T), want int64(5) (same type as cap)", payload["evidence_count"], payload["evidence_count"])
	}
	if payload["case_run_id"] != "3001" {
		t.Fatalf("skip case_run_id = %v, want 3001", payload["case_run_id"])
	}

	// The execution.evidence tool surface keeps its explicit skipped signal.
	result, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "one more",
	})
	if err != nil {
		t.Fatalf("EvidenceText at cap: %v", err)
	}
	skipped, ok := result.(map[string]any)
	if !ok || skipped["evidence_skipped"] != 1 || skipped["evidence_skipped_reason"] != "evidence_cap_reached" {
		t.Fatalf("EvidenceText result = %#v, want the unified evidence_skipped count signal", result)
	}
}

// TestEvidenceCapAutoPathsSignalSkip pins that the automatic evidence paths
// (http.request, db.query) surface a cap drop explicitly in their tool result
// instead of silently omitting the evidence id, and that every drop emits one
// evidence.skipped event.
func TestEvidenceCapAutoPathsSignalSkip(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.settings.ints["execution.evidence_max_per_case_run"] = 1
	if _, err := fixture.runner.AddEvidence(context.Background(), executionScope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: []byte("fill the cap"),
	}); err != nil {
		t.Fatalf("fill evidence: %v", err)
	}

	httpResult, err := fixture.runner.HTTP(context.Background(), executionScope(), json.RawMessage(
		`{"sut_id":"7","path":"/api/orders","method":"GET"}`))
	if err != nil {
		t.Fatalf("HTTP at cap must not fail the tool: %v", err)
	}
	httpOut := httpResult.(map[string]any)
	if httpOut["evidence_skipped"] != 1 || httpOut["evidence_skipped_reason"] != "evidence_cap_reached" {
		t.Fatalf("http result = %#v, want the unified evidence_skipped count signal", httpOut)
	}
	if _, present := httpOut["evidence_id"]; present {
		t.Fatalf("http result must not carry an evidence_id when the row was dropped: %#v", httpOut)
	}

	dbResult, err := fixture.runner.DBQuery(context.Background(), executionScope(), json.RawMessage(
		`{"sut_id":"7","sql":"SELECT 1"}`))
	if err != nil {
		t.Fatalf("DBQuery at cap must not fail the tool: %v", err)
	}
	dbOut := dbResult.(map[string]any)
	if dbOut["evidence_skipped"] != 1 || dbOut["evidence_skipped_reason"] != "evidence_cap_reached" {
		t.Fatalf("db result = %#v, want the unified evidence_skipped count signal", dbOut)
	}

	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 1 {
		t.Fatalf("evidence rows = %d, want the configured cap 1", len(rows))
	}
	if got := countEvents(fixture.events, "evidence.skipped"); got != 2 {
		t.Fatalf("evidence.skipped events = %d, want 2 (http + db)", got)
	}
}

// TestExecSettlesRecordingArtifactKinds pins that executor artifacts carrying
// the declared recording kinds command/file/trace settle as evidence rows of
// exactly those kinds (the artifact kind vocabulary is the evidence-kind
// registry). The settlement drain is the producer now that the browser bridge
// retired (T16); the mapping is identical.
func TestExecSettlesRecordingArtifactKinds(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_1", Outcome: "exited"}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{
			{Kind: "command", CommandID: "cmd_1", Filename: "command-cmd_1.json", Base64: "e30="},
			{Kind: "file", MIME: "application/octet-stream", Base64: "AAAA", Filename: "workspace.txt"},
			{Kind: "trace", MIME: "application/zip", Base64: "CCCC", Filename: "trace.zip"},
		},
		NextCursor: 3,
	}}
	outcome, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("Exec with recording artifacts: %v", err)
	}
	if len(outcome.EvidenceIDs) != 3 {
		t.Fatalf("evidence ids = %#v, want 3 settled recording artifacts", outcome.EvidenceIDs)
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if len(rows) != 3 {
		t.Fatalf("evidence rows = %d, want 3", len(rows))
	}
	wantKinds := []execution.EvidenceKind{execution.EvidenceCommand, execution.EvidenceFile, execution.EvidenceTrace}
	for i, row := range rows {
		if row.Kind != wantKinds[i] {
			t.Fatalf("evidence row %d kind = %q, want %q", i, row.Kind, wantKinds[i])
		}
	}
}

// TestExecUndeclaredArtifactKindFailsClosed pins constitution art.4.5: an
// executor artifact whose kind is not declared in the evidence-kind registry
// must fail closed — never a silent continue. The settlement drain's fail-closed
// form (T6 ruling) is an EXPLICIT counted drop that degrades the verdict to
// inconclusive, plus an evidence.skipped event naming the cause. Mutation guard:
// restoring `default: continue` (no drop counted) makes this test red.
func TestExecUndeclaredArtifactKindFailsClosed(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_1", Outcome: "exited"}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{
			{Kind: "screenshot", MIME: "image/png", Base64: "AAAA"},
			{Kind: "video", MIME: "video/mp4", Base64: "BBBB"},
		},
		NextCursor: 2,
	}}
	outcome, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
		Command: executionapp.ExecCommand{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if outcome.EvidenceSkipped != 1 {
		t.Fatalf("evidence_skipped = %d, want the undeclared kind counted as a drop", outcome.EvidenceSkipped)
	}
	if got := countEvents(fixture.events, "evidence.skipped"); got < 1 {
		t.Fatalf("evidence.skipped events = %d, want the undeclared-kind drop evented", got)
	}
	// The declared screenshot still settled; the undeclared video did not.
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if len(rows) != 1 || rows[0].Kind != execution.EvidenceScreenshot {
		t.Fatalf("evidence rows = %+v, want only the declared screenshot", rows)
	}
	// The counted drop degrades the terminal verdict (art.4.5 / ruling T6-A).
	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("verdict = %q, want inconclusive after the undeclared-kind drop", finished.Status)
	}
}
