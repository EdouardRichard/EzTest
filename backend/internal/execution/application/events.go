package executionapp

import (
	"context"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// P3 event types (contracts/events.md §12). Every event is published to
// version:{version_id}; scope IDs are decimal strings.
const (
	EventIntentCreated    = "execution.intent_created"
	EventIntentCancelled  = "execution.intent_cancelled"
	EventIntentSuperseded = "execution.intent_superseded"
	EventWaveCreated      = "wave.created"
	EventWaveStarted      = "wave.started"
	EventWavePaused       = "wave.paused"
	EventWaveResumed      = "wave.resumed"
	EventWaveFinished     = "wave.finished"
	EventCaseRunStarted   = "case_run.started"
	EventCaseRunPaused    = "case_run.paused"
	EventCaseRunResumed   = "case_run.resumed"
	EventCaseRunFinished  = "case_run.finished"
	// EventCaseRunReplayed marks a server-side command-log replay verification
	// (v0.2.4-C EV-006). The payload carries the source case_run id, the replay
	// input fingerprint (environment + code baseline + evidence versions) and the
	// server-validated replay verdict.
	EventCaseRunReplayed   = "case_run.replayed"
	EventStepFinished      = "case_run.step_finished"
	EventAssertionFinished = "case_run.assertion_finished"
	EventEvidenceCreated   = "evidence.created"
	// EventEvidenceSkipped marks one evidence row dropped by the configured
	// per-case-run cap (execution.evidence_max_per_case_run). One event per
	// dropped item; the payload carries kind/reason/cap/evidence_count.
	EventEvidenceSkipped  = "evidence.skipped"
	EventHumanCreated     = "human_request.created"
	EventHumanResolved    = "human_request.resolved"
	EventHumanExpired     = "human_request.expired"
	EventQuestionCreated  = "question.created"
	EventQuestionAnswered = "question.answered"
	EventQuestionExpired  = "question.expired"
	EventQuestionReopened = "question.reopened"
	EventQuestionSkipped  = "question.skipped"
	// v0.22 R-W2-1 version-level decision requests.
	EventApprovalRequested = "approval.requested"
	EventApprovalDecided   = "approval.decided"
	EventFailureCandidate  = "execution.failure_candidate"
	EventCoverageGap       = "execution.coverage_gap"
)

// EventVersionScopeSnapshotCreated announces the frozen effective scope of a
// newly planned wave (R-EX-4/EV-11). It is published after wave.created on the
// version channel; the payload carries the decimal version/wave IDs and the
// snapshot frozen_at timestamp.
const EventVersionScopeSnapshotCreated = "version.scope.snapshot_created"

// publishEvent publishes one event to the version channel. Publishing is
// best-effort and never fails the business write.
func publishEvent(ctx context.Context, publisher eventingapp.Publisher, eventType string, versionID int64, scope, payload map[string]any, actorID *int64) {
	if publisher == nil || versionID <= 0 {
		return
	}
	versionRef := platformid.Format(versionID)
	if scope == nil {
		scope = map[string]any{}
	}
	scope["version_id"] = versionRef
	_ = publisher.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + versionRef,
		Type:      eventType,
		Scope:     scope,
		Payload:   payload,
		ActorID:   actorID,
		RequestID: requestid.From(ctx),
	})
}

// eventTime formats a timestamp for event payloads.
func eventTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// idsToStrings formats an ID list as decimal strings for JSON/events.
func idsToStrings(ids []int64) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, platformid.Format(id))
	}
	return values
}

// auditEvent builds one audit entry with a decimal target ID.
func auditEvent(action, targetKind string, targetID int64, metadata map[string]any) auditapp.Event {
	return auditapp.Event{
		Action:     action,
		TargetKind: targetKind,
		TargetID:   platformid.Format(targetID),
		Metadata:   metadata,
	}
}

// recordAudit records one best-effort audit entry.
func recordAudit(ctx context.Context, recorder AuditRecorder, action, targetKind string, targetID, actorID int64, metadata map[string]any) {
	if recorder == nil {
		return
	}
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	_ = recorder.Record(ctx, auditapp.Event{
		ActorID:    actor,
		Action:     action,
		TargetKind: targetKind,
		TargetID:   platformid.Format(targetID),
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}
