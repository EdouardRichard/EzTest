package visualapp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// TestW2VisualThresholdAndFourStates is the W2-10 acceptance regression: a
// 0.02 threshold is persisted and used by every comparison, the admin baseline
// history supersedes the previous approval, and the four real classifications
// are stored on visual_diff (blocked included, even though a hard executor
// failure has no actual bytes and therefore no production diff row).
func TestW2VisualThresholdAndFourStates(t *testing.T) {
	ctx := context.Background()
	config, assertions, _ := newConfigFixture(t)

	threshold := 0.02
	viewport := &visual.Viewport{Width: 100, Height: 100}
	assertion, err := config.Upsert(ctx, 4001, 9001, 77, visual.ConfigInput{
		Threshold: &threshold, Viewport: viewport,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if assertion.Threshold != 0.02 {
		t.Fatalf("threshold = %v, want 0.02", assertion.Threshold)
	}
	if assertion.ViewportWidth != 100 || assertion.ViewportHeight != 100 {
		t.Fatalf("viewport = %dx%d", assertion.ViewportWidth, assertion.ViewportHeight)
	}

	pngBytes := testPNG(t, 100, 100)
	sutID := int64(5001)
	baselines := &fakeBaselineRepo{}
	events := &fakeEvents{}
	baselineService := visualapp.NewBaselineService(visualapp.BaselineServiceDeps{
		Baselines: baselines, Assertions: assertions,
		Evidence: &fakeEvidenceReader{evidence: visualapp.VisualEvidence{
			ID: 7001, CaseRunID: 3001, CaseVersionID: 8001, VersionSUTID: &sutID,
			Kind: "visual_actual", Data: pngBytes, Width: 100, Height: 100,
			ConfigRevision: assertion.ConfigRevision,
		}},
		Settings: &fakeSettings{ints: map[string]int64{"visual.baseline_max_bytes": 5 << 20}},
		Clock:    &fakeClock{now: time.Date(2026, 11, 6, 5, 0, 0, 0, time.UTC)},
		IDs:      &fakeIDs{next: 200}, Audit: &fakeAudit{}, Events: events,
	})

	first, err := baselineService.Approve(ctx, 4001, assertion.ID, 7001, 9)
	if err != nil {
		t.Fatalf("Approve first baseline: %v", err)
	}
	if first.Status != visual.BaselineApproved {
		t.Fatalf("baseline = %+v", first)
	}
	if first.ConfigRevision != assertion.ConfigRevision {
		t.Fatalf("baseline config revision = %d, want %d", first.ConfigRevision, assertion.ConfigRevision)
	}
	second, err := baselineService.Approve(ctx, 4001, assertion.ID, 7001, 10)
	if err != nil {
		t.Fatalf("Approve second baseline: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("the second approval reused the first baseline id")
	}
	history, err := baselineService.List(ctx, 4001, assertion.ID)
	if err != nil {
		t.Fatalf("List baselines: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("baseline history = %d, want >= 2", len(history))
	}
	byID := map[int64]visual.Baseline{}
	for _, row := range history {
		byID[row.ID] = row
	}
	if row := byID[second.ID]; row.Status != visual.BaselineApproved {
		t.Fatalf("newest baseline = %+v", row)
	}
	if row := byID[first.ID]; row.Status != visual.BaselineSuperseded {
		t.Fatalf("superseded baseline = %+v", row)
	}
	if baselines.superseded != 1 {
		t.Fatalf("superseded count = %d", baselines.superseded)
	}
	if len(events.events) != 2 {
		t.Fatalf("visual.baseline_approved events = %d, want 2", len(events.events))
	}

	diffs := newFakeDiffRepo()
	diffService := visualapp.NewDiffService(visualapp.DiffServiceDeps{
		Diffs: diffs, Assertions: assertions,
		Clock: &fakeClock{now: time.Date(2026, 11, 6, 5, 5, 0, 0, time.UTC)},
		IDs:   &fakeIDs{next: 300}, Audit: &fakeAudit{}, Events: &fakeEvents{},
	})
	states := []visual.Classification{
		visual.ClassificationPassed, visual.ClassificationFailed,
		visual.ClassificationInconclusive, visual.ClassificationBlocked,
	}
	for index, state := range states {
		baselineID := second.ID
		stored, recordErr := diffService.Record(ctx, visual.RecordDiffInput{
			VersionID: 4001, VisualAssertionID: assertion.ID, CaseRunID: 3001,
			AssertionResultID: int64(2001 + index), BaselineID: &baselineID,
			ActualEvidenceID: 7001, DiffRatio: 0.01, Threshold: assertion.Threshold,
			Classification: state, Reason: "w2_four_states", EnvironmentMatch: true,
			Width: 100, Height: 100, MismatchBoxes: json.RawMessage(`[]`),
		})
		if recordErr != nil {
			t.Fatalf("Record %s: %v", state, recordErr)
		}
		if stored.Classification != state {
			t.Fatalf("classification = %s, want %s", stored.Classification, state)
		}
		if stored.Threshold != 0.02 {
			t.Fatalf("%s threshold = %v, want 0.02", state, stored.Threshold)
		}
	}
	list, _, err := diffService.List(ctx, 4001, visual.DiffFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List diffs: %v", err)
	}
	if len(list) != len(states) {
		t.Fatalf("diff rows = %d, want %d", len(list), len(states))
	}
}
