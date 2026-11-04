package adapters

import (
	"context"
	"reflect"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// cancelledWaveSupersedingResults adds the QA release REL-Q2 shape to the read
// model fixture: a cancelled retest wave whose case_runs superseded the earlier
// results (release run IDs 376154856219480065..67). The superseded rows are
// newer than the passing run of case version 3002, so a newest-row-wins
// projection erases that pass and drops the report coverage (1/3 instead of
// 2/3 in the release evidence).
func cancelledWaveSupersedingResults(deps Deps) {
	waves := deps.Waves.(*fakeWaves)
	waves.waves = append(waves.waves, execution.RunWave{
		ID: 13, VersionID: 7, Status: execution.WaveCancelled, Trigger: execution.WaveTriggerManual,
		Counts:    execution.WaveCounts{Total: 2, Superseded: 2, ByVersionSUT: map[string]execution.SUTCounts{}},
		StartedAt: &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
	})
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs = append(runs.runs,
		execution.CaseRun{
			ID: 5013, WaveID: 13, CaseVersionID: 3001, Status: execution.CaseRunSuperseded,
			Summary:   execution.CaseRunSummary{TerminalCause: execution.TerminalCauseCaseVersionSuperseded},
			CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
		execution.CaseRun{
			ID: 5014, WaveID: 13, CaseVersionID: 3002, Status: execution.CaseRunSuperseded,
			Summary:   execution.CaseRunSummary{TerminalCause: execution.TerminalCauseCaseVersionSuperseded},
			CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
	)
}

// TestSupersededRunDoesNotEraseCoverage reproduces the QA release REL-Q2
// regression: after a cancelled wave retires its case_runs as superseded, the
// report coverage must keep counting the prior passing result instead of
// treating the newer superseded row as the case result. A superseded row is a
// process state, never a verdict; a case whose only run is superseded was not
// executed and is reported as not run, not as a failed coverage.
func TestSupersededRunDoesNotEraseCoverage(t *testing.T) {
	ctx := context.Background()

	t.Run("prior pass survives a newer superseded run", func(t *testing.T) {
		deps, _ := newReadModelFixture()
		cancelledWaveSupersedingResults(deps)
		snapshot, err := NewReportDataReader(deps).VersionSnapshot(ctx, 7)
		if err != nil {
			t.Fatalf("VersionSnapshot: %v", err)
		}
		coverage := snapshot.Coverage
		// Case 3002 passed in wave 11 and stays covered despite the newer
		// superseded run 5014. Case 3001's latest real result is still the failed
		// run 5001, so it stays a failed-coverage gap; the superseded row must not
		// add a second gap.
		if coverage.RequirementsTotal != 1 || coverage.RequirementsCovered != 1 ||
			coverage.CaseVersionsTotal != 2 || coverage.CaseVersionsCovered != 1 {
			t.Fatalf("coverage = %+v, want 1/1 requirements and 1/2 case versions after the cancelled wave", coverage)
		}
		wantGaps := []delivery.CoverageGapSnapshot{
			{RequirementID: 101, CaseVersionID: 3001, Reason: "execution_failed_coverage"},
		}
		if !reflect.DeepEqual(coverage.Gaps, wantGaps) {
			t.Fatalf("gaps = %+v, want only the genuinely failed case %+v", coverage.Gaps, wantGaps)
		}
		wantRows := []delivery.RequirementCoverageSnapshot{{
			RequirementID:              101,
			CaseVersionsTotal:          2,
			CaseVersionsCovered:        1,
			CaseVersionsFailedCoverage: 1,
		}}
		if !reflect.DeepEqual(coverage.ByRequirement, wantRows) {
			t.Fatalf("per-requirement coverage = %+v, want %+v", coverage.ByRequirement, wantRows)
		}
	})

	t.Run("case with only a superseded run is not run", func(t *testing.T) {
		deps, _ := newReadModelFixture()
		design := deps.Design.(*fakeDesign)
		design.cases[7] = append(design.cases[7], designapp.ExecutableCaseView{
			CaseID: 203, CaseVersionID: 3010, RequirementID: 101, RequirementVersionID: 1001,
			Title: "Case C", Priority: "P2",
		})
		cancelledWaveSupersedingResults(deps)
		runs := deps.CaseRuns.(*fakeCaseRuns)
		runs.runs = append(runs.runs, execution.CaseRun{
			ID: 5015, WaveID: 13, CaseVersionID: 3010, Status: execution.CaseRunSuperseded,
			Summary:   execution.CaseRunSummary{TerminalCause: execution.TerminalCauseCaseVersionSuperseded},
			CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		})
		snapshot, err := NewReportDataReader(deps).VersionSnapshot(ctx, 7)
		if err != nil {
			t.Fatalf("VersionSnapshot: %v", err)
		}
		coverage := snapshot.Coverage
		if coverage.CaseVersionsTotal != 3 || coverage.CaseVersionsCovered != 1 {
			t.Fatalf("coverage = %+v, want 1 of 3 case versions covered", coverage)
		}
		wantGaps := []delivery.CoverageGapSnapshot{
			{RequirementID: 101, CaseVersionID: 3001, Reason: "execution_failed_coverage"},
			{RequirementID: 101, CaseVersionID: 3010, Reason: "execution_not_run"},
		}
		if !reflect.DeepEqual(coverage.Gaps, wantGaps) {
			t.Fatalf("gaps = %+v, want the failed case and the never-run case %+v", coverage.Gaps, wantGaps)
		}
		wantRows := []delivery.RequirementCoverageSnapshot{{
			RequirementID:              101,
			CaseVersionsTotal:          3,
			CaseVersionsCovered:        1,
			CaseVersionsNotRun:         1,
			CaseVersionsFailedCoverage: 1,
		}}
		if !reflect.DeepEqual(coverage.ByRequirement, wantRows) {
			t.Fatalf("per-requirement coverage = %+v, want %+v", coverage.ByRequirement, wantRows)
		}
	})
}

// TestCoverageSequenceStable pins the report sequence of the QA release
// evidence: the coverage published before a cancelled wave must not regress
// when a report is generated after it, because superseded process rows carry no
// verdict. Every wave report of the version exposes the same stable projection,
// while a genuinely new passing run still moves coverage forward.
func TestCoverageSequenceStable(t *testing.T) {
	ctx := context.Background()
	deps, _ := newReadModelFixture()
	reader := NewReportDataReader(deps)

	before, err := reader.VersionSnapshot(ctx, 7)
	if err != nil {
		t.Fatalf("VersionSnapshot before cancel: %v", err)
	}
	if before.Coverage.CaseVersionsCovered != 1 || before.Coverage.RequirementsCovered != 1 {
		t.Fatalf("baseline coverage = %+v, want 1 covered case", before.Coverage)
	}

	cancelledWaveSupersedingResults(deps)

	after, err := reader.VersionSnapshot(ctx, 7)
	if err != nil {
		t.Fatalf("VersionSnapshot after cancel: %v", err)
	}
	if !reflect.DeepEqual(after.Coverage, before.Coverage) {
		t.Fatalf("coverage changed across a cancelled wave: before=%+v after=%+v", before.Coverage, after.Coverage)
	}
	cancelledWaveReport, err := reader.WaveSnapshot(ctx, 7, 13)
	if err != nil {
		t.Fatalf("WaveSnapshot(cancelled): %v", err)
	}
	if !reflect.DeepEqual(cancelledWaveReport.Coverage, before.Coverage) {
		t.Fatalf("cancelled wave coverage = %+v, want the version projection %+v",
			cancelledWaveReport.Coverage, before.Coverage)
	}

	// The sequence is stable against process rows, not frozen against real
	// results: a fresh passed run of case 3001 closes its failed-coverage gap.
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs = append(runs.runs, execution.CaseRun{
		ID: 5020, WaveID: 12, CaseVersionID: 3001, Status: execution.CaseRunPassed,
		Summary:   execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1},
		StartedAt: &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
	})
	next, err := reader.VersionSnapshot(ctx, 7)
	if err != nil {
		t.Fatalf("VersionSnapshot after fresh pass: %v", err)
	}
	if next.Coverage.CaseVersionsCovered != 2 || next.Coverage.RequirementsCovered != 1 ||
		len(next.Coverage.Gaps) != 0 {
		t.Fatalf("coverage after the fresh pass = %+v, want both case versions covered and no gaps", next.Coverage)
	}
}
