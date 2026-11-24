//go:build integration

package executionpg

import (
	"context"
	"errors"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// newResolutionCaseRun creates the intent/wave/case_run anchor a sut_resolution
// fact references (case_run_id is a real within-context FK).
func newResolutionCaseRun(t *testing.T, repo *Repository, caseRunID int64) {
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
		DispatchToken: "resolution-test",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := repo.CaseRuns.Create(ctx, run); err != nil {
		t.Fatalf("create case_run %d: %v", caseRunID, err)
	}
}

// TestSUTResolutionAppendListRoundTrip pins the append-only fact store against a
// real migrated schema: the BIGINT[] id arrays round-trip, a re-resolution
// appends a second row (latest wins, no rewrite), and List returns append order.
func TestSUTResolutionAppendListRoundTrip(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	newResolutionCaseRun(t, repo, 9301)

	first := execution.SUTResolution{
		CaseRunID:        9301,
		SUTIDs:           []int64{9001},
		ResolutionMethod: "catalog_hit",
		TouchedPageIDs:   []int64{101, 102},
		TouchedAPIIDs:    []int64{201},
		Ambiguous:        true,
	}
	written, err := repo.SUTResolutions.AppendSUTResolution(ctx, first)
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	if written.ID <= 0 || written.CreatedAt.IsZero() {
		t.Fatalf("append must assign id and created_at: %+v", written)
	}

	// A second resolution of the same run appends (append-only, latest wins).
	second := execution.SUTResolution{
		CaseRunID:        9301,
		SUTIDs:           []int64{9002},
		ResolutionMethod: "exploration",
	}
	if _, err := repo.SUTResolutions.AppendSUTResolution(ctx, second); err != nil {
		t.Fatalf("append second: %v", err)
	}

	rows, err := repo.SUTResolutions.ListSUTResolutions(ctx, 9301)
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
	if got.ResolutionMethod != "catalog_hit" || len(got.SUTIDs) != 1 || got.SUTIDs[0] != 9001 ||
		len(got.TouchedPageIDs) != 2 || len(got.TouchedAPIIDs) != 1 {
		t.Fatalf("first fact round-trip = %+v", got)
	}
	if !got.Ambiguous {
		t.Fatal("the Ambiguous flag must round-trip (true)")
	}
	if got.TouchedPageIDs[0] != 101 || got.TouchedPageIDs[1] != 102 || got.TouchedAPIIDs[0] != 201 {
		t.Fatalf("touched asset ids round-trip = %v / %v", got.TouchedPageIDs, got.TouchedAPIIDs)
	}
	// The latest fact normalizes nil arrays to empty (never nil).
	latest := rows[1]
	if latest.TouchedPageIDs == nil || latest.TouchedAPIIDs == nil || latest.SUTIDs == nil {
		t.Fatalf("latest fact arrays must be non-nil: %+v", latest)
	}
	if len(latest.TouchedPageIDs) != 0 || len(latest.TouchedAPIIDs) != 0 {
		t.Fatalf("exploration fact has no touched assets: %+v", latest)
	}
	if latest.Ambiguous {
		t.Fatal("the Ambiguous flag must round-trip (false default)")
	}
}

// TestSUTResolutionCaseRunFK pins the within-context real FK: a fact referencing a
// non-existent case_run is rejected (ErrNotFound via the FK violation mapping).
func TestSUTResolutionCaseRunFK(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	_, err := repo.SUTResolutions.AppendSUTResolution(ctx, execution.SUTResolution{
		CaseRunID:        999999, // no such case_run
		SUTIDs:           []int64{9001},
		ResolutionMethod: "catalog_hit",
	})
	if !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("missing case_run FK must map to ErrNotFound, got %v", err)
	}
}

// TestSUTResolutionEmptyList: a run with no resolution returns an empty (non-nil)
// slice, not an error.
func TestSUTResolutionEmptyList(t *testing.T) {
	repo, _ := newTestRepository(t)
	rows, err := repo.SUTResolutions.ListSUTResolutions(context.Background(), 424242)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
}
