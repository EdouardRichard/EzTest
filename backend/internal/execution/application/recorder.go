package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Recorder tool error codes. They are the spec §5.5 stable identifiers a
// corrective agent call must be able to branch on.
const (
	toolCodeInvalidArguments = "invalid_arguments"
	toolCodeNotFound         = "not_found"
	toolCodeInternal         = "internal"
	toolCodePaused           = "paused"
)

// CaseRunLocator resolves one case run by its id alone. CaseRunRepository reads
// are batch-scoped for tenant isolation, but the execution agent only knows the
// case run id carried by its invocation; the composition root supplies this
// lookup (for example a narrow by-id read over the same table).
type CaseRunLocator interface {
	GetCaseRunByID(ctx context.Context, caseRunID int64) (domain.CaseRun, error)
}

// CaseFinishedAuditor records the terminal outcome of one case run
// (spec §5.11). It is optional: a nil auditor disables the audit entry.
type CaseFinishedAuditor interface {
	CaseFinished(ctx context.Context, run domain.CaseRun, actor int64) error
}

// RecorderDeps are the ports of the structured-submission surface. Locator,
// CaseRuns, Steps, Assertions, Evidence, Batches, Versions and Sessions are
// required; Events and Audit are optional. DBQuery and DBCapture are the P7
// execution.db_assert ports: DBQuery resolves and executes the environment's
// read-only target, DBCapture persists the db_result evidence artifact.
type RecorderDeps struct {
	Locator    CaseRunLocator
	CaseRuns   CaseRunRepository
	Steps      StepRepository
	Assertions AssertionRepository
	Evidence   EvidenceRepository
	Refs       EvidenceRefRepository
	Batches    BatchRepository
	Versions   VersionPort
	Sessions   SessionPort
	Events     EventPublisher
	// DBQuery is the read-only database port of execution.db_assert.
	DBQuery DBQueryPort
	// DBCapture captures the db_result evidence artifact of execution.db_assert.
	// *EvidenceService satisfies it.
	DBCapture EvidenceCapturePort
	// Audit records execution.case.finished after the terminal finish commits.
	Audit CaseFinishedAuditor
	Clock Clock
	IDs   IDGenerator
}

// Recorder implements the execution agent's structured submission surface
// (spec §5.5): it never trusts the agent's order, ownership or evidence claims
// and re-validates each submission before it is persisted.
type CaseRecorder struct {
	deps RecorderDeps
	now  func() time.Time
}

// NewRecorder wires the recorder. Every rejection is an
// *agentdomain.ToolError with a stable code so the tool executor can report a
// correctable error to the model.
func NewRecorder(deps RecorderDeps) *CaseRecorder {
	if deps.Clock == nil {
		deps.Clock = wallClock{}
	}
	if deps.IDs == nil {
		deps.IDs = defaultIDGenerator()
	}
	return &CaseRecorder{deps: deps, now: deps.Clock.Now}
}

// CaseContext returns the pinned run, its checkpoint and the target allowlist
// for the execution agent (spec §5.5). No session credentials are included.
func (r *CaseRecorder) CaseContext(ctx context.Context, caseRunID int64) (CaseContextView, error) {
	if err := r.ready(); err != nil {
		return CaseContextView{}, err
	}
	run, err := r.loadCaseRun(ctx, caseRunID)
	if err != nil {
		return CaseContextView{}, err
	}
	view := CaseContextView{
		CaseRun:            run,
		CompletedSeqs:      []int{},
		CompletedSteps:     []CaseContextCompletedStep{},
		RemainingSteps:     append([]domain.CaseSnapshotStep(nil), run.Snapshot.Steps...),
		InterruptedStepSeq: run.InterruptedStepSeq,
		CleanupRequired:    run.CleanupRequired,
	}
	if r.deps.Steps != nil {
		steps, err := r.deps.Steps.ListByCaseRun(ctx, caseRunID)
		if err != nil {
			return CaseContextView{}, recorderToolError(toolCodeInternal, "read case checkpoint: %v", err)
		}
		completed, nextSeq := domain.Checkpoint(steps)
		view.CompletedSeqs = completed
		view.CompletedSteps = completedCheckpointSteps(steps, completed)
		view.RemainingSteps = remainingCheckpointSteps(run.Snapshot.Steps, nextSeq)
	}
	if r.deps.Versions == nil || r.deps.Sessions == nil {
		return view, nil
	}
	version, err := r.deps.Versions.GetVersion(ctx, run.VersionID)
	if err != nil {
		return CaseContextView{}, recorderToolError(toolCodeInternal, "read version %d: %v", run.VersionID, err)
	}
	if version.EnvironmentID == nil {
		return CaseContextView{}, recorderToolError(toolCodeInternal,
			"version %d is not bound to an environment", run.VersionID)
	}
	environment, err := r.deps.Sessions.Environment(ctx, *version.EnvironmentID)
	if err != nil {
		return CaseContextView{}, recorderToolError(toolCodeInternal,
			"read environment %d: %v", *version.EnvironmentID, err)
	}
	view.BaseURL = environment.BaseURL
	view.AllowedHosts = append([]string(nil), environment.AllowedHosts...)
	return view, nil
}

// completedCheckpointSteps projects the recorded results of the completed
// checkpoint prefix, carrying the actual result and evidence count the resumed
// agent needs (spec §5.5).
func completedCheckpointSteps(steps []domain.StepResult, completed []int) []CaseContextCompletedStep {
	bySeq := make(map[int]domain.StepResult, len(steps))
	for _, step := range steps {
		if _, exists := bySeq[step.Seq]; !exists {
			bySeq[step.Seq] = step
		}
	}
	results := make([]CaseContextCompletedStep, 0, len(completed))
	for _, seq := range completed {
		step := bySeq[seq]
		results = append(results, CaseContextCompletedStep{
			Seq:           seq,
			Status:        step.Status,
			Actual:        step.Actual,
			EvidenceCount: step.EvidenceCount,
		})
	}
	return results
}

// remainingCheckpointSteps returns the snapshot steps the resumed run still has
// to execute, starting at the checkpoint's next seq.
func remainingCheckpointSteps(steps []domain.CaseSnapshotStep, nextSeq int) []domain.CaseSnapshotStep {
	remaining := make([]domain.CaseSnapshotStep, 0, len(steps))
	for _, step := range steps {
		if step.Seq >= nextSeq {
			remaining = append(remaining, step)
		}
	}
	return remaining
}

// RecordStep records one step result. Seq must be the next unfinished step: no
// skipping, rewinding or repeating (spec §5.5). Failed requires an actual;
// every referenced evidence id must belong to the case run.
func (r *CaseRecorder) RecordStep(ctx context.Context, caseRunID int64, in StepRecordInput) (domain.StepResult, error) {
	if err := r.ready(); err != nil {
		return domain.StepResult{}, err
	}
	if in.Seq <= 0 {
		return domain.StepResult{}, recorderToolError(toolCodeInvalidArguments, "step seq must be positive")
	}
	if in.Status != domain.StepStatusPassed && in.Status != domain.StepStatusFailed {
		return domain.StepResult{}, recorderToolError(toolCodeInvalidArguments,
			"step status %q must be passed or failed", in.Status)
	}
	if in.Status == domain.StepStatusFailed && strings.TrimSpace(in.Actual) == "" {
		return domain.StepResult{}, recorderToolError(toolCodeInvalidArguments,
			"a failed step requires the actual result or failure reason")
	}

	run, err := r.loadCaseRun(ctx, caseRunID)
	if err != nil {
		return domain.StepResult{}, err
	}
	if err := requireRecordableRun(run); err != nil {
		return domain.StepResult{}, err
	}

	steps, err := r.deps.Steps.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return domain.StepResult{}, recorderToolError(toolCodeInternal, "read recorded steps: %v", err)
	}
	_, nextSeq := domain.Checkpoint(steps)
	if in.Seq != nextSeq {
		return domain.StepResult{}, recorderToolError(toolCodeInvalidArguments,
			"step %d is not the next unfinished step %d", in.Seq, nextSeq)
	}

	snapshot, ok := findSnapshotStep(run.Snapshot, in.Seq)
	if !ok {
		return domain.StepResult{}, recorderToolError(toolCodeInvalidArguments,
			"step %d is not part of case run %d snapshot", in.Seq, caseRunID)
	}
	evidenceCount, err := r.validateEvidence(ctx, run, in.EvidenceIDs)
	if err != nil {
		return domain.StepResult{}, err
	}

	id, err := nextID(r.deps.IDs)
	if err != nil {
		return domain.StepResult{}, recorderToolError(toolCodeInternal, "mint step result id: %v", err)
	}
	now := r.now()
	step := domain.StepResult{
		ID:            id,
		BatchID:       run.BatchID,
		CaseRunID:     run.ID,
		Seq:           in.Seq,
		Action:        snapshot.Action,
		Expected:      snapshot.Expected,
		Actual:        in.Actual,
		Status:        in.Status,
		EvidenceCount: evidenceCount,
		StartedAt:     timePointer(now),
		FinishedAt:    timePointer(now),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	stored, err := r.deps.Steps.Upsert(ctx, step)
	if err != nil {
		return domain.StepResult{}, recorderToolError(toolCodeInternal, "persist step %d: %v", in.Seq, err)
	}

	r.createRefs(ctx, stored.ID, in.EvidenceIDs, "step_result")
	r.publish(ctx, Event{
		Type:      "step.finished",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		StepID:    stored.ID,
		Summary:   "step finished",
		Detail: map[string]any{
			"case_run_id":    eventID(run.ID),
			"seq":            stored.Seq,
			"status":         string(stored.Status),
			"evidence_count": stored.EvidenceCount,
		},
		Refs: EventRefs{EvidenceIDs: copyIDs(in.EvidenceIDs), CaseIDs: []int64{run.CaseNodeID}},
	})
	return stored, nil
}

// RecordAssertion records one assertion result. The assertion must belong to the
// case snapshot, may be submitted once, and passed/failed require at least one
// evidence id of this case run (spec §5.5).
func (r *CaseRecorder) RecordAssertion(ctx context.Context, caseRunID int64, in AssertionRecordInput) (domain.AssertionResult, error) {
	if err := r.ready(); err != nil {
		return domain.AssertionResult{}, err
	}
	verdict := strings.ToLower(strings.TrimSpace(in.Verdict))
	if verdict != "passed" && verdict != "failed" {
		return domain.AssertionResult{}, recorderToolError(toolCodeInvalidArguments,
			"assertion verdict %q must be passed or failed", in.Verdict)
	}
	if len(in.EvidenceIDs) == 0 {
		return domain.AssertionResult{}, recorderToolError(toolCodeInvalidArguments,
			"assertion %d requires at least one evidence id", in.AssertionID)
	}

	run, err := r.loadCaseRun(ctx, caseRunID)
	if err != nil {
		return domain.AssertionResult{}, err
	}
	if err := requireRecordableRun(run); err != nil {
		return domain.AssertionResult{}, err
	}

	assertion, ok := findSnapshotAssertion(run.Snapshot, in.AssertionID)
	if !ok {
		return domain.AssertionResult{}, recorderToolError(toolCodeInvalidArguments,
			"assertion %d is not part of case run %d snapshot", in.AssertionID, caseRunID)
	}
	// A db assertion result is server-owned (spec §9.5): only execution.db_assert
	// may write it. Rejecting the generic path closes the forged-passed hole.
	if strings.EqualFold(strings.TrimSpace(assertion.Kind), "db") {
		return domain.AssertionResult{}, recorderToolError(toolCodeInvalidArguments,
			"assertion %d is a db assertion and must be recorded by execution.db_assert (assertion_already_recorded)",
			in.AssertionID)
	}
	existing, err := r.deps.Assertions.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return domain.AssertionResult{}, recorderToolError(toolCodeInternal, "read recorded assertions: %v", err)
	}
	for _, recorded := range existing {
		if recorded.AssertionID == in.AssertionID {
			return domain.AssertionResult{}, recorderToolError(toolCodeInvalidArguments,
				"assertion %d was already submitted", in.AssertionID)
		}
	}
	if _, err := r.validateEvidence(ctx, run, in.EvidenceIDs); err != nil {
		return domain.AssertionResult{}, err
	}

	stepResultID := r.stepResultIDForSeq(ctx, caseRunID, assertion.StepSeq)
	id, err := nextID(r.deps.IDs)
	if err != nil {
		return domain.AssertionResult{}, recorderToolError(toolCodeInternal, "mint assertion result id: %v", err)
	}
	now := r.now()
	result := domain.AssertionResult{
		ID:           id,
		BatchID:      run.BatchID,
		CaseRunID:    run.ID,
		StepResultID: stepResultID,
		AssertionID:  assertion.ID,
		Kind:         assertion.Kind,
		Target:       assertion.Target,
		Expected:     assertion.Expected,
		Actual:       in.Actual,
		Verdict:      verdict,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	stored, err := r.deps.Assertions.Upsert(ctx, result)
	if err != nil {
		return domain.AssertionResult{}, recorderToolError(toolCodeInternal, "persist assertion %d: %v", in.AssertionID, err)
	}
	r.createRefs(ctx, stored.ID, in.EvidenceIDs, "assertion_result")
	return stored, nil
}

// FinishCase validates and stores the terminal decision of a case run. The
// domain validator owns the verdict/class matrix, the db-assertion rule and the
// evidence requirement (spec §5.8); the recorder adds ownership checks, the
// atomic recount and the finish events.
func (r *CaseRecorder) FinishCase(ctx context.Context, caseRunID int64, in FinishInput) (domain.CaseRun, error) {
	if err := r.ready(); err != nil {
		return domain.CaseRun{}, err
	}
	run, err := r.loadCaseRun(ctx, caseRunID)
	if err != nil {
		return domain.CaseRun{}, err
	}
	// A finish is a running -> terminal CAS. An interrupted run is a checkpoint
	// that only resume may turn into pending; it must never be overwritten with a
	// verdict and its cleanup/interrupted-step flags must be preserved.
	if run.Status != domain.CaseRunStatusRunning {
		return domain.CaseRun{}, requireRecordableRun(run)
	}
	if _, err := r.validateEvidence(ctx, run, in.EvidenceIDs); err != nil {
		return domain.CaseRun{}, err
	}
	steps, err := r.deps.Steps.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return domain.CaseRun{}, recorderToolError(toolCodeInternal, "read recorded steps: %v", err)
	}
	assertions, err := r.deps.Assertions.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return domain.CaseRun{}, recorderToolError(toolCodeInternal, "read recorded assertions: %v", err)
	}
	// The finish evidence gate counts every artifact of the case run: step,
	// assertion and db_result evidence all satisfy it (spec §6.6.2).
	evidenceCount, err := r.caseEvidenceCount(ctx, caseRunID)
	if err != nil {
		return domain.CaseRun{}, err
	}
	if err := domain.ValidateFinish(run.Snapshot, steps, assertions, in.Decision, evidenceCount); err != nil {
		return domain.CaseRun{}, recorderToolErrorFromReason(err)
	}
	// A pause can land while the submission above is validated. Re-read before
	// the write so an interrupted checkpoint is rejected instead of overwritten;
	// the repository CAS remains the final arbiter.
	current, err := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID)
	if err != nil {
		return domain.CaseRun{}, recorderToolError(toolCodeInternal,
			"re-read case run %d before finish: %v", caseRunID, err)
	}
	if current.Status != domain.CaseRunStatusRunning {
		return domain.CaseRun{}, requireRecordableRun(current)
	}

	now := r.now()
	finished := run
	finished.Status = caseRunStatusForVerdict(in.Decision.Verdict)
	finished.FailureClass = copyFailureClass(in.Decision.FailureClass)
	finished.FailureReason = finishReason(in.Decision)
	finished.Summary = in.Decision.Reason
	finished.CleanupRequired = false
	finished.InterruptedStepSeq = nil
	finished.FinishedAt = timePointer(now)
	finished.UpdatedAt = now
	if run.StartedAt != nil {
		finished.DurationMS = nonNegativeMillis(now.Sub(*run.StartedAt))
	}

	stored, err := r.deps.CaseRuns.FinishCaseRun(ctx, finished)
	if err != nil {
		if errors.Is(err, domain.ErrCaseRunTerminal) {
			return domain.CaseRun{}, recorderToolError(toolCodeInvalidArguments,
				"case run %d is already terminal", caseRunID)
		}
		return domain.CaseRun{}, recorderToolError(toolCodeInternal, "persist case finish: %v", err)
	}

	if r.deps.Batches != nil {
		if _, err := r.deps.Batches.UpdateCounts(ctx, run.BatchID); err != nil {
			slog.WarnContext(ctx, "execution: recount batch after case finish failed",
				"batch_id", run.BatchID, "case_run_id", run.ID, "error", err)
		}
	}
	r.publishFinish(ctx, stored, evidenceCount, in.EvidenceIDs)
	r.auditFinish(ctx, stored)
	return stored, nil
}

// auditFinish records execution.case.finished after the terminal row committed.
// Auditing is best-effort: the finish is authoritative and already durable, so a
// failed audit write is logged and never fails the submission.
func (r *CaseRecorder) auditFinish(ctx context.Context, run domain.CaseRun) {
	if r.deps.Audit == nil {
		return
	}
	if err := r.deps.Audit.CaseFinished(ctx, run, 0); err != nil {
		slog.WarnContext(ctx, "execution: audit case finish failed",
			"case_run_id", run.ID, "error", err)
	}
}

// publishFinish emits the terminal case events after the finish row committed.
// evidenceCount is the case run's committed evidence total (step, assertion and
// db_result artifacts), the same total the finish gate validated.
func (r *CaseRecorder) publishFinish(ctx context.Context, run domain.CaseRun, evidenceCount int, evidenceIDs []int64) {
	detail := map[string]any{
		"case_run_id": eventID(run.ID),
		"status":      string(run.Status),
		"attempt":     run.Attempt,
		"duration_ms": run.DurationMS,
	}
	if run.FailureClass != nil {
		detail["failure_class"] = string(*run.FailureClass)
	}
	if run.FailureReason != "" {
		detail["failure_reason"] = run.FailureReason
	}
	detail["evidence_count"] = evidenceCount
	refs := EventRefs{
		EvidenceIDs: copyIDs(evidenceIDs),
		CaseIDs:     []int64{run.CaseNodeID},
	}
	r.publish(ctx, Event{
		Type:      "run.finished",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case run finished",
		Detail:    detail,
		Refs:      refs,
	})
	r.publish(ctx, Event{
		Type:      "case.status_changed",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case status changed",
		Detail:    detail,
		Refs:      refs,
	})
}

// ready rejects a mis-wired recorder before it touches an agent submission.
func (r *CaseRecorder) ready() error {
	if r == nil {
		return recorderToolError(toolCodeInternal, "recorder is not configured")
	}
	switch {
	case r.deps.Locator == nil:
		return recorderToolError(toolCodeInternal, "case run locator is not configured")
	case r.deps.CaseRuns == nil:
		return recorderToolError(toolCodeInternal, "case run repository is not configured")
	case r.deps.Steps == nil:
		return recorderToolError(toolCodeInternal, "step repository is not configured")
	case r.deps.Assertions == nil:
		return recorderToolError(toolCodeInternal, "assertion repository is not configured")
	}
	return nil
}

// loadCaseRun resolves a run by id and maps the missing-row sentinel to a tool
// error.
func (r *CaseRecorder) loadCaseRun(ctx context.Context, caseRunID int64) (domain.CaseRun, error) {
	if r == nil || r.deps.Locator == nil {
		return domain.CaseRun{}, recorderToolError(toolCodeInternal, "case run locator is not configured")
	}
	if caseRunID <= 0 {
		return domain.CaseRun{}, recorderToolError(toolCodeInvalidArguments, "case run id must be positive")
	}
	run, err := r.deps.Locator.GetCaseRunByID(ctx, caseRunID)
	if err != nil {
		if errors.Is(err, ErrCaseRunNotFound) {
			return domain.CaseRun{}, recorderToolError(toolCodeNotFound, "case run %d not found", caseRunID)
		}
		return domain.CaseRun{}, recorderToolError(toolCodeInternal, "read case run %d: %v", caseRunID, err)
	}
	return run, nil
}

// requireRecordableRun rejects submissions against a paused or terminal run.
func requireRecordableRun(run domain.CaseRun) error {
	switch run.Status {
	case domain.CaseRunStatusRunning:
		return nil
	case domain.CaseRunStatusPending:
		return recorderToolError(toolCodePaused, "case run %d is paused", run.ID)
	case domain.CaseRunStatusInterrupted:
		return recorderToolError(toolCodeInvalidArguments,
			"case run %d is interrupted and must be resumed before recording", run.ID)
	default:
		return recorderToolError(toolCodeInvalidArguments,
			"case run %d is already terminal (%s)", run.ID, run.Status)
	}
}

// validateEvidence resolves every evidence id, requires that it belongs to the
// same case run, and returns the distinct count. Evidence is append-only, so a
// missing row means the id was never captured.
func (r *CaseRecorder) validateEvidence(ctx context.Context, run domain.CaseRun, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if r.deps.Evidence == nil {
		return 0, recorderToolError(toolCodeInternal, "evidence repository is not configured")
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return 0, recorderToolError(toolCodeInvalidArguments, "evidence id %d must be positive", id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		evidence, err := r.deps.Evidence.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrEvidenceNotFound) {
				return 0, recorderToolError(toolCodeNotFound, "evidence %d not found", id)
			}
			return 0, recorderToolError(toolCodeInternal, "read evidence %d: %v", id, err)
		}
		if evidence.BatchID != run.BatchID {
			return 0, recorderToolError(toolCodeInvalidArguments,
				"evidence %d belongs to another batch", id)
		}
		if evidence.CaseRunID == nil || *evidence.CaseRunID != run.ID {
			return 0, recorderToolError(toolCodeInvalidArguments,
				"evidence %d does not belong to case run %d", id, run.ID)
		}
		seen[id] = struct{}{}
	}
	return len(seen), nil
}

// caseEvidenceCount returns the number of evidence artifacts committed for the
// case run. It is the finish gate's evidence total: the P6 step count is a
// subset, and P7 db_result/assertion evidence must count too (spec §6.6.2).
func (r *CaseRecorder) caseEvidenceCount(ctx context.Context, caseRunID int64) (int, error) {
	if r.deps.Evidence == nil {
		return 0, nil
	}
	rows, err := r.deps.Evidence.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return 0, recorderToolError(toolCodeInternal, "count case run evidence: %v", err)
	}
	return len(rows), nil
}

// createRefs links the submitted evidence to the recorded result. The
// repository makes a repeated link idempotent; a failure is enrichment loss and
// is only logged (the evidence row is already durable).
func (r *CaseRecorder) createRefs(ctx context.Context, refID int64, evidenceIDs []int64, kind string) {
	if r.deps.Refs == nil || refID <= 0 {
		return
	}
	for _, evidenceID := range evidenceIDs {
		if evidenceID <= 0 {
			continue
		}
		if _, err := r.deps.Refs.Create(ctx, domain.EvidenceRef{
			EvidenceID: evidenceID,
			RefKind:    kind,
			RefID:      refID,
		}); err != nil {
			slog.WarnContext(ctx, "execution: evidence ref creation failed",
				"evidence_id", evidenceID, "ref_kind", kind, "ref_id", refID, "error", err)
		}
	}
}

// stepResultIDForSeq returns the recorded step result of a snapshot step, when
// it exists, so an assertion can point at it.
func (r *CaseRecorder) stepResultIDForSeq(ctx context.Context, caseRunID int64, seq int) *int64 {
	if r.deps.Steps == nil || seq <= 0 {
		return nil
	}
	steps, err := r.deps.Steps.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return nil
	}
	for _, step := range steps {
		if step.Seq == seq && step.ID > 0 {
			return int64Pointer(step.ID)
		}
	}
	return nil
}

// publish emits one execution event, logging and swallowing publisher errors.
func (r *CaseRecorder) publish(ctx context.Context, event Event) {
	if r.deps.Events == nil {
		return
	}
	if err := r.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "execution event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"batch_id", event.BatchID,
			"error", err,
		)
	}
}

// findSnapshotStep finds a snapshot step by seq.
func findSnapshotStep(snapshot domain.CaseSnapshot, seq int) (domain.CaseSnapshotStep, bool) {
	for _, step := range snapshot.Steps {
		if step.Seq == seq {
			return step, true
		}
	}
	return domain.CaseSnapshotStep{}, false
}

// findSnapshotAssertion finds a snapshot assertion by id.
func findSnapshotAssertion(snapshot domain.CaseSnapshot, assertionID int64) (domain.CaseSnapshotAssertion, bool) {
	for _, assertion := range snapshot.Assertions {
		if assertion.ID == assertionID {
			return assertion, true
		}
	}
	return domain.CaseSnapshotAssertion{}, false
}

// finishReason is the audit reason stored on the case run. The agent's stated
// reason wins; otherwise the failure class names a non-passing outcome. A
// passed case has no failure reason.
func finishReason(decision domain.FinishDecision) string {
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		return reason
	}
	if decision.Verdict == domain.VerdictPassed {
		return ""
	}
	if decision.FailureClass != nil {
		return string(*decision.FailureClass)
	}
	return string(decision.Verdict)
}

// copyFailureClass copies an optional failure class so the stored run cannot be
// mutated through the input pointer.
func copyFailureClass(class *domain.FailureClass) *domain.FailureClass {
	if class == nil {
		return nil
	}
	copied := *class
	return &copied
}

// copyIDs copies an id slice for an event payload.
func copyIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	return append([]int64(nil), ids...)
}

// recorderToolError builds a typed tool failure.
func recorderToolError(code, format string, args ...any) error {
	return &agentdomain.ToolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// recorderToolErrorFromReason preserves the domain validator's stable reason in
// the model-facing message and maps it to invalid_arguments.
func recorderToolErrorFromReason(err error) error {
	var reasonErr *domain.ReasonError
	if errors.As(err, &reasonErr) {
		message := reasonErr.Message
		if reasonErr.Reason != "" {
			message = reasonErr.Reason + ": " + message
		}
		return &agentdomain.ToolError{Code: toolCodeInvalidArguments, Message: message}
	}
	return &agentdomain.ToolError{Code: toolCodeInternal, Message: err.Error()}
}

// ensure Recorder satisfies the frozen interface.
var _ Recorder = (*CaseRecorder)(nil)
