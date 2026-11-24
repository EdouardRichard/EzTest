package designapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// DesignReviewGate is THE shared designing -> case_review review predicate
// (T9, spec SC-E2/SC-E4/SC-E5). Both advance paths consume it: the design
// reconciler's automatic converge (Planner.convergeCompleteDesign) and the
// version stage service's manual advance (versionapp.DesignReviewGate, wired by
// the composition root in T13 to the SAME *ReviewGateService instance). One
// predicate, one truth: neither path may carry its own second review gate.
type DesignReviewGate interface {
	ReviewGate(ctx context.Context, versionID int64) error
}

// ReviewGraphReader is the design graph read the gate enumerates activated
// requirements from. design.Repository (and the planner's repo) satisfies it.
type ReviewGraphReader interface {
	ReadGraph(ctx context.Context, versionID int64) (design.GraphData, error)
}

// ReviewStateReader is the T6 review-state authority (JudgmentService.
// ReviewStateFor): the single projection that derives a case version's review
// state from the append-only judge facts. The gate never re-derives it.
type ReviewStateReader interface {
	ReviewStateFor(ctx context.Context, versionID, caseID, currentVersionID int64) (design.ReviewState, error)
}

// ReviewGateService is the shared review gate. It is validation only: it reads
// the graph and the review-state projection and returns a verdict; it never
// writes a stage, a case or a judgment (server truth stays with the writers).
type ReviewGateService struct {
	graphs ReviewGraphReader
	states ReviewStateReader
}

// NewReviewGateService builds the shared gate from the graph read and the
// review-state authority. The composition root (T13) wires one instance into
// both the design planner (SetReviewGate) and the version stage service
// (SetDesignReviewGate).
func NewReviewGateService(graphs ReviewGraphReader, states ReviewStateReader) *ReviewGateService {
	return &ReviewGateService{graphs: graphs, states: states}
}

var _ DesignReviewGate = (*ReviewGateService)(nil)

// SetReviewGate wires the shared review gate into the planner's converge path.
// It is a setter because the composition root builds the judgment service after
// the design planner (T13). An unwired gate fails closed: convergeCompleteDesign
// does not advance, mirroring the manual edge's fail-closed nil port.
func (p *Planner) SetReviewGate(gate DesignReviewGate) *Planner {
	if p != nil {
		p.reviewGate = gate
	}
	return p
}

// ReviewGate reports whether the version may leave designing for case_review.
// For every activated, still-clarified requirement on its current revision:
//
//  1. coverage complete — the confirmed-union-current-draft covered_acs set
//     covers every requirement AC (spec SC-E2, T5 CoverageUnion/CoverageComplete);
//  2. every current draft case review-complete — its T6 review state is pass,
//     escalated_fail or judge_unavailable. R-E4r: fail-exhausted and
//     judge-unavailable drafts proceed to case_review for a human decision; only
//     pending/reworking block.
//
// A nil error opens the gate. A closed gate returns a typed 409 naming every
// missing AC set and every unreviewed case (one pass surfaces all gaps). An
// unwired read port or an unknown review state fails closed (constitution
// VII.6); an infrastructure read failure propagates as itself, never as a
// verdict about a case.
func (s *ReviewGateService) ReviewGate(ctx context.Context, versionID int64) error {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return err
	}
	if s == nil || s.graphs == nil {
		return platform.Unavailable("the design graph reader is not wired for the review gate; a version cannot advance to case review without its coverage check")
	}
	graph, err := s.graphs.ReadGraph(ctx, versionID)
	if err != nil {
		return mapDesignError(err, "version")
	}
	blockers := make([]*platform.Error, 0)
	for _, requirement := range graph.Requirements {
		if !strings.EqualFold(strings.TrimSpace(requirement.Requirement.Status), string(version.RequirementClarified)) {
			continue
		}
		if requirement.Requirement.CurrentVersionID <= 0 {
			continue
		}
		// judgeCoverage is the single current-revision selection authority shared
		// with the judge scheduler: only scenarios bound to the current revision
		// contribute, split by lifecycle status.
		_, confirmedACs, draftACs, draftCases := judgeCoverage(requirement)
		union := design.CoverageUnion(confirmedACs, draftACs)
		if missing := design.MissingACs(union, requirement.Requirement.ACIDs); len(missing) > 0 {
			blockers = append(blockers, reviewCoverageGapError(requirement.Requirement.ID, missing))
		}
		for _, item := range draftCases {
			if s.states == nil {
				return platform.Unavailable("the review-state reader is not wired for the review gate; a draft case cannot advance without its judge review state")
			}
			state, err := s.states.ReviewStateFor(ctx, versionID, item.Case.ID, item.Current.ID)
			if err != nil {
				// Infrastructure failure, not a verdict about the case.
				return err
			}
			switch state {
			case design.ReviewStatePass, design.ReviewStateEscalatedFail, design.ReviewStateJudgeUnavailable:
				// Review-complete (R-E4r): escalated/unavailable drafts proceed to
				// case_review for the human decision; they do not block the edge.
			case design.ReviewStatePending, design.ReviewStateReworking:
				blockers = append(blockers, judgmentPendingError(item.Case.ID, item.Current.ID, state))
			default:
				// An unknown review state is a defect, not a permission to advance.
				return platform.Unavailable("unknown case review state " + string(state) + "; the review gate cannot decide")
			}
		}
	}
	switch len(blockers) {
	case 0:
		return nil
	case 1:
		return blockers[0]
	default:
		return aggregateReviewGateBlocks(blockers)
	}
}

// reviewCoverageGapError is the typed 409 for a requirement whose coverage union
// is incomplete. It names the missing AC ids so the design pass can repair the
// exact gap (spec SC-E2/SC-E4).
func reviewCoverageGapError(requirementID int64, missing []string) *platform.Error {
	return &platform.Error{
		Status: http.StatusConflict,
		Code:   "ac_coverage_gap",
		Message: fmt.Sprintf(
			"requirement %d is missing case coverage for %s; generate or repair cases covering these acceptance criteria before the version can enter case review.",
			requirementID, strings.Join(missing, ", "),
		),
		Details: map[string]any{
			"requirement_id": platformid.Format(requirementID),
			"missing_acs":    missing,
			"next_action":    "generate or repair cases covering the missing acceptance criteria, then retry the stage advance",
		},
	}
}

// aggregateReviewGateBlocks reports every gap of one version in a single 409 so
// the user can resolve the whole advance at once. The code is the first gap's
// code in enumeration order; each entry keeps its own code/message/details.
func aggregateReviewGateBlocks(blockers []*platform.Error) *platform.Error {
	parts := make([]string, 0, len(blockers))
	entries := make([]map[string]any, 0, len(blockers))
	for _, blocker := range blockers {
		parts = append(parts, fmt.Sprintf("[%s] %s", blocker.Code, blocker.Message))
		entries = append(entries, map[string]any{
			"code":    blocker.Code,
			"message": blocker.Message,
			"details": blocker.Details,
		})
	}
	return &platform.Error{
		Status:  http.StatusConflict,
		Code:    blockers[0].Code,
		Message: fmt.Sprintf("%d review gaps block the case-review gate: %s", len(blockers), strings.Join(parts, "; ")),
		Details: map[string]any{
			"blockers":    entries,
			"next_action": "resolve every gap (cover the missing acceptance criteria and finish each draft's judge review), then retry the stage advance",
		},
	}
}
