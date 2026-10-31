package designapp

import (
	"context"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// StaleService implements versionapp.CaseStaleHook: when a requirement moves to
// a new revision it marks the confirmed current case versions stale and
// reports their decimal IDs.
type StaleService struct {
	repo   design.Repository
	clock  clock.Clock
	audit  auditapp.Recorder
	events eventingapp.Publisher
}

// NewStaleService wires the stale service.
func NewStaleService(repo design.Repository, clk clock.Clock, audit auditapp.Recorder, events eventingapp.Publisher) *StaleService {
	if clk == nil {
		clk = clock.System{}
	}
	return &StaleService{repo: repo, clock: clk, audit: audit, events: events}
}

// MarkRequirementChanged marks every current case version of the requirement
// that is still confirmed as stale (spec §4.2). The operation is idempotent: a
// second call reports no affected versions.
func (s *StaleService) MarkRequirementChanged(ctx context.Context, versionID, requirementID int64, reason string, revision int) ([]string, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return nil, err
	}
	if err := requirePositiveID("requirement_id", requirementID); err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	if reason == "" {
		reason = "requirement_changed"
	}
	type affectedCase struct {
		caseID        int64
		caseVersionID int64
	}
	affected := make([]affectedCase, 0)
	err := s.repo.WithStaleLock(ctx, versionID, requirementID, func(ctx context.Context, tx design.StaleTx) error {
		requirement, err := tx.Requirement(ctx)
		if err != nil {
			return err
		}
		if requirement.CurrentVersionID == 0 {
			return nil
		}
		cases, err := tx.CasesByRequirement(ctx)
		if err != nil {
			return err
		}
		for _, item := range cases {
			if item.Current == nil || item.Current.Status != design.CaseStatusConfirmed {
				continue
			}
			if err := tx.MarkCaseVersionStale(ctx, item.Current.ID, now); err != nil {
				return err
			}
			if err := tx.MarkCaseEntityStale(ctx, item.Case.ID, now); err != nil {
				return err
			}
			affected = append(affected, affectedCase{caseID: item.Case.ID, caseVersionID: item.Current.ID})
		}
		return nil
	})
	if err != nil {
		return nil, mapDesignError(err, "requirement")
	}

	ids := make([]string, 0, len(affected))
	for _, item := range affected {
		ids = append(ids, platformid.Format(item.caseVersionID))
		s.publishStaleEvent(ctx, versionID, requirementID, item.caseID, item.caseVersionID, reason)
	}
	if len(affected) > 0 && s.audit != nil {
		event := auditapp.Event{
			Action:     "case.stale",
			TargetKind: "requirement",
			TargetID:   platformid.Format(requirementID),
			RequestID:  requestid.From(ctx),
			Metadata: map[string]any{
				"requirement_id":         platformid.Format(requirementID),
				"reason":                 reason,
				"affected_case_versions": ids,
			},
		}
		_ = s.audit.Record(ctx, event)
	}
	return ids, nil
}

func (s *StaleService) publishStaleEvent(ctx context.Context, versionID, requirementID, caseID, caseVersionID int64, reason string) {
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"requirement_id":  platformid.Format(requirementID),
		"case_id":         platformid.Format(caseID),
		"case_version_id": platformid.Format(caseVersionID),
		"reason":          reason,
	}
	scope := map[string]any{
		"version_id":     platformid.Format(versionID),
		"requirement_id": platformid.Format(requirementID),
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      "design.case_version_stale",
		Scope:     scope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}
