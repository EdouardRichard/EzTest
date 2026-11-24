package version_test

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestEvaluateVersionCloseWaivedOverridesCheck8AndRequirementsClosed pins the
// R-G6/R-G8 waiver predicate: the admin waiver overrides RequirementsClosed and
// the report-currency leg of check 8 (the unverified report is regenerated
// AFTER the close), while the six execution-settled checks still hold.
func TestEvaluateVersionCloseWaivedOverridesCheck8AndRequirementsClosed(t *testing.T) {
	in := passingVersionCloseInput()
	// The waiver scenario: requirements are still open and no current report
	// exists, but execution is settled.
	in.Requirements = []version.Requirement{
		{ID: 1001, VersionID: 701, Status: version.RequirementClarified},
	}
	in.Report = version.ReportStatus{}

	if version.EvaluateVersionClose(in).Passed() {
		t.Fatal("full predicate passed with open requirements and no report; the fixture does not reproduce the waiver scenario")
	}
	waived := version.EvaluateVersionCloseWaived(in)
	if !waived.Passed() {
		t.Fatalf("waived predicate = %+v, want passed (check 8 + RequirementsClosed overridden)", waived)
	}
	if !waived.RequirementsClosed || !waived.VersionReportPublished {
		t.Fatalf("waived predicate must override RequirementsClosed and VersionReportPublished, got %+v", waived)
	}
}

// TestEvaluateVersionCloseWaivedStillRequiresExecutionSettled pins the R-G6
// negative space: each of the six execution-settled checks still rejects a
// waived close, and a version without execution data cannot be waived closed
// (the mandatory post-close unverified report has nothing to render).
func TestEvaluateVersionCloseWaivedStillRequiresExecutionSettled(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*version.VersionCloseInput)
		check  string
	}{
		{
			name:   "not running",
			mutate: func(in *version.VersionCloseInput) { in.Version.Status = version.StatusCompleted },
			check:  version.VersionCloseCheckRunning,
		},
		{
			name:   "blocking work item",
			mutate: func(in *version.VersionCloseInput) { in.BlockingWorkItems = 1 },
			check:  version.VersionCloseCheckNoBlockingWorkItems,
		},
		{
			name:   "active intent",
			mutate: func(in *version.VersionCloseInput) { in.ActiveIntents = 1 },
			check:  version.VersionCloseCheckNoActiveIntents,
		},
		{
			name:   "open human request",
			mutate: func(in *version.VersionCloseInput) { in.OpenHumanRequests = 1 },
			check:  version.VersionCloseCheckNoOpenHumanRequests,
		},
		{
			name:   "active wave",
			mutate: func(in *version.VersionCloseInput) { in.ActiveWaves = 1 },
			check:  version.VersionCloseCheckNoActiveWaves,
		},
		{
			name:   "in-flight case run",
			mutate: func(in *version.VersionCloseInput) { in.InflightCaseRuns = 1 },
			check:  version.VersionCloseCheckNoInflightCaseRuns,
		},
		{
			name:   "unconfirmed confirmed case",
			mutate: func(in *version.VersionCloseInput) { in.UnconfirmedCases = 1 },
			check:  version.VersionCloseCheckNoInflightCaseRuns,
		},
		{
			name:   "no execution data",
			mutate: func(in *version.VersionCloseInput) { in.HasExecutionData = false },
			check:  version.VersionCloseCheckVersionReportPublished,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := passingVersionCloseInput()
			in.Requirements = []version.Requirement{
				{ID: 1001, VersionID: 701, Status: version.RequirementClarified},
			}
			in.Report = version.ReportStatus{}
			tc.mutate(&in)

			if version.EvaluateVersionCloseWaived(in).Passed() {
				t.Fatalf("waived predicate passed despite %s", tc.name)
			}
			failed := version.VersionCloseWaivedFailureCounts(in)
			if _, ok := failed[tc.check]; !ok {
				t.Fatalf("waived failure counts = %v, want %s reported", failed, tc.check)
			}
			if _, ok := failed[version.VersionCloseCheckRequirementsClosed]; ok {
				t.Fatalf("waived failure counts report requirements_closed (%v); the waiver overrides it", failed)
			}
		})
	}
}

// TestVersionCloseWaivedFailureCountsIgnoreReportCurrency pins that a stale or
// missing report does not appear in the waived failure map when execution data
// exists: the waiver regenerates the report after the close.
func TestVersionCloseWaivedFailureCountsIgnoreReportCurrency(t *testing.T) {
	in := passingVersionCloseInput()
	in.Report = version.ReportStatus{Status: version.ReportStatusStale}
	failed := version.VersionCloseWaivedFailureCounts(in)
	if len(failed) != 0 {
		t.Fatalf("waived failure counts = %v, want empty (report currency overridden)", failed)
	}
}
