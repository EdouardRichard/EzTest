package adapters

import (
	"context"
	"sort"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------------------------------------------------------------------------
// Fakes for the application ports the adapters consume.
// ---------------------------------------------------------------------------

type fakeVersions struct {
	views map[int64]versionapp.VersionView
}

func (f *fakeVersions) Get(_ context.Context, id int64) (versionapp.VersionView, error) {
	view, ok := f.views[id]
	if !ok {
		return versionapp.VersionView{}, version.ErrNotFound
	}
	return view, nil
}

type fakeRequirements struct {
	views map[int64][]versionapp.RequirementView
	raw   map[int64]version.Requirement
}

func (f *fakeRequirements) List(_ context.Context, versionID int64, _ string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error) {
	views := append([]versionapp.RequirementView(nil), f.views[versionID]...)
	sort.Slice(views, func(i, j int) bool { return views[i].ID > views[j].ID })
	filtered := views[:0]
	for _, view := range views {
		if beforeID != nil && view.ID >= *beforeID {
			continue
		}
		filtered = append(filtered, view)
	}
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}
	return filtered, hasMore, nil
}

func (f *fakeRequirements) GetRequirement(_ context.Context, requirementID int64) (version.Requirement, error) {
	requirement, ok := f.raw[requirementID]
	if !ok {
		return version.Requirement{}, version.ErrRequirementNotFound
	}
	return requirement, nil
}

type fakeClosures struct {
	latest map[int64]version.RequirementClosure
}

func (f *fakeClosures) Latest(_ context.Context, requirementID int64) (version.RequirementClosure, bool, error) {
	closure, ok := f.latest[requirementID]
	return closure, ok, nil
}

type fakeDesign struct {
	cases    map[int64][]designapp.ExecutableCaseView
	history  map[int64][]designapp.CaseVersionHistoryView
	byIDCall int
}

func (f *fakeDesign) ListExecutable(_ context.Context, versionID int64) ([]designapp.ExecutableCaseView, error) {
	return append([]designapp.ExecutableCaseView(nil), f.cases[versionID]...), nil
}

func (f *fakeDesign) ReadCaseVersionsByIDs(_ context.Context, versionID int64, ids []int64) ([]designapp.CaseVersionHistoryView, error) {
	f.byIDCall++
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]designapp.CaseVersionHistoryView, 0)
	for _, row := range f.history[versionID] {
		if want[row.ID] {
			out = append(out, row)
		}
	}
	return out, nil
}

type fakeWaves struct {
	waves []execution.RunWave
}

func (f *fakeWaves) Get(_ context.Context, id int64) (execution.RunWave, error) {
	for _, wave := range f.waves {
		if wave.ID == id {
			return wave, nil
		}
	}
	return execution.RunWave{}, execution.ErrNotFound
}

func (f *fakeWaves) List(_ context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error) {
	rows := make([]execution.RunWave, 0)
	for _, wave := range f.waves {
		if wave.VersionID == versionID && (beforeID == nil || wave.ID < *beforeID) {
			rows = append(rows, wave)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

type fakeCaseRuns struct {
	runs []execution.CaseRun
}

func (f *fakeCaseRuns) Get(_ context.Context, id int64) (execution.CaseRun, error) {
	for _, run := range f.runs {
		if run.ID == id {
			return run, nil
		}
	}
	return execution.CaseRun{}, execution.ErrNotFound
}

func (f *fakeCaseRuns) ListAllByWave(_ context.Context, waveID int64) ([]execution.CaseRun, error) {
	out := make([]execution.CaseRun, 0)
	for _, run := range f.runs {
		if run.WaveID == waveID {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeCaseRuns) LatestByCaseVersions(_ context.Context, _ int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error) {
	want := make(map[int64]bool, len(caseVersionIDs))
	for _, id := range caseVersionIDs {
		want[id] = true
	}
	out := make(map[int64]execution.CaseRun)
	for _, run := range f.runs {
		if !want[run.CaseVersionID] || run.Status == execution.CaseRunSuperseded {
			continue
		}
		if existing, ok := out[run.CaseVersionID]; !ok || run.ID > existing.ID {
			out[run.CaseVersionID] = run
		}
	}
	return out, nil
}

type fakeResults struct {
	steps      map[int64][]execution.StepResult
	assertions map[int64][]execution.AssertionResult
}

func (f *fakeResults) ListSteps(_ context.Context, caseRunID int64) ([]execution.StepResult, error) {
	return append([]execution.StepResult(nil), f.steps[caseRunID]...), nil
}

func (f *fakeResults) ListAssertions(_ context.Context, caseRunID int64) ([]execution.AssertionResult, error) {
	return append([]execution.AssertionResult(nil), f.assertions[caseRunID]...), nil
}

type fakeEvidence struct {
	rows map[int64][]execution.Evidence
}

func (f *fakeEvidence) ListByCaseRun(_ context.Context, caseRunID int64, _ int) ([]execution.Evidence, error) {
	return append([]execution.Evidence(nil), f.rows[caseRunID]...), nil
}

type fakeSUTContexts struct {
	rows map[int64][]execution.CaseRunSUTContext
}

func (f *fakeSUTContexts) ListByCaseRun(_ context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error) {
	return append([]execution.CaseRunSUTContext(nil), f.rows[caseRunID]...), nil
}

type fakeIntents struct {
	active map[int64][]execution.ExecutionIntent
}

func (f *fakeIntents) ListActive(_ context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	return append([]execution.ExecutionIntent(nil), f.active[versionID]...), nil
}

type fakeHumans struct {
	open []execution.HumanRequest
}

func (f *fakeHumans) ListOpen(_ context.Context) ([]execution.HumanRequest, error) {
	return append([]execution.HumanRequest(nil), f.open...), nil
}

type fakeIssues struct {
	rows map[int64][]delivery.Issue
	refs map[int64][]delivery.IssueRef
}

func (f *fakeIssues) List(_ context.Context, versionID int64, filter deliveryapp.IssueFilter) ([]delivery.Issue, error) {
	rows := make([]delivery.Issue, 0)
	for _, issue := range f.rows[versionID] {
		if filter.Before > 0 && issue.ID >= filter.Before {
			continue
		}
		rows = append(rows, issue)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (f *fakeIssues) ListRefs(_ context.Context, issueID int64) ([]delivery.IssueRef, error) {
	return append([]delivery.IssueRef(nil), f.refs[issueID]...), nil
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

var (
	fixtureTime  = time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	fixtureAfter = fixtureTime.Add(time.Minute)
)

func newReadModelFixture() (Deps, *fakeIssues) {
	sutRef := int64(70)
	versionSUT := int64(70)
	waves := &fakeWaves{waves: []execution.RunWave{
		{
			ID: 11, VersionID: 7, Status: execution.WaveDone, Trigger: execution.WaveTriggerManual,
			Counts:    execution.WaveCounts{Total: 2, Passed: 1, Failed: 1, Evidence: 1, ByVersionSUT: map[string]execution.SUTCounts{"70": {Total: 2, Passed: 1, Failed: 1}}},
			StartedAt: &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
		{
			ID: 12, VersionID: 7, Status: execution.WaveDone, Trigger: execution.WaveTriggerAuto,
			Counts:    execution.WaveCounts{Total: 0, ByVersionSUT: map[string]execution.SUTCounts{}},
			CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
		},
	}}
	runs := &fakeCaseRuns{runs: []execution.CaseRun{
		{
			ID: 5001, WaveID: 11, CaseVersionID: 3001, Status: execution.CaseRunFailed,
			FailureClass: execution.FailureClassApplication,
			Summary: execution.CaseRunSummary{
				StepTotal: 2, StepFailed: 1, AssertionTotal: 2, AssertionFailed: 1, EvidenceCount: 1,
			},
			StartedAt: &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
		{
			ID: 5002, WaveID: 11, CaseVersionID: 3002, Status: execution.CaseRunPassed,
			Summary:   execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1},
			StartedAt: &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
		{
			ID: 5003, WaveID: 12, CaseVersionID: 3003, Status: execution.CaseRunFailed,
			FailureClass: execution.FailureClassEnvironment,
			Summary:      execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1},
			CreatedAt:    fixtureTime, UpdatedAt: fixtureTime,
		},
	}}
	results := &fakeResults{
		steps: map[int64][]execution.StepResult{
			5001: {
				{ID: 6001, CaseRunID: 5001, Seq: 1, TargetVersionSUTID: &sutRef, Status: execution.StepFailed},
				{ID: 6002, CaseRunID: 5001, Seq: 2, Status: execution.StepPassed},
			},
		},
		assertions: map[int64][]execution.AssertionResult{
			5001: {
				{ID: 6501, CaseRunID: 5001, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindAPI, Verdict: execution.AssertionFailed, Expected: "200", Actual: "500", Reason: "boom", TargetVersionSUTID: &sutRef},
				{ID: 6502, CaseRunID: 5001, StepSeq: 2, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionPassed},
			},
		},
	}
	evidence := &fakeEvidence{rows: map[int64][]execution.Evidence{
		5001: {{ID: 7001, CaseRunID: 5001, VersionSUTID: &sutRef, StepSeq: intPtr(1), AssertionSeq: intPtr(1), Kind: execution.EvidenceScreenshot, SHA256: "aabb", SizeBytes: 128}},
	}}
	suts := &fakeSUTContexts{rows: map[int64][]execution.CaseRunSUTContext{
		5001: {{ID: 1, CaseRunID: 5001, VersionSUTID: 70, Status: execution.SUTContextClosed}},
	}}
	design := &fakeDesign{
		cases: map[int64][]designapp.ExecutableCaseView{7: {
			{CaseID: 201, CaseVersionID: 3001, RequirementID: 101, RequirementVersionID: 1001, Title: "Case A", Priority: "P1", VersionSUTIDs: []int64{versionSUT}},
			{CaseID: 202, CaseVersionID: 3002, RequirementID: 101, RequirementVersionID: 1001, Title: "Case B", Priority: "P2"},
		}},
		history: map[int64][]designapp.CaseVersionHistoryView{7: {
			{ID: 3003, Revision: 1, Title: "Historical C", Status: "superseded"},
		}},
	}
	requirements := &fakeRequirements{
		views: map[int64][]versionapp.RequirementView{7: {
			{ID: 101, CurrentVersionID: 1001, Title: "Requirement A", Status: "closed", UpdatedAt: fixtureAfter, ClosedAt: &fixtureAfter},
		}},
		raw: map[int64]version.Requirement{
			101: {ID: 101, VersionID: 7, CurrentVersionID: int64Ptr(1001), Status: version.RequirementClosed, ClosedAt: &fixtureAfter},
		},
	}
	closures := &fakeClosures{latest: map[int64]version.RequirementClosure{
		101: {ID: 9001, RequirementID: 101, RequirementVersionID: 1001, CloseRound: 1, ClosedAt: fixtureAfter, ReopenCount: 0},
	}}
	versions := &fakeVersions{views: map[int64]versionapp.VersionView{7: {
		ID: 7, Title: "Version", Status: "running", CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		Bindings: []versionapp.BindingView{{
			ID:          70,
			SUT:         resourceapp.SUTView{ID: "700", Name: "SUT"},
			Environment: resourceapp.EnvironmentView{ID: "710", Name: "dev"},
		}},
	}}}
	issues := &fakeIssues{
		rows: map[int64][]delivery.Issue{7: {
			{ID: 8001, VersionID: 7, Title: "Issue A", Status: delivery.IssueStatusOpen, Category: "api", Severity: "major", Source: "failure_candidate", CreatedAt: fixtureTime, UpdatedAt: fixtureAfter},
			{ID: 8002, VersionID: 7, Title: "Issue B", Status: delivery.IssueStatusMerged, Category: "ui", Severity: "minor", Source: "failure_candidate", MergedIntoID: int64Ptr(8001), CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
		}},
		refs: map[int64][]delivery.IssueRef{
			8001: {{ID: 1, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5001}},
			8002: {{ID: 2, IssueID: 8002, Kind: delivery.IssueRefKindCaseRun, RefID: 5001}},
		},
	}
	deps := Deps{
		Versions:     versions,
		Requirements: requirements,
		Closures:     closures,
		Design:       design,
		Waves:        waves,
		CaseRuns:     runs,
		Results:      results,
		Evidence:     evidence,
		SUTContexts:  suts,
		Intents:      &fakeIntents{},
		Humans:       &fakeHumans{},
		Issues:       issues,
	}
	return deps, issues
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestExecutionReaderListFailedApplicationCaseRuns(t *testing.T) {
	deps, _ := newReadModelFixture()
	reader := NewExecutionReader(deps)
	snapshots, err := reader.ListFailedApplicationCaseRuns(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListFailedApplicationCaseRuns: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v, want only the failed(application) run", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.ID != 5001 || snapshot.VersionID != 7 || snapshot.WaveID != 11 || snapshot.CaseVersionID != 3001 {
		t.Fatalf("ids = %+v", snapshot)
	}
	if snapshot.CaseTitle != "Case A" || snapshot.RequirementID != 101 {
		t.Fatalf("case = %+v", snapshot)
	}
	if snapshot.VersionSUTID == nil || *snapshot.VersionSUTID != 70 || len(snapshot.VersionSUTIDs) != 1 {
		t.Fatalf("version SUTs = %v/%v", snapshot.VersionSUTID, snapshot.VersionSUTIDs)
	}
	if snapshot.Category != "api" || snapshot.Severity != "major" {
		t.Fatalf("category/severity = %q/%q, want api/major", snapshot.Category, snapshot.Severity)
	}
	if snapshot.StepCount != 2 || snapshot.StepFailedCount != 1 || snapshot.AssertionCount != 2 || snapshot.AssertionFailedCount != 1 {
		t.Fatalf("counts = %+v", snapshot)
	}
	if snapshot.FailedStepSeq != 1 || snapshot.FailedAssertionSeq != 1 || snapshot.FailedStepResultID != 6001 {
		t.Fatalf("failure position = %+v", snapshot)
	}
	if len(snapshot.FailedAssertionResultIDs) != 1 || snapshot.FailedAssertionResultIDs[0] != 6501 {
		t.Fatalf("failed assertion ids = %v", snapshot.FailedAssertionResultIDs)
	}
	if snapshot.FailedExpected != "200" || snapshot.FailedActual != "500" || snapshot.FailedReason != "boom" {
		t.Fatalf("failure detail = %+v", snapshot)
	}
	if len(snapshot.EvidenceIDs) != 1 || snapshot.EvidenceIDs[0] != 7001 {
		t.Fatalf("evidence ids = %v", snapshot.EvidenceIDs)
	}
	if !snapshot.CreatedAt.Equal(fixtureTime) {
		t.Fatalf("created_at = %v", snapshot.CreatedAt)
	}
}

func TestExecutionReaderCaseRunSnapshot(t *testing.T) {
	deps, _ := newReadModelFixture()
	reader := NewExecutionReader(deps)
	snapshot, err := reader.CaseRunSnapshot(context.Background(), 5002)
	if err != nil {
		t.Fatalf("CaseRunSnapshot: %v", err)
	}
	if snapshot.ID != 5002 || snapshot.CaseTitle != "Case B" || snapshot.Status != "passed" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if _, err := reader.CaseRunSnapshot(context.Background(), 9999); err == nil {
		t.Fatal("CaseRunSnapshot(missing) = nil error")
	}
}

func TestReportDataReaderVersionSnapshot(t *testing.T) {
	deps, _ := newReadModelFixture()
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.VersionID != 7 || len(snapshot.Requirements) != 1 || len(snapshot.Waves) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	requirement := snapshot.Requirements[0]
	if requirement.ID != 101 || requirement.Title != "Requirement A" || requirement.Status != "closed" ||
		requirement.ClosureID == nil || *requirement.ClosureID != 9001 || requirement.CloseRound != 1 {
		t.Fatalf("requirement = %+v", requirement)
	}
	if len(snapshot.VersionSUTs) != 1 || snapshot.VersionSUTs[0].ID != 70 || snapshot.VersionSUTs[0].SUTName != "SUT" ||
		snapshot.VersionSUTs[0].EnvironmentName != "dev" {
		t.Fatalf("version SUTs = %+v", snapshot.VersionSUTs)
	}
	if len(snapshot.CaseRuns) != 3 {
		t.Fatalf("case runs = %+v, want all three version runs", snapshot.CaseRuns)
	}
	var historical delivery.CaseRunSnapshot
	for _, run := range snapshot.CaseRuns {
		if run.ID == 5003 {
			historical = run
		}
	}
	if historical.CaseTitle != "Historical C" {
		t.Fatalf("historical case title = %q, want the superseded row title", historical.CaseTitle)
	}
	if len(snapshot.Issues) != 2 {
		t.Fatalf("issues = %+v", snapshot.Issues)
	}
	var merged delivery.IssueSnapshot
	for _, issue := range snapshot.Issues {
		if issue.ID == 8001 {
			if len(issue.SourceIssueIDs) != 1 || issue.SourceIssueIDs[0] != 8002 {
				t.Fatalf("issue 8001 source ids = %v, want [8002]", issue.SourceIssueIDs)
			}
			if len(issue.CaseRunIDs) != 1 || issue.CaseRunIDs[0] != 5001 {
				t.Fatalf("issue 8001 case runs = %v", issue.CaseRunIDs)
			}
		}
		if issue.ID == 8002 {
			merged = issue
		}
	}
	if merged.MergedIntoID == nil || *merged.MergedIntoID != 8001 {
		t.Fatalf("merged issue = %+v", merged)
	}
	coverage := snapshot.Coverage
	if coverage.RequirementsTotal != 1 || coverage.RequirementsCovered != 1 ||
		coverage.CaseVersionsTotal != 2 || coverage.CaseVersionsCovered != 1 || len(coverage.Gaps) != 1 {
		t.Fatalf("coverage = %+v", coverage)
	}
	if coverage.Gaps[0].CaseVersionID != 3001 || coverage.Gaps[0].Reason != "execution_failed_coverage" {
		t.Fatalf("coverage gap = %+v", coverage.Gaps[0])
	}
}

func TestReportDataReaderWaveSnapshotIsScoped(t *testing.T) {
	deps, _ := newReadModelFixture()
	reader := NewReportDataReader(deps)
	snapshot, err := reader.WaveSnapshot(context.Background(), 7, 11)
	if err != nil {
		t.Fatalf("WaveSnapshot: %v", err)
	}
	if snapshot.WaveID == nil || *snapshot.WaveID != 11 || len(snapshot.Waves) != 1 || snapshot.Waves[0].ID != 11 {
		t.Fatalf("wave snapshot = %+v", snapshot)
	}
	if len(snapshot.CaseRuns) != 2 {
		t.Fatalf("wave case runs = %+v, want only wave 11 runs", snapshot.CaseRuns)
	}
	if len(snapshot.Requirements) != 0 {
		t.Fatalf("wave report requirements = %+v, want none", snapshot.Requirements)
	}
	if _, err := reader.WaveSnapshot(context.Background(), 8, 11); err == nil {
		t.Fatal("WaveSnapshot(foreign version) = nil error")
	}
}

// TestWaveReportCoverageReflectsCases reproduces QA Q3: the wave report used to
// carry the zero-value coverage (0/0/0/0) even though the version had
// requirements and executable cases. Coverage is a version-level projection, so
// every wave report of the version must expose it (spec §6.1) regardless of
// which cases the wave itself ran; zeros stay reserved for a version that
// genuinely has no requirements/cases.
func TestWaveReportCoverageReflectsCases(t *testing.T) {
	deps, _ := newReadModelFixture()
	reader := NewReportDataReader(deps)
	version, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if version.Coverage.RequirementsTotal != 1 || version.Coverage.CaseVersionsTotal != 2 {
		t.Fatalf("version coverage = %+v, want 1 requirement and 2 case versions", version.Coverage)
	}
	for _, waveID := range []int64{11, 12} {
		snapshot, err := reader.WaveSnapshot(context.Background(), 7, waveID)
		if err != nil {
			t.Fatalf("WaveSnapshot(%d): %v", waveID, err)
		}
		coverage := snapshot.Coverage
		if coverage.RequirementsTotal == 0 || coverage.CaseVersionsTotal == 0 {
			t.Fatalf("wave %d coverage = %+v, want non-zero denominators (QA Q3)", waveID, coverage)
		}
		if coverage.RequirementsTotal != version.Coverage.RequirementsTotal ||
			coverage.RequirementsCovered != version.Coverage.RequirementsCovered ||
			coverage.CaseVersionsTotal != version.Coverage.CaseVersionsTotal ||
			coverage.CaseVersionsCovered != version.Coverage.CaseVersionsCovered {
			t.Fatalf("wave %d coverage = %+v, want the version projection %+v", waveID, coverage, version.Coverage)
		}
	}

	// 0/0 is only correct when the version really has no requirements/cases.
	empty, _ := newReadModelFixture()
	empty.Requirements = &fakeRequirements{}
	empty.Design = &fakeDesign{}
	emptySnapshot, err := NewReportDataReader(empty).WaveSnapshot(context.Background(), 7, 12)
	if err != nil {
		t.Fatalf("empty WaveSnapshot: %v", err)
	}
	if coverage := emptySnapshot.Coverage; coverage.RequirementsTotal != 0 ||
		coverage.RequirementsCovered != 0 || coverage.CaseVersionsTotal != 0 ||
		coverage.CaseVersionsCovered != 0 || len(coverage.Gaps) != 0 {
		t.Fatalf("empty version coverage = %+v, want 0/0 without gaps", coverage)
	}
}

// TestWaveReportCoverageShowsGaps pins that a wave report lists the execution
// coverage gaps of the version instead of the misleading "无覆盖缺口" empty
// state, and that the reason matches the execution.coverage_gap analysis
// (failed run vs. never run).
func TestWaveReportCoverageShowsGaps(t *testing.T) {
	t.Run("failed run is a gap", func(t *testing.T) {
		deps, _ := newReadModelFixture()
		snapshot, err := NewReportDataReader(deps).WaveSnapshot(context.Background(), 7, 11)
		if err != nil {
			t.Fatalf("WaveSnapshot: %v", err)
		}
		if len(snapshot.Coverage.Gaps) != 1 {
			t.Fatalf("gaps = %+v, want the failed case 3001", snapshot.Coverage.Gaps)
		}
		gap := snapshot.Coverage.Gaps[0]
		if gap.RequirementID != 101 || gap.CaseVersionID != 3001 || gap.Reason != "execution_failed_coverage" {
			t.Fatalf("gap = %+v, want requirement 101 / case 3001 / execution_failed_coverage", gap)
		}
		if snapshot.Coverage.CaseVersionsCovered != 1 || snapshot.Coverage.RequirementsCovered != 1 {
			t.Fatalf("coverage = %+v, want the passing case to count", snapshot.Coverage)
		}
	})

	t.Run("never-run case is a gap", func(t *testing.T) {
		deps, _ := newReadModelFixture()
		runs := deps.CaseRuns.(*fakeCaseRuns)
		runs.runs = runs.runs[1:] // keep only the passed 3002 run
		snapshot, err := NewReportDataReader(deps).WaveSnapshot(context.Background(), 7, 11)
		if err != nil {
			t.Fatalf("WaveSnapshot: %v", err)
		}
		if len(snapshot.Coverage.Gaps) != 1 {
			t.Fatalf("gaps = %+v, want the never-run case 3001", snapshot.Coverage.Gaps)
		}
		gap := snapshot.Coverage.Gaps[0]
		if gap.CaseVersionID != 3001 || gap.Reason != "execution_not_run" {
			t.Fatalf("gap = %+v, want case 3001 / execution_not_run", gap)
		}
		if snapshot.Coverage.CaseVersionsCovered != 1 || snapshot.Coverage.RequirementsCovered != 1 {
			t.Fatalf("coverage = %+v, want the passing case to count", snapshot.Coverage)
		}
	})

	t.Run("no passing case leaves the requirement uncovered", func(t *testing.T) {
		deps, _ := newReadModelFixture()
		runs := deps.CaseRuns.(*fakeCaseRuns)
		for index := range runs.runs {
			if runs.runs[index].CaseVersionID == 3002 {
				runs.runs[index].Status = execution.CaseRunInconclusive
			}
		}
		// Wave 12 has no executable case run of its own: the version projection
		// must still list both uncovered cases.
		snapshot, err := NewReportDataReader(deps).WaveSnapshot(context.Background(), 7, 12)
		if err != nil {
			t.Fatalf("WaveSnapshot: %v", err)
		}
		if len(snapshot.Coverage.Gaps) != 2 {
			t.Fatalf("gaps = %+v, want both executable cases", snapshot.Coverage.Gaps)
		}
		if snapshot.Coverage.CaseVersionsCovered != 0 || snapshot.Coverage.RequirementsCovered != 0 {
			t.Fatalf("coverage = %+v, want zero covered with no passing case", snapshot.Coverage)
		}
	})
}

// TestCoverageMatchesCaseResults pins that the coverage projection is derived
// from the deterministic case results a report renders: the per-requirement
// projection reconciles with the aggregate counts, the gap reasons and the
// latest case_run status of every executable case version. The report narrative
// guard consumes these facts, so they must stay authoritative (PO E2E Report B
// claimed "no valid execution" for requirements whose cases passed).
func TestCoverageMatchesCaseResults(t *testing.T) {
	deps, _ := newReadModelFixture()
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	coverage := snapshot.Coverage
	if coverage.RequirementsTotal != 1 || coverage.RequirementsCovered != 1 ||
		coverage.CaseVersionsTotal != 2 || coverage.CaseVersionsCovered != 1 {
		t.Fatalf("coverage = %+v, want the fixture's 1 requirement / 2 case versions / 1 covered", coverage)
	}

	// The latest run of 3002 passed (covered); the latest run of 3001 failed
	// (a coverage gap). Neither 3001 nor 3002 has a "not run" gap.
	latest := map[int64]string{}
	order := map[int64]int64{}
	for _, run := range snapshot.CaseRuns {
		if run.CaseVersionID != 3001 && run.CaseVersionID != 3002 {
			continue
		}
		if run.ID >= order[run.CaseVersionID] {
			order[run.CaseVersionID] = run.ID
			latest[run.CaseVersionID] = run.Status
		}
	}
	if latest[3001] != "failed" || latest[3002] != "passed" {
		t.Fatalf("latest executable run statuses = %+v, want 3001 failed / 3002 passed", latest)
	}

	want := delivery.RequirementCoverageSnapshot{
		RequirementID:              101,
		CaseVersionsTotal:          2,
		CaseVersionsCovered:        1,
		CaseVersionsFailedCoverage: 1,
	}
	if len(coverage.ByRequirement) != 1 || coverage.ByRequirement[0] != want {
		t.Fatalf("per-requirement coverage = %+v, want %+v", coverage.ByRequirement, want)
	}
	row := coverage.ByRequirement[0]
	if row.CaseVersionsCovered+row.CaseVersionsNotRun+row.CaseVersionsFailedCoverage != row.CaseVersionsTotal {
		t.Fatalf("per-requirement coverage does not add up: %+v", row)
	}

	// The per-requirement rows reconcile with the aggregate projection and with
	// the gap list the report renders.
	var totals, covered, notRun, failedCoverage delivery.RequirementCoverageSnapshot
	for _, row := range coverage.ByRequirement {
		totals.CaseVersionsTotal += row.CaseVersionsTotal
		covered.CaseVersionsCovered += row.CaseVersionsCovered
		notRun.CaseVersionsNotRun += row.CaseVersionsNotRun
		failedCoverage.CaseVersionsFailedCoverage += row.CaseVersionsFailedCoverage
	}
	gapNotRun, gapFailedCoverage := 0, 0
	for _, gap := range coverage.Gaps {
		switch gap.Reason {
		case "execution_not_run":
			gapNotRun++
		case "execution_failed_coverage":
			gapFailedCoverage++
		default:
			t.Fatalf("unexpected coverage gap reason %q", gap.Reason)
		}
	}
	if totals.CaseVersionsTotal != coverage.CaseVersionsTotal ||
		covered.CaseVersionsCovered != coverage.CaseVersionsCovered ||
		notRun.CaseVersionsNotRun != gapNotRun ||
		failedCoverage.CaseVersionsFailedCoverage != gapFailedCoverage {
		t.Fatalf("coverage does not reconcile: totals=%+v gaps not_run=%d failed_coverage=%d aggregate=%+v",
			totals, gapNotRun, gapFailedCoverage, coverage)
	}
	if gapNotRun+gapFailedCoverage != coverage.CaseVersionsTotal-coverage.CaseVersionsCovered {
		t.Fatalf("coverage gaps = %d, want the %d uncovered case versions",
			gapNotRun+gapFailedCoverage, coverage.CaseVersionsTotal-coverage.CaseVersionsCovered)
	}

	// A wave report exposes the same version-level projection (QA Q3).
	wave, err := reader.WaveSnapshot(context.Background(), 7, 11)
	if err != nil {
		t.Fatalf("WaveSnapshot: %v", err)
	}
	if len(wave.Coverage.ByRequirement) != 1 || wave.Coverage.ByRequirement[0] != want {
		t.Fatalf("wave per-requirement coverage = %+v, want %+v", wave.Coverage.ByRequirement, want)
	}
}

func TestReportDataReaderCapsIssueList(t *testing.T) {
	deps, issues := newReadModelFixture()
	rows := make([]delivery.Issue, 0, delivery.MaxReportIssues+1)
	for index := 0; index < delivery.MaxReportIssues+1; index++ {
		rows = append(rows, delivery.Issue{
			ID: int64(10000 + index), VersionID: 7, Title: "I", Status: delivery.IssueStatusOpen,
			Category: "api", Severity: "minor", Source: "failure_candidate",
			CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
		})
	}
	issues.rows[7] = rows
	issues.refs = map[int64][]delivery.IssueRef{}
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if len(snapshot.Issues) != delivery.MaxReportIssues {
		t.Fatalf("issues = %d, want cap %d", len(snapshot.Issues), delivery.MaxReportIssues)
	}
	if len(snapshot.TruncatedSections) != 1 || snapshot.TruncatedSections[0] != delivery.ReportTruncationIssues {
		t.Fatalf("truncated sections = %v", snapshot.TruncatedSections)
	}
}

func TestReportDataReaderVersionReportReady(t *testing.T) {
	deps, _ := newReadModelFixture()
	intents := &fakeIntents{active: map[int64][]execution.ExecutionIntent{7: {{ID: 1, VersionID: 7, Status: execution.IntentOpen}}}}
	deps.Intents = intents
	reader := NewReportDataReader(deps)
	if ok, err := reader.VersionReportReady(context.Background(), 7); err != nil || ok {
		t.Fatalf("ready with active intent = %v/%v, want false", ok, err)
	}

	deps.Intents = &fakeIntents{}
	deps.Humans = &fakeHumans{open: []execution.HumanRequest{{ID: 2, VersionID: 7, Status: execution.HumanPending}}}
	reader = NewReportDataReader(deps)
	if ok, err := reader.VersionReportReady(context.Background(), 7); err != nil || ok {
		t.Fatalf("ready with open human request = %v/%v, want false", ok, err)
	}

	deps.Humans = &fakeHumans{}
	reader = NewReportDataReader(deps)
	if ok, err := reader.VersionReportReady(context.Background(), 7); err != nil || !ok {
		t.Fatalf("ready without blockers = %v/%v, want true", ok, err)
	}
}

// TestReadModelRequirementExecutionSettled pins the derived settled projection
// the report due gate consumes (R2-NEW-02). A requirement is settled only when
// it has at least one executable case and every executable case's latest
// non-superseded run is terminal (blocked/inconclusive count as terminal, a
// missing run does not). A requirement without executable cases is never
// settled. ExecutionSettled is derived metadata and must not participate in the
// report fingerprint.
func TestReadModelRequirementExecutionSettled(t *testing.T) {
	deps, _ := newReadModelFixture()
	design := deps.Design.(*fakeDesign)
	requirements := deps.Requirements.(*fakeRequirements)
	requirements.views[7] = append(requirements.views[7], versionapp.RequirementView{
		ID: 102, Title: "Requirement B", Status: "clarified", UpdatedAt: fixtureAfter,
	})
	reader := NewReportDataReader(deps)

	// The fixture requirement 101 has two executable cases whose latest runs are
	// failed and passed: both are terminal, so the requirement is settled.
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	settledByID := func(snapshot delivery.ReportSnapshot) map[int64]bool {
		out := make(map[int64]bool, len(snapshot.Requirements))
		for _, requirement := range snapshot.Requirements {
			out[requirement.ID] = requirement.ExecutionSettled
		}
		return out
	}
	settled := settledByID(snapshot)
	if !settled[101] {
		t.Fatalf("requirement 101 settled = false, want true (failed+passed runs are terminal)")
	}
	if settled[102] {
		t.Fatalf("requirement 102 without executable cases settled = true, want false")
	}

	// A third executable case without any run leaves the requirement unsettled.
	design.cases[7] = append(design.cases[7], designapp.ExecutableCaseView{
		CaseID: 203, CaseVersionID: 3004, RequirementID: 101, RequirementVersionID: 1001, Title: "Case C",
	})
	snapshot, err = reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot after adding an unrun case: %v", err)
	}
	if settledByID(snapshot)[101] {
		t.Fatal("requirement 101 settled = true while one executable case has no run")
	}

	// A terminal blocked run settles the remaining case: blocked is terminal.
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs = append(runs.runs, execution.CaseRun{
		ID: 5004, WaveID: 11, CaseVersionID: 3004, Status: execution.CaseRunBlocked,
		CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
	})
	settledSnapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot after the blocked run: %v", err)
	}
	settled = settledByID(settledSnapshot)
	if !settled[101] {
		t.Fatal("requirement 101 settled = false after every executable case became terminal")
	}
	if settled[102] {
		t.Fatal("requirement 102 without executable cases settled = true, want false")
	}

	// The derived flag must never enter the canonical fingerprint payload.
	baseline := delivery.Fingerprint(settledSnapshot)
	for index := range settledSnapshot.Requirements {
		settledSnapshot.Requirements[index].ExecutionSettled = false
	}
	if got := delivery.Fingerprint(settledSnapshot); got != baseline {
		t.Fatalf("fingerprint changed with ExecutionSettled: %s != %s", got, baseline)
	}
}

func intPtr(value int) *int { return &value }
