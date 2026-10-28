package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Batch service defaults and stable identifiers.
const (
	// versionStatusExecuting is the only version status that may start a batch
	// (spec §5.1). The execution domain intentionally does not depend on the
	// version context's constants.
	versionStatusExecuting = "executing"
	// DefaultMaxCasesPerBatch is the per-batch case cap used when no
	// EZTEST_EXEC_MAX_CASES_PER_BATCH value is wired.
	DefaultMaxCasesPerBatch = 200
	// pauseReasonUser is stored on run_batch.pause_reason for an explicit user
	// pause. Crash recovery uses its own reason (restart).
	pauseReasonUser = "user"
	// caseRunPageSize bounds the internal scan used by Cancel.
	caseRunPageSize = 200
)

// BatchServiceDeps are the ports of the batch use cases. Versions, Mindmap,
// Batches, CaseRuns and IDs are required; Events and Clock may be nil.
type BatchServiceDeps struct {
	Versions VersionPort
	Mindmap  MindmapPort
	Batches  BatchRepository
	CaseRuns CaseRunRepository
	Events   EventPublisher
	Clock    Clock
	IDs      IDGenerator
	// MaxCases is the per-batch case cap (EZTEST_EXEC_MAX_CASES_PER_BATCH).
	MaxCases int
}

// BatchService owns the run_batch lifecycle: creation with snapshot pinning,
// reads, pause/resume/cancel and manual rerun (spec §5.1-§5.4, §6.2). It only
// orchestrates: execution, evidence and scheduling live in their own services.
type BatchService struct {
	deps BatchServiceDeps
	now  func() time.Time
}

// NewBatchService wires the batch use cases to their ports.
func NewBatchService(deps BatchServiceDeps) *BatchService {
	if deps.MaxCases <= 0 {
		deps.MaxCases = DefaultMaxCasesPerBatch
	}
	if deps.IDs == nil {
		deps.IDs = defaultIDGenerator()
	}
	now := time.Now
	if deps.Clock != nil {
		now = deps.Clock.Now
	}
	return &BatchService{deps: deps, now: now}
}

// Create starts a batch for an executing, environment-bound version
// (spec §5.1, §4.3). It validates every gate, pins the baseline hash and the
// immutable per-case snapshots, allocates the version's next batch number and
// then writes the batch and its pending runs in one repository call before
// publishing batch.created and batch.started. Allocating before the insert
// means a committed batch always carries a batch_no the version already
// recorded; if the insert fails the number is handed back (when it is still the
// newest) and the original error is returned, so a failure can never leave a
// batch with an unrecorded number or wedge future batches. The executor health
// probe deliberately does not live here: it is an HTTP-layer gate (T19).
//
// A nil or empty caseIDs selects every baseline case. Explicit ids must all be
// baseline cases; duplicates are collapsed.
func (s *BatchService) Create(ctx context.Context, versionID, actor int64, caseIDs []int64) (*domain.RunBatch, error) {
	if err := s.requireCreateDeps(); err != nil {
		return nil, err
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if version.Status != versionStatusExecuting {
		return nil, newReasonError("version_not_executable",
			fmt.Sprintf("version %d is %s, execution requires executing", versionID, version.Status))
	}
	if version.SystemID == nil || version.EnvironmentID == nil {
		return nil, newReasonError("environment_required",
			fmt.Sprintf("version %d must be bound to a system and an environment", versionID))
	}

	baseline, err := s.deps.Mindmap.Baseline(ctx, versionID)
	if err != nil {
		if errors.Is(err, ErrBaselineRequired) {
			return nil, newReasonError("baseline_required",
				fmt.Sprintf("version %d has no case baseline", versionID))
		}
		return nil, err
	}
	if baseline.Hash == "" {
		return nil, newReasonError("baseline_required",
			fmt.Sprintf("version %d has no case baseline", versionID))
	}

	selected, err := selectCaseIDs(caseIDs, baseline.CaseIDs)
	if err != nil {
		return nil, newInvalidReasonError("invalid_case_ids", err.Error())
	}
	if len(selected) == 0 {
		return nil, newReasonError("no_cases",
			fmt.Sprintf("version %d baseline selects no cases", versionID))
	}
	if len(selected) > s.deps.MaxCases {
		return nil, newInvalidReasonError("too_many_cases",
			fmt.Sprintf("batch selects %d cases; the limit is %d", len(selected), s.deps.MaxCases))
	}

	views, err := s.deps.Mindmap.SnapshotCases(ctx, versionID, selected)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCaseIDs) {
			return nil, newInvalidReasonError("invalid_case_ids", err.Error())
		}
		return nil, err
	}

	now := s.now()
	batchID, err := nextID(s.deps.IDs)
	if err != nil {
		return nil, err
	}
	runs, err := s.buildRuns(batchID, versionID, views, selected, baseline.Hash, now)
	if err != nil {
		return nil, err
	}

	batch := domain.RunBatch{
		ID:                 batchID,
		VersionID:          versionID,
		Status:             domain.BatchStatusRunning,
		Paused:             false,
		PauseMode:          domain.PauseModeGraceful,
		BaselineHash:       baseline.Hash,
		CodeBaselineCommit: copyStringPointer(baseline.CodeCommit),
		CaseCount:          len(runs),
		CreatedBy:          actor,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	startedAt := now
	batch.StartedAt = &startedAt

	// Record the number before the insert. AllocateBatchNo is the atomic
	// version-side increment, so the unique (version_id, batch_no) index can
	// only reject a genuinely concurrent creator, never our own write.
	allocated, err := s.deps.Versions.AllocateBatchNo(ctx, versionID)
	if err != nil {
		return nil, err
	}
	batch.BatchNo = allocated

	created, err := s.deps.Batches.CreateBatchWithRuns(ctx, batch, runs)
	if err != nil {
		// Hand the number back so a failed create cannot wedge later batches.
		s.releaseBatchNo(ctx, versionID, allocated)
		if errors.Is(err, domain.ErrBatchActive) {
			return nil, newReasonError("batch_active",
				fmt.Sprintf("version %d already has an active batch", versionID))
		}
		// Anything else (including a repository conflict) is not an active
		// batch: return it unchanged rather than mislabelling it batch_active.
		return nil, err
	}

	s.publish(ctx, Event{
		Type:      "batch.created",
		VersionID: versionID,
		BatchID:   created.ID,
		Summary:   "batch created",
		Detail: map[string]any{
			"batch_no":      created.BatchNo,
			"case_count":    created.CaseCount,
			"baseline_hash": created.BaselineHash,
			"status":        string(created.Status),
		},
	})
	s.publish(ctx, Event{
		Type:      "batch.started",
		VersionID: versionID,
		BatchID:   created.ID,
		Summary:   "batch started",
		Detail: map[string]any{
			"batch_no":   created.BatchNo,
			"case_count": created.CaseCount,
			"status":     string(created.Status),
		},
	})
	return &created, nil
}

// Get returns one batch of versionID. A missing or foreign batch is
// ErrBatchNotFound.
func (s *BatchService) Get(ctx context.Context, versionID, batchID int64) (*domain.RunBatch, error) {
	if s.deps.Batches == nil {
		return nil, errors.New("execution: batch repository is not configured")
	}
	batch, err := s.deps.Batches.GetBatch(ctx, versionID, batchID)
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

// List returns one page of batches newest first. beforeSeq is the exclusive id
// cursor (the shared interface name predates the id keyset); zero starts at the
// newest batch.
func (s *BatchService) List(ctx context.Context, versionID int64, beforeSeq int64, limit int) ([]domain.RunBatch, bool, error) {
	if s.deps.Batches == nil {
		return nil, false, errors.New("execution: batch repository is not configured")
	}
	return s.deps.Batches.ListBatches(ctx, versionID, beforeSeq, limit)
}

// Pause pauses an active batch in the requested mode (spec §5.4). The batch
// keeps status running (it still owns the version's active slot); only the
// paused flag, the pause mode and the version mirror change. A terminal batch is
// batch_finished, a paused batch is already_paused.
func (s *BatchService) Pause(ctx context.Context, versionID, batchID int64, mode domain.PauseMode, actor int64) (*domain.RunBatch, error) {
	if mode == "" {
		mode = domain.PauseModeGraceful
	}
	if mode != domain.PauseModeGraceful && mode != domain.PauseModeImmediate {
		return nil, newInvalidReasonError("invalid_pause_mode",
			fmt.Sprintf("pause mode %q must be graceful or immediate", mode))
	}
	if s.deps.Batches == nil || s.deps.Versions == nil {
		return nil, errors.New("execution: pause dependencies are not configured")
	}
	batch, err := s.deps.Batches.GetBatch(ctx, versionID, batchID)
	if err != nil {
		return nil, err
	}
	if !domain.CanPause(batch.Status) {
		return nil, newReasonError("batch_finished",
			fmt.Sprintf("batch %d is %s and cannot be paused", batchID, batch.Status))
	}
	// Check the terminal state first: a finished batch that still carries a
	// stale paused flag is batch_finished, not already_paused (spec §5.4).
	if batch.Paused {
		return nil, newReasonError("already_paused",
			fmt.Sprintf("batch %d is already paused", batchID))
	}

	updated, err := s.deps.Batches.SetPaused(ctx, batchID, true, mode, pauseReasonUser)
	if err != nil {
		return nil, mapBatchStateError(err)
	}
	if err := s.deps.Versions.MarkPaused(ctx, versionID, true, string(mode), pauseReasonUser, actor); err != nil {
		// Best-effort rollback so the batch and the version mirror cannot
		// disagree after a failed pause.
		_, _ = s.deps.Batches.SetPaused(ctx, batchID, false, mode, "")
		return nil, err
	}
	s.publish(ctx, Event{
		Type:      "batch.paused",
		VersionID: versionID,
		BatchID:   batchID,
		Summary:   "batch paused",
		Detail: map[string]any{
			"mode":     string(mode),
			"paused":   true,
			"actor_id": actor,
		},
	})
	return &updated, nil
}

// Resume clears the pause flag of a paused, active batch and its version mirror
// (spec §5.4). Only a paused batch may resume: a finished batch is
// batch_finished, an unpaused one is not_paused.
func (s *BatchService) Resume(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error) {
	if s.deps.Batches == nil || s.deps.Versions == nil {
		return nil, errors.New("execution: resume dependencies are not configured")
	}
	batch, err := s.deps.Batches.GetBatch(ctx, versionID, batchID)
	if err != nil {
		return nil, err
	}
	if !domain.CanPause(batch.Status) {
		return nil, newReasonError("batch_finished",
			fmt.Sprintf("batch %d is %s and cannot be resumed", batchID, batch.Status))
	}
	if !batch.Paused {
		return nil, newReasonError("not_paused",
			fmt.Sprintf("batch %d is not paused", batchID))
	}
	mode := batch.PauseMode
	if mode == "" {
		mode = domain.PauseModeGraceful
	}
	updated, err := s.deps.Batches.SetPaused(ctx, batchID, false, mode, "")
	if err != nil {
		return nil, mapBatchStateError(err)
	}
	if err := s.deps.Versions.MarkPaused(ctx, versionID, false, string(mode), "", actor); err != nil {
		_, _ = s.deps.Batches.SetPaused(ctx, batchID, true, mode, batch.PauseReason)
		return nil, err
	}
	s.publish(ctx, Event{
		Type:      "batch.resumed",
		VersionID: versionID,
		BatchID:   batchID,
		Summary:   "batch resumed",
		Detail: map[string]any{
			"mode":     string(mode),
			"paused":   false,
			"actor_id": actor,
		},
	})
	return &updated, nil
}

// Cancel terminates an active batch (spec §5.4): pending runs become skipped,
// running runs become interrupted with cleanup_required, the tally is refreshed
// and the batch becomes cancelled. A paused batch has its pause flag and the
// version mirror cleared before the batch turns terminal, so neither the batch
// nor the version stays paused forever. The version status deliberately stays
// executing so a new batch can be created. Lease/context release belongs to the
// scheduler.
func (s *BatchService) Cancel(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error) {
	if s.deps.Batches == nil || s.deps.CaseRuns == nil || s.deps.Versions == nil {
		return nil, errors.New("execution: cancel dependencies are not configured")
	}
	batch, err := s.deps.Batches.GetBatch(ctx, versionID, batchID)
	if err != nil {
		return nil, err
	}
	if !domain.CanPause(batch.Status) {
		return nil, newReasonError("batch_finished",
			fmt.Sprintf("batch %d is %s and cannot be cancelled", batchID, batch.Status))
	}

	now := s.now()
	runs, err := s.listAllCaseRuns(ctx, batchID)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		switch runs[i].Status {
		case domain.CaseRunStatusPending, domain.CaseRunStatusRunning:
			to := domain.CaseRunStatusSkipped
			if runs[i].Status == domain.CaseRunStatusRunning {
				to = domain.CaseRunStatusInterrupted
			}
			updated, err := s.deps.CaseRuns.SetStatus(ctx, runs[i].ID, runs[i].Status, to, now)
			if err != nil {
				if errors.Is(err, domain.ErrCaseRunTerminal) || errors.Is(err, ErrConflict) {
					current, readErr := s.deps.CaseRuns.GetCaseRun(ctx, batchID, runs[i].ID)
					if readErr != nil {
						return nil, readErr
					}
					runs[i] = current
					continue
				}
				return nil, err
			}
			runs[i] = updated
		case domain.CaseRunStatusInterrupted:
			// A run left interrupted without its cleanup flag (a partially
			// failed earlier cancel or a crash between the status and cleanup
			// writes) is repaired here so the invariant cannot rot.
			if runs[i].CleanupRequired {
				continue
			}
		default:
			continue
		}
		if runs[i].Status != domain.CaseRunStatusInterrupted {
			continue
		}
		// A cancelled running case stops mid-flight: its browser/HTTP state is
		// unknown, so a same-attempt resume must cleanup first (spec §5.4,
		// §5.12 invariant 5).
		runs[i].CleanupRequired = true
		runs[i].UpdatedAt = now
		updated, err := s.deps.CaseRuns.UpdateCaseRun(ctx, runs[i])
		if err != nil {
			return nil, err
		}
		runs[i] = updated
	}

	counts, err := s.deps.Batches.UpdateCounts(ctx, batchID)
	if err != nil {
		return nil, err
	}
	if batch.Paused {
		if err := s.clearPause(ctx, batch, actor); err != nil {
			return nil, err
		}
	}
	finishedAt := now
	cancelled, err := s.deps.Batches.UpdateBatchStatus(ctx, batchID, batch.Status, domain.BatchStatusCancelled, &finishedAt)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, Event{
		Type:      "batch.finished",
		VersionID: versionID,
		BatchID:   batchID,
		Summary:   "batch cancelled",
		Detail: map[string]any{
			"status":   string(cancelled.Status),
			"counts":   eventCounts(counts),
			"actor_id": actor,
		},
	})
	return &cancelled, nil
}

// clearPause unpauses the batch and clears the version mirror while the batch is
// still active. Cancel calls it before the terminal status write because
// SetPaused only accepts a planned/running batch.
func (s *BatchService) clearPause(ctx context.Context, batch domain.RunBatch, actor int64) error {
	mode := batch.PauseMode
	if mode == "" {
		mode = domain.PauseModeGraceful
	}
	if _, err := s.deps.Batches.SetPaused(ctx, batch.ID, false, mode, ""); err != nil {
		return mapBatchStateError(err)
	}
	if err := s.deps.Versions.MarkPaused(ctx, batch.VersionID, false, string(mode), "", actor); err != nil {
		// Best-effort rollback so a failed mirror write leaves the pair paused
		// together and the operator can retry the cancel.
		_, _ = s.deps.Batches.SetPaused(ctx, batch.ID, true, mode, batch.PauseReason)
		return err
	}
	return nil
}

// Rerun creates the next attempt of a terminal case run (spec §5.8): the
// snapshot, revision and hash are copied unchanged into an independent deep copy
// (the scenario maps included), attempt is incremented and
// retry_of_case_run_id points at the run being retried. A non-terminal run is
// case_run_terminal.
//
// It deliberately publishes no event: the new attempt is pending and the
// scheduler owns run.started, which it emits when it dispatches the attempt
// (spec §5.10). Emitting here would duplicate the event for one dispatch.
func (s *BatchService) Rerun(ctx context.Context, versionID, batchID, caseRunID, actor int64) (*domain.CaseRun, error) {
	if s.deps.Batches == nil || s.deps.CaseRuns == nil {
		return nil, errors.New("execution: rerun dependencies are not configured")
	}
	batch, err := s.deps.Batches.GetBatch(ctx, versionID, batchID)
	if err != nil {
		return nil, err
	}
	// The scheduler only dispatches planned/running batches, so a retry created
	// under a terminal batch would stay pending forever. Reject it here; the UI
	// may still offer rerun, but the backend is authoritative.
	if !domain.CanPause(batch.Status) {
		return nil, newReasonError("batch_finished",
			fmt.Sprintf("batch %d is %s and cannot be rerun", batchID, batch.Status))
	}
	run, err := s.deps.CaseRuns.GetCaseRun(ctx, batchID, caseRunID)
	if err != nil {
		return nil, err
	}
	if !isTerminalCaseRun(run.Status) {
		return nil, newReasonError("case_run_terminal",
			fmt.Sprintf("case run %d is %s; retry requires a terminal run", caseRunID, run.Status))
	}

	id, err := nextID(s.deps.IDs)
	if err != nil {
		return nil, err
	}
	now := s.now()
	retryOf := run.ID
	rerun := domain.CaseRun{
		ID:                 id,
		BatchID:            run.BatchID,
		VersionID:          run.VersionID,
		CaseNodeID:         run.CaseNodeID,
		CaseRevision:       run.CaseRevision,
		CaseHash:           run.CaseHash,
		Snapshot:           cloneSnapshot(run.Snapshot),
		Attempt:            run.Attempt + 1,
		Status:             domain.CaseRunStatusPending,
		RetryOfCaseRunID:   &retryOf,
		CleanupRequired:    false,
		InterruptedStepSeq: nil,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	created, err := s.deps.CaseRuns.CreateCaseRun(ctx, rerun)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// buildRuns pins one immutable snapshot and one pending run per selected case.
func (s *BatchService) buildRuns(batchID, versionID int64, views []CaseSnapshotSourceView, selected []int64, baselineHash string, now time.Time) ([]domain.CaseRun, error) {
	if len(views) != len(selected) {
		return nil, fmt.Errorf("execution: mindmap returned %d snapshots for %d selected cases", len(views), len(selected))
	}
	byID := make(map[int64]CaseSnapshotSourceView, len(views))
	for _, view := range views {
		if _, duplicate := byID[view.CaseNodeID]; duplicate {
			return nil, fmt.Errorf("execution: mindmap returned duplicate snapshots for case %d", view.CaseNodeID)
		}
		byID[view.CaseNodeID] = view
	}
	runs := make([]domain.CaseRun, 0, len(selected))
	for _, caseNodeID := range selected {
		view, ok := byID[caseNodeID]
		if !ok {
			return nil, fmt.Errorf("execution: mindmap returned no snapshot for case %d", caseNodeID)
		}
		if view.GraphHash != baselineHash {
			return nil, newReasonError("baseline_changed",
				fmt.Sprintf("case %d frozen graph hash does not match the baseline", caseNodeID))
		}
		snapshot := snapshotFromView(view, now)
		caseHash := domain.CanonicalCaseHash(snapshot)
		if caseHash == "" {
			return nil, fmt.Errorf("execution: case %d snapshot cannot be hashed", caseNodeID)
		}
		id, err := nextID(s.deps.IDs)
		if err != nil {
			return nil, err
		}
		runs = append(runs, domain.CaseRun{
			ID:           id,
			BatchID:      batchID,
			VersionID:    versionID,
			CaseNodeID:   caseNodeID,
			CaseRevision: view.Revision,
			CaseHash:     caseHash,
			Snapshot:     snapshot,
			Attempt:      1,
			Status:       domain.CaseRunStatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	return runs, nil
}

// listAllCaseRuns scans every page of a batch's case runs.
func (s *BatchService) listAllCaseRuns(ctx context.Context, batchID int64) ([]domain.CaseRun, error) {
	all := make([]domain.CaseRun, 0)
	beforeID := int64(0)
	for {
		page, more, err := s.deps.CaseRuns.ListCaseRuns(ctx, batchID, "", beforeID, caseRunPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if !more || len(page) == 0 {
			return all, nil
		}
		beforeID = page[len(page)-1].ID
	}
}

// requireCreateDeps rejects a mis-wired service before touching the version.
func (s *BatchService) requireCreateDeps() error {
	switch {
	case s.deps.Versions == nil:
		return errors.New("execution: version port is not configured")
	case s.deps.Mindmap == nil:
		return errors.New("execution: mindmap port is not configured")
	case s.deps.Batches == nil:
		return errors.New("execution: batch repository is not configured")
	case s.deps.IDs == nil:
		return errors.New("execution: id generator is not configured")
	}
	return nil
}

// releaseBatchNo compensates an allocated batch number after the batch insert
// failed. The version adapter only rolls the number back while it is still the
// newest one, so a concurrent creator's allocation is never lost. Compensation
// is best effort: numbers are monotonic rather than gapless, so a failed release
// costs at most a skipped number and must not mask the original create error.
func (s *BatchService) releaseBatchNo(ctx context.Context, versionID int64, allocated int) {
	if err := s.deps.Versions.ReleaseBatchNo(ctx, versionID, allocated); err != nil {
		slog.WarnContext(ctx, "execution: release batch_no after a failed create",
			"version_id", versionID,
			"batch_no", allocated,
			"error", err,
		)
	}
}

// publish emits one event after the committed write. Publication failures are
// logged and swallowed: the API is authoritative and the stream self-heals.
func (s *BatchService) publish(ctx context.Context, event Event) {
	if s.deps.Events == nil {
		return
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "execution event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"batch_id", event.BatchID,
			"error", err,
		)
	}
}

// selectCaseIDs validates a requested selection against the frozen baseline and
// returns it sorted ascending and deduplicated. An empty request selects every
// baseline case.
func selectCaseIDs(requested, baselineIDs []int64) ([]int64, error) {
	valid := make(map[int64]struct{}, len(baselineIDs))
	for _, id := range baselineIDs {
		if id > 0 {
			valid[id] = struct{}{}
		}
	}
	if len(requested) == 0 {
		return sortedUniqueIDs(baselineIDs), nil
	}
	selected := make([]int64, 0, len(requested))
	seen := make(map[int64]struct{}, len(requested))
	for _, id := range requested {
		if _, ok := valid[id]; !ok {
			return nil, fmt.Errorf("case %d is not a case of the frozen baseline", id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		selected = append(selected, id)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	return selected, nil
}

// sortedUniqueIDs returns ids ascending without duplicates.
func sortedUniqueIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// snapshotFromView pins the mindmap projection into an immutable case snapshot.
func snapshotFromView(view CaseSnapshotSourceView, capturedAt time.Time) domain.CaseSnapshot {
	sources := make([]domain.CaseSnapshotSource, 0, len(view.RequirementIDs))
	for _, requirementID := range view.RequirementIDs {
		sources = append(sources, domain.CaseSnapshotSource{RequirementID: requirementID})
	}
	return domain.CaseSnapshot{
		Node:       view.Node,
		Steps:      append([]domain.CaseSnapshotStep(nil), view.Steps...),
		Assertions: append([]domain.CaseSnapshotAssertion(nil), view.Assertions...),
		Sources:    sources,
		GraphHash:  view.GraphHash,
		CapturedAt: capturedAt,
	}
}

// cloneSnapshot copies a snapshot including its slices and the scenario maps
// (node test_data and each step's data) so a rerun cannot share mutable state
// with the original attempt.
func cloneSnapshot(snapshot domain.CaseSnapshot) domain.CaseSnapshot {
	cloned := snapshot
	cloned.Node.TestData = cloneAnyMap(snapshot.Node.TestData)
	if snapshot.Steps != nil {
		cloned.Steps = make([]domain.CaseSnapshotStep, len(snapshot.Steps))
		for i, step := range snapshot.Steps {
			cloned.Steps[i] = step
			cloned.Steps[i].Data = cloneAnyMap(step.Data)
		}
	}
	cloned.Assertions = append([]domain.CaseSnapshotAssertion(nil), snapshot.Assertions...)
	cloned.Sources = append([]domain.CaseSnapshotSource(nil), snapshot.Sources...)
	return cloned
}

// cloneAnyMap deep-copies a JSON-shaped map: nested maps and slices are rebuilt
// so no mutation can cross between the original and the copy.
func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneAnyValue(value)
	}
	return cloned
}

// cloneAnyValue deep-copies the container values a JSON-decoded scenario can
// hold; scalars are immutable and are returned as is.
func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneAnyValue(item)
		}
		return cloned
	default:
		return value
	}
}

// copyStringPointer copies an optional string so callers cannot mutate the
// stored value through the returned batch.
func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// mapBatchStateError translates a repository state conflict into the spec §6.4
// reason. Unknown errors are returned unchanged so the HTTP layer can classify
// them.
func mapBatchStateError(err error) error {
	switch {
	case errors.Is(err, domain.ErrBatchActive):
		return newReasonError("batch_active", err.Error())
	case errors.Is(err, domain.ErrAlreadyPaused):
		return newReasonError("already_paused", err.Error())
	case errors.Is(err, domain.ErrNotPaused):
		return newReasonError("not_paused", err.Error())
	case errors.Is(err, domain.ErrBatchFinished):
		return newReasonError("batch_finished", err.Error())
	}
	return err
}

// isTerminalCaseRun reports whether a case run already ended and may be rerun.
func isTerminalCaseRun(status domain.CaseRunStatus) bool {
	switch status {
	case domain.CaseRunStatusPassed, domain.CaseRunStatusFailed, domain.CaseRunStatusBlocked,
		domain.CaseRunStatusInconclusive, domain.CaseRunStatusSkipped, domain.CaseRunStatusInterrupted:
		return true
	}
	return false
}

// eventCounts renders a recount for an event detail without exposing internals.
func eventCounts(counts domain.BatchCounts) map[string]int {
	return map[string]int{
		"total":        counts.CaseCount,
		"passed":       counts.PassedCount,
		"failed":       counts.FailedCount,
		"blocked":      counts.BlockedCount,
		"inconclusive": counts.InconclusiveCount,
		"skipped":      counts.SkippedCount,
		"interrupted":  counts.InterruptedCount,
	}
}

// newReasonError builds a spec §6.4 state/gate error: the reason is the stable
// identifier and is repeated as the API code.
func newReasonError(reason, message string) *domain.ReasonError {
	return domain.NewReasonError(reason, reason, message, false, nil)
}

// newInvalidReasonError builds a request-validation error (HTTP 422).
func newInvalidReasonError(reason, message string) *domain.ReasonError {
	return domain.NewReasonError("invalid_argument", reason, message, false, nil)
}
