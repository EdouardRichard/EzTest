//go:build integration

package versionpg_test

// Fix round 1 integration coverage for Task 27. The prior tests proved the SQL
// joins; these tests pin the review findings against the real v0.22 schema:
//   - every pending question of a multi-question human_request batch becomes its
//     own open item (no identity collision on the request id);
//   - requirement source refs in the documented "requirement:FR-012" form are
//     resolved through requirement.external_id (handoff and search);
//   - the requirements block holds draft/clarified rows only (no closed rows);
//   - oversized requirements/facts keep the newest rows;
//   - a repository inherited from two SUTs gets a deterministic role;
//   - search reports each row's actual version and excludes other versions,
//     design-conversation messages and other-version questions/facts;
//   - a direct adapter caller's limit is clamped to the contract maximum.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// handoffSeedQuestionRow inserts one clarification human_request batch.
func handoffSeedQuestionRow(
	t *testing.T,
	pool *pgxpool.Pool,
	versionID, requestID, messageID, conversationID int64,
	questionJSON, status, responseJSON string,
	resolvedAt any,
) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO human_request (id, version_id, conversation_id, message_id, kind, payload,
			question, status, response, expires_at, resolved_at)
		VALUES ($1, $2, $3, $4, 'clarification_question', '{}'::jsonb, $5::jsonb, $6, $7::jsonb,
			'2026-11-06T06:00:00Z', $8)
	`, requestID, versionID, conversationID, messageID, questionJSON, status, responseJSON, resolvedAt); err != nil {
		t.Fatalf("seed question row %d: %v", requestID, err)
	}
}

// handoffSeedConversationKind seeds a conversation of an explicit kind plus one
// text message linked to requirement 101.
func handoffSeedConversationKind(
	t *testing.T,
	pool *pgxpool.Pool,
	versionID, conversationID, messageID int64,
	kind, text string,
) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO conversation (id, version_id, kind, title) VALUES ($1, $2, $3, '对话')
	`, conversationID, versionID, kind); err != nil {
		t.Fatalf("seed %s conversation: %v", kind, err)
	}
	parts, _ := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO conversation_message (id, conversation_id, role, parts, refs)
		VALUES ($1, $2, 'user', $3::jsonb, '[{"kind":"requirement","id":"101"}]'::jsonb)
	`, messageID, conversationID, parts); err != nil {
		t.Fatalf("seed %s message: %v", kind, err)
	}
}

// handoffSeedSharedRepository binds one repository to a version through two SUTs
// with different roles. The role selection must stay stable across
// regenerations instead of depending on physical row order.
func handoffSeedSharedRepository(t *testing.T, pool *pgxpool.Pool, versionID int64) {
	t.Helper()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed shared repository: %v", err)
		}
	}
	exec(`INSERT INTO sut (id, name, created_by) VALUES (2101, 'Backend SUT', 1)`)
	exec(`INSERT INTO sut (id, name, created_by) VALUES (2102, 'Frontend SUT', 1)`)
	exec(`INSERT INTO sut_environment (id, sut_id, name, base_url)
		VALUES (2201, 2101, 'backend', 'https://backend.example.test')`)
	exec(`INSERT INTO sut_environment (id, sut_id, name, base_url)
		VALUES (2202, 2102, 'frontend', 'https://frontend.example.test')`)
	exec(`INSERT INTO version_sut (id, version_id, sut_id, environment_id, status)
		VALUES (2301, $1, 2101, 2201, 'active')`, versionID)
	exec(`INSERT INTO version_sut (id, version_id, sut_id, environment_id, status)
		VALUES (2302, $1, 2102, 2202, 'active')`, versionID)
	exec(`INSERT INTO code_repository (id, name, path, created_by)
		VALUES (2401, 'shared-repo', '/repos/shared', 1)`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (2501, 2101, 2401, 'frontend', true)`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (2502, 2102, 2401, 'backend', true)`)
}

// handoffSeedBulkRequirementsAndFacts seeds more rows than every handoff block
// cap so the cap direction (newest kept) is observable.
func handoffSeedBulkRequirementsAndFacts(t *testing.T, pool *pgxpool.Pool, versionID int64) {
	t.Helper()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed bulk handoff rows: %v", err)
		}
	}
	exec(`INSERT INTO requirement (id, version_id, status, source_quote)
		SELECT 3000 + g, $1, 'clarified', '' FROM generate_series(1, 250) AS g`, versionID)
	exec(`INSERT INTO requirement_version (id, requirement_id, revision, title, description,
			acceptance_criteria, priority, content_hash)
		SELECT 4000 + g, 3000 + g, 1, '批量需求 ' || g, '', '', 'P2', repeat('d', 64)
		FROM generate_series(1, 250) AS g`)
	exec(`UPDATE requirement SET current_version_id = 4000 + (id - 3000) WHERE version_id = $1`, versionID)
	exec(`INSERT INTO version_fact (id, version_id, kind, key, payload, source, source_ref, content_hash)
		SELECT 5000 + g, $1, 'decision', '批量决策 ' || g,
		       jsonb_build_object('summary', '批量决策 ' || g), 'exploration', '', repeat('e', 64)
		FROM generate_series(1, 150) AS g`, versionID)
	exec(`INSERT INTO version_fact (id, version_id, kind, key, payload, source, source_ref, content_hash)
		SELECT 6000 + g, $1, 'gap', '批量缺口 ' || g,
		       jsonb_build_object('summary', '批量缺口 ' || g), 'exploration', '', repeat('f', 64)
		FROM generate_series(1, 150) AS g`, versionID)
}

// TestHandoffRepositoryKeepsBatchQuestionsAndResolvesExternalRefs pins the two
// open-block identity/ref fixes against PostgreSQL: one batch with three pending
// questions yields three open items (unique source per question key), and the
// documented external "requirement:FR-102" refs resolve to internal requirement
// 102.
func TestHandoffRepositoryKeepsBatchQuestionsAndResolvesExternalRefs(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID := int64(1010)
	handoffSeedVersion(t, pool, versionID, "designing")
	handoffSeedRequirements(t, pool, versionID)
	batch := `{"questions":[` +
		`{"id":"q1","text":"问题一（外部 FR 引用）","options":[{"id":"a","label":"A"}],"source_refs":["requirement:FR-102"]},` +
		`{"id":"q2","text":"问题二（内部数字引用）","options":[{"id":"a","label":"A"}],"source_refs":["requirement:102"]},` +
		`{"id":"q3","text":"问题三（附件引用）","options":[{"id":"a","label":"A"}],"source_refs":["attachment:7"]}` +
		`],"batch_id":"qb-batch","expires_at":"2026-11-06T06:00:00Z"}`
	handoffSeedQuestionRow(t, pool, versionID, 1601, 9601, 1301, batch, "pending", "{}", nil)

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
	if len(document.QA) != 3 {
		t.Fatalf("qa = %d, want the three batch questions", len(document.QA))
	}
	for index, key := range []string{"q1", "q2", "q3"} {
		item := document.QA[index]
		if item.RequestID != 1601 || item.QuestionKey != key {
			t.Fatalf("qa[%d] = %+v, want request 1601 key %s", index, item, key)
		}
	}
	if document.QA[0].RequirementID != 102 || document.QA[1].RequirementID != 102 ||
		document.QA[2].RequirementID != 0 {
		t.Fatalf("qa requirement refs = %+v, want 102/102/0", document.QA)
	}
	if len(document.QA[0].RequirementRefs) != 1 || document.QA[0].RequirementRefs[0] != 102 {
		t.Fatalf("external requirement refs = %+v", document.QA[0].RequirementRefs)
	}

	openQuestions := make([]versionapp.HandoffOpenItem, 0, 3)
	for _, item := range document.Open {
		if item.Kind == "question" {
			openQuestions = append(openQuestions, item)
		}
	}
	if len(openQuestions) != 3 {
		t.Fatalf("open questions = %d (%+v), want all three batch questions", len(openQuestions), openQuestions)
	}
	for index, key := range []string{"q1", "q2", "q3"} {
		if openQuestions[index].ReferenceID != 1601 ||
			openQuestions[index].Source != "human_request:1601:"+key {
			t.Fatalf("open question %d = %+v, want request 1601 source ...:%s", index, openQuestions[index], key)
		}
	}
}

// TestHandoffRepositoryCapsKeepNewest pins the cap direction at the SQL + app
// boundary: with 250 clarified requirements, 150 decisions and 150 gaps, the
// newest 200/100/100 rows survive.
func TestHandoffRepositoryCapsKeepNewest(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID := int64(1011)
	handoffSeedVersion(t, pool, versionID, "designing")
	handoffSeedBulkRequirementsAndFacts(t, pool, versionID)

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
	if len(document.Requirements) != versionapp.HandoffMaxRequirements {
		t.Fatalf("requirements = %d, want cap %d", len(document.Requirements), versionapp.HandoffMaxRequirements)
	}
	if document.Requirements[0].ID != 3051 ||
		document.Requirements[len(document.Requirements)-1].ID != 3250 {
		t.Fatalf("requirements keep %d..%d, want newest 3051..3250",
			document.Requirements[0].ID, document.Requirements[len(document.Requirements)-1].ID)
	}
	if len(document.Decisions) != versionapp.HandoffMaxDecisions {
		t.Fatalf("decisions = %d, want cap %d", len(document.Decisions), versionapp.HandoffMaxDecisions)
	}
	if document.Decisions[0].FactID != 5051 ||
		document.Decisions[len(document.Decisions)-1].FactID != 5150 {
		t.Fatalf("decisions keep %d..%d, want newest 5051..5150",
			document.Decisions[0].FactID, document.Decisions[len(document.Decisions)-1].FactID)
	}
	openGaps := make([]versionapp.HandoffOpenItem, 0, 100)
	for _, item := range document.Open {
		if item.Kind == "gap" {
			openGaps = append(openGaps, item)
		}
	}
	if len(openGaps) != versionapp.HandoffMaxOpen {
		t.Fatalf("open gaps = %d, want cap %d", len(openGaps), versionapp.HandoffMaxOpen)
	}
	if openGaps[0].ReferenceID != 6051 || openGaps[len(openGaps)-1].ReferenceID != 6150 {
		t.Fatalf("open gaps keep %d..%d, want newest 6051..6150",
			openGaps[0].ReferenceID, openGaps[len(openGaps)-1].ReferenceID)
	}
}

// TestHandoffRepositorySharedRepositoryRoleDeterministic pins deterministic role
// selection when the same repository is inherited from two SUTs. The
// lexicographically smallest role wins ("backend" before "frontend"), and two
// regenerations produce identical content and stay on revision 1.
func TestHandoffRepositorySharedRepositoryRoleDeterministic(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID := int64(1012)
	handoffSeedVersion(t, pool, versionID, "designing")
	handoffSeedSharedRepository(t, pool, versionID)

	service := versionapp.NewHandoffService(
		versionpg.NewHandoffStore(pool),
		versionpg.NewHandoffReader(pool),
		handoffIntegrationIDs(t),
		nil,
	)
	first, err := service.Generate(ctx, versionID)
	if err != nil {
		t.Fatalf("Generate first: %v", err)
	}
	if len(first.Resources.Repositories) != 1 {
		t.Fatalf("repositories = %+v, want the one shared repository", first.Resources.Repositories)
	}
	if first.Resources.Repositories[0].Role != "backend" {
		t.Fatalf("shared repository role = %q, want the deterministic role %q",
			first.Resources.Repositories[0].Role, "backend")
	}
	firstResources, err := json.Marshal(first.Resources)
	if err != nil {
		t.Fatalf("marshal first resources: %v", err)
	}
	second, err := service.Generate(ctx, versionID)
	if err != nil {
		t.Fatalf("Generate second: %v", err)
	}
	secondResources, err := json.Marshal(second.Resources)
	if err != nil {
		t.Fatalf("marshal second resources: %v", err)
	}
	if string(firstResources) != string(secondResources) {
		t.Fatalf("resources changed across regenerations:\nfirst:  %s\nsecond: %s", firstResources, secondResources)
	}
	if first.Revision != 1 || second.Revision != 1 {
		t.Fatalf("revisions = %d/%d, want stable revision 1", first.Revision, second.Revision)
	}
	if countHandoffRows(t, pool, versionID) != 1 {
		t.Fatalf("handoff rows = %d, want 1 (no revision churn from the role)", countHandoffRows(t, pool, versionID))
	}
}

// TestClarificationSearchStoreEvidenceAndScope pins the search evidence rules:
// the returned source-id set is exactly the queried version's rows (other
// version facts/questions, design-conversation messages are absent), and
// requirement-scoped search resolves both numeric and external refs.
func TestClarificationSearchStoreEvidenceAndScope(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID, otherVersionID := int64(2001), int64(2002)
	handoffSeedVersion(t, pool, versionID, "designing")
	handoffSeedVersion(t, pool, otherVersionID, "designing")
	handoffSeedRequirements(t, pool, versionID)
	handoffSeedQuestions(t, pool, versionID, 2301)
	handoffSeedFactsAndPlan(t, pool, versionID, 0)
	handoffSeedConversation(t, pool, versionID, 2301, 2401, "退款到账时间按 T+1 验收，退款失败可重试。")
	// A design conversation in the same version is not clarification evidence.
	handoffSeedConversationKind(t, pool, versionID, 2303, 2403, "design", "设计阶段的退款讨论，不属于澄清检索。")
	// Other-version rows with the same keyword must never enter the result.
	handoffSeedFactsAndPlan(t, pool, otherVersionID, 10000)
	handoffSeedConversation(t, pool, otherVersionID, 2302, 2402, "其它版本的退款说明，绝不能返回。")
	handoffSeedQuestionRow(t, pool, otherVersionID, 2501, 9801, 2302,
		`{"questions":[{"id":"q1","text":"其它版本的退款问题，不能返回","options":[{"id":"a","label":"A"}],"source_refs":["requirement:999"]}],"batch_id":"qb-other"}`,
		"pending", "{}", nil)

	store := versionpg.NewClarificationSearchStore(pool)
	hits, err := store.SearchClarification(ctx, agenttools.ClarificationSearchQuery{
		VersionID: versionID,
		Query:     "退款",
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("SearchClarification: %v", err)
	}
	gotSources := make([]string, 0, len(hits))
	for _, hit := range hits {
		if hit.VersionID != versionID {
			t.Fatalf("hit reports version %d, want %d: %+v", hit.VersionID, versionID, hit)
		}
		// Source attribution must resolve the external ref even without a
		// requirement filter: question 502 is linked via "requirement:FR-102".
		if hit.Source == "human_request:502" && hit.RequirementID != 102 {
			t.Fatalf("qa hit 502 requirement_id = %d, want resolved 102: %+v", hit.RequirementID, hit)
		}
		gotSources = append(gotSources, hit.Source)
	}
	sort.Strings(gotSources)
	wantSources := []string{
		"conversation_message:2401",
		"human_request:501",
		"human_request:502",
		"version_fact:601",
		"version_fact:602",
	}
	if strings.Join(gotSources, ",") != strings.Join(wantSources, ",") {
		t.Fatalf("sources = %v, want exactly %v", gotSources, wantSources)
	}

	// requirement 102 is referenced by question 502 as "requirement:FR-102"
	// (external id); it must resolve to the internal id 102.
	byExternal, err := store.SearchClarification(ctx, agenttools.ClarificationSearchQuery{
		VersionID:     versionID,
		RequirementID: 102,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("SearchClarification by external requirement: %v", err)
	}
	if len(byExternal) != 1 || byExternal[0].Source != "human_request:502" ||
		byExternal[0].RequirementID != 102 {
		t.Fatalf("external requirement hits = %+v, want only human_request:502/102", byExternal)
	}

	// Numeric refs keep working: requirement 101 links question 501, fact 601
	// and message 2401.
	byNumeric, err := store.SearchClarification(ctx, agenttools.ClarificationSearchQuery{
		VersionID:     versionID,
		RequirementID: 101,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("SearchClarification by numeric requirement: %v", err)
	}
	gotNumeric := make([]string, 0, len(byNumeric))
	for _, hit := range byNumeric {
		if hit.VersionID != versionID || hit.RequirementID != 101 {
			t.Fatalf("numeric requirement hit = %+v", hit)
		}
		gotNumeric = append(gotNumeric, hit.Source)
	}
	sort.Strings(gotNumeric)
	wantNumeric := []string{"conversation_message:2401", "human_request:501", "version_fact:601"}
	if strings.Join(gotNumeric, ",") != strings.Join(wantNumeric, ",") {
		t.Fatalf("numeric requirement sources = %v, want exactly %v", gotNumeric, wantNumeric)
	}
}

// TestClarificationSearchStoreClampsDirectCallerLimit pins the direct adapter
// contract: an over-max limit is clamped to the tool maximum, not reset to the
// default page size.
func TestClarificationSearchStoreClampsDirectCallerLimit(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	versionID := int64(2003)
	handoffSeedVersion(t, pool, versionID, "designing")
	if _, err := pool.Exec(ctx, `
		INSERT INTO version_fact (id, version_id, kind, key, payload, source, source_ref, content_hash)
		SELECT 7000 + g, $1, 'decision', '批量决策 ' || g,
		       jsonb_build_object('summary', '批量决策 ' || g), 'exploration', '', repeat('a', 64)
		FROM generate_series(1, 60) AS g
	`, versionID); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	store := versionpg.NewClarificationSearchStore(pool)
	hits, err := store.SearchClarification(ctx, agenttools.ClarificationSearchQuery{
		VersionID: versionID,
		Query:     "批量",
		Kinds:     []string{agenttools.ClarificationSearchKindDecision},
		Limit:     1000,
	})
	if err != nil {
		t.Fatalf("SearchClarification: %v", err)
	}
	if len(hits) != agenttools.MaxClarificationSearchLimit {
		t.Fatalf("hits = %d, want the clamped maximum %d", len(hits), agenttools.MaxClarificationSearchLimit)
	}
}
