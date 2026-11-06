package executionapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// v0.22 R-W2-1: approval / blocked_recovery / conflict decision requests.
// These requests are version-level (no case_run) and never gate the stage
// machine; only blocked_recovery approve asks the version stage port to unblock.

// fakeStageUnblocker is the narrow version-stage port fake. It records every
// Unblock call and keeps the version's blocked flag so a reject can be proven
// not to clear it.
type fakeStageUnblocker struct {
	mu      sync.Mutex
	calls   []int64
	blocked map[int64]bool
	err     error
}

func newFakeStageUnblocker(blocked ...int64) *fakeStageUnblocker {
	port := &fakeStageUnblocker{blocked: map[int64]bool{}}
	for _, versionID := range blocked {
		port.blocked[versionID] = true
	}
	return port
}

func (f *fakeStageUnblocker) Unblock(_ context.Context, versionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, versionID)
	if f.err != nil {
		return f.err
	}
	f.blocked[versionID] = false
	return nil
}

func (f *fakeStageUnblocker) unblockedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.calls...)
}

func (f *fakeStageUnblocker) isBlocked(versionID int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked[versionID]
}

// decisionFixture wires the human decision service over the shared task-9
// fakes plus the narrow stage port.
type decisionFixture struct {
	humans    *fakeHumanStore
	events    *corePublisher
	audit     *coreAudit
	clock     *coreClock
	ids       *coreIDs
	stage     *fakeStageUnblocker
	svc       *HumanService
	questions *QuestionService
}

func newDecisionFixture() *decisionFixture {
	now := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	settings := &fakeSettings{values: map[string]any{"question.timeout_seconds": int64(86400)}}
	f := &decisionFixture{
		humans: newFakeHumanStore(),
		events: &corePublisher{},
		audit:  &coreAudit{},
		clock:  &coreClock{now: now},
		ids:    &coreIDs{next: 5000},
		stage:  newFakeStageUnblocker(7, 8),
	}
	f.svc = NewHumanService(HumanServiceDeps{
		Humans:   f.humans,
		CaseRuns: newFakeCaseRunStore(),
		Waves:    newFakeWaveStore(),
		Settings: settings,
		IDs:      f.ids,
		Clock:    f.clock,
		Events:   f.events,
		Audit:    f.audit,
		Stages:   f.stage,
	})
	f.questions = NewQuestionService(QuestionServiceDeps{
		Humans:   f.humans,
		Settings: settings,
		Clock:    f.clock,
		Events:   f.events,
		Audit:    f.audit,
	})
	return f
}

// snapshot returns the recorded events without mutating the publisher.
func (p *corePublisher) snapshot() []eventingapp.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]eventingapp.Event(nil), p.events...)
}

func eventStringValue(event eventingapp.Event, key string) string {
	value, _ := event.Payload[key].(string)
	return value
}

// requireEventPayload pins the frozen event payload fields (decimal-string IDs).
func requireEventPayload(t *testing.T, event eventingapp.Event, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if got := eventStringValue(event, key); got != value {
			t.Fatalf("%s payload[%q] = %q, want %q (payload=%#v)", event.Type, key, got, value, event.Payload)
		}
	}
}

func requireOneEvent(t *testing.T, publisher *corePublisher, eventType string) eventingapp.Event {
	t.Helper()
	events := publisher.byType(eventType)
	if len(events) != 1 {
		t.Fatalf("%s events = %d (%v), want 1", eventType, len(events), publisher.types())
	}
	return events[0]
}

// byType returns every recorded event of one type in publish order.
func (p *corePublisher) byType(eventType string) []eventingapp.Event {
	events := p.snapshot()
	matched := make([]eventingapp.Event, 0, len(events))
	for _, event := range events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

func TestApprovalCreateResolveEmitsEvents(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()

	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "approval", Reason: "发布？", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if request.ID == 0 || request.Kind != execution.HumanApproval || request.Status != execution.HumanPending {
		t.Fatalf("request = %+v, want a pending approval request", request)
	}
	if request.CaseRunID != nil {
		t.Fatalf("case_run_id = %v, want nil (version-level approval)", request.CaseRunID)
	}
	if ttl := request.ExpiresAt.Sub(f.clock.now); ttl != 24*time.Hour {
		t.Fatalf("expires_at ttl = %s, want question.timeout_seconds (24h)", ttl)
	}

	requested := requireOneEvent(t, f.events, "approval.requested")
	if requested.Channel != "version:7" {
		t.Fatalf("approval.requested channel = %q, want version:7", requested.Channel)
	}
	requireEventPayload(t, requested, map[string]string{
		"human_request_id": platformid.Format(request.ID),
		"kind":             "approval",
		"version_id":       "7",
		"title":            "发布？",
		"reason":           "发布？",
		"actor":            "9",
	})

	resolved, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "rejected", "reason": "no"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != execution.HumanAnswered {
		t.Fatalf("status = %s, want answered", resolved.Status)
	}
	if resolved.Response["decision"] != "rejected" || resolved.Response["reason"] != "no" {
		t.Fatalf("response = %#v, want the rejected decision + reason", resolved.Response)
	}
	if resolved.ResolvedBy == nil || *resolved.ResolvedBy != 9 || resolved.ResolvedAt == nil {
		t.Fatalf("resolved_by/at = %v/%v", resolved.ResolvedBy, resolved.ResolvedAt)
	}
	persisted, err := f.humans.Get(ctx, request.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Response["decision"] != "rejected" || persisted.Response["reason"] != "no" {
		t.Fatalf("persisted response = %#v", persisted.Response)
	}

	decided := requireOneEvent(t, f.events, "approval.decided")
	if decided.Channel != "version:7" {
		t.Fatalf("approval.decided channel = %q, want version:7", decided.Channel)
	}
	requireEventPayload(t, decided, map[string]string{
		"human_request_id": platformid.Format(request.ID),
		"kind":             "approval",
		"version_id":       "7",
		"decision":         "rejected",
		"reason":           "no",
		"actor":            "9",
	})
	if got := f.events.count(EventHumanResolved); got != 0 {
		t.Fatalf("human_request.resolved events = %d, want the approval.decided event only", got)
	}
	// A decision request never resumes a case run/wave.
	for _, eventType := range f.events.types() {
		switch eventType {
		case EventCaseRunResumed, EventCaseRunPaused, EventWavePaused, EventWaveResumed, EventHumanCreated:
			t.Fatalf("decision request published execution event %q", eventType)
		}
	}
}

func TestBlockedRecoveryApproveUnblocksAndRejectKeepsBlocked(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()

	approvedRequest, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "blocked_recovery", Reason: "执行器离线", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if !f.stage.isBlocked(7) {
		t.Fatal("fixture: version 7 must start blocked")
	}
	resolved, err := f.svc.Resolve(ctx, approvedRequest.ID, 9, map[string]any{"decision": "approved"})
	if err != nil {
		t.Fatalf("Resolve approved: %v", err)
	}
	if resolved.Status != execution.HumanAnswered {
		t.Fatalf("status = %s, want answered", resolved.Status)
	}
	if got := f.stage.unblockedIDs(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("Unblock calls = %v, want [7]", got)
	}
	if f.stage.isBlocked(7) {
		t.Fatal("approved blocked_recovery left version 7 blocked")
	}

	rejectedRequest, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 8, Kind: "blocked_recovery", Reason: "账号失效", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest rejected: %v", err)
	}
	rejected, err := f.svc.Resolve(ctx, rejectedRequest.ID, 9, map[string]any{"decision": "rejected", "reason": "账号未修复"})
	if err != nil {
		t.Fatalf("Resolve rejected: %v", err)
	}
	if rejected.Status != execution.HumanAnswered {
		t.Fatalf("rejected status = %s, want answered", rejected.Status)
	}
	if rejected.Response["decision"] != "rejected" || rejected.Response["reason"] != "账号未修复" {
		t.Fatalf("rejected response = %#v, want the recorded reason", rejected.Response)
	}
	if got := f.stage.unblockedIDs(); len(got) != 1 {
		t.Fatalf("reject called Unblock: calls = %v, want only the approve call", got)
	}
	if !f.stage.isBlocked(8) {
		t.Fatal("rejected blocked_recovery cleared version 8's blocker")
	}
	if got := f.events.count("approval.decided"); got != 2 {
		t.Fatalf("approval.decided events = %d, want 2", got)
	}
}

func TestBlockedRecoveryApproveWithoutStagePortIsNilSafe(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()
	f.svc.SetStageUnblocker(nil)

	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "blocked_recovery", Reason: "执行器离线", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	resolved, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "approved"})
	if err != nil {
		t.Fatalf("Resolve without a stage port: %v", err)
	}
	if resolved.Status != execution.HumanAnswered {
		t.Fatalf("status = %s, want answered", resolved.Status)
	}
}

func TestBlockedRecoveryUnblockFailureKeepsRequestPending(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()
	f.stage.err = errors.New("blocker not cleared")

	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "blocked_recovery", Reason: "执行器离线", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "approved"}); err == nil {
		t.Fatal("Resolve ignored the stage Unblock failure")
	}
	persisted, err := f.humans.Get(ctx, request.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Status != execution.HumanPending {
		t.Fatalf("status = %s, want pending so the decision can be retried", persisted.Status)
	}
	if got := f.events.count("approval.decided"); got != 0 {
		t.Fatalf("approval.decided events = %d, want 0 on a failed unblock", got)
	}
}

// TestBlockedRecoveryStageGateErrorMapsToConflict pins the review finding that a
// typed version-stage rejection (gate closed / invalid transition) must surface
// as its 409 platform error instead of leaking as an opaque 500, while the
// request stays pending so the decision can be retried and no approval.decided
// is published.
func TestBlockedRecoveryStageGateErrorMapsToConflict(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()
	f.stage.err = &versionapp.StageGateError{
		Gate:   "blocker",
		Reason: "blocker_not_cleared",
		Action: "clear the blocker before approving the recovery",
		Next:   "stage_request",
	}

	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "blocked_recovery", Reason: "执行器离线", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "approved"}); err == nil {
		t.Fatal("Resolve accepted a closed stage gate")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("Resolve error = %T %v, want a *platform.Error (409), not a raw stage error", err, err)
		}
		if apiErr.Status != http.StatusConflict || apiErr.Code != "stage_gate_failed" {
			t.Fatalf("Resolve error = %d %q, want 409 stage_gate_failed", apiErr.Status, apiErr.Code)
		}
		if apiErr.Details["reason"] != "blocker_not_cleared" {
			t.Fatalf("Resolve error details = %#v, want the gate reason", apiErr.Details)
		}
	}

	persisted, err := f.humans.Get(ctx, request.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Status != execution.HumanPending {
		t.Fatalf("status = %s, want pending after the stage rejection", persisted.Status)
	}
	if got := f.events.count("approval.decided"); got != 0 {
		t.Fatalf("approval.decided events = %d, want 0 when the stage gate rejects", got)
	}
}

// TestApprovalEventsIncludeConversationAndMessage pins EV-5: the approval
// requested/decided payloads carry conversation_id/message_id when the request
// was created with them, and omit the keys when absent.
func TestApprovalEventsIncludeConversationAndMessage(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()

	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "approval", Reason: "发布？", ActorID: 9,
		ConversationID: 2001, MessageID: 3001,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	requested := requireOneEvent(t, f.events, "approval.requested")
	requireEventPayload(t, requested, map[string]string{
		"conversation_id": "2001",
		"message_id":      "3001",
	})

	if _, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "approved"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	decided := requireOneEvent(t, f.events, "approval.decided")
	requireEventPayload(t, decided, map[string]string{
		"conversation_id": "2001",
		"message_id":      "3001",
	})

	// Without the optional hints the keys must be omitted rather than rendered
	// as the zero ID.
	f2 := newDecisionFixture()
	plain, err := f2.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "approval", Reason: "发布？", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest without hints: %v", err)
	}
	plainRequested := requireOneEvent(t, f2.events, "approval.requested")
	for _, key := range []string{"conversation_id", "message_id"} {
		if _, ok := plainRequested.Payload[key]; ok {
			t.Fatalf("approval.requested payload[%q] present without a hint: %#v", key, plainRequested.Payload)
		}
	}
	if _, err := f2.svc.Resolve(ctx, plain.ID, 9, map[string]any{"decision": "approved"}); err != nil {
		t.Fatalf("Resolve without hints: %v", err)
	}
	plainDecided := requireOneEvent(t, f2.events, "approval.decided")
	for _, key := range []string{"conversation_id", "message_id"} {
		if _, ok := plainDecided.Payload[key]; ok {
			t.Fatalf("approval.decided payload[%q] present without a hint: %#v", key, plainDecided.Payload)
		}
	}
}

func TestConflictDecisionRecordsOnly(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()

	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "conflict", Reason: "需求冲突：T+1 vs T+3", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	resolved, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "approved", "reason": "按 T+1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != execution.HumanAnswered {
		t.Fatalf("status = %s, want answered", resolved.Status)
	}
	if resolved.Response["decision"] != "approved" || resolved.Response["reason"] != "按 T+1" {
		t.Fatalf("response = %#v", resolved.Response)
	}
	if got := f.stage.unblockedIDs(); len(got) != 0 {
		t.Fatalf("conflict decision called Unblock: %v", got)
	}
	requireOneEvent(t, f.events, "approval.decided")
	requireOneEvent(t, f.events, "approval.requested")
}

func TestCreateRequestValidatesDecisionPayloads(t *testing.T) {
	ctx := context.Background()
	cases := map[string]HumanCreateInput{
		"empty kind":       {VersionID: 7, Kind: "  ", Reason: "x"},
		"login takeover":   {VersionID: 7, Kind: "login_takeover", Reason: "x"},
		"clarification":    {VersionID: 7, Kind: "clarification_question", Reason: "x"},
		"other kind":       {VersionID: 7, Kind: "other", Reason: "x"},
		"missing version":  {VersionID: 0, Kind: "approval", Reason: "x"},
		"empty reason":     {VersionID: 7, Kind: "approval", Reason: "   "},
		"oversized reason": {VersionID: 7, Kind: "conflict", Reason: strings.Repeat("x", 2001)},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDecisionFixture()
			_, err := f.svc.CreateRequest(ctx, input)
			requireAPIError(t, err, "invalid_argument")
			rows, _, listErr := f.humans.ListByVersion(ctx, 7, "", 10, nil)
			if listErr != nil {
				t.Fatalf("ListByVersion: %v", listErr)
			}
			if len(rows) != 0 {
				t.Fatalf("invalid input persisted %d human_request rows", len(rows))
			}
			if got := f.events.count("approval.requested"); got != 0 {
				t.Fatalf("invalid input published %d approval.requested events", got)
			}
		})
	}
}

func TestResolveDecisionValidatesDecision(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()
	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "approval", Reason: "发布？", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	invalid := []map[string]any{
		{"code": "123456"},
		{"decision": "maybe"},
		{"decision": ""},
		{"decision": "rejected"},
		{"decision": "rejected", "reason": "   "},
	}
	for _, response := range invalid {
		if _, err := f.svc.Resolve(ctx, request.ID, 9, response); err == nil {
			t.Fatalf("Resolve accepted invalid decision response %#v", response)
		}
	}
	persisted, err := f.humans.Get(ctx, request.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Status != execution.HumanPending {
		t.Fatalf("status = %s, want pending after invalid decisions", persisted.Status)
	}
	if got := f.events.count("approval.decided"); got != 0 {
		t.Fatalf("approval.decided events = %d, want 0", got)
	}

	if _, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "approved"}); err != nil {
		t.Fatalf("Resolve approved: %v", err)
	}
	if _, err := f.svc.Resolve(ctx, request.ID, 9, map[string]any{"decision": "approved"}); err == nil {
		t.Fatal("second Resolve of an answered request succeeded")
	} else {
		requireAPIError(t, err, "state_conflict")
	}
}

// TestDecisionRequestsAppearInQuestionTodo pins the spec extension of
// GET /api/versions/{id}/questions?status=: the approval/blocked_recovery/
// conflict pending rows are part of the version todo projection while the
// question answer surface still only scopes clarification_question rows.
func TestDecisionRequestsAppearInQuestionTodo(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()
	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "approval", Reason: "发布？", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	rows, _, err := f.questions.List(ctx, 7, string(execution.HumanPending), 50, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != request.ID || rows[0].Kind != execution.HumanApproval {
		t.Fatalf("pending todo rows = %#v, want the approval request", rows)
	}
	if _, err := f.questions.Get(ctx, 7, request.ID); err == nil {
		t.Fatal("the question read accepted a decision request")
	} else {
		requireAPIError(t, err, "not_found")
	}
}

func TestExpireQuestionsWorker(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture()
	request, err := f.svc.CreateRequest(ctx, HumanCreateInput{
		VersionID: 7, Kind: "approval", Reason: "发布？", ActorID: 9,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	f.clock.now = f.clock.now.Add(25 * time.Hour)

	count, err := f.svc.Expire(ctx, f.clock.now)
	if err != nil || count != 1 {
		t.Fatalf("human Expire = %d, %v; want 1, nil", count, err)
	}
	persisted, err := f.humans.Get(ctx, request.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Status != execution.HumanExpired {
		t.Fatalf("status = %s, want expired", persisted.Status)
	}
	if got := f.events.count(EventHumanExpired); got != 1 {
		t.Fatalf("human_request.expired events = %d, want 1", got)
	}
	if got := f.stage.unblockedIDs(); len(got) != 0 {
		t.Fatalf("expiry called Unblock: %v", got)
	}

	// The structured-question sweep stays independent and keeps working.
	question := execution.HumanRequest{
		ID:        8101,
		VersionID: 7,
		Kind:      execution.HumanClarificationQuestion,
		Status:    execution.HumanPending,
		ExpiresAt: f.clock.now.Add(-time.Hour),
		CreatedAt: f.clock.now.Add(-25 * time.Hour),
		UpdatedAt: f.clock.now.Add(-25 * time.Hour),
	}
	if err := f.humans.Create(ctx, question); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	questionsCount, err := f.questions.ExpireQuestions(ctx, f.clock.now)
	if err != nil || questionsCount != 1 {
		t.Fatalf("question ExpireQuestions = %d, %v; want 1, nil", questionsCount, err)
	}

	// The sweep is idempotent: a second pass expires nothing.
	again, err := f.svc.Expire(ctx, f.clock.now)
	if err != nil || again != 0 {
		t.Fatalf("second human sweep = %d, %v; want 0, nil", again, err)
	}
	// Expiry never pauses/fails execution.
	for _, eventType := range f.events.types() {
		switch eventType {
		case EventCaseRunPaused, EventCaseRunFinished, EventWavePaused:
			t.Fatalf("expiry published execution event %q", eventType)
		}
	}
}
