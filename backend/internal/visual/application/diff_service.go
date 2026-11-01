package visualapp

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// DiffServiceDeps wires the visual comparison service.
type DiffServiceDeps struct {
	Diffs      DiffRepository
	Assertions AssertionRepository
	Clock      Clock
	IDs        IDGenerator
	Audit      AuditRecorder
	Events     EventPublisher
}

// DiffService records append-only comparisons and serves the version-scoped
// diff pages. Recording is idempotent by assertion_result_id.
type DiffService struct {
	diffs      DiffRepository
	assertions AssertionRepository
	clock      Clock
	ids        IDGenerator
	audit      AuditRecorder
	events     EventPublisher
}

// NewDiffService wires the diff service.
func NewDiffService(deps DiffServiceDeps) *DiffService {
	return &DiffService{
		diffs: deps.Diffs, assertions: deps.Assertions, clock: deps.Clock,
		ids: deps.IDs, audit: deps.Audit, events: deps.Events,
	}
}

// Record validates and persists one comparison. A replay of the same
// assertion_result_id returns the stored row without a second event.
func (s *DiffService) Record(ctx context.Context, in visual.RecordDiffInput) (visual.Diff, error) {
	if err := in.Validate(); err != nil {
		return visual.Diff{}, diffInvalidFrom(err)
	}
	assertion, err := s.assertions.Get(ctx, in.VisualAssertionID)
	if err != nil {
		return visual.Diff{}, mapVisualError(err, "visual_assertion")
	}
	if assertion.VersionID != in.VersionID {
		return visual.Diff{}, platform.NotFound("visual_assertion")
	}
	boxes, err := visual.NormalizeMismatchBoxes(in.MismatchBoxes)
	if err != nil {
		return visual.Diff{}, diffInvalidFrom(err)
	}
	if s.ids == nil {
		return visual.Diff{}, errors.New("visual diff service has no ID generator")
	}
	now := s.now()
	diff := visual.Diff{
		ID:                s.ids.Next(),
		VisualAssertionID: in.VisualAssertionID,
		CaseRunID:         in.CaseRunID,
		AssertionResultID: in.AssertionResultID,
		BaselineID:        in.BaselineID,
		ActualEvidenceID:  in.ActualEvidenceID,
		DiffEvidenceID:    in.DiffEvidenceID,
		DiffRatio:         in.DiffRatio,
		Threshold:         in.Threshold,
		Classification:    in.Classification,
		Reason:            in.Reason,
		EnvironmentMatch:  in.EnvironmentMatch,
		Width:             in.Width,
		Height:            in.Height,
		MismatchBoxes:     boxes,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	stored, created, err := s.diffs.Insert(ctx, diff)
	if err != nil {
		return visual.Diff{}, mapVisualError(err, "visual_diff")
	}
	if created {
		s.recordAudit(ctx, in.VersionID, stored)
		s.publishCreated(ctx, in.VersionID, stored)
	}
	return stored, nil
}

// List returns one descending-ID page of comparisons of a version.
func (s *DiffService) List(ctx context.Context, versionID int64, filter visual.DiffFilter) ([]visual.Diff, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("version_id", "must be a positive ID")
	}
	if err := filter.Validate(); err != nil {
		return nil, false, diffInvalidFrom(err)
	}
	list, hasMore, err := s.diffs.List(ctx, versionID, filter)
	if err != nil {
		return nil, false, mapVisualError(err, "visual_diff")
	}
	return list, hasMore, nil
}

// Get returns one comparison, scoped to its version.
func (s *DiffService) Get(ctx context.Context, versionID, id int64) (visual.Diff, error) {
	if versionID <= 0 {
		return visual.Diff{}, platform.Invalid("version_id", "must be a positive ID")
	}
	if id <= 0 {
		return visual.Diff{}, platform.Invalid("diff_id", "must be a positive ID")
	}
	diff, err := s.diffs.Get(ctx, id)
	if err != nil {
		return visual.Diff{}, mapVisualError(err, "visual_diff")
	}
	assertion, err := s.assertions.Get(ctx, diff.VisualAssertionID)
	if err != nil {
		return visual.Diff{}, mapVisualError(err, "visual_assertion")
	}
	if assertion.VersionID != versionID {
		return visual.Diff{}, platform.NotFound("visual_diff")
	}
	return diff, nil
}

// ByAssertionResult returns the comparison of one assertion result (used by the
// execution-side idempotent replay).
func (s *DiffService) ByAssertionResult(ctx context.Context, assertionResultID int64) (visual.Diff, bool, error) {
	if assertionResultID <= 0 {
		return visual.Diff{}, false, platform.Invalid("assertion_result_id", "must be a positive ID")
	}
	return s.diffs.ByAssertionResult(ctx, assertionResultID)
}

func diffInvalidFrom(err error) error {
	if errors.Is(err, visual.ErrInvalidDiff) {
		return diffInvalid("", err.Error())
	}
	if errors.Is(err, visual.ErrInvalidConfig) {
		return configInvalid("threshold", err.Error())
	}
	return err
}

func (s *DiffService) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

func (s *DiffService) recordAudit(ctx context.Context, versionID int64, diff visual.Diff) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, AuditEntry{
		Action: "visual.diff.create", TargetKind: "visual_diff",
		TargetID: platformid.Format(diff.ID),
		Metadata: map[string]any{
			"version_id":          platformid.Format(versionID),
			"visual_assertion_id": platformid.Format(diff.VisualAssertionID),
			"case_run_id":         platformid.Format(diff.CaseRunID),
			"classification":      string(diff.Classification),
			"diff_ratio":          diff.DiffRatio,
		},
	})
}

func (s *DiffService) publishCreated(ctx context.Context, versionID int64, diff visual.Diff) {
	if s.events == nil {
		return
	}
	_ = s.events.Publish(ctx, Event{
		Type:      "visual.diff_created",
		VersionID: versionID,
		Scope: map[string]any{
			"visual_diff_id":      platformid.Format(diff.ID),
			"visual_assertion_id": platformid.Format(diff.VisualAssertionID),
			"case_run_id":         platformid.Format(diff.CaseRunID),
		},
		Payload: map[string]any{
			"visual_diff_id":      platformid.Format(diff.ID),
			"visual_assertion_id": platformid.Format(diff.VisualAssertionID),
			"case_run_id":         platformid.Format(diff.CaseRunID),
			"classification":      string(diff.Classification),
			"diff_ratio":          diff.DiffRatio,
			"reason":              diff.Reason,
		},
	})
}
