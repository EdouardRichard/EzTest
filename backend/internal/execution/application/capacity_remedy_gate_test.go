package executionapp

// v0.2.4-H T17 (H-B9): cross-language behaviour gate for the capacity remedy.
//
// The executor-capacity remedy is shown to an operator through two surfaces: the
// `action` field of the `case_run.blocked` event (written by blockedAction here)
// and the terminal-cause override the UI renders from the persisted
// `terminal_cause` (CASE_RUN_TERMINAL_CAUSE_REMEDY in
// frontend/src/features/execution/labels.ts). For
// context_requirement_exceeds_capacity both describe the same remedy for the same
// terminal verdict, so they must not drift into two hand-maintained copies
// (constitution 五.3).
//
// There is no Go->TS codegen pipeline in this worktree, so — following the
// confirmation_precheck precedent — the single source is a SHARED, checked case
// set: testdata/capacity_remedy_gate.json. This Go test evaluates the AUTHORITY
// (blockedAction) against it; frontend/src/features/execution/
// capacity-remedy.authority.test.ts evaluates the TypeScript mirror against the
// SAME file. A wording drift on either side fails its own gate instead of
// silently diverging.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type capacityRemedyGateFixture struct {
	Remedies []struct {
		Reason string `json:"reason"`
		Remedy string `json:"remedy"`
	} `json:"remedies"`
}

func loadCapacityRemedyGateFixture(t *testing.T) capacityRemedyGateFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/capacity_remedy_gate.json")
	if err != nil {
		t.Fatalf("read shared capacity remedy gate fixture: %v", err)
	}
	var fixture capacityRemedyGateFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode shared capacity remedy gate fixture: %v", err)
	}
	if len(fixture.Remedies) == 0 {
		t.Fatal("shared capacity remedy gate fixture is empty; the gate would pass vacuously")
	}
	return fixture
}

// TestCapacityRemedyCrossLanguageGate pins blockedAction to the shared case set.
// A change to the Go authority wording without updating the fixture (and the
// frontend mirror) turns this red.
func TestCapacityRemedyCrossLanguageGate(t *testing.T) {
	for _, test := range loadCapacityRemedyGateFixture(t).Remedies {
		if strings.TrimSpace(test.Reason) == "" || strings.TrimSpace(test.Remedy) == "" {
			t.Fatalf("shared capacity remedy case has a blank reason or remedy: %+v", test)
		}
		test := test
		t.Run(test.Reason, func(t *testing.T) {
			if got := blockedAction(test.Reason); got != test.Remedy {
				t.Fatalf("blockedAction(%q) = %q, shared fixture remedy = %q (H-B9 cross-language gate)",
					test.Reason, got, test.Remedy)
			}
		})
	}
}
