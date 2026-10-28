package application_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// fakeSupplementBatchReader is the in-memory SupplementBatchReader of the run
// gate and plan tests.
type fakeSupplementBatchReader struct {
	batches map[int64]application.SupplementBatch
	latest  map[int64]application.SupplementBatch
}

func newFakeSupplementBatchReader(batch application.SupplementBatch) fakeSupplementBatchReader {
	return fakeSupplementBatchReader{
		batches: map[int64]application.SupplementBatch{batch.ID: batch},
		latest:  map[int64]application.SupplementBatch{batch.VersionID: batch},
	}
}

func (f fakeSupplementBatchReader) GetBatch(_ context.Context, batchID int64) (application.SupplementBatch, error) {
	batch, ok := f.batches[batchID]
	if !ok {
		return application.SupplementBatch{}, application.ErrSupplementBatchNotFound
	}
	return batch, nil
}

func (f fakeSupplementBatchReader) LatestTerminalBatch(_ context.Context, versionID int64) (application.SupplementBatch, error) {
	batch, ok := f.latest[versionID]
	if !ok {
		return application.SupplementBatch{}, application.ErrSupplementBatchNotFound
	}
	return batch, nil
}

// terminalParentBatch is the latest terminal parent of the fixture version.
func terminalParentBatch() application.SupplementBatch {
	return application.SupplementBatch{
		ID:        77,
		VersionID: runTestVersionID,
		Kind:      "baseline",
		Status:    "done",
	}
}

// runStatsWarningCodes returns the warning codes recorded in the run stats.
func runStatsWarningCodes(t *testing.T, run domain.CaseDesignRun) []string {
	t.Helper()
	if len(run.Stats) == 0 {
		return nil
	}
	var stats struct {
		Warnings []application.IncrementalPlanWarning `json:"warnings"`
	}
	if err := json.Unmarshal(run.Stats, &stats); err != nil {
		t.Fatalf("decode run stats %q: %v", string(run.Stats), err)
	}
	codes := make([]string, 0, len(stats.Warnings))
	for _, warning := range stats.Warnings {
		codes = append(codes, warning.Code)
	}
	return codes
}

func hasStatsWarning(t *testing.T, run domain.CaseDesignRun, code string) bool {
	t.Helper()
	for _, got := range runStatsWarningCodes(t, run) {
		if got == code {
			return true
		}
	}
	return false
}

// TestStartSupplementRunValidatesParentBatch covers the spec §7.6 parent-batch
// preconditions when the optional execution-batch reader is wired: the parent
// must be a terminal batch of the version and the latest terminal one.
func TestStartSupplementRunValidatesParentBatch(t *testing.T) {
	ctx := context.Background()
	t.Run("terminal latest parent starts", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.batches = newFakeSupplementBatchReader(terminalParentBatch())
		run, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if codes := runStatsWarningCodes(t, run); len(codes) != 0 {
			t.Fatalf("run warnings = %v, want none for a verified parent", codes)
		}
	})
	t.Run("non-terminal parent is rejected", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		parent := terminalParentBatch()
		parent.Status = "running"
		fixture.batches = newFakeSupplementBatchReader(parent)
		_, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
	})
	t.Run("foreign parent is rejected", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		parent := terminalParentBatch()
		parent.VersionID = 999
		fixture.batches = fakeSupplementBatchReader{
			batches: map[int64]application.SupplementBatch{parent.ID: parent},
			latest:  map[int64]application.SupplementBatch{runTestVersionID: terminalParentBatch()},
		}
		_, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		requireDomainError(t, err, domain.KindInvalid, "invalid_batch_id")
	})
	t.Run("missing parent is rejected", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.batches = fakeSupplementBatchReader{}
		_, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		requireDomainError(t, err, domain.KindInvalid, "invalid_batch_id")
	})
	t.Run("stale parent is rejected", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		parent := terminalParentBatch()
		latest := terminalParentBatch()
		latest.ID = 78
		fixture.batches = fakeSupplementBatchReader{
			batches: map[int64]application.SupplementBatch{parent.ID: parent},
			latest:  map[int64]application.SupplementBatch{runTestVersionID: latest},
		}
		_, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
	})
	t.Run("incremental_design validates the previous terminal parent", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		fixture.batches = newFakeSupplementBatchReader(terminalParentBatch())
		if _, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindIncrementalDesign, BatchID: 77}); err != nil {
			t.Fatalf("start incremental_design: %v", err)
		}
	})
	t.Run("incremental_design without reader degrades with a warning", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		run, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindIncrementalDesign, BatchID: 77})
		if err != nil {
			t.Fatalf("start without reader: %v", err)
		}
		if !hasStatsWarning(t, run, "batch_precondition_unavailable") {
			t.Fatalf("run stats = %s, want batch_precondition_unavailable", run.Stats)
		}
	})
	t.Run("no reader degrades with a warning", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		run, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if !hasStatsWarning(t, run, "batch_precondition_unavailable") {
			t.Fatalf("run stats = %s, want batch_precondition_unavailable", run.Stats)
		}
	})
}

// TestIncrementalPlanValidatesParentBatch covers the plan side of the optional
// batch reader: a foreign/non-terminal parent is a warning, a missing reader
// degrades with a warning, and a verified terminal parent adds none.
func TestIncrementalPlanValidatesParentBatch(t *testing.T) {
	t.Run("verified terminal parent", func(t *testing.T) {
		fixture, _ := newIncrementalPlanFixture(t)
		fixture.batches = newFakeSupplementBatchReader(terminalParentBatch())
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if containsPlanWarning(view.Warnings, "invalid_parent_batch") ||
			containsPlanWarning(view.Warnings, "batch_precondition_unavailable") {
			t.Fatalf("warnings = %+v, want no batch precondition warning", view.Warnings)
		}
	})
	t.Run("non-terminal parent warns", func(t *testing.T) {
		fixture, _ := newIncrementalPlanFixture(t)
		parent := terminalParentBatch()
		parent.Status = "running"
		fixture.batches = newFakeSupplementBatchReader(parent)
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if !containsPlanWarning(view.Warnings, "invalid_parent_batch") {
			t.Fatalf("warnings = %+v, want invalid_parent_batch", view.Warnings)
		}
	})
	t.Run("foreign parent warns", func(t *testing.T) {
		fixture, _ := newIncrementalPlanFixture(t)
		parent := terminalParentBatch()
		parent.VersionID = 999
		fixture.batches = fakeSupplementBatchReader{
			batches: map[int64]application.SupplementBatch{parent.ID: parent},
			latest:  map[int64]application.SupplementBatch{runTestVersionID: terminalParentBatch()},
		}
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if !containsPlanWarning(view.Warnings, "invalid_parent_batch") {
			t.Fatalf("warnings = %+v, want invalid_parent_batch", view.Warnings)
		}
	})
	t.Run("missing reader degrades with a warning", func(t *testing.T) {
		fixture, _ := newIncrementalPlanFixture(t)
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if !containsPlanWarning(view.Warnings, "batch_precondition_unavailable") {
			t.Fatalf("warnings = %+v, want batch_precondition_unavailable", view.Warnings)
		}
	})
}
