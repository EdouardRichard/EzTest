package designapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// ConfirmInput is one POST /design/confirmations request, or one internal
// automatic confirmation (spec R-EX-6). The auto fields are zero for the user
// request path, so the HTTP surface cannot forge a system confirmation.
type ConfirmInput struct {
	Scope    string
	TargetID int64
	BatchID  string
	// AutoConfirmed marks an internal system confirmation. Trigger is
	// "discovery_delta" for the additive revision; SourceObservationID keeps the
	// originating execution observation on the confirmation audit row.
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
	for _, target := range targets {
		data, err := s.repo.ReadCase(ctx, versionID, target.CaseID, nil)
		if err != nil {
			return mapDesignError(err, "case")
		}
		if data.Selected.Status != design.CaseStatusDraft {
			continue
		}
		if err := s.validateConfirmableCaseVersion(ctx, versionID, design.CaseWithCurrent{Case: data.Case, Current: &data.Selected}); err != nil {
			return err
		}
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
// draft case on a scenario of the requirement's current revision. Cases bound
// to an older revision are excluded, matching ConfirmationTx.CasesByRequirement.
func hasDraftCurrentCase(requirement design.RequirementGraph) bool {
	for _, scenario := range requirement.Scenarios {
		if scenario.Scenario.RequirementVersionID != requirement.Requirement.CurrentVersionID {
			continue
		}
		for _, item := range scenario.Cases {
			if item.Current.Status == design.CaseStatusDraft {
				return true
			}
		}
	}
	return false
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
		if requirement.Status != "clarified" {
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

// validateConfirmableCaseVersion enforces the confirmed-version rules: a case
// version may only enter the confirmed set when it is complete (Task 95 finalize
// rule) and every kind=visual assertion resolves to an active visual_assertion
// configuration (Task 115). The PO O8 AC-FR-011-01 version (1 step, 0
// assertions) was confirmed and then executed into inconclusive(incomplete);
// the retry11 FR-025 version carried a visual assertion without a
// visual_assertion_id and executed into inconclusive(visual_assertion_not_run).
// Confirmation must instead return a typed repair request before anything is
// frozen.
func (s *GraphService) validateConfirmableCaseVersion(ctx context.Context, versionID int64, item design.CaseWithCurrent) error {
	if item.Current == nil {
		return nil
	}
	if !design.CaseVersionComplete(*item.Current) {
		return incompleteCaseVersionError(item)
	}
	return s.validateVisualAssertionReferences(ctx, versionID, item)
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
