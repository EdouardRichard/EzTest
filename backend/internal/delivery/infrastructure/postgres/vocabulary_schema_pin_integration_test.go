//go:build integration

package deliverypg

// Real-DB compensating controls for Ruling T11d-R4 and schema pins for the
// vocabularies T11e-1 made reachable.
//
//   - TestAnalysisConflictPredicateMatchesIndex compares the non-parameterizable
//     ON CONFLICT literals with the LIVE partial unique index predicate, so the
//     Go SQL, the domain constants and the DB index are pinned three-way.
//   - TestAnalysisCreateQueuedSurvivesGenericPlan executes the conflicting insert
//     far past PostgreSQL's custom→generic plan threshold. This is the born-green
//     trap detector: a parameterized index predicate passes the first four
//     executions and fails with SQLSTATE 42P10 from the fifth, so any test that
//     only inserts once or twice would be falsely green.
//   - TestReportRefKindMatchesSchema / TestRenderStatusMatchesSchema /
//     TestIssueActorKindMatchesSchema pin the newly reachable authorities against
//     the frozen CHECK constraints (expectations come from the CONTRACT, not from
//     observed output).

import (
	"context"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// constraintValues reads a constraint/index definition and returns its quoted
// literals. PostgreSQL rewrites `IN ('a','b')` as `= ANY (ARRAY['a'::text,...])`,
// so the extraction is on quoted literals rather than on the IN syntax.
func constraintValues(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []string {
	t.Helper()
	var def string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&def); err != nil {
		t.Fatalf("load constraint definition: %v", err)
	}
	values := quotedValues(def)
	if len(values) == 0 {
		t.Fatalf("constraint definition carries no quoted literals: %s", def)
	}
	return values
}

func TestAnalysisConflictPredicateMatchesIndex(t *testing.T) {
	_, pool := newAnalysisRepoForTest(t)
	got := constraintValues(t, pool, `
		SELECT indexdef FROM pg_indexes
		WHERE tablename = 'delivery_analysis_run'
		  AND indexname = 'uk_delivery_analysis_run_version_id_kind_dedupe_key'
	`)
	want := sortedSet(analysisActiveStatuses())
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live partial unique index statuses %v != domain-derived active statuses %v", got, want)
	}
	match := conflictPredicatePattern.FindStringSubmatch(insertAnalysisRunSQL)
	if match == nil {
		t.Fatal("insertAnalysisRunSQL lost its ON CONFLICT index predicate")
	}
	if strings.Join(quotedValues(match[1]), ",") != strings.Join(got, ",") {
		t.Fatalf("ON CONFLICT predicate %v != live index predicate %v; PostgreSQL cannot infer the index",
			quotedValues(match[1]), got)
	}
}

// TestAnalysisCreateQueuedSurvivesGenericPlan is the 42P10 regression. pgx caches
// the prepared statement per connection, and PostgreSQL switches to a generic
// plan after ~5 executions; a parameterized index predicate breaks exactly there.
func TestAnalysisCreateQueuedSurvivesGenericPlan(t *testing.T) {
	repo, pool := newAnalysisRepoForTest(t)
	// One connection so every execution reuses the SAME cached prepared statement
	// (a pool that hands out different connections would reset the plan counter
	// and hide the failure).
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	cfg.MinConns = 1
	single, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("single-connection pool: %v", err)
	}
	defer single.Close()
	repo = NewAnalysisRepository(single, repo.ids)

	ctx := context.Background()
	run := analysisRepoQueuedRun(t, "9101")
	first, err := repo.CreateQueued(ctx, run)
	if err != nil {
		t.Fatalf("CreateQueued #1: %v", err)
	}
	for attempt := 2; attempt <= 12; attempt++ {
		again, err := repo.CreateQueued(ctx, run)
		if err != nil {
			t.Fatalf("CreateQueued #%d: %v -- a parameterized ON CONFLICT index predicate fails here "+
				"with SQLSTATE 42P10 once the cached statement uses a generic plan (Ruling T11d-R4)", attempt, err)
		}
		if again.ID != first.ID {
			t.Fatalf("CreateQueued #%d returned id %d, want the idempotent %d", attempt, again.ID, first.ID)
		}
	}
	var count int
	if err := single.QueryRow(ctx, `
		SELECT count(*) FROM delivery_analysis_run WHERE version_id = $1 AND dedupe_key = $2
	`, run.VersionID, run.DedupeKey).Scan(&count); err != nil {
		t.Fatalf("count analysis runs: %v", err)
	}
	if count != 1 {
		t.Fatalf("12 conflicting inserts produced %d rows, want 1 (the partial index dedupe stopped working)", count)
	}
}

func TestReportRefKindMatchesSchema(t *testing.T) {
	_, pool := newAnalysisRepoForTest(t)
	got := constraintValues(t, pool, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'delivery_report_ref'::regclass AND conname = 'delivery_report_ref_kind_check'
	`)
	want := sortedSet(delivery.ReportRefKinds())
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("delivery_report_ref kind CHECK %v != delivery.ReportRefKinds() %v "+
			"(four-site lockstep: emit + validator + CHECK + chain provider)", got, want)
	}
	for _, kind := range delivery.ReportRefKinds() {
		if !validReportRefKind(kind) {
			t.Fatalf("validReportRefKind rejects the domain kind %q", kind)
		}
	}
	if validReportRefKind("version_sut") {
		t.Fatal("validReportRefKind still accepts the pre-B 'version_sut' label")
	}
}

func TestRenderStatusMatchesSchema(t *testing.T) {
	_, pool := newAnalysisRepoForTest(t)
	got := constraintValues(t, pool, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'delivery_report_revision'::regclass
		  AND pg_get_constraintdef(oid) LIKE '%render_status%'
	`)
	want := sortedSet([]string{
		deliveryapp.ReportRenderPending, deliveryapp.ReportRenderRunning,
		deliveryapp.ReportRenderDone, deliveryapp.ReportRenderFailed,
	})
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("render_status CHECK %v != the exported render-status authority %v", got, want)
	}
	for _, value := range want {
		if !validRenderStatus(value) {
			t.Fatalf("validRenderStatus rejects the authority value %q", value)
		}
	}
	if validRenderStatus(string(delivery.PDFStatusDisabled)) {
		t.Fatal("validRenderStatus accepts 'disabled', which only the pdf_status CHECK allows")
	}
}

func TestIssueActorKindMatchesSchema(t *testing.T) {
	_, pool := newAnalysisRepoForTest(t)
	got := constraintValues(t, pool, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'delivery_issue_status_history'::regclass
		  AND pg_get_constraintdef(oid) LIKE '%actor_kind%'
	`)
	// The CHECK also allows 'agent', which has no producer today; the pin asserts
	// the authority is a SUBSET of the schema (never a value the DB would reject).
	allowed := map[string]bool{}
	for _, value := range got {
		allowed[value] = true
	}
	for _, value := range []string{deliveryapp.IssueActorKindUser, deliveryapp.IssueActorKindSystem} {
		if !allowed[value] {
			t.Fatalf("actor-kind authority %q is not allowed by the live CHECK %v", value, got)
		}
	}
	if !allowed["agent"] {
		t.Fatalf("the actor_kind CHECK no longer lists 'agent' (%v); re-adjudicate the reserved value", got)
	}
}

// TestIssueSeverityMatchesSchema pins the issue-severity authority T11e-6 created
// (delivery.IssueSeverity*) against the frozen delivery_issue.severity CHECK. The
// report projection (render_v3.go severityLabel) and the domain validator
// (validIssueSeverity) both source these constants now, so a severity added on
// one side without the others is a drift this pin catches: validator + CHECK +
// report label + authority.
func TestIssueSeverityMatchesSchema(t *testing.T) {
	_, pool := newAnalysisRepoForTest(t)
	got := constraintValues(t, pool, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'delivery_issue'::regclass
		  AND pg_get_constraintdef(oid) LIKE '%severity%'
	`)
	want := sortedSet(delivery.IssueSeverities())
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("delivery_issue severity CHECK %v != delivery.IssueSeverities() %v", got, want)
	}
}
