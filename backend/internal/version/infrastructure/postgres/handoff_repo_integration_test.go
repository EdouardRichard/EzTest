//go:build integration

package versionpg_test

// Task 27 integration coverage: the handoff store/read model and the
// clarification.search SQL adapter against the real v0.22 schema. The unit
// tests pin the application/tool contracts; this test proves the SQL joins (and
// the version scoping) against PostgreSQL.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// versionpgSearchSnippetMax mirrors the adapter's per-hit snippet bound.
const versionpgSearchSnippetMax = 400

func handoffIntegrationIDs(t *testing.T) platformid.Generator {
	t.Helper()
	generator, err := platformid.NewSnowflake(3)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

// handoffSeedVersion inserts a version row in the requested stage.
func handoffSeedVersion(t *testing.T, pool *pgxpool.Pool, id int64, stage string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, title, created_by, stage)
		VALUES ($1, 'handoff integration version', 1, $2)
	`, id, stage); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

// handoffSeedResources seeds one SUT/environment binding with an excluded page,
// one available page/API/database/account/repository.
func handoffSeedResources(t *testing.T, pool *pgxpool.Pool, versionID, sutID, environmentID int64) {
	t.Helper()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed resources: %v", err)
		}
	}
	exec(`INSERT INTO sut (id, name, created_by) VALUES ($1, 'Order SUT', 1)`, sutID)
	exec(`INSERT INTO sut_environment (id, sut_id, name, base_url) VALUES ($1, $2, 'staging', 'https://orders.example.test')`, environmentID, sutID)
	exec(`INSERT INTO version_sut (id, version_id, sut_id, environment_id, status, scope_exclude)
		VALUES ($1, $2, $3, $4, 'active', '{"pages":[9002],"apis":[],"databases":[]}'::jsonb)`,
		sutID+1, versionID, sutID, environmentID)
	exec(`INSERT INTO sut_page (id, sut_id, environment_id, name, url, route_template, status)
		VALUES (9001, $1, $2, 'Refund page', 'https://orders.example.test/refund', '/refund', 'approved')`, sutID, environmentID)
	exec(`INSERT INTO sut_page (id, sut_id, environment_id, name, url, route_template, status)
		VALUES (9002, $1, $2, 'Excluded page', 'https://orders.example.test/secret', '/secret', 'approved')`, sutID, environmentID)
	exec(`INSERT INTO sut_api (id, sut_id, environment_id, method, path_template, name, status)
		VALUES (9101, $1, $2, 'POST', '/api/refunds', 'Create refund', 'approved')`, sutID, environmentID)
	exec(`INSERT INTO target_database (id, sut_id, environment_id, name, dsn, username, read_only, status)
		VALUES (9201, $1, $2, 'orders', 'postgres://dbuser:db-pass@db/orders', 'dbuser', true, 'active')`, sutID, environmentID)
	exec(`INSERT INTO test_account (id, username, password, roles)
		VALUES (9301, 'qa-refund', 'account-password', '["admin"]'::jsonb)`)
	exec(`INSERT INTO version_account (id, version_id, account_id, enabled) VALUES (9401, $1, 9301, true)`, versionID)
	exec(`INSERT INTO code_repository (id, name, path, created_by) VALUES (9501, 'order-backend', '/repos/order', 1)`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled) VALUES (9601, $1, 9501, 'backend', true)`, sutID)
	exec(`INSERT INTO version_repository_override (id, version_id, repo_id, included) VALUES (9701, $1, 9501, true)`, versionID)
}

// handoffSeedRequirements seeds one confirmed and one draft requirement.
func handoffSeedRequirements(t *testing.T, pool *pgxpool.Pool, versionID int64) {
	t.Helper()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed requirements: %v", err)
		}
	}
	exec(`INSERT INTO requirement (id, version_id, status, source_quote, confidence, external_id)
		VALUES (101, $1, 'clarified', '退款到账时间按 T+1 验收', 0.95, 'FR-101')`, versionID)
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, title, description,
			acceptance_criteria, priority, content_hash)
		VALUES (201, 101, 2, '退款到账时效', '退款后约定时效到账。', 'T+1 到账', 'P1', $1)`, strings.Repeat("a", 64))
	exec(`UPDATE requirement SET current_version_id = 201 WHERE id = 101`)
	exec(`INSERT INTO requirement_confirmation (id, requirement_version_id, scope, actor_id)
		VALUES (301, 201, 'requirement', 1)`)
	exec(`INSERT INTO requirement (id, version_id, status, source_quote, external_id)
		VALUES (102, $1, 'draft', '', 'FR-102')`, versionID)
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, title, description,
			acceptance_criteria, priority, content_hash)
		VALUES (202, 102, 1, '退款失败重试', '退款失败允许重试。', '重试不重复退款', 'P2', $1)`, strings.Repeat("b", 64))
	exec(`UPDATE requirement SET current_version_id = 202 WHERE id = 102`)
	// A historical closed requirement must not enter the handoff (B §8.1:
	// confirmed/draft only).
	exec(`INSERT INTO requirement (id, version_id, status, source_quote)
		VALUES (103, $1, 'closed', '')`, versionID)
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, title, description,
			acceptance_criteria, priority, content_hash)
		VALUES (203, 103, 1, '已关闭需求', '', '', 'P2', $1)`, strings.Repeat("c", 64))
	exec(`UPDATE requirement SET current_version_id = 203 WHERE id = 103`)
}

// handoffSeedQuestions seeds one answered and one pending structured question.
func handoffSeedQuestions(t *testing.T, pool *pgxpool.Pool, versionID, conversationID int64) {
	t.Helper()
	answeredQuestion := `{"questions":[{"id":"q1","text":"退款到账时间按 T+1 还是 T+3 验收？","options":[{"id":"a","label":"T+1"},{"id":"b","label":"T+3"}],"multiple":false,"allow_free_text":true,"required":true,"source_refs":["requirement:101"]}],"batch_id":"qb-1","expires_at":"2026-11-06T06:00:00Z"}`
	answeredResponse := `{"answers":[{"question_id":"q1","selected":["a"],"text":""}],"answered_at":"2026-11-05T06:00:00Z"}`
	// The source ref uses the requirement's external id ("requirement:FR-102",
	// the B §6.1 example format); the handoff/search reader must resolve it to
	// the internal requirement id 102.
	pendingQuestion := `{"questions":[{"id":"q1","text":"是否需要覆盖退款失败场景？","options":[{"id":"yes","label":"需要"},{"id":"no","label":"不需要"}],"allow_free_text":false,"source_refs":["requirement:FR-102"]}],"batch_id":"qb-2","expires_at":"2026-11-06T06:00:00Z"}`
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO human_request (id, version_id, conversation_id, message_id, kind, payload,
			question, status, response, expires_at, resolved_at)
		VALUES (501, $1, $2, 801, 'clarification_question', '{}'::jsonb, $3::jsonb,
			'answered', $4::jsonb, '2026-11-06T06:00:00Z', '2026-11-05T06:00:00Z')
	`, versionID, conversationID, answeredQuestion, answeredResponse); err != nil {
		t.Fatalf("seed answered question: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO human_request (id, version_id, conversation_id, message_id, kind, payload,
			question, status, response, expires_at)
		VALUES (502, $1, $2, 802, 'clarification_question', '{}'::jsonb, $3::jsonb,
			'pending', '{}'::jsonb, '2026-11-06T06:00:00Z')
	`, versionID, conversationID, pendingQuestion); err != nil {
		t.Fatalf("seed pending question: %v", err)
	}
}

// handoffSeedFactsAndPlan seeds decision/constraint/gap facts and a three-item
// plan. baseID offsets the fixed IDs so two versions can be seeded in one test.
func handoffSeedFactsAndPlan(t *testing.T, pool *pgxpool.Pool, versionID, baseID int64) {
	t.Helper()
	hash := strings.Repeat("c", 64)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version_fact (id, version_id, kind, key, payload, confidence, source, source_ref, content_hash)
		VALUES
			($3+601, $1, 'decision', 'refund.sla', '{"summary":"退款 SLA 采用 T+1","requirement_id":101}'::jsonb, 0.9, 'clarification_host', 'requirement:101', $2),
			($3+602, $1, 'constraint', 'refund.region', '{"summary":"仅中国大陆订单支持退款"}'::jsonb, 0.8, 'clarification_host', '', $2),
			($3+603, $1, 'gap', 'refund.failure', '{"summary":"退款失败场景尚未覆盖"}'::jsonb, NULL, 'explorer', 'requirement:102', $2)
	`, versionID, hash, baseID); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version_plan_item (id, version_id, phase, title, status, sort_order)
		VALUES
			($2+701, $1, 'clarifying', '确认需求', 'done', 0),
			($2+702, $1, 'designing', '输出退款用例设计', 'in_progress', 1),
			($2+703, $1, 'designing', '评审退款用例', 'pending', 2)
	`, versionID, baseID); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
}

func handoffSeedConversation(t *testing.T, pool *pgxpool.Pool, versionID, conversationID, messageID int64, text string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO conversation (id, version_id, kind, title) VALUES ($1, $2, 'clarification', '澄清')
	`, conversationID, versionID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	parts, _ := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO conversation_message (id, conversation_id, role, parts, refs)
		VALUES ($1, $2, 'user', $3::jsonb, '[{"kind":"requirement","id":"101"}]'::jsonb)
	`, messageID, conversationID, parts); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func countHandoffRows(t *testing.T, pool *pgxpool.Pool, versionID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM clarification_handoff WHERE version_id = $1`, versionID).Scan(&count); err != nil {
		t.Fatalf("count handoff rows: %v", err)
	}
	return count
}

// TestHandoffRepositoryGenerateAndRead pins the SQL half of R-B5-1/R-B5-2: the
// six blocks are assembled from the real tables, resource exclusions are
// honored, no secret leaks, identical regeneration is idempotent and a changed
// requirement writes the next revision.
func TestHandoffRepositoryGenerateAndRead(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID := int64(1001)
	handoffSeedVersion(t, pool, versionID, "designing")
	handoffSeedResources(t, pool, versionID, 1101, 1201)
	handoffSeedRequirements(t, pool, versionID)
	handoffSeedQuestions(t, pool, versionID, 1301)
	handoffSeedFactsAndPlan(t, pool, versionID, 0)

	service := versionapp.NewHandoffService(
		versionpg.NewHandoffStore(pool),
		versionpg.NewHandoffReader(pool),
		handoffIntegrationIDs(t),
		nil,
	)
	document, err := service.Generate(ctx, versionID)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if document.Revision != 1 || document.VersionID != versionID {
		t.Fatalf("handoff = version %d revision %d", document.VersionID, document.Revision)
	}
	if len(document.Requirements) != 2 {
		t.Fatalf("requirements = %d, want 2", len(document.Requirements))
	}
	for _, requirement := range document.Requirements {
		// B §8.1: confirmed/draft only. The seeded closed requirement 103 must
		// never enter the handoff.
		if requirement.ID == 103 || requirement.Status == "closed" {
			t.Fatalf("closed requirement leaked into the handoff: %+v", requirement)
		}
	}
	if len(document.QA) != 2 {
		t.Fatalf("qa = %d, want 2", len(document.QA))
	}
	if document.QA[0].AnswerSelected[0] != "a" || document.QA[0].MessageID != 801 {
		t.Fatalf("answered qa = %+v", document.QA[0])
	}
	if document.QA[1].Status != "pending" {
		t.Fatalf("pending qa = %+v", document.QA[1])
	}
	// The pending question references "requirement:FR-102"; the handoff must
	// resolve it through requirement.external_id to internal requirement 102.
	if document.QA[1].RequirementID != 102 || len(document.QA[1].RequirementRefs) != 1 ||
		document.QA[1].RequirementRefs[0] != 102 {
		t.Fatalf("external requirement ref was not resolved: %+v", document.QA[1])
	}
	if len(document.Decisions) != 2 {
		t.Fatalf("decisions = %d, want decision+constraint", len(document.Decisions))
	}
	openKinds := make(map[string]bool, len(document.Open))
	for _, item := range document.Open {
		openKinds[item.Kind] = true
	}
	if !openKinds["question"] || !openKinds["gap"] || !openKinds["requirement"] {
		t.Fatalf("open block = %+v, want pending question, gap and draft requirement", document.Open)
	}
	if document.Plan.Stage != "designing" || document.Plan.Current == nil || document.Plan.Current.ID != 702 ||
		document.Plan.Next == nil || document.Plan.Next.ID != 703 {
		t.Fatalf("plan = %+v", document.Plan)
	}
	resources := document.Resources
	if len(resources.SUTs) != 1 || len(resources.Pages) != 1 || resources.Pages[0].ID != 9001 ||
		len(resources.APIs) != 1 || len(resources.Accounts) != 1 || len(resources.Databases) != 1 ||
		len(resources.Repositories) != 1 || resources.Repositories[0].Source != "override" {
		t.Fatalf("resources = %+v", resources)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	t.Logf("handoff document (revision %d): %s", document.Revision, raw)
	for _, secret := range []string{"account-password", "db-pass", "super-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("handoff JSON leaked %q: %s", secret, raw)
		}
	}

	// Identical regeneration is a no-op.
	again, err := service.Generate(ctx, versionID)
	if err != nil {
		t.Fatalf("Generate again: %v", err)
	}
	if again.Revision != 1 || countHandoffRows(t, pool, versionID) != 1 {
		t.Fatalf("idempotent regeneration wrote a revision: %+v rows=%d", again, countHandoffRows(t, pool, versionID))
	}

	// A changed requirement writes revision 2; revision 1 stays readable.
	if _, err := pool.Exec(ctx, `UPDATE requirement_version SET title = '退款到账时效（修订）' WHERE id = 201`); err != nil {
		t.Fatalf("update requirement title: %v", err)
	}
	changed, err := service.Generate(ctx, versionID)
	if err != nil {
		t.Fatalf("Generate changed: %v", err)
	}
	if changed.Revision != 2 || changed.Requirements[0].Title != "退款到账时效（修订）" {
		t.Fatalf("changed handoff = %+v", changed)
	}
	if countHandoffRows(t, pool, versionID) != 2 {
		t.Fatalf("handoff rows = %d, want 2", countHandoffRows(t, pool, versionID))
	}
	first, err := service.Latest(ctx, versionID, 1)
	if err != nil || first.Revision != 1 || first.Requirements[0].Title != "退款到账时效" {
		t.Fatalf("Latest revision 1 = %+v err=%v", first, err)
	}
	latest, err := service.Latest(ctx, versionID, 0)
	if err != nil || latest.Revision != 2 {
		t.Fatalf("Latest = %+v err=%v", latest, err)
	}
}

// TestHandoffStoreRevisionConflict pins the unique (version_id, revision) guard
// the service relies on for its concurrency retry.
func TestHandoffStoreRevisionConflict(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID := int64(1002)
	handoffSeedVersion(t, pool, versionID, "designing")
	store := versionpg.NewHandoffStore(pool)
	ids := handoffIntegrationIDs(t)
	document := versionapp.Handoff{VersionID: versionID, Revision: 1}
	if _, err := store.Create(ctx, versionapp.HandoffRecord{ID: ids.Next(), Handoff: document}); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	_, err := store.Create(ctx, versionapp.HandoffRecord{ID: ids.Next(), Handoff: document})
	if !errors.Is(err, versionapp.ErrHandoffRevisionConflict) {
		t.Fatalf("duplicate revision err = %v, want ErrHandoffRevisionConflict", err)
	}
}

// TestClarificationSearchStoreScoped pins the SQL half of R-B5-3: results are
// version-scoped, carry sources, honor requirement_id and kinds, and return
// bounded text-only snippets. A same-keyword row seeded in another version is
// never returned.
func TestClarificationSearchStoreScoped(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID, otherVersionID := int64(2001), int64(2002)
	handoffSeedVersion(t, pool, versionID, "designing")
	handoffSeedVersion(t, pool, otherVersionID, "designing")
	handoffSeedRequirements(t, pool, versionID)
	handoffSeedQuestions(t, pool, versionID, 2301)
	handoffSeedFactsAndPlan(t, pool, versionID, 0)
	handoffSeedConversation(t, pool, versionID, 2301, 2401, "退款到账时间按 T+1 验收，退款失败可重试。")
	handoffSeedConversation(t, pool, otherVersionID, 2302, 2402, "其它版本的退款说明，绝不能返回。")
	handoffSeedFactsAndPlan(t, pool, otherVersionID, 10000)

	store := versionpg.NewClarificationSearchStore(pool)
	hits, err := store.SearchClarification(ctx, agenttools.ClarificationSearchQuery{
		VersionID: versionID,
		Query:     "退款",
		Kinds:     agenttools.ClarificationSearchKinds(),
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("SearchClarification: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits for the seeded query")
	}
	for _, hit := range hits {
		t.Logf("search hit: %+v", hit)
	}
	sawQA, sawDecision, sawMessage := false, false, false
	for _, hit := range hits {
		if hit.VersionID != versionID {
			t.Fatalf("cross-version hit: %+v", hit)
		}
		if hit.Source == "" {
			t.Fatalf("hit without source: %+v", hit)
		}
		if hit.MessageID == 2402 || strings.Contains(hit.Snippet, "其它版本") {
			t.Fatalf("cross-version message leaked: %+v", hit)
		}
		if runes := len([]rune(hit.Snippet)); runes > versionpgSearchSnippetMax {
			t.Fatalf("snippet runes = %d, want <= %d", runes, versionpgSearchSnippetMax)
		}
		switch hit.Kind {
		case agenttools.ClarificationSearchKindQA:
			sawQA = true
			if hit.QuestionID != 501 && hit.QuestionID != 502 {
				t.Fatalf("qa hit = %+v", hit)
			}
		case agenttools.ClarificationSearchKindDecision, agenttools.ClarificationSearchKindConstraint:
			sawDecision = true
		case agenttools.ClarificationSearchKindMessage:
			sawMessage = true
			if hit.MessageID != 2401 {
				t.Fatalf("message hit = %+v", hit)
			}
		default:
			t.Fatalf("unexpected kind %q", hit.Kind)
		}
	}
	if !sawQA || !sawDecision || !sawMessage {
		t.Fatalf("hits = %+v, want qa+decision+message", hits)
	}

	byRequirement, err := store.SearchClarification(ctx, agenttools.ClarificationSearchQuery{
		VersionID:     versionID,
		RequirementID: 101,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("SearchClarification by requirement: %v", err)
	}
	if len(byRequirement) == 0 {
		t.Fatal("no hits for requirement 101")
	}
	for _, hit := range byRequirement {
		if hit.VersionID != versionID || hit.RequirementID != 101 {
			t.Fatalf("requirement-scoped hit = %+v", hit)
		}
	}

	messageHits, err := store.SearchClarification(ctx, agenttools.ClarificationSearchQuery{
		VersionID: versionID,
		Query:     "退款",
		Kinds:     []string{agenttools.ClarificationSearchKindMessage},
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("SearchClarification messages: %v", err)
	}
	if len(messageHits) != 1 || messageHits[0].MessageID != 2401 ||
		messageHits[0].Source != "conversation_message:2401" {
		t.Fatalf("message hits = %+v", messageHits)
	}
	if strings.Contains(messageHits[0].Snippet, "其它版本") {
		t.Fatalf("cross-version snippet leaked: %q", messageHits[0].Snippet)
	}
}
