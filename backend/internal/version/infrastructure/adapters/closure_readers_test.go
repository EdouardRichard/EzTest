package adapters

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeRequirementRepo struct {
	requirements map[int64]version.Requirement
	aggregates   map[int64]version.RequirementAggregate
}

func (f *fakeRequirementRepo) GetRequirement(_ context.Context, requirementID int64) (version.Requirement, error) {
	requirement, ok := f.requirements[requirementID]
	if !ok {
		return version.Requirement{}, version.ErrRequirementNotFound
	}
	return requirement, nil
}

// Get serves the requirement aggregate (current revision acceptance criteria)
// the AC-level CoverageRecorded predicate consumes (v0.2.4-G SC-G4 P29).
func (f *fakeRequirementRepo) Get(_ context.Context, _ int64, requirementID int64) (version.RequirementAggregate, error) {
	aggregate, ok := f.aggregates[requirementID]
	if !ok {
		return version.RequirementAggregate{}, version.ErrRequirementNotFound
	}
	return aggregate, nil
}

type fakeClosureDesign struct {
	cases map[int64][]designapp.ExecutableCaseView
}

func (f *fakeClosureDesign) ListExecutable(_ context.Context, versionID int64) ([]designapp.ExecutableCaseView, error) {
	return append([]designapp.ExecutableCaseView(nil), f.cases[versionID]...), nil
}

type fakeClosureRuns struct {
	runs []execution.CaseRun
	byID map[int64]execution.CaseRun
}

func (f *fakeClosureRuns) LatestByCaseVersions(_ context.Context, _ int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error) {
	want := make(map[int64]bool, len(caseVersionIDs))
	for _, id := range caseVersionIDs {
		want[id] = true
	}
	out := make(map[int64]execution.CaseRun)
	for _, run := range f.runs {
		if !want[run.CaseVersionID] {
			continue
		}
		if existing, ok := out[run.CaseVersionID]; !ok || run.ID > existing.ID {
			out[run.CaseVersionID] = run
		}
	}
	return out, nil
}

func (f *fakeClosureRuns) ListAllByWave(_ context.Context, waveID int64) ([]execution.CaseRun, error) {
	out := make([]execution.CaseRun, 0)
	for _, run := range f.runs {
		if run.WaveID == waveID {
			out = append(out, run)
		}
	}
	return out, nil
}

type fakeClosureWaves struct {
	waves []execution.RunWave
}

func (f *fakeClosureWaves) GetActive(_ context.Context, versionID int64) (*execution.RunWave, error) {
	for index := range f.waves {
		wave := f.waves[index]
		if wave.VersionID == versionID && wave.Status.Active() {
			return &wave, nil
		}
	}
	return nil, nil
}

func (f *fakeClosureWaves) List(_ context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error) {
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

type fakeClosureIntents struct {
	active map[int64][]execution.ExecutionIntent
}

func (f *fakeClosureIntents) ListActive(_ context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	return append([]execution.ExecutionIntent(nil), f.active[versionID]...), nil
}

type fakeClosureHumans struct {
	open []execution.HumanRequest
}

func (f *fakeClosureHumans) ListByVersion(_ context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error) {
	rows := make([]execution.HumanRequest, 0)
	for _, request := range f.open {
		if request.VersionID != versionID {
			continue
		}
		if status != "" && string(request.Status) != status {
			continue
		}
		if beforeID != nil && request.ID >= *beforeID {
			continue
		}
		rows = append(rows, request)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

func (f *fakeClosureHumans) ListOpen(_ context.Context) ([]execution.HumanRequest, error) {
	return append([]execution.HumanRequest(nil), f.open...), nil
}

type fakeClosureWorkItems struct {
	items map[int64][]agent.WorkItem
}

func (f *fakeClosureWorkItems) ListByVersion(_ context.Context, versionID int64, limit int) ([]agent.WorkItem, error) {
	rows := f.items[versionID]
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return append([]agent.WorkItem(nil), rows...), nil
}

type fakeClosureIssues struct {
	byRun map[int64]delivery.Issue
}

func (f *fakeClosureIssues) FindByCaseRun(_ context.Context, versionID, caseRunID int64) (delivery.Issue, bool, error) {
	issue, ok := f.byRun[caseRunID]
	if !ok {
		return delivery.Issue{}, false, nil
	}
	return issue, true, nil
}

type fakeVersionReportStatus struct {
	status deliveryapp.VersionReportStatus
	err    error
	calls  int
}

func (f *fakeVersionReportStatus) VersionReportStatus(_ context.Context, _ int64) (deliveryapp.VersionReportStatus, error) {
	f.calls++
	return f.status, f.err
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

func newClosureFixture() Deps {
	currentVersion := int64(1001)
	requirements := &fakeRequirementRepo{
		requirements: map[int64]version.Requirement{
			101: {ID: 101, VersionID: 7, CurrentVersionID: &currentVersion, Status: version.RequirementClarified},
		},
		aggregates: map[int64]version.RequirementAggregate{
			101: {Versions: []version.RequirementVersion{
				{ID: 1001, RequirementID: 101, AcceptanceCriteria: []version.AcceptanceCriterion{
					{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
					{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
				}},
			}},
		},
	}
	design := &fakeClosureDesign{cases: map[int64][]designapp.ExecutableCaseView{7: {
		{CaseID: 201, CaseVersionID: 3001, RequirementID: 101, RequirementVersionID: 1001, Title: "Case A", Priority: "P0", CoveredACs: []string{"AC-1"}},
		{CaseID: 202, CaseVersionID: 3002, RequirementID: 101, RequirementVersionID: 1001, Title: "Case B", Priority: "P3", CoveredACs: []string{"AC-2"}},
		{CaseID: 203, CaseVersionID: 3003, RequirementID: 102, RequirementVersionID: 1002, Title: "Other", Priority: "P2"},
	}}}
	runs := &fakeClosureRuns{
		runs: []execution.CaseRun{
			{ID: 5001, WaveID: 11, CaseVersionID: 3001, Status: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication},
			{ID: 5002, WaveID: 11, CaseVersionID: 3002, Status: execution.CaseRunPassed},
			{ID: 5003, WaveID: 12, CaseVersionID: 3003, Status: execution.CaseRunPassed},
			{ID: 5004, WaveID: 12, CaseVersionID: 3001, Status: execution.CaseRunRunning},
		},
	}
	waves := &fakeClosureWaves{waves: []execution.RunWave{
		{ID: 11, VersionID: 7, Status: execution.WaveDone},
		{ID: 12, VersionID: 7, Status: execution.WaveRunning},
	}}
	intents := &fakeClosureIntents{active: map[int64][]execution.ExecutionIntent{7: {
		{ID: 1, VersionID: 7, CaseVersionID: 3001, Status: execution.IntentOpen},
		{ID: 2, VersionID: 7, CaseVersionID: 3003, Status: execution.IntentRunning},
	}}}
	humans := &fakeClosureHumans{open: []execution.HumanRequest{
		{ID: 2, VersionID: 7, Kind: execution.HumanLoginTakeover, Status: execution.HumanPending},
		{ID: 3, VersionID: 8, Kind: execution.HumanLoginTakeover, Status: execution.HumanPending},
		// Pending structured questions never block version closure (R-B3-4/C-2).
		{ID: 4, VersionID: 7, Kind: execution.HumanClarificationQuestion, Status: execution.HumanPending},
	}}
	workItems := &fakeClosureWorkItems{items: map[int64][]agent.WorkItem{7: {
		{ID: 10, VersionID: 7, Type: "design.gap", Status: agent.WorkItemOpen, Payload: json.RawMessage(`{"requirement_id":"101"}`)},
		{ID: 11, VersionID: 7, Type: "analyze.failure", Status: agent.WorkItemRunning, Payload: json.RawMessage(`{"requirement_id":"101"}`)},
		{ID: 12, VersionID: 7, Type: "design.requirement", Status: agent.WorkItemDone, Payload: json.RawMessage(`{"requirement_id":"101"}`)},
		{ID: 13, VersionID: 7, Type: "report.version", Status: agent.WorkItemOpen, Payload: json.RawMessage(`{}`)},
		{ID: 14, VersionID: 7, Type: "issue.triage", Status: agent.WorkItemClaimed, Payload: json.RawMessage(`{"issue_id":"9001"}`)},
		{ID: 15, VersionID: 7, Type: "design.gap", Status: agent.WorkItemOpen, Payload: json.RawMessage(`{"requirement_id":"999"}`)},
	}}}
	issues := &fakeClosureIssues{byRun: map[int64]delivery.Issue{5001: {ID: 9001, VersionID: 7}}}
	return Deps{
		Requirements: requirements,
		Design:       design,
		CaseRuns:     runs,
		Waves:        waves,
		Intents:      intents,
		Humans:       humans,
		WorkItems:    workItems,
		Issues:       issues,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestClosureCasesAndRuns(t *testing.T) {
	adapters := NewClosureReaders(newClosureFixture())
	ctx := context.Background()

	cases, err := adapters.CurrentConfirmedCases(ctx, 101)
	if err != nil {
		t.Fatalf("CurrentConfirmedCases: %v", err)
	}
	if len(cases) != 2 || cases[0].CaseVersionID != 3001 || cases[1].CaseVersionID != 3002 {
		t.Fatalf("cases = %+v", cases)
	}

	latest, err := adapters.LatestCaseRuns(ctx, 101)
	if err != nil {
		t.Fatalf("LatestCaseRuns: %v", err)
	}
	if len(latest) != 2 || latest[3001].CaseRunID != 5004 || latest[3002].CaseRunID != 5002 {
		t.Fatalf("latest runs = %+v", latest)
	}

	intents, err := adapters.CountActiveIntents(ctx, 101)
	if err != nil {
		t.Fatalf("CountActiveIntents: %v", err)
	}
	if intents != 1 {
		t.Fatalf("active intents = %d, want 1 (only requirement 101's case)", intents)
	}

	inflight, err := adapters.CountInflightCaseRuns(ctx, 101)
	if err != nil {
		t.Fatalf("CountInflightCaseRuns: %v", err)
	}
	if inflight != 1 {
		t.Fatalf("inflight case runs = %d, want 1 (run 5004 on case 3001)", inflight)
	}

	coverage, err := adapters.CoverageRecorded(ctx, 101)
	if err != nil {
		t.Fatalf("CoverageRecorded: %v", err)
	}
	if !coverage {
		t.Fatal("coverage_recorded = false, every requirement AC (AC-1,AC-2) is link-covered by a confirmed case")
	}
}

// TestCoverageRecordedACLevel pins the P29/SC-G4 redefinition: CoverageRecorded
// is AC-level LINK coverage (requirement AC全集 ⊆ ∪covered_acs of current
// confirmed cases), not the old "every case has a latest run" requirement-level
// check. An AC with no covering confirmed case is a deterministic gap (false);
// AC ids are normalized through the design domain union (single source of truth).
func TestCoverageRecordedACLevel(t *testing.T) {
	ctx := context.Background()

	// Complete link coverage: AC-1/AC-2 both covered -> true, even though case
	// 3001's latest run (5004) is still running (link coverage is independent of
	// execution; the cases_terminal/cases_verified checks gate execution).
	adapters := NewClosureReaders(newClosureFixture())
	complete, err := adapters.CoverageRecorded(ctx, 101)
	if err != nil {
		t.Fatalf("CoverageRecorded complete: %v", err)
	}
	if !complete {
		t.Fatal("complete AC link coverage = false, want true")
	}

	// Gap: requirement 101 has AC-1/AC-2/AC-3 but the confirmed cases cover only
	// AC-1/AC-2 -> AC-3 is an uncovered gap -> false.
	gapFixture := newClosureFixture()
	gapReqs := gapFixture.Requirements.(*fakeRequirementRepo)
	gapReqs.aggregates[101] = version.RequirementAggregate{Versions: []version.RequirementVersion{
		{ID: 1001, RequirementID: 101, AcceptanceCriteria: []version.AcceptanceCriterion{
			{ID: "AC-1"}, {ID: "AC-2"}, {ID: "AC-3"},
		}},
	}}
	gapAdapters := NewClosureReaders(gapFixture)
	gap, err := gapAdapters.CoverageRecorded(ctx, 101)
	if err != nil {
		t.Fatalf("CoverageRecorded gap: %v", err)
	}
	if gap {
		t.Fatal("AC-3 has no covering confirmed case; coverage_recorded must be false (deterministic gap)")
	}

	// No confirmed cases -> false (has_confirmed_case carries the reason).
	emptyFixture := newClosureFixture()
	emptyFixture.Design = &fakeClosureDesign{cases: map[int64][]designapp.ExecutableCaseView{7: {}}}
	emptyAdapters := NewClosureReaders(emptyFixture)
	empty, err := emptyAdapters.CoverageRecorded(ctx, 101)
	if err != nil {
		t.Fatalf("CoverageRecorded empty: %v", err)
	}
	if empty {
		t.Fatal("no confirmed cases; coverage_recorded must be false")
	}

	// Normalization: covered_acs whitespace/duplicates normalize through the
	// design union, so " AC-1 "/"AC-1" still covers AC-1 (no second truth).
	normFixture := newClosureFixture()
	normDesign := normFixture.Design.(*fakeClosureDesign)
	normDesign.cases[7] = []designapp.ExecutableCaseView{
		{CaseID: 201, CaseVersionID: 3001, RequirementID: 101, RequirementVersionID: 1001, CoveredACs: []string{" AC-1 ", "AC-1", "AC-2"}},
	}
	normAdapters := NewClosureReaders(normFixture)
	norm, err := normAdapters.CoverageRecorded(ctx, 101)
	if err != nil {
		t.Fatalf("CoverageRecorded normalized: %v", err)
	}
	if !norm {
		t.Fatal("normalized covered_acs {AC-1,AC-2} must cover the requirement AC set")
	}

	// Fail-closed (fix round minor #2): a broken data invariant must NOT vacuously
	// pass the predicate on an empty AC set — it returns a typed error.
	// (a) no current revision.
	nilRevFixture := newClosureFixture()
	nilRevReqs := nilRevFixture.Requirements.(*fakeRequirementRepo)
	nilRevReqs.requirements[101] = version.Requirement{ID: 101, VersionID: 7, CurrentVersionID: nil, Status: version.RequirementClarified}
	nilRevAdapters := NewClosureReaders(nilRevFixture)
	_, err = nilRevAdapters.CoverageRecorded(ctx, 101)
	if err == nil {
		t.Fatal("no current revision must fail closed with a typed error, not vacuously pass")
	}
	if typed := platform.AsError(err); typed.Status != 409 || typed.Code != "state_conflict" {
		t.Fatalf("no-current-revision error = %d/%s, want the typed 409/state_conflict the comment promises", typed.Status, typed.Code)
	}
	// (b) current revision absent from the aggregate.
	missingRevFixture := newClosureFixture()
	missingRevReqs := missingRevFixture.Requirements.(*fakeRequirementRepo)
	missingRevReqs.aggregates[101] = version.RequirementAggregate{Versions: []version.RequirementVersion{
		{ID: 9999, RequirementID: 101, AcceptanceCriteria: []version.AcceptanceCriterion{{ID: "AC-1"}}},
	}}
	missingRevAdapters := NewClosureReaders(missingRevFixture)
	_, err = missingRevAdapters.CoverageRecorded(ctx, 101)
	if err == nil {
		t.Fatal("current revision absent from the aggregate must fail closed with a typed error")
	}
	if typed := platform.AsError(err); typed.Status != 409 || typed.Code != "state_conflict" {
		t.Fatalf("missing-revision error = %d/%s, want the typed 409/state_conflict the comment promises", typed.Status, typed.Code)
	}
}

func TestClosureIssueRefs(t *testing.T) {
	adapters := NewClosureReaders(newClosureFixture())
	refs, err := adapters.CaseRunIssueRefs(context.Background(), 101)
	if err != nil {
		t.Fatalf("CaseRunIssueRefs: %v", err)
	}
	if len(refs[5001]) != 1 || refs[5001][0] != 9001 {
		t.Fatalf("issue refs = %+v", refs)
	}
}

func TestClosureOpenWorkItems(t *testing.T) {
	adapters := NewClosureReaders(newClosureFixture())
	counts, err := adapters.OpenWorkItems(context.Background(), 101)
	if err != nil {
		t.Fatalf("OpenWorkItems: %v", err)
	}
	// design.gap open + analyze.failure running are open for requirement 101;
	// done/other-requirement/report.version/issue.triage rows never count. GD-17
	// removed the requirement.close self-task split.
	if counts.Total != 2 {
		t.Fatalf("open work items = %+v, want total 2", counts)
	}
}

func TestVersionCloseReaders(t *testing.T) {
	adapters := NewClosureReaders(newClosureFixture())
	ctx := context.Background()

	blocking, err := adapters.BlockingWorkItems(ctx, 7)
	if err != nil {
		t.Fatalf("BlockingWorkItems: %v", err)
	}
	// Open/claimed/running: design.gap, analyze.failure, report.version,
	// issue.triage and the second design.gap = 5. GD-17 removed the version.close
	// self-task split.
	if blocking.Total != 5 {
		t.Fatalf("blocking work items = %+v, want total 5", blocking)
	}

	activeIntents, err := adapters.CountVersionActiveIntents(ctx, 7)
	if err != nil {
		t.Fatalf("CountVersionActiveIntents: %v", err)
	}
	if activeIntents != 2 {
		t.Fatalf("active intents = %d, want 2", activeIntents)
	}

	activeWaves, err := adapters.CountActiveWaves(ctx, 7)
	if err != nil {
		t.Fatalf("CountActiveWaves: %v", err)
	}
	if activeWaves != 1 {
		t.Fatalf("active waves = %d, want 1", activeWaves)
	}

	inflight, err := adapters.CountVersionInflightCaseRuns(ctx, 7)
	if err != nil {
		t.Fatalf("CountVersionInflightCaseRuns: %v", err)
	}
	if inflight != 1 {
		t.Fatalf("inflight case runs = %d, want 1", inflight)
	}

	// Execution coverage: case 3001's latest run (5004) is still running, so it
	// is the one confirmed case not yet executed; every other confirmed case has
	// a terminal latest run.
	coverage, err := adapters.ExecutionCoverage(ctx, 7)
	if err != nil {
		t.Fatalf("ExecutionCoverage: %v", err)
	}
	if !coverage.HasExecutionData {
		t.Fatal("HasExecutionData = false, the version has case runs")
	}
	if coverage.UnconfirmedCases != 1 {
		t.Fatalf("UnconfirmedCases = %d, want 1 (case 3001)", coverage.UnconfirmedCases)
	}

	openHumans, err := adapters.CountOpenHumanRequests(ctx, 7)
	if err != nil {
		t.Fatalf("CountOpenHumanRequests: %v", err)
	}
	if openHumans != 1 {
		t.Fatalf("open human requests = %d, want 1", openHumans)
	}
}

func TestReportStatusReaderMapsEffectiveStatus(t *testing.T) {
	reader := &fakeVersionReportStatus{status: deliveryapp.VersionReportStatus{
		Status: "published", Fingerprint: "stored", RevisionID: 42, CurrentFingerprint: "current",
	}}
	adapter := NewReportStatusReader(reader)
	status, err := adapter.VersionReportStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionReportStatus: %v", err)
	}
	if status.Status != version.ReportStatusPublished || status.Fingerprint != "stored" ||
		status.RevisionID != 42 || status.CurrentFingerprint != "current" {
		t.Fatalf("version report status = %+v", status)
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d", reader.calls)
	}
}
