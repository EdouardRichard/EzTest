//go:build integration

package executionpg

import (
	"context"
	"errors"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// newAssetTestCaseRun creates the intent/wave/case_run anchor an evidence row
// references (evidence.case_run_id is a real within-context FK). Self-contained
// (uses the stable repo_integration_test helpers) so it does not depend on any
// sibling test file's helpers.
func newAssetTestCaseRun(t *testing.T, repo *Repository, caseRunID int64) {
	t.Helper()
	ctx := context.Background()
	intentID := caseRunID + 100000
	waveID := caseRunID + 200000
	mustCreateIntent(t, repo, intentID, 100, 300, testFingerprint, execution.PurposeManual)
	mustCreateWave(t, repo, waveID, 100)
	run := execution.CaseRun{
		ID:            caseRunID,
		WaveID:        waveID,
		IntentID:      intentID,
		CaseVersionID: 300,
		Status:        execution.CaseRunPending,
		Attempts:      1,
		DispatchEpoch: 1,
		DispatchToken: "asset-test",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := repo.CaseRuns.Create(ctx, run); err != nil {
		t.Fatalf("create case_run %d: %v", caseRunID, err)
	}
}

// TestAssetRegistrationAppendListRoundTrip pins the append-only P17 fact store
// against a real migrated schema: the JSONB evidence_refs array round-trips, a
// re-registration appends a second row (latest wins, no rewrite), List returns
// append order, and the source/gate_result/asset ids survive. sut_id/asset_id are
// logical BIGINT (no cross-context FK), so no resource anchor is needed.
func TestAssetRegistrationAppendListRoundTrip(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	first := execution.AssetRegistration{
		SUTID:        9001,
		AssetKind:    execution.AssetKindPage,
		AssetID:      7001,
		Source:       execution.AssetSourceAI,
		EvidenceRefs: []string{"sha-proof-1", "sha-proof-2"},
		GateResult:   "approved",
	}
	written, err := repo.AssetRegistrations.AppendAssetRegistration(ctx, first)
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	if written.ID <= 0 || written.CreatedAt.IsZero() {
		t.Fatalf("append must assign id and created_at: %+v", written)
	}

	// A second registration of the same SUT appends (append-only, latest wins).
	second := execution.AssetRegistration{
		SUTID:      9001,
		AssetKind:  execution.AssetKindAPI,
		AssetID:    7002,
		Source:     execution.AssetSourceAI,
		GateResult: "approved",
	}
	if _, err := repo.AssetRegistrations.AppendAssetRegistration(ctx, second); err != nil {
		t.Fatalf("append second: %v", err)
	}

	rows, err := repo.AssetRegistrations.ListAssetRegistrations(ctx, 9001)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (append-only, no rewrite)", len(rows))
	}
	if rows[0].ID >= rows[1].ID {
		t.Fatalf("list must be append order (id asc): %d then %d", rows[0].ID, rows[1].ID)
	}
	got := rows[0]
	if got.AssetKind != execution.AssetKindPage || got.AssetID != 7001 || got.Source != execution.AssetSourceAI || got.GateResult != "approved" {
		t.Fatalf("first fact round-trip = %+v", got)
	}
	if len(got.EvidenceRefs) != 2 || got.EvidenceRefs[0] != "sha-proof-1" || got.EvidenceRefs[1] != "sha-proof-2" {
		t.Fatalf("evidence_refs JSONB round-trip = %v", got.EvidenceRefs)
	}
	// The latest fact normalizes a nil evidence slice to empty (never nil).
	latest := rows[1]
	if latest.EvidenceRefs == nil || len(latest.EvidenceRefs) != 0 {
		t.Fatalf("latest fact evidence_refs must be non-nil empty: %+v", latest)
	}
}

// TestAssetRegistrationEmptyList: a SUT with no registration returns an empty
// (non-nil) slice, not an error.
func TestAssetRegistrationEmptyList(t *testing.T) {
	repo, _ := newTestRepository(t)
	rows, err := repo.AssetRegistrations.ListAssetRegistrations(context.Background(), 424242)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
}

// TestEvidenceByCaseRunSHA pins the P17 gate's evidence lookup against a real
// schema: a case_run's evidence is found by its content sha256, a missing sha and
// a foreign case_run both map to ErrNotFound (the gate's fail-closed no-evidence
// branch).
func TestEvidenceByCaseRunSHA(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	newAssetTestCaseRun(t, repo, 9401)
	step, assertion := 1, 1
	sutID := int64(9001)
	const sha = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if err := repo.Evidence.Insert(ctx, execution.Evidence{
		ID: 940101, CaseRunID: 9401, SUTID: &sutID, StepSeq: &step, AssertionSeq: &assertion,
		Kind: execution.EvidenceScreenshot, ContentRefID: 1, SHA256: sha, SizeBytes: 0,
	}); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}

	found, err := repo.Evidence.EvidenceByCaseRunSHA(ctx, 9401, sha)
	if err != nil {
		t.Fatalf("lookup by sha: %v", err)
	}
	if found.ID != 940101 || found.SHA256 != sha || found.StepSeq == nil || *found.StepSeq != 1 || found.AssertionSeq == nil || *found.AssertionSeq != 1 {
		t.Fatalf("evidence round-trip = %+v", found)
	}

	if _, err := repo.Evidence.EvidenceByCaseRunSHA(ctx, 9401, "no-such-sha"); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("missing sha must map to ErrNotFound, got %v", err)
	}
	// The lookup is scoped to the case_run: the same sha under a different run is
	// not found (the gate cannot borrow another run's evidence).
	if _, err := repo.Evidence.EvidenceByCaseRunSHA(ctx, 9402, sha); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("foreign case_run must map to ErrNotFound, got %v", err)
	}
}
