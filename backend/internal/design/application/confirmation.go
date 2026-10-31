package designapp

import (
	"context"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// ConfirmInput is one POST /design/confirmations request.
type ConfirmInput struct {
	Scope    string
	TargetID int64
	BatchID  string
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
		event := auditapp.Event{
			Action:     "case.confirmed",
			TargetKind: "case",
			TargetID:   platformid.Format(in.TargetID),
			RequestID:  requestid.From(ctx),
			Metadata: map[string]any{
				"scope":            in.Scope,
				"target_id":        platformid.Format(in.TargetID),
				"batch_id":         batchID,
				"case_version_ids": formatIDList(caseVersionIDs),
			},
		}
		if actorID > 0 {
			actor := actorID
			event.ActorID = &actor
		}
		_ = s.audit.Record(ctx, event)
	}
	return result, nil
}
