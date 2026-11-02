package version_test

import (
	"errors"
	"strings"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func publishedVersionReport() version.ReportStatus {
	fingerprint := strings.Repeat("a", 64)
	return version.ReportStatus{
		Status:             version.ReportStatusPublished,
		Fingerprint:        fingerprint,
		RevisionID:         9001,
		CurrentFingerprint: fingerprint,
	}
}

func passingVersionCloseInput() version.VersionCloseInput {
	return version.VersionCloseInput{
		Version: version.Version{ID: 701, Status: version.StatusRunning},
		Requirements: []version.Requirement{
			{ID: 1001, VersionID: 701, Status: version.RequirementClosed},
			{ID: 1002, VersionID: 701, Status: version.RequirementObsolete},
		},
		Report: publishedVersionReport(),
	}
}

func allVersionCloseConditions() version.ClosePredicate {
	return version.ClosePredicate{
		Running:                true,
		NoBlockingWorkItems:    true,
		NoActiveIntents:        true,
		NoOpenHumanRequests:    true,
		NoActiveWaves:          true,
		NoInflightCaseRuns:     true,
		RequirementsClosed:     true,
		VersionReportPublished: true,
	}
}

func wantVersionClosePredicate(mutate func(*version.ClosePredicate)) version.ClosePredicate {
	predicate := allVersionCloseConditions()
	mutate(&predicate)
	return predicate
}

func TestClosePredicateRequiresEveryCondition(t *testing.T) {
	base := passingVersionCloseInput()
	if got := version.EvaluateVersionClose(base); got != allVersionCloseConditions() {
		t.Fatalf("passing input predicate = %+v, want all true", got)
	}

	cases := []struct {
		name   string
		mutate func(*version.VersionCloseInput)
		want   version.ClosePredicate
	}{
		{
			name: "completed version is not running",
			mutate: func(in *version.VersionCloseInput) {
				in.Version.Status = version.StatusCompleted
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.Running = false }),
		},
		{
			name: "initial version is not running",
			mutate: func(in *version.VersionCloseInput) {
				in.Version.Status = version.StatusInitial
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.Running = false }),
		},
		{
			name: "one blocking work item",
			mutate: func(in *version.VersionCloseInput) {
				in.BlockingWorkItems = 1
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.NoBlockingWorkItems = false }),
		},
		{
			name: "one active intent",
			mutate: func(in *version.VersionCloseInput) {
				in.ActiveIntents = 1
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.NoActiveIntents = false }),
		},
		{
			name: "one open human request covers the waiting_human state",
			mutate: func(in *version.VersionCloseInput) {
				in.OpenHumanRequests = 1
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.NoOpenHumanRequests = false }),
		},
		{
			name: "paused wave counts as active",
			mutate: func(in *version.VersionCloseInput) {
				in.ActiveWaves = 1
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.NoActiveWaves = false }),
		},
		{
			name: "one in-flight case run",
			mutate: func(in *version.VersionCloseInput) {
				in.InflightCaseRuns = 1
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.NoInflightCaseRuns = false }),
		},
		{
			name: "clarified requirement blocks close",
			mutate: func(in *version.VersionCloseInput) {
				in.Requirements = []version.Requirement{
					{ID: 1001, VersionID: 701, Status: version.RequirementClosed},
					{ID: 1002, VersionID: 701, Status: version.RequirementClarified},
				}
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.RequirementsClosed = false }),
		},
		{
			name: "draft requirement blocks close",
			mutate: func(in *version.VersionCloseInput) {
				in.Requirements = []version.Requirement{{ID: 1001, VersionID: 701, Status: version.RequirementDraft}}
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.RequirementsClosed = false }),
		},
		{
			name: "version without requirements is closed by definition",
			mutate: func(in *version.VersionCloseInput) {
				in.Requirements = nil
			},
			want: allVersionCloseConditions(),
		},
		{
			name: "draft version report blocks close",
			mutate: func(in *version.VersionCloseInput) {
				in.Report.Status = version.ReportStatusDraft
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.VersionReportPublished = false }),
		},
		{
			name: "stale version report blocks close",
			mutate: func(in *version.VersionCloseInput) {
				in.Report.Status = version.ReportStatusStale
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.VersionReportPublished = false }),
		},
		{
			name: "published report with a missing fingerprint blocks close",
			mutate: func(in *version.VersionCloseInput) {
				in.Report.Fingerprint = ""
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.VersionReportPublished = false }),
		},
		{
			name: "published report whose stored fingerprint is not current blocks close",
			mutate: func(in *version.VersionCloseInput) {
				in.Report.Fingerprint = strings.Repeat("b", 64)
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.VersionReportPublished = false }),
		},
		{
			name: "published report without a revision blocks close",
			mutate: func(in *version.VersionCloseInput) {
				in.Report.RevisionID = 0
			},
			want: wantVersionClosePredicate(func(p *version.ClosePredicate) { p.VersionReportPublished = false }),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := passingVersionCloseInput()
			tc.mutate(&input)
			if got := version.EvaluateVersionClose(input); got != tc.want {
				t.Fatalf("predicate = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClosePredicatePassedRequiresAll(t *testing.T) {
	if !allVersionCloseConditions().Passed() {
		t.Fatal("all-true predicate did not pass")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*version.ClosePredicate)
	}{
		{"running", func(p *version.ClosePredicate) { p.Running = false }},
		{"blocking work items", func(p *version.ClosePredicate) { p.NoBlockingWorkItems = false }},
		{"active intents", func(p *version.ClosePredicate) { p.NoActiveIntents = false }},
		{"open human requests", func(p *version.ClosePredicate) { p.NoOpenHumanRequests = false }},
		{"active waves", func(p *version.ClosePredicate) { p.NoActiveWaves = false }},
		{"in-flight case runs", func(p *version.ClosePredicate) { p.NoInflightCaseRuns = false }},
		{"requirements", func(p *version.ClosePredicate) { p.RequirementsClosed = false }},
		{"version report", func(p *version.ClosePredicate) { p.VersionReportPublished = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			predicate := allVersionCloseConditions()
			tc.mutate(&predicate)
			if predicate.Passed() {
				t.Fatalf("predicate with %s false passed", tc.name)
			}
		})
	}
}

func TestVersionCloseFailureCounts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*version.VersionCloseInput)
		want   map[string]int
	}{
		{
			name: "running version with blockers reports every count",
			mutate: func(in *version.VersionCloseInput) {
				in.Version.Status = version.StatusCompleted
				in.BlockingWorkItems = 3
				in.ActiveIntents = 2
				in.OpenHumanRequests = 1
				in.ActiveWaves = 1
				in.InflightCaseRuns = 4
				in.Requirements = []version.Requirement{
					{ID: 1001, VersionID: 701, Status: version.RequirementClosed},
					{ID: 1002, VersionID: 701, Status: version.RequirementClarified},
					{ID: 1003, VersionID: 701, Status: version.RequirementDraft},
				}
				in.Report = version.ReportStatus{Status: version.ReportStatusStale}
			},
			want: map[string]int{
				version.VersionCloseCheckRunning:                1,
				version.VersionCloseCheckNoBlockingWorkItems:    3,
				version.VersionCloseCheckNoActiveIntents:        2,
				version.VersionCloseCheckNoOpenHumanRequests:    1,
				version.VersionCloseCheckNoActiveWaves:          1,
				version.VersionCloseCheckNoInflightCaseRuns:     4,
				version.VersionCloseCheckRequirementsClosed:     2,
				version.VersionCloseCheckVersionReportPublished: 1,
			},
		},
		{
			name:   "passing predicate has no failed checks",
			mutate: func(*version.VersionCloseInput) {},
			want:   map[string]int{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := passingVersionCloseInput()
			tc.mutate(&input)
			failed := version.VersionCloseFailureCounts(input)
			if len(failed) != len(tc.want) {
				t.Fatalf("failed counts = %#v, want %#v", failed, tc.want)
			}
			for name, count := range tc.want {
				if failed[name] != count {
					t.Fatalf("failed[%q] = %d, want %d (%#v)", name, failed[name], count, failed)
				}
			}
		})
	}
}

func TestVersionCloseFingerprintChangesWithPredicateInput(t *testing.T) {
	base := passingVersionCloseInput()
	fingerprint := version.VersionCloseFingerprint(base)
	if len(fingerprint) != 64 || strings.Trim(fingerprint, "0123456789abcdef") != "" {
		t.Fatalf("fingerprint = %q, want lowercase sha256 hex", fingerprint)
	}
	if again := version.VersionCloseFingerprint(base); again != fingerprint {
		t.Fatalf("fingerprint is not stable: %q != %q", again, fingerprint)
	}

	cases := []struct {
		name   string
		mutate func(*version.VersionCloseInput)
	}{
		{"version", func(in *version.VersionCloseInput) { in.Version.ID = 702 }},
		{"version status", func(in *version.VersionCloseInput) { in.Version.Status = version.StatusCompleted }},
		{"requirement status", func(in *version.VersionCloseInput) {
			in.Requirements[1].Status = version.RequirementClarified
		}},
		{"requirement set", func(in *version.VersionCloseInput) {
			in.Requirements = append(in.Requirements, version.Requirement{ID: 1003, Status: version.RequirementClosed})
		}},
		{"report revision", func(in *version.VersionCloseInput) { in.Report.RevisionID = 9002 }},
		{"report fingerprint", func(in *version.VersionCloseInput) { in.Report.Fingerprint = strings.Repeat("c", 64) }},
		{"blocking work items", func(in *version.VersionCloseInput) { in.BlockingWorkItems = 1 }},
		{"active intents", func(in *version.VersionCloseInput) { in.ActiveIntents = 1 }},
		{"open human requests", func(in *version.VersionCloseInput) { in.OpenHumanRequests = 1 }},
		{"active waves", func(in *version.VersionCloseInput) { in.ActiveWaves = 1 }},
		{"in-flight case runs", func(in *version.VersionCloseInput) { in.InflightCaseRuns = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := passingVersionCloseInput()
			tc.mutate(&input)
			if got := version.VersionCloseFingerprint(input); got == fingerprint {
				t.Fatalf("fingerprint did not change when %s changed", tc.name)
			}
		})
	}
}

func TestErrVersionClosePredicateFailedRendersStableChecks(t *testing.T) {
	err := &version.ErrVersionClosePredicateFailed{Failed: map[string]int{
		version.VersionCloseCheckNoActiveWaves:       1,
		version.VersionCloseCheckNoBlockingWorkItems: 2,
	}}
	if !strings.Contains(err.Error(), version.VersionCloseCheckNoActiveWaves) ||
		!strings.Contains(err.Error(), version.VersionCloseCheckNoBlockingWorkItems) {
		t.Fatalf("error = %q", err.Error())
	}
	var target *version.ErrVersionClosePredicateFailed
	if !errors.As(err, &target) {
		t.Fatal("error does not match ErrVersionClosePredicateFailed")
	}
}
