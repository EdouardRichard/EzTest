package execution

import "testing"

func TestEvidenceKinds(t *testing.T) {
	valid := []EvidenceKind{
		EvidenceScreenshot,
		EvidenceSnapshot,
		EvidenceConsole,
		EvidenceNetwork,
		EvidenceHTTP,
		EvidenceDB,
		EvidenceDownload,
		EvidenceText,
	}
	for _, kind := range valid {
		if !kind.Valid() {
			t.Errorf("%s must be a valid evidence kind", kind)
		}
	}
	if EvidenceKind("video").Valid() {
		t.Fatal("unknown evidence kind must be invalid")
	}
	if EvidenceMaxPerCaseRun != 100 {
		t.Fatalf("evidence cap must be 100, got %d", EvidenceMaxPerCaseRun)
	}
}

func TestHumanRequestTransitions(t *testing.T) {
	for _, to := range []HumanRequestStatus{HumanResolved, HumanCancelled, HumanExpired} {
		if !HumanOpen.CanTransitionTo(to) {
			t.Errorf("open -> %s must be allowed", to)
		}
		if HumanResolved.CanTransitionTo(HumanOpen) {
			t.Errorf("resolved -> open must be rejected")
		}
	}
	for _, terminal := range []HumanRequestStatus{HumanResolved, HumanCancelled, HumanExpired} {
		if !terminal.Terminal() {
			t.Errorf("%s must be terminal", terminal)
		}
	}
	if HumanOpen.Terminal() {
		t.Fatal("open must not be terminal")
	}
	if HumanRequestStatus("bogus").Valid() {
		t.Fatal("unknown human request status must be invalid")
	}
	for _, kind := range []HumanKind{HumanLoginTakeover, HumanConflict, HumanOther} {
		if !kind.Valid() {
			t.Errorf("%s must be a valid human request kind", kind)
		}
	}
	if HumanKind("bogus").Valid() {
		t.Fatal("unknown human request kind must be invalid")
	}
}

func TestExecutorStatusHelpers(t *testing.T) {
	if !ExecutorActive.Available() || !ExecutorDraining.Available() {
		t.Fatal("active and draining executors keep their in-flight contexts")
	}
	if ExecutorOffline.Available() {
		t.Fatal("offline executor must not be available")
	}
	if !ExecutorActive.Dispatchable() {
		t.Fatal("active executor must be dispatchable")
	}
	if ExecutorDraining.Dispatchable() || ExecutorOffline.Dispatchable() {
		t.Fatal("draining/offline executors must not receive new runs")
	}
}

func TestStepAndAssertionHelpers(t *testing.T) {
	if !StepFailed.Terminal() || StepPending.Terminal() || StepRunning.Terminal() {
		t.Fatal("only finished step statuses are terminal")
	}
	if !AssertionFailed.Terminal() || !AssertionPassed.Terminal() || AssertionSkipped.Terminal() {
		t.Fatal("assertion verdicts are always terminal")
	}
	if !CaseRunStatus("").Valid() {
		t.Fatal("empty case-run status is allowed as zero value")
	}
	if (FinishCandidate{Verdict: CaseRunPassed}).Valid() != true {
		t.Fatal("passed finish candidate must be valid")
	}
	if (FinishCandidate{Verdict: CaseRunStatus("nope")}).Valid() {
		t.Fatal("invalid finish candidate must be rejected")
	}
}
