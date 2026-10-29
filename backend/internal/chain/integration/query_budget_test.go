//go:build integration

// Package integration_test holds the P8 exploration-chain integration tests. It
// runs against a real PostgreSQL database (the shared testutil helper enforces
// the dedicated *_test database rule) and is skipped unless
// EZTEST_TEST_DATABASE_URL is set.
package integration_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	codeintelchain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/chain"
	deliverychain "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/chain"
	executionchain "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/chain"
	mindmapchain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/chain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionchain "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/chain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// chainBudgetVersionID is the version explored by the budget fixture.
const (
	chainBudgetVersionID = int64(9001)
	chainBudgetBatchOld  = int64(9101)
	chainBudgetBatchNew  = int64(9102)

	// chainBudgetQueryLimit is the spec §5.2 per-request SQL budget.
	chainBudgetQueryLimit = 10
)

// chainQueryCounter counts every statement the traced pool executes. Only the
// provider pool is traced: fixture inserts go through the untraced testutil
// pool, so the counter sees exactly the chain Build queries.
type chainQueryCounter struct {
	mu   sync.Mutex
	sqls []string
}

// TraceQueryStart records one executed statement.
func (c *chainQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.sqls = append(c.sqls, data.SQL)
	c.mu.Unlock()
	return ctx
}

// TraceQueryEnd implements pgx.QueryTracer.
func (c *chainQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

// Reset drops the statements recorded so far.
func (c *chainQueryCounter) Reset() {
	c.mu.Lock()
	c.sqls = nil
	c.mu.Unlock()
}

// Count returns the number of statements recorded since the last Reset.
func (c *chainQueryCounter) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sqls)
}

// SQLs returns a copy of the recorded statements for failure diagnostics.
func (c *chainQueryCounter) SQLs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sqls...)
}

// chainOpenTracedPool opens a second pool on the already-migrated test database
// with the query counter attached. The URL is validated by testutil.OpenTestDB
// before this helper runs; parse/connect failures deliberately do not echo the
// connection string (it contains credentials).
func chainOpenTracedPool(t *testing.T, counter *chainQueryCounter) *pgxpool.Pool {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal("parse EZTEST_TEST_DATABASE_URL for the traced pool: invalid configuration")
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal("open the traced test database pool: invalid configuration")
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatal("traced test database pool is not reachable")
	}
	return pool
}

// chainSeedBudgetFixture inserts the §5.2 budget fixture: 100 requirements, 300
// cases, 300 case runs (100 cases have two attempts), 100 issues, 2 latest
// analysis impact candidates and enough clarity questions/evidence/issues refs
// to exercise the scalar aggregates.
func chainSeedBudgetFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`INSERT INTO version (id, name, status, revision, created_by, updated_by)
		 VALUES (9001, 'p8-chain-budget', 'executing', 1, 1, 1)`,
		`INSERT INTO run_batch
			(id, version_id, batch_no, status, baseline_hash, graph_hash, kind, created_by, created_at)
		 VALUES
			(9101, 9001, 1, 'done', repeat('a', 64), repeat('a', 64), 'baseline', 1,
			 '2026-10-29T00:00:00Z'),
			(9102, 9001, 2, 'done', repeat('b', 64), repeat('b', 64), 'incremental', 1,
			 '2026-10-29T01:00:00Z')`,
		`INSERT INTO requirement (id, version_id, title, priority, status, created_by, updated_by, created_at)
		 SELECT 10000 + i, 9001, 'Requirement ' || i,
		        CASE i % 3 WHEN 0 THEN 'must' WHEN 1 THEN 'should' ELSE 'could' END,
		        CASE WHEN i % 10 = 0 THEN 'draft' WHEN i % 10 = 5 THEN 'designed' ELSE 'clarified' END,
		        1, 1, '2026-10-29T02:00:00Z'::timestamptz + make_interval(mins => i)
		 FROM generate_series(0, 99) AS i`,
		`INSERT INTO clarification_question (id, version_id, requirement_id, question, status, created_by)
		 SELECT 30000 + i, 9001, 10000 + i, 'Question ' || i, 'open', 1
		 FROM generate_series(0, 99) AS i`,
		`INSERT INTO clarification_question (id, version_id, requirement_id, question, status, answer, created_by)
		 VALUES (30100, 9001, 10000, 'Extra open', 'open', '', 1),
		        (30101, 9001, 10000, 'Answered', 'answered', 'yes', 1)`,
		`INSERT INTO case_node
			(id, version_id, kind, title, priority, source, status, sort_order, created_by, updated_by)
		 SELECT 20000 + i, 9001, 'case', 'Case ' || i,
		        CASE i % 4 WHEN 0 THEN 'P0' WHEN 1 THEN 'P1' WHEN 2 THEN 'P2' ELSE 'P3' END,
		        CASE WHEN i % 5 = 0 THEN 'ai_supplement' ELSE 'user_input' END,
		        CASE WHEN i % 7 = 0 THEN 'needs_revalidation'
		             WHEN i % 3 = 0 THEN 'executed' ELSE 'reviewed' END,
		        i, 1, 1
		 FROM generate_series(0, 299) AS i`,
		`INSERT INTO case_node
			(id, version_id, kind, title, priority, source, status, sort_order, created_by, updated_by)
		 VALUES (29999, 9001, 'scenario', 'Scenario', 'P1', 'user_input', 'draft', 300, 1, 1)`,
		`INSERT INTO case_source (id, version_id, node_id, ref_kind, ref_id, source)
		 SELECT 40000 + i, 9001, 20000 + i, 'requirement', 10000 + (i % 100),
		        CASE WHEN i % 5 = 0 THEN 'ai_supplement' ELSE 'user_input' END
		 FROM generate_series(0, 299) AS i`,
		`INSERT INTO case_source (id, version_id, node_id, ref_kind, ref_id, source)
		 VALUES (40300, 9001, 20000, 'requirement', 10009, 'user_input')`,
		`INSERT INTO case_source (id, version_id, node_id, ref_kind, ref_id, source)
		 VALUES (40399, 9001, 29999, 'requirement', 10011, 'user_input')`,
		`INSERT INTO case_step (id, node_id, seq, action, source)
		 SELECT 50000 + i * 10 + seq, 20000 + i, seq, 'Step ' || seq, 'user_input'
		 FROM generate_series(0, 299) AS i, generate_series(1, 2) AS seq`,
		`INSERT INTO case_assertion (id, node_id, kind, expression, source)
		 SELECT 60000 + i, 20000 + i, 'ui', 'visible', 'user_input'
		 FROM generate_series(0, 299) AS i`,
		`INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot,
			 attempt, status, failure_class, summary, duration_ms, created_at)
		 SELECT 70000 + i * 10 + 1, 9102, 9001, 20000 + i, 1, repeat('c', 64),
		        '{"node":{"title":"old"}}'::jsonb, 1, 'failed', 'application', 'old attempt', 100,
		        '2026-10-29T03:00:00Z'::timestamptz + make_interval(mins => i)
		 FROM generate_series(0, 99) AS i`,
		`INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot,
			 attempt, status, failure_class, summary, duration_ms, created_at)
		 SELECT 70000 + i * 10 + 2, 9102, 9001, 20000 + i, 1, repeat('c', 64),
		        '{"node":{"title":"latest"}}'::jsonb, 2, 'passed', NULL, 'latest attempt', 1500,
		        '2026-10-29T04:00:00Z'::timestamptz + make_interval(mins => i)
		 FROM generate_series(0, 99) AS i`,
		`INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot,
			 attempt, status, failure_class, summary, duration_ms, created_at)
		 SELECT 71100 + i, 9102, 9001, 20000 + i, 1, repeat('c', 64),
		        '{"node":{"title":"only"}}'::jsonb, 1, 'failed', 'environment', 'only attempt', 30,
		        '2026-10-29T04:30:00Z'::timestamptz + make_interval(mins => i)
		 FROM generate_series(100, 199) AS i`,
		`INSERT INTO evidence (id, batch_id, case_run_id, kind, path, sha256, size_bytes)
		 VALUES (80001, 9102, 70001, 'screenshot', 'shots/old-1.png', repeat('d', 64), 10),
		        (80002, 9102, 70001, 'screenshot', 'shots/old-2.png', repeat('d', 64), 10),
		        (80003, 9102, 70002, 'screenshot', 'shots/latest.png', repeat('d', 64), 10),
		        (80004, 9102, 70012, 'screenshot', 'shots/case-1.png', repeat('d', 64), 10)`,
		`INSERT INTO issue
			(id, version_id, batch_id, case_run_id, case_node_id, source, category, severity,
			 status, confidence, title, dismissed_reason, created_by, updated_by, created_at)
		 SELECT 90000 + i, 9001, 9102, 70000 + i * 10 + 2, NULL, 'human', 'defect',
		        CASE i % 4 WHEN 0 THEN 'blocker' WHEN 1 THEN 'major' WHEN 2 THEN 'minor' ELSE 'info' END,
		        'open', 'high', 'Issue ' || i, '', 1, 1,
		        '2026-10-29T05:00:00Z'::timestamptz + make_interval(mins => i)
		 FROM generate_series(0, 9) AS i`,
		`INSERT INTO issue
			(id, version_id, batch_id, case_run_id, case_node_id, source, category, severity,
			 status, confidence, title, dismissed_reason, created_by, updated_by, created_at)
		 VALUES (90010, 9001, 9102, 70001, NULL, 'human', 'defect', 'major', 'confirmed',
		         'high', 'Old attempt issue', '', 1, 1, '2026-10-29T05:10:00Z')`,
		`INSERT INTO issue
			(id, version_id, batch_id, case_run_id, case_node_id, source, category, severity,
			 status, confidence, title, dismissed_reason, created_by, updated_by, created_at)
		 SELECT 90000 + i, 9001, 9102, NULL, NULL, 'human', 'requirement_gap', 'info',
		        CASE WHEN i % 3 = 0 THEN 'dismissed' ELSE 'open' END,
		        'high', 'Gap ' || i,
		        CASE WHEN i % 3 = 0 THEN 'duplicate' ELSE '' END, 1, 1,
		        '2026-10-29T06:00:00Z'::timestamptz + make_interval(mins => i)
		 FROM generate_series(11, 69) AS i`,
		`INSERT INTO issue
			(id, version_id, batch_id, case_run_id, case_node_id, source, category, severity,
			 status, confidence, title, dismissed_reason, created_by, updated_by, created_at)
		 SELECT 90000 + i, 9001, 9101, NULL, NULL, 'human', 'requirement_gap', 'info',
		        'open', 'high', 'Old batch gap ' || i, '', 1, 1,
		        '2026-10-29T06:30:00Z'::timestamptz + make_interval(mins => i)
		 FROM generate_series(70, 99) AS i`,
		`INSERT INTO issue_ref (issue_id, ref_kind, ref_id)
		 VALUES (90000, 'evidence', '80001'), (90000, 'evidence', '80002'),
		        (90000, 'code_anchor', '41'), (90010, 'evidence', '80001')`,
		`INSERT INTO code_analysis
			(id, version_id, repo_id, base_commit, head_commit, status, prompt_version, created_by, created_at)
		 VALUES
			(95001, 9001, 1, repeat('1', 40), repeat('2', 40), 'done', 'chain-budget', 1,
			 '2026-10-29T07:00:00Z'),
			(95002, 9001, 1, repeat('3', 40), repeat('4', 40), 'done', 'chain-budget', 1,
			 '2026-10-29T08:00:00Z')`,
		`INSERT INTO code_impact_candidate
			(id, analysis_id, subject_kind, subject_id, reason, confidence, source, selected)
		 VALUES
			(96001, 95001, 'requirement', 10000, 'old analysis', 'high', 'direct', true),
			(96002, 95002, 'requirement', 10001, 'new analysis', 'high', 'direct', true),
			(96003, 95002, 'requirement', 10002, 'unselected low', 'low', 'ai', false)`,
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("seed chain budget fixture statement %d: %v", index, err)
		}
	}
}

// chainFindNode returns the node with the given id.
func chainFindNode(nodes []chainapp.NodeDTO, id string) (chainapp.NodeDTO, bool) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true
		}
	}
	return chainapp.NodeDTO{}, false
}

// chainHasEdge reports whether the edge id exists.
func chainHasEdge(edges []chainapp.EdgeDTO, id string) bool {
	for _, edge := range edges {
		if edge.ID == id {
			return true
		}
	}
	return false
}

// TestExplorationChainQueryBudgetAndLatestAttempts is the Task 7 §5.2 budget
// gate: on a real database seeded with 100 requirements, 300 cases, 300 case
// runs, 100 issues and 2 latest-analysis impact candidates, one Service.Build
// must issue at most 10 SQL statements while still returning the complete
// layers, the latest attempt per case and the Task 6 deferred case_run-only
// issue link.
func TestExplorationChainQueryBudgetAndLatestAttempts(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	counter := &chainQueryCounter{}
	traced := chainOpenTracedPool(t, counter)
	chainSeedBudgetFixture(t, pool)

	service := chainapp.NewService(chainapp.Providers{
		Version:   versionchain.New(traced),
		Mindmap:   mindmapchain.New(traced),
		Execution: executionchain.New(traced),
		Delivery:  deliverychain.New(traced),
		CodeIntel: codeintelchain.New(traced),
	})

	counter.Reset()
	snapshot, err := service.Build(context.Background(), chainapp.Query{
		VersionID:         "9001",
		Limit:             2000,
		RequirementLimit:  100,
		IncludeSupplement: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	queries := counter.Count()
	t.Logf("exploration-chain Build issued %d SQL statements (budget %d)", queries, chainBudgetQueryLimit)
	if queries > chainBudgetQueryLimit {
		t.Fatalf("Build issued %d SQL statements, want <= %d:\n%s",
			queries, chainBudgetQueryLimit, strings.Join(counter.SQLs(), "\n"))
	}

	// Batch + complete layers.
	if snapshot.Batch.ID != "9102" || snapshot.Batch.BatchNo != 2 ||
		snapshot.Batch.Kind != "incremental" || snapshot.Batch.Status != "done" {
		t.Fatalf("batch = %+v, want the latest batch 9102/2/incremental/done", snapshot.Batch)
	}
	if snapshot.Counts.Requirements != 100 || snapshot.Counts.Cases != 300 ||
		snapshot.Counts.Results != 200 || snapshot.Counts.Issues != 70 {
		t.Fatalf("counts = %+v, want 100 requirements / 300 cases / 200 results / 70 issues",
			snapshot.Counts)
	}
	if snapshot.Counts.ByResultStatus["passed"] != 100 || snapshot.Counts.ByResultStatus["failed"] != 100 {
		t.Fatalf("byResultStatus = %+v, want 100 passed / 100 failed",
			snapshot.Counts.ByResultStatus)
	}
	if snapshot.Counts.SupplementCases != 60 {
		t.Fatalf("supplementCases = %d, want 60", snapshot.Counts.SupplementCases)
	}
	if snapshot.Counts.RevalidationCases != 43 {
		t.Fatalf("revalidationCases = %d, want 43", snapshot.Counts.RevalidationCases)
	}
	if snapshot.Counts.UncoveredRequirements != 0 {
		t.Fatalf("uncoveredRequirements = %d, want 0", snapshot.Counts.UncoveredRequirements)
	}
	if snapshot.Truncated.Requirements || snapshot.Truncated.Cases ||
		snapshot.Truncated.Results || snapshot.Truncated.Issues {
		t.Fatalf("truncated = %+v, want no truncation under the 2000-node limit", snapshot.Truncated)
	}

	// Latest attempt per case: case 20000's attempt 2 wins over attempt 1, case
	// 20100's single attempt is projected, case 20200 has no result.
	latest, ok := chainFindNode(snapshot.Nodes, "result:70002")
	if !ok {
		t.Fatal("latest attempt result:70002 is missing")
	}
	if latest.Attempt == nil || *latest.Attempt != 2 {
		t.Fatalf("result:70002 attempt = %v, want 2", latest.Attempt)
	}
	if latest.Status != "passed" || latest.Summary == nil || *latest.Summary != "latest attempt" {
		t.Fatalf("result:70002 = %+v, want the latest attempt fields", latest)
	}
	if latest.EvidenceCount == nil || *latest.EvidenceCount != 1 {
		t.Fatalf("result:70002 evidenceCount = %v, want 1", latest.EvidenceCount)
	}
	if _, ok := chainFindNode(snapshot.Nodes, "result:70001"); ok {
		t.Fatal("result:70001 (losing first attempt) must not be returned")
	}
	onlyAttempt, ok := chainFindNode(snapshot.Nodes, "result:71200")
	if !ok || onlyAttempt.Attempt == nil || *onlyAttempt.Attempt != 1 || onlyAttempt.Status != "failed" {
		t.Fatalf("result:71200 = %+v, want the only attempt of case 20100", onlyAttempt)
	}
	for _, node := range snapshot.Nodes {
		if node.Kind == chainapp.NodeKindResult && node.CaseNodeID != nil && *node.CaseNodeID == "20200" {
			t.Fatalf("case 20200 has no case run but returned %s", node.ID)
		}
	}

	caseNode, ok := chainFindNode(snapshot.Nodes, "case:20000")
	if !ok {
		t.Fatal("case:20000 is missing")
	}
	if caseNode.StepCount == nil || *caseNode.StepCount != 2 ||
		caseNode.AssertionCount == nil || *caseNode.AssertionCount != 1 {
		t.Fatalf("case:20000 step/assertion counts = %v/%v, want 2/1",
			caseNode.StepCount, caseNode.AssertionCount)
	}
	if caseNode.RequirementIDs == nil {
		t.Fatal("case:20000 requirementIds is nil")
	}
	gotRequirementIDs := strings.Join(*caseNode.RequirementIDs, ",")
	if gotRequirementIDs != "10000,10009" {
		t.Fatalf("case:20000 requirementIds = %q, want 10000,10009 (case_source + coverage union)",
			gotRequirementIDs)
	}

	requirementNode, ok := chainFindNode(snapshot.Nodes, "requirement:10000")
	if !ok {
		t.Fatal("requirement:10000 is missing")
	}
	if requirementNode.OpenQuestionCount == nil || *requirementNode.OpenQuestionCount != 2 {
		t.Fatalf("requirement:10000 openQuestionCount = %v, want 2", requirementNode.OpenQuestionCount)
	}

	// Latest-linked issue draws result_issue; the old-attempt issue only knows
	// its case_run and must still degrade to case_issue via the case_run join
	// (Task 6 deferred edge case).
	if !chainHasEdge(snapshot.Edges, "result_issue:70002:90000") {
		t.Fatal("issue 90000 must draw result_issue from the latest result 70002")
	}
	if !chainHasEdge(snapshot.Edges, "case_issue:20000:90010") {
		t.Fatal("issue 90010 (case_run_id only, older attempt) must degrade to case_issue")
	}
	if chainHasEdge(snapshot.Edges, "result_issue:70001:90010") {
		t.Fatal("issue 90010 must not draw an edge from the non-latest result 70001")
	}
	oldAttemptIssue, ok := chainFindNode(snapshot.Nodes, "issue:90010")
	if !ok || oldAttemptIssue.CaseNodeID == nil || *oldAttemptIssue.CaseNodeID != "20000" {
		t.Fatalf("issue:90010 = %+v, want caseNodeId 20000 filled from the case_run join", oldAttemptIssue)
	}
	if chainHasEdge(snapshot.Edges, "case_issue:20000:90011") ||
		chainHasEdge(snapshot.Edges, "result_issue:70002:90011") {
		t.Fatal("the unlinked issue 90011 must not draw an edge")
	}

	// Only the latest analysis' selected-or-high/medium requirement candidate
	// produces impact edges; the older analysis and the unselected low candidate
	// are ignored.
	if !chainHasEdge(snapshot.Edges, "impact_case:10001:20001") {
		t.Fatal("requirement 10001 must draw an impact_case edge from the latest analysis")
	}
	for _, edge := range snapshot.Edges {
		if edge.Kind != chainapp.EdgeKindImpactCase {
			continue
		}
		if edge.Source == "requirement:10000" || edge.Source == "requirement:10002" {
			t.Fatalf("unexpected impact edge %s: only the latest high/selected candidate may win", edge.ID)
		}
	}
}
