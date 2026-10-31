package designapp

import (
	"context"
	"encoding/json"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// DataPlanInput is one design.data_plan_submit payload.
type DataPlanInput struct {
	Preconditions *string
	TestData      json.RawMessage
}

// DataPlanResult is the design.data_plan_submit response payload.
type DataPlanResult struct {
	CaseID        int64
	CaseVersionID int64
	Revision      int
}

// SubmitDataPlan creates a new draft revision of the current draft case version
// with the data planner's preconditions/test_data (spec §5.3).
func (s *GraphService) SubmitDataPlan(ctx context.Context, versionID, caseVersionID, runID int64, in DataPlanInput) (DataPlanResult, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return DataPlanResult{}, err
	}
	if err := requirePositiveID("case_version_id", caseVersionID); err != nil {
		return DataPlanResult{}, err
	}
	if in.Preconditions != nil {
		if err := validation.String("preconditions", *in.Preconditions, 0, design.CaseDescriptionMax); err != nil {
			return DataPlanResult{}, err
		}
	}
	if len(in.TestData) > 0 {
		if _, err := design.NormalizeJSONObject(in.TestData); err != nil {
			return DataPlanResult{}, platform.Invalid("test_data", "must be a JSON object")
		}
	}

	current, err := s.repo.CurrentCaseVersion(ctx, versionID, caseVersionID)
	if err != nil {
		return DataPlanResult{}, mapDesignError(err, "case version")
	}
	if current.Status != design.CaseStatusDraft {
		return DataPlanResult{}, platform.StateConflict("only the current draft case version can be updated by the data planner")
	}
	detail, err := s.repo.ReadCase(ctx, versionID, current.CaseID, &caseVersionID)
	if err != nil {
		return DataPlanResult{}, mapDesignError(err, "case")
	}

	now := s.clock.Now().UTC()
	revision := cloneCaseVersionForRevision(current, s.ids, now)
	if in.Preconditions != nil {
		revision.Preconditions = *in.Preconditions
	}
	if len(in.TestData) > 0 {
		normalized, err := design.NormalizeJSONObject(in.TestData)
		if err != nil {
			return DataPlanResult{}, platform.Invalid("test_data", "must be a JSON object")
		}
		revision.TestData = normalized
	}
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID := actor.ID
		revision.CreatedBy = &actorID
	}
	if runID > 0 {
		run := runID
		revision.CreatedByRunID = &run
	}
	hash, err := design.ComputeCaseContentHash(revision)
	if err != nil {
		return DataPlanResult{}, err
	}
	revision.ContentHash = hash

	stored, err := s.repo.InsertDataPlanRevision(ctx, versionID, caseVersionID, revision, now)
	if err != nil {
		return DataPlanResult{}, mapDesignError(err, "case version")
	}

	result := DataPlanResult{CaseID: stored.CaseID, CaseVersionID: stored.ID, Revision: stored.Revision}
	requirementVersionID := detail.Requirement.CurrentVersionID
	s.publishGraphDelta(ctx, versionID, detail.Requirement.ID, requirementVersionID,
		[]int64{detail.Scenario.ID}, []int64{stored.CaseID}, []int64{stored.ID}, "data_plan", 0)
	s.publishCaseVersionCreated(ctx, versionID, detail.Requirement.ID, createdCaseVersion{
		caseID: stored.CaseID, caseVersionID: stored.ID, revision: stored.Revision, created: false,
	}, 0)
	if s.audit != nil {
		event := auditapp.Event{
			Action:     "case.updated",
			TargetKind: "case",
			TargetID:   platformid.Format(stored.CaseID),
			RequestID:  requestid.From(ctx),
			Metadata: map[string]any{
				"requirement_id":  platformid.Format(detail.Requirement.ID),
				"case_id":         platformid.Format(stored.CaseID),
				"case_version_id": platformid.Format(stored.ID),
				"revision":        stored.Revision,
			},
		}
		if stored.CreatedBy != nil && *stored.CreatedBy > 0 {
			actor := *stored.CreatedBy
			event.ActorID = &actor
		}
		_ = s.audit.Record(ctx, event)
	}
	return result, nil
}

// cloneCaseVersionForRevision copies the immutable content of a case version
// with fresh snowflake IDs and a revision number one higher than the base.
func cloneCaseVersionForRevision(base design.CaseVersion, ids platformid.Generator, now time.Time) design.CaseVersion {
	revision := base
	revision.ID = ids.Next()
	revision.Revision = base.Revision + 1
	revision.Status = design.CaseStatusDraft
	revision.CreatedAt = now
	revision.UpdatedAt = now
	revision.CreatedBy = nil
	revision.CreatedByRunID = nil

	revision.Steps = make([]design.CaseStep, 0, len(base.Steps))
	for _, step := range base.Steps {
		copied := step
		copied.ID = ids.Next()
		copied.CaseVersionID = 0
		copied.CreatedAt = now
		revision.Steps = append(revision.Steps, copied)
	}
	revision.Assertions = make([]design.CaseAssertion, 0, len(base.Assertions))
	for _, assertion := range base.Assertions {
		copied := assertion
		copied.ID = ids.Next()
		copied.CaseVersionID = 0
		copied.CreatedAt = now
		revision.Assertions = append(revision.Assertions, copied)
	}
	revision.Sources = make([]design.CaseSource, 0, len(base.Sources))
	for _, source := range base.Sources {
		copied := source
		copied.ID = ids.Next()
		copied.CaseVersionID = 0
		copied.CreatedAt = now
		revision.Sources = append(revision.Sources, copied)
	}
	return revision
}
