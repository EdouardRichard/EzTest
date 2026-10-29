//go:build integration

package chain_test

// This file exercises the codeintel-side chain provider against the real
// PostgreSQL schema: the impact candidates of the newest P4 analysis, filtered
// to selected or high/medium-confidence rows (spec §5.2 query 10). It is skipped
// unless EZTEST_TEST_DATABASE_URL points at a dedicated *_test database
// (testutil.OpenTestDB enforces that).

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	codeintelchain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/chain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// codeintelChainSeedVersion inserts one version row.
func codeintelChainSeedVersion(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, name, status, revision, created_by, updated_by)
		VALUES ($1, 'codeintel-chain-fixture', 'executing', 1, 1, 1)`, id)
	if err != nil {
		t.Fatalf("seed version %d: %v", id, err)
	}
}

// codeintelChainSeedAnalysis inserts one done analysis with a unique commit
// range.
func codeintelChainSeedAnalysis(t *testing.T, pool *pgxpool.Pool, id, versionID int64, seed byte, createdAt time.Time) {
	t.Helper()
	base := strings.Repeat(string(seed), 40)
	head := strings.Repeat(string(seed+1), 40)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO code_analysis
			(id, version_id, repo_id, base_commit, head_commit, status, prompt_version,
			 created_by, created_at)
		VALUES ($1, $2, 1, $3, $4, 'done', 'chain-fixture', 1, $5)`,
		id, versionID, base, head, createdAt)
	if err != nil {
		t.Fatalf("seed analysis %d: %v", id, err)
	}
}

// codeintelChainSeedCandidate inserts one impact candidate.
func codeintelChainSeedCandidate(t *testing.T, pool *pgxpool.Pool, id, analysisID int64, subjectKind string, subjectID int64, reason, confidence string, selected bool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO code_impact_candidate
			(id, analysis_id, subject_kind, subject_id, reason, confidence, source, selected)
		VALUES ($1, $2, $3, $4, $5, $6, 'direct', $7)`,
		id, analysisID, subjectKind, subjectID, reason, confidence, selected)
	if err != nil {
		t.Fatalf("seed impact candidate %d: %v", id, err)
	}
}

// TestCodeIntelChainReadsLatestAnalysisImpact pins the impact read: only the
// newest analysis of the version is considered, its candidates are filtered by
// the selected-or-high/medium rule, and a version without an analysis degrades
// to an empty slice.
func TestCodeIntelChainReadsLatestAnalysisImpact(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	const (
		versionID       = int64(601)
		otherVersion    = int64(602)
		noAnalysisID    = int64(603)
		oldAnalysisID   = int64(6101)
		latestAnalysis  = int64(6102)
		foreignAnalysis = int64(6103)
	)
	codeintelChainSeedVersion(t, pool, versionID)
	codeintelChainSeedVersion(t, pool, otherVersion)
	codeintelChainSeedVersion(t, pool, noAnalysisID)
	codeintelChainSeedAnalysis(t, pool, oldAnalysisID, versionID, '1',
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	codeintelChainSeedAnalysis(t, pool, latestAnalysis, versionID, '3',
		time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	codeintelChainSeedAnalysis(t, pool, foreignAnalysis, otherVersion, '5',
		time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))

	// The older analysis' candidate must be ignored entirely.
	codeintelChainSeedCandidate(t, pool, 6201, oldAnalysisID, "requirement", 100, "old analysis", "high", true)
	// Latest analysis: selected, medium-confidence, selected-low and a
	// non-selected low candidate that only the service-level rule may drop.
	codeintelChainSeedCandidate(t, pool, 6202, latestAnalysis, "requirement", 200, "new analysis", "high", true)
	codeintelChainSeedCandidate(t, pool, 6203, latestAnalysis, "app_model", 77, "medium app model", "medium", false)
	codeintelChainSeedCandidate(t, pool, 6204, latestAnalysis, "requirement", 400, "selected low", "low", true)
	codeintelChainSeedCandidate(t, pool, 6205, latestAnalysis, "requirement", 500, "unselected low", "low", false)
	// Another version's analysis is invisible to this version.
	codeintelChainSeedCandidate(t, pool, 6206, foreignAnalysis, "requirement", 600, "foreign", "high", true)

	reads := codeintelchain.New(pool)
	impact, err := reads.Impact(ctx, versionID)
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	want := []chainapp.ImpactRow{
		{SubjectKind: "requirement", SubjectID: 200, Reason: "new analysis", Confidence: "high", Selected: true},
		{SubjectKind: "app_model", SubjectID: 77, Reason: "medium app model", Confidence: "medium", Selected: false},
		{SubjectKind: "requirement", SubjectID: 400, Reason: "selected low", Confidence: "low", Selected: true},
	}
	if !reflect.DeepEqual(impact, want) {
		t.Fatalf("impact = %+v, want %+v", impact, want)
	}

	empty, err := reads.Impact(ctx, noAnalysisID)
	if err != nil {
		t.Fatalf("Impact(no analysis): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Impact(no analysis) = %+v, want an empty layer", empty)
	}
}
