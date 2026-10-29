package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// factReviewTestTime is a whole-second UTC timestamp, lossless in TIMESTAMPTZ.
var factReviewTestTime = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

// factReviewFakeRow answers scanFactReview with fixed column values in
// factReviewColumnList order, so the column order stays pinned without a DB.
type factReviewFakeRow struct {
	values []any
}

func (r factReviewFakeRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return errors.New("fact review scan: destination count does not match")
	}
	for index, target := range dest {
		switch typed := target.(type) {
		case *int64:
			*typed = r.values[index].(int64)
		case *string:
			*typed = r.values[index].(string)
		case *time.Time:
			*typed = r.values[index].(time.Time)
		default:
			return errors.New("fact review scan: unsupported destination")
		}
	}
	return nil
}

// TestFactReviewSQLPinsReviewIdentity pins the two write statements: the insert
// is conflict-free on (repo_id, fact_id, commit_sha) and the fallback update
// overwrites the verdict/note/author/time of that same row, which is exactly the
// spec's "same fact/commit is unique, update overrides".
func TestFactReviewSQLPinsReviewIdentity(t *testing.T) {
	for _, needle := range []string{
		"INSERT INTO code_fact_review",
		"ON CONFLICT (repo_id, fact_id, commit_sha) DO NOTHING",
	} {
		if !strings.Contains(insertFactReviewSQL, needle) {
			t.Fatalf("insert SQL does not contain %q: %s", needle, insertFactReviewSQL)
		}
	}
	for _, needle := range []string{
		"UPDATE code_fact_review",
		"SET verdict = $4",
		"reason = $5",
		"created_by = $6",
		"created_at = $7",
		"WHERE repo_id = $1 AND fact_id = $2 AND commit_sha = $3",
	} {
		if !strings.Contains(updateFactReviewSQL, needle) {
			t.Fatalf("update SQL does not contain %q: %s", needle, updateFactReviewSQL)
		}
	}
}

// TestFactLookupSQLScopesRepository pins the 404 ownership check: the fact
// lookup reads code_entity restricted to the path repository.
func TestFactLookupSQLScopesRepository(t *testing.T) {
	for _, needle := range []string{
		"FROM code_entity",
		"repo_id = $1",
		"id = $2",
	} {
		if !strings.Contains(getFactReviewFactSQL, needle) {
			t.Fatalf("fact lookup SQL does not contain %q: %s", needle, getFactReviewFactSQL)
		}
	}
	for _, forbidden := range []string{"INSERT", "UPDATE", "DELETE"} {
		if strings.Contains(getFactReviewFactSQL, forbidden) {
			t.Fatalf("fact lookup SQL must stay read-only: %s", getFactReviewFactSQL)
		}
	}
}

// TestListFactReviewsSQLPinsCommitAndRepo pins the facts-page enrichment query:
// reviews are loaded per (repo, commit) for the requested fact ids only.
func TestListFactReviewsSQLPinsCommitAndRepo(t *testing.T) {
	for _, needle := range []string{
		"FROM code_fact_review",
		"repo_id = $1",
		"commit_sha = $2",
		"fact_id = ANY($3)",
		"ORDER BY fact_id",
	} {
		if !strings.Contains(listFactReviewsSQL, needle) {
			t.Fatalf("list SQL does not contain %q: %s", needle, listFactReviewsSQL)
		}
	}
}

// TestQualitySQLPinsAggregationSources pins which persisted source each metric
// comes from: evidence/invalid facts from the newest done analysis, confidence
// from the head commit's AI facts, reviews from code_fact_review, and precision/
// recall from the newest evaluation run.
func TestQualitySQLPinsAggregationSources(t *testing.T) {
	for _, needle := range []string{
		"FROM code_analysis",
		"repo_id = $1",
		"status = 'done'",
		"ORDER BY created_at DESC, id DESC",
		"LIMIT 1",
	} {
		if !strings.Contains(latestQualityAnalysisSQL, needle) {
			t.Fatalf("latest analysis SQL does not contain %q: %s", needle, latestQualityAnalysisSQL)
		}
	}
	for _, needle := range []string{
		"FROM code_index_run",
		"status = 'done'",
		"ORDER BY created_at DESC, id DESC",
	} {
		if !strings.Contains(latestIndexedCommitSQL, needle) {
			t.Fatalf("latest index SQL does not contain %q: %s", needle, latestIndexedCommitSQL)
		}
	}
	for _, needle := range []string{
		"FROM code_entity",
		"repo_id = $1",
		"commit_sha = $2",
		"source = 'ai'",
		"GROUP BY confidence",
	} {
		if !strings.Contains(factConfidenceSQL, needle) {
			t.Fatalf("confidence SQL does not contain %q: %s", needle, factConfidenceSQL)
		}
	}
	for _, needle := range []string{
		"FROM code_fact_review",
		"repo_id = $1",
		"FILTER (WHERE verdict = 'correct')",
		"FILTER (WHERE verdict = 'incorrect')",
	} {
		if !strings.Contains(factReviewCountsSQL, needle) {
			t.Fatalf("counts SQL does not contain %q: %s", needle, factReviewCountsSQL)
		}
	}
	for _, needle := range []string{"to_regclass('public.eval_run')", "to_regclass('public.eval_metric')"} {
		if !strings.Contains(evalStoreAvailableSQL, needle) {
			t.Fatalf("eval availability SQL does not contain %q: %s", needle, evalStoreAvailableSQL)
		}
	}
	for _, needle := range []string{
		"FROM eval_run",
		"ORDER BY started_at DESC, id DESC",
		"LIMIT 1",
	} {
		if !strings.Contains(latestEvalRunSQL, needle) {
			t.Fatalf("latest eval run SQL does not contain %q: %s", needle, latestEvalRunSQL)
		}
	}
	for _, needle := range []string{
		"FROM eval_metric",
		"run_id = $1",
		"name = ANY($2)",
	} {
		if !strings.Contains(latestEvalImpactMetricsSQL, needle) {
			t.Fatalf("eval metric SQL does not contain %q: %s", needle, latestEvalImpactMetricsSQL)
		}
	}
	if strings.Contains(latestEvalImpactMetricsSQL, "INSERT") ||
		strings.Contains(latestEvalImpactMetricsSQL, "UPDATE") {
		t.Fatalf("eval metric read must stay read-only: %s", latestEvalImpactMetricsSQL)
	}
}

// TestScanFactReviewPinsColumnOrder decodes one row and proves the stored
// decimal fact id is parsed back without a float round-trip.
func TestScanFactReviewPinsColumnOrder(t *testing.T) {
	row := factReviewFakeRow{values: []any{
		int64(9301), int64(7), "1844674407370955201", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		domain.FactReviewIncorrect, "代码已删除该接口", int64(2), factReviewTestTime,
	}}

	review, err := scanFactReview(row)
	if err != nil {
		t.Fatalf("scanFactReview: %v", err)
	}
	if review.ID != 9301 || review.RepoID != 7 || review.FactID != 1844674407370955201 {
		t.Fatalf("ids = %+v", review)
	}
	if review.CommitSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ||
		review.Verdict != domain.FactReviewIncorrect || review.Reason != "代码已删除该接口" ||
		review.CreatedBy != 2 || !review.CreatedAt.Equal(factReviewTestTime) {
		t.Fatalf("review = %+v", review)
	}

	_, err = scanFactReview(factReviewFakeRow{values: []any{
		int64(9302), int64(7), "not-a-decimal-id", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		domain.FactReviewCorrect, "", int64(2), factReviewTestTime,
	}})
	if err == nil {
		t.Fatal("scanFactReview accepted a non-decimal fact id")
	}
}

// TestDecodeAnalysisQualityStats pins the projection of the stored analysis
// stats document: only the four quality counters are read and a malformed
// document degrades to zeros instead of failing the aggregate.
func TestDecodeAnalysisQualityStats(t *testing.T) {
	document := []byte(`{"changedFiles":12,"analyzedFiles":10,"cacheHits":7,` +
		`"evidenceChecks":30,"evidenceFailures":1,"invalidFacts":2}`)

	stats := decodeAnalysisQualityStats(document)
	if stats.EvidenceChecks != 30 || stats.EvidenceFailures != 1 ||
		stats.InvalidFacts != 2 || stats.CacheHits != 7 || stats.AnalyzedFiles != 10 {
		t.Fatalf("decoded stats = %+v", stats)
	}

	if got := decodeAnalysisQualityStats([]byte("{")); got != (analysisQualityStats{}) {
		t.Fatalf("malformed stats decode = %+v, want zeros", got)
	}
	if got := decodeAnalysisQualityStats(nil); got != (analysisQualityStats{}) {
		t.Fatalf("empty stats decode = %+v, want zeros", got)
	}
}

// TestBuildImpactMetrics pins the "latest evaluation" mapping: both metrics
// present makes the block available; a run without the two impact metrics is
// flagged, and never reports a fabricated 0.0.
func TestBuildImpactMetrics(t *testing.T) {
	runID := int64(9201)
	evaluatedAt := factReviewTestTime.Add(-time.Hour)
	available := buildImpactMetrics(&runID, &evaluatedAt, map[string]float64{
		"impact_precision": 0.75,
		"impact_recall":    0.8,
	})
	if available.Reason != "" || available.Precision == nil || *available.Precision != 0.75 ||
		available.Recall == nil || *available.Recall != 0.8 {
		t.Fatalf("available metrics = %+v", available)
	}
	if available.EvalRunID == nil || *available.EvalRunID != runID ||
		available.EvaluatedAt == nil || !available.EvaluatedAt.Equal(evaluatedAt) {
		t.Fatalf("eval ref = %+v", available)
	}

	missing := buildImpactMetrics(&runID, &evaluatedAt, map[string]float64{"impact_recall": 0.8})
	if missing.Precision != nil || missing.Reason != "no_impact_metric" {
		t.Fatalf("partial metrics = %+v, want unavailable without precision", missing)
	}

	noRun := buildImpactMetrics(nil, nil, nil)
	if noRun.Precision != nil || noRun.Recall != nil || noRun.Reason != "no_eval_run" {
		t.Fatalf("absent run = %+v, want unavailable no_eval_run", noRun)
	}
}

// TestApplyConfidenceCount pins the distribution aggregation: only the three
// documented levels are counted and unknown levels are ignored.
func TestApplyConfidenceCount(t *testing.T) {
	var stats domain.RepoQualityStats
	applyConfidenceCount(&stats, domain.ConfidenceHigh, 4)
	applyConfidenceCount(&stats, domain.ConfidenceMedium, 2)
	applyConfidenceCount(&stats, domain.ConfidenceLow, 1)
	applyConfidenceCount(&stats, "unknown", 9)
	if stats.ConfidenceHigh != 4 || stats.ConfidenceMedium != 2 || stats.ConfidenceLow != 1 {
		t.Fatalf("confidence stats = %+v", stats)
	}
}
