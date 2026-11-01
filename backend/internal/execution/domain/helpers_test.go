package execution

import "testing"

func TestCaseRunStatusValidAndEvidenceCount(t *testing.T) {
	for _, status := range []CaseRunStatus{
		CaseRunPending, CaseRunRunning, CaseRunPaused, CaseRunPassed, CaseRunFailed,
		CaseRunBlocked, CaseRunInconclusive, CaseRunSkipped, CaseRunInterrupted, CaseRunSuperseded,
	} {
		if !status.Valid() {
			t.Fatalf("%s must be valid", status)
		}
	}
	if CaseRunStatus("bogus").Valid() {
		t.Fatal("unknown case-run status must be invalid")
	}
	summary := CaseRunSummary{EvidenceCount: 3}
	if summary.EvidenceCountValue() != 3 {
		t.Fatalf("evidence count = %d", summary.EvidenceCountValue())
	}
}

func TestStepAndAssertionVerdictValidity(t *testing.T) {
	for _, status := range []StepStatus{StepPending, StepRunning, StepPassed, StepFailed, StepBlocked, StepSkipped} {
		if !status.Valid() {
			t.Fatalf("%s must be valid", status)
		}
	}
	if StepStatus("bogus").Valid() {
		t.Fatal("unknown step status must be invalid")
	}
	for _, verdict := range []AssertionVerdict{AssertionPassed, AssertionFailed, AssertionInconclusive, AssertionSkipped} {
		if !verdict.Valid() {
			t.Fatalf("%s must be valid", verdict)
		}
	}
	if AssertionVerdict("bogus").Valid() {
		t.Fatal("unknown assertion verdict must be invalid")
	}
}

func TestEvidenceMIMEAndOwnerKinds(t *testing.T) {
	cases := map[EvidenceKind]string{
		EvidenceScreenshot:   "image/png",
		EvidenceSnapshot:     "text/plain; charset=utf-8",
		EvidenceConsole:      "application/json",
		EvidenceNetwork:      "application/json",
		EvidenceHTTP:         "application/json",
		EvidenceDB:           "text/plain; charset=utf-8",
		EvidenceText:         "text/plain; charset=utf-8",
		EvidenceDownload:     "application/octet-stream",
		EvidenceVisualActual: "image/png",
		EvidenceVisualDiff:   "image/png",
	}
	for kind, want := range cases {
		if got := kind.MIME(); got != want {
			t.Fatalf("%s MIME = %q, want %q", kind, got, want)
		}
	}
	if EvidenceKind("bogus").MIME() != "application/octet-stream" {
		t.Fatal("unknown kind must fall back to octet-stream")
	}
	for _, owner := range []string{EvidenceOwnerCaseRun, EvidenceOwnerStepResult, EvidenceOwnerAssertionResult, EvidenceOwnerHumanRequest} {
		if !ValidEvidenceOwnerKind(owner) {
			t.Fatalf("%s must be a valid owner kind", owner)
		}
	}
	if ValidEvidenceOwnerKind("bogus") {
		t.Fatal("unknown owner kind must be invalid")
	}
}

func TestWaveCountsNormalizeAndPauseMode(t *testing.T) {
	var counts WaveCounts
	normalized := counts.Normalize()
	if normalized.ByVersionSUT == nil {
		t.Fatal("normalize must allocate the by_version_sut map")
	}
	if (RunWave{}).PauseModeOrDefault() != PauseModeGraceful {
		t.Fatal("empty pause mode must default to graceful")
	}
	if (RunWave{PauseMode: PauseModeImmediate}).PauseModeOrDefault() != PauseModeImmediate {
		t.Fatal("explicit pause mode must be preserved")
	}
	if !(RunWave{Status: WavePaused}).Status.Active() {
		t.Fatal("paused must occupy the active wave slot")
	}
}

func TestQuotaFromSettingsDefaults(t *testing.T) {
	quota := QuotaFromSettings(0, 0, 0)
	if quota.Global != 3 || quota.PerSUT != 2 || quota.CrossSystem != 1 {
		t.Fatalf("default quota = %+v", quota)
	}
	custom := QuotaFromSettings(5, 4, 2)
	if custom.Global != 5 || custom.PerSUT != 4 || custom.CrossSystem != 2 {
		t.Fatalf("custom quota = %+v", custom)
	}
}

func TestExecutorFreeContextsAndValid(t *testing.T) {
	for _, status := range []ExecutorStatus{ExecutorActive, ExecutorDraining, ExecutorOffline} {
		if !status.Valid() {
			t.Fatalf("%s must be valid", status)
		}
	}
	if ExecutorStatus("bogus").Valid() {
		t.Fatal("unknown executor status must be invalid")
	}
	instance := ExecutorInstance{Capacity: 8, ActiveContexts: 3}
	if instance.FreeContexts() != 5 {
		t.Fatalf("free contexts = %d", instance.FreeContexts())
	}
	over := ExecutorInstance{Capacity: 2, ActiveContexts: 5}
	if over.FreeContexts() != 0 {
		t.Fatal("free contexts must never be negative")
	}
}

func TestIntentTrimDedupeKey(t *testing.T) {
	if TrimDedupeKey("  exec:abc  ") != "exec:abc" {
		t.Fatal("dedupe key was not trimmed")
	}
}

func TestAggregateWithoutCaseAssertionsUsesResults(t *testing.T) {
	// A snapshot built before assertions were loaded still aggregates from the
	// recorded results and does not report no_assertions when results exist.
	in := VerdictInput{
		Case: CaseSnapshot{
			CaseID: 1, CaseVersionID: 2,
			Steps: []CaseStep{{Seq: 1, Action: "step"}},
		},
		Steps:      []StepResult{passedStep(1)},
		Assertions: []AssertionResult{passedAssertion(1, 1, "ui")},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil || status != CaseRunPassed || class != "" {
		t.Fatalf("aggregate = %s/%s err=%v", status, class, err)
	}
	if summary.VerdictReason != VerdictReasonPassed {
		t.Fatalf("verdict reason = %q", summary.VerdictReason)
	}
}

func TestAggregateNoAssertionsIsInconclusive(t *testing.T) {
	in := VerdictInput{
		Case:  CaseSnapshot{CaseID: 1, CaseVersionID: 2, Steps: []CaseStep{{Seq: 1, Action: "step"}}},
		Steps: []StepResult{passedStep(1)},
	}
	status, _, summary, err := Aggregate(in)
	if err != nil || status != CaseRunInconclusive {
		t.Fatalf("aggregate = %s err=%v", status, err)
	}
	if summary.VerdictReason != VerdictReasonNoAssertions {
		t.Fatalf("verdict reason = %q", summary.VerdictReason)
	}
}

func TestAggregateSkippedRequiredAssertionIsIncomplete(t *testing.T) {
	in := VerdictInput{
		Case:       caseSnapshotForVerdict(1),
		Steps:      []StepResult{passedStep(1)},
		Assertions: []AssertionResult{{CaseRunID: 1, StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: AssertionSkipped}},
	}
	status, _, summary, err := Aggregate(in)
	if err != nil || status != CaseRunInconclusive {
		t.Fatalf("aggregate = %s err=%v", status, err)
	}
	if summary.VerdictReason != VerdictReasonIncomplete {
		t.Fatalf("verdict reason = %q", summary.VerdictReason)
	}
}

func TestAggregateNonApplicationFailureWithoutResults(t *testing.T) {
	in := VerdictInput{
		Case:   caseSnapshotForVerdict(1),
		Finish: &FinishCandidate{Verdict: CaseRunFailed, FailureClass: FailureClassEnvironment},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil || status != CaseRunFailed || class != FailureClassEnvironment {
		t.Fatalf("aggregate = %s/%s err=%v", status, class, err)
	}
	if summary.VerdictReason != VerdictReasonAgentFailure {
		t.Fatalf("verdict reason = %q", summary.VerdictReason)
	}
}

func TestAggregateUnknownAgentFailure(t *testing.T) {
	status, class, _, err := Aggregate(VerdictInput{
		Case:         caseSnapshotForVerdict(1),
		AgentFailure: FailureClassUnknown,
	})
	if err != nil || status != CaseRunFailed || class != FailureClassUnknown {
		t.Fatalf("aggregate = %s/%s err=%v", status, class, err)
	}
}
