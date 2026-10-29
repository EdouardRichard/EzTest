package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Review decisions accepted by ReviewCommand.Decision (spec §7.2 endpoint 21).
const (
	ReviewDecisionApprove = "approve"
	ReviewDecisionReject  = "reject"

	// reviewDefaultListLimit is applied when a caller omits the page limit; the
	// review repository requires a positive limit.
	reviewDefaultListLimit = 20
)

// ReviewCommand is the POST /review body of spec §7.2 endpoint 21.
type ReviewCommand struct {
	Decision            string `json:"decision"`
	Comment             string `json:"comment,omitempty"`
	AcknowledgeFindings bool   `json:"acknowledgeFindings,omitempty"`
}

// ApproveResult is the response projection of an approval: the frozen baseline,
// the approved review round and the version snapshot after it reached executing.
type ApproveResult struct {
	Baseline domain.CaseBaseline
	Review   domain.CaseReview
	Version  VersionSnapshot
}

// RejectResult is the response projection of a rejection: the rejected review
// round and the version snapshot after it returned to designing.
type RejectResult struct {
	Review  domain.CaseReview
	Version VersionSnapshot
}

// BaselineFreezer is the baseline-service slice the approval gate needs. The
// concrete *BaselineService satisfies it; the narrow interface keeps the review
// use case independent from the freeze ports.
type BaselineFreezer interface {
	Freeze(ctx context.Context, versionID, actor int64) (domain.CaseBaseline, error)
}

// ReviewRunStarter is the run-coordinator slice StartReview delegates to. The
// concrete *RunService satisfies it.
type ReviewRunStarter interface {
	Start(ctx context.Context, versionID, actor int64, cmd StartRunCommand) (domain.CaseDesignRun, error)
}

// ReviewDeps are the ports of the review use cases: the review/finding/baseline
// stores, the version lifecycle write port, the event sink, the baseline freeze
// service, the run coordinator, the id generator and the clock.
type ReviewDeps struct {
	Runs      ReviewRunStarter
	Reviews   ReviewRepository
	Findings  FindingRepository
	Baselines BaselineRepository
	Versions  VersionLifecycle
	Events    EventPublisher
	Baseline  BaselineFreezer
	IDs       IDGenerator
	Clock     Clock
}

// BlockerFindingsError is the approve-gate failure of spec §6.3: it carries the
// open blocker finding ids in details while keeping the domain error shape
// (reason blocker_findings_open, HTTP 409) for the shared error envelope.
type BlockerFindingsError struct {
	Domain     *domain.Error
	FindingIDs []int64
}

// Error implements error.
func (e *BlockerFindingsError) Error() string {
	if e == nil || e.Domain == nil {
		return "blocker_findings_open"
	}
	return e.Domain.Error()
}

// Unwrap exposes the underlying domain error so errors.As(err, **domain.Error)
// still reports the stable kind and reason.
func (e *BlockerFindingsError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Domain
}

// Details returns the error-envelope details of the approve gate. Finding ids are
// decimal strings, matching the D29 JSON convention for every mindmap id.
func (e *BlockerFindingsError) Details() map[string]any {
	ids := make([]string, 0, len(e.FindingIDs))
	for _, id := range e.FindingIDs {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	reason := "blocker_findings_open"
	if e != nil && e.Domain != nil && e.Domain.Reason != "" {
		reason = e.Domain.Reason
	}
	return map[string]any{"reason": reason, "findingIds": ids}
}

// ReviewService implements the review state machine of spec §6.3: start a review
// run, approve (freeze the baseline) or reject (back to designing), and the
// finding disposition endpoints. Version transitions always go through
// VersionLifecycle; the mindmap context never writes the version table.
type ReviewService struct {
	deps ReviewDeps
}

// NewReviewService wires the review use cases to their ports.
func NewReviewService(deps ReviewDeps) *ReviewService {
	return &ReviewService{deps: deps}
}

// StartReview starts a review run through the shared run coordinator, reusing its
// version-state validation, confirmed-requirement check and per-kind
// single-flight slot (spec §6.6).
func (s *ReviewService) StartReview(ctx context.Context, versionID, actor int64) (domain.CaseDesignRun, error) {
	if s.deps.Runs == nil {
		return domain.CaseDesignRun{}, errors.New("mindmap: run coordinator is not configured")
	}
	return s.deps.Runs.Start(ctx, versionID, actor, StartRunCommand{Kind: domain.RunKindReview})
}

// Approve approves the current review round and freezes the baseline (spec
// §6.3/§6.4):
//
//  1. the version must be reviewing and must not have a baseline;
//  2. open blocker findings require acknowledgeFindings=true, otherwise the
//     request fails with blocker_findings_open and the blocker ids and nothing is
//     written;
//  3. a stale approved review with no baseline is demoted to draft, then the
//     latest draft review is approved (or an approved review with no findings is
//     created when the version has none);
//  4. BaselineService.Freeze writes the baseline and the reviewed nodes in one
//     transaction, then moves the version to executing (with compensation when
//     that transition loses the race); a failed freeze demotes the review again;
//  5. gate.resolved and case.baseline_frozen are published after the commit.
func (s *ReviewService) Approve(ctx context.Context, versionID, actor int64, cmd ReviewCommand) (ApproveResult, error) {
	if err := s.validateApprove(); err != nil {
		return ApproveResult{}, err
	}
	if cmd.Decision != "" && cmd.Decision != ReviewDecisionApprove {
		return ApproveResult{}, domain.NewInvalid("decision", "invalid_decision",
			fmt.Sprintf("approve requires decision %q, got %q", ReviewDecisionApprove, cmd.Decision))
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return ApproveResult{}, err
	}
	// The baseline is checked before the status: after a successful approval the
	// version is executing, but a second approval must report baseline_frozen,
	// not version_state_conflict (spec §6.3, D5).
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return ApproveResult{}, err
	}
	if baseline != nil {
		return ApproveResult{}, baselineFrozenError(versionID)
	}
	if version.Status != versionStatusReviewing {
		return ApproveResult{}, reviewVersionStateError(versionID, version.Status)
	}
	blockerIDs, err := s.openBlockerIDs(ctx, versionID, cmd.AcknowledgeFindings)
	if err != nil {
		return ApproveResult{}, err
	}
	if len(blockerIDs) > 0 {
		return ApproveResult{}, &BlockerFindingsError{
			Domain: domain.NewStateConflict("blocker_findings_open",
				fmt.Sprintf("version %d has %d open blocker findings; acknowledge them or fix them first",
					versionID, len(blockerIDs))),
			FindingIDs: blockerIDs,
		}
	}
	// A stale approved review whose baseline was never written (an interrupted
	// or compensated approval) would collide with uk_case_review_approved when
	// the current round is approved. Revert it first; the caller verified above
	// that the version has no baseline yet.
	if err := s.revertStaleApprovedReview(ctx, versionID, actor); err != nil {
		return ApproveResult{}, err
	}
	review, err := s.approveReview(ctx, versionID, actor, cmd.Comment)
	if err != nil {
		return ApproveResult{}, err
	}
	frozen, err := s.deps.Baseline.Freeze(ctx, versionID, actor)
	if err != nil {
		// The review was already approved; put it back to draft so the approval
		// can be retried instead of wedging the version (spec §6.4 compensation).
		s.revertReviewAfterFreezeFailure(ctx, versionID, review, actor)
		return ApproveResult{}, err
	}
	version, err = s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return ApproveResult{}, err
	}
	s.publish(ctx, Event{
		VersionID: versionID,
		Type:      domain.EventGateResolved,
		Summary:   truncateRunes("mindmap review approved", runMaxSummaryRunes),
		ActorID:   actor,
		Source:    domain.NodeSourceUserInput,
		Detail: map[string]any{
			"gate":       "mindmap_review",
			"decision":   ReviewDecisionApprove,
			"status":     version.Status,
			"reviewId":   review.ID,
			"baselineId": frozen.ID,
			"nodeCount":  frozen.NodeCount,
			"caseCount":  frozen.CaseCount,
		},
	})
	s.publish(ctx, Event{
		VersionID: versionID,
		Type:      domain.EventCaseBaselineFrozen,
		Summary: truncateRunes(fmt.Sprintf("case baseline frozen: %d nodes, %d cases",
			frozen.NodeCount, frozen.CaseCount), runMaxSummaryRunes),
		ActorID: actor,
		Source:  domain.NodeSourceUserInput,
		Detail: map[string]any{
			"baselineId": frozen.ID,
			"reviewId":   frozen.ReviewID,
			"nodeCount":  frozen.NodeCount,
			"caseCount":  frozen.CaseCount,
			"graphHash":  reviewHashPrefix(frozen.GraphHash),
			"status":     version.Status,
		},
	})
	return ApproveResult{Baseline: frozen, Review: review, Version: version}, nil
}

// Reject rejects the latest review round with comment and returns the version to
// designing (spec §6.3). No baseline is written and the graph stays editable.
func (s *ReviewService) Reject(ctx context.Context, versionID, actor int64, comment string) (RejectResult, error) {
	if s.deps.Versions == nil || s.deps.Reviews == nil {
		return RejectResult{}, errors.New("mindmap: review service ports are not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return RejectResult{}, err
	}
	if version.Status != versionStatusReviewing {
		return RejectResult{}, reviewVersionStateError(versionID, version.Status)
	}
	if s.deps.Baselines != nil {
		baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
		if err != nil {
			return RejectResult{}, err
		}
		if baseline != nil {
			return RejectResult{}, baselineFrozenError(versionID)
		}
	}
	review, err := s.rejectReview(ctx, versionID, actor, comment)
	if err != nil {
		return RejectResult{}, err
	}
	version, err = s.deps.Versions.RejectReview(ctx, versionID, actor)
	if err != nil {
		return RejectResult{}, err
	}
	s.publish(ctx, Event{
		VersionID: versionID,
		Type:      domain.EventGateResolved,
		Summary:   truncateRunes("mindmap review rejected", runMaxSummaryRunes),
		ActorID:   actor,
		Source:    domain.NodeSourceUserInput,
		Detail: map[string]any{
			"gate":     "mindmap_review",
			"decision": ReviewDecisionReject,
			"status":   version.Status,
			"reviewId": review.ID,
		},
	})
	return RejectResult{Review: review, Version: version}, nil
}

// List returns one page of review rounds of the version.
func (s *ReviewService) List(ctx context.Context, versionID int64, limit int, cursor string) ([]domain.CaseReview, string, bool, error) {
	if s.deps.Reviews == nil {
		return nil, "", false, errors.New("mindmap: review repository is not configured")
	}
	if limit <= 0 {
		limit = reviewDefaultListLimit
	}
	return s.deps.Reviews.List(ctx, versionID, limit, cursor)
}

// ListFindings returns the findings of the version, filtered by status,
// severity and optional node. Empty filters match everything (spec §7.2 endpoint
// 23; the response is a flat array).
func (s *ReviewService) ListFindings(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error) {
	if s.deps.Findings == nil {
		return nil, errors.New("mindmap: finding repository is not configured")
	}
	return s.deps.Findings.List(ctx, versionID, status, severity, nodeID)
}

// UpdateFinding records a human disposition of one finding: fixed or dismissed
// (spec §7.2 endpoint 24). The finding must belong to the version.
func (s *ReviewService) UpdateFinding(ctx context.Context, versionID, findingID, actor int64, status string) (domain.CaseReviewFinding, error) {
	switch status {
	case domain.FindingFixed, domain.FindingDismissed:
	default:
		return domain.CaseReviewFinding{}, domain.NewInvalid("status", "invalid_finding_status",
			fmt.Sprintf("finding status %q is not fixed or dismissed", status))
	}
	if s.deps.Findings == nil {
		return domain.CaseReviewFinding{}, errors.New("mindmap: finding repository is not configured")
	}
	findings, err := s.deps.Findings.List(ctx, versionID, "", "", nil)
	if err != nil {
		return domain.CaseReviewFinding{}, err
	}
	for _, finding := range findings {
		if finding.ID == findingID {
			return s.deps.Findings.UpdateStatus(ctx, findingID, status, actor)
		}
	}
	return domain.CaseReviewFinding{}, domain.NewNotFound("finding", findingID)
}

// validateApprove reports the mandatory approval ports.
func (s *ReviewService) validateApprove() error {
	if s.deps.Versions == nil || s.deps.Reviews == nil || s.deps.Findings == nil ||
		s.deps.Baselines == nil || s.deps.Baseline == nil || s.deps.IDs == nil {
		return errors.New("mindmap: review service ports are not configured")
	}
	return nil
}

// openBlockerIDs returns the open blocker finding ids of the version when they
// are not acknowledged. An acknowledged approval skips the id scan but still
// counts the findings through the repository so a gate bypass is impossible.
func (s *ReviewService) openBlockerIDs(ctx context.Context, versionID int64, acknowledged bool) ([]int64, error) {
	count, err := s.deps.Findings.CountOpenBlockers(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if acknowledged || count == 0 {
		return nil, nil
	}
	blockers, err := s.deps.Findings.List(ctx, versionID, domain.FindingOpen, domain.SeverityBlocker, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(blockers))
	for _, blocker := range blockers {
		ids = append(ids, blocker.ID)
	}
	return ids, nil
}

// approveReview approves the latest draft review and creates an approved review
// with no findings when the version has none. An already-approved row is reused
// only when it appeared concurrently: a stale one was demoted by the caller.
func (s *ReviewService) approveReview(ctx context.Context, versionID, actor int64, comment string) (domain.CaseReview, error) {
	latest, err := s.deps.Reviews.GetLatest(ctx, versionID)
	if err != nil {
		if !reviewIsNotFound(err) {
			return domain.CaseReview{}, err
		}
		return s.createReview(ctx, versionID, actor, domain.ReviewApproved, comment)
	}
	switch latest.Status {
	case domain.ReviewApproved:
		return latest, nil
	case domain.ReviewDraft:
		return s.deps.Reviews.Decide(ctx, latest.ID, domain.ReviewApproved, comment, actor)
	default:
		// A rejected round cannot be approved again; record a fresh approval.
		return s.createReview(ctx, versionID, actor, domain.ReviewApproved, comment)
	}
}

// revertStaleApprovedReview demotes an approved review that has no baseline back
// to draft, so the current approval can record its own approved row without
// colliding with uk_case_review_approved. The caller has already established that
// the version has no baseline; the re-check here covers a concurrent freeze.
func (s *ReviewService) revertStaleApprovedReview(ctx context.Context, versionID, actor int64) error {
	approved, err := s.deps.Reviews.GetApproved(ctx, versionID)
	if err != nil {
		if reviewIsNotFound(err) {
			return nil
		}
		return err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return err
	}
	if baseline != nil {
		return nil
	}
	if _, err := s.deps.Reviews.Reopen(ctx, approved.ID, "", actor); err != nil {
		return err
	}
	return nil
}

// revertReviewAfterFreezeFailure compensates a failed baseline freeze by putting
// the already-approved review back to draft. A concurrent approval that committed
// a baseline wins: the review then stays bound to it. The caller keeps the
// original freeze error, so a failed revert is only logged as a warning.
func (s *ReviewService) revertReviewAfterFreezeFailure(ctx context.Context, versionID int64, review domain.CaseReview, actor int64) {
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: could not confirm the baseline absence after a failed freeze; review left approved",
			"version_id", versionID, "review_id", review.ID, "error", err)
		return
	}
	if baseline != nil {
		return
	}
	// Reopen only updates an approved row; the empty comment keeps the approval
	// comment recorded on the review.
	if _, err := s.deps.Reviews.Reopen(ctx, review.ID, "", actor); err != nil {
		slog.WarnContext(ctx, "mindmap: revert review after a failed baseline freeze failed",
			"version_id", versionID, "review_id", review.ID, "error", err)
	}
}

// rejectReview rejects the latest draft review, or records a rejected review with
// no findings when the version has no draft round left.
func (s *ReviewService) rejectReview(ctx context.Context, versionID, actor int64, comment string) (domain.CaseReview, error) {
	latest, err := s.deps.Reviews.GetLatest(ctx, versionID)
	if err != nil {
		if !reviewIsNotFound(err) {
			return domain.CaseReview{}, err
		}
		return s.createReview(ctx, versionID, actor, domain.ReviewRejected, comment)
	}
	if latest.Status == domain.ReviewDraft {
		return s.deps.Reviews.Decide(ctx, latest.ID, domain.ReviewRejected, comment, actor)
	}
	return s.createReview(ctx, versionID, actor, domain.ReviewRejected, comment)
}

// createReview persists a decided review row with no findings.
func (s *ReviewService) createReview(ctx context.Context, versionID, actor int64, status, comment string) (domain.CaseReview, error) {
	if s.deps.IDs == nil {
		return domain.CaseReview{}, errors.New("mindmap: id generator is not configured")
	}
	id, err := s.nextID()
	if err != nil {
		return domain.CaseReview{}, err
	}
	now := s.now()
	decidedBy := actor
	decidedAt := now
	review := domain.CaseReview{
		ID:         id,
		VersionID:  versionID,
		Status:     status,
		Comment:    comment,
		ReviewerID: actor,
		DecidedBy:  &decidedBy,
		DecidedAt:  &decidedAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	return s.deps.Reviews.Create(ctx, review)
}

// publish sends one event after the commit. A publish failure is logged and never
// returned to the caller (spec §6.11).
func (s *ReviewService) publish(ctx context.Context, event Event) {
	if s.deps.Events == nil {
		return
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "mindmap event publish failed",
			"type", event.Type, "version_id", event.VersionID, "error", err)
	}
}

// nextID mints one application-generated Snowflake id (D29).
func (s *ReviewService) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("mindmap: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, errors.New("mindmap: id generator returned a non-positive id")
	}
	return id, nil
}

// now returns the injected clock time (falling back to the wall clock).
func (s *ReviewService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// reviewVersionStateError is the stable 409 of a review decision requested on a
// version that is not reviewing.
func reviewVersionStateError(versionID int64, status string) error {
	return domain.NewStateConflict("version_state_conflict",
		fmt.Sprintf("version %d is %s; review decisions require reviewing", versionID, status))
}

// reviewHashPrefix bounds the graph hash carried by an event to its short prefix
// (spec §6.11: baselineId/nodeCount/caseCount/graphHash prefix).
func reviewHashPrefix(hash string) string {
	const prefix = 12
	if len(hash) <= prefix {
		return hash
	}
	return hash[:prefix]
}

// reviewIsNotFound reports whether err is a mindmap not-found domain error.
func reviewIsNotFound(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound
}
