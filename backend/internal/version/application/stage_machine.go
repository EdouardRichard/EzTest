package versionapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Stage-entry work item kinds the deterministic stage machine creates (spec
// §5.4, D21 route 1). Task 7 (P2-T2) adapts the narrow StageJobEnqueuer port to
// the work-item dispatcher; the version context never imports that context.
const (
	StageJobDesignCases   = "design.cases"
	StageJobReviewCases   = "review.cases"
	StageJobExecuteBatch  = "execute.batch"
	StageJobReportVersion = "report.version"
)

// Stage machine limits and audit names.
const (
	stageReasonMax       = 128
	stageBlockerMax      = 4000
	stageRejectReasonMax = 2000

	stageAuditDecision = "version.stage_decision"
	stageAuditDenied   = "version.stage_gate_denied"

	stageGateRequirement = "requirement"
	stageGateCase        = "case"
)

// StageJob is one task the stage machine asks the dispatcher to create. An
// empty RequirementIDs or CaseIDs list means "all items of that kind"; a
// non-empty list is a partial confirmation (D23) and narrows the job.
type StageJob struct {
	VersionID      int64
	Kind           string
	RequirementIDs []int64
	CaseIDs        []int64
}

// StageJobEnqueuer ensures one stage-entry job exists. Implementations must be
// idempotent on (version_id, kind, payload) because every stage re-entry may
// re-enqueue the same job.
type StageJobEnqueuer interface {
	EnqueueStageJob(ctx context.Context, job StageJob) error
}

// StageView is the user-facing stage projection (contracts/openapi.yaml
// StageView).
type StageView struct {
	VersionID      int64
	Stage          version.Stage
	StageEnteredAt time.Time
	Blocker        string
}

// StageMachine is the only writer of version.stage (spec §5.3). It validates
// domain transitions, records the suspension source, publishes
// version.stage_changed after the write commits and asks the injected enqueuer
// for the stage-entry job. LLM output never reaches this service.
type StageMachine struct {
	repo   version.StageRepository
	jobs   StageJobEnqueuer
	audit  auditapp.Recorder
	events eventingapp.Publisher
	clock  clock.Clock
}

// NewStageMachine wires the deterministic stage machine. jobs, audit and
// events may be nil in degraded assemblies; a nil enqueuer only skips stage
// job dispatch, never the stage write.
func NewStageMachine(
	repo version.StageRepository,
	jobs StageJobEnqueuer,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
	clk clock.Clock,
) *StageMachine {
	if clk == nil {
		clk = clock.System{}
	}
	return &StageMachine{repo: repo, jobs: jobs, audit: audit, events: events, clock: clk}
}

// Stage returns the current stage projection.
func (m *StageMachine) Stage(ctx context.Context, versionID int64) (StageView, error) {
	state, err := m.load(ctx, versionID)
	if err != nil {
		return StageView{}, err
	}
	return viewFromState(state), nil
}

// Transition validates and applies one ordinary stage transition. Entering
// blocked/paused is not an ordinary transition; use Suspend so the previous
// stage is recorded. The stage-entry job of the target stage is enqueued after
// the version row is written.
func (m *StageMachine) Transition(ctx context.Context, versionID int64, to version.Stage, reason string) error {
	_, err := m.transition(ctx, versionID, to, reason, stageEntryJob(versionID, to))
	return err
}

// Suspend enters blocked or paused and records the current workflow stage in
// version.suspended_from together with the user-visible blocker reason.
func (m *StageMachine) Suspend(ctx context.Context, versionID int64, to version.Stage, reason string) error {
	if to != version.StageBlocked && to != version.StagePaused {
		return platform.Invalid("stage", "must be one of blocked, paused")
	}
	blocker := strings.TrimSpace(reason)
	if err := validateStageText("reason", blocker, stageBlockerMax); err != nil {
		return err
	}
	current, err := m.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage.IsSuspended() {
		return platform.StateConflict("version is already suspended")
	}
	if !current.Stage.CanTransitionTo(to) {
		return platform.StateConflict(fmt.Sprintf("cannot suspend version from stage %s", current.Stage))
	}
	suspendedFrom := current.Stage
	next := version.StageState{
		VersionID:      versionID,
		Stage:          to,
		StageEnteredAt: m.now(),
		Blocker:        blocker,
		SuspendedFrom:  &suspendedFrom,
	}
	return m.apply(ctx, current, next, string(to), blocker, nil)
}

// Resume restores the stage recorded in version.suspended_from. A suspension
// with no recorded stage (legacy or corrupted row) is not resumable.
func (m *StageMachine) Resume(ctx context.Context, versionID int64) error {
	current, err := m.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.SuspendedFrom == nil {
		return platform.StateConflict("version has no recorded suspended stage")
	}
	return m.resumeTo(ctx, current, *current.SuspendedFrom)
}

// ResumeTo resumes a suspended version to target. target must equal the stage
// recorded in version.suspended_from; any other target is rejected so a resume
// can never jump ahead past a hard gate.
func (m *StageMachine) ResumeTo(ctx context.Context, versionID int64, target version.Stage) error {
	current, err := m.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.SuspendedFrom == nil {
		return platform.StateConflict("version has no recorded suspended stage")
	}
	if target != *current.SuspendedFrom {
		return platform.StateConflict(fmt.Sprintf(
			"resume target %s does not match the recorded suspended stage %s", target, *current.SuspendedFrom,
		))
	}
	return m.resumeTo(ctx, current, target)
}

// OnRequirementConfirmed applies the requirement hard gate
// (requirement_review -> designing) and dispatches design.cases for the
// confirmed requirements. An empty requirementIDs list means "all".
func (m *StageMachine) OnRequirementConfirmed(ctx context.Context, versionID int64, requirementIDs []int64) (StageView, error) {
	ids, err := normalizeStageIDs("requirement_ids", requirementIDs)
	if err != nil {
		return StageView{}, err
	}
	next, err := m.transition(ctx, versionID, version.StageDesigning, "requirement_confirmed", &StageJob{
		VersionID:      versionID,
		Kind:           StageJobDesignCases,
		RequirementIDs: ids,
	})
	if err != nil {
		return StageView{}, err
	}
	m.recordDecision(ctx, versionID, stageGateRequirement, "confirmed", version.StageDesigning, "", ids, nil)
	return viewFromState(next), nil
}

// OnCasesConfirmed applies the case hard gate (case_review -> executing) and
// dispatches execute.batch. A non-empty caseIDs list executes only those
// confirmed cases (D23); an empty list means every confirmed case.
func (m *StageMachine) OnCasesConfirmed(ctx context.Context, versionID int64, caseIDs []int64) (StageView, error) {
	ids, err := normalizeStageIDs("case_ids", caseIDs)
	if err != nil {
		return StageView{}, err
	}
	next, err := m.transition(ctx, versionID, version.StageExecuting, "cases_confirmed", &StageJob{
		VersionID: versionID,
		Kind:      StageJobExecuteBatch,
		CaseIDs:   ids,
	})
	if err != nil {
		return StageView{}, err
	}
	m.recordDecision(ctx, versionID, stageGateCase, "confirmed", version.StageExecuting, "", nil, ids)
	return viewFromState(next), nil
}

// OnWaveTerminal enters reporting after a terminal execution wave and
// dispatches report.version.
func (m *StageMachine) OnWaveTerminal(ctx context.Context, versionID int64) (StageView, error) {
	next, err := m.transition(ctx, versionID, version.StageReporting, "wave_terminal", &StageJob{
		VersionID: versionID,
		Kind:      StageJobReportVersion,
	})
	if err != nil {
		return StageView{}, err
	}
	return viewFromState(next), nil
}

// Reject returns a hard gate one stage back: requirement_review -> clarifying
// and case_review -> designing. target is the optional client-selected back
// stage; it must equal the gate's previous stage.
func (m *StageMachine) Reject(ctx context.Context, versionID int64, reason string, target *version.Stage) (StageView, error) {
	reason = strings.TrimSpace(reason)
	if err := validateStageText("reason", reason, stageRejectReasonMax); err != nil {
		return StageView{}, err
	}
	current, err := m.load(ctx, versionID)
	if err != nil {
		return StageView{}, err
	}
	var back version.Stage
	var gate string
	switch current.Stage {
	case version.StageRequirementReview:
		back, gate = version.StageClarifying, stageGateRequirement
	case version.StageCaseReview:
		back, gate = version.StageDesigning, stageGateCase
	default:
		return StageView{}, platform.StateConflict("only a version at a hard gate can be rejected")
	}
	if target != nil && *target != back {
		return StageView{}, platform.StateConflict(fmt.Sprintf(
			"reject target %s does not match the gate's previous stage %s", *target, back,
		))
	}
	next, err := m.transitionFrom(ctx, current, back, "rejected", nil)
	if err != nil {
		return StageView{}, err
	}
	m.recordDecision(ctx, versionID, gate, "rejected", back, reason, nil, nil)
	return viewFromState(next), nil
}

// RecordGateDenied records a forbidden hard-gate attempt (assistant/service
// token). It never touches the stage row.
func (m *StageMachine) RecordGateDenied(ctx context.Context, versionID int64, gate string) {
	metadata := map[string]any{"gate": gate}
	if actor, ok := platform.ActorFrom(ctx); ok {
		metadata["role"] = actor.Role
	}
	m.recordAudit(ctx, stageAuditDenied, versionID, metadata)
}

// ---------- internals ----------

func (m *StageMachine) transition(ctx context.Context, versionID int64, to version.Stage, reason string, job *StageJob) (version.StageState, error) {
	if err := validateStageText("reason", strings.TrimSpace(reason), stageReasonMax); err != nil {
		return version.StageState{}, err
	}
	current, err := m.load(ctx, versionID)
	if err != nil {
		return version.StageState{}, err
	}
	return m.transitionFrom(ctx, current, to, reason, job)
}

func (m *StageMachine) transitionFrom(ctx context.Context, current version.StageState, to version.Stage, reason string, job *StageJob) (version.StageState, error) {
	if to.IsSuspended() {
		return version.StageState{}, platform.Invalid("stage", "blocked and paused require Suspend")
	}
	if current.Stage.IsSuspended() {
		return version.StageState{}, platform.StateConflict("a suspended version must resume before any other transition")
	}
	if !current.Stage.CanTransitionTo(to) {
		return version.StageState{}, platform.StateConflict(fmt.Sprintf(
			"cannot transition version from %s to %s", current.Stage, to,
		))
	}
	next := version.StageState{
		VersionID:      current.VersionID,
		Stage:          to,
		StageEnteredAt: m.now(),
	}
	if err := m.apply(ctx, current, next, reason, "", job); err != nil {
		return version.StageState{}, err
	}
	return next, nil
}

func (m *StageMachine) resumeTo(ctx context.Context, current version.StageState, target version.Stage) error {
	if !current.Stage.CanResumeTo(target) {
		return platform.StateConflict(fmt.Sprintf(
			"cannot resume version from %s to %s", current.Stage, target,
		))
	}
	next := version.StageState{
		VersionID:      current.VersionID,
		Stage:          target,
		StageEnteredAt: m.now(),
	}
	return m.apply(ctx, current, next, "resumed", "", nil)
}

// apply writes the validated transition, then publishes the stage event and
// asks the dispatcher for the stage-entry job. Event delivery and job enqueue
// are best effort: the DB row is the source of truth and a later reconciler
// can recreate a missed job (spec §5.7).
func (m *StageMachine) apply(ctx context.Context, current, next version.StageState, reason, blocker string, job *StageJob) error {
	if err := m.repo.ApplyStage(ctx, current.Stage, next); err != nil {
		return mapStageRepositoryError(err)
	}
	m.publishStageChanged(ctx, current, next, reason, blocker)
	if job != nil && m.jobs != nil {
		_ = m.jobs.EnqueueStageJob(ctx, *job)
	}
	return nil
}

func (m *StageMachine) load(ctx context.Context, versionID int64) (version.StageState, error) {
	if versionID <= 0 {
		return version.StageState{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	state, err := m.repo.GetStage(ctx, versionID)
	if err != nil {
		return version.StageState{}, mapStageRepositoryError(err)
	}
	state.VersionID = versionID
	return state, nil
}

func (m *StageMachine) now() time.Time { return m.clock.Now().UTC() }

func (m *StageMachine) publishStageChanged(ctx context.Context, from, to version.StageState, reason, blocker string) {
	if m.events == nil {
		return
	}
	payload := map[string]any{
		"from":             string(from.Stage),
		"to":               string(to.Stage),
		"reason":           reason,
		"stage_entered_at": to.StageEnteredAt.UTC().Format(time.RFC3339),
	}
	if blocker != "" {
		payload["blocker"] = blocker
	}
	versionID := platformid.Format(to.VersionID)
	scope := map[string]any{"version_id": versionID}
	for _, channel := range []string{"version:" + versionID, "system"} {
		_ = m.events.Publish(ctx, eventingapp.Event{
			Channel:   channel,
			Type:      "version.stage_changed",
			Scope:     scope,
			Payload:   payload,
			ActorID:   actorIDFrom(ctx),
			RequestID: requestid.From(ctx),
		})
	}
}

func (m *StageMachine) recordDecision(
	ctx context.Context,
	versionID int64,
	gate, decision string,
	target version.Stage,
	reason string,
	requirementIDs, caseIDs []int64,
) {
	metadata := map[string]any{
		"gate":            gate,
		"decision":        decision,
		"target_stage":    string(target),
		"requirement_ids": formatStageIDs(requirementIDs),
		"case_ids":        formatStageIDs(caseIDs),
	}
	if reason != "" {
		metadata["reason"] = reason
	}
	m.recordAudit(ctx, stageAuditDecision, versionID, metadata)
}

func (m *StageMachine) recordAudit(ctx context.Context, action string, versionID int64, metadata map[string]any) {
	if m.audit == nil {
		return
	}
	_ = m.audit.Record(ctx, auditapp.Event{
		ActorID:    actorIDFrom(ctx),
		Action:     action,
		TargetKind: "version",
		TargetID:   platformid.Format(versionID),
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}

func actorIDFrom(ctx context.Context) *int64 {
	actor, ok := platform.ActorFrom(ctx)
	if !ok || actor.ID <= 0 {
		return nil
	}
	id := actor.ID
	return &id
}

func viewFromState(state version.StageState) StageView {
	return StageView{
		VersionID:      state.VersionID,
		Stage:          state.Stage,
		StageEnteredAt: state.StageEnteredAt,
		Blocker:        state.Blocker,
	}
}

// stageEntryJob maps a target stage to the deterministic stage-entry job
// (spec §5.4). Stages without a stage-entry job return nil.
func stageEntryJob(versionID int64, to version.Stage) *StageJob {
	kind := ""
	switch to {
	case version.StageDesigning:
		kind = StageJobDesignCases
	case version.StageCaseReview:
		kind = StageJobReviewCases
	case version.StageExecuting:
		kind = StageJobExecuteBatch
	case version.StageReporting:
		kind = StageJobReportVersion
	default:
		return nil
	}
	return &StageJob{VersionID: versionID, Kind: kind}
}

func normalizeStageIDs(field string, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	normalized := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, platform.Invalid(field, "must contain positive decimal IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized, nil
}

func formatStageIDs(ids []int64) []string {
	formatted := make([]string, 0, len(ids))
	for _, id := range ids {
		formatted = append(formatted, platformid.Format(id))
	}
	return formatted
}

func validateStageText(field, value string, max int) error {
	if value == "" {
		return platform.Invalid(field, "must not be empty")
	}
	if len([]rune(value)) > max {
		return platform.Invalid(field, fmt.Sprintf("must be at most %d characters", max))
	}
	return nil
}

func mapStageRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrNotFound):
		return platform.NotFound("version")
	case errors.Is(err, version.ErrStageConflict):
		return platform.StateConflict("version stage changed concurrently; retry the transition")
	default:
		return err
	}
}
