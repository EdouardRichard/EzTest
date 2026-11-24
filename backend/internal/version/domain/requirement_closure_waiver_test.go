package version_test

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestRequirementClosureWaiverOverridesCompleteness pins the requirement-level
// waiver (GD-06/P23, SC-G11): an admin-marked Waived requirement overrides the
// closure COMPLETENESS checks (has_confirmed_case / cases_verified /
// failed_case_has_issue / coverage_recorded) so the deterministic reconciler
// can close it through the normal terminal Close entry.
func TestRequirementClosureWaiverOverridesCompleteness(t *testing.T) {
	in := passingClosureInput()
	// A blocked latest run verifies nothing and a missing issue ref would
	// normally keep the requirement open.
	in.LatestRuns[1101] = version.CaseRunSnapshot{
		CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunBlocked,
	}
	in.CoverageComplete = false

	if version.EvaluateRequirementClosure(in).Passed() {
		t.Fatal("unwaived closure passed with a blocked run and incomplete coverage; fixture is not discriminating")
	}
	in.Requirement.Waived = true
	checks := version.EvaluateRequirementClosure(in)
	if !checks.Passed() {
		t.Fatalf("waived closure checks = %+v, want passed (completeness overridden)", checks)
	}
	if failed := version.ClosureFailureCounts(in); len(failed) != 0 {
		t.Fatalf("waived failure counts = %v, want empty", failed)
	}
}

// TestRequirementClosureWaiverOverridesMissingCases pins that a waived
// requirement with no confirmed cases at all still closes (the waiver covers
// the has_confirmed_case completeness check).
func TestRequirementClosureWaiverOverridesMissingCases(t *testing.T) {
	in := passingClosureInput()
	in.CaseVersions = nil
	in.LatestRuns = map[int64]version.CaseRunSnapshot{}
	in.CoverageComplete = false
	in.Requirement.Waived = true

	if !version.EvaluateRequirementClosure(in).Passed() {
		t.Fatalf("waived closure checks = %+v, want passed with no cases", version.EvaluateRequirementClosure(in))
	}
}

// TestRequirementClosureWaiverStillRequiresExecutionSettled pins the negative
// space: the waiver never overrides the execution-settled checks
// (requirement_state / cases_terminal / no_open_work / no_active_intent /
// no_inflight_case_run).
func TestRequirementClosureWaiverStillRequiresExecutionSettled(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*version.ClosureInput)
		check  string
	}{
		{
			name:   "draft requirement",
			mutate: func(in *version.ClosureInput) { in.Requirement.Status = version.RequirementDraft },
			check:  version.ClosureCheckRequirementState,
		},
		{
			name: "non-terminal latest run",
			mutate: func(in *version.ClosureInput) {
				in.LatestRuns[1101] = version.CaseRunSnapshot{
					CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunRunning,
				}
			},
			check: version.ClosureCheckCasesTerminal,
		},
		{
			name:   "open work item",
			mutate: func(in *version.ClosureInput) { in.OpenWorkItems = 1 },
			check:  version.ClosureCheckNoOpenWork,
		},
		{
			name:   "active intent",
			mutate: func(in *version.ClosureInput) { in.ActiveIntents = 1 },
			check:  version.ClosureCheckNoActiveIntent,
		},
		{
			name:   "in-flight case run",
			mutate: func(in *version.ClosureInput) { in.InflightCaseRuns = 1 },
			check:  version.ClosureCheckNoInflightCaseRun,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := passingClosureInput()
			in.Requirement.Waived = true
			tc.mutate(&in)
			if version.EvaluateRequirementClosure(in).Passed() {
				t.Fatalf("waived closure passed despite %s", tc.name)
			}
			failed := version.ClosureFailureCounts(in)
			if _, ok := failed[tc.check]; !ok {
				t.Fatalf("waived failure counts = %v, want %s reported", failed, tc.check)
			}
		})
	}
}

// TestUnwaivedClosureFailureCountsUnchanged guards the failure-count derivation
// refactor: with Waived=false the counts must keep their exact historical shape.
func TestUnwaivedClosureFailureCountsUnchanged(t *testing.T) {
	in := passingClosureInput()
	in.LatestRuns[1101] = version.CaseRunSnapshot{
		CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunFailed,
		FailureClass: version.FailureClassApplication,
	}
	in.CoverageComplete = false
	failed := version.ClosureFailureCounts(in)
	// failed is a definitive (verified) outcome, so only the missing issue ref
	// and the incomplete coverage fail.
	want := map[string]int{
		version.ClosureCheckFailedCaseHasIssue: 1,
		version.ClosureCheckCoverageRecorded:   1,
	}
	if len(failed) != len(want) {
		t.Fatalf("failure counts = %v, want %v", failed, want)
	}
	for name, count := range want {
		if failed[name] != count {
			t.Fatalf("failure counts[%s] = %d, want %d (full map %v)", name, failed[name], count, failed)
		}
	}
}
