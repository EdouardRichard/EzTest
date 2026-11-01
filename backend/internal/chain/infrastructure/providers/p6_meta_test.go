package providers

import (
	"context"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

func TestP6DesignProviderTypedMetaAndSnapshot(t *testing.T) {
	reader := &stubDesignReader{graph: designapp.GraphView{
		VersionID: 42,
		Requirements: []designapp.RequirementGraphView{{
			RequirementID: 1,
			Scenarios: []designapp.ScenarioGraphView{{
				ID:   10,
				Name: "Checkout",
				Cases: []designapp.CaseCardView{
					{CaseID: 100, CurrentVersionID: 101, Title: "Pays", Status: "confirmed", StepCount: 4, AssertionCount: 3},
					{CaseID: 102, CurrentVersionID: 103, Title: "Old", Status: "stale"},
				},
			}},
		}},
	}}
	provider := NewDesignProvider(reader)

	scenarios, err := provider.Scenarios(context.Background(), 42)
	if err != nil {
		t.Fatalf("Scenarios: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("scenarios = %+v", scenarios)
	}
	if scenarios[0].Meta["requirement_id"] != "1" || scenarios[0].Meta["case_count"] != 2 {
		t.Errorf("scenario meta = %v", scenarios[0].Meta)
	}

	cases, err := provider.Cases(context.Background(), 42)
	if err != nil {
		t.Fatalf("Cases: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("cases = %+v", cases)
	}
	if cases[0].Meta["scenario_id"] != "10" || cases[0].Meta["stale"] != false ||
		cases[0].Meta["step_count"] != 4 || cases[0].Meta["assertion_count"] != 3 {
		t.Errorf("case meta = %v", cases[0].Meta)
	}
	if cases[1].Meta["stale"] != true {
		t.Errorf("stale case meta = %v", cases[1].Meta)
	}

	reader.graphReads = 0
	scenarios, cases, err = provider.DesignSnapshot(context.Background(), 42)
	if err != nil {
		t.Fatalf("DesignSnapshot: %v", err)
	}
	if reader.graphReads != 1 {
		t.Errorf("DesignSnapshot graph reads = %d, want 1", reader.graphReads)
	}
	if len(scenarios) != 1 || len(cases) != 2 {
		t.Errorf("snapshot = %d scenarios / %d cases", len(scenarios), len(cases))
	}
}

type fakeWaveRepository struct {
	executionapp.WaveRepository
	waves     []execution.RunWave
	listCalls int
}

func (f *fakeWaveRepository) List(context.Context, int64, int, *int64) ([]execution.RunWave, bool, error) {
	f.listCalls++
	return f.waves, false, nil
}

type fakeCaseRunRepository struct {
	executionapp.CaseRunRepository
	runs []execution.CaseRun
}

func (f *fakeCaseRunRepository) ListAllByWave(context.Context, int64) ([]execution.CaseRun, error) {
	return f.runs, nil
}

func TestP6ExecutionProviderTypedMetaAndSnapshot(t *testing.T) {
	started := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	finished := started.Add(1500 * time.Millisecond)
	waves := &fakeWaveRepository{waves: []execution.RunWave{{
		ID: 7, Status: execution.WaveDone, Trigger: execution.WaveTriggerAuto,
		Counts: execution.WaveCounts{Total: 2},
	}}}
	runs := &fakeCaseRunRepository{runs: []execution.CaseRun{
		{
			ID: 200, CaseVersionID: 100, WaveID: 7, Status: execution.CaseRunFailed,
			Attempts: 2, FailureClass: execution.FailureClassApplication,
			StartedAt: &started, FinishedAt: &finished,
			Summary: execution.CaseRunSummary{EvidenceCount: 3},
		},
		{
			ID: 201, CaseVersionID: 101, WaveID: 7, Status: execution.CaseRunInterrupted,
			Summary: execution.CaseRunSummary{EvidenceCount: 1},
		},
	}}
	provider := NewExecutionProvider(waves, runs, nil)

	waveNodes, err := provider.Waves(context.Background(), 42)
	if err != nil {
		t.Fatalf("Waves: %v", err)
	}
	if waveNodes[0].Meta["wave_status"] != "done" || waveNodes[0].Meta["trigger"] != "auto" {
		t.Errorf("wave meta = %v", waveNodes[0].Meta)
	}

	runNodes, err := provider.CaseRuns(context.Background(), 42)
	if err != nil {
		t.Fatalf("CaseRuns: %v", err)
	}
	if runNodes[0].Meta["attempt"] != 2 || runNodes[0].Meta["failure_class"] != "application" ||
		runNodes[0].Meta["evidence_count"] != 3 || runNodes[0].Meta["duration_ms"] != int64(1500) {
		t.Errorf("case run meta = %v", runNodes[0].Meta)
	}
	if runNodes[1].Result != "interrupted" {
		t.Errorf("interrupted Result = %q, want interrupted", runNodes[1].Result)
	}

	waves.listCalls = 0
	wavesOut, runsOut, err := provider.ExecutionSnapshot(context.Background(), 42)
	if err != nil {
		t.Fatalf("ExecutionSnapshot: %v", err)
	}
	if waves.listCalls != 1 {
		t.Errorf("ExecutionSnapshot wave reads = %d, want 1", waves.listCalls)
	}
	if len(wavesOut) != 1 || len(runsOut) != 2 {
		t.Errorf("snapshot = %d waves / %d runs", len(wavesOut), len(runsOut))
	}
}

type p6IssueReader struct {
	IssueReader
	issues []delivery.Issue
	refs   map[int64][]delivery.IssueRef
}

func (r *p6IssueReader) List(context.Context, int64, deliveryapp.IssueFilter) ([]delivery.Issue, error) {
	return r.issues, nil
}

func (r *p6IssueReader) ListRefs(_ context.Context, issueID int64) ([]delivery.IssueRef, error) {
	return r.refs[issueID], nil
}

type p6ReportReader struct {
	ReportReader
	reports   []delivery.Report
	revisions map[int64][]delivery.ReportRevision
	refs      map[int64][]delivery.ReportRef
}

func (r *p6ReportReader) ListReports(context.Context, int64, deliveryapp.ReportFilter) ([]delivery.Report, error) {
	return r.reports, nil
}

func (r *p6ReportReader) ListRevisions(_ context.Context, reportID int64) ([]delivery.ReportRevision, error) {
	return r.revisions[reportID], nil
}

func (r *p6ReportReader) ListRefs(_ context.Context, reportID int64) ([]delivery.ReportRef, error) {
	return r.refs[reportID], nil
}

func TestP6DeliveryProviderTypedMeta(t *testing.T) {
	merged := int64(999)
	refs := &p6IssueReader{
		issues: []delivery.Issue{{
			ID: 300, Title: "Amount mismatch", Status: delivery.IssueStatusOpen,
			Category: "api", Severity: "major", MergedIntoID: &merged,
		}},
		refs: map[int64][]delivery.IssueRef{300: {
			{Kind: delivery.IssueRefKindCaseRun, RefID: 200},
			{Kind: delivery.IssueRefKindEvidence, RefID: 501},
			{Kind: delivery.IssueRefKindEvidence, RefID: 502},
		}},
	}
	currentRevision := int64(12)
	reports := &p6ReportReader{
		reports: []delivery.Report{{
			ID: 900, Kind: delivery.ReportKindWave, Status: delivery.ReportStatusPublished,
			CurrentRevisionID: &currentRevision,
		}},
		revisions: map[int64][]delivery.ReportRevision{900: {{ID: 12, Revision: 3}}},
		refs:      map[int64][]delivery.ReportRef{},
	}
	provider := NewDeliveryProvider(refs, reports, &stubRequirementLister{}, &stubClosureSummaryReader{})

	issues, err := provider.Issues(context.Background(), 42)
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	meta := issues[0].Meta
	if meta["severity"] != "major" || meta["issue_status"] != "open" || meta["category"] != "api" ||
		meta["evidence_count"] != 2 || meta["code_ref_count"] != 0 || meta["merged_into_id"] != "999" {
		t.Errorf("issue meta = %v", meta)
	}

	reportNodes, err := provider.Reports(context.Background(), 42)
	if err != nil {
		t.Fatalf("Reports: %v", err)
	}
	reportMeta := reportNodes[0].Meta
	if reportMeta["report_kind"] != "wave" || reportMeta["report_status"] != "published" ||
		reportMeta["revision"] != 3 {
		t.Errorf("report meta = %v", reportMeta)
	}
}

type fakeVersionReader struct {
	VersionReader
	view     versionapp.VersionView
	getCalls int
}

func (r *fakeVersionReader) Get(context.Context, int64) (versionapp.VersionView, error) {
	r.getCalls++
	return r.view, nil
}

type fakeSUTReader struct {
	SUTReader
	suts map[int64]resourceapp.SUTView
}

func (r *fakeSUTReader) GetSUT(_ context.Context, id int64) (resourceapp.SUTView, error) {
	return r.suts[id], nil
}

func TestP6ResourceProviderSnapshotReadsVersionOnce(t *testing.T) {
	version := &fakeVersionReader{view: versionapp.VersionView{
		Bindings: []versionapp.BindingView{{ID: 9401}},
	}}
	version.view.Bindings[0].SUT.Name = "Payments"
	version.view.Bindings[0].SUT.ID = "9101"
	version.view.Bindings[0].Pages = []resourceapp.PageView{{ID: "9301", Name: "Checkout page"}}
	suts := &fakeSUTReader{suts: map[int64]resourceapp.SUTView{9101: {
		Name: "Payments canonical",
		Environments: []resourceapp.EnvironmentView{{
			Pages: []resourceapp.PageView{{ID: "9302", Name: "Refund page"}},
		}},
	}}}
	provider := NewResourceProvider(version, suts)

	sutNames, pageNames, err := provider.ResourceSnapshot(context.Background(), 42)
	if err != nil {
		t.Fatalf("ResourceSnapshot: %v", err)
	}
	if version.getCalls != 1 {
		t.Errorf("version reads = %d, want 1", version.getCalls)
	}
	if sutNames[9401] != "Payments canonical" {
		t.Errorf("sut names = %v", sutNames)
	}
	if pageNames[9301] != "Checkout page" || pageNames[9302] != "Refund page" {
		t.Errorf("page names = %v", pageNames)
	}
}
