package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// recWorld is the fake world of one recorder test.
type recWorld struct {
	runs       *schedCaseRunRepo
	steps      *schedStepRepo
	assertions *schedAssertionRepo
	evidence   *recEvidenceRepo
	refs       *recRefRepo
	batches    *schedBatchRepo
	versions   *schedVersionPort
	sessions   *schedSessionPort
	events     *schedEvents
	clock      *schedClock
	ids        *schedIDs
}

// newRecorderTest wires a Recorder to deterministic in-memory fakes.
func newRecorderTest(t *testing.T) (*CaseRecorder, *recWorld) {
	t.Helper()
	world := newRecWorld()
	recorder := NewRecorder(RecorderDeps{
		Locator:    world.runs,
		CaseRuns:   world.runs,
		Steps:      world.steps,
		Assertions: world.assertions,
		Evidence:   world.evidence,
		Refs:       world.refs,
		Batches:    world.batches,
		Versions:   world.versions,
		Sessions:   world.sessions,
		Events:     world.events,
		Clock:      world.clock,
		IDs:        world.ids,
	})
	return recorder, world
}

func newRecWorld() *recWorld {
	world := &recWorld{
		runs:       newSchedCaseRunRepo(),
		steps:      newSchedStepRepo(),
		assertions: newSchedAssertionRepo(),
		evidence:   newRecEvidenceRepo(),
		refs:       &recRefRepo{},
		versions:   &schedVersionPort{status: "executing", systemID: int64Pointer(schedSystemID), environmentID: int64Pointer(schedEnvID)},
		sessions:   newSchedSessionPort(),
		events:     &schedEvents{},
		clock:      &schedClock{now: testNow},
		ids:        &schedIDs{},
	}
	world.batches = newSchedBatchRepo(world.runs)
	world.batches.seed(domain.RunBatch{
		ID: schedBatchID, VersionID: schedVersionID, BatchNo: 1,
		Status: domain.BatchStatusRunning, CaseCount: 1, CreatedAt: testNow, UpdatedAt: testNow,
	})
	world.sessions.environment = EnvironmentRef{
		ID: schedEnvID, SystemID: schedSystemID, Name: "local",
		BaseURL: "http://127.0.0.1:13335", AllowedHosts: []string{"127.0.0.1"},
	}
	return world
}

// seedRunningRun stores one running case run with the supplied snapshot.
func (w *recWorld) seedRunningRun(snapshot domain.CaseSnapshot) domain.CaseRun {
	started := testNow.Add(-time.Minute)
	run := domain.CaseRun{
		ID:           2,
		BatchID:      schedBatchID,
		VersionID:    schedVersionID,
		CaseNodeID:   snapshot.Node.ID,
		CaseRevision: 1,
		CaseHash:     "hash",
		Snapshot:     snapshot,
		Attempt:      1,
		Status:       domain.CaseRunStatusRunning,
		StartedAt:    &started,
		CreatedAt:    started,
		UpdatedAt:    started,
	}
	w.runs.seed(run)
	return run
}

// evidenceFor records one evidence row of the given case run.
func (w *recWorld) evidenceFor(caseRunID int64, metadata string) domain.Evidence {
	return w.evidence.seed(domain.Evidence{
		BatchID:   schedBatchID,
		CaseRunID: int64Pointer(caseRunID),
		Kind:      "screenshot",
		Path:      fmt.Sprintf("55/%d/ev.png", caseRunID),
		SHA256:    "sha",
		SizeBytes: 3,
		Mime:      "image/png",
		Metadata:  map[string]any{"label": metadata},
		CreatedAt: testNow,
	})
}

// recorderSnapshot is a one-step, one-ui-assertion case snapshot.
func recorderSnapshot() domain.CaseSnapshot {
	return domain.CaseSnapshot{
		Node:  domain.CaseSnapshotNode{ID: 10, Title: "case 10", Priority: "P0"},
		Steps: []domain.CaseSnapshotStep{{Seq: 1, Action: "open", Expected: "opened"}},
		Assertions: []domain.CaseSnapshotAssertion{
			{ID: 20, StepSeq: 1, Kind: "ui", Target: "body", Expression: "visible", Expected: "shown"},
		},
		GraphHash:  testBaselineHash,
		CapturedAt: testNow,
	}
}

// recordStepAndAssertion records step 1 and assertion 20 with one evidence id.
func recordStepAndAssertion(t *testing.T, recorder *CaseRecorder, run domain.CaseRun, evidenceID int64, stepStatus domain.StepStatus, assertionVerdict string) (domain.StepResult, domain.AssertionResult) {
	t.Helper()
	step, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 1, Status: stepStatus, Actual: "actual", EvidenceIDs: []int64{evidenceID},
	})
	if err != nil {
		t.Fatalf("RecordStep: %v", err)
	}
	assertion, err := recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 20, Verdict: assertionVerdict, Actual: "actual", EvidenceIDs: []int64{evidenceID},
	})
	if err != nil {
		t.Fatalf("RecordAssertion: %v", err)
	}
	return step, assertion
}

// ---------------------------------------------------------------------------
// RecordStep
// ---------------------------------------------------------------------------

func TestRecorderRejectsOutOfOrderStep(t *testing.T) {
	recorder, world := newRecorderTest(t)
	run := world.seedRunningRun(schedSnapshot(1, "P0"))

	_, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 2, Status: domain.StepStatusPassed, Actual: "done",
	})
	wantToolError(t, err, "invalid_arguments", "next unfinished step 1")
	if steps, _ := world.steps.ListByCaseRun(context.Background(), run.ID); len(steps) != 0 {
		t.Fatalf("a rejected step must not be stored: %+v", steps)
	}

	if _, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "done",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "again",
	})
	wantToolError(t, err, "invalid_arguments", "next unfinished step 2")
}

func TestRecorderStepPersistsSnapshotFields(t *testing.T) {
	recorder, world := newRecorderTest(t)
	snapshot := schedSnapshot(1, "P0")
	run := world.seedRunningRun(snapshot)
	evidence := world.evidenceFor(run.ID, "step evidence")

	step, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 1, Status: domain.StepStatusFailed, Actual: "button missing",
		EvidenceIDs: []int64{evidence.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if step.Seq != 1 || step.Status != domain.StepStatusFailed {
		t.Fatalf("step = %+v", step)
	}
	if step.Action != snapshot.Steps[0].Action || step.Expected != snapshot.Steps[0].Expected {
		t.Fatalf("step snapshot fields = %+v", step)
	}
	if step.Actual != "button missing" || step.EvidenceCount != 1 {
		t.Fatalf("step actual/evidence = %+v", step)
	}
	if step.BatchID != schedBatchID || step.CaseRunID != run.ID || step.ID <= 0 {
		t.Fatalf("step identity = %+v", step)
	}
	stepEvent := world.events.firstOf("step.finished")
	if stepEvent == nil {
		t.Fatalf("events = %v", world.events.types())
	}
	// D29: every execution id in an event detail is a decimal string.
	if got := stepEvent.Detail["case_run_id"]; got != fmt.Sprintf("%d", run.ID) {
		t.Fatalf("step.finished case_run_id = %#v, want decimal string %q", got, fmt.Sprintf("%d", run.ID))
	}
	found := false
	for _, ref := range world.refs.all() {
		if ref.RefKind == "step_result" && ref.RefID == step.ID && ref.EvidenceID == evidence.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("step evidence ref missing: %+v", world.refs.all())
	}
}

func TestRecorderStepValidatesStatusActualAndEvidence(t *testing.T) {
	t.Run("failed needs an actual", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		_, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusFailed, Actual: "   ",
		})
		wantToolError(t, err, "invalid_arguments", "actual")
	})
	t.Run("status must be passed or failed", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		_, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusRunning, Actual: "x",
		})
		wantToolError(t, err, "invalid_arguments", "passed or failed")
	})
	t.Run("missing evidence", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		_, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusPassed, Actual: "ok", EvidenceIDs: []int64{404},
		})
		wantToolError(t, err, "not_found", "404")
	})
	t.Run("foreign evidence", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		foreign := world.evidenceFor(999, "other case")
		_, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusPassed, Actual: "ok", EvidenceIDs: []int64{foreign.ID},
		})
		wantToolError(t, err, "invalid_arguments", "does not belong")
	})
	t.Run("terminal run", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		run.Status = domain.CaseRunStatusPassed
		world.runs.seed(run)
		_, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusPassed, Actual: "ok",
		})
		wantToolError(t, err, "invalid_arguments", "already terminal")
	})
}

// ---------------------------------------------------------------------------
// RecordAssertion
// ---------------------------------------------------------------------------

func TestRecorderAssertionBelongsToCaseAndIsSubmittedOnce(t *testing.T) {
	recorder, world := newRecorderTest(t)
	run := world.seedRunningRun(recorderSnapshot())
	evidence := world.evidenceFor(run.ID, "assertion evidence")

	_, err := recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 777, Verdict: "passed", EvidenceIDs: []int64{evidence.ID},
	})
	wantToolError(t, err, "invalid_arguments", "not part of")

	_, err = recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 20, Verdict: "passed",
	})
	wantToolError(t, err, "invalid_arguments", "evidence")

	foreign := world.evidenceFor(999, "foreign")
	_, err = recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 20, Verdict: "passed", EvidenceIDs: []int64{foreign.ID},
	})
	wantToolError(t, err, "invalid_arguments", "does not belong")

	step, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "ok", EvidenceIDs: []int64{evidence.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 20, Verdict: "passed", Actual: "visible", EvidenceIDs: []int64{evidence.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssertionID != 20 || result.Verdict != "passed" || result.Kind != "ui" ||
		result.Target != "body" || result.Expected != "shown" || result.Actual != "visible" {
		t.Fatalf("assertion result = %+v", result)
	}
	if result.StepResultID == nil || *result.StepResultID != step.ID {
		t.Fatalf("assertion step result = %v, want %d", result.StepResultID, step.ID)
	}
	if result.BatchID != schedBatchID || result.CaseRunID != run.ID {
		t.Fatalf("assertion identity = %+v", result)
	}
	_, err = recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 20, Verdict: "failed", EvidenceIDs: []int64{evidence.ID},
	})
	wantToolError(t, err, "invalid_arguments", "already submitted")
}

// ---------------------------------------------------------------------------
// FinishCase
// ---------------------------------------------------------------------------

func TestRecorderFinishCaseValidates(t *testing.T) {
	t.Run("all steps and assertions must be recorded", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "pass")
		if _, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusPassed, Actual: "ok", EvidenceIDs: []int64{evidence.ID},
		}); err != nil {
			t.Fatal(err)
		}
		_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
		})
		wantToolError(t, err, "invalid_arguments", "not submitted")
	})
	t.Run("assertion evidence satisfies the evidence gate", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "assertion only")
		if _, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusPassed, Actual: "ok",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
			AssertionID: 20, Verdict: "passed", EvidenceIDs: []int64{evidence.ID},
		}); err != nil {
			t.Fatal(err)
		}
		// Spec §6.6.2 counts every evidence artifact of the case run, not only
		// the step-attached ones.
		finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
		})
		if err != nil {
			t.Fatalf("FinishCase with assertion evidence: %v", err)
		}
		if finished.Status != domain.CaseRunStatusPassed {
			t.Fatalf("status = %s, want passed", finished.Status)
		}
	})
	t.Run("passed needs evidence", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		snapshot := recorderSnapshot()
		snapshot.Assertions = nil
		run := world.seedRunningRun(snapshot)
		if _, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusPassed, Actual: "ok",
		}); err != nil {
			t.Fatal(err)
		}
		_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
		})
		wantToolError(t, err, "invalid_arguments", "evidence_missing")
	})
	t.Run("passed requires every step passed", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "failed step")
		recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusFailed, "passed")
		_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
		})
		wantToolError(t, err, "invalid_arguments", "verdict_not_justified")
	})
	t.Run("failed requires a failed assertion", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "passed")
		recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusPassed, "passed")
		_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{
				Verdict: domain.VerdictFailed, FailureClass: failureClassPointer(domain.FailureClassApplication),
			},
		})
		wantToolError(t, err, "invalid_arguments", "failed requires at least one failed")
	})
	t.Run("failed with a failed assertion is application", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "failed assertion")
		recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusFailed, "failed")
		finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{
				Verdict:      domain.VerdictFailed,
				FailureClass: failureClassPointer(domain.FailureClassApplication),
				Reason:       "assertion mismatch",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if finished.Status != domain.CaseRunStatusFailed || finished.FailureClass == nil ||
			*finished.FailureClass != domain.FailureClassApplication {
			t.Fatalf("finished = %+v", finished)
		}
		if finished.FinishedAt == nil || finished.DurationMS < 0 {
			t.Fatalf("finish timing = %+v", finished)
		}
		if !world.events.has("run.finished") || !world.events.has("case.status_changed") {
			t.Fatalf("events = %v", world.events.types())
		}
		if got := world.events.firstOf("run.finished").Detail["case_run_id"]; got != fmt.Sprintf("%d", run.ID) {
			t.Fatalf("run.finished case_run_id = %#v, want decimal string %q", got, fmt.Sprintf("%d", run.ID))
		}
		if world.batches.countsCalls == 0 {
			t.Fatal("finishing a case must recount the batch")
		}
	})
	t.Run("blocked environment is accepted", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "blocked")
		recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusPassed, "passed")
		finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{
				Verdict: domain.VerdictBlocked, FailureClass: failureClassPointer(domain.FailureClassEnvironment),
				Reason: "host_not_allowed",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if finished.Status != domain.CaseRunStatusBlocked ||
			*finished.FailureClass != domain.FailureClassEnvironment {
			t.Fatalf("finished = %+v", finished)
		}
	})
	t.Run("inconclusive model budget is accepted", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "budget")
		recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusPassed, "passed")
		finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{
				Verdict: domain.VerdictInconclusive, FailureClass: failureClassPointer(domain.FailureClassModel),
				Reason: "budget_exhausted",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if finished.Status != domain.CaseRunStatusInconclusive ||
			*finished.FailureClass != domain.FailureClassModel {
			t.Fatalf("finished = %+v", finished)
		}
	})
	t.Run("failed cannot claim environment", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "x")
		recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusPassed, "passed")
		_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{
				Verdict: domain.VerdictFailed, FailureClass: failureClassPointer(domain.FailureClassEnvironment),
			},
		})
		wantToolError(t, err, "invalid_arguments", "invalid_failure_class")
	})
}

// recAudit captures the execution.case.finished audits of one recorder test and
// the execution.db_assert.executed events when its DBAssertExecuted method is
// used.
type recAudit struct {
	mu        sync.Mutex
	entries   []recAuditEntry
	dbEntries []DBAssertAuditEvent
	err       error
}

type recAuditEntry struct {
	run   domain.CaseRun
	actor int64
}

func (a *recAudit) CaseFinished(_ context.Context, run domain.CaseRun, actor int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, recAuditEntry{run: run, actor: actor})
	return a.err
}

func (a *recAudit) DBAssertExecuted(_ context.Context, event DBAssertAuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dbEntries = append(a.dbEntries, event)
	return a.err
}

func (a *recAudit) recorded() []recAuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]recAuditEntry(nil), a.entries...)
}

func (a *recAudit) dbAssertAudits() []DBAssertAuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]DBAssertAuditEvent(nil), a.dbEntries...)
}

// TestRecorderFinishCaseRecordsAudit pins that a committed terminal finish is
// audited with the stored run, that an audit failure never fails the finish, and
// that a recorder without an auditor stays safe.
func TestRecorderFinishCaseRecordsAudit(t *testing.T) {
	finishPassed := func(t *testing.T, recorder *CaseRecorder, world *recWorld) domain.CaseRun {
		t.Helper()
		run := world.seedRunningRun(recorderSnapshot())
		evidence := world.evidenceFor(run.ID, "audit")
		recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusPassed, "passed")
		finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
		})
		if err != nil {
			t.Fatalf("FinishCase: %v", err)
		}
		return finished
	}

	t.Run("terminal finish records the audit", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		audit := &recAudit{}
		recorder.deps.Audit = audit
		finished := finishPassed(t, recorder, world)

		entries := audit.recorded()
		if len(entries) != 1 {
			t.Fatalf("audit entries = %d, want 1", len(entries))
		}
		if entries[0].run.ID != finished.ID || entries[0].run.Status != domain.CaseRunStatusPassed {
			t.Fatalf("audited run = %+v, want stored passed run %d", entries[0].run, finished.ID)
		}
		if entries[0].actor != 0 {
			t.Fatalf("audit actor = %d, want the system actor 0", entries[0].actor)
		}
	})

	t.Run("nil auditor is safe", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		finished := finishPassed(t, recorder, world)
		if finished.Status != domain.CaseRunStatusPassed {
			t.Fatalf("status = %s, want passed", finished.Status)
		}
	})

	t.Run("audit failure is best effort", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		recorder.deps.Audit = &recAudit{err: errors.New("audit store is down")}
		finished := finishPassed(t, recorder, world)
		if finished.Status != domain.CaseRunStatusPassed {
			t.Fatalf("audit failure changed the finish: %+v", finished)
		}
	})
}

// TestRecorderRejectsDBAssertionThroughGenericPath pins T8's review fix (spec
// §9.5): only execution.db_assert writes db assertion results; the generic
// execution.assertion_record path must reject a snapshot db assertion so the
// agent cannot forge a passed db result. Skipping db_assert leaves the
// assertion unsubmitted, so a passed finish is also rejected.
func TestRecorderRejectsDBAssertionThroughGenericPath(t *testing.T) {
	recorder, world := newRecorderTest(t)
	snapshot := recorderSnapshot()
	snapshot.Assertions[0].Kind = "db"
	run := world.seedRunningRun(snapshot)
	evidence := world.evidenceFor(run.ID, "db")

	_, err := recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 20, Verdict: "passed", Actual: "1", EvidenceIDs: []int64{evidence.ID},
	})
	wantToolError(t, err, "invalid_arguments", "db_assert")
	if rows := world.assertions.rows[run.ID]; len(rows) != 0 {
		t.Fatalf("generic assertion_record wrote a db result: %+v", rows)
	}
	if _, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "ok", EvidenceIDs: []int64{evidence.ID},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
	})
	wantToolError(t, err, "invalid_arguments", "invalid_assertion_results")
}

// TestRecorderRejectsVisualAssertionThroughGenericPath pins the §9.3 analogue of
// the db rule: only execution.visual_assert writes a visual assertion result; the
// generic execution.assertion_record path must reject it so the agent cannot
// forge a passed visual comparison.
func TestRecorderRejectsVisualAssertionThroughGenericPath(t *testing.T) {
	recorder, world := newRecorderTest(t)
	snapshot := recorderSnapshot()
	snapshot.Assertions[0].Kind = "visual"
	snapshot.Assertions[0].Visual = &domain.CaseSnapshotVisual{
		Selector: "#receipt", Threshold: 0.01, ViewportWidth: 1280, ViewportHeight: 720,
		DeviceScaleFactor: 1,
	}
	run := world.seedRunningRun(snapshot)
	evidence := world.evidenceFor(run.ID, "visual")

	_, err := recorder.RecordAssertion(context.Background(), run.ID, AssertionRecordInput{
		AssertionID: 20, Verdict: "passed", Actual: "{}", EvidenceIDs: []int64{evidence.ID},
	})
	wantToolError(t, err, "invalid_arguments", "visual_assert")
	if rows := world.assertions.rows[run.ID]; len(rows) != 0 {
		t.Fatalf("generic assertion_record wrote a visual result: %+v", rows)
	}
}

func TestRecorderRejectsPausedAndTerminalSubmissions(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		run.Status = domain.CaseRunStatusPending
		world.runs.seed(run)
		_, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
			Seq: 1, Status: domain.StepStatusPassed, Actual: "ok",
		})
		wantToolError(t, err, "paused", "paused")
		_, err = recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{Verdict: domain.VerdictInconclusive,
				FailureClass: failureClassPointer(domain.FailureClassModel)},
		})
		wantToolError(t, err, "paused", "paused")
	})
	t.Run("terminal", func(t *testing.T) {
		recorder, world := newRecorderTest(t)
		run := world.seedRunningRun(recorderSnapshot())
		run.Status = domain.CaseRunStatusPassed
		world.runs.seed(run)
		_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
			Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
		})
		wantToolError(t, err, "invalid_arguments", "already terminal")
	})
}

func TestRecorderFinishCaseRejectsInterruptedRun(t *testing.T) {
	recorder, world := newRecorderTest(t)
	run := world.seedRunningRun(recorderSnapshot())
	interruptedStep := 2
	run.Status = domain.CaseRunStatusInterrupted
	run.FailureReason = "paused"
	run.CleanupRequired = true
	run.InterruptedStepSeq = &interruptedStep
	world.runs.seed(run)

	_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
	})
	wantToolError(t, err, "invalid_arguments", "interrupted")

	stored, getErr := world.runs.GetCaseRun(context.Background(), schedBatchID, run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status != domain.CaseRunStatusInterrupted {
		t.Fatalf("status = %s, want interrupted", stored.Status)
	}
	if !stored.CleanupRequired {
		t.Fatalf("a rejected finish must not clear cleanup_required: %+v", stored)
	}
	if stored.InterruptedStepSeq == nil || *stored.InterruptedStepSeq != 2 {
		t.Fatalf("a rejected finish must not clear the interrupted step: %+v", stored)
	}
	if got := world.events.count("run.finished"); got != 0 {
		t.Fatalf("run.finished events = %d, want 0 for a rejected finish", got)
	}
}

func TestRecorderFinishCaseRejectsInterruptRacingValidation(t *testing.T) {
	recorder, world := newRecorderTest(t)
	run := world.seedRunningRun(recorderSnapshot())
	evidence := world.evidenceFor(run.ID, "race")
	recordStepAndAssertion(t, recorder, run, evidence.ID, domain.StepStatusPassed, "passed")

	// The immediate pause commits while FinishCase is reading the recorded
	// subtree, after the initial status check.
	world.steps.onList = func() {
		current, err := world.runs.GetCaseRunByID(context.Background(), run.ID)
		if err != nil {
			t.Errorf("reload run: %v", err)
			return
		}
		current.Status = domain.CaseRunStatusInterrupted
		current.FailureReason = "paused"
		current.CleanupRequired = true
		world.runs.seed(current)
	}

	_, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
	})
	wantToolError(t, err, "invalid_arguments", "interrupted")

	stored, getErr := world.runs.GetCaseRun(context.Background(), schedBatchID, run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status != domain.CaseRunStatusInterrupted || !stored.CleanupRequired {
		t.Fatalf("racing finish overwrote the checkpoint: %+v", stored)
	}
	if got := world.events.count("run.finished"); got != 0 {
		t.Fatalf("run.finished events = %d, want 0 for a rejected finish", got)
	}
}

// ---------------------------------------------------------------------------
// CaseContext
// ---------------------------------------------------------------------------

func TestRecorderCaseContextReturnsCheckpointAndEnvironment(t *testing.T) {
	recorder, world := newRecorderTest(t)
	run := world.seedRunningRun(recorderSnapshot())
	interrupted := 2
	run.InterruptedStepSeq = &interrupted
	run.CleanupRequired = true
	world.runs.seed(run)
	world.steps.seed(domain.StepResult{
		ID: 1, BatchID: schedBatchID, CaseRunID: run.ID, Seq: 1,
		Status: domain.StepStatusPassed, EvidenceCount: 1,
	})
	world.steps.seed(domain.StepResult{
		ID: 2, BatchID: schedBatchID, CaseRunID: run.ID, Seq: 2,
		Status: domain.StepStatusRunning,
	})

	view, err := recorder.CaseContext(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.CompletedSeqs, []int{1}) {
		t.Fatalf("completed seqs = %v, want [1]", view.CompletedSeqs)
	}
	if view.InterruptedStepSeq == nil || *view.InterruptedStepSeq != 2 || !view.CleanupRequired {
		t.Fatalf("checkpoint flags = %+v", view)
	}
	if view.BaseURL != "http://127.0.0.1:13335" ||
		!reflect.DeepEqual(view.AllowedHosts, []string{"127.0.0.1"}) {
		t.Fatalf("target allowlist = %q %v", view.BaseURL, view.AllowedHosts)
	}
	if view.CaseRun.ID != run.ID || view.CaseRun.CaseNodeID != run.CaseNodeID {
		t.Fatalf("case run = %+v", view.CaseRun)
	}
}

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type recEvidenceRepo struct {
	mu     sync.Mutex
	rows   map[int64]domain.Evidence
	nextID int64
}

func newRecEvidenceRepo() *recEvidenceRepo {
	return &recEvidenceRepo{rows: map[int64]domain.Evidence{}}
}

func (e *recEvidenceRepo) seed(evidence domain.Evidence) domain.Evidence {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextID++
	evidence.ID = e.nextID
	if evidence.CreatedAt.IsZero() {
		evidence.CreatedAt = testNow
	}
	e.rows[evidence.ID] = evidence
	return evidence
}

func (e *recEvidenceRepo) Create(ctx context.Context, evidence domain.Evidence) (domain.Evidence, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rows[evidence.ID] = evidence
	return evidence, nil
}

func (e *recEvidenceRepo) Get(ctx context.Context, evidenceID int64) (domain.Evidence, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	evidence, ok := e.rows[evidenceID]
	if !ok {
		return domain.Evidence{}, ErrEvidenceNotFound
	}
	return evidence, nil
}

func (e *recEvidenceRepo) ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.Evidence, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]domain.Evidence, 0)
	for _, evidence := range e.rows {
		if evidence.CaseRunID != nil && *evidence.CaseRunID == caseRunID {
			out = append(out, evidence)
		}
	}
	return out, nil
}

type recRefRepo struct {
	mu   sync.Mutex
	refs []domain.EvidenceRef
}

func (r *recRefRepo) Create(ctx context.Context, ref domain.EvidenceRef) (domain.EvidenceRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refs = append(r.refs, ref)
	return ref, nil
}

func (r *recRefRepo) all() []domain.EvidenceRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.EvidenceRef(nil), r.refs...)
}

// wantToolError asserts err is an *agentdomain.ToolError with the given code and
// an optional message fragment.
func wantToolError(t *testing.T, err error, code, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want tool error %q", code)
	}
	var toolErr *agentdomain.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("error %v (%T) is not an *agentdomain.ToolError", err, err)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code = %q, want %q (message %q)", toolErr.Code, code, toolErr.Message)
	}
	if contains != "" && !strings.Contains(toolErr.Message, contains) {
		t.Fatalf("tool error message %q does not contain %q", toolErr.Message, contains)
	}
}
