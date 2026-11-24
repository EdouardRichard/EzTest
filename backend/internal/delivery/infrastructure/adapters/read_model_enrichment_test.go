package adapters

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------------------------------------------------------------------------
// Enrichment fakes (v0.2.4-G T8): the read ports the enriched report consumes.
// ---------------------------------------------------------------------------

// fakeEvidenceRefs serves the evidence_ref reverse lookup (T4 port). It records
// every owner query so a test can prove the production read side is consumed.
type fakeEvidenceRefs struct {
	links map[string][]executionapp.EvidenceRef
	calls []string
}

func evidenceRefKey(kind string, id int64) string {
	return kind + ":" + strconv.FormatInt(id, 10)
}

func (f *fakeEvidenceRefs) ListByOwner(_ context.Context, ownerKind string, ownerID int64) ([]executionapp.EvidenceRef, error) {
	key := evidenceRefKey(ownerKind, ownerID)
	f.calls = append(f.calls, key)
	return append([]executionapp.EvidenceRef(nil), f.links[key]...), nil
}

// fakeSettings serves the report evidence projection cap keys. An unset key
// errors like a settings outage so the fail-safe default path is exercised.
type fakeSettings struct {
	ints  map[string]int64
	err   error
	reads []string
}

func (f *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	f.reads = append(f.reads, key)
	if f.err != nil {
		return 0, f.err
	}
	value, ok := f.ints[key]
	if !ok {
		return 0, fmt.Errorf("settings key %q not set", key)
	}
	return value, nil
}

func (f *fakeSettings) Bool(context.Context, string) (bool, error) { return false, nil }

// ---------------------------------------------------------------------------
// Enrichment fixture: one version with natural-language design/requirement/SUT
// context, two intents with frozen fingerprints, and a failed run with an issue.
// ---------------------------------------------------------------------------

func newEnrichmentFixture() Deps {
	sutRef := int64(700)
	waves := &fakeWaves{waves: []execution.RunWave{{
		ID: 11, VersionID: 7, Status: execution.WaveDone, Trigger: execution.WaveTriggerManual,
		Counts:    execution.WaveCounts{Total: 2, Passed: 1, Failed: 1, BySUT: map[string]execution.SUTCounts{"700": {Total: 2, Passed: 1, Failed: 1}}},
		StartedAt: &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
	}}}
	runs := &fakeCaseRuns{runs: []execution.CaseRun{
		{
			ID: 5001, WaveID: 11, IntentID: 91, CaseVersionID: 3001, Status: execution.CaseRunFailed,
			FailureClass:    execution.FailureClassApplication,
			EvidenceDropped: 2,
			Summary:         execution.CaseRunSummary{StepTotal: 2, StepFailed: 1, AssertionTotal: 2, AssertionFailed: 1, EvidenceCount: 5},
			StartedAt:       &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
		{
			ID: 5002, WaveID: 11, IntentID: 92, CaseVersionID: 3002, Status: execution.CaseRunPassed,
			Summary:   execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1},
			StartedAt: &fixtureTime, FinishedAt: &fixtureAfter, CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
	}}
	results := &fakeResults{
		steps: map[int64][]execution.StepResult{
			5001: {
				{ID: 6001, CaseRunID: 5001, Seq: 1, TargetSUTID: &sutRef, Status: execution.StepFailed},
				{ID: 6002, CaseRunID: 5001, Seq: 2, Status: execution.StepPassed},
			},
		},
		assertions: map[int64][]execution.AssertionResult{
			5001: {
				{ID: 6501, CaseRunID: 5001, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindAPI, Verdict: execution.AssertionFailed, Expected: "200", Actual: "500", Reason: "boom", TargetSUTID: &sutRef},
				{ID: 6502, CaseRunID: 5001, StepSeq: 2, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionPassed},
			},
		},
	}
	evidence := &fakeEvidence{rows: map[int64][]execution.Evidence{
		5001: {
			{ID: 7001, CaseRunID: 5001, SUTID: &sutRef, StepSeq: intPtr(1), AssertionSeq: intPtr(1), Kind: execution.EvidenceScreenshot, SHA256: strings.Repeat("a", 64), SizeBytes: 128},
			{ID: 7002, CaseRunID: 5001, Kind: execution.EvidenceConsole, SHA256: strings.Repeat("b", 64), SizeBytes: 64},
			{ID: 7003, CaseRunID: 5001, Kind: execution.EvidenceNetwork, SHA256: strings.Repeat("c", 64), SizeBytes: 32},
			{ID: 7004, CaseRunID: 5001, Kind: execution.EvidenceHTTP, SHA256: strings.Repeat("d", 64), SizeBytes: 16},
			{ID: 7005, CaseRunID: 5001, Kind: execution.EvidenceText, SHA256: strings.Repeat("e", 64), SizeBytes: 8},
		},
	}}
	suts := &fakeSUTContexts{rows: map[int64][]execution.CaseRunSUTContext{
		5001: {{ID: 1, CaseRunID: 5001, SUTID: 700, Status: execution.SUTContextClosed}},
	}}
	design := &fakeDesign{
		cases: map[int64][]designapp.ExecutableCaseView{7: {
			{
				CaseID: 201, CaseVersionID: 3001, RequirementID: 101, RequirementVersionID: 1001,
				Title: "导出订单", Priority: "P1",
				Description:    "验证运营人员可以导出订单 CSV",
				Preconditions:  "已登录且存在订单",
				SutDescription: "订单系统 Web 端",
				Steps: []designapp.ExecutableStepView{
					{Seq: 1, Action: "打开订单列表页", TargetDescription: "订单列表页顶部工具栏", Expected: "页面加载完成"},
					{Seq: 2, Action: "点击导出按钮", TargetDescription: "工具栏导出按钮", Expected: "弹出下载确认"},
				},
			},
			{CaseID: 202, CaseVersionID: 3002, RequirementID: 101, RequirementVersionID: 1001, Title: "查看订单", Priority: "P2"},
		}},
	}
	requirements := &fakeRequirements{
		views: map[int64][]versionapp.RequirementView{7: {
			{ID: 101, CurrentVersionID: 1001, Title: "需求 A", Status: "closed", Waived: true, UpdatedAt: fixtureAfter, ClosedAt: &fixtureAfter},
		}},
		details: map[int64]versionapp.RequirementDetail{
			101: {
				RequirementView: versionapp.RequirementView{ID: 101, CurrentVersionID: 1001, Title: "需求 A", Status: "closed", Waived: true},
				CurrentVersion: versionapp.RequirementVersionView{
					ID: 1001, RequirementID: 101, Revision: 1, Title: "需求 A",
					UserStory: versionapp.RequirementUserStoryView{AsA: "运营人员", IWant: "导出订单", SoThat: "对账"},
					AcceptanceCriteria: []versionapp.RequirementAcceptanceCriterionView{
						{ID: "AC-1", Given: "已登录", When: "点击导出", Then: "下载 CSV"},
					},
					SutDescription: "订单系统 Web 端",
				},
			},
		},
	}
	closures := &fakeClosures{latest: map[int64]version.RequirementClosure{
		101: {ID: 9001, RequirementID: 101, RequirementVersionID: 1001, CloseRound: 1, ClosedAt: fixtureAfter},
	}}
	versions := &fakeVersions{views: map[int64]versionapp.VersionView{7: {
		ID: 7, Title: "Version", Status: "completed", CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		EnvironmentID: 710, ClosureKind: version.ClosureKindWaived, Unverified: true,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 7, EnvironmentID: 710,
			Environment: resourceapp.EnvironmentView{
				ID: "710", Name: "dev",
				SUTs: []resourceapp.SUTView{{ID: "700", EnvironmentID: "710", Name: "订单系统", Intro: "面向内部的订单管理系统"}},
			},
		},
	}}}
	issues := &fakeIssues{
		rows: map[int64][]delivery.Issue{7: {
			{ID: 8001, VersionID: 7, Title: "Issue A", Status: delivery.IssueStatusOpen, Category: "api", Severity: "major", Source: "failure_candidate", DedupeKey: strings.Repeat("f", 32), CreatedAt: fixtureTime, UpdatedAt: fixtureAfter},
		}},
		refs: map[int64][]delivery.IssueRef{
			8001: {{ID: 1, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5001}},
		},
	}
	intents := &fakeIntents{byID: map[int64]execution.ExecutionIntent{
		91: {ID: 91, VersionID: 7, CaseVersionID: 3001, EnvironmentFingerprint: strings.Repeat("a", 64), CodeBaseline: strings.Repeat("b", 40)},
		92: {ID: 92, VersionID: 7, CaseVersionID: 3002, EnvironmentFingerprint: strings.Repeat("c", 64), CodeBaseline: strings.Repeat("d", 40)},
	}}
	return Deps{
		Versions:     versions,
		Requirements: requirements,
		Closures:     closures,
		Design:       design,
		Waves:        waves,
		CaseRuns:     runs,
		Results:      results,
		Evidence:     evidence,
		SUTContexts:  suts,
		Intents:      intents,
		Humans:       &fakeHumans{},
		Issues:       issues,
	}
}

func runByID(snapshot delivery.ReportSnapshot, id int64) (delivery.CaseRunSnapshot, bool) {
	for _, run := range snapshot.CaseRuns {
		if run.ID == id {
			return run, true
		}
	}
	return delivery.CaseRunSnapshot{}, false
}

func issueByID(snapshot delivery.ReportSnapshot, id int64) (delivery.IssueSnapshot, bool) {
	for _, issue := range snapshot.Issues {
		if issue.ID == id {
			return issue, true
		}
	}
	return delivery.IssueSnapshot{}, false
}

// ---------------------------------------------------------------------------
// TestCardNLContextPresent (SC-G2 C2): the defect card snapshot carries the
// natural-language context a human needs — requirement user story, case
// description/preconditions, step action/target_description and the SUT
// description — without looking up any ID.
// ---------------------------------------------------------------------------

func TestCardNLContextPresent(t *testing.T) {
	deps := newEnrichmentFixture()
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	run, ok := runByID(snapshot, 5001)
	if !ok {
		t.Fatal("run 5001 missing from snapshot")
	}
	if run.CaseDescription != "验证运营人员可以导出订单 CSV" {
		t.Fatalf("case description = %q", run.CaseDescription)
	}
	if run.CasePreconditions != "已登录且存在订单" {
		t.Fatalf("case preconditions = %q", run.CasePreconditions)
	}
	if run.SUTDescription != "订单系统 Web 端" {
		t.Fatalf("case sut_description = %q", run.SUTDescription)
	}
	if len(run.Steps) != 2 {
		t.Fatalf("steps = %+v, want the two design steps", run.Steps)
	}
	if run.Steps[0].Seq != 1 || run.Steps[0].Action != "打开订单列表页" || run.Steps[0].TargetDescription != "订单列表页顶部工具栏" {
		t.Fatalf("step 1 = %+v", run.Steps[0])
	}
	if run.Steps[1].Seq != 2 || run.Steps[1].Action != "点击导出按钮" || run.Steps[1].TargetDescription != "工具栏导出按钮" {
		t.Fatalf("step 2 = %+v", run.Steps[1])
	}
	if len(snapshot.Requirements) != 1 {
		t.Fatalf("requirements = %+v", snapshot.Requirements)
	}
	requirement := snapshot.Requirements[0]
	if requirement.UserStory.AsA != "运营人员" || requirement.UserStory.IWant != "导出订单" || requirement.UserStory.SoThat != "对账" {
		t.Fatalf("requirement user_story = %+v", requirement.UserStory)
	}
	if requirement.SutDescription != "订单系统 Web 端" {
		t.Fatalf("requirement sut_description = %q", requirement.SutDescription)
	}
	if len(snapshot.SUTs) != 1 || snapshot.SUTs[0].SUTDescription != "面向内部的订单管理系统" {
		t.Fatalf("SUT description = %+v, want the resource SUT intro", snapshot.SUTs)
	}

	// The wave report carries the same run-level NL context.
	wave, err := NewReportDataReader(deps).WaveSnapshot(context.Background(), 7, 11)
	if err != nil {
		t.Fatalf("WaveSnapshot: %v", err)
	}
	waveRun, ok := runByID(wave, 5001)
	if !ok || waveRun.CaseDescription != "验证运营人员可以导出订单 CSV" || len(waveRun.Steps) != 2 {
		t.Fatalf("wave run NL context = %+v", waveRun)
	}
}

// TestCardNLContextReadFailureFailsClosed: a requirement detail read failure
// must fail the snapshot build (no silently missing NL context).
func TestCardNLContextReadFailureFailsClosed(t *testing.T) {
	deps := newEnrichmentFixture()
	deps.Requirements.(*fakeRequirements).getErr = fmt.Errorf("version detail unavailable")
	if _, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7); err == nil {
		t.Fatal("VersionSnapshot with a failing requirement detail read = nil error, want fail-closed")
	}
}

// ---------------------------------------------------------------------------
// TestNonIssueFailureProjected (SC-G2 C1): terminal runs that never create an
// issue (blocked/inconclusive/environment/...) carry a failure-reason
// projection so a human can ask "why did case X not pass".
// ---------------------------------------------------------------------------

func TestNonIssueFailureProjected(t *testing.T) {
	deps := newEnrichmentFixture()
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs = append(runs.runs,
		execution.CaseRun{
			ID: 5003, WaveID: 11, IntentID: 92, CaseVersionID: 3002, Status: execution.CaseRunBlocked,
			FailureClass: execution.FailureClassBlocked,
			Summary:      execution.CaseRunSummary{TerminalCause: execution.TerminalCauseExecutorCapability},
			CreatedAt:    fixtureTime, UpdatedAt: fixtureTime,
		},
		execution.CaseRun{
			ID: 5004, WaveID: 11, IntentID: 92, CaseVersionID: 3002, Status: execution.CaseRunInconclusive,
			FailureClass: execution.FailureClassUnknown,
			Summary:      execution.CaseRunSummary{TerminalCause: execution.TerminalCauseTurnBudget},
			CreatedAt:    fixtureTime, UpdatedAt: fixtureTime,
		},
		execution.CaseRun{
			ID: 5006, WaveID: 11, IntentID: 92, CaseVersionID: 3002, Status: execution.CaseRunFailed,
			FailureClass: execution.FailureClassEnvironment,
			CreatedAt:    fixtureTime, UpdatedAt: fixtureTime,
		},
	)
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	byRun := make(map[int64]delivery.FailureExplanationSnapshot, len(snapshot.FailureExplanations))
	for _, explanation := range snapshot.FailureExplanations {
		byRun[explanation.CaseRunID] = explanation
	}
	// The failed(application) run WITH an issue is explained by its defect card,
	// not by the non-issue failure section; the passed run is not a failure.
	if _, ok := byRun[5001]; ok {
		t.Fatalf("run 5001 has an issue but got a non-issue explanation: %+v", byRun[5001])
	}
	if _, ok := byRun[5002]; ok {
		t.Fatalf("passed run 5002 got a failure explanation: %+v", byRun[5002])
	}
	blocked, ok := byRun[5003]
	if !ok {
		t.Fatalf("blocked run 5003 has no failure explanation; explanations = %+v", snapshot.FailureExplanations)
	}
	if blocked.EffectiveFailureClass != delivery.FailureClassBlocked || blocked.TerminalCause != execution.TerminalCauseExecutorCapability {
		t.Fatalf("blocked explanation = %+v", blocked)
	}
	if blocked.Explanation == "" || blocked.CaseTitle != "查看订单" {
		t.Fatalf("blocked explanation text/title = %+v", blocked)
	}
	inconclusive, ok := byRun[5004]
	if !ok {
		t.Fatalf("inconclusive run 5004 has no failure explanation")
	}
	if inconclusive.TerminalCause != execution.TerminalCauseTurnBudget || inconclusive.Explanation == "" {
		t.Fatalf("inconclusive explanation = %+v", inconclusive)
	}
	environment, ok := byRun[5006]
	if !ok {
		t.Fatalf("failed(environment) run 5006 has no failure explanation")
	}
	if environment.EffectiveFailureClass != string(execution.FailureClassEnvironment) || environment.Explanation == "" {
		t.Fatalf("environment explanation = %+v", environment)
	}
}

// ---------------------------------------------------------------------------
// TestEvidenceProjectionBounded (SC-G7 R-G2/R-C I-4): the display projection is
// bounded by a per-run AND an aggregate cap with explicit truncation and total
// counts, every displayed row is viewable, and the fingerprint digest stays
// causally complete over ALL evidence regardless of the display cap.
// ---------------------------------------------------------------------------

func TestEvidenceProjectionBounded(t *testing.T) {
	deps := newEnrichmentFixture()
	settings := &fakeSettings{ints: map[string]int64{
		"report.evidence_max_per_case_run": 2,
		"report.evidence_max_total":        3,
	}}
	deps.Settings = settings
	// A second run with evidence so the aggregate cap binds across runs.
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs = append(runs.runs, execution.CaseRun{
		ID: 5007, WaveID: 11, IntentID: 92, CaseVersionID: 3002, Status: execution.CaseRunPassed,
		Summary: execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1}, CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
	})
	deps.Evidence.(*fakeEvidence).rows[5007] = []execution.Evidence{
		{ID: 7010, CaseRunID: 5007, Kind: execution.EvidenceScreenshot, SHA256: strings.Repeat("1", 64), SizeBytes: 10},
		{ID: 7011, CaseRunID: 5007, Kind: execution.EvidenceConsole, SHA256: strings.Repeat("2", 64), SizeBytes: 10},
		{ID: 7012, CaseRunID: 5007, Kind: execution.EvidenceNetwork, SHA256: strings.Repeat("3", 64), SizeBytes: 10},
	}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	run5001, _ := runByID(snapshot, 5001)
	run5007, _ := runByID(snapshot, 5007)
	// Per-run cap 2: run 5001 shows 2 of its 5 rows; the aggregate cap 3 leaves
	// run 5007 only 1 display row. Totals stay the full counts.
	if len(run5001.Evidence) != 2 || run5001.EvidenceTotalCount() != 5 || !run5001.EvidenceTruncated() {
		t.Fatalf("run 5001 evidence display = %d rows, total %d, truncated %v; want 2/5/true",
			len(run5001.Evidence), run5001.EvidenceTotalCount(), run5001.EvidenceTruncated())
	}
	if len(run5007.Evidence) != 1 || run5007.EvidenceTotalCount() != 3 || !run5007.EvidenceTruncated() {
		t.Fatalf("run 5007 evidence display = %d rows, total %d, truncated %v; want 1/3/true (aggregate cap)",
			len(run5007.Evidence), run5007.EvidenceTotalCount(), run5007.EvidenceTruncated())
	}
	if snapshot.EvidenceTotalCount != 8 {
		t.Fatalf("snapshot evidence total = %d, want 8 (5+3 before caps)", snapshot.EvidenceTotalCount)
	}
	truncated := false
	for _, section := range snapshot.TruncatedSections {
		if section == delivery.ReportTruncationEvidence {
			truncated = true
		}
	}
	if !truncated {
		t.Fatalf("truncated sections = %v, want the evidence marker", snapshot.TruncatedSections)
	}
	// Every displayed row carries the viewable content link (existing endpoint).
	for _, row := range run5001.Evidence {
		want := fmt.Sprintf("/api/versions/7/evidence/%d/content", row.ID)
		if row.ContentPath != want {
			t.Fatalf("evidence %d content path = %q, want %q", row.ID, row.ContentPath, want)
		}
	}
	// The caps were read from the settings center (production read side).
	readPerRun, readTotal := false, false
	for _, key := range settings.reads {
		if key == "report.evidence_max_per_case_run" {
			readPerRun = true
		}
		if key == "report.evidence_max_total" {
			readTotal = true
		}
	}
	if !readPerRun || !readTotal {
		t.Fatalf("settings reads = %v, want both evidence cap keys", settings.reads)
	}

	// Causal completeness: the fingerprint digest covers ALL evidence, not the
	// capped display sample. Changing a beyond-cap sha must change the
	// fingerprint; changing only the display cap must not.
	cappedFingerprint := delivery.Fingerprint(snapshot)
	fullDeps := newEnrichmentFixture()
	fullRuns := fullDeps.CaseRuns.(*fakeCaseRuns)
	fullRuns.runs = append(fullRuns.runs, runs.runs[len(runs.runs)-1])
	fullDeps.Evidence.(*fakeEvidence).rows[5007] = deps.Evidence.(*fakeEvidence).rows[5007]
	fullSnapshot, err := NewReportDataReader(fullDeps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("full-display VersionSnapshot: %v", err)
	}
	if got := delivery.Fingerprint(fullSnapshot); got != cappedFingerprint {
		t.Fatalf("display cap changed the fingerprint: %s != %s (digest must be display-independent)", got, cappedFingerprint)
	}
	// Mutate the LAST (beyond-cap) evidence sha of run 5001: the digest must move.
	mutatedDeps := newEnrichmentFixture()
	mutatedRuns := mutatedDeps.CaseRuns.(*fakeCaseRuns)
	mutatedRuns.runs = append(mutatedRuns.runs, runs.runs[len(runs.runs)-1])
	mutatedDeps.Evidence.(*fakeEvidence).rows[5007] = deps.Evidence.(*fakeEvidence).rows[5007]
	mutatedDeps.Settings = settings
	mutatedEvidence := mutatedDeps.Evidence.(*fakeEvidence).rows[5001]
	mutatedEvidence[4].SHA256 = strings.Repeat("9", 64)
	mutatedSnapshot, err := NewReportDataReader(mutatedDeps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("mutated VersionSnapshot: %v", err)
	}
	if got := delivery.Fingerprint(mutatedSnapshot); got == cappedFingerprint {
		t.Fatal("changing a beyond-display-cap evidence sha did not change the fingerprint (capped digest?)")
	}
}

// TestEvidenceProjectionDefaultsAndUnlimited: without settings the projection
// keeps the bounded defaults (100 per run / 2000 aggregate); 0 means unlimited
// (the report cap never reverse-limits the write side, C R-C5).
func TestEvidenceProjectionDefaultsAndUnlimited(t *testing.T) {
	deps := newEnrichmentFixture()
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	run, _ := runByID(snapshot, 5001)
	if len(run.Evidence) != 5 || run.EvidenceTruncated() {
		t.Fatalf("default display = %d rows truncated %v, want all 5 rows", len(run.Evidence), run.EvidenceTruncated())
	}
	deps.Settings = &fakeSettings{ints: map[string]int64{
		"report.evidence_max_per_case_run": 0,
		"report.evidence_max_total":        0,
	}}
	unlimited, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("unlimited VersionSnapshot: %v", err)
	}
	unlimitedRun, _ := runByID(unlimited, 5001)
	if len(unlimitedRun.Evidence) != 5 || unlimitedRun.EvidenceTruncated() {
		t.Fatalf("0=unlimited display = %d rows truncated %v, want all 5 rows", len(unlimitedRun.Evidence), unlimitedRun.EvidenceTruncated())
	}
	for _, section := range unlimited.TruncatedSections {
		if section == delivery.ReportTruncationEvidence {
			t.Fatalf("unlimited projection marked evidence truncated: %v", unlimited.TruncatedSections)
		}
	}
}

// TestEvidenceCapDoesNotLimitIssueIngestion (C R-C5): the report display cap
// must not reverse-limit the write-side issue candidate evidence ingestion.
func TestEvidenceCapDoesNotLimitIssueIngestion(t *testing.T) {
	deps := newEnrichmentFixture()
	deps.Settings = &fakeSettings{ints: map[string]int64{
		"report.evidence_max_per_case_run": 1,
		"report.evidence_max_total":        1,
	}}
	snapshots, err := NewExecutionReader(deps).ListFailedApplicationCaseRuns(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListFailedApplicationCaseRuns: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	if len(snapshots[0].EvidenceIDs) != 5 {
		t.Fatalf("issue candidate evidence ids = %v, want all 5 (report display cap must not limit ingestion)", snapshots[0].EvidenceIDs)
	}
}

// ---------------------------------------------------------------------------
// TestMergedIssueRepresentativeRun (SC-G2 I4): an issue linking N runs takes
// its five-element expected/observed from ONE defined representative run
// (newest failed), never mixing runs; the other linked runs stay listed.
// ---------------------------------------------------------------------------

func TestMergedIssueRepresentativeRun(t *testing.T) {
	deps := newEnrichmentFixture()
	runs := deps.CaseRuns.(*fakeCaseRuns)
	// A newer failed run (5005) and an even newer passed run (5007) both link
	// to issue 8001: the representative must be the newest FAILED run (5005),
	// not the newest run overall, and never the older 5001.
	runs.runs = append(runs.runs,
		execution.CaseRun{
			ID: 5005, WaveID: 11, IntentID: 91, CaseVersionID: 3001, Status: execution.CaseRunFailed,
			FailureClass: execution.FailureClassApplication,
			Summary:      execution.CaseRunSummary{StepTotal: 2, StepFailed: 1, AssertionTotal: 1, AssertionFailed: 1},
			CreatedAt:    fixtureTime, UpdatedAt: fixtureAfter,
		},
		execution.CaseRun{
			ID: 5007, WaveID: 11, IntentID: 92, CaseVersionID: 3002, Status: execution.CaseRunPassed,
			Summary:   execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1},
			CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		},
	)
	results := deps.Results.(*fakeResults)
	results.steps[5005] = []execution.StepResult{{ID: 6005, CaseRunID: 5005, Seq: 2, Status: execution.StepFailed}}
	results.assertions[5005] = []execution.AssertionResult{
		{ID: 6505, CaseRunID: 5005, StepSeq: 2, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionFailed, Expected: "CSV 文件", Actual: "错误页", Reason: "export broken"},
	}
	issues := deps.Issues.(*fakeIssues)
	issues.refs[8001] = []delivery.IssueRef{
		{ID: 1, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5001},
		{ID: 3, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5007},
		{ID: 2, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5005},
	}
	// evidence_ref reverse lookup: the representative's failed assertion and
	// failed step bind viewable evidence (T4 port production consumption).
	refs := &fakeEvidenceRefs{links: map[string][]executionapp.EvidenceRef{
		evidenceRefKey(execution.EvidenceOwnerAssertionResult, 6505): {
			{Ref: execution.EvidenceRef{ID: 801, EvidenceID: 7005, OwnerKind: execution.EvidenceOwnerAssertionResult, OwnerID: 6505, Role: "failure"},
				Evidence: execution.Evidence{ID: 7005, CaseRunID: 5005, Kind: execution.EvidenceScreenshot, SHA256: strings.Repeat("e", 64), SizeBytes: 8}},
		},
		evidenceRefKey(execution.EvidenceOwnerStepResult, 6005): {
			{Ref: execution.EvidenceRef{ID: 802, EvidenceID: 7006, OwnerKind: execution.EvidenceOwnerStepResult, OwnerID: 6005, Role: "attachment"},
				Evidence: execution.Evidence{ID: 7006, CaseRunID: 5005, Kind: execution.EvidenceConsole, SHA256: strings.Repeat("6", 64), SizeBytes: 4}},
		},
	}}
	deps.EvidenceRefs = refs

	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	issue, ok := issueByID(snapshot, 8001)
	if !ok {
		t.Fatal("issue 8001 missing")
	}
	if issue.RepresentativeCaseRunID != 5005 {
		t.Fatalf("representative run = %d, want 5005 (newest failed)", issue.RepresentativeCaseRunID)
	}
	if issue.FailedExpected != "CSV 文件" || issue.FailedActual != "错误页" || issue.FailedReason != "export broken" {
		t.Fatalf("five-element detail = %q/%q/%q, want the representative run's (no mixing with 5001's 200/500/boom)",
			issue.FailedExpected, issue.FailedActual, issue.FailedReason)
	}
	if len(issue.CaseRunIDs) != 3 {
		t.Fatalf("linked runs = %v, want all three listed", issue.CaseRunIDs)
	}
	// The reverse lookup consumed the evidence_ref read port for the
	// representative's failed assertion AND failed step, with viewable links.
	if len(issue.RepresentativeEvidence) != 2 {
		t.Fatalf("representative evidence = %+v, want the assertion+step bound rows", issue.RepresentativeEvidence)
	}
	for _, row := range issue.RepresentativeEvidence {
		if row.ContentPath != fmt.Sprintf("/api/versions/7/evidence/%d/content", row.ID) {
			t.Fatalf("representative evidence %d content path = %q", row.ID, row.ContentPath)
		}
	}
	sawAssertion, sawStep := false, false
	for _, call := range refs.calls {
		if call == evidenceRefKey(execution.EvidenceOwnerAssertionResult, 6505) {
			sawAssertion = true
		}
		if call == evidenceRefKey(execution.EvidenceOwnerStepResult, 6005) {
			sawStep = true
		}
	}
	if !sawAssertion || !sawStep {
		t.Fatalf("evidence_ref calls = %v, want the representative assertion+step reverse lookups", refs.calls)
	}

	// Deterministic: ref order must not change the representative.
	issues.refs[8001] = []delivery.IssueRef{
		{ID: 2, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5005},
		{ID: 1, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5001},
		{ID: 3, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5007},
	}
	reordered, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("reordered VersionSnapshot: %v", err)
	}
	reorderedIssue, _ := issueByID(reordered, 8001)
	if reorderedIssue.RepresentativeCaseRunID != 5005 || reorderedIssue.FailedExpected != "CSV 文件" {
		t.Fatalf("reordered representative = %+v, want the same 5005/CSV 文件", reorderedIssue)
	}
}

// ---------------------------------------------------------------------------
// TestEvidenceCapSettingRegistered (SC-G7 M3): the cap keys are registered in
// the settings catalog with bounded defaults and a production read side.
// ---------------------------------------------------------------------------

func TestEvidenceCapSettingRegistered(t *testing.T) {
	deps := newEnrichmentFixture()
	settings := &fakeSettings{ints: map[string]int64{
		"report.evidence_max_per_case_run": 1,
		"report.evidence_max_total":        100,
	}}
	deps.Settings = settings
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	run, _ := runByID(snapshot, 5001)
	if len(run.Evidence) != 1 {
		t.Fatalf("configured per-run cap not honored: %d rows, want 1", len(run.Evidence))
	}
	// The catalog registration itself is pinned in the settings package tests
	// (TestGReportEvidenceCapSettingsRegistered); here we pin the production
	// READ side: both keys are consumed through the settings port.
	if len(settings.reads) == 0 {
		t.Fatal("the read model never read the evidence cap settings")
	}
}

// ---------------------------------------------------------------------------
// T7 carrier population (report §1.2 handoff): per-run actual fingerprint via
// IntentReader.GetByID (frozen, not recomputed), evidence dropped count,
// requirement waived/AC hash, issue dedupe key, version closure kind.
// ---------------------------------------------------------------------------

func TestPerRunFingerprintCarriersPopulated(t *testing.T) {
	deps := newEnrichmentFixture()
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	failed, _ := runByID(snapshot, 5001)
	passed, _ := runByID(snapshot, 5002)
	if failed.EnvironmentFingerprint != strings.Repeat("a", 64) || failed.CodeBaseline != strings.Repeat("b", 40) {
		t.Fatalf("failed run fp carriers = %q/%q, want intent 91's frozen values", failed.EnvironmentFingerprint, failed.CodeBaseline)
	}
	if passed.EnvironmentFingerprint != strings.Repeat("c", 64) || passed.CodeBaseline != strings.Repeat("d", 40) {
		t.Fatalf("passed run fp carriers = %q/%q, want intent 92's frozen values (every run carries its actual fp)", passed.EnvironmentFingerprint, passed.CodeBaseline)
	}
	if failed.EvidenceDroppedCount != 2 {
		t.Fatalf("evidence dropped count = %d, want the run fact 2", failed.EvidenceDroppedCount)
	}
	// The carriers are fingerprint conclusion inputs: changing the intent's
	// frozen fp changes the report fingerprint through the real read path.
	baseline := delivery.Fingerprint(snapshot)
	deps.Intents.(*fakeIntents).byID[91] = execution.ExecutionIntent{
		ID: 91, VersionID: 7, CaseVersionID: 3001,
		EnvironmentFingerprint: strings.Repeat("9", 64), CodeBaseline: strings.Repeat("b", 40),
	}
	changed, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("changed VersionSnapshot: %v", err)
	}
	if delivery.Fingerprint(changed) == baseline {
		t.Fatal("changing the intent frozen fp did not change the report fingerprint")
	}
}

func TestRequirementAndVersionCarriersPopulated(t *testing.T) {
	deps := newEnrichmentFixture()
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.ClosureKind != version.ClosureKindWaived || !snapshot.Unverified {
		t.Fatalf("version closure carriers = %q/%v, want waived/true", snapshot.ClosureKind, snapshot.Unverified)
	}
	requirement := snapshot.Requirements[0]
	if !requirement.Waived {
		t.Fatal("requirement waived carrier = false, want the view flag true")
	}
	// ACHash uses the version domain AC normalization (single source): it equals
	// the domain hash of the current revision's criteria, and whitespace-only
	// variants normalize to the same hash.
	wantHash, err := version.HashAcceptanceCriteria([]version.AcceptanceCriterion{
		{ID: "AC-1", Given: "已登录", When: "点击导出", Then: "下载 CSV"},
	})
	if err != nil {
		t.Fatalf("HashAcceptanceCriteria: %v", err)
	}
	if requirement.ACHash != wantHash {
		t.Fatalf("AC hash = %q, want the version domain normalized hash %q", requirement.ACHash, wantHash)
	}
	normalized, err := version.HashAcceptanceCriteria([]version.AcceptanceCriterion{
		{ID: "AC-1", Given: "  已登录  ", When: " 点击导出 ", Then: " 下载  CSV "},
	})
	if err != nil {
		t.Fatalf("HashAcceptanceCriteria(normalized): %v", err)
	}
	if normalized != wantHash {
		t.Fatalf("AC hash normalization drifted: %q != %q", normalized, wantHash)
	}
	issue, _ := issueByID(snapshot, 8001)
	if issue.DedupeKey != strings.Repeat("f", 32) {
		t.Fatalf("issue dedupe key carrier = %q, want the issue row key", issue.DedupeKey)
	}
	// The carriers are conclusion inputs: an AC content change moves the hash
	// and the fingerprint through the real read path.
	baseline := delivery.Fingerprint(snapshot)
	detail := deps.Requirements.(*fakeRequirements).details[101]
	detail.CurrentVersion.AcceptanceCriteria[0].Then = "下载 XLSX"
	deps.Requirements.(*fakeRequirements).details[101] = detail
	changed, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("changed VersionSnapshot: %v", err)
	}
	if delivery.Fingerprint(changed) == baseline {
		t.Fatal("an AC content change did not change the report fingerprint")
	}
}

// ---------------------------------------------------------------------------
// Fix round 1 (R-G8b / C3 / IMPORTANT #3 / minors).
// ---------------------------------------------------------------------------

// TestHistoricalCaseRunKeepsFullNL (R-G8b): a run referencing a superseded case
// version keeps the full card NL context (description/preconditions/steps) — the
// immutable historical row carries it; the gap was read-port width, not data
// absence. Once a case is edited, prior runs' cards must NOT revert to title-only.
func TestHistoricalCaseRunKeepsFullNL(t *testing.T) {
	deps := newEnrichmentFixture()
	design := deps.Design.(*fakeDesign)
	design.history = map[int64][]designapp.CaseVersionHistoryView{7: {{
		ID: 3003, CaseID: 203, Revision: 1, Title: "历史导出用例", Status: "superseded",
		Description:    "验证旧版导出流程",
		Preconditions:  "已登录旧版系统",
		SutDescription: "订单系统旧版 Web 端",
		Steps: []designapp.ExecutableStepView{
			{Seq: 1, Action: "打开旧版订单页", TargetDescription: "旧版订单列表", Expected: "页面加载"},
		},
	}}}
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs = append(runs.runs, execution.CaseRun{
		ID: 5008, WaveID: 11, IntentID: 91, CaseVersionID: 3003, Status: execution.CaseRunFailed,
		FailureClass: execution.FailureClassApplication,
		Summary:      execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1, AssertionFailed: 1},
		CreatedAt:    fixtureTime, UpdatedAt: fixtureAfter,
	})
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	run, ok := runByID(snapshot, 5008)
	if !ok {
		t.Fatal("historical run 5008 missing")
	}
	if run.CaseTitle != "历史导出用例" {
		t.Fatalf("historical title = %q", run.CaseTitle)
	}
	if run.CaseDescription != "验证旧版导出流程" || run.CasePreconditions != "已登录旧版系统" || run.SUTDescription != "订单系统旧版 Web 端" {
		t.Fatalf("historical NL = %q/%q/%q, want full context (not title-only)", run.CaseDescription, run.CasePreconditions, run.SUTDescription)
	}
	if len(run.Steps) != 1 || run.Steps[0].Action != "打开旧版订单页" || run.Steps[0].TargetDescription != "旧版订单列表" {
		t.Fatalf("historical steps = %+v, want the superseded row's steps", run.Steps)
	}
}

// TestAggregateCapKeepsNewestFailureAndCardsViewable (C3, IMPORTANT #2): when the
// version exceeds the aggregate evidence cap, the newest FAILED run still gets
// viewable display evidence (failure-first/newest-first allocation, not ascending
// first-come), and defect cards keep viewable representative evidence via an
// INDEPENDENT card budget.
func TestAggregateCapKeepsNewestFailureAndCardsViewable(t *testing.T) {
	deps := newEnrichmentFixture()
	// Oldest run 5001 becomes a PASSED run with 10 evidence rows; the newest run
	// 5005 is a FAILED application run with an issue. Aggregate cap 2 / per-run 2:
	// ascending first-come would give the old passed run the whole budget and the
	// newest failure 0; failure-first must give the failure its evidence.
	runs := deps.CaseRuns.(*fakeCaseRuns)
	for i := range runs.runs {
		if runs.runs[i].ID == 5001 {
			runs.runs[i].Status = execution.CaseRunPassed
			runs.runs[i].FailureClass = ""
		}
	}
	oldEvidence := make([]execution.Evidence, 0, 10)
	for i := 0; i < 10; i++ {
		oldEvidence = append(oldEvidence, execution.Evidence{
			ID: int64(7100 + i), CaseRunID: 5001, Kind: execution.EvidenceScreenshot,
			SHA256: strings.Repeat(strconv.Itoa(i%10), 64), SizeBytes: 10,
		})
	}
	deps.Evidence.(*fakeEvidence).rows[5001] = oldEvidence
	runs.runs = append(runs.runs, execution.CaseRun{
		ID: 5005, WaveID: 11, IntentID: 91, CaseVersionID: 3001, Status: execution.CaseRunFailed,
		FailureClass: execution.FailureClassApplication,
		Summary:      execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1, AssertionFailed: 1},
		CreatedAt:    fixtureTime, UpdatedAt: fixtureAfter,
	})
	deps.Evidence.(*fakeEvidence).rows[5005] = []execution.Evidence{
		{ID: 7005, CaseRunID: 5005, Kind: execution.EvidenceScreenshot, SHA256: strings.Repeat("e", 64), SizeBytes: 8},
		{ID: 7006, CaseRunID: 5005, Kind: execution.EvidenceConsole, SHA256: strings.Repeat("6", 64), SizeBytes: 4},
	}
	results := deps.Results.(*fakeResults)
	results.steps[5005] = []execution.StepResult{{ID: 6005, CaseRunID: 5005, Seq: 1, Status: execution.StepFailed}}
	results.assertions[5005] = []execution.AssertionResult{
		{ID: 6505, CaseRunID: 5005, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionFailed, Expected: "CSV", Actual: "错误", Reason: "broken"},
	}
	issues := deps.Issues.(*fakeIssues)
	issues.refs[8001] = []delivery.IssueRef{{ID: 1, IssueID: 8001, Kind: delivery.IssueRefKindCaseRun, RefID: 5005}}
	deps.EvidenceRefs = &fakeEvidenceRefs{links: map[string][]executionapp.EvidenceRef{
		evidenceRefKey(execution.EvidenceOwnerAssertionResult, 6505): {
			{Ref: execution.EvidenceRef{ID: 801, EvidenceID: 7005, OwnerKind: execution.EvidenceOwnerAssertionResult, OwnerID: 6505},
				Evidence: execution.Evidence{ID: 7005, CaseRunID: 5005, Kind: execution.EvidenceScreenshot, SHA256: strings.Repeat("e", 64), SizeBytes: 8}},
		},
	}}
	deps.Settings = &fakeSettings{ints: map[string]int64{
		"report.evidence_max_per_case_run": 2,
		"report.evidence_max_total":        2,
	}}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	failed, _ := runByID(snapshot, 5005)
	if len(failed.Evidence) == 0 {
		t.Fatal("newest failed run 5005 got 0 display evidence under the aggregate cap (ascending first-come starved the failure?)")
	}
	for _, row := range failed.Evidence {
		if row.ContentPath == "" {
			t.Fatalf("failed run evidence %d has no viewable link", row.ID)
		}
	}
	issue, _ := issueByID(snapshot, 8001)
	if len(issue.RepresentativeEvidence) == 0 {
		t.Fatal("defect card representative evidence cleared by the run-level aggregate cap (card budget must be independent)")
	}
}

// TestCardEvidenceBudgetNewestFailureFirst pins final fix wave #6 (art.7.6): the
// independent card evidence budget is allocated NEWEST/FAILURE-FIRST (mirroring
// the run-level evidenceAllocationPriority), not ascending-issue first-come. With
// many issues whose combined representative evidence exceeds the aggregate card
// cap, the NEWEST defect card (the one the owner cares about) still has viewable
// evidence, and a starved older card discloses truncation
// (RepresentativeEvidenceTruncated) instead of falsely claiming 无代表 run 证据.
func TestCardEvidenceBudgetNewestFailureFirst(t *testing.T) {
	deps := newEnrichmentFixture()
	runs := deps.CaseRuns.(*fakeCaseRuns)
	results := deps.Results.(*fakeResults)
	evidence := deps.Evidence.(*fakeEvidence)
	issues := deps.Issues.(*fakeIssues)
	refs := &fakeEvidenceRefs{links: map[string][]executionapp.EvidenceRef{}}
	deps.EvidenceRefs = refs

	// Three failed application runs (ascending ID), each with its own issue and
	// two representative evidence rows. Issue IDs ascend with run IDs, so an
	// ascending-issue first-come budget would feed the OLDEST card and starve the
	// newest; newest-first must feed the newest card. All three runs cover the
	// fixture's confirmed case 3001 (its NL context is present).
	runs.runs = runs.runs[:0]
	issueRows := make([]delivery.Issue, 0, 3)
	issueRefs := make(map[int64][]delivery.IssueRef, 3)
	for i := 0; i < 3; i++ {
		runID := int64(5001 + i)
		caseVersionID := int64(3001)
		assertionID := int64(6501 + i)
		issueID := int64(8001 + i)
		runs.runs = append(runs.runs, execution.CaseRun{
			ID: runID, WaveID: 11, IntentID: 91, CaseVersionID: caseVersionID, Status: execution.CaseRunFailed,
			FailureClass: execution.FailureClassApplication,
			Summary:      execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1, AssertionFailed: 1},
			CreatedAt:    fixtureTime, UpdatedAt: fixtureAfter,
		})
		results.assertions[runID] = []execution.AssertionResult{
			{ID: assertionID, CaseRunID: runID, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionFailed, Expected: "ok", Actual: "broken", Reason: "boom"},
		}
		evRows := make([]execution.Evidence, 0, 2)
		for j := 0; j < 2; j++ {
			evID := runID*10 + int64(j)
			evRows = append(evRows, execution.Evidence{
				ID: evID, CaseRunID: runID, Kind: execution.EvidenceScreenshot,
				SHA256: strings.Repeat(strconv.Itoa((i+j)%10), 64), SizeBytes: 8,
			})
			refs.links[evidenceRefKey(execution.EvidenceOwnerAssertionResult, assertionID)] = append(
				refs.links[evidenceRefKey(execution.EvidenceOwnerAssertionResult, assertionID)],
				executionapp.EvidenceRef{
					Ref:      execution.EvidenceRef{ID: evID, EvidenceID: evID, OwnerKind: execution.EvidenceOwnerAssertionResult, OwnerID: assertionID},
					Evidence: execution.Evidence{ID: evID, CaseRunID: runID, Kind: execution.EvidenceScreenshot, SHA256: strings.Repeat(strconv.Itoa((i+j)%10), 64), SizeBytes: 8},
				})
		}
		evidence.rows[runID] = evRows
		issueRows = append(issueRows, delivery.Issue{
			ID: issueID, VersionID: 7, Title: "缺陷", Status: delivery.IssueStatusOpen,
			Category: "ui", Severity: "major", Source: "failure_candidate",
			CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
		})
		issueRefs[issueID] = []delivery.IssueRef{{ID: issueID, IssueID: issueID, Kind: delivery.IssueRefKindCaseRun, RefID: runID}}
	}
	issues.rows[7] = issueRows
	issues.refs = issueRefs
	// Aggregate card cap 2 (per-run 2): three cards × 2 evidence = 6 > 2. Only the
	// newest card can be fed; the two older cards are truncated.
	deps.Settings = &fakeSettings{ints: map[string]int64{
		"report.evidence_max_per_case_run": 2,
		"report.evidence_max_total":        2,
	}}

	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	newest, ok := issueByID(snapshot, 8003)
	if !ok {
		t.Fatal("newest issue 8003 missing from snapshot")
	}
	if len(newest.RepresentativeEvidence) == 0 {
		t.Fatal("newest defect card 8003 got 0 representative evidence (ascending first-come starved the newest failure?)")
	}
	for _, row := range newest.RepresentativeEvidence {
		if row.ContentPath == "" {
			t.Fatalf("newest card evidence %d has no viewable link", row.ID)
		}
	}
	// The starved oldest card discloses truncation — never a false 无代表 run 证据.
	oldest, _ := issueByID(snapshot, 8001)
	if len(oldest.RepresentativeEvidence) == 0 && !oldest.RepresentativeEvidenceTruncated {
		t.Fatal("starved oldest card 8001 has no evidence AND no truncation disclosure (would render false 无代表 run 证据)")
	}
}

// TestCardEvidenceTrimmedToPerRunCapAtRead pins the H-G4 fix-wave #39 memory
// hardening: a card whose representative reverse-lookup exceeds the per-run
// display cap retains only the cap's worth of rows (the allocation never grants
// more), while the truncation disclosure still reflects the FULL count. The
// display output is identical to holding the full set until allocation; only
// peak retention drops.
func TestCardEvidenceTrimmedToPerRunCapAtRead(t *testing.T) {
	deps := newEnrichmentFixture()
	runs := deps.CaseRuns.(*fakeCaseRuns)
	results := deps.Results.(*fakeResults)
	issues := deps.Issues.(*fakeIssues)
	refs := &fakeEvidenceRefs{links: map[string][]executionapp.EvidenceRef{}}
	deps.EvidenceRefs = refs

	const runID, assertionID, issueID = int64(5001), int64(6501), int64(8001)
	runs.runs = runs.runs[:0]
	runs.runs = append(runs.runs, execution.CaseRun{
		ID: runID, WaveID: 11, IntentID: 91, CaseVersionID: 3001, Status: execution.CaseRunFailed,
		FailureClass: execution.FailureClassApplication,
		Summary:      execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1, AssertionFailed: 1},
		CreatedAt:    fixtureTime, UpdatedAt: fixtureAfter,
	})
	results.assertions[runID] = []execution.AssertionResult{
		{ID: assertionID, CaseRunID: runID, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionFailed, Expected: "ok", Actual: "broken", Reason: "boom"},
	}
	// Five representative evidence rows against a per-run cap of 2.
	key := evidenceRefKey(execution.EvidenceOwnerAssertionResult, assertionID)
	for j := 0; j < 5; j++ {
		evID := runID*10 + int64(j)
		refs.links[key] = append(refs.links[key], executionapp.EvidenceRef{
			Ref:      execution.EvidenceRef{ID: evID, EvidenceID: evID, OwnerKind: execution.EvidenceOwnerAssertionResult, OwnerID: assertionID},
			Evidence: execution.Evidence{ID: evID, CaseRunID: runID, Kind: execution.EvidenceScreenshot, SHA256: strings.Repeat(strconv.Itoa(j%10), 64), SizeBytes: 8},
		})
	}
	issues.rows[7] = []delivery.Issue{{
		ID: issueID, VersionID: 7, Title: "缺陷", Status: delivery.IssueStatusOpen,
		Category: "ui", Severity: "major", Source: "failure_candidate",
		CreatedAt: fixtureTime, UpdatedAt: fixtureAfter,
	}}
	issues.refs = map[int64][]delivery.IssueRef{
		issueID: {{ID: issueID, IssueID: issueID, Kind: delivery.IssueRefKindCaseRun, RefID: runID}},
	}
	deps.Settings = &fakeSettings{ints: map[string]int64{
		"report.evidence_max_per_case_run": 2,
		"report.evidence_max_total":        100,
	}}

	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	card, ok := issueByID(snapshot, issueID)
	if !ok {
		t.Fatal("issue 8001 missing from snapshot")
	}
	if len(card.RepresentativeEvidence) != 2 {
		t.Fatalf("card representative evidence = %d rows, want the per-run cap 2 (full set is 5)", len(card.RepresentativeEvidence))
	}
	if !card.RepresentativeEvidenceTruncated {
		t.Fatal("card with 5 evidence rows under a cap of 2 must disclose truncation (full count, not trimmed count)")
	}
}

// TestTruncatedIssueRunNotMislabeledNoIssue (IMPORTANT #3): a run whose issue was
// truncated by the 500-issue cap is NOT mislabeled as a non-issue failure — the
// has-issue set is derived from the UNTRUNCATED issue list.
func TestTruncatedIssueRunNotMislabeledNoIssue(t *testing.T) {
	deps := newEnrichmentFixture()
	issues := deps.Issues.(*fakeIssues)
	// 501 issues ascending ID; the cap keeps the first 500. The LAST issue
	// (ID 10500, truncated) references the failed run 5001.
	rows := make([]delivery.Issue, 0, delivery.MaxReportIssues+1)
	refs := make(map[int64][]delivery.IssueRef, delivery.MaxReportIssues+1)
	for i := 0; i <= delivery.MaxReportIssues; i++ {
		id := int64(10000 + i)
		rows = append(rows, delivery.Issue{
			ID: id, VersionID: 7, Title: "I", Status: delivery.IssueStatusOpen,
			Category: "api", Severity: "minor", Source: "failure_candidate",
			CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
		})
		refRun := int64(9000 + i) // distinct dummy runs for the first 500
		if i == delivery.MaxReportIssues {
			refRun = 5001 // the truncated issue references the real failed run
		}
		refs[id] = []delivery.IssueRef{{ID: id, IssueID: id, Kind: delivery.IssueRefKindCaseRun, RefID: refRun}}
	}
	issues.rows[7] = rows
	issues.refs = refs
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if len(snapshot.Issues) != delivery.MaxReportIssues {
		t.Fatalf("issues = %d, want the cap %d", len(snapshot.Issues), delivery.MaxReportIssues)
	}
	for _, explanation := range snapshot.FailureExplanations {
		if explanation.CaseRunID == 5001 {
			t.Fatalf("run 5001 (issue truncated by the 500-cap) mislabeled as a non-issue failure: %+v", explanation)
		}
	}
}

// TestProductionCarrierAlwaysNonNil (#5): the production read model always
// populates a non-nil sha carrier, even for a run with zero evidence, so the
// digest never silently falls back to the (possibly capped) display list.
func TestProductionCarrierAlwaysNonNil(t *testing.T) {
	deps := newEnrichmentFixture()
	// run 5002 has no evidence rows in the fixture.
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	run, _ := runByID(snapshot, 5002)
	if run.EvidenceSHA256s == nil {
		t.Fatal("production carrier is nil for a zero-evidence run; the digest would fall back to the display list")
	}
	if len(run.EvidenceSHA256s) != 0 || len(run.Evidence) != 0 {
		t.Fatalf("zero-evidence run carrier/display = %d/%d, want 0/0", len(run.EvidenceSHA256s), len(run.Evidence))
	}
}

// TestSettingsOutageFallsBackToBoundedDefaults (#7): when the settings center is
// unreadable, the display caps fall back to the bounded defaults (100 per run /
// 2000 aggregate), never an unbounded projection.
func TestSettingsOutageFallsBackToBoundedDefaults(t *testing.T) {
	deps := newEnrichmentFixture()
	// 150 evidence rows on run 5001: the default per-run cap 100 must bind.
	rows := make([]execution.Evidence, 0, 150)
	for i := 0; i < 150; i++ {
		rows = append(rows, execution.Evidence{
			ID: int64(7000 + i), CaseRunID: 5001, Kind: execution.EvidenceScreenshot,
			SHA256: strings.Repeat("a", 63) + strconv.Itoa(i%10), SizeBytes: 10,
		})
	}
	deps.Evidence.(*fakeEvidence).rows[5001] = rows
	deps.Settings = &fakeSettings{err: fmt.Errorf("settings center unavailable")}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	run, _ := runByID(snapshot, 5001)
	if len(run.Evidence) != 100 {
		t.Fatalf("settings-outage per-run display = %d, want the bounded default 100", len(run.Evidence))
	}
	if run.EvidenceTotalCount() != 150 || !run.EvidenceTruncated() {
		t.Fatalf("total/truncated = %d/%v, want 150/true", run.EvidenceTotalCount(), run.EvidenceTruncated())
	}
}

// TestZeroACRequirementACHashNonEmpty (#11): a requirement with an empty AC set
// still gets a stable non-empty ACHash, distinguishable from "not populated".
func TestZeroACRequirementACHashNonEmpty(t *testing.T) {
	deps := newEnrichmentFixture()
	detail := deps.Requirements.(*fakeRequirements).details[101]
	detail.CurrentVersion.AcceptanceCriteria = nil
	deps.Requirements.(*fakeRequirements).details[101] = detail
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.Requirements[0].ACHash == "" {
		t.Fatal("zero-AC requirement ACHash is empty; indistinguishable from not populated")
	}
}
