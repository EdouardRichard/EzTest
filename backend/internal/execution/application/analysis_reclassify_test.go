package executionapp

// V-0022 / SC-F9 (Review Focus 3): the deterministic analysis fallback is a
// TERMINAL -> TERMINAL reclassification through the single terminal entry, and
// its verdict comes from the single domain authority execution.Aggregate (the
// hand-written deterministicResult second truth is gone). Reclassification must
// preserve the issue-candidate handoff and the wave-count refresh, and it must
// apply the evidence gate: a failed verdict with zero persisted evidence
// degrades inconclusive (constitution art.1.2), reading the live repository
// count, never the summary projection.

import (
	"context"
	"errors"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestClassifyRunPreservesIssueCandidate is the reachability pin: a blocked run
// with recorded application evidence reclassifies to failed(application) through
// the guarded terminal->terminal entry, emits exactly one issue candidate and
// refreshes the wave counts. Routing reclassification through the plain
// settlement CAS (which refuses terminal rows) would be a silent no-op here:
// the run would stay blocked and the candidate would vanish while a grep gate
// stayed green. Removing Aggregate (keeping the prior verdict) kills the
// candidate the same way.
func TestClassifyRunPreservesIssueCandidate(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.waves.runsStore = f.runs
	const caseRunID int64 = 5301
	f.addCase(901, 900, 901, "P1")
	f.addBlockedMissingFeatureRun(caseRunID)

	for attempt := 0; attempt < 2; attempt++ {
		if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
			t.Fatalf("AnalyzeWave %d: %v", attempt, err)
		}
	}
	run, err := f.runs.Get(ctx, caseRunID)
	if err != nil {
		t.Fatalf("get reclassified run: %v", err)
	}
	if run.Status != execution.CaseRunFailed || run.FailureClass != execution.FailureClassApplication {
		t.Fatalf("run = %s/%q, want failed/application (reclassification must not be a CAS no-op)", run.Status, run.FailureClass)
	}
	if run.Summary.EvidenceCount != 14 {
		t.Fatalf("evidence count = %d, want the 14 persisted evidence rows", run.Summary.EvidenceCount)
	}
	events := f.caseRunEvents(EventFailureCandidate)
	if len(events) != 1 {
		t.Fatalf("failure_candidate events = %d, want exactly 1 after repeated analysis", len(events))
	}
	if events[0].Payload["failure_class"] != string(execution.FailureClassApplication) {
		t.Fatalf("candidate payload = %+v", events[0].Payload)
	}
	if f.team.count() != 1 {
		t.Fatalf("team finding posts = %d, want 1", f.team.count())
	}
	wave, err := f.waves.Get(ctx, f.wave.ID)
	if err != nil {
		t.Fatalf("get wave: %v", err)
	}
	if wave.Counts.Failed != 1 || wave.Counts.Blocked != 0 {
		t.Fatalf("wave counts = failed %d / blocked %d, want failed 1 / blocked 0 after reclassification",
			wave.Counts.Failed, wave.Counts.Blocked)
	}
}

// TestClassifyRunNoEvidenceDegradesFailed pins the evidence gate on the
// reclassification path: a failed(application) run whose evidence repository
// holds zero rows degrades inconclusive(no_evidence) and emits no issue
// candidate, even though a failed assertion was recorded.
func TestClassifyRunNoEvidenceDegradesFailed(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	const caseRunID int64 = 5302
	f.addCase(901, 900, 901, "P1")
	f.addRun(caseRunID, 1, 901, execution.CaseRunFailed, execution.FailureClassApplication)
	f.addFailedAssertion(caseRunID, 1, 1)
	// Deliberately NO evidence rows: the recorded assertion is not immutable
	// evidence; a failure nobody can inspect is not a trustworthy defect.

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	run, err := f.runs.Get(ctx, caseRunID)
	if err != nil {
		t.Fatalf("get reclassified run: %v", err)
	}
	if run.Status != execution.CaseRunInconclusive || run.FailureClass != "" {
		t.Fatalf("run = %s/%q, want inconclusive with no failure class (no-evidence failed degrades)", run.Status, run.FailureClass)
	}
	if run.Summary.VerdictReason != execution.VerdictReasonNoEvidence {
		t.Fatalf("verdict reason = %q, want %q", run.Summary.VerdictReason, execution.VerdictReasonNoEvidence)
	}
	if events := f.caseRunEvents(EventFailureCandidate); len(events) != 0 {
		t.Fatalf("failure_candidate events = %d, want 0 for a no-evidence failure", len(events))
	}
	if f.team.count() != 0 {
		t.Fatalf("team finding posts = %d, want 0 for a no-evidence failure", f.team.count())
	}
}

// TestClassifyRunEvidenceCountFromRepository pins that reclassification supplies
// Aggregate the LIVE persisted count, not the stale summary projection
// (constitution art.5.8: a projection must never gate a verdict).
func TestClassifyRunEvidenceCountFromRepository(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	const caseRunID int64 = 5303
	f.addCase(901, 900, 901, "P1")
	run := f.addRun(caseRunID, 1, 901, execution.CaseRunFailed, execution.FailureClassApplication)
	run.Summary = execution.CaseRunSummary{EvidenceCount: 99} // stale projection
	f.runs.byID[caseRunID] = run
	f.addFailedAssertion(caseRunID, 1, 1)
	f.addEvidenceRows(caseRunID, 2) // live truth

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	stored, err := f.runs.Get(ctx, caseRunID)
	if err != nil {
		t.Fatalf("get reclassified run: %v", err)
	}
	if stored.Status != execution.CaseRunFailed || stored.FailureClass != execution.FailureClassApplication {
		t.Fatalf("run = %s/%q, want failed/application (2 evidence rows exist)", stored.Status, stored.FailureClass)
	}
	if stored.Summary.EvidenceCount != 2 {
		t.Fatalf("evidence count = %d, want the live repository count 2 (not the stale projection 99)", stored.Summary.EvidenceCount)
	}
}

// TestClassifyRunCountReadFailureNotSwallowed pins that an evidence-count read
// failure aborts the analysis loudly instead of silently classifying with a
// wrong count (constitution art.7.6).
func TestClassifyRunCountReadFailureNotSwallowed(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	const caseRunID int64 = 5304
	f.addCase(901, 900, 901, "P1")
	f.addRun(caseRunID, 1, 901, execution.CaseRunFailed, execution.FailureClassApplication)
	f.addFailedAssertion(caseRunID, 1, 1)
	f.evidence.countErr = errors.New("evidence store unavailable")

	err := f.analysis.AnalyzeWave(ctx, f.wave.ID)
	if err == nil {
		t.Fatal("AnalyzeWave must surface the evidence-count read failure, not swallow it")
	}
	if !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("error = %v, want it to name the evidence count failure", err)
	}
	stored, _ := f.runs.Get(ctx, caseRunID)
	if stored.Status != execution.CaseRunFailed {
		t.Fatalf("run status = %s, want the stored verdict untouched on a count read failure", stored.Status)
	}
}
