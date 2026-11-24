//go:build integration

package executionpg

import (
	"context"
	"strings"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestEvidenceNarrowReads pins the v0.2.4-G T8 fix-round evidence reads (#4):
// SHA256sByCaseRun returns the FULL sha set (narrow column read) for the
// causally-complete fingerprint digest; ListNewestByCaseRun returns only the
// bounded newest display rows (limit 0 = unlimited), never clamped to the 100
// page bound.
func TestEvidenceNarrowReads(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateWave(t, repo, 50, 100)
	mustCreateCaseRun(t, repo, 500, 50, 1, 300)
	// A second run without evidence: case_run is unique on (wave_id, intent_id),
	// so it needs its own intent.
	mustCreateIntent(t, repo, 2, 100, 301, testFingerprint, execution.PurposeInitial)
	mustCreateCaseRun(t, repo, 501, 50, 2, 301)

	now := time.Now().UTC()
	shas := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
	for i, sha := range shas {
		ev := execution.Evidence{
			ID: int64(700 + i), CaseRunID: 500, Kind: execution.EvidenceScreenshot,
			ContentRefID: int64(42 + i), SHA256: sha, SizeBytes: 100, MIME: "image/png", CreatedAt: now,
		}
		if err := repo.Evidence.Insert(ctx, ev); err != nil {
			t.Fatalf("insert evidence %d: %v", ev.ID, err)
		}
	}

	// Full sha carrier, newest first (id DESC): 702, 701, 700.
	got, err := repo.Evidence.SHA256sByCaseRun(ctx, 500)
	if err != nil {
		t.Fatalf("SHA256sByCaseRun: %v", err)
	}
	want := []string{shas[2], shas[1], shas[0]}
	if len(got) != len(want) {
		t.Fatalf("sha carrier = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sha carrier[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Bounded display read: limit 2 -> the two newest rows.
	display, err := repo.Evidence.ListNewestByCaseRun(ctx, 500, 2)
	if err != nil {
		t.Fatalf("ListNewestByCaseRun(2): %v", err)
	}
	if len(display) != 2 || display[0].ID != 702 || display[1].ID != 701 {
		t.Fatalf("display(2) = %+v, want the two newest rows 702/701", display)
	}

	// limit 0 = unlimited (NOT clamped to a page bound): all three rows.
	all, err := repo.Evidence.ListNewestByCaseRun(ctx, 500, 0)
	if err != nil {
		t.Fatalf("ListNewestByCaseRun(0): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("display(0) = %d rows, want all 3 (unlimited)", len(all))
	}

	// A run without evidence: non-nil empty carrier, empty display.
	empty, err := repo.Evidence.SHA256sByCaseRun(ctx, 501)
	if err != nil {
		t.Fatalf("SHA256sByCaseRun(empty): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty carrier = %v, want non-nil empty", empty)
	}
}
