//go:build integration

package executionpg

import (
	"context"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestHumanRequestDecisionKindsPersistAndExpire covers the W2-2 repository
// extension: approval/blocked_recovery/conflict rows are created version-level,
// their decision/reason is persisted in response, they participate in the
// decision expiry sweep, and they show up in the version question todo read
// (while the login takeover sweep/list stays separate).
func TestHumanRequestDecisionKindsPersistAndExpire(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := &HumanRequestRepository{base{pool: pool}}
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)

	decision := execution.HumanRequest{
		ID: 7001, VersionID: 1001, Kind: execution.HumanApproval,
		Payload: map[string]any{"reason": "发布？"}, Status: execution.HumanPending,
		ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, decision); err != nil {
		t.Fatalf("Create approval: %v", err)
	}
	changed, err := repo.Transition(ctx, decision.ID, execution.HumanPending, execution.HumanAnswered,
		map[string]any{"decision": "approved", "reason": "ok"}, 9, now, nil)
	if err != nil || !changed {
		t.Fatalf("Transition = %v, %v; want true, nil", changed, err)
	}
	answered, err := repo.Get(ctx, decision.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if answered.Status != execution.HumanAnswered {
		t.Fatalf("status = %s, want answered", answered.Status)
	}
	if answered.Response["decision"] != "approved" || answered.Response["reason"] != "ok" {
		t.Fatalf("persisted response = %#v, want decision/reason", answered.Response)
	}
	if answered.ResolvedBy == nil || *answered.ResolvedBy != 9 || answered.ResolvedAt == nil {
		t.Fatalf("resolved_by/at = %v / %v", answered.ResolvedBy, answered.ResolvedAt)
	}

	overdue := []struct {
		id   int64
		kind execution.HumanKind
	}{
		{7002, execution.HumanBlockedRecovery},
		{7003, execution.HumanConflict},
		{7004, execution.HumanApproval},
	}
	for _, item := range overdue {
		request := execution.HumanRequest{
			ID: item.id, VersionID: 1001, Kind: item.kind,
			Payload: map[string]any{"reason": "overdue"}, Status: execution.HumanPending,
			ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
		}
		if err := repo.Create(ctx, request); err != nil {
			t.Fatalf("Create %s: %v", item.kind, err)
		}
	}
	// Login takeover expiry keeps working alongside the new kinds.
	takeover := execution.HumanRequest{
		ID: 7005, VersionID: 1001, Kind: execution.HumanLoginTakeover,
		Payload: map[string]any{"reason": "2FA"}, Status: execution.HumanPending,
		ExpiresAt: now.Add(-time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, takeover); err != nil {
		t.Fatalf("Create login_takeover: %v", err)
	}
	question := execution.HumanRequest{
		ID: 7006, VersionID: 1001, Kind: execution.HumanClarificationQuestion,
		Status: execution.HumanPending, ExpiresAt: now.Add(24 * time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, question); err != nil {
		t.Fatalf("Create clarification_question: %v", err)
	}

	expired, err := repo.Expire(ctx, now)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	t.Logf("DB evidence: human_request kinds seeded=%d (approval/blocked_recovery/conflict/login_takeover/clarification_question); expire sweep returned %d rows",
		6, len(expired))
	gotIDs := map[int64]execution.HumanRequestStatus{}
	for _, request := range expired {
		gotIDs[request.ID] = request.Status
	}
	for _, id := range []int64{7002, 7003, 7004, 7005} {
		if gotIDs[id] != execution.HumanExpired {
			t.Fatalf("expired[%d] = %q (all=%v), want expired", id, gotIDs[id], gotIDs)
		}
	}
	if len(expired) != 4 {
		t.Fatalf("expired rows = %d (%v), want 4", len(expired), gotIDs)
	}
	again, err := repo.Expire(ctx, now)
	if err != nil || len(again) != 0 {
		t.Fatalf("second Expire = %d, %v; want 0, nil", len(again), err)
	}

	// The version question todo read includes the decision kinds (and the
	// clarification question) but never the login takeover row.
	rows, _, err := repo.ListQuestions(ctx, 1001, "", 50, nil)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	kinds := map[int64]execution.HumanKind{}
	for _, request := range rows {
		kinds[request.ID] = request.Kind
	}
	if len(rows) != 5 {
		t.Fatalf("ListQuestions rows = %d (%v), want 5", len(rows), kinds)
	}
	if kinds[7005] != "" {
		t.Fatalf("ListQuestions leaked login_takeover: %v", kinds)
	}
	for _, id := range []int64{7001, 7002, 7003, 7004, 7006} {
		if kinds[id] == "" {
			t.Fatalf("ListQuestions missing human_request %d: %v", id, kinds)
		}
	}

	// The clarification question sweep only expires clarification_question rows.
	questionsExpired, err := repo.ExpireQuestions(ctx, now)
	if err != nil {
		t.Fatalf("ExpireQuestions: %v", err)
	}
	if len(questionsExpired) != 0 {
		t.Fatalf("ExpireQuestions = %d rows, want 0 (no overdue question, decision rows are out of scope)", len(questionsExpired))
	}
	stillExpired, err := repo.Get(ctx, 7002)
	if err != nil {
		t.Fatalf("Get expired blocked_recovery: %v", err)
	}
	if stillExpired.Status != execution.HumanExpired {
		t.Fatalf("blocked_recovery status = %s, want expired", stillExpired.Status)
	}
}
