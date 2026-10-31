package executionapp

import (
	"context"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestAnalyzeWaveClassifiesFailures(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(300, 900, 901, "P1")
	f.addCase(301, 900, 901, "P2")
	f.addCase(302, 900, 901, "P2")
	f.addRun(500, 1, 300, execution.CaseRunFailed, "")
	f.addFailedAssertion(500, 1, 1)
	f.addRun(501, 2, 301, execution.CaseRunFailed, "")
	f.addFailedStep(501, 1)
	f.addRun(502, 3, 302, execution.CaseRunFailed, execution.FailureClassApplication)

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	assertionFailure, _ := f.runs.Get(ctx, 500)
	if assertionFailure.FailureClass != execution.FailureClassApplication || assertionFailure.Status != execution.CaseRunFailed {
		t.Fatalf("assertion failure run = %+v", assertionFailure)
	}
	if assertionFailure.Summary.AssertionFailed != 1 || assertionFailure.Summary.AssertionTotal != 1 {
		t.Fatalf("assertion summary = %+v", assertionFailure.Summary)
	}
	stepFailure, _ := f.runs.Get(ctx, 501)
	if stepFailure.FailureClass != execution.FailureClassAutomation {
		t.Fatalf("step failure class = %s, want automation", stepFailure.FailureClass)
	}
	noEvidence, _ := f.runs.Get(ctx, 502)
	if noEvidence.Status != execution.CaseRunInconclusive || noEvidence.FailureClass != "" {
		t.Fatalf("application without evidence = %+v, want inconclusive", noEvidence)
	}
	if noEvidence.Summary.VerdictReason != execution.VerdictReasonIncomplete {
		t.Fatalf("evidence-less verdict reason = %q", noEvidence.Summary.VerdictReason)
	}
}

func TestFailureCandidateOnlyApplication(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(300, 900, 901, "P0")
	f.addCase(301, 900, 901, "P2")
	f.addRun(500, 1, 300, execution.CaseRunFailed, "")
	f.addFailedAssertion(500, 1, 1)
	f.addRun(501, 2, 301, execution.CaseRunFailed, "")
	f.addFailedStep(501, 1)

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	events := f.caseRunEvents(EventFailureCandidate)
	if len(events) != 1 {
		t.Fatalf("failure_candidate events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Payload["failure_class"] != string(execution.FailureClassApplication) {
		t.Fatalf("event payload = %+v", event.Payload)
	}
	if event.Payload["severity"] != "major" {
		t.Fatalf("P0 severity = %v, want major", event.Payload["severity"])
	}
	if hint, _ := event.Payload["dedupe_hint"].(string); len(hint) != 32 {
		t.Fatalf("dedupe_hint = %q", hint)
	}
	if f.team.count() != 1 {
		t.Fatalf("team posts = %d, want 1", f.team.count())
	}
	if f.team.posts[0].Kind != "finding" {
		t.Fatalf("post kind = %s", f.team.posts[0].Kind)
	}
}

func TestCoverageGapCreatesDesignGap(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.requirements.requirements = []RequirementRef{
		{RequirementID: 900, RequirementVersionID: 901, Status: "clarified"},
		{RequirementID: 910, RequirementVersionID: 911, Status: "closed"},
	}
	f.addCase(300, 900, 901, "P1")
	f.addCase(301, 910, 911, "P1")
	f.addCase(302, 900, 901, "P2")
	f.addRun(500, 1, 300, execution.CaseRunFailed, execution.FailureClassApplication)
	f.addRun(501, 2, 301, execution.CaseRunPassed, "")

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	if f.gaps.count() != 2 {
		t.Fatalf("gaps = %v, want 2", f.gaps.keys)
	}
	joined := strings.Join(f.gaps.keys, ",")
	if !strings.Contains(joined, "execution_failed_coverage") || !strings.Contains(joined, "execution_not_run") {
		t.Fatalf("gap reasons = %v", f.gaps.keys)
	}
	if f.events.count(EventCoverageGap) != 2 {
		t.Fatalf("coverage_gap events = %d, want 2", f.events.count(EventCoverageGap))
	}

	// Repeated analysis is idempotent for gaps.
	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("second AnalyzeWave: %v", err)
	}
	if f.gaps.count() != 2 {
		t.Fatalf("gaps after second analysis = %v", f.gaps.keys)
	}

	// auto_gap_design=false keeps the event/post but skips the design.gap item.
	f.settings.values["feature.auto_gap_design"] = false
	f.gaps.keys = nil
	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave with auto gap off: %v", err)
	}
	if f.gaps.count() != 0 {
		t.Fatalf("gaps with auto_gap_design=false = %v", f.gaps.keys)
	}
}

func TestEnsureAnalysisTasksIdempotent(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(300, 900, 901, "P2")
	f.addRun(500, 1, 300, execution.CaseRunFailed, "")
	f.addFailedAssertion(500, 1, 1)

	for i := 0; i < 2; i++ {
		if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
			t.Fatalf("AnalyzeWave %d: %v", i, err)
		}
	}
	if f.queue.countType(WorkTypeAnalyzeFailure) != 1 {
		t.Fatalf("analyze.failure items = %d, want 1", f.queue.countType(WorkTypeAnalyzeFailure))
	}
	if f.queue.countType(WorkTypeAnalyzeCoverage) != 1 {
		t.Fatalf("analyze.coverage items = %d, want 1", f.queue.countType(WorkTypeAnalyzeCoverage))
	}
	for _, item := range f.queue.items {
		if item.DedupeKey != "failure:50" && item.DedupeKey != "coverage:50" {
			t.Fatalf("dedupe key = %s", item.DedupeKey)
		}
	}
}

func TestSubmitFailureCannotEscalate(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(300, 900, 901, "P1")
	f.addRun(500, 1, 300, execution.CaseRunFailed, execution.FailureClassAutomation)
	f.addFailedStep(500, 1)
	f.addRun(501, 2, 300, execution.CaseRunFailed, execution.FailureClassApplication)
	f.addFailedAssertion(501, 1, 1)
	scope := tools.Scope{VersionID: f.versionID, WaveID: f.wave.ID, CaseRunID: 500}

	// An analyst may not upgrade a non-application failure to application.
	if _, err := f.analysis.SubmitFailure(ctx, scope, tools.FailureSubmitInput{
		CaseRunID: 500, FailureClass: "application", Category: "ui", Severity: "major", Summary: "looks bad",
	}); err == nil {
		t.Fatal("escalation to application must be rejected")
	}
	if _, err := f.analysis.SubmitFailure(ctx, scope, tools.FailureSubmitInput{
		CaseRunID: 500, FailureClass: "automation", Category: "ui", Severity: "minor", Summary: "target missing",
	}); err != nil {
		t.Fatalf("same-class submission: %v", err)
	}

	// application -> environment downgrade is allowed with evidence.
	waveScope := tools.Scope{VersionID: f.versionID, WaveID: f.wave.ID}
	if _, err := f.analysis.SubmitFailure(ctx, waveScope, tools.FailureSubmitInput{
		CaseRunID: 501, FailureClass: "environment", Category: "ui", Severity: "minor", Summary: "sut down",
	}); err != nil {
		t.Fatalf("downgrade submission: %v", err)
	}
	run, _ := f.runs.Get(ctx, 501)
	if run.FailureClass != execution.FailureClassEnvironment {
		t.Fatalf("run class = %s, want environment", run.FailureClass)
	}

	// Out-of-wave case_run is not found.
	other := f.addRun(600, 9, 302, execution.CaseRunFailed, "")
	if _, err := f.analysis.SubmitFailure(ctx, scope, tools.FailureSubmitInput{
		CaseRunID: other.ID, FailureClass: "automation",
	}); err == nil {
		t.Fatal("cross-wave case_run must be rejected")
	}
}

func TestSubmitCoverageValidatesWave(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.requirements.requirements = []RequirementRef{{RequirementID: 900, RequirementVersionID: 901, Status: "clarified"}}
	f.addCase(300, 900, 901, "P1")
	f.addRun(500, 1, 300, execution.CaseRunPassed, "")
	scope := tools.Scope{VersionID: f.versionID, WaveID: f.wave.ID}

	if _, err := f.analysis.SubmitCoverage(ctx, scope, tools.CoverageSubmitInput{
		RequirementID: 900, Reason: "execution_not_run", EvidenceCaseRunIDs: []int64{500},
	}); err != nil {
		t.Fatalf("SubmitCoverage: %v", err)
	}
	if f.gaps.count() != 1 {
		t.Fatalf("gaps = %v", f.gaps.keys)
	}
	if f.events.count(EventCoverageGap) != 1 {
		t.Fatalf("coverage_gap events = %d", f.events.count(EventCoverageGap))
	}

	if _, err := f.analysis.SubmitCoverage(ctx, scope, tools.CoverageSubmitInput{
		RequirementID: 999, Reason: "execution_not_run",
	}); err == nil {
		t.Fatal("unknown requirement must be rejected")
	}

	// Evidence case_run outside the scope wave is not found.
	otherWaveRun := execution.CaseRun{ID: 601, WaveID: 51, IntentID: 9, CaseVersionID: 300, Status: execution.CaseRunPassed}
	f.runs.byID[601] = otherWaveRun
	if _, err := f.analysis.SubmitCoverage(ctx, scope, tools.CoverageSubmitInput{
		RequirementID: 900, Reason: "execution_not_run", EvidenceCaseRunIDs: []int64{601},
	}); err == nil {
		t.Fatal("cross-wave evidence must be rejected")
	}
}
