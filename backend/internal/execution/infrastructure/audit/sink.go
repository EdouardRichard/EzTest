// Package audit adapts the execution context's audit surface to the P0 audit
// recorder (spec §5.11). Entries carry ids, statuses, failure classes and
// durations only: never credentials, prompts, evidence bytes or case bodies.
// P0 request metadata is stamped when the call happens inside an HTTP request.
package audit

import (
	"context"
	"strconv"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Execution audit actions (spec §5.11). Each name is stable because it is
// persisted and filtered on.
const (
	// ActionBatchCreated is recorded when a batch is created.
	ActionBatchCreated = "execution.batch.created"
	// ActionBatchPaused is recorded when a batch execution pauses.
	ActionBatchPaused = "execution.batch.paused"
	// ActionBatchResumed is recorded when a paused batch resumes.
	ActionBatchResumed = "execution.batch.resumed"
	// ActionBatchCancelled is recorded when a batch is cancelled.
	ActionBatchCancelled = "execution.batch.cancelled"
	// ActionCaseFinished is recorded when a case run reaches a terminal state.
	ActionCaseFinished = "execution.case.finished"
)

// Audit target types.
const (
	targetRunBatch = "run_batch"
	targetCaseRun  = "case_run"
)

// Recorder is the narrow slice of the audit application recorder the sink needs.
// *audit/application.Service satisfies it.
type Recorder interface {
	Record(ctx context.Context, entry auditdomain.Entry) error
}

// Sink records execution audit entries through the P0 audit log.
type Sink struct {
	recorder Recorder
}

// NewSink returns the audit sink. A nil recorder disables auditing (used by
// partially wired processes and tests).
func NewSink(recorder Recorder) *Sink {
	return &Sink{recorder: recorder}
}

// The P0 audit recorder must keep satisfying the adapter contract.
var _ Recorder = (*auditapp.Service)(nil)

// BatchCreated records the creation of one batch.
func (s *Sink) BatchCreated(ctx context.Context, batch executiondomain.RunBatch, actor int64) error {
	return s.record(ctx, actor, ActionBatchCreated, targetRunBatch, batch.ID, map[string]any{
		"version_id": batch.VersionID,
		"batch_no":   batch.BatchNo,
		"status":     string(batch.Status),
		"case_count": batch.CaseCount,
	})
}

// BatchPaused records a pause (paused=true) or a resume (paused=false) of one
// batch.
func (s *Sink) BatchPaused(ctx context.Context, batch executiondomain.RunBatch, paused bool, actor int64) error {
	action := ActionBatchPaused
	if !paused {
		action = ActionBatchResumed
	}
	metadata := map[string]any{
		"version_id": batch.VersionID,
		"batch_no":   batch.BatchNo,
		"paused":     paused,
	}
	if batch.PauseMode != "" {
		metadata["pause_mode"] = string(batch.PauseMode)
	}
	if batch.PauseReason != "" {
		metadata["reason"] = batch.PauseReason
	}
	return s.record(ctx, actor, action, targetRunBatch, batch.ID, metadata)
}

// BatchCancelled records the cancellation of one batch.
func (s *Sink) BatchCancelled(ctx context.Context, batchID, versionID int64, actorID int64) error {
	return s.record(ctx, actorID, ActionBatchCancelled, targetRunBatch, batchID, map[string]any{
		"version_id": versionID,
	})
}

// CaseFinished records the terminal outcome of one case run. The summary text is
// deliberately not recorded: only ids, status, failure class and duration.
func (s *Sink) CaseFinished(ctx context.Context, run executiondomain.CaseRun, actor int64) error {
	metadata := map[string]any{
		"batch_id":     run.BatchID,
		"version_id":   run.VersionID,
		"case_node_id": run.CaseNodeID,
		"attempt":      run.Attempt,
		"status":       string(run.Status),
		"duration_ms":  run.DurationMS,
	}
	if run.FailureClass != nil {
		metadata["failure_class"] = string(*run.FailureClass)
	}
	if run.FailureReason != "" {
		metadata["failure_reason"] = run.FailureReason
	}
	return s.record(ctx, actor, ActionCaseFinished, targetCaseRun, run.ID, metadata)
}

// record appends one audit entry. A nil sink or recorder is a no-op so audit
// wiring is optional; a recorder failure is returned to the caller.
func (s *Sink) record(ctx context.Context, actor int64, action, targetType string, targetID int64, metadata map[string]any) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	entry := auditdomain.Entry{
		Action:     action,
		TargetType: targetType,
		TargetID:   strconv.FormatInt(targetID, 10),
		Metadata:   metadata,
	}
	if actor > 0 {
		actorID := actor
		entry.ActorUserID = &actorID
	} else if contextActor, ok := auditapp.ActorIDFromContext(ctx); ok && contextActor > 0 {
		actorID := contextActor
		entry.ActorUserID = &actorID
	}
	// Request correlation (P0): an entry recorded inside an HTTP request carries
	// that request's metadata; an asynchronous worker falls back to the request
	// id copied onto its context.
	if meta := auditapp.MetaFromContext(ctx); meta != (auditapp.RequestMeta{}) {
		entry.RequestID = meta.RequestID
		entry.IP = meta.IP
		entry.UserAgent = meta.UserAgent
	}
	if entry.RequestID == "" {
		entry.RequestID = platform.RequestID(ctx)
	}
	return s.recorder.Record(ctx, entry)
}
