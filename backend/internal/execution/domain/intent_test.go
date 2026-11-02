package execution

import "testing"

func TestIntentTransitions(t *testing.T) {
	valid := []struct{ from, to IntentStatus }{
		{IntentOpen, IntentClaimed},
		{IntentOpen, IntentFailed},
		{IntentOpen, IntentCancelled},
		{IntentOpen, IntentSuperseded},
		{IntentClaimed, IntentRunning},
		{IntentClaimed, IntentCancelled},
		{IntentClaimed, IntentSuperseded},
		{IntentClaimed, IntentFailed},
		{IntentRunning, IntentDone},
		{IntentRunning, IntentFailed},
		{IntentRunning, IntentCancelled},
		{IntentRunning, IntentSuperseded},
	}
	for _, tc := range valid {
		if !tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be allowed", tc.from, tc.to)
		}
	}
	invalid := []struct{ from, to IntentStatus }{
		{IntentOpen, IntentRunning},
		{IntentOpen, IntentDone},
		{IntentClaimed, IntentDone},
		{IntentDone, IntentRunning},
		{IntentDone, IntentDone},
		{IntentFailed, IntentOpen},
		{IntentCancelled, IntentRunning},
		{IntentSuperseded, IntentOpen},
		{IntentCancelled, IntentCancelled},
	}
	for _, tc := range invalid {
		if tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be rejected", tc.from, tc.to)
		}
	}
}

func TestIntentStatusHelpers(t *testing.T) {
	for _, status := range []IntentStatus{IntentOpen, IntentClaimed, IntentRunning} {
		if !status.Active() {
			t.Errorf("%s must be active", status)
		}
		if status.Terminal() {
			t.Errorf("%s must not be terminal", status)
		}
	}
	for _, status := range []IntentStatus{IntentDone, IntentFailed, IntentCancelled, IntentSuperseded} {
		if status.Active() {
			t.Errorf("%s must not be active", status)
		}
		if !status.Terminal() {
			t.Errorf("%s must be terminal", status)
		}
	}
	if IntentStatus("bogus").Valid() {
		t.Fatal("unknown intent status must be invalid")
	}
}

func TestIntentCancelIdempotent(t *testing.T) {
	intent := ExecutionIntent{ID: 1, Status: IntentOpen}
	changed, err := intent.Cancel()
	if err != nil || !changed || intent.Status != IntentCancelled {
		t.Fatalf("first cancel: changed=%v err=%v status=%s", changed, err, intent.Status)
	}
	changed, err = intent.Cancel()
	if err != nil || changed || intent.Status != IntentCancelled {
		t.Fatalf("second cancel must be a no-op: changed=%v err=%v status=%s", changed, err, intent.Status)
	}

	running := ExecutionIntent{Status: IntentRunning}
	if changed, err := running.Cancel(); err != nil || !changed {
		t.Fatalf("running intent must be cancelable: changed=%v err=%v", changed, err)
	}

	done := ExecutionIntent{Status: IntentDone}
	if _, err := done.Cancel(); err == nil {
		t.Fatal("done intent must reject cancel")
	}
}

func TestExecutionIntentValidate(t *testing.T) {
	if err := (ExecutionIntent{}).Validate(); err == nil {
		t.Fatal("empty intent must be invalid")
	}
	valid := ExecutionIntent{
		ID:                     1,
		VersionID:              2,
		CaseVersionID:          3,
		VersionSUTIDs:          []int64{10, 11},
		EnvironmentFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Purpose:                PurposeInitial,
		Status:                 IntentOpen,
		AttemptNo:              1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid intent rejected: %v", err)
	}

	badFingerprint := valid
	badFingerprint.EnvironmentFingerprint = "short"
	if err := badFingerprint.Validate(); err == nil {
		t.Fatal("fingerprint must be 64 hex characters")
	}

	badPurpose := valid
	badPurpose.Purpose = Purpose("bogus")
	if err := badPurpose.Validate(); err == nil {
		t.Fatal("unknown purpose must be rejected")
	}

	badStatus := valid
	badStatus.Status = IntentStatus("bogus")
	if err := badStatus.Validate(); err == nil {
		t.Fatal("unknown intent status must be rejected")
	}
}

func TestPurposeValid(t *testing.T) {
	for _, purpose := range []Purpose{PurposeInitial, PurposeRetest, PurposeManual} {
		if !purpose.Valid() {
			t.Errorf("%s must be valid", purpose)
		}
	}
	if Purpose("other").Valid() {
		t.Fatal("unknown purpose must be invalid")
	}
}
