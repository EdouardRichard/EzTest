//go:build integration

package executionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p6DeadWorkItemFake is the agent application port stub used by the execution
// reader tests: it records the limit and returns canned dead work items.
type p6DeadWorkItemFake struct {
	limit int
	rows  []agentapp.DeadWorkItemRow
	err   error
}

func (f *p6DeadWorkItemFake) ListDeadWorkItems(_ context.Context, limit int) ([]agentapp.DeadWorkItemRow, error) {
	f.limit = limit
	return f.rows, f.err
}

func p6ExecutionInt64Ptr(value int64) *int64 { return &value }

func p6SeedExecutionIntent(t *testing.T, pool *pgxpool.Pool, id, versionID, caseVersionID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO execution_intent (id, version_id, case_version_id, environment_fingerprint, dedupe_key)
		VALUES ($1, $2, $3, $4, $5)
	`, id, versionID, caseVersionID, strings.Repeat("a", 64), fmt.Sprintf("p6-intent-%d", id)); err != nil {
		t.Fatalf("seed execution intent: %v", err)
	}
}

func p6SeedExecutionWave(t *testing.T, pool *pgxpool.Pool, id, versionID int64, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO run_wave (id, version_id, status)
		VALUES ($1, $2, $3)
	`, id, versionID, status); err != nil {
		t.Fatalf("seed run wave: %v", err)
	}
}

func p6SeedExecutionCaseRun(t *testing.T, pool *pgxpool.Pool, id, waveID, intentID, caseVersionID int64, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status)
		VALUES ($1, $2, $3, $4, $5)
	`, id, waveID, intentID, caseVersionID, status); err != nil {
		t.Fatalf("seed case run: %v", err)
	}
}

func p6SeedExecutionEvidence(t *testing.T, pool *pgxpool.Pool, id, caseRunID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO evidence (id, case_run_id, kind, content_ref_id, sha256, size_bytes)
		VALUES ($1, $2, 'screenshot', 1, $3, 0)
	`, id, caseRunID, strings.Repeat("d", 64)); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
}

func p6SeedHumanRequest(t *testing.T, pool *pgxpool.Pool, id, versionID int64, kind, status, reason string, createdAt time.Time) {
	t.Helper()
	payload := map[string]string{}
	if reason != "" {
		payload["reason"] = reason
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal human request payload: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO human_request (id, version_id, kind, payload, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7)
	`, id, versionID, kind, string(raw), status, createdAt.Add(30*time.Minute), createdAt); err != nil {
		t.Fatalf("seed human request: %v", err)
	}
}

// p6SeedExecutionMetrics seeds two waves of terminal and non-terminal case runs
// plus evidence rows used by both rate readers.
func p6SeedExecutionMetrics(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	p6SeedExecutionWave(t, pool, 3001, 1001, "done")
	p6SeedExecutionWave(t, pool, 3002, 1002, "done")
	statuses := []struct {
		runID, intentID, caseVersionID int64
		status                         string
	}{
		{4001, 5001, 9001, "passed"},
		{4002, 5002, 9002, "failed"},
		{4003, 5003, 9003, "blocked"},
		{4004, 5004, 9004, "inconclusive"},
		{4005, 5005, 9005, "pending"},
		{4006, 5006, 9006, "skipped"},
	}
	for _, item := range statuses {
		p6SeedExecutionIntent(t, pool, item.intentID, 1001, item.caseVersionID)
		p6SeedExecutionCaseRun(t, pool, item.runID, 3001, item.intentID, item.caseVersionID, item.status)
	}
	p6SeedExecutionIntent(t, pool, 5011, 1002, 9011)
	p6SeedExecutionCaseRun(t, pool, 4011, 3002, 5011, 9011, "passed")
	p6SeedExecutionIntent(t, pool, 5012, 1002, 9012)
	p6SeedExecutionCaseRun(t, pool, 4012, 3002, 5012, 9012, "failed")

	// Evidence on a passed and a blocked run of 1001, on the failed run of 1002
	// and on a pending run that is outside the denominator.
	p6SeedExecutionEvidence(t, pool, 4101, 4001)
	p6SeedExecutionEvidence(t, pool, 4102, 4003)
	p6SeedExecutionEvidence(t, pool, 4103, 4012)
	p6SeedExecutionEvidence(t, pool, 4104, 4005)
}

func TestP6ExecutionQueryReaderCountExecutionPass(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6DeadWorkItemFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	p6SeedExecutionMetrics(t, pool)

	num, denom, err := reader.CountExecutionPass(ctx, nil)
	if err != nil {
		t.Fatalf("CountExecutionPass global: %v", err)
	}
	if num != 2 || denom != 6 {
		t.Fatalf("global counts = %d/%d, want 2/6", num, denom)
	}

	num, denom, err = reader.CountExecutionPass(ctx, p6ExecutionInt64Ptr(1001))
	if err != nil {
		t.Fatalf("CountExecutionPass version: %v", err)
	}
	if num != 1 || denom != 4 {
		t.Fatalf("version counts = %d/%d, want 1/4", num, denom)
	}

	num, denom, err = reader.CountExecutionPass(ctx, p6ExecutionInt64Ptr(1002))
	if err != nil {
		t.Fatalf("CountExecutionPass version 1002: %v", err)
	}
	if num != 1 || denom != 2 {
		t.Fatalf("version 1002 counts = %d/%d, want 1/2", num, denom)
	}

	num, denom, err = reader.CountExecutionPass(ctx, p6ExecutionInt64Ptr(1003))
	if err != nil {
		t.Fatalf("CountExecutionPass empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6ExecutionQueryReaderCountEvidenceCompleteness(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6DeadWorkItemFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	p6SeedExecutionMetrics(t, pool)

	num, denom, err := reader.CountEvidenceCompleteness(ctx, nil)
	if err != nil {
		t.Fatalf("CountEvidenceCompleteness global: %v", err)
	}
	if num != 3 || denom != 6 {
		t.Fatalf("global counts = %d/%d, want 3/6", num, denom)
	}

	num, denom, err = reader.CountEvidenceCompleteness(ctx, p6ExecutionInt64Ptr(1001))
	if err != nil {
		t.Fatalf("CountEvidenceCompleteness version: %v", err)
	}
	if num != 2 || denom != 4 {
		t.Fatalf("version counts = %d/%d, want 2/4", num, denom)
	}

	num, denom, err = reader.CountEvidenceCompleteness(ctx, p6ExecutionInt64Ptr(1002))
	if err != nil {
		t.Fatalf("CountEvidenceCompleteness version 1002: %v", err)
	}
	if num != 1 || denom != 2 {
		t.Fatalf("version 1002 counts = %d/%d, want 1/2", num, denom)
	}

	num, denom, err = reader.CountEvidenceCompleteness(ctx, p6ExecutionInt64Ptr(1003))
	if err != nil {
		t.Fatalf("CountEvidenceCompleteness empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6ExecutionQueryReaderListOpenHumanRequests(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6DeadWorkItemFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedHumanRequest(t, pool, 6001, 1001, "login_takeover", "pending", "验证码超时", base.Add(3*time.Hour))
	p6SeedHumanRequest(t, pool, 6002, 1001, "blocked_recovery", "pending", "", base.Add(2*time.Hour))
	p6SeedHumanRequest(t, pool, 6003, 1001, "clarification_question", "pending", "退款按 T+1 还是 T+3？", base.Add(5*time.Hour))
	p6SeedHumanRequest(t, pool, 6004, 1001, "conflict", "pending", "账号冲突", base.Add(4*time.Hour))
	p6SeedHumanRequest(t, pool, 6005, 1001, "other", "pending", "", base.Add(1*time.Hour))
	p6SeedHumanRequest(t, pool, 6006, 1001, "login_takeover", "answered", "已处理", base.Add(6*time.Hour))
	p6SeedHumanRequest(t, pool, 6007, 1001, "approval", "pending", "发布确认", base.Add(5*time.Hour))
	p6SeedHumanRequest(t, pool, 6011, 1002, "blocked_recovery", "pending", "恢复执行", base)

	rows, err := reader.ListOpenHumanRequests(ctx, true, 10)
	if err != nil {
		t.Fatalf("ListOpenHumanRequests admin: %v", err)
	}
	// Execution todos are login_takeover, approval, blocked_recovery and
	// conflict (W2-2): structured clarification_question rows have their own
	// version list, and the legacy other kind is not an execution todo.
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5 open execution todo rows: %#v", len(rows), rows)
	}
	if rows[0].ID != 6001 || rows[1].ID != 6007 || rows[2].ID != 6004 || rows[3].ID != 6002 || rows[4].ID != 6011 {
		t.Fatalf("ids = %d,%d,%d,%d,%d, want takeover first then created_at DESC",
			rows[0].ID, rows[1].ID, rows[2].ID, rows[3].ID, rows[4].ID)
	}
	for _, row := range rows {
		switch row.Kind {
		case "login_takeover", "approval", "blocked_recovery", "conflict":
		default:
			t.Fatalf("execution todo leaked kind %q: %#v", row.Kind, row)
		}
	}
	if rows[0].Kind != "login_takeover" || rows[0].Title != "验证码超时" || rows[0].VersionID != 1001 {
		t.Fatalf("row = %#v", rows[0])
	}
	if rows[0].ExpiresAt == nil || !rows[0].ExpiresAt.Equal(base.Add(3*time.Hour).Add(30*time.Minute)) {
		t.Fatalf("expires_at = %v", rows[0].ExpiresAt)
	}
	if rows[1].Kind != "approval" || rows[1].Title != "发布确认" {
		t.Fatalf("approval row = %#v, want the reason title", rows[1])
	}
	if rows[2].Kind != "conflict" || rows[2].Title != "账号冲突" {
		t.Fatalf("conflict row = %#v, want the reason title", rows[2])
	}
	if rows[3].Kind != "blocked_recovery" || rows[3].Title != "阻塞恢复" {
		t.Fatalf("blocked recovery row = %#v, want the kind fallback title", rows[3])
	}
	if rows[4].Title != "恢复执行" {
		t.Fatalf("blocked recovery reason title = %q", rows[4].Title)
	}
	if rows[0].VersionTitle != "" {
		t.Fatalf("version_title = %q, want empty (owned by the version context)", rows[0].VersionTitle)
	}

	memberRows, err := reader.ListOpenHumanRequests(ctx, false, 10)
	if err != nil {
		t.Fatalf("ListOpenHumanRequests member: %v", err)
	}
	if len(memberRows) != 4 {
		t.Fatalf("rows = %d, want 4 non-takeover execution todo rows", len(memberRows))
	}
	for _, row := range memberRows {
		switch row.Kind {
		case "approval", "blocked_recovery", "conflict":
		default:
			t.Fatalf("non-admin read leaked %#v", row)
		}
	}

	limited, err := reader.ListOpenHumanRequests(ctx, true, 1)
	if err != nil {
		t.Fatalf("ListOpenHumanRequests limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 6001 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6ExecutionQueryReaderListDeadWorkItems(t *testing.T) {
	pool := testutil.NewDB(t)
	created := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	dead := &p6DeadWorkItemFake{rows: []agentapp.DeadWorkItemRow{
		{ID: 11, VersionID: 1001, Title: "execute.case 重试耗尽", CreatedAt: created},
		{ID: 12, VersionID: 1002, Title: "design.plan 重试耗尽", CreatedAt: created.Add(time.Hour)},
	}}
	reader, err := NewQueryReader(pool, dead)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()

	rows, err := reader.ListDeadWorkItems(ctx, 7)
	if err != nil {
		t.Fatalf("ListDeadWorkItems: %v", err)
	}
	if dead.limit != 7 {
		t.Fatalf("port limit = %d, want 7", dead.limit)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	want := executionapp.DeadWorkItemRow{
		ID: 11, VersionID: 1001, Title: "execute.case 重试耗尽", CreatedAt: created,
	}
	if rows[0] != want {
		t.Fatalf("row = %#v, want %#v", rows[0], want)
	}
	if rows[0].VersionTitle != "" {
		t.Fatalf("version_title = %q, want empty (resolved by the consumer)", rows[0].VersionTitle)
	}

	failing, err := NewQueryReader(pool, &p6DeadWorkItemFake{err: errors.New("boom")})
	if err != nil {
		t.Fatalf("NewQueryReader failing: %v", err)
	}
	if _, err := failing.ListDeadWorkItems(ctx, 5); err == nil {
		t.Fatal("expected the agent port error to propagate")
	}
}

func TestP6ExecutionQueryReaderRequiresPoolAndAgentPort(t *testing.T) {
	pool := testutil.NewDB(t)
	if _, err := NewQueryReader(nil, &p6DeadWorkItemFake{}); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
	if _, err := NewQueryReader(pool, nil); err == nil {
		t.Fatal("expected an error for a missing dead work item port")
	}
}
