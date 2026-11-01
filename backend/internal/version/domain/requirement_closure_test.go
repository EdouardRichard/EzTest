package version_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func closureRequirement(status version.RequirementStatus, withCurrent bool) version.Requirement {
	requirement := version.Requirement{
		ID:        1001,
		VersionID: 701,
		Status:    status,
	}
	if withCurrent {
		current := int64(1002)
		requirement.CurrentVersionID = &current
	}
	return requirement
}

func passingClosureInput() version.ClosureInput {
	return version.ClosureInput{
		Requirement: closureRequirement(version.RequirementClarified, true),
		CaseVersions: []version.CaseVersionSnapshot{{
			CaseVersionID:        1101,
			CaseID:               1100,
			RequirementID:        1001,
			RequirementVersionID: 1002,
			Title:                "Checkout applies a coupon",
		}},
		LatestRuns: map[int64]version.CaseRunSnapshot{
			1101: {CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunPassed},
		},
		IssueRefs:        map[int64]bool{},
		CoverageComplete: true,
	}
}

func allClosureChecks() version.ClosureChecks {
	return version.ClosureChecks{
		RequirementState:   true,
		HasConfirmedCase:   true,
		CasesTerminal:      true,
		FailedCaseHasIssue: true,
		NoOpenWork:         true,
		NoActiveIntent:     true,
		NoInflightCaseRun:  true,
		CoverageRecorded:   true,
	}
}

func TestClosureEvaluationRequiresEveryCheck(t *testing.T) {
	base := passingClosureInput()
	if got := version.EvaluateRequirementClosure(base); got != allClosureChecks() {
		t.Fatalf("passing input checks = %+v, want all true", got)
	}

	other := base.CaseVersions[0]
	other.CaseVersionID = 1102
	other.Title = "Checkout rejects an expired coupon"

	cases := []struct {
		name   string
		mutate func(*version.ClosureInput)
		want   version.ClosureChecks
	}{
		{
			name: "draft requirement fails the state check",
			mutate: func(in *version.ClosureInput) {
				in.Requirement.Status = version.RequirementDraft
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.RequirementState = false }),
		},
		{
			name: "closed requirement keeps the state check green",
			mutate: func(in *version.ClosureInput) {
				in.Requirement.Status = version.RequirementClosed
			},
			want: allClosureChecks(),
		},
		{
			name: "obsolete requirement fails the state check",
			mutate: func(in *version.ClosureInput) {
				in.Requirement.Status = version.RequirementObsolete
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.RequirementState = false }),
		},
		{
			name: "missing current revision fails the state check",
			mutate: func(in *version.ClosureInput) {
				in.Requirement.CurrentVersionID = nil
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.RequirementState = false }),
		},
		{
			name: "no confirmed case fails the case check",
			mutate: func(in *version.ClosureInput) {
				in.CaseVersions = nil
				in.LatestRuns = map[int64]version.CaseRunSnapshot{}
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.HasConfirmedCase = false }),
		},
		{
			name: "case without a run is not terminal",
			mutate: func(in *version.ClosureInput) {
				in.CaseVersions = append(in.CaseVersions, other)
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.CasesTerminal = false }),
		},
		{
			name: "pending latest run is not terminal",
			mutate: func(in *version.ClosureInput) {
				in.LatestRuns[1101] = version.CaseRunSnapshot{
					CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunPending,
				}
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.CasesTerminal = false }),
		},
		{
			name: "running latest run is not terminal",
			mutate: func(in *version.ClosureInput) {
				in.LatestRuns[1101] = version.CaseRunSnapshot{
					CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunRunning,
				}
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.CasesTerminal = false }),
		},
		{
			name: "failed application case requires an issue ref",
			mutate: func(in *version.ClosureInput) {
				in.LatestRuns[1101] = version.CaseRunSnapshot{
					CaseRunID: 1201, CaseVersionID: 1101,
					Status: version.CaseRunFailed, FailureClass: version.FailureClassApplication,
				}
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.FailedCaseHasIssue = false }),
		},
		{
			name: "failed application case with any issue ref passes (open does not block)",
			mutate: func(in *version.ClosureInput) {
				in.LatestRuns[1101] = version.CaseRunSnapshot{
					CaseRunID: 1201, CaseVersionID: 1101,
					Status: version.CaseRunFailed, FailureClass: version.FailureClassApplication,
				}
				in.IssueRefs[1201] = true
			},
			want: allClosureChecks(),
		},
		{
			name: "failed infrastructure case needs no issue ref",
			mutate: func(in *version.ClosureInput) {
				in.LatestRuns[1101] = version.CaseRunSnapshot{
					CaseRunID: 1201, CaseVersionID: 1101,
					Status: version.CaseRunFailed, FailureClass: "environment",
				}
			},
			want: wantClosureChecks(nil),
		},
		{
			name: "open work item fails the work check",
			mutate: func(in *version.ClosureInput) {
				in.OpenWorkItems = 2
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.NoOpenWork = false }),
		},
		{
			name: "active intent fails the intent check",
			mutate: func(in *version.ClosureInput) {
				in.ActiveIntents = 1
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.NoActiveIntent = false }),
		},
		{
			name: "inflight case run fails the inflight check",
			mutate: func(in *version.ClosureInput) {
				in.InflightCaseRuns = 1
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.NoInflightCaseRun = false }),
		},
		{
			name: "missing coverage fails the coverage check",
			mutate: func(in *version.ClosureInput) {
				in.CoverageComplete = false
			},
			want: wantClosureChecks(func(c *version.ClosureChecks) { c.CoverageRecorded = false }),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := passingClosureInput()
			tc.mutate(&in)
			if got := version.EvaluateRequirementClosure(in); got != tc.want {
				t.Fatalf("checks = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func wantClosureChecks(mutate func(*version.ClosureChecks)) version.ClosureChecks {
	checks := allClosureChecks()
	if mutate != nil {
		mutate(&checks)
	}
	return checks
}

func TestClosureTerminalStatusMatrix(t *testing.T) {
	terminal := []string{
		version.CaseRunPassed,
		version.CaseRunFailed,
		version.CaseRunBlocked,
		version.CaseRunInconclusive,
		version.CaseRunSkipped,
	}
	for _, status := range terminal {
		t.Run(status, func(t *testing.T) {
			in := passingClosureInput()
			in.LatestRuns[1101] = version.CaseRunSnapshot{
				CaseRunID: 1201, CaseVersionID: 1101,
				Status: status, FailureClass: version.FailureClassApplication,
			}
			if status == version.CaseRunFailed {
				in.IssueRefs[1201] = true
			}
			if !version.EvaluateRequirementClosure(in).Passed() {
				t.Fatalf("status %q should be terminal and closable", status)
			}
		})
	}
	for _, status := range []string{version.CaseRunPending, version.CaseRunRunning, "unknown"} {
		t.Run(status, func(t *testing.T) {
			in := passingClosureInput()
			in.LatestRuns[1101] = version.CaseRunSnapshot{
				CaseRunID: 1201, CaseVersionID: 1101, Status: status,
			}
			if version.EvaluateRequirementClosure(in).CasesTerminal {
				t.Fatalf("status %q must not be terminal", status)
			}
		})
	}
}

func TestClosurePassedRequiresAllChecks(t *testing.T) {
	if !allClosureChecks().Passed() {
		t.Fatal("all-true checks must pass")
	}
	names := []string{
		"requirement_state", "has_confirmed_case", "cases_terminal",
		"failed_case_has_issue", "no_open_work", "no_active_intent",
		"no_inflight_case_run", "coverage_recorded",
	}
	for _, name := range names {
		checks := allClosureChecks()
		switch name {
		case "requirement_state":
			checks.RequirementState = false
		case "has_confirmed_case":
			checks.HasConfirmedCase = false
		case "cases_terminal":
			checks.CasesTerminal = false
		case "failed_case_has_issue":
			checks.FailedCaseHasIssue = false
		case "no_open_work":
			checks.NoOpenWork = false
		case "no_active_intent":
			checks.NoActiveIntent = false
		case "no_inflight_case_run":
			checks.NoInflightCaseRun = false
		case "coverage_recorded":
			checks.CoverageRecorded = false
		}
		if checks.Passed() {
			t.Fatalf("check %q false must fail the predicate", name)
		}
	}
}

func TestClosureFailureCounts(t *testing.T) {
	twoCases := func(in *version.ClosureInput) {
		other := in.CaseVersions[0]
		other.CaseVersionID = 1102
		in.CaseVersions = append(in.CaseVersions, other)
	}

	in := passingClosureInput()
	in.Requirement = closureRequirement(version.RequirementDraft, false)
	twoCases(&in)
	in.LatestRuns[1101] = version.CaseRunSnapshot{
		CaseRunID: 1201, CaseVersionID: 1101,
		Status: version.CaseRunFailed, FailureClass: version.FailureClassApplication,
	}
	in.LatestRuns[1102] = version.CaseRunSnapshot{
		CaseRunID: 1202, CaseVersionID: 1102, Status: version.CaseRunRunning,
	}
	in.OpenWorkItems = 2
	in.ActiveIntents = 1
	in.InflightCaseRuns = 2
	in.CoverageComplete = false

	got := version.ClosureFailureCounts(in)
	want := map[string]int{
		version.ClosureCheckRequirementState:   1,
		version.ClosureCheckCasesTerminal:      1,
		version.ClosureCheckFailedCaseHasIssue: 1,
		version.ClosureCheckNoOpenWork:         2,
		version.ClosureCheckNoActiveIntent:     1,
		version.ClosureCheckNoInflightCaseRun:  2,
		version.ClosureCheckCoverageRecorded:   1,
	}
	if len(got) != len(want) {
		t.Fatalf("failure counts = %#v, want %#v", got, want)
	}
	for name, count := range want {
		if got[name] != count {
			t.Fatalf("failure counts[%q] = %d, want %d (%#v)", name, got[name], count, got)
		}
	}

	empty := passingClosureInput()
	empty.CaseVersions = nil
	empty.LatestRuns = map[int64]version.CaseRunSnapshot{}
	counts := version.ClosureFailureCounts(empty)
	if counts[version.ClosureCheckHasConfirmedCase] != 0 {
		t.Fatalf("has_confirmed_case count = %d, want 0", counts[version.ClosureCheckHasConfirmedCase])
	}
}

func TestClosurePredicateErrorCarriesFailedChecks(t *testing.T) {
	err := &version.ErrClosePredicateFailed{Failed: map[string]int{
		version.ClosureCheckNoOpenWork:       2,
		version.ClosureCheckCoverageRecorded: 1,
	}}
	var target *version.ErrClosePredicateFailed
	if !errors.As(err, &target) {
		t.Fatalf("error %v is not ErrClosePredicateFailed", err)
	}
	if target.Failed[version.ClosureCheckNoOpenWork] != 2 {
		t.Fatalf("failed map = %#v", target.Failed)
	}
	if message := err.Error(); !strings.Contains(message, "no_open_work") || !strings.Contains(message, "close predicate") {
		t.Fatalf("error message = %q", message)
	}
}

func TestClosureCheckResultJSONReservesWaived(t *testing.T) {
	raw, err := json.Marshal(version.ClosureCheckResult{ClosureChecks: allClosureChecks()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 9 {
		t.Fatalf("check result keys = %#v", decoded)
	}
	if waived, ok := decoded["waived"].(bool); !ok || waived {
		t.Fatalf("waived = %#v, want false", decoded["waived"])
	}
	for _, name := range []string{
		"requirement_state", "has_confirmed_case", "cases_terminal",
		"failed_case_has_issue", "no_open_work", "no_active_intent",
		"no_inflight_case_run", "coverage_recorded",
	} {
		if value, ok := decoded[name].(bool); !ok || !value {
			t.Fatalf("%q = %#v, want true", name, decoded[name])
		}
	}
}

func TestClosureCanTransitionClosedToDraft(t *testing.T) {
	if !version.CanTransitionRequirement(version.RequirementClosed, version.RequirementDraft) {
		t.Fatal("closed -> draft must be allowed")
	}
	if version.CanTransitionRequirement(version.RequirementClosed, version.RequirementClarified) {
		t.Fatal("closed -> clarified must be rejected")
	}
	if version.CanTransitionRequirement(version.RequirementDraft, version.RequirementClosed) {
		t.Fatal("draft -> closed must be rejected")
	}
}
