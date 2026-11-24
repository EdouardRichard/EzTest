package designapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Confirmation judgment error codes. THE single Go authority for the two
// judgment gate conflict codes (spec SC-E5); the HTTP contract documents these
// values, never the reverse.
const (
	// JudgmentPendingCode marks a case version whose judge review is not
	// finished (review_state pending or reworking).
	JudgmentPendingCode = "judgment_pending"
	// JudgmentFailedCode marks a case version escalated to a human (review
	// state escalated_fail or judge_unavailable) without a valid override.
	JudgmentFailedCode = "judgment_failed"
)

// JudgmentGate is the confirmation-time view of the independent judge facts.
// ReviewStateFor is the single review-state authority (T6 JudgmentService);
// the two fact reads let the confirmation predicate decide override validity,
// which the projection deliberately does not fold in.
type JudgmentGate interface {
	ReviewStateFor(ctx context.Context, versionID, caseID, currentVersionID int64) (design.ReviewState, error)
	LatestOverride(ctx context.Context, caseID, caseVersionID int64) (*design.Override, error)
	LatestJudgment(ctx context.Context, caseID int64) (*design.Judgment, error)
}

// judgmentGate composes the review-state projection with the append-only fact
// reads. Both halves are required: the projection answers "what state is this
// case version in", the facts answer "was a human decision made against this
// exact content, after the latest judge fact".
type judgmentGate struct {
	states *JudgmentService
	facts  JudgmentRepository
}

// NewJudgmentGate builds the confirmation gate from the judgment service and
// its fact repository. The composition root wires it onto the graph service.
func NewJudgmentGate(states *JudgmentService, facts JudgmentRepository) JudgmentGate {
	return judgmentGate{states: states, facts: facts}
}

func (g judgmentGate) ReviewStateFor(ctx context.Context, versionID, caseID, currentVersionID int64) (design.ReviewState, error) {
	return g.states.ReviewStateFor(ctx, versionID, caseID, currentVersionID)
}

func (g judgmentGate) LatestOverride(ctx context.Context, caseID, caseVersionID int64) (*design.Override, error) {
	return g.facts.LatestOverride(ctx, caseID, caseVersionID)
}

func (g judgmentGate) LatestJudgment(ctx context.Context, caseID int64) (*design.Judgment, error) {
	return g.facts.LatestJudgment(ctx, caseID)
}

// SetJudgmentGate attaches the judgment gate consumed by Confirm and by the
// explicit-selection precheck. It returns the service for fluent wiring.
// Without a gate the confirmation path fails closed (constitution VII.6).
func (s *GraphService) SetJudgmentGate(gate JudgmentGate) *GraphService {
	if s != nil {
		s.judgment = gate
	}
	return s
}

// ConfirmInput is one POST /design/confirmations request. Confirm is the SINGLE
// terminal entry for case confirmation and every live caller is a human action:
// the HTTP handler passes Scope/TargetID/BatchID only, and the internal
// case/requirement/"all" fan-out passes Scope/TargetID only.
//
// The four auto-confirm fields below are **reserved, expiry version 0.3**
// (constitution III.7: a dangling surface must be wired, deleted, or explicitly
// marked reserved with an expiry). R-E2r abolished discovery auto-confirm, so
// they have ZERO production setters today (verified by grep over ConfirmInput
// literals); they are kept only because the confirmation write set and the
// case.confirmed audit metadata still read them, and pre-E historical rows carry
// the values. The HTTP surface cannot set them, so a system confirmation can
// never be forged through the API. The stale "spec R-EX-6" reference is removed:
// that spec's automatic-confirmation path no longer exists (spec SC-E6 / R-E2r).
// 0.3 decision: delete the fields together with the confirmation columns'
// auto-confirm write path, or wire them if a new internal producer is approved.
type ConfirmInput struct {
	Scope    string
	TargetID int64
	BatchID  string
	// AutoConfirmed marked an internal system confirmation. Trigger was
	// "discovery_delta" for the additive revision; SourceObservationID kept the
	// originating execution observation on the confirmation audit row.
	// reserved (0.3): no production setter since R-E2r.
	AutoConfirmed       bool
	AutoConfirmedBy     string
	Trigger             string
	SourceObservationID *int64
}

// ConfirmedCaseView is one confirmed case version.
type ConfirmedCaseView struct {
	CaseID        int64
	CaseVersionID int64
}

// SkippedCaseView is one case version skipped as already confirmed/stale.
type SkippedCaseView struct {
	CaseID        int64
	CaseVersionID int64
	Status        string
}

// ConfirmResult is the confirmation response payload.
type ConfirmResult struct {
	Scope       string
	TargetID    int64
	BatchID     string
	Confirmed   []ConfirmedCaseView
	Skipped     []SkippedCaseView
	ConfirmedAt time.Time
}

// countOrLen prefers a projection count and falls back to the loaded slice for
// fakes/legacy reads that do not materialize the count columns.
func countOrLen(count, length int) int {
	if count > 0 {
		return count
	}
	return length
}

// ConfirmCasesByIDs freezes explicit current-version cases selected by the
// version case hard gate. An empty list confirms every confirmed-eligible
// current case of the version (the POST /stage/confirm-cases "all" case); a
// version with no eligible draft case is a typed 409 no_cases_to_confirm, since
// spec §5.3 only enters executing when confirmed cases exist.
//
// It is deliberately layered on the single confirmation use case: stale,
// foreign, already-confirmed and old-requirement-version cases get exactly the
// same typed behavior as POST /design/confirmations, and the gate only advances
// when every confirmation succeeded.
func (s *GraphService) ConfirmCasesByIDs(ctx context.Context, versionID int64, caseIDs []int64) error {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return err
	}
	ids, err := normalizeCaseSelectionIDs(caseIDs)
	if err != nil {
		return err
	}
	actorID := confirmationActorID(ctx)
	if len(ids) == 0 {
		confirmed, err := s.confirmAllCases(ctx, versionID, actorID)
		if err != nil {
			return err
		}
		if confirmed == 0 {
			return platform.NewError(http.StatusConflict, "no_cases_to_confirm",
				"the version has no draft case to confirm")
		}
		return nil
	}

	// Validate the whole selection before changing anything, so a foreign or
	// missing ID can never leave an earlier case of the same selection
	// confirmed while the gate reports failure.
	targets := make([]design.ConfirmationTarget, 0, len(ids))
	for _, caseID := range ids {
		target, err := s.repo.ResolveConfirmationTarget(ctx, versionID, design.ConfirmScopeCase, caseID)
		if err != nil {
			return mapDesignError(err, "case")
		}
		targets = append(targets, target)
	}
	// Task 95: an incomplete draft version (the PO O8 1-step/0-assertion
	// version) must block the whole selected batch before any sibling is frozen.
	// Confirming a complete sibling first and then failing on the incomplete one
	// would leave a partially confirmed selection behind a failed gate.
	drafts := make([]design.CaseWithCurrent, 0, len(targets))
	for _, target := range targets {
		data, err := s.repo.ReadCase(ctx, versionID, target.CaseID, nil)
		if err != nil {
			return mapDesignError(err, "case")
		}
		if data.Selected.Status != design.CaseStatusDraft {
			continue
		}
		item := design.CaseWithCurrent{Case: data.Case, Current: &data.Selected}
		if err := s.validateConfirmableCaseVersion(ctx, versionID, item); err != nil {
			return err
		}
		drafts = append(drafts, item)
	}
	// Spec SC-E5: the judgment gate is part of the same validate-before-write
	// block, so one unreviewed draft blocks the whole selection.
	if err := s.validateJudgmentForCases(ctx, versionID, drafts); err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := s.Confirm(ctx, versionID, ConfirmInput{
			Scope: design.ConfirmScopeCase, TargetID: target.CaseID,
		}, actorID); err != nil {
			return err
		}
	}
	return nil
}

// confirmAllCases confirms every draft current case of the version by reusing
// the requirement-scope confirmation, one clarified requirement at a time, and
// reports how many case versions were frozen. A requirement without draft cases
// is skipped: "all" means all eligible cases, not "fail when some requirement
// has nothing to confirm". Zero confirmations means the version has no eligible
// draft case, which the gate rejects instead of advancing to executing.
func (s *GraphService) confirmAllCases(ctx context.Context, versionID int64, actorID int64) (int, error) {
	graph, err := s.repo.ReadGraph(ctx, versionID)
	if err != nil {
		return 0, mapDesignError(err, "version")
	}
	// Spec SC-E5: "all" spans every requirement, but each requirement is
	// confirmed in its own transaction. Without a version-wide judgment
	// precheck the first requirements would already be frozen when a later one
	// is blocked, leaving a partially confirmed version behind a failed gate.
	// Validate every eligible draft of the version before the first write.
	//
	// SANCTIONED TOCTOU (H-F10 T7): this precheck reads the graph OUTSIDE any
	// confirmation lock, so a judge verdict landing between the precheck and a
	// later requirement's transaction is not seen here. It is a fast-fail
	// convenience, never the gate: each Confirm re-validates under
	// WithConfirmationLock inside its own transaction and answers 409, so no
	// case is ever frozen on a stale precheck. What the precheck cannot remove
	// is the partial-progress window for NON-judgment reasons (completeness /
	// non-clarified requirements are checked per requirement, H-F4 — an E-era
	// ruling H does not reopen).
	//
	// Pool note (H-F10 T7): the precheck runs before the first Confirm
	// transaction, so it does not hold a connection while reading; the
	// per-requirement transactions each take one connection in turn. This is
	// pool pressure under concurrent confirmations, not a lock cycle.
	drafts := make([]design.CaseWithCurrent, 0)
	for _, requirement := range graph.Requirements {
		drafts = append(drafts, draftCurrentCases(requirement)...)
	}
	if err := s.validateJudgmentForCases(ctx, versionID, drafts); err != nil {
		return 0, err
	}
	confirmed := 0
	for _, requirement := range graph.Requirements {
		if !hasDraftCurrentCase(requirement) {
			continue
		}
		result, err := s.Confirm(ctx, versionID, ConfirmInput{
			Scope: design.ConfirmScopeRequirement, TargetID: requirement.Requirement.ID,
		}, actorID)
		if err != nil {
			return 0, err
		}
		confirmed += len(result.Confirmed)
	}
	return confirmed, nil
}

// hasDraftCurrentCase reports whether a requirement graph node has at least one
// draft case on a scenario of the requirement's current revision.
func hasDraftCurrentCase(requirement design.RequirementGraph) bool {
	return len(draftCurrentCases(requirement)) > 0
}

// draftCurrentCases collects the draft current case versions of one requirement
// graph node. Cases bound to an older requirement revision are excluded,
// matching ConfirmationTx.CasesByRequirement. THE single selection authority
// for the "all" confirmation path: the version-wide judgment precheck and the
// per-requirement loop must agree on which cases are about to be frozen.
func draftCurrentCases(requirement design.RequirementGraph) []design.CaseWithCurrent {
	drafts := make([]design.CaseWithCurrent, 0)
	for _, scenario := range requirement.Scenarios {
		if scenario.Scenario.RequirementVersionID != requirement.Requirement.CurrentVersionID {
			continue
		}
		for _, item := range scenario.Cases {
			if item.Current.Status != design.CaseStatusDraft {
				continue
			}
			current := item.Current
			drafts = append(drafts, design.CaseWithCurrent{Case: item.Case, Current: &current})
		}
	}
	return drafts
}

func normalizeCaseSelectionIDs(raw []int64) ([]int64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for index, id := range raw {
		if id <= 0 {
			return nil, platform.Invalid(fmt.Sprintf("case_ids[%d]", index), "must be a positive decimal ID")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func confirmationActorID(ctx context.Context) int64 {
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		return actor.ID
	}
	return 0
}

// Confirm freezes the selected draft case versions in one transaction. The
// selection universe is case / scenario / requirement (spec §4.5).
func (s *GraphService) Confirm(ctx context.Context, versionID int64, in ConfirmInput, actorID int64) (ConfirmResult, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return ConfirmResult{}, err
	}
	if err := design.ValidateConfirmationScope(in.Scope); err != nil {
		return ConfirmResult{}, err
	}
	if err := requirePositiveID("target_id", in.TargetID); err != nil {
		return ConfirmResult{}, err
	}
	if err := design.ValidateBatchID(in.BatchID); err != nil {
		return ConfirmResult{}, err
	}

	target, err := s.repo.ResolveConfirmationTarget(ctx, versionID, in.Scope, in.TargetID)
	if err != nil {
		return ConfirmResult{}, mapDesignError(err, "design confirmation target")
	}
	batchID := strings.TrimSpace(in.BatchID)
	if batchID == "" {
		generated, err := design.NewBatchID()
		if err != nil {
			return ConfirmResult{}, err
		}
		batchID = generated
	}
	now := s.clock.Now().UTC()
	result := ConfirmResult{
		Scope:       in.Scope,
		TargetID:    in.TargetID,
		BatchID:     batchID,
		Confirmed:   []ConfirmedCaseView{},
		Skipped:     []SkippedCaseView{},
		ConfirmedAt: now,
	}
	supersededByCase := map[int64][]int64{}

	err = s.repo.WithConfirmationLock(ctx, versionID, target.RequirementID, func(ctx context.Context, tx design.ConfirmationTx) error {
		requirement, err := tx.Requirement(ctx)
		if err != nil {
			return err
		}
		if requirement.Status != string(version.RequirementClarified) {
			return platform.Invalid("scope", "the requirement must be clarified before its cases can be confirmed")
		}

		var candidates []design.CaseWithCurrent
		scenarioVersionChecked := false
		switch in.Scope {
		case design.ConfirmScopeCase:
			item, err := tx.Case(ctx, target.CaseID)
			if err != nil {
				return err
			}
			scenario, err := tx.Scenario(ctx, item.Case.ScenarioID)
			if err != nil {
				return err
			}
			if scenario.RequirementVersionID != requirement.CurrentVersionID {
				return platform.Invalid("target_id", "the case is bound to an older requirement version")
			}
			candidates = []design.CaseWithCurrent{item}
		case design.ConfirmScopeScenario:
			scenario, err := tx.Scenario(ctx, target.ScenarioID)
			if err != nil {
				return err
			}
			if scenario.RequirementVersionID != requirement.CurrentVersionID {
				return platform.Invalid("target_id", "the scenario is bound to an older requirement version")
			}
			scenarioVersionChecked = true
			if candidates, err = tx.CasesByScenario(ctx, target.ScenarioID); err != nil {
				return err
			}
		case design.ConfirmScopeRequirement:
			if candidates, err = tx.CasesByRequirement(ctx); err != nil {
				return err
			}
		}
		_ = scenarioVersionChecked

		toConfirm := make([]design.CaseWithCurrent, 0, len(candidates))
		for _, item := range candidates {
			if item.Current == nil {
				continue
			}
			switch item.Current.Status {
			case design.CaseStatusDraft:
				toConfirm = append(toConfirm, item)
			case design.CaseStatusConfirmed:
				result.Skipped = append(result.Skipped, SkippedCaseView{
					CaseID: item.Case.ID, CaseVersionID: item.Current.ID, Status: item.Current.Status,
				})
			case design.CaseStatusStale:
				if in.Scope == design.ConfirmScopeCase {
					return platform.StateConflict("the current case version is stale; redesign it before confirming")
				}
				result.Skipped = append(result.Skipped, SkippedCaseView{
					CaseID: item.Case.ID, CaseVersionID: item.Current.ID, Status: item.Current.Status,
				})
			default:
				if in.Scope == design.ConfirmScopeCase {
					return platform.StateConflict("the current case version cannot be confirmed in its current state")
				}
				result.Skipped = append(result.Skipped, SkippedCaseView{
					CaseID: item.Case.ID, CaseVersionID: item.Current.ID, Status: item.Current.Status,
				})
			}
		}
		if len(toConfirm) == 0 {
			if in.Scope == design.ConfirmScopeCase {
				// A repeated confirmation of the current version is idempotent.
				return nil
			}
			return platform.StateConflict("no draft case version is available for this confirmation scope")
		}
		// Task 95: a bulk confirmation is atomic and may only freeze complete
		// versions. Validate every candidate before the first write so a
		// version that lost its designed steps/assertions cannot be confirmed
		// alongside its complete siblings.
		for _, item := range toConfirm {
			if err := s.validateConfirmableCaseVersion(ctx, versionID, item); err != nil {
				return err
			}
		}
		// Spec SC-E5: the same atomic block carries the judgment gate. A
		// scenario/requirement confirmation may only freeze case versions whose
		// judge review is complete (or human-overridden); one unreviewed draft
		// blocks the whole scope before the first write.
		if err := s.validateJudgmentForCases(ctx, versionID, toConfirm); err != nil {
			return err
		}

		affected := make([]string, 0, len(toConfirm))
		for _, item := range toConfirm {
			affected = append(affected, platformid.Format(item.Current.ID))
		}
		for _, item := range toConfirm {
			if item.Current.ID <= 0 {
				continue
			}
			snapshot := map[string]any{
				"title":           item.Current.Title,
				"priority":        item.Current.Priority,
				"step_count":      countOrLen(item.Current.StepCount, len(item.Current.Steps)),
				"assertion_count": countOrLen(item.Current.AssertionCount, len(item.Current.Assertions)),
				"source_count":    countOrLen(item.Current.SourceCount, len(item.Current.Sources)),
				"content_hash":    item.Current.ContentHash,
			}
			confirmation := design.Confirmation{
				ID:                     s.ids.Next(),
				CaseVersionID:          item.Current.ID,
				Scope:                  in.Scope,
				TargetScopeID:          in.TargetID,
				BatchID:                batchID,
				AffectedCaseVersionIDs: affected,
				Snapshot:               snapshot,
				ConfirmedBy:            actorID,
				ConfirmedAt:            now,
				AutoConfirmed:          in.AutoConfirmed,
				AutoConfirmedBy:        in.AutoConfirmedBy,
				Trigger:                in.Trigger,
				SourceObservationID:    in.SourceObservationID,
			}
			if err := tx.ConfirmCaseVersion(ctx, design.ConfirmCaseVersionCommand{
				Confirmation: confirmation,
				EntityStatus: design.CaseStatusConfirmed,
			}); err != nil {
				return err
			}
			oldVersionIDs, err := tx.SupersededVersionIDs(ctx, item.Case.ID, item.Current.ID)
			if err != nil {
				return err
			}
			if len(oldVersionIDs) > 0 {
				if err := tx.SupersedeVersions(ctx, item.Case.ID, item.Current.ID, now); err != nil {
					return err
				}
				supersededByCase[item.Case.ID] = oldVersionIDs
			}
			result.Confirmed = append(result.Confirmed, ConfirmedCaseView{
				CaseID: item.Case.ID, CaseVersionID: item.Current.ID,
			})
		}
		return nil
	})
	if err != nil {
		return ConfirmResult{}, mapDesignError(err, "design confirmation")
	}
	if len(result.Confirmed) == 0 {
		return result, nil
	}

	caseIDs := make([]int64, 0, len(result.Confirmed))
	caseVersionIDs := make([]int64, 0, len(result.Confirmed))
	for _, item := range result.Confirmed {
		caseIDs = append(caseIDs, item.CaseID)
		caseVersionIDs = append(caseVersionIDs, item.CaseVersionID)
	}
	s.publishEvent(ctx, versionID, "design.case_confirmed", target.RequirementID, map[string]any{
		"scope":            in.Scope,
		"target_id":        platformid.Format(in.TargetID),
		"batch_id":         batchID,
		"case_ids":         formatIDList(caseIDs),
		"case_version_ids": formatIDList(caseVersionIDs),
	}, actorID)
	for caseID, versionIDs := range supersededByCase {
		for _, supersededVersionID := range versionIDs {
			s.publishEvent(ctx, versionID, "design.case_version_superseded", target.RequirementID, map[string]any{
				"case_id":         platformid.Format(caseID),
				"case_version_id": platformid.Format(supersededVersionID),
			}, actorID)
		}
	}
	if s.audit != nil {
		metadata := map[string]any{
			"scope":            in.Scope,
			"target_id":        platformid.Format(in.TargetID),
			"batch_id":         batchID,
			"case_version_ids": formatIDList(caseVersionIDs),
		}
		if in.AutoConfirmed {
			metadata["auto_confirmed"] = true
			metadata["auto_confirmed_by"] = in.AutoConfirmedBy
			metadata["trigger"] = in.Trigger
			if in.SourceObservationID != nil {
				metadata["source_observation_id"] = platformid.Format(*in.SourceObservationID)
			}
		}
		event := auditapp.Event{
			Action:     "case.confirmed",
			TargetKind: "case",
			TargetID:   platformid.Format(in.TargetID),
			RequestID:  requestid.From(ctx),
			Metadata:   metadata,
		}
		if actorID > 0 {
			actor := actorID
			event.ActorID = &actor
		}
		_ = s.audit.Record(ctx, event)
	}
	s.reopenCompleted(ctx, versionID, "case confirmed")
	return result, nil
}

// validateConfirmableCaseVersion enforces the confirmed-version rule: a case
// version may only enter the confirmed set when it is complete (Task 95 finalize
// rule). The PO O8 AC-FR-011-01 version (1 step, 0 assertions) was confirmed and
// then executed into inconclusive(incomplete); confirmation must instead return
// a typed repair request before anything is frozen.
func (s *GraphService) validateConfirmableCaseVersion(ctx context.Context, versionID int64, item design.CaseWithCurrent) error {
	if item.Current == nil {
		return nil
	}
	if !design.CaseVersionComplete(*item.Current) {
		return incompleteCaseVersionError(item)
	}
	return nil
}

// incompleteCaseVersionError is the typed 409 block/repair response for an
// incomplete case version. The message carries the counts and the repair action
// because the design UI renders the API message; Details are for API consumers.
func incompleteCaseVersionError(item design.CaseWithCurrent) *platform.Error {
	version := design.CaseVersion{}
	if item.Current != nil {
		version = *item.Current
	}
	steps, assertions := design.CaseVersionContentCounts(version)
	missing := make([]string, 0, 2)
	if steps < design.MinimumCaseVersionSteps {
		missing = append(missing, "steps")
	}
	if assertions < design.MinimumCaseVersionAssertions {
		missing = append(missing, "assertions")
	}
	title := strings.TrimSpace(version.Title)
	if title == "" {
		title = "untitled case"
	}
	return &platform.Error{
		Status: http.StatusConflict,
		Code:   "incomplete_case_version",
		Message: fmt.Sprintf(
			"case %q version %d is incomplete (%d step(s), %d assertion(s)); a confirmable case needs at least %d step and %d assertion. Repair the case in design (restore the designed steps/assertions) and confirm it again.",
			title, version.ID, steps, assertions,
			design.MinimumCaseVersionSteps, design.MinimumCaseVersionAssertions,
		),
		Details: map[string]any{
			"case_id":         platformid.Format(item.Case.ID),
			"case_version_id": platformid.Format(version.ID),
			"step_count":      steps,
			"assertion_count": assertions,
			"missing":         missing,
			"next_action":     "repair the case version in design (restore the designed steps/assertions), then confirm the repaired version again",
		},
	}
}

// validateJudgmentForCases applies the judgment predicate to every draft of a
// confirmation batch before the first write (Task 95 atomic pattern). Every
// blocking case is named so the user can resolve the whole selection at once;
// an infrastructure failure is never aggregated into a verdict about the case.
func (s *GraphService) validateJudgmentForCases(ctx context.Context, versionID int64, items []design.CaseWithCurrent) error {
	blockers := make([]*platform.Error, 0)
	for _, item := range items {
		if item.Current == nil {
			continue
		}
		err := s.validateJudgmentForConfirm(ctx, versionID, item.Case.ID, item.Current.ID)
		if err == nil {
			continue
		}
		var apiErr *platform.Error
		if errors.As(err, &apiErr) && (apiErr.Code == JudgmentPendingCode || apiErr.Code == JudgmentFailedCode) {
			blockers = append(blockers, apiErr)
			continue
		}
		// Not a judgment verdict (unwired gate, store failure): propagate it as
		// itself instead of misattributing it to the case under review.
		return err
	}
	switch len(blockers) {
	case 0:
		return nil
	case 1:
		return blockers[0]
	default:
		return aggregateJudgmentBlocks(blockers)
	}
}

// validateJudgmentForConfirm is the confirmation judgment predicate (spec
// SC-E5). review_state is the only routing input: pass is confirmable,
// pending/reworking answer judgment_pending, and an escalated fail or an
// unavailable judge answer judgment_failed unless a human override is bound to
// this exact case version content and postdates the latest judge fact.
//
// It is internal validation of the single terminal write entry (Confirm); it
// never writes a state of its own.
func (s *GraphService) validateJudgmentForConfirm(ctx context.Context, versionID, caseID, currentVersionID int64) error {
	if s.judgment == nil {
		return platform.Unavailable("the judgment gate is not wired; a case version cannot be confirmed without its judge review state")
	}
	state, err := s.judgment.ReviewStateFor(ctx, versionID, caseID, currentVersionID)
	if err != nil {
		return err
	}
	switch state {
	case design.ReviewStatePass:
		return nil
	case design.ReviewStatePending, design.ReviewStateReworking:
		return judgmentPendingError(caseID, currentVersionID, state)
	case design.ReviewStateEscalatedFail, design.ReviewStateJudgeUnavailable:
		return s.validateJudgmentOverride(ctx, versionID, caseID, currentVersionID, state)
	default:
		// An unknown review state is a defect, not a permission to confirm.
		return platform.Unavailable("unknown case review state " + string(state) + "; the case version cannot be confirmed")
	}
}

// validateJudgmentOverride decides whether a human override unblocks an
// escalated case version. The current content hash is read from the server,
// never taken from the override row itself.
func (s *GraphService) validateJudgmentOverride(ctx context.Context, versionID, caseID, currentVersionID int64, state design.ReviewState) error {
	current, err := s.repo.CurrentCaseVersion(ctx, versionID, currentVersionID)
	if err != nil {
		return mapDesignError(err, "case version")
	}
	override, err := s.judgment.LatestOverride(ctx, caseID, currentVersionID)
	if err != nil {
		return err
	}
	latest, err := s.judgment.LatestJudgment(ctx, caseID)
	if err != nil {
		return err
	}
	if overrideValidForVersion(override, current, latest, currentVersionID) {
		return nil
	}
	return judgmentFailedError(caseID, currentVersionID, state, latest)
}

// overrideValidForVersion reports whether the override is a decision about the
// content that is about to be frozen, made after the latest judge fact.
//
// Ordering: when a judgment exists for this version, the override must either
// reference that judgment or be created later than it. When no judgment exists
// for this version (judge_unavailable), the version binding is the ordering
// proof: JudgmentService.Override only records a judge_exhausted override once
// the escalation fact of this exact case version exists, and that fact is
// append-only and bound to the same immutable version.
func overrideValidForVersion(override *design.Override, current design.CaseVersion, latest *design.Judgment, currentVersionID int64) bool {
	if override == nil {
		return false
	}
	if override.CaseVersionID != currentVersionID || override.ContentHash != current.ContentHash {
		return false
	}
	if !design.JudgmentBoundTo(latest, currentVersionID) {
		return true
	}
	if override.JudgmentID != nil && *override.JudgmentID == latest.ID {
		return true
	}
	return override.CreatedAt.After(latest.CreatedAt)
}

// judgmentPendingError is the typed 409 for a case version whose judge review
// has not finished. The design UI renders the API message.
func judgmentPendingError(caseID, caseVersionID int64, state design.ReviewState) *platform.Error {
	return &platform.Error{
		Status: http.StatusConflict,
		Code:   JudgmentPendingCode,
		Message: fmt.Sprintf(
			"case %d version %d is not reviewed yet (%s): the independent judge must finish reviewing this case version before it can be confirmed.",
			caseID, caseVersionID, state,
		),
		Details: map[string]any{
			"case_id":         platformid.Format(caseID),
			"case_version_id": platformid.Format(caseVersionID),
			"review_state":    string(state),
			"next_action":     "wait for the judge review (or the running rework round) of this case version to finish, then confirm it again",
		},
	}
}

// judgmentFailedError is the typed 409 for an escalated case version with no
// valid override. It names the judge findings or the escalation reason so the
// human decision is informed, and points at the override route.
func judgmentFailedError(caseID, caseVersionID int64, state design.ReviewState, latest *design.Judgment) *platform.Error {
	reason := "the judge is unavailable for this case version (its judge generation budget is exhausted)"
	findings := make([]map[string]any, 0)
	if state == design.ReviewStateEscalatedFail && latest != nil {
		reason = fmt.Sprintf("the judge failed this case version in round %d and its rework budget is exhausted", latest.Round)
		if summary := judgmentFindingSummary(latest.Findings); summary != "" {
			reason += ": " + summary
		}
		for _, finding := range latest.Findings {
			findings = append(findings, map[string]any{
				"dimension": finding.Dimension,
				"severity":  finding.Severity,
				"message":   finding.Message,
				"case_ref":  finding.CaseRef,
			})
		}
	}
	return &platform.Error{
		Status: http.StatusConflict,
		Code:   JudgmentFailedCode,
		Message: fmt.Sprintf(
			"case %d version %d cannot be confirmed: %s. A human must override the judgment or redesign the case first.",
			caseID, caseVersionID, reason,
		),
		Details: map[string]any{
			"case_id":         platformid.Format(caseID),
			"case_version_id": platformid.Format(caseVersionID),
			"review_state":    string(state),
			"findings":        findings,
			"next_action":     "review the judge report, then override the judgment for this case version or redesign the case and have it judged again",
		},
	}
}

// judgmentFindingSummary renders the findings behind a fail for the API message
// and for the rework payload. Blocking findings come first; a fail carrying ONLY
// minor findings still gets its reasons quoted rather than an empty summary.
// (SubmitJudgment enforces the pass-side rubric — a pass may not carry
// blocker/important — but it does NOT require a fail to carry a blocking
// finding, so the minor-only fail is reachable; H-F10 T19 corrected the
// "unreachable" note against the code.) It is bounded so one verbose judge
// report cannot produce an unbounded error.
func judgmentFindingSummary(findings []design.JudgmentFinding) string {
	const maxQuoted = 3
	quote := func(severityFilter func(string) bool) []string {
		parts := make([]string, 0, maxQuoted)
		for _, finding := range findings {
			if !severityFilter(finding.Severity) {
				continue
			}
			if len(parts) == maxQuoted {
				parts = append(parts, "...")
				break
			}
			parts = append(parts, finding.Dimension+": "+finding.Message)
		}
		return parts
	}
	parts := quote(func(severity string) bool { return severity != design.SeverityMinor })
	if len(parts) == 0 {
		parts = quote(func(string) bool { return true })
	}
	return strings.Join(parts, "; ")
}

// aggregateJudgmentBlocks reports every blocking case of one batch. The code is
// the first blocking case's code in selection order; each blocker keeps its own
// code and review state in Details so no reason is lost.
func aggregateJudgmentBlocks(blockers []*platform.Error) *platform.Error {
	parts := make([]string, 0, len(blockers))
	entries := make([]map[string]any, 0, len(blockers))
	for _, blocker := range blockers {
		caseID, _ := blocker.Details["case_id"].(string)
		caseVersionID, _ := blocker.Details["case_version_id"].(string)
		parts = append(parts, fmt.Sprintf("case %s version %s [%s] %s", caseID, caseVersionID, blocker.Code, blocker.Message))
		entries = append(entries, map[string]any{
			"case_id":         caseID,
			"case_version_id": caseVersionID,
			"review_state":    blocker.Details["review_state"],
			"code":            blocker.Code,
			"message":         blocker.Message,
		})
	}
	return &platform.Error{
		Status:  http.StatusConflict,
		Code:    blockers[0].Code,
		Message: fmt.Sprintf("%d case versions block this confirmation: %s", len(blockers), strings.Join(parts, "; ")),
		Details: map[string]any{
			"blocking_cases": entries,
			"next_action":    "resolve every blocking case (finish its review, override its judgment, or redesign it), then confirm the selection again",
		},
	}
}
