//go:build integration

package versionadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestBriefingReaderSQL seeds every briefing source table and reads the version
// briefing source through the production read-only SQL adapter. It pins the
// real-data mapping, the deterministic ordering and the bounded reads.
func TestBriefingReaderSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	briefingSeedSQL(t, pool, base)

	reader, err := NewBriefingReader(pool)
	if err != nil {
		t.Fatalf("NewBriefingReader: %v", err)
	}
	source, err := reader.Load(ctx, 9001)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if source.Version.Goal != "结算版本" || source.Version.Stage != "executing" || source.Version.Blocker != "" {
		t.Fatalf("version = %#v", source.Version)
	}

	// Confirmed requirements keep creation order and carry acceptance criteria;
	// draft requirements are the clarification gaps.
	if len(source.Requirements.Confirmed) != 3 {
		t.Fatalf("confirmed requirements = %#v", source.Requirements.Confirmed)
	}
	wantRequirements := []string{"优惠券可用", "包邮门槛", "未覆盖需求"}
	for index, want := range wantRequirements {
		if source.Requirements.Confirmed[index].Title != want {
			t.Fatalf("confirmed requirement %d = %#v, want %q", index, source.Requirements.Confirmed[index], want)
		}
		if source.Requirements.Confirmed[index].Acceptance == "" {
			t.Fatalf("confirmed requirement %d lost acceptance criteria", index)
		}
	}
	if !reflect.DeepEqual(source.Requirements.Gaps, []string{"草稿需求"}) {
		t.Fatalf("requirement gaps = %#v", source.Requirements.Gaps)
	}

	// Case counts are the current case entity statuses; requirements without a
	// case are the coverage gaps.
	if source.Cases.Draft != 1 || source.Cases.Confirmed != 1 || source.Cases.Stale != 1 {
		t.Fatalf("case counts = %#v", source.Cases)
	}
	if !reflect.DeepEqual(source.Cases.CoverageGaps, []string{"未覆盖需求"}) {
		t.Fatalf("coverage gaps = %#v", source.Cases.CoverageGaps)
	}

	// Open plan items only, dependency first, and the dependency reference is
	// rendered by title.
	if len(source.Plan) != 2 {
		t.Fatalf("plan = %#v", source.Plan)
	}
	if source.Plan[0].Title != "准备" || source.Plan[1].Title != "执行" {
		t.Fatalf("plan order = %#v", source.Plan)
	}
	if !reflect.DeepEqual(source.Plan[1].DependsOn, []string{"准备"}) {
		t.Fatalf("plan dependency = %#v", source.Plan[1].DependsOn)
	}
	// The optimistic-lock revision is read with the item: a value written
	// explicitly must surface, and a fixture row written without one keeps the
	// migration default 1. Without the read the Host would have to guess.
	if source.Plan[0].Revision != 1 || source.Plan[1].Revision != 4 {
		t.Fatalf("plan revisions = %d/%d, want 1/4", source.Plan[0].Revision, source.Plan[1].Revision)
	}

	// Facts: current rows only, ranked by confidence then recency (the same
	// order the briefing renderer uses for its top-K), with a deterministic
	// summary.
	if len(source.Findings) != 3 {
		t.Fatalf("findings = %#v", source.Findings)
	}
	if source.Findings[0].ID != 9601 || source.Findings[1].ID != 9602 || source.Findings[2].ID != 9604 {
		t.Fatalf("findings order = %#v", source.Findings)
	}
	if source.Findings[0].Summary != "结算页可访问" {
		t.Fatalf("fact summary = %q", source.Findings[0].Summary)
	}
	if source.Findings[0].Confidence == nil || *source.Findings[0].Confidence != 0.9 {
		t.Fatalf("fact confidence = %#v", source.Findings[0].Confidence)
	}
	if !source.Findings[2].CreatedAt.Equal(base.Add(3 * time.Hour)) {
		t.Fatalf("fact created_at = %v", source.Findings[2].CreatedAt)
	}

	// Execution summary of the latest wave.
	if source.Execution.WaveStatus != "paused" ||
		source.Execution.Passed != 1 || source.Execution.Failed != 1 ||
		source.Execution.Blocked != 1 || source.Execution.Skipped != 1 ||
		source.Execution.Blocker != "等待人工登录" {
		t.Fatalf("execution = %#v", source.Execution)
	}

	// Approved knowledge, top-K with binding and system-scope filtering. The
	// account-role bindings prove the trimmed role keys: the fixture stores
	// " buyer " / " auditor ", and 11005/11010 must still match.
	wantKnowledge := []int64{11001, 11002, 11005, 11010, 11008, 11009}
	if len(source.Knowledge) != len(wantKnowledge) {
		t.Fatalf("knowledge = %#v, want IDs %v", source.Knowledge, wantKnowledge)
	}
	for index, wantID := range wantKnowledge {
		if source.Knowledge[index].ID != wantID {
			t.Fatalf("knowledge[%d] = %#v, want ID %d", index, source.Knowledge[index], wantID)
		}
		if source.Knowledge[index].Title == "" || source.Knowledge[index].Summary == "" {
			t.Fatalf("knowledge[%d] lost title/summary: %#v", index, source.Knowledge[index])
		}
	}

	// Open human requests split into questions (conflict/other) and human
	// requests (login takeover).
	if !reflect.DeepEqual(source.OpenQuestions.Questions, []string{"账号归属冲突"}) {
		t.Fatalf("questions = %#v", source.OpenQuestions.Questions)
	}
	if !reflect.DeepEqual(source.OpenQuestions.HumanRequests, []string{"需要短信验证码"}) {
		t.Fatalf("human requests = %#v", source.OpenQuestions.HumanRequests)
	}

	// The briefing must never carry the credentials stored in sibling tables.
	serialized := fmt.Sprintf("%+v", source)
	for _, secret := range []string{"db-password", "hunter2", "postgres://user:secret@db/test"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("briefing source leaked secret %q", secret)
		}
	}

	// Equal data renders equal bytes: a second read is deep-equal.
	again, err := reader.Load(ctx, 9001)
	if err != nil {
		t.Fatalf("Load (second): %v", err)
	}
	if !reflect.DeepEqual(source, again) {
		t.Fatalf("briefing source is not deterministic:\n%#v\n%#v", source, again)
	}

	// A missing version is reported with the version not-found error.
	if _, err := reader.Load(ctx, 999999); !errors.Is(err, versiondomain.ErrNotFound) {
		t.Fatalf("missing version error = %v, want %v", err, versiondomain.ErrNotFound)
	}
}

// TestLoadBriefingPlanSQLRevision pins the R-A8-2 optimistic-lock token at the
// production plan read: loadBriefingPlan must SELECT the stored revision and
// scan it into the snapshot, because the Host consumes it to guard plan.update
// instead of guessing it and absorbing a conflict. The function is called
// directly so the test stays focused on the plan read (Task 29 updated the
// sibling knowledge read to the v0.22 version_sut.scope_exclude shape). A row
// written without a revision keeps the migration default 1.
func TestLoadBriefingPlanSQLRevision(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	exec(`INSERT INTO version (id, title, status, created_by, stage)
		VALUES (9006, '计划版本', 'running', 1, 'executing')`)
	exec(`INSERT INTO version_plan_item (id, version_id, phase, title, status, sort_order, revision)
		VALUES (9511, 9006, 'execute', '准备', 'pending', 0, 4)`)
	exec(`INSERT INTO version_plan_item (id, version_id, phase, title, status, sort_order)
		VALUES (9512, 9006, 'execute', '执行', 'pending', 1)`)
	exec(`INSERT INTO version_plan_item (id, version_id, phase, title, status, sort_order, revision)
		VALUES (9513, 9006, 'execute', '已完成', 'done', 2, 7)`)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var source agentcontext.Source
	if err := loadBriefingPlan(ctx, tx, 9006, &source); err != nil {
		t.Fatalf("loadBriefingPlan: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit read transaction: %v", err)
	}
	t.Logf("briefing plan snapshot: %+v", source.Plan)

	if len(source.Plan) != 2 {
		t.Fatalf("plan = %#v, want the two open items", source.Plan)
	}
	if source.Plan[0].ID != 9511 || source.Plan[0].Revision != 4 {
		t.Fatalf("plan[0] = %#v, want id 9511 revision 4", source.Plan[0])
	}
	if source.Plan[1].ID != 9512 || source.Plan[1].Revision != 1 {
		t.Fatalf("plan[1] = %#v, want id 9512 revision 1 (migration default)", source.Plan[1])
	}
}

// TestBriefingReaderSQLEmptyAndLimits pins the two remaining review-focus
// edges against PostgreSQL: an empty version produces zero/empty sections
// without an error, and a large version is bounded by the reader limits.
func TestBriefingReaderSQLEmptyAndLimits(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	hash := func(value int) string { return fmt.Sprintf("%064x", value) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	reader, err := NewBriefingReader(pool)
	if err != nil {
		t.Fatalf("NewBriefingReader: %v", err)
	}

	exec(`INSERT INTO version (id, title, status, created_by, stage) VALUES (9002, '空版本', 'running', 1, 'created')`)
	empty, err := reader.Load(ctx, 9002)
	if err != nil {
		t.Fatalf("Load(empty): %v", err)
	}
	if empty.Version.Goal != "空版本" || empty.Version.Stage != "created" {
		t.Fatalf("empty version snapshot = %#v", empty.Version)
	}
	if empty.Requirements.Confirmed == nil || empty.Requirements.Gaps == nil ||
		empty.Cases.CoverageGaps == nil || empty.Plan == nil || empty.Findings == nil ||
		empty.Knowledge == nil || empty.OpenQuestions.Questions == nil ||
		empty.OpenQuestions.HumanRequests == nil {
		t.Fatalf("empty version has nil sections: %#v", empty)
	}
	if len(empty.Requirements.Confirmed) != 0 || len(empty.Requirements.Gaps) != 0 ||
		len(empty.Cases.CoverageGaps) != 0 || len(empty.Plan) != 0 ||
		len(empty.Findings) != 0 || len(empty.Knowledge) != 0 ||
		len(empty.OpenQuestions.Questions) != 0 || len(empty.OpenQuestions.HumanRequests) != 0 {
		t.Fatalf("empty version sections are not empty: %#v", empty)
	}

	exec(`INSERT INTO version (id, title, status, created_by, stage) VALUES (9003, '大版本', 'running', 1, 'executing')`)
	for index := 0; index < 25; index++ {
		requirementID := int64(9301 + index)
		revisionID := int64(9401 + index)
		title := fmt.Sprintf("需求-%02d", index)
		exec(`INSERT INTO requirement (id, version_id, current_version_id, status)
			VALUES ($1, 9003, $2, 'clarified')`, requirementID, revisionID)
		exec(`INSERT INTO requirement_version
			(id, requirement_id, revision, title, acceptance_criteria, content_hash)
			VALUES ($1, $2, 1, $3, $4, $5)`,
			revisionID, requirementID, title, "验收："+title, hash(index+100))
		exec(`INSERT INTO version_plan_item (id, version_id, phase, title, status, sort_order)
			VALUES ($1, 9003, 'execute', $2, 'pending', $3)`,
			int64(9601+index), fmt.Sprintf("计划-%02d", index), index)
		// The oldest fact carries the highest confidence: the candidate read must
		// rank by confidence before recency so it survives the limit.
		var confidence *float64
		if index == 0 {
			high := 0.99
			confidence = &high
		}
		exec(`INSERT INTO version_fact
			(id, version_id, kind, key, payload, confidence, source, content_hash, created_at)
			VALUES ($1, 9003, 'page', $2, '{}'::jsonb, $3, 'tool', $4, $5)`,
			int64(9501+index), fmt.Sprintf("key-%02d", index), confidence, hash(index+200),
			base.Add(time.Duration(index)*time.Minute))
		exec(`INSERT INTO knowledge_item
			(id, scope, title, content, status, confidence)
			VALUES ($1, 'global', $2, '内容', 'approved', 0.5)`,
			int64(9701+index), fmt.Sprintf("知识-%02d", index))
	}

	large, err := reader.Load(ctx, 9003)
	if err != nil {
		t.Fatalf("Load(large): %v", err)
	}
	for name, length := range map[string]int{
		"requirements.confirmed": len(large.Requirements.Confirmed),
		"plan":                   len(large.Plan),
		"findings":               len(large.Findings),
		"knowledge":              len(large.Knowledge),
	} {
		if length != 20 {
			t.Fatalf("large version %s length = %d, want the reader limit 20", name, length)
		}
	}
	// The highest-confidence fact wins the candidate cut even though it is the
	// oldest; among the equal-confidence rows the newest comes first.
	if large.Requirements.Confirmed[0].Title != "需求-00" ||
		large.Findings[0].ID != 9501 || large.Findings[1].ID != 9525 {
		t.Fatalf("large version order changed: %#v %#v", large.Requirements.Confirmed[0], large.Findings[:2])
	}
}

// TestBriefingReaderSQLExecutionBlocker pins the latest-wave blocker mapping.
// postgres.WaveRepository.Transition keeps pause_reason when a transition passes
// an empty reason, so a resumed or finished wave still carries the previous
// reason in the row. Only a paused wave may surface it, and the internal
// waiting_human token must never reach the prompt: the user-visible reason is
// the open human request.
func TestBriefingReaderSQLExecutionBlocker(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	reader, err := NewBriefingReader(pool)
	if err != nil {
		t.Fatalf("NewBriefingReader: %v", err)
	}
	load := func() agentcontext.Source {
		t.Helper()
		source, err := reader.Load(ctx, 9004)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return source
	}
	assertBlocker := func(wantStatus, wantBlocker string) agentcontext.Source {
		t.Helper()
		source := load()
		if source.Execution.WaveStatus != wantStatus {
			t.Fatalf("wave status = %q, want %q", source.Execution.WaveStatus, wantStatus)
		}
		if source.Execution.Blocker != wantBlocker {
			t.Fatalf("%s wave blocker = %q, want %q", wantStatus, source.Execution.Blocker, wantBlocker)
		}
		return source
	}

	exec(`INSERT INTO version (id, title, status, created_by, stage)
		VALUES (9004, '阻塞版本', 'running', 1, 'executing')`)

	// A login-takeover pause was resumed: the internal token is still on the
	// running wave, but the briefing must not show a stale blocker.
	exec(`INSERT INTO run_wave (id, version_id, status, pause_reason)
		VALUES (9911, 9004, 'running', 'waiting_human')`)
	assertBlocker("running", "")

	// A wave that finished after a pause also keeps the last reason.
	exec(`UPDATE run_wave SET status = 'done' WHERE id = 9911`)
	assertBlocker("done", "")

	// A paused wave with a user-entered display reason keeps it.
	exec(`INSERT INTO run_wave (id, version_id, status, pause_reason)
		VALUES (9912, 9004, 'paused', '等待人工登录')`)
	assertBlocker("paused", "等待人工登录")

	// The internal waiting_human token is not user-visible text; the open human
	// request carries the user-visible reason instead.
	exec(`UPDATE run_wave SET pause_reason = 'waiting_human' WHERE id = 9912`)
	if source := assertBlocker("paused", ""); len(source.OpenQuestions.HumanRequests) != 0 {
		t.Fatalf("human requests before seeding = %#v", source.OpenQuestions.HumanRequests)
	}
	exec(`INSERT INTO human_request (id, version_id, kind, payload, status, expires_at)
		VALUES (10011, 9004, 'login_takeover', '{"reason":"需要短信验证码"}'::jsonb, 'pending', now())`)
	source := assertBlocker("paused", "")
	if !reflect.DeepEqual(source.OpenQuestions.HumanRequests, []string{"需要短信验证码"}) {
		t.Fatalf("human requests = %#v", source.OpenQuestions.HumanRequests)
	}

	// Resuming a user pause keeps the display reason on the row, but a running
	// wave carries no blocker.
	exec(`UPDATE run_wave SET status = 'running', pause_reason = '等待人工登录' WHERE id = 9912`)
	assertBlocker("running", "")

	// Cancelling a paused wave keeps it too; a cancelled wave carries none.
	exec(`UPDATE run_wave SET status = 'cancelled' WHERE id = 9912`)
	assertBlocker("cancelled", "")

	// Open-question labels are capped, because the open-questions section is
	// never truncated by the renderer.
	longReason := strings.Repeat("验", 300)
	exec(`INSERT INTO human_request (id, version_id, kind, payload, status, expires_at)
		VALUES (10012, 9004, 'other', jsonb_build_object('reason', $1::text), 'pending', now())`, longReason)
	source = assertBlocker("cancelled", "")
	if len(source.OpenQuestions.Questions) != 1 {
		t.Fatalf("questions = %#v", source.OpenQuestions.Questions)
	}
	label := source.OpenQuestions.Questions[0]
	if runes := []rune(label); len(runes) != 241 || !strings.HasSuffix(label, "…") {
		t.Fatalf("question label uses %d runes, want the 240-rune cap plus an ellipsis: %q", len(runes), label)
	}
}

// TestBriefingReaderSQLSourceStale pins the R87 source-staleness policy at the
// production read boundary end to end: the SQL source selects the knowledge
// status and computes whether the item's source version still exists, and the
// briefing builder therefore suppresses a source-gone approved item instead of
// relying on the SQL approved filter alone. An item without a source version and
// an item whose source version still exists stay injectable.
func TestBriefingReaderSQLSourceStale(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	reader, err := NewBriefingReader(pool)
	if err != nil {
		t.Fatalf("NewBriefingReader: %v", err)
	}

	exec(`INSERT INTO version (id, title, status, created_by, stage)
		VALUES (9005, '源失效版本', 'running', 1, 'executing')`)
	exec(`INSERT INTO knowledge_item (id, scope, title, content, status, confidence, source_version_id)
		VALUES (12001, 'global', '源版本存在知识', '应注入简报', 'approved', 0.90, 9005)`)
	exec(`INSERT INTO knowledge_item (id, scope, title, content, status, confidence, source_version_id)
		VALUES (12002, 'global', '源版本已删除知识', '不应注入简报', 'approved', 0.95, 999999)`)
	exec(`INSERT INTO knowledge_item (id, scope, title, content, status, confidence)
		VALUES (12003, 'global', '无源版本知识', '应注入简报', 'approved', 0.80)`)

	// End to end: the production reader feeds the briefing builder, and the
	// source-gone item must never reach the rendered prompt.
	briefing, err := agentcontext.NewBriefingBuilder(reader).BuildBriefing(ctx, 9005)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	rendered := briefing.Render()
	if strings.Contains(rendered, "源版本已删除知识") {
		t.Fatalf("briefing rendered a source-gone approved item:\n%s", rendered)
	}
	for _, want := range []string{"源版本存在知识", "无源版本知识"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("briefing is missing injectable knowledge %q:\n%s", want, rendered)
		}
	}

	// The read model must carry the state the builder filters on: the status is
	// populated for every approved row and SourceStale marks exactly the item
	// whose source version is gone (a nil source version is not stale).
	source, err := reader.Load(ctx, 9005)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(source.Knowledge) != 3 {
		t.Fatalf("knowledge = %#v, want all three approved candidates", source.Knowledge)
	}
	wantStale := map[int64]bool{12001: false, 12002: true, 12003: false}
	for _, item := range source.Knowledge {
		if item.Status != "approved" {
			t.Fatalf("knowledge %d status = %q, want approved", item.ID, item.Status)
		}
		want, ok := wantStale[item.ID]
		if !ok {
			t.Fatalf("unexpected knowledge item %d: %#v", item.ID, item)
		}
		if item.SourceStale != want {
			t.Fatalf("knowledge %d source_stale = %v, want %v", item.ID, item.SourceStale, want)
		}
	}
}

// briefingSeedSQL seeds every table the briefing reads plus unrelated credential
// rows that must never be read.
func briefingSeedSQL(t *testing.T, pool *pgxpool.Pool, base time.Time) {
	t.Helper()
	hash := func(value int) string { return fmt.Sprintf("%064x", value) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}

	exec(`INSERT INTO version (id, title, status, created_by, stage, blocker)
		VALUES ($1, $2, 'running', 1, 'executing', '')`, 9001, "结算版本")

	exec(`INSERT INTO sut (id, name, created_by, family) VALUES ($1, $2, 1, 'payments')`, 7001, "Shop")
	exec(`INSERT INTO sut_environment (id, sut_id, name, base_url)
		VALUES (7101, 7001, 'staging', 'https://shop.test')`)
	exec(`INSERT INTO sut_page (id, sut_id, environment_id, name, url, route_template)
		VALUES (7201, 7001, 7101, 'Checkout', 'https://shop.test/checkout', '/checkout')`)
	exec(`INSERT INTO sut_api (id, sut_id, environment_id, method, path_template, name)
		VALUES (7301, 7001, 7101, 'POST', '/api/checkout', 'Checkout API')`)
	// v0.22 binding shape: version_sut carries the environment exclusions
	// (empty = whole environment) and accounts are the enabled rows of the
	// global test-account pool.
	exec(`INSERT INTO version_sut (id, version_id, sut_id, environment_id)
		VALUES (7401, 9001, 7001, 7101)`)

	// Credentials in sibling resource tables must never reach the briefing.
	exec(`INSERT INTO target_database (id, sut_id, environment_id, name, dsn, username, password)
		VALUES (7801, 7001, 7101, 'main', 'postgres://user:secret@db/test', 'user', 'db-password')`)
	exec(`INSERT INTO test_account (id, username, password, roles)
		VALUES (7601, 'buyer', 'hunter2', '[" buyer ", " auditor ", ""]'::jsonb)`)
	exec(`INSERT INTO version_account (id, version_id, account_id, enabled)
		VALUES (7501, 9001, 7601, true)`)

	requirements := []struct {
		id, currentID int64
		status        string
		title         string
	}{
		{9101, 9201, "clarified", "优惠券可用"},
		{9102, 9202, "clarified", "包邮门槛"},
		{9103, 9203, "clarified", "未覆盖需求"},
		{9104, 9204, "draft", "草稿需求"},
	}
	for index, requirement := range requirements {
		exec(`INSERT INTO requirement (id, version_id, current_version_id, status)
			VALUES ($1, 9001, $2, $3)`, requirement.id, requirement.currentID, requirement.status)
		exec(`INSERT INTO requirement_version
			(id, requirement_id, revision, title, acceptance_criteria, content_hash)
			VALUES ($1, $2, 1, $3, $4, $5)`,
			requirement.currentID, requirement.id, requirement.title,
			"验收："+requirement.title, hash(index+1))
	}

	// Two clarified requirements have cases; 未覆盖需求 has none (coverage gap).
	exec(`INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES (9301, 9001, 9101, 9201, '优惠券场景')`)
	exec(`INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES (9302, 9001, 9102, 9202, '包邮场景')`)
	exec(`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (9401, 9301, 94001, 'confirmed')`)
	exec(`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (9402, 9301, 94002, 'draft')`)
	exec(`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (9403, 9302, 94003, 'stale')`)
	cases := []struct {
		id         int64
		caseID     int64
		status     string
		title      string
		contentIdx int
	}{
		{94001, 9401, "confirmed", "优惠券用例", 50},
		{94002, 9402, "draft", "草稿用例", 51},
		{94003, 9403, "stale", "失效用例", 52},
	}
	for _, item := range cases {
		exec(`INSERT INTO case_version (id, case_id, revision, title, content_hash, status)
			VALUES ($1, $2, 1, $3, $4, $5)`,
			item.id, item.caseID, item.title, hash(item.contentIdx), item.status)
	}

	// The dependent plan item sorts before its dependency; the reader must
	// restore dependency-first order. A done item must be omitted. 9501 carries
	// an explicit revision while 9502 relies on the migration default (1), so
	// the read mapping is pinned for both shapes.
	exec(`INSERT INTO version_plan_item (id, version_id, phase, title, status, depends_on, sort_order, revision)
		VALUES (9501, 9001, 'execute', '执行', 'pending', '{9502}'::bigint[], 0, 4)`)
	exec(`INSERT INTO version_plan_item (id, version_id, phase, title, status, depends_on, sort_order)
		VALUES (9502, 9001, 'execute', '准备', 'pending', '{}'::bigint[], 1)`)
	exec(`INSERT INTO version_plan_item (id, version_id, phase, title, status, depends_on, sort_order)
		VALUES (9503, 9001, 'execute', '完成项', 'done', '{}'::bigint[], 2)`)

	facts := []struct {
		id         int64
		kind, key  string
		payload    string
		confidence *float64
		createdAt  time.Time
		superseded *int64
	}{
		{9601, "page", "/checkout", `{"summary":"结算页可访问"}`, ptrFloat(0.9), base.Add(2 * time.Hour), nil},
		{9602, "api", "POST /api/checkout", `{"status":200}`, ptrFloat(0.5), base.Add(time.Hour), nil},
		{9603, "page", "/legacy", `{"summary":"旧事实"}`, nil, base.Add(30 * time.Minute), int64Ptr(9601)},
		{9604, "session", "buyer", `{}`, nil, base.Add(3 * time.Hour), nil},
	}
	for index, fact := range facts {
		exec(`INSERT INTO version_fact
			(id, version_id, kind, key, payload, confidence, source, content_hash, created_at, superseded_by)
			VALUES ($1, 9001, $2, $3, $4::jsonb, $5, 'tool', $6, $7, $8)`,
			fact.id, fact.kind, fact.key, fact.payload, fact.confidence, hash(index+20), fact.createdAt, fact.superseded)
	}

	// Two waves: the newest one is the briefing's execution source.
	exec(`INSERT INTO run_wave (id, version_id, status, created_at) VALUES (9701, 9001, 'done', $1)`, base)
	exec(`INSERT INTO run_wave (id, version_id, status, pause_reason, created_at)
		VALUES (9702, 9001, 'paused', '等待人工登录', $1)`, base.Add(4*time.Hour))
	statuses := []string{"passed", "failed", "blocked", "skipped", "inconclusive"}
	for index, status := range statuses {
		intentID := int64(9901 + index)
		exec(`INSERT INTO execution_intent
			(id, version_id, case_version_id, environment_fingerprint, status, dedupe_key)
			VALUES ($1, 9001, 9401, $2, 'done', $3)`, intentID, hash(40+index), fmt.Sprintf("briefing-%d", index))
		exec(`INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status)
			VALUES ($1, 9702, $2, 9401, $3)`, int64(9801+index), intentID, status)
	}

	exec(`INSERT INTO human_request (id, version_id, kind, payload, status, expires_at, created_at)
		VALUES (10001, 9001, 'login_takeover', '{"reason":"需要短信验证码"}'::jsonb, 'pending', $1, $2)`,
		base.Add(5*time.Hour), base.Add(3*time.Hour))
	exec(`INSERT INTO human_request (id, version_id, kind, payload, status, expires_at, created_at)
		VALUES (10002, 9001, 'conflict', '{"reason":"账号归属冲突"}'::jsonb, 'pending', $1, $2)`,
		base.Add(5*time.Hour), base.Add(4*time.Hour))
	exec(`INSERT INTO human_request (id, version_id, kind, payload, status, expires_at, created_at)
		VALUES (10003, 9001, 'other', '{"reason":"已处理"}'::jsonb, 'answered', $1, $2)`,
		base.Add(5*time.Hour), base.Add(5*time.Hour))

	knowledge := []struct {
		id         int64
		scope      string
		systemKey  *string
		title      string
		content    string
		status     string
		confidence *float64
		bindings   string
	}{
		{11001, "global", nil, "通用知识", "全局重试建议", "approved", ptrFloat(0.95), `{}`},
		{11002, "system", ptrString("payments"), "支付知识", "支付重试建议", "approved", ptrFloat(0.9), `{}`},
		{11003, "system", ptrString("other"), "无关系统", "不应出现", "approved", ptrFloat(0.99), `{}`},
		{11004, "global", nil, "草稿知识", "不应出现", "draft", ptrFloat(0.99), `{}`},
		{11005, "global", nil, "页面知识", "选中页面可用", "approved", ptrFloat(0.8),
			`{"system_id":7001,"environment_id":7101,"page_key":"/checkout","account_role":"buyer"}`},
		{11006, "global", nil, "外部页面知识", "不应出现", "approved", ptrFloat(0.85),
			`{"system_id":7001,"environment_id":7101,"page_key":"/other"}`},
		{11007, "global", nil, "外部系统知识", "不应出现", "approved", ptrFloat(0.86),
			`{"system_id":7999}`},
		{11008, "global", nil, "API知识", "选中 API 可用", "approved", ptrFloat(0.7),
			`{"api_key":"POST /api/checkout"}`},
		{11009, "global", nil, "无置信知识", "没有置信度", "approved", nil, `{}`},
		{11010, "global", nil, "账号角色知识", "绑定账号角色可用", "approved", ptrFloat(0.75),
			`{"account_role":"auditor"}`},
	}
	for _, item := range knowledge {
		exec(`INSERT INTO knowledge_item
			(id, scope, system_key, title, content, status, confidence, bindings, source_version_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, 9001)`,
			item.id, item.scope, item.systemKey, item.title, item.content, item.status,
			item.confidence, item.bindings)
	}
}

func ptrFloat(value float64) *float64 { return &value }

func ptrString(value string) *string { return &value }

func int64Ptr(value int64) *int64 { return &value }

// TestBriefingReaderLoadsClarificationHandoff covers the v0.22 R-B5-2 handoff
// injection (Task 36 fix round 1): the briefing source reads the latest
// clarification_handoff artifact revision and the Design Host briefing renders
// its revision and document.
func TestBriefingReaderLoadsClarificationHandoff(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	briefingSeedSQL(t, pool, base)
	document := `{"version_id":9001,"revision":3,"requirements":[{"id":501,"title":"登录需求"}],"open":[{"title":"短信通道"}]}`
	if _, err := pool.Exec(ctx, `
		INSERT INTO clarification_handoff (id, version_id, revision, content)
		VALUES (9901, 9001, 3, $1::jsonb)
	`, document); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}

	reader, err := NewBriefingReader(pool)
	if err != nil {
		t.Fatalf("NewBriefingReader: %v", err)
	}
	source, err := reader.Load(ctx, 9001)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if source.Handoff == nil || source.Handoff.Revision != 3 {
		t.Fatalf("handoff source = %#v", source.Handoff)
	}
	if !strings.Contains(source.Handoff.Document, "登录需求") {
		t.Fatalf("handoff document = %q", source.Handoff.Document)
	}

	briefing, err := agentcontext.NewBriefingBuilder(reader).BuildBriefing(ctx, 9001)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	rendered := briefing.Render()
	for _, want := range []string{"澄清交接包", "revision=3", "短信通道"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("design host briefing missing %q:\n%s", want, rendered)
		}
	}
}
