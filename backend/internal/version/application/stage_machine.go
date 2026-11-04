package versionapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
// empty RequirementIDs or CaseIDs list means "all items of that kind": the hard
// gate expands an empty confirm selection to the version's current draft items
// before the stage advances, so the stage-entry job stays unrestricted. A
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

// RequirementGateConfirmer freezes the requirement rows selected by the
// requirement hard gate before the stage machine advances. An empty selection
// means "all current draft requirements of the version" (contract
// StageConfirmRequirementsRequest); an implementation must expand it and reject
// a version with no draft requirement with a typed 409
// no_requirements_to_confirm. A foreign or missing ID is a typed not-found.
type RequirementGateConfirmer interface {
	BatchConfirm(ctx context.Context, versionID int64, requirementIDs []int64) error
}

// CaseGateConfirmer freezes the case rows selected by the case hard gate before
// the stage machine advances. An empty list means "all confirmed-eligible cases
// of the version"; a version with no eligible draft case must be rejected with a
// typed 409 no_cases_to_confirm (spec §5.3: executing requires confirmed cases).
type CaseGateConfirmer interface {
	ConfirmCases(ctx context.Context, versionID int64, caseIDs []int64) error
}

// StageMachineOption configures optional StageMachine collaborators without
// widening the positional constructor for every caller.
type StageMachineOption func(*StageMachine)

// WithRequirementGateConfirmer installs the requirement gate entity-confirmation
// port. Production must set it; unit tests may omit it only when they never call
// the requirement gate.
func WithRequirementGateConfirmer(confirmer RequirementGateConfirmer) StageMachineOption {
	return func(m *StageMachine) {
		m.requirements = confirmer
	}
}

// WithCaseGateConfirmer installs the case gate entity-confirmation port.
// Production must set it; unit tests may omit it only when they never call the
// case gate.
func WithCaseGateConfirmer(confirmer CaseGateConfirmer) StageMachineOption {
	return func(m *StageMachine) {
		m.cases = confirmer
	}
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
	// requirements and cases confirm the selected entities before a hard gate
	// advances. They are narrow ports so the version context never imports the
	// requirement or design services.
	requirements RequirementGateConfirmer
	cases        CaseGateConfirmer
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
	options ...StageMachineOption,
) *StageMachine {
	if clk == nil {
		clk = clock.System{}
	}
	machine := &StageMachine{repo: repo, jobs: jobs, audit: audit, events: events, clock: clk}
	for _, option := range options {
		if option != nil {
			option(machine)
		}
	}
	return machine
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

// StartClarification applies the automatic created→clarifying edge (spec §5.3:
// created starts the discovery sub-loop and exits automatically). It is
// idempotent: a version that already left created is left untouched, so every
// duplicate version.created event is harmless.
func (m *StageMachine) StartClarification(ctx context.Context, versionID int64) error {
	current, err := m.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StageCreated {
		return nil
	}
	return m.Transition(ctx, versionID, version.StageClarifying, "discovery_started")
}

// ConvergeClarification applies the clarifying→requirement_review edge after
// the clarification round converged. It is the production caller the S2
// requirement gate depends on; like StartClarification it only acts on its
// source stage, so a repeated confirmation request is a no-op and can never
// move a version that is already at or past the requirement gate.
func (m *StageMachine) ConvergeClarification(ctx context.Context, versionID int64) error {
	current, err := m.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StageClarifying {
		return nil
	}
	return m.Transition(ctx, versionID, version.StageRequirementReview, "clarification_converged")
}

// ConvergeDesign applies the designing→case_review edge once the design pass
// generated usable cases for the version's activated requirements (Task 69). It
// is called by the design reconciler; like ConvergeClarification it only acts
// on its source stage, so a repeated completion is a no-op and can never move a
// version that already reached the case hard gate or beyond. case_review has no
// stage-entry job, so the transition enqueues nothing.
func (m *StageMachine) ConvergeDesign(ctx context.Context, versionID int64) error {
	current, err := m.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StageDesigning {
		return nil
	}
	return m.Transition(ctx, versionID, version.StageCaseReview, "design_converged")
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
	return m.resumeRecorded(ctx, versionID, "resumed")
}

// resumeRecorded restores the stage recorded in version.suspended_from using
// reason for the published event. The v0.22 StageService uses it so a blocked
// round-trip is distinguishable from a paused resume.
func (m *StageMachine) resumeRecorded(ctx context.Context, versionID int64, reason string) error {
	current, err := m.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.SuspendedFrom == nil {
		return platform.StateConflict("version has no recorded suspended stage")
	}
	return m.resumeTo(ctx, current, *current.SuspendedFrom, reason)
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
	return m.resumeTo(ctx, current, target, "resumed")
}

// OnRequirementConfirmed applies the requirement hard gate
// (requirement_review -> designing), after freezing the selected requirement
// rows, and dispatches design.cases for them. An empty selection is expanded by
// the confirmer to every current draft requirement; a version with none is a
// typed 409 no_requirements_to_confirm. The version must be exactly at
// requirement_review: the graph also has a legitimate case_review -> designing
// reject edge, so the source stage must be pinned explicitly.
func (m *StageMachine) OnRequirementConfirmed(ctx context.Context, versionID int64, requirementIDs []int64) (StageView, error) {
	ids, err := normalizeStageIDs("requirement_ids", requirementIDs)
	if err != nil {
		return StageView{}, err
	}
	current, err := m.load(ctx, versionID)
	if err != nil {
		return StageView{}, err
	}
	if current.Stage != version.StageRequirementReview {
		return StageView{}, invalidStageForGate(stageGateRequirement, version.StageRequirementReview, current.Stage)
	}
	if m.requirements == nil {
		return StageView{}, fmt.Errorf("stage machine: requirement gate confirmer is not configured")
	}
	if err := m.requirements.BatchConfirm(ctx, versionID, ids); err != nil {
		return StageView{}, err
	}
	next, err := m.transitionFrom(ctx, current, version.StageDesigning, "requirement_confirmed", &StageJob{
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

// OnCasesConfirmed applies the case hard gate (case_review -> executing),
// after freezing the selected case rows, and dispatches execute.batch. A
// non-empty caseIDs list confirms only those cases (D23); an empty list means
// every confirmed-eligible case, and a version with no eligible draft case is a
// typed 409 no_cases_to_confirm. The version must be exactly at case_review.
func (m *StageMachine) OnCasesConfirmed(ctx context.Context, versionID int64, caseIDs []int64) (StageView, error) {
	ids, err := normalizeStageIDs("case_ids", caseIDs)
	if err != nil {
		return StageView{}, err
	}
	current, err := m.load(ctx, versionID)
	if err != nil {
		return StageView{}, err
	}
	if current.Stage != version.StageCaseReview {
		return StageView{}, invalidStageForGate(stageGateCase, version.StageCaseReview, current.Stage)
	}
	if m.cases == nil {
		return StageView{}, fmt.Errorf("stage machine: case gate confirmer is not configured")
	}
	if err := m.cases.ConfirmCases(ctx, versionID, ids); err != nil {
		return StageView{}, err
	}
	next, err := m.transitionFrom(ctx, current, version.StageExecuting, "cases_confirmed", &StageJob{
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
	reason = strings.TrimSpace(reason)
	if err := validateStageText("reason", reason, stageReasonMax); err != nil {
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

func (m *StageMachine) resumeTo(ctx context.Context, current version.StageState, target version.Stage, reason string) error {
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
	return m.apply(ctx, current, next, reason, "", nil)
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
		"actor":            stageEventActor(ctx),
		"stage_entered_at": to.StageEnteredAt.UTC().Format(time.RFC3339),
	}
	if blocker != "" {
		payload["blocker"] = blocker
	}
	versionID := platformid.Format(to.VersionID)
	scope := map[string]any{"version_id": versionID}
	// version.stage_changed is the stable 0.21 event name still consumed by the
	// eventing stage-transition reader and the current version page. v0.22
	// R-B4-1 specifies version.stage.changed: publish that name in parallel until
	// the Lead wiring (Task 37) migrates the consumers.
	for _, eventType := range []string{"version.stage_changed", "version.stage.changed"} {
		for _, channel := range []string{"version:" + versionID, "system"} {
			_ = m.events.Publish(ctx, eventingapp.Event{
				Channel:   channel,
				Type:      eventType,
				Scope:     scope,
				Payload:   payload,
				ActorID:   actorIDFrom(ctx),
				RequestID: requestid.From(ctx),
			})
		}
	}
}

// stageEventActor projects the authenticated caller into the stage event
// payload (R-B4-1 from/to/reason/actor). A background callback has no actor and
// is reported as system.
func stageEventActor(ctx context.Context) string {
	if actor, ok := platform.ActorFrom(ctx); ok {
		if actor.Role != "" {
			return actor.Role
		}
		if actor.ID > 0 {
			return platformid.Format(actor.ID)
		}
	}
	return "system"
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
// (spec §5.4). Stages without a stage-entry job return nil: case_review is
// entered after generation+review, so it must not enqueue review.cases.
func stageEntryJob(versionID int64, to version.Stage) *StageJob {
	kind := ""
	switch to {
	case version.StageDesigning:
		kind = StageJobDesignCases
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

// invalidStageForGate is the typed 409 returned when a hard gate is called at a
// stage other than its single source stage. Reusing the plain transition
// validator is not enough: the graph has legitimate back edges (for example
// case_review -> designing), so a gate must pin its source explicitly to avoid
// walking the version backwards. The details carry the required/current stage
// and a next action so the API and UI can guide the user instead of only
// showing the raw error (Task 78).
func invalidStageForGate(gate string, want, current version.Stage) error {
	return &platform.Error{
		Status: http.StatusConflict,
		Code:   "invalid_stage_for_gate",
		Message: fmt.Sprintf(
			"%s gate requires stage %s, but the version is at %s", gate, want, current,
		),
		Details: map[string]any{
			"gate":           gate,
			"required_stage": string(want),
			"current_stage":  string(current),
			"next_action":    gateNextAction(gate, want, current),
		},
	}
}

// gateNextAction turns a wrong-stage gate attempt into the user's next step.
// Both gates are reached through the deterministic workflow, so the guidance is
// expressed in product terms rather than internal stage names only.
func gateNextAction(gate string, want, current version.Stage) string {
	if gate == stageGateRequirement {
		switch current {
		case version.StageCreated, version.StageClarifying:
			return "finish clarification and confirm the requirement drafts, then retry the requirement gate"
		case version.StageDesigning, version.StageCaseReview, version.StageExecuting, version.StageReporting:
			return "the requirement gate is already past; continue with the current stage"
		}
		return fmt.Sprintf("reach stage %s before confirming requirements", want)
	}
	switch current {
	case version.StageCreated, version.StageClarifying, version.StageRequirementReview:
		return "confirm the requirements and start design; confirm the selected cases at the case gate and execute them again"
	case version.StageDesigning:
		return "wait for design generation to reach the case gate, then confirm and execute the selected cases"
	case version.StageCaseReview:
		return "retry the case gate"
	case version.StageExecuting, version.StageReporting:
		return "a batch is already executing; wait for it to finish or cancel it before confirming another batch"
	}
	return fmt.Sprintf("reach stage %s before confirming cases", want)
}

// ===========================================================================
// v0.22 StageService (Task 25, R-B4-1..R-B4-6)
// ===========================================================================

// StageService is the orchestrated v0.22 stage machine: the single application
// entry point Hosts and users may call (R-B4-1). It owns the forward workflow
// edges, validates every HTTP-level gate from spec §4.12/B §7.1, publishes
// version.stage.changed with from/to/reason/actor (the legacy
// version.stage_changed name is still emitted), records structured blockers as
// JSON in version.blocker and exposes the production callbacks:
//
//   - OnWaveTerminal(waveID): executing -> reporting after every wave is
//     terminal and the version report has been ensured;
//   - OnReportPublished(versionID): reporting -> completed after the version
//     report is published, no blocking issue is open and the version row closed.
//
// It composes the existing *StageMachine atomic core rather than duplicating the
// stage write: the StageMachine remains the only code that persists version.stage
// and the repository's ApplyStage keeps its compare-and-swap guard.
type StageService struct {
	machine     *StageMachine
	gates       StageGates
	waves       StageWaveReader
	waveControl StageWaveController
	reports     StageReports
	closer      StageVersionCloser
	handoff     StageHandoffGenerator
}

// StageServiceDeps wires the v0.22 stage service. Repo is required. Events,
// audit, jobs and clock are optional (the clock defaults to the system clock).
// Gates, Waves, Reports and Closer are required by the edge that needs them: a
// missing port fails closed with a typed gate_not_configured error instead of
// silently skipping the gate. WaveControl is required for Pause (and optional
// for Resume, which must not strand a version that has no running wave).
type StageServiceDeps struct {
	Repo        version.StageRepository
	Jobs        StageJobEnqueuer
	Audit       auditapp.Recorder
	Events      eventingapp.Publisher
	Clock       clock.Clock
	Gates       StageGates
	Waves       StageWaveReader
	WaveControl StageWaveController
	Reports     StageReports
	Closer      StageVersionCloser
	Handoff     StageHandoffGenerator
}

// NewStageService wires the v0.22 orchestrated stage machine.
func NewStageService(deps StageServiceDeps) *StageService {
	return &StageService{
		machine:     NewStageMachine(deps.Repo, deps.Jobs, deps.Audit, deps.Events, deps.Clock),
		gates:       deps.Gates,
		waves:       deps.Waves,
		waveControl: deps.WaveControl,
		reports:     deps.Reports,
		closer:      deps.Closer,
		handoff:     deps.Handoff,
	}
}

// Stage returns the current stage projection.
func (s *StageService) Stage(ctx context.Context, versionID int64) (StageView, error) {
	return s.machine.Stage(ctx, versionID)
}

// Request validates one requested stage and applies it. The target is carried
// explicitly because the graph has legitimate back edges (gate rejects) and the
// two suspension stages have their own entry points (Block/Pause): a Request for
// blocked or paused is always rejected.
//
// Request is idempotent: asking for the stage the version is already in is a
// no-op. A gate rejection returns *StageGateError and writes nothing; a
// non-graph transition returns *StageTransitionError.
func (s *StageService) Request(ctx context.Context, versionID int64, to version.Stage) error {
	if to == version.StageBlocked || to == version.StagePaused {
		return newStageTransitionError(version.Stage(""), to,
			"use Block for blocked and Pause for paused; Request only advances workflow stages")
	}
	current, err := s.machine.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage == to {
		return nil
	}
	if current.Stage.IsSuspended() {
		return newStageTransitionError(current.Stage, to,
			"resume or unblock the version before requesting another stage")
	}
	if !current.Stage.CanTransitionTo(to) {
		return newStageTransitionError(current.Stage, to, "")
	}
	if err := s.checkGate(ctx, current.VersionID, current.Stage, to); err != nil {
		return err
	}
	if err := s.machine.Transition(ctx, current.VersionID, to, stageTransitionReason(current.Stage, to)); err != nil {
		return err
	}
	s.runPostTransition(ctx, current.VersionID, current.Stage, to)
	return nil
}

// Advance requests the next workflow stage of the deterministic graph.
func (s *StageService) Advance(ctx context.Context, versionID int64) error {
	current, err := s.machine.load(ctx, versionID)
	if err != nil {
		return err
	}
	next, ok := nextWorkflowStage(current.Stage)
	if !ok {
		return newStageTransitionError(current.Stage, version.Stage(""),
			"the version has no automatic next stage")
	}
	return s.Request(ctx, versionID, next)
}

// Block enters blocked with a structured reason/action/next (R-B4-4). The JSON
// encoding is stored in version.blocker (TEXT, ruling C6) and the previous
// workflow stage is recorded in version.suspended_from.
func (s *StageService) Block(ctx context.Context, versionID int64, blocker Blocker) error {
	normalized, err := blocker.Normalize()
	if err != nil {
		return err
	}
	encoded, err := normalized.JSON()
	if err != nil {
		return err
	}
	if _, err := s.machine.load(ctx, versionID); err != nil {
		return err
	}
	return s.machine.Suspend(ctx, versionID, version.StageBlocked, encoded)
}

// Unblock resumes a blocked version to its recorded stage. R-B4-4 requires the
// reason to be cleared first: the BlockerCleared port must answer true; nil
// fails closed. The blocker row is cleared by the repository write.
func (s *StageService) Unblock(ctx context.Context, versionID int64) error {
	current, err := s.machine.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StageBlocked {
		return newStageTransitionError(current.Stage, version.Stage(""),
			"only a blocked version can be unblocked")
	}
	if s.gates.BlockerCleared == nil {
		return newStageGateNotConfigured("blocker")
	}
	blocker := ParseBlocker(current.Blocker)
	cleared, err := s.gates.BlockerCleared(ctx, versionID, blocker)
	if err != nil {
		return newStageGateError("blocker", "gate_check_failed",
			"retry the blocker clearance check", "unblock", err)
	}
	if !cleared {
		return newStageGateError("blocker", "blocker_not_cleared",
			blocker.Action, blocker.Next, nil)
	}
	return s.machine.resumeRecorded(ctx, versionID, "unblocked")
}

// Pause enters paused and asks the execution context to gracefully pause the
// version's running waves (R-B4-5 minimal paused support). No complex
// scheduling is attempted.
func (s *StageService) Pause(ctx context.Context, versionID int64, reason string) error {
	current, err := s.machine.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage.IsSuspended() {
		return newStageTransitionError(current.Stage, version.StagePaused,
			"the version is already suspended")
	}
	if !current.Stage.CanTransitionTo(version.StagePaused) {
		return newStageTransitionError(current.Stage, version.StagePaused,
			"only a workflow version can be paused")
	}
	if s.waveControl == nil {
		return newStageGateNotConfigured("wave_control")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "user_paused"
	}
	var actorID int64
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID = actor.ID
	}
	if err := s.waveControl.PauseVersionWaves(ctx, versionID, reason, actorID); err != nil {
		return err
	}
	if err := s.machine.Suspend(ctx, versionID, version.StagePaused, reason); err != nil {
		// The waves are already pausing; continue them rather than leaving the
		// execution stranded behind a failed stage write.
		_ = s.waveControl.ResumeVersionWaves(ctx, versionID)
		return err
	}
	return nil
}

// Resume leaves paused and continues the paused waves (best effort: the stage
// resume is the truth and a wave resume failure is retried by the execution
// reconciler).
func (s *StageService) Resume(ctx context.Context, versionID int64) error {
	current, err := s.machine.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StagePaused {
		return newStageTransitionError(current.Stage, version.Stage(""),
			"only a paused version can be resumed; use Unblock for blocked")
	}
	if err := s.machine.Resume(ctx, versionID); err != nil {
		return err
	}
	if s.waveControl != nil {
		if err := s.waveControl.ResumeVersionWaves(ctx, versionID); err != nil {
			return err
		}
	}
	return nil
}

// OnWaveTerminal is the production wave-terminal callback (R-B4-3). The wave ID
// is resolved to its version, the wave must be terminal, and every other wave of
// the version must be terminal too before executing -> reporting advances.
// Calls for versions that are not executing (or are suspended/completed) are
// idempotent no-ops so duplicate or late events are harmless.
func (s *StageService) OnWaveTerminal(ctx context.Context, waveID int64) error {
	if waveID <= 0 {
		return platform.Invalid("wave_id", "must be a positive decimal ID")
	}
	if s.waves == nil {
		return newStageGateNotConfigured("wave")
	}
	wave, err := s.waves.GetWave(ctx, waveID)
	if err != nil {
		return err
	}
	if wave.VersionID <= 0 {
		return newStageGateError("wave", "wave_not_scoped",
			"link the wave to a version", "report", nil)
	}
	current, err := s.machine.load(ctx, wave.VersionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StageExecuting {
		return nil
	}
	if !wave.Terminal {
		return newStageGateError("wave", "wave_not_terminal",
			"wait for the wave to reach a terminal state", "reporting", nil)
	}
	return s.Request(ctx, wave.VersionID, version.StageReporting)
}

// OnReportPublished is the production report.published callback (R-B4-3). It
// completes a reporting version after the report is published, no blocking issue
// remains open and the version row is closed. Duplicate deliveries are no-ops.
func (s *StageService) OnReportPublished(ctx context.Context, versionID int64) error {
	current, err := s.machine.load(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StageReporting {
		return nil
	}
	return s.Request(ctx, versionID, version.StageCompleted)
}

// ---------- gate evaluation ----------

// checkGate dispatches the one gate that guards the requested edge (B §7.1).
func (s *StageService) checkGate(ctx context.Context, versionID int64, from, to version.Stage) error {
	switch {
	case from == version.StageCreated && to == version.StageClarifying:
		return s.requireEnvironmentBound(ctx, versionID)
	case from == version.StageClarifying && to == version.StageRequirementReview:
		return s.requireRequirements(ctx, versionID)
	case from == version.StageRequirementReview && to == version.StageDesigning:
		return s.requireConfirmedRequirements(ctx, versionID)
	case from == version.StageDesigning && to == version.StageCaseReview:
		return s.requireCaseDrafts(ctx, versionID)
	case from == version.StageCaseReview && to == version.StageExecuting:
		return s.requireExecutable(ctx, versionID)
	case from == version.StageExecuting && to == version.StageReporting:
		return s.requireReportReady(ctx, versionID)
	case from == version.StageReporting && to == version.StageCompleted:
		return s.requireCompletable(ctx, versionID)
	default:
		return nil
	}
}

func (s *StageService) requireEnvironmentBound(ctx context.Context, versionID int64) error {
	if s.gates.EnvironmentBound == nil {
		return newStageGateNotConfigured("environment")
	}
	bound, err := s.gates.EnvironmentBound(ctx, versionID)
	if err != nil {
		return newStageGateError("environment", "gate_check_failed",
			"retry the environment binding check", "clarifying", err)
	}
	if !bound {
		return newStageGateError("environment", "environment_not_bound",
			"bind a SUT environment to the version", "clarifying", nil)
	}
	return nil
}

// requireRequirements is C-2: only the requirement count opens the gate. No
// pending question state participates, so a pending question never blocks the
// stage (spec §4.11 R-B3-4, §4.12 R-B4-2).
func (s *StageService) requireRequirements(ctx context.Context, versionID int64) error {
	if s.gates.RequirementCount == nil {
		return newStageGateNotConfigured("requirement")
	}
	count, err := s.gates.RequirementCount(ctx, versionID)
	if err != nil {
		return newStageGateError("requirement", "gate_check_failed",
			"retry the requirement count check", "requirement_review", err)
	}
	if count < 1 {
		return newStageGateError("requirement", "missing_requirement",
			"add at least one requirement in the clarification conversation",
			"requirement_review", nil)
	}
	return nil
}

func (s *StageService) requireConfirmedRequirements(ctx context.Context, versionID int64) error {
	if s.gates.RequirementsConfirmed == nil {
		return newStageGateNotConfigured("requirement_confirmation")
	}
	confirmed, err := s.gates.RequirementsConfirmed(ctx, versionID)
	if err != nil {
		return newStageGateError("requirement_confirmation", "gate_check_failed",
			"retry the requirement confirmation check", "designing", err)
	}
	if !confirmed {
		return newStageGateError("requirement_confirmation", "requirements_unconfirmed",
			"confirm every current requirement before starting design",
			"designing", nil)
	}
	if s.gates.OpenGaps == nil {
		return newStageGateNotConfigured("requirement_gap")
	}
	gaps, err := s.gates.OpenGaps(ctx, versionID)
	if err != nil {
		return newStageGateError("requirement_gap", "gate_check_failed",
			"retry the requirement gap check", "designing", err)
	}
	if gaps > 0 {
		return newStageGateError("requirement_gap", "open_gap",
			"resolve or explicitly accept every open requirement gap",
			"designing", nil)
	}
	return nil
}

func (s *StageService) requireCaseDrafts(ctx context.Context, versionID int64) error {
	if s.gates.ConfirmedCaseDrafts == nil {
		return newStageGateNotConfigured("case_draft")
	}
	count, err := s.gates.ConfirmedCaseDrafts(ctx, versionID)
	if err != nil {
		return newStageGateError("case_draft", "gate_check_failed",
			"retry the case draft check", "case_review", err)
	}
	if count < 1 {
		return newStageGateError("case_draft", "no_confirmed_case_draft",
			"generate at least one confirmed case draft in the design pass",
			"case_review", nil)
	}
	if s.gates.CoverageComputed == nil {
		return newStageGateNotConfigured("coverage")
	}
	computed, err := s.gates.CoverageComputed(ctx, versionID)
	if err != nil {
		return newStageGateError("coverage", "gate_check_failed",
			"retry the coverage matrix check", "case_review", err)
	}
	if !computed {
		return newStageGateError("coverage", "coverage_not_computed",
			"compute the coverage matrix before reviewing cases",
			"case_review", nil)
	}
	return nil
}

func (s *StageService) requireExecutable(ctx context.Context, versionID int64) error {
	if s.gates.ConfirmedCases == nil {
		return newStageGateNotConfigured("case_confirmation")
	}
	confirmed, err := s.gates.ConfirmedCases(ctx, versionID)
	if err != nil {
		return newStageGateError("case_confirmation", "gate_check_failed",
			"retry the case confirmation check", "executing", err)
	}
	if confirmed < 1 {
		return newStageGateError("case_confirmation", "cases_unconfirmed",
			"confirm at least one case before execution", "executing", nil)
	}
	if s.gates.ExecutorOnline == nil {
		return newStageGateNotConfigured("executor")
	}
	online, err := s.gates.ExecutorOnline(ctx)
	if err != nil {
		return newStageGateError("executor", "gate_check_failed",
			"retry the executor health check", "executing", err)
	}
	if !online {
		return newStageGateError("executor", "executor_offline",
			"start the executor and wait for its heartbeat", "executing", nil)
	}
	if s.gates.AccountsValid == nil {
		return newStageGateNotConfigured("account")
	}
	valid, err := s.gates.AccountsValid(ctx, versionID)
	if err != nil {
		return newStageGateError("account", "gate_check_failed",
			"retry the account and target database check", "executing", err)
	}
	if !valid {
		return newStageGateError("account", "account_invalid",
			"fix the account or target database binding", "executing", nil)
	}
	return nil
}

func (s *StageService) requireReportReady(ctx context.Context, versionID int64) error {
	if s.gates.WavesTerminal == nil {
		return newStageGateNotConfigured("wave")
	}
	terminal, err := s.gates.WavesTerminal(ctx, versionID)
	if err != nil {
		return newStageGateError("wave", "gate_check_failed",
			"retry the wave terminal check", "reporting", err)
	}
	if !terminal {
		return newStageGateError("wave", "waves_active",
			"wait for every execution wave to reach a terminal state",
			"reporting", nil)
	}
	if s.reports == nil {
		return newStageGateNotConfigured("report")
	}
	if err := s.reports.EnsureVersionReport(ctx, versionID); err != nil {
		return newStageGateError("report", "report_not_generated",
			"generate the version report", "reporting", err)
	}
	return nil
}

func (s *StageService) requireCompletable(ctx context.Context, versionID int64) error {
	if s.gates.ReportPublished == nil {
		return newStageGateNotConfigured("report")
	}
	published, err := s.gates.ReportPublished(ctx, versionID)
	if err != nil {
		return newStageGateError("report", "gate_check_failed",
			"retry the report publication check", "completed", err)
	}
	if !published {
		return newStageGateError("report", "report_not_published",
			"publish the version report before completing the version",
			"completed", nil)
	}
	if s.gates.OpenBlockingIssues == nil {
		return newStageGateNotConfigured("issue")
	}
	open, err := s.gates.OpenBlockingIssues(ctx, versionID)
	if err != nil {
		return newStageGateError("issue", "gate_check_failed",
			"retry the blocking issue check", "completed", err)
	}
	if open > 0 {
		return newStageGateError("issue", "open_blocking_issue",
			"close or dismiss every blocking issue before completing the version",
			"completed", nil)
	}
	if s.closer == nil {
		return newStageGateNotConfigured("version_close")
	}
	if err := s.closer.CloseVersion(ctx, versionID, "stage_completed"); err != nil {
		return newStageGateError("version_close", "version_close_failed",
			"resolve the version close predicate", "completed", err)
	}
	return nil
}

// runPostTransition performs the best-effort hooks after the stage write: the
// Task 27 handoff generation when design starts. Its own reconciler retries a
// missed generation, so a failure never blocks the stage.
func (s *StageService) runPostTransition(ctx context.Context, versionID int64, from, to version.Stage) {
	if from == version.StageRequirementReview && to == version.StageDesigning && s.handoff != nil {
		_, _ = s.handoff.Generate(ctx, versionID)
	}
}

// stageTransitionReason is the event reason of one forward edge.
func stageTransitionReason(from, to version.Stage) string {
	switch {
	case from == version.StageCreated && to == version.StageClarifying:
		return "environment_bound"
	case from == version.StageClarifying && to == version.StageRequirementReview:
		return "requirements_ready"
	case from == version.StageRequirementReview && to == version.StageDesigning:
		return "requirements_confirmed"
	case from == version.StageDesigning && to == version.StageCaseReview:
		return "cases_ready"
	case from == version.StageCaseReview && to == version.StageExecuting:
		return "cases_confirmed"
	case from == version.StageExecuting && to == version.StageReporting:
		return "wave_terminal"
	case from == version.StageReporting && to == version.StageCompleted:
		return "report_published"
	default:
		return "stage_requested"
	}
}

// nextWorkflowStage is the automatic next stage used by Advance.
func nextWorkflowStage(stage version.Stage) (version.Stage, bool) {
	switch stage {
	case version.StageCreated:
		return version.StageClarifying, true
	case version.StageClarifying:
		return version.StageRequirementReview, true
	case version.StageRequirementReview:
		return version.StageDesigning, true
	case version.StageDesigning:
		return version.StageCaseReview, true
	case version.StageCaseReview:
		return version.StageExecuting, true
	case version.StageExecuting:
		return version.StageReporting, true
	case version.StageReporting:
		return version.StageCompleted, true
	default:
		return version.Stage(""), false
	}
}
