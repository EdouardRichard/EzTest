package execution

import "testing"

func TestCaseRunTransitions(t *testing.T) {
	valid := []struct{ from, to CaseRunStatus }{
		{CaseRunPending, CaseRunRunning},
		{CaseRunPending, CaseRunPaused},
		{CaseRunPending, CaseRunSuperseded},
		{CaseRunPending, CaseRunSkipped},
		{CaseRunRunning, CaseRunPaused},
		{CaseRunRunning, CaseRunPassed},
		{CaseRunRunning, CaseRunFailed},
		{CaseRunRunning, CaseRunBlocked},
		{CaseRunRunning, CaseRunInconclusive},
		{CaseRunRunning, CaseRunInterrupted},
		{CaseRunRunning, CaseRunSuperseded},
		{CaseRunPaused, CaseRunPending},
		{CaseRunPaused, CaseRunFailed},
		{CaseRunPaused, CaseRunSuperseded},
		{CaseRunInterrupted, CaseRunPending},
		{CaseRunInterrupted, CaseRunFailed},
		{CaseRunInterrupted, CaseRunSuperseded},
	}
	for _, tc := range valid {
		if !tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be allowed", tc.from, tc.to)
		}
	}
	invalid := []struct{ from, to CaseRunStatus }{
		{CaseRunPending, CaseRunPassed},
		{CaseRunRunning, CaseRunPending},
		{CaseRunPassed, CaseRunFailed},
		{CaseRunFailed, CaseRunPending},
		{CaseRunBlocked, CaseRunRunning},
		{CaseRunSuperseded, CaseRunPending},
		{CaseRunPassed, CaseRunPassed},
		{CaseRunStatus("bogus"), CaseRunRunning},
	}
	for _, tc := range invalid {
		if tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be rejected", tc.from, tc.to)
		}
	}
}

func TestCaseRunStatusHelpers(t *testing.T) {
	for _, status := range []CaseRunStatus{CaseRunPending, CaseRunRunning, CaseRunPaused} {
		if !status.Active() || status.Terminal() || status.Finished() {
			t.Errorf("%s must be active and unfinished", status)
		}
	}
	for _, status := range []CaseRunStatus{CaseRunPassed, CaseRunFailed, CaseRunBlocked, CaseRunInconclusive} {
		if !status.Terminal() || !status.FourState() || !status.Finished() || status.Active() {
			t.Errorf("%s must be a finished four-state result", status)
		}
	}
	for _, status := range []CaseRunStatus{CaseRunSkipped, CaseRunSuperseded} {
		if !status.Terminal() || !status.Finished() || status.FourState() || status.Active() {
			t.Errorf("%s must be a finished process state", status)
		}
	}
	if CaseRunInterrupted.Terminal() || CaseRunInterrupted.FourState() {
		t.Fatal("interrupted must not be terminal while a redispatch is possible")
	}
	if !CaseRunInterrupted.Finished() {
		t.Fatal("interrupted must count as finished for wave completion")
	}
	if CaseRunStatus("bogus").Valid() {
		t.Fatal("unknown case-run status must be invalid")
	}
}

func TestFailureClassOnlyApplicationCandidate(t *testing.T) {
	valid := []FailureClass{
		FailureClassApplication,
		FailureClassEnvironment,
		FailureClassSession,
		FailureClassModel,
		FailureClassAutomation,
		FailureClassTimeout,
		FailureClassExecutorLost,
		FailureClassBlocked,
		FailureClassUnknown,
	}
	for _, class := range valid {
		if !class.Valid() {
			t.Errorf("%s must be valid", class)
		}
		if class.IsIssueCandidate() != (class == FailureClassApplication) {
			t.Errorf("only application produces an issue candidate, got %s", class)
		}
	}
	if FailureClass("bogus").Valid() {
		t.Fatal("unknown failure class must be invalid")
	}
}
