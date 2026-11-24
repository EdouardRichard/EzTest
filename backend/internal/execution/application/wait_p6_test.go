package executionapp_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestWaitCaseTimeoutZeroDisablesTimeout pins design §4.5: the case timeout is
// opt-in (execution.case_timeout_seconds=0 means off). With it off, a wait that
// would exceed a zero/short timeout is allowed; with it on, the same wait is
// rejected as a state conflict.
func TestWaitCaseTimeoutZeroDisablesTimeout(t *testing.T) {
	t.Run("on rejects an over-timeout wait", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareOneOfFourSteps(t, fixture)
		fixture.settings.ints["execution.case_timeout_seconds"] = 1

		_, err := fixture.runner.Wait(fixture.runCtx, executionScope(), tools.WaitInput{Seconds: 2, Reason: "page settles"})
		if err == nil {
			t.Fatal("a 2s wait against a 1s case timeout must be rejected")
		}
		if apiErr, ok := err.(*platform.Error); !ok || apiErr.Code != "state_conflict" {
			t.Fatalf("error = %v, want a state_conflict platform error", err)
		}
	})

	t.Run("off allows the same wait", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareOneOfFourSteps(t, fixture)
		fixture.settings.ints["execution.case_timeout_seconds"] = 0

		out, err := fixture.runner.Wait(fixture.runCtx, executionScope(), tools.WaitInput{Seconds: 2, Reason: "page settles"})
		if err != nil {
			t.Fatalf("with the case timeout off a 2s wait must succeed, got %v", err)
		}
		result, ok := out.(map[string]any)
		if !ok || result["waited_seconds"] != 2 {
			t.Fatalf("wait result = %#v, want waited_seconds=2", out)
		}
	})
}
