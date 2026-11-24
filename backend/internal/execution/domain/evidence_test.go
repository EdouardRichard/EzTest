package execution

import "testing"

// TestEvidenceKinds pins the single evidence-kind registry: the eight legacy
// kinds plus the recording-layer kinds command/file/trace. Valid() must accept
// exactly the registry members, and the per-case-run row cap is no longer a
// domain constant (it is the execution.evidence_max_per_case_run setting,
// 0 = unlimited).
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
		EvidenceCommand,
		EvidenceFile,
		EvidenceTrace,
	}
	for _, kind := range valid {
		if !kind.Valid() {
			t.Errorf("%s must be a valid evidence kind", kind)
		}
	}
	if EvidenceKind("video").Valid() {
		t.Fatal("unknown evidence kind must be invalid")
	}
	if string(EvidenceCommand) != "command" || string(EvidenceFile) != "file" || string(EvidenceTrace) != "trace" {
		t.Fatalf("recording kind values = %q/%q/%q, want command/file/trace",
			EvidenceCommand, EvidenceFile, EvidenceTrace)
	}
	// The registry is the single source of the persisted kind set.
	registry := EvidenceKinds()
	if len(registry) != len(valid) {
		t.Fatalf("registry has %d kinds, want %d", len(registry), len(valid))
	}
	seen := make(map[EvidenceKind]bool, len(registry))
	for _, kind := range registry {
		if seen[kind] {
			t.Fatalf("duplicate registry kind %q", kind)
		}
		seen[kind] = true
		if !kind.Valid() {
			t.Fatalf("registry kind %q must be Valid()", kind)
		}
	}
	for _, kind := range valid {
		if !seen[kind] {
			t.Fatalf("kind %q missing from the registry", kind)
		}
	}
}

// TestEvidenceRecordingKindMIME pins the default MIME of the recording-layer
// kinds: binary-safe octet-stream until the producer declares a concrete type
// (the content ledger detects the actual MIME on upload).
func TestEvidenceRecordingKindMIME(t *testing.T) {
	for _, kind := range []EvidenceKind{EvidenceCommand, EvidenceFile, EvidenceTrace} {
		if got := kind.MIME(); got != "application/octet-stream" {
			t.Fatalf("%s MIME = %q, want application/octet-stream", kind, got)
		}
	}
}

func TestHumanRequestTransitions(t *testing.T) {
	for _, to := range []HumanRequestStatus{HumanAnswered, HumanCancelled, HumanExpired} {
		if !HumanPending.CanTransitionTo(to) {
			t.Errorf("pending -> %s must be allowed", to)
		}
		if !HumanAnswered.CanTransitionTo(HumanPending) {
			t.Errorf("answered -> pending (reopen) must be allowed")
		}
	}
	if HumanPending.CanTransitionTo(HumanPending) {
		t.Error("pending -> pending must be rejected")
	}
	if HumanAnswered.CanTransitionTo(HumanExpired) {
		t.Error("answered -> expired must be rejected")
	}
	for _, terminal := range []HumanRequestStatus{HumanAnswered, HumanCancelled, HumanExpired} {
		if !terminal.Terminal() {
			t.Errorf("%s must be terminal", terminal)
		}
	}
	if HumanPending.Terminal() {
		t.Fatal("pending must not be terminal")
	}
	if HumanRequestStatus("bogus").Valid() {
		t.Fatal("unknown human request status must be invalid")
	}
	for _, kind := range []HumanKind{HumanClarificationQuestion, HumanApproval, HumanBlockedRecovery,
		HumanLoginTakeover, HumanConflict} {
		if !kind.Valid() {
			t.Errorf("%s must be a valid human request kind", kind)
		}
	}
	// Q06 (H-A11): human_request.other had zero creators and is removed from the
	// enum, the DB CHECK and the published OpenAPI/frontend surfaces. It must no
	// longer be a valid kind (a re-added member fails here).
	if HumanKind("other").Valid() {
		t.Fatal("human_request kind \"other\" must be removed (Q06 / H-A11)")
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
