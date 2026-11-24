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
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// versionpgSearchSnippetMax mirrors the adapter's per-hit snippet bound.
const versionpgSearchSnippetMax = 400

// handoffFixtureJSON marshals a fixture payload, failing the test on error. The
// question/answer payloads are built from the execution domain wire structs so
// their field names (text/answers/answered_at/...) derive from the domain's own
// json tags rather than hand-written literals (H-B15 single-source hygiene).
func handoffFixtureJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture payload: %v", err)
	}
	return string(raw)
}

// handoffQuestionBatchJSON builds a human_request.question batch payload from
// domain question structs. The batch-level keys (questions/batch_id/expires_at)
// are the human-batch wire shape; expiresAt is omitted when empty so a fixture
// that seeds no batch expiry keeps that exact shape.
func handoffQuestionBatchJSON(t *testing.T, questions []execution.HumanQuestion, batchID, expiresAt string) string {
	t.Helper()
	payload := map[string]any{"questions": questions, "batch_id": batchID}
	if expiresAt != "" {
		payload["expires_at"] = expiresAt
	}
	return handoffFixtureJSON(t, payload)
}

func handoffIntegrationIDs(t *testing.T) platformid.Generator {
	t.Helper()
	generator, err := platformid.NewSnowflake(3)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

// handoffIntegrationService wires the handoff service exactly as the assembly
// root does (H-B1): the version-owned adapter plus the REAL resource application
// service over the same pool. The resources block is therefore read through
// resourceapp.HandoffResourceReader against PostgreSQL — the effective-scope gate
// exercised here is the production one (SUTView.IsApproved / EffectiveAssets and
// the environment code pool), not a test double.
func handoffIntegrationService(t *testing.T, pool *pgxpool.Pool) *versionapp.HandoffService {
	t.Helper()
	ids := handoffIntegrationIDs(t)
	resourceSvc := resourceapp.New(resourcepg.New(pool), ids, clock.System{}, nil, nil, nil)
	return versionapp.NewHandoffService(
		versionpg.NewHandoffStore(pool),
		versionpg.NewHandoffReader(pool),
		resourceSvc,
		ids,
		nil,
	)
}

// handoffSeedVersion inserts a version row in the requested stage.
func handoffSeedVersion(t *testing.T, pool *pgxpool.Pool, id int64, stage string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO environment (id, name, status) VALUES (990001, 'fixture env', $1) ON CONFLICT (id) DO NOTHING`, resource.StatusActive); err != nil {
		t.Fatalf("seed fixture environment: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, title, created_by, stage, environment_id)
		VALUES ($1, 'handoff integration version', 1, $2, 990001)
	`, id, stage); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

// handoffSeedResources seeds one SUT of the version's bound environment (the
// fixture environment 990001 created by handoffSeedVersion) with an approved
// page/API/active database/account/repository, plus a PROPOSED page and a
// DISABLED database that are out of the effective scope and must never enter the
// handoff (the trust-boundary invariant). The v0.2.4-B hierarchy inversion removed
// version_sut/sut_environment/scope_exclude/version_repository_override, so the
// SUT belongs to the environment directly and the assets are owned by sut_id.
func handoffSeedResources(t *testing.T, pool *pgxpool.Pool, versionID, sutID int64) {
	t.Helper()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed resources: %v", err)
		}
	}
	// The SUT belongs to the same environment the version binds (990001).
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES ($1, 990001, 'Order SUT', 'https://orders.example.test', 1)`, sutID)
	// The effective assets are seeded with the RESOURCE DOMAIN CONSTANTS, not
	// hand-written literals, so this test pins the reader's SQL against the same
	// single source (resource/application/scope.go) the reader now parameters from.
	// If the SQL were re-hardcoded to a stale literal, a domain rename would move
	// these seeds away from it and this test would fail.
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES (9001, $1, 'Refund page', 'https://orders.example.test/refund', '/refund', $2)`, sutID, resource.PageStatusApproved)
	// A proposed page is raw catalog state, NOT effective scope: it must be excluded.
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES (9002, $1, 'Proposed page', 'https://orders.example.test/proposed', '/proposed', $2)`, sutID, resource.PageStatusProposed)
	exec(`INSERT INTO sut_api (id, sut_id, method, path_template, name, status)
		VALUES (9101, $1, 'POST', '/api/refunds', 'Create refund', $2)`, sutID, resource.APIStatusApproved)
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, read_only, status)
		VALUES (9201, $1, 'orders', 'postgres://dbuser:db-pass@db/orders', 'dbuser', true, $2)`, sutID, resource.StatusActive)
	// A disabled database is out of scope; including it would regress the
	// trust-boundary fix (a disabled database's summary handed to the design pack).
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, read_only, status)
		VALUES (9202, $1, 'disabled-db', 'postgres://dbuser:disabled-pass@db/off', 'dbuser', true, $2)`, sutID, resource.StatusDisabled)
	exec(`INSERT INTO test_account (id, username, password, roles)
		VALUES (9301, 'qa-refund', 'account-password', '["admin"]'::jsonb)`)
	exec(`INSERT INTO version_account (id, version_id, account_id, enabled) VALUES (9401, $1, 9301, true)`, versionID)
	exec(`INSERT INTO code_repository (id, name, path, created_by) VALUES (9501, 'order-backend', '/repos/order', 1)`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled) VALUES (9601, $1, 9501, 'backend', true)`, sutID)
	// A PROPOSED SUT in the same environment is out of scope WHOLE (R-B4 IsApproved
	// gate): even though it owns an approved page and an active database, none of
	// them may reach the pack. Seeded with the domain constant, not a literal.
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by, status)
		VALUES ($1, 990001, 'Proposed SUT', 'https://proposed.example.test', 1, $2)`, sutID+1, resource.SUTStatusProposed)
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES (9003, $1, 'Proposed SUT page', 'https://proposed.example.test/p', '/p', $2)`, sutID+1, resource.PageStatusApproved)
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, read_only, status)
		VALUES (9203, $1, 'proposed-db', 'postgres://dbuser:proposed-pass@db/p', 'dbuser', true, $2)`, sutID+1, resource.StatusActive)
	// The proposed SUT also owns a repository association: R-B4 excludes it from
	// the code pool too (an unapproved SUT must not decide pool[0], the baseline).
	exec(`INSERT INTO code_repository (id, name, path, created_by) VALUES (9502, 'proposed-repo', '/repos/proposed', 1)`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled) VALUES (9602, $1, 9502, 'backend', true)`, sutID+1)
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
		VALUES (101, $1, $2, '退款到账时间按 T+1 验收', 0.95, 'FR-101')`, versionID, string(version.RequirementClarified))
	// v0.2.4-B (T8/R-B1): requirement_version carries the V1 natural-language
	// contract. Requirement 101 is seeded with a FULL contract (user_story,
	// structured Gherkin AC, sut_description, scope_out, account_roles_hint) so the
	// handoff projection can be proven faithful to the stored revision.
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, title, user_story, rules,
			acceptance_criteria, priority, sut_description, scope_out, account_roles_hint, content_hash)
		VALUES (201, 101, 2, '退款到账时效',
		        '{"as_a":"买家","i_want":"退款后按时到账","so_that":"确认退款完成"}'::jsonb,
		        '退款后约定时效到账。',
		        '[{"id":"AC-1","given":"买家已申请退款","when":"退款成功","then":"T+1 到账"}]'::jsonb,
		        'P1', '订单中心退款流程', '不覆盖跨境退款', '["admin"]'::jsonb, $1)`, strings.Repeat("a", 64))
	exec(`UPDATE requirement SET current_version_id = 201 WHERE id = 101`)
	exec(`INSERT INTO requirement_confirmation (id, requirement_version_id, scope, actor_id)
		VALUES (301, 201, $1, 1)`, version.ConfirmationScopeRequirement)
	exec(`INSERT INTO requirement (id, version_id, status, source_quote, external_id)
		VALUES (102, $1, $2, '', 'FR-102')`, versionID, string(version.RequirementDraft))
	// Requirement 102 stays extraction-shaped (a Then-only criterion, empty
	// user_story/sut_description): the handoff must carry it verbatim too, without
	// treating its AC id as durable.
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, title, rules,
			acceptance_criteria, priority, content_hash)
		VALUES (202, 102, 1, '退款失败重试', '退款失败允许重试。',
		        '[{"id":"AC-1","given":"","when":"","then":"重试不重复退款"}]'::jsonb, $2, $1)`, strings.Repeat("b", 64), version.DefaultPriority)
	exec(`UPDATE requirement SET current_version_id = 202 WHERE id = 102`)
	// A historical closed requirement must not enter the handoff (B §8.1:
	// draft/clarified only).
	exec(`INSERT INTO requirement (id, version_id, status, source_quote)
		VALUES (103, $1, $2, '')`, versionID, string(version.RequirementClosed))
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, title, rules,
			acceptance_criteria, priority, content_hash)
		VALUES (203, 103, 1, '已关闭需求', '', '[]'::jsonb, $2, $1)`, strings.Repeat("c", 64), version.DefaultPriority)
	exec(`UPDATE requirement SET current_version_id = 203 WHERE id = 103`)
}

// handoffSeedQuestions seeds one answered and one pending structured question.
func handoffSeedQuestions(t *testing.T, pool *pgxpool.Pool, versionID, conversationID int64) {
	t.Helper()
	answeredQuestion := handoffQuestionBatchJSON(t, []execution.HumanQuestion{{
		ID:            "q1",
		Text:          "退款到账时间按 T+1 还是 T+3 验收？",
		Options:       []execution.HumanQuestionOption{{ID: "a", Label: "T+1"}, {ID: "b", Label: "T+3"}},
		Multiple:      false,
		AllowFreeText: true,
		Required:      true,
		SourceRefs:    []string{"requirement:101"},
	}}, "qb-1", "2026-11-06T06:00:00Z")
	answeredResponse := handoffFixtureJSON(t, map[string]any{
		execution.HumanResponseAnswers: []execution.HumanQuestionAnswer{{
			QuestionID: "q1",
			Selected:   []string{"a"},
			Text:       "",
		}},
		execution.HumanResponseAnsweredAt: "2026-11-05T06:00:00Z",
	})
	// The source ref uses the requirement's external id ("requirement:FR-102",
	// the B §6.1 example format); the handoff/search reader must resolve it to
	// the internal requirement id 102.
	pendingQuestion := handoffQuestionBatchJSON(t, []execution.HumanQuestion{{
		ID:            "q1",
		Text:          "是否需要覆盖退款失败场景？",
		Options:       []execution.HumanQuestionOption{{ID: "yes", Label: "需要"}, {ID: "no", Label: "不需要"}},
		AllowFreeText: false,
		SourceRefs:    []string{"requirement:FR-102"},
	}}, "qb-2", "2026-11-06T06:00:00Z")
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO human_request (id, version_id, conversation_id, message_id, kind, payload,
			question, status, response, expires_at, resolved_at)
		VALUES (501, $1, $2, 801, $5, '{}'::jsonb, $3::jsonb,
			$6, $4::jsonb, '2026-11-06T06:00:00Z', '2026-11-05T06:00:00Z')
	`, versionID, conversationID, answeredQuestion, answeredResponse,
		string(execution.HumanClarificationQuestion), string(execution.HumanAnswered)); err != nil {
		t.Fatalf("seed answered question: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO human_request (id, version_id, conversation_id, message_id, kind, payload,
			question, status, response, expires_at)
		VALUES (502, $1, $2, 802, $4, '{}'::jsonb, $3::jsonb,
			$5, '{}'::jsonb, '2026-11-06T06:00:00Z')
	`, versionID, conversationID, pendingQuestion,
		string(execution.HumanClarificationQuestion), string(execution.HumanPending)); err != nil {
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
			($3+601, $1, $4, 'refund.sla', '{"summary":"退款 SLA 采用 T+1","requirement_id":101}'::jsonb, 0.9, 'clarification_host', 'requirement:101', $2),
			($3+602, $1, $5, 'refund.region', '{"summary":"仅中国大陆订单支持退款"}'::jsonb, 0.8, 'clarification_host', '', $2),
			($3+603, $1, $6, 'refund.failure', '{"summary":"退款失败场景尚未覆盖"}'::jsonb, NULL, 'explorer', 'requirement:102', $2)
	`, versionID, hash, baseID, version.FactKindDecision, version.FactKindConstraint, version.FactKindGap); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version_plan_item (id, version_id, phase, title, status, sort_order)
		VALUES
			($2+701, $1, 'clarifying', '确认需求', $3, 0),
			($2+702, $1, 'designing', '输出退款用例设计', $4, 1),
			($2+703, $1, 'designing', '评审退款用例', $5, 2)
	`, versionID, baseID, versionapp.ExtractionPlanStatusDone, version.PlanItemStatusInProgress, version.PlanItemStatusPending); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
}

func handoffSeedConversation(t *testing.T, pool *pgxpool.Pool, versionID, conversationID, messageID int64, text string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO conversation (id, version_id, kind, title) VALUES ($1, $2, $3, '澄清')
	`, conversationID, versionID, version.ConversationClarification); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	parts, _ := json.Marshal([]version.MessagePart{{Type: version.MessagePartText, Text: text}})
	refs := handoffFixtureJSON(t, []map[string]any{{"kind": version.MessageRefKindRequirement, "id": "101"}})
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO conversation_message (id, conversation_id, role, parts, refs)
		VALUES ($1, $2, $4, $3::jsonb, $5::jsonb)
	`, messageID, conversationID, parts, version.MessageRoleUser, refs); err != nil {
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
	handoffSeedResources(t, pool, versionID, 1101)
	handoffSeedRequirements(t, pool, versionID)
	handoffSeedQuestions(t, pool, versionID, 1301)
	handoffSeedFactsAndPlan(t, pool, versionID, 0)

	service := handoffIntegrationService(t, pool)
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
		if requirement.ID == 103 || requirement.Status == string(version.RequirementClosed) {
			t.Fatalf("closed requirement leaked into the handoff: %+v", requirement)
		}
	}
	// R-B1: the requirements block is a faithful V1 projection of
	// requirement_version. Requirement 101 was seeded with a full contract; the
	// handoff must carry the same user_story/AC/sut_description/optional fields,
	// verified both against the seed and against the stored revision itself.
	var clarified *versionapp.HandoffRequirement
	for index := range document.Requirements {
		if document.Requirements[index].ID == 101 {
			clarified = &document.Requirements[index]
		}
	}
	if clarified == nil {
		t.Fatalf("clarified requirement 101 missing from the handoff: %+v", document.Requirements)
	}
	if clarified.UserStory.AsA != "买家" || clarified.UserStory.IWant != "退款后按时到账" || clarified.UserStory.SoThat != "确认退款完成" {
		t.Fatalf("user_story projection = %+v", clarified.UserStory)
	}
	if clarified.SutDescription != "订单中心退款流程" || clarified.Rules != "退款后约定时效到账。" || clarified.ScopeOut != "不覆盖跨境退款" {
		t.Fatalf("sut_description/rules/scope_out projection = %+v", clarified)
	}
	if len(clarified.AccountRolesHint) != 1 || clarified.AccountRolesHint[0] != "admin" {
		t.Fatalf("account_roles_hint projection = %+v", clarified.AccountRolesHint)
	}
	if len(clarified.AcceptanceCriteria) != 1 {
		t.Fatalf("acceptance_criteria projection = %+v", clarified.AcceptanceCriteria)
	}
	if ac := clarified.AcceptanceCriteria[0]; ac.ID != "AC-1" || ac.Given != "买家已申请退款" || ac.When != "退款成功" || ac.Then != "T+1 到账" {
		t.Fatalf("acceptance criterion projection = %+v", ac)
	}
	var storyRaw, acRaw, rolesRaw []byte
	var sutDesc, scopeOut, rules string
	if err := pool.QueryRow(ctx, `
		SELECT user_story, acceptance_criteria, account_roles_hint, sut_description, scope_out, rules
		FROM requirement_version WHERE id = 201
	`).Scan(&storyRaw, &acRaw, &rolesRaw, &sutDesc, &scopeOut, &rules); err != nil {
		t.Fatalf("read requirement_version 201: %v", err)
	}
	var wantStory versionapp.HandoffUserStory
	_ = json.Unmarshal(storyRaw, &wantStory)
	if clarified.UserStory != wantStory || clarified.SutDescription != sutDesc || clarified.ScopeOut != scopeOut || clarified.Rules != rules {
		t.Fatalf("handoff requirement diverges from requirement_version: got %+v", clarified)
	}
	var wantAC []versionapp.HandoffAcceptanceCriterion
	_ = json.Unmarshal(acRaw, &wantAC)
	if len(clarified.AcceptanceCriteria) != len(wantAC) {
		t.Fatalf("acceptance_criteria count diverges: got %+v want %+v", clarified.AcceptanceCriteria, wantAC)
	}
	for index := range wantAC {
		if clarified.AcceptanceCriteria[index] != wantAC[index] {
			t.Fatalf("acceptance_criteria[%d] diverges: got %+v want %+v", index, clarified.AcceptanceCriteria[index], wantAC[index])
		}
	}
	if len(document.QA) != 2 {
		t.Fatalf("qa = %d, want 2", len(document.QA))
	}
	if document.QA[0].AnswerSelected[0] != "a" || document.QA[0].MessageID != 801 {
		t.Fatalf("answered qa = %+v", document.QA[0])
	}
	if document.QA[1].Status != string(execution.HumanPending) {
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
	if !openKinds["question"] || !openKinds[version.FactKindGap] || !openKinds["requirement"] {
		t.Fatalf("open block = %+v, want pending question, gap and draft requirement", document.Open)
	}
	if document.Plan.Stage != "designing" || document.Plan.Current == nil || document.Plan.Current.ID != 702 ||
		document.Plan.Next == nil || document.Plan.Next.ID != 703 {
		t.Fatalf("plan = %+v", document.Plan)
	}
	resources := document.Resources
	// The environment summary heads the block; there is no version_sut binding id.
	if resources.Environment.ID != 990001 || resources.Environment.Name != "fixture env" {
		t.Fatalf("environment summary = %+v", resources.Environment)
	}
	if len(resources.SUTs) != 1 || resources.SUTs[0].SUTID != 1101 {
		t.Fatalf("suts = %+v, want only the approved SUT 1101 (proposed SUT 1102 excluded whole)", resources.SUTs)
	}
	// R-B4 SUT-status gate: the proposed SUT (1102) is excluded whole, so its
	// approved page (9003) and active database (9203) must not appear anywhere.
	for _, s := range resources.SUTs {
		if s.SUTID == 1102 {
			t.Fatalf("proposed SUT 1102 leaked into the pack: %+v", s)
		}
		for _, p := range s.Pages {
			if p.ID == 9003 {
				t.Fatalf("proposed SUT's page 9003 leaked into the pack: %+v", p)
			}
		}
		for _, d := range s.Databases {
			if d.ID == 9203 {
				t.Fatalf("proposed SUT's database 9203 leaked into the pack: %+v", d)
			}
		}
	}
	sut := resources.SUTs[0]
	// Effective scope only: the approved page (9001) is in, the proposed page
	// (9002) is out; the active database (9201) is in, the disabled one (9202) out.
	if len(sut.Pages) != 1 || sut.Pages[0].ID != 9001 {
		t.Fatalf("sut pages = %+v, want only the approved page 9001", sut.Pages)
	}
	if len(sut.APIs) != 1 || sut.APIs[0].ID != 9101 {
		t.Fatalf("sut apis = %+v", sut.APIs)
	}
	if len(sut.Databases) != 1 || sut.Databases[0].ID != 9201 {
		t.Fatalf("sut databases = %+v, want only the active database 9201 (disabled 9202 excluded)", sut.Databases)
	}
	// Each database row carries its owning SUT id: a design db assertion takes
	// sut_id, not the database id, so the row must give the model a legitimate
	// sut_id source (mirrors T9's pack fix in agent context.go).
	if sut.Databases[0].SUTID != 1101 {
		t.Fatalf("database sut_id = %d, want the owning SUT 1101", sut.Databases[0].SUTID)
	}
	if len(resources.Accounts) != 1 || len(resources.Repositories) != 1 || resources.Repositories[0].Source != versionapp.HandoffRepositorySourceSUT {
		t.Fatalf("accounts/repositories = %+v / %+v", resources.Accounts, resources.Repositories)
	}
	// R-B4 code-pool gate: the proposed SUT's repository (9502) must be absent —
	// an unapproved SUT cannot contribute to the pool that decides the baseline.
	for _, repo := range resources.Repositories {
		if repo.ID == 9502 {
			t.Fatalf("proposed SUT's repository 9502 leaked into the code pool: %+v", repo)
		}
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	t.Logf("handoff document (revision %d): %s", document.Revision, raw)
	for _, secret := range []string{"account-password", "db-pass", "disabled-pass", "proposed-pass", "super-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("handoff JSON leaked %q: %s", secret, raw)
		}
	}
	// No SUT-binding id and no flat description survive anywhere in the document.
	if strings.Contains(string(raw), `"version_sut_id"`) || strings.Contains(string(raw), `"description"`) {
		t.Fatalf("handoff JSON carries a removed field: %s", raw)
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
	// Positive guard: the snippet must actually carry the seeded message text. If
	// the MessagePartText discriminator drifts, handoffMessageTextSnippet skips
	// every part and returns "" — this assertion fails loudly instead of the Host
	// silently losing all message context.
	if messageHits[0].Snippet == "" || !strings.Contains(messageHits[0].Snippet, "退款到账时间按 T+1 验收") {
		t.Fatalf("message snippet = %q, want non-empty and containing the seeded text", messageHits[0].Snippet)
	}
}
