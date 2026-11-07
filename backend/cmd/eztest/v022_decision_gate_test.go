package main

// v0.22 W2-2F regression tests: human decision rows (approval/blocked_recovery/
// conflict) are recorded decisions, not requirement gaps and not Host turn
// blockers. These tests run the real execution services over an in-memory human
// request repository, so they pin the composition-root behaviour without a
// database.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// v022FakeHumanRepo is the in-memory HumanRequestRepository used by the W2-2F
// regression tests. Only the read/transition paths the tests exercise are
// implemented; the unused write paths return the same empty results the real
// repository returns for a version without matches.
type v022FakeHumanRepo struct {
	mu       sync.Mutex
	requests []execution.HumanRequest
}

func (r *v022FakeHumanRepo) seed(requests ...execution.HumanRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, requests...)
}

func (r *v022FakeHumanRepo) Create(_ context.Context, request execution.HumanRequest) error {
	r.seed(request)
	return nil
}

func (r *v022FakeHumanRepo) Get(_ context.Context, id int64) (execution.HumanRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, request := range r.requests {
		if request.ID == id {
			return request, nil
		}
	}
	return execution.HumanRequest{}, errors.New("human request not found")
}

func (r *v022FakeHumanRepo) GetOpenByCaseRun(context.Context, int64) (*execution.HumanRequest, error) {
	return nil, nil
}

func (r *v022FakeHumanRepo) ListByVersion(_ context.Context, versionID int64, status string, _ int, _ *int64) ([]execution.HumanRequest, bool, error) {
	return r.listQuestions(versionID, status, 0), false, nil
}

func (r *v022FakeHumanRepo) ListOpen(context.Context) ([]execution.HumanRequest, error) {
	return nil, nil
}

func (r *v022FakeHumanRepo) ListQuestions(_ context.Context, versionID int64, status string, limit int, _ *int64) ([]execution.HumanRequest, bool, error) {
	rows := r.listQuestions(versionID, status, limit)
	return rows, false, nil
}

func (r *v022FakeHumanRepo) CountByCaseRun(context.Context, int64) (int, error) { return 0, nil }

func (r *v022FakeHumanRepo) Transition(_ context.Context, id int64, from, to execution.HumanRequestStatus, response map[string]any, actorID int64, at time.Time, _ *time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.requests {
		if r.requests[i].ID != id || r.requests[i].Status != from {
			continue
		}
		r.requests[i].Status = to
		r.requests[i].Response = response
		r.requests[i].ResolvedBy = &actorID
		r.requests[i].ResolvedAt = &at
		r.requests[i].UpdatedAt = at
		return true, nil
	}
	return false, nil
}

func (r *v022FakeHumanRepo) AttachMessage(context.Context, int64, int64) error { return nil }

func (r *v022FakeHumanRepo) Expire(context.Context, time.Time) ([]execution.HumanRequest, error) {
	return nil, nil
}

func (r *v022FakeHumanRepo) ExpireQuestions(context.Context, time.Time) ([]execution.HumanRequest, error) {
	return nil, nil
}

func (r *v022FakeHumanRepo) listQuestions(versionID int64, status string, limit int) []execution.HumanRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := make([]execution.HumanRequest, 0, len(r.requests))
	for _, request := range r.requests {
		if request.VersionID != versionID {
			continue
		}
		switch request.Kind {
		case execution.HumanClarificationQuestion, execution.HumanApproval,
			execution.HumanBlockedRecovery, execution.HumanConflict:
		default:
			continue
		}
		if status != "" && string(request.Status) != status {
			continue
		}
		rows = append(rows, request)
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// v022RecordingPublisher records every published event for the decision tests.
type v022RecordingPublisher struct {
	mu     sync.Mutex
	events []eventingapp.Event
}

func (p *v022RecordingPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *v022RecordingPublisher) count(eventType string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, event := range p.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// v022FixedClock is the injectable execution clock of the decision tests.
type v022FixedClock struct{ now time.Time }

func (c v022FixedClock) Now() time.Time { return c.now }

// v022FakeStageUnblocker is the narrow version-stage port fake. It records every
// Unblock call and returns the configured typed stage error.
type v022FakeStageUnblocker struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *v022FakeStageUnblocker) Unblock(context.Context, int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *v022FakeStageUnblocker) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newV022Questions builds the real structured-question service over the fake
// repository. Settings/IDs/messages are not touched by List or TurnAnswered.
func newV022Questions(repo *v022FakeHumanRepo, now time.Time) *executionapp.QuestionService {
	return executionapp.NewQuestionService(executionapp.QuestionServiceDeps{
		Humans: repo,
		Clock:  v022FixedClock{now: now},
	})
}

// decisionRows returns one pending approval, blocked_recovery and conflict
// request for the version: the decision rows W2-2 added to the question list.
func decisionRows(versionID int64) []execution.HumanRequest {
	return []execution.HumanRequest{
		{ID: 101, VersionID: versionID, Kind: execution.HumanApproval, Status: execution.HumanPending},
		{ID: 102, VersionID: versionID, Kind: execution.HumanBlockedRecovery, Status: execution.HumanPending},
		{ID: 103, VersionID: versionID, Kind: execution.HumanConflict, Status: execution.HumanPending},
	}
}

// TestOpenGapsCountsOnlyPendingClarificationQuestions pins finding 1/2: pending
// approval/blocked_recovery/conflict rows must not count as requirement gaps,
// while a pending clarification question still does.
func TestOpenGapsCountsOnlyPendingClarificationQuestions(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	repo := &v022FakeHumanRepo{}
	repo.seed(decisionRows(7)...)
	repo.seed(execution.HumanRequest{
		ID: 100, VersionID: 7, Kind: execution.HumanClarificationQuestion, Status: execution.HumanPending,
	})
	questions := newV022Questions(repo, now)
	gates := newV022StageGates(v022StageGateDeps{
		Requirements: &versionapp.RequirementService{},
		Questions:    questions,
	})
	if gates.OpenGaps == nil {
		t.Fatal("OpenGaps gate is nil")
	}

	open, err := gates.OpenGaps(context.Background(), 7)
	if err != nil {
		t.Fatalf("OpenGaps: %v", err)
	}
	if open != 1 {
		t.Fatalf("OpenGaps = %d, want 1 (only the pending clarification question; decision rows are not gaps)", open)
	}

	// A version with only decision rows has no requirement gap, so the
	// requirement_review -> designing gate is not blocked by them.
	decisionOnly := &v022FakeHumanRepo{}
	decisionOnly.seed(decisionRows(8)...)
	decisionGates := newV022StageGates(v022StageGateDeps{
		Requirements: &versionapp.RequirementService{},
		Questions:    newV022Questions(decisionOnly, now),
	})
	open, err = decisionGates.OpenGaps(context.Background(), 8)
	if err != nil {
		t.Fatalf("OpenGaps(decision-only): %v", err)
	}
	if open != 0 {
		t.Fatalf("OpenGaps(decision-only) = %d, want 0", open)
	}
}

// TestManualBlockerClearedIgnoresDecisionRows pins finding 2a: a manual blocker
// is cleared when no clarification question remains even though the
// blocked_recovery decision row that asks to clear it is still pending.
func TestManualBlockerClearedIgnoresDecisionRows(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	repo := &v022FakeHumanRepo{}
	repo.seed(decisionRows(7)...)
	gates := newV022StageGates(v022StageGateDeps{
		Waves:        fakeWiringWaves{},
		Requirements: &versionapp.RequirementService{},
		Questions:    newV022Questions(repo, now),
	})
	if gates.BlockerCleared == nil {
		t.Fatal("BlockerCleared gate is nil")
	}
	cleared, err := gates.BlockerCleared(context.Background(), 7, versionapp.Blocker{Kind: versionapp.BlockerKindManual})
	if err != nil {
		t.Fatalf("BlockerCleared: %v", err)
	}
	if !cleared {
		t.Fatal("manual blocker still blocked by a pending decision row (self-lock)")
	}
}

// TestHostTurnAnsweredIgnoresDecisionRows pins finding 2c: a pending version
// decision must not keep the Host task_complete waiting, but a pending
// clarification question still must.
func TestHostTurnAnsweredIgnoresDecisionRows(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	repo := &v022FakeHumanRepo{}
	repo.seed(decisionRows(7)...)
	reader := hostTurnCompletionReader{questions: newV022Questions(repo, now)}

	answered, err := reader.TurnAnswered(context.Background(), tools.Scope{VersionID: 7})
	if err != nil {
		t.Fatalf("TurnAnswered(decision-only): %v", err)
	}
	if !answered {
		t.Fatal("TurnAnswered = false, want true: decision rows are not outstanding clarification questions")
	}

	repo.seed(execution.HumanRequest{
		ID: 100, VersionID: 7, Kind: execution.HumanClarificationQuestion, Status: execution.HumanPending,
	})
	answered, err = reader.TurnAnswered(context.Background(), tools.Scope{VersionID: 7})
	if err != nil {
		t.Fatalf("TurnAnswered(pending clarification): %v", err)
	}
	if answered {
		t.Fatal("TurnAnswered = true, want false: a pending clarification question is an outstanding turn")
	}
}

// TestDecisionStageUnblockerTreatsAlreadyUnblockedAsSuccess pins finding 3: the
// version StageService rejects Unblock on a non-blocked version with a typed
// StageTransitionError; the composition-root port must treat that as success
// (the blocker was already cleared, e.g. through the version panel) while every
// genuine stage rejection keeps flowing to the 409 mapping.
func TestDecisionStageUnblockerTreatsAlreadyUnblockedAsSuccess(t *testing.T) {
	ctx := context.Background()
	alreadyUnblocked := &v022FakeStageUnblocker{err: &versionapp.StageTransitionError{
		From:   versiondomain.StageRequirementReview,
		Action: "only a blocked version can be unblocked",
	}}
	if err := (decisionStageUnblocker{stages: alreadyUnblocked}).Unblock(ctx, 7); err != nil {
		t.Fatalf("Unblock(already unblocked) = %v, want success", err)
	}
	if alreadyUnblocked.callCount() != 1 {
		t.Fatalf("Unblock calls = %d, want 1", alreadyUnblocked.callCount())
	}

	closedGate := &v022FakeStageUnblocker{err: &versionapp.StageGateError{
		Gate: "blocker", Reason: "blocker_not_cleared",
	}}
	err := decisionStageUnblocker{stages: closedGate}.Unblock(ctx, 7)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) || gateErr.Reason != "blocker_not_cleared" {
		t.Fatalf("Unblock(closed gate) = %v, want the typed blocker gate error", err)
	}

	invalidTransition := &v022FakeStageUnblocker{err: &versionapp.StageTransitionError{
		From: versiondomain.StageBlocked,
	}}
	err = decisionStageUnblocker{stages: invalidTransition}.Unblock(ctx, 7)
	var transitionErr *versionapp.StageTransitionError
	if !errors.As(err, &transitionErr) || transitionErr.From != versiondomain.StageBlocked {
		t.Fatalf("Unblock(invalid transition from blocked) = %v, want the typed transition error preserved", err)
	}
}

// TestBlockedRecoveryApproveOnAlreadyUnblockedVersionCompletes pins the W2-3
// self-lock regression: the user clears the blocker through the version panel,
// then approves the still-pending blocked_recovery card. Resolve must answer the
// request exactly once instead of returning the invalid-transition 409.
func TestBlockedRecoveryApproveOnAlreadyUnblockedVersionCompletes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	repo := &v022FakeHumanRepo{}
	repo.seed(execution.HumanRequest{
		ID: 501, VersionID: 7, Kind: execution.HumanBlockedRecovery,
		Status: execution.HumanPending, ExpiresAt: now.Add(time.Hour),
	})
	stage := &v022FakeStageUnblocker{err: &versionapp.StageTransitionError{
		From:   versiondomain.StageRequirementReview,
		Action: "only a blocked version can be unblocked",
	}}
	publisher := &v022RecordingPublisher{}
	humans := executionapp.NewHumanService(executionapp.HumanServiceDeps{
		Humans: repo,
		Clock:  v022FixedClock{now: now},
		Events: publisher,
	})
	humans.SetStageUnblocker(decisionStageUnblocker{stages: stage})

	request, err := humans.Resolve(ctx, 501, 9, map[string]any{"decision": "approved"})
	if err != nil {
		t.Fatalf("Resolve on an already-unblocked version = %v, want success without a self-lock 409", err)
	}
	if request.Status != execution.HumanAnswered {
		t.Fatalf("status = %s, want answered", request.Status)
	}
	if stage.callCount() != 1 {
		t.Fatalf("Unblock calls = %d, want 1", stage.callCount())
	}
	if got := publisher.count(executionapp.EventApprovalDecided); got != 1 {
		t.Fatalf("approval.decided events = %d, want exactly 1", got)
	}
}

// TestBlockedRecoveryApproveKeepsGenuineGateConflict pins constraint 5: the W2-2
// 409 mapping survives the idempotency wrapper. A blocker that is really not
// cleared still keeps the request pending and publishes no approval.decided.
func TestBlockedRecoveryApproveKeepsGenuineGateConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	repo := &v022FakeHumanRepo{}
	repo.seed(execution.HumanRequest{
		ID: 502, VersionID: 7, Kind: execution.HumanBlockedRecovery,
		Status: execution.HumanPending, ExpiresAt: now.Add(time.Hour),
	})
	stage := &v022FakeStageUnblocker{err: &versionapp.StageGateError{
		Gate: "blocker", Reason: "blocker_not_cleared",
	}}
	publisher := &v022RecordingPublisher{}
	humans := executionapp.NewHumanService(executionapp.HumanServiceDeps{
		Humans: repo,
		Clock:  v022FixedClock{now: now},
		Events: publisher,
	})
	humans.SetStageUnblocker(decisionStageUnblocker{stages: stage})

	if _, err := humans.Resolve(ctx, 502, 9, map[string]any{"decision": "approved"}); err == nil {
		t.Fatal("Resolve accepted a genuinely closed blocker gate")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("Resolve error = %T %v, want the mapped *platform.Error", err, err)
		}
		if apiErr.Status != http.StatusConflict || apiErr.Code != "stage_gate_failed" {
			t.Fatalf("Resolve error = %d %q, want 409 stage_gate_failed", apiErr.Status, apiErr.Code)
		}
	}
	persisted, err := repo.Get(ctx, 502)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Status != execution.HumanPending {
		t.Fatalf("status = %s, want pending after the genuine gate rejection", persisted.Status)
	}
	if got := publisher.count(executionapp.EventApprovalDecided); got != 0 {
		t.Fatalf("approval.decided events = %d, want 0 when the blocker gate rejects", got)
	}
}
