//go:build integration

package integration

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// insertAIFact seeds one AI fact at commit so the confidence distribution and
// the fact ownership checks have real rows. Every call uses a unique fact key
// and Snowflake id.
func insertAIFact(t *testing.T, db *pgxpool.Pool, repoID int64, commit, factKey, kind, confidence, path string) int64 {
	t.Helper()
	id := nextSeedID()
	if _, err := db.Exec(context.Background(),
		`INSERT INTO code_entity
			(id, repo_id, commit_sha, kind, path, name, source, confidence, fact_key, fact_version)
		 VALUES ($1, $2, $3, $4, $5, $6, 'ai', $7, $8, 1)`,
		id, repoID, commit, kind, path, factKey, confidence, factKey,
	); err != nil {
		t.Fatalf("seed AI fact %s: %v", factKey, err)
	}
	return id
}

// TestFactReviewRepositoryIntegration proves the real PostgreSQL behavior the
// unit SQL-pinning tests cannot: the (repo, fact, commit) upsert overwrites the
// same row, a foreign fact is not found, and the quality aggregate reads the
// real analysis/entity/evaluation sources.
func TestFactReviewRepositoryIntegration(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repoID, versionID, _ := seedRepoVersionRequirement(t, pool)
	foreignRepoID, _, _ := seedRepoVersionRequirement(t, pool)

	repo := codeintelpg.NewFactReviewRepository(pool)
	factID := insertAIFact(t, pool, repoID, seedHeadCommit,
		"api:POST /api/refund", domain.KindAPI, domain.ConfidenceHigh, "src/api/refund.ts")

	// The fact is visible only through its own repository; the 404 is the
	// ownership verdict the review write maps to HTTP.
	fact, err := repo.GetFact(ctx, repoID, factID)
	if err != nil {
		t.Fatalf("GetFact: %v", err)
	}
	if fact.ID != factID || fact.RepoID != repoID || fact.CommitSHA != seedHeadCommit ||
		fact.FactKey != "api:POST /api/refund" {
		t.Fatalf("fact projection = %+v", fact)
	}
	if _, err := repo.GetFact(ctx, foreignRepoID, factID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign fact error = %v, want not-found", err)
	}

	// First verdict inserts, the second verdict overwrites the same row (same
	// id) instead of appending history.
	now := ciNow()
	first := domain.FactReview{
		ID: nextSeedID(), RepoID: repoID, FactID: factID, CommitSHA: seedHeadCommit,
		Verdict: domain.FactReviewCorrect, Reason: "证据成立", CreatedBy: 2, CreatedAt: now,
	}
	stored, err := repo.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("Upsert(create): %v", err)
	}
	if stored.ID != first.ID || stored.Verdict != domain.FactReviewCorrect || stored.Reason != "证据成立" {
		t.Fatalf("created review = %+v", stored)
	}

	second := first
	second.ID = nextSeedID()
	second.Verdict = domain.FactReviewIncorrect
	second.Reason = "复核后判定错误"
	second.CreatedAt = now.Add(time.Minute)
	stored, err = repo.Upsert(ctx, second)
	if err != nil {
		t.Fatalf("Upsert(overwrite): %v", err)
	}
	if stored.ID != first.ID {
		t.Fatalf("overwrite changed the review id: %d -> %d", first.ID, stored.ID)
	}
	if stored.Verdict != domain.FactReviewIncorrect || stored.Reason != "复核后判定错误" {
		t.Fatalf("overwritten review = %+v", stored)
	}
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_fact_review WHERE repo_id = $1`, repoID,
	).Scan(&rows); err != nil {
		t.Fatalf("count reviews: %v", err)
	}
	if rows != 1 {
		t.Fatalf("review rows = %d, want one row per (repo, fact, commit)", rows)
	}

	counts, err := repo.Counts(ctx, repoID)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.Total != 1 || counts.Correct != 0 || counts.Incorrect != 1 {
		t.Fatalf("counts = %+v", counts)
	}

	// A done analysis and two more AI facts at its head supply the evidence
	// counters and the confidence distribution.
	insertAIFact(t, pool, repoID, seedHeadCommit,
		"page:/orders", domain.KindPage, domain.ConfidenceMedium, "src/orders.ts")
	insertAIFact(t, pool, repoID, seedHeadCommit,
		"component:src/OrderList.tsx#OrderList", domain.KindComponent, domain.ConfidenceLow,
		"src/OrderList.tsx")
	analysisID := nextSeedID()
	if _, err := pool.Exec(ctx,
		`INSERT INTO code_analysis
			(id, version_id, repo_id, base_commit, head_commit, status, prompt_version, stats, created_by, created_at)
		 VALUES ($1, $2, $3, $4, $5, 'done', 'p4.1', $6::jsonb, 1, now())`,
		analysisID, versionID, repoID, seedBaseCommit, seedHeadCommit,
		`{"evidenceChecks":20,"evidenceFailures":5,"invalidFacts":2,"cacheHits":6,"analyzedFiles":12}`,
	); err != nil {
		t.Fatalf("seed done analysis: %v", err)
	}

	stats, err := repo.QualityStats(ctx, repoID)
	if err != nil {
		t.Fatalf("QualityStats: %v", err)
	}
	if stats.AnalysisID == nil || *stats.AnalysisID != analysisID ||
		stats.AnalysisHeadCommit != seedHeadCommit || stats.AnalyzedAt == nil {
		t.Fatalf("analysis projection = %+v", stats)
	}
	if stats.EvidenceChecks != 20 || stats.EvidenceFailures != 5 || stats.InvalidFacts != 2 ||
		stats.CacheHits != 6 || stats.AnalyzedFiles != 12 {
		t.Fatalf("evidence counters = %+v", stats)
	}
	if stats.ConfidenceHigh != 1 || stats.ConfidenceMedium != 1 || stats.ConfidenceLow != 1 {
		t.Fatalf("confidence distribution = %+v", stats)
	}
	if stats.IndexedCommit != "" {
		t.Fatalf("indexed commit = %q, want empty without an index run", stats.IndexedCommit)
	}

	// No evaluation run yet: the impact block is explicitly unavailable.
	impact, err := repo.LatestImpactMetrics(ctx)
	if err != nil {
		t.Fatalf("LatestImpactMetrics(empty): %v", err)
	}
	if impact.Reason != "no_eval_run" || impact.Precision != nil || impact.Recall != nil {
		t.Fatalf("empty impact = %+v", impact)
	}

	evalRunID := nextSeedID()
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_run (id, mode, corpus_version, model, status, summary, started_at, finished_at)
		 VALUES ($1, 'deterministic', 'v1', '', 'failed', '{}'::jsonb, now(), now())`,
		evalRunID,
	); err != nil {
		t.Fatalf("seed eval run: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_metric (run_id, name, value, threshold, passed, detail)
		 VALUES ($1, 'impact_precision', 0.75, 0.80, false, '{}'::jsonb),
		        ($1, 'impact_recall', 0.80, 0.90, false, '{}'::jsonb)`,
		evalRunID,
	); err != nil {
		t.Fatalf("seed eval metrics: %v", err)
	}

	impact, err = repo.LatestImpactMetrics(ctx)
	if err != nil {
		t.Fatalf("LatestImpactMetrics: %v", err)
	}
	if impact.Reason != "" || impact.Precision == nil || impact.Recall == nil {
		t.Fatalf("impact metrics = %+v", impact)
	}
	if math.Abs(*impact.Precision-0.75) > 1e-9 || math.Abs(*impact.Recall-0.80) > 1e-9 {
		t.Fatalf("impact values = %v/%v, want 0.75/0.80", *impact.Precision, *impact.Recall)
	}
	if impact.EvalRunID == nil || *impact.EvalRunID != evalRunID || impact.EvaluatedAt == nil {
		t.Fatalf("impact evaluation ref = %+v", impact)
	}
}
