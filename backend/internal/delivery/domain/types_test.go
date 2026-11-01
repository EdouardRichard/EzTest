package delivery

import "testing"

func TestIssueStatusValid(t *testing.T) {
	valid := map[IssueStatus]string{
		IssueStatusOpen:      "open",
		IssueStatusConfirmed: "confirmed",
		IssueStatusDismissed: "dismissed",
		IssueStatusMerged:    "merged",
	}
	for status, wire := range valid {
		if !status.Valid() {
			t.Errorf("%q.Valid() = false, want true", status)
		}
		if string(status) != wire {
			t.Errorf("issue status wire value = %q, want %q", status, wire)
		}
		if !IssueStatus(wire).Valid() {
			t.Errorf("IssueStatus(%q).Valid() = false, want true", wire)
		}
	}
	for _, wire := range []string{"", "unknown", "Open", "merged "} {
		if IssueStatus(wire).Valid() {
			t.Errorf("IssueStatus(%q).Valid() = true, want false", wire)
		}
	}
}

func TestReportKindValid(t *testing.T) {
	valid := map[ReportKind]string{
		ReportKindWave:    "wave",
		ReportKindVersion: "version",
	}
	for kind, wire := range valid {
		if !kind.Valid() {
			t.Errorf("%q.Valid() = false, want true", kind)
		}
		if string(kind) != wire {
			t.Errorf("report kind wire value = %q, want %q", kind, wire)
		}
		if !ReportKind(wire).Valid() {
			t.Errorf("ReportKind(%q).Valid() = false, want true", wire)
		}
	}
	for _, wire := range []string{"", "summary", "Wave"} {
		if ReportKind(wire).Valid() {
			t.Errorf("ReportKind(%q).Valid() = true, want false", wire)
		}
	}
}

func TestReportStatusValid(t *testing.T) {
	valid := map[ReportStatus]string{
		ReportStatusDraft:     "draft",
		ReportStatusPublished: "published",
		ReportStatusStale:     "stale",
	}
	for status, wire := range valid {
		if !status.Valid() {
			t.Errorf("%q.Valid() = false, want true", status)
		}
		if string(status) != wire {
			t.Errorf("report status wire value = %q, want %q", status, wire)
		}
		if !ReportStatus(wire).Valid() {
			t.Errorf("ReportStatus(%q).Valid() = false, want true", wire)
		}
	}
	for _, wire := range []string{"", "rendered", "Published"} {
		if ReportStatus(wire).Valid() {
			t.Errorf("ReportStatus(%q).Valid() = true, want false", wire)
		}
	}
}

func TestAnalysisStatusValid(t *testing.T) {
	valid := map[AnalysisStatus]string{
		AnalysisStatusQueued:   "queued",
		AnalysisStatusRunning:  "running",
		AnalysisStatusDone:     "done",
		AnalysisStatusFailed:   "failed",
		AnalysisStatusDegraded: "degraded",
	}
	for status, wire := range valid {
		if !status.Valid() {
			t.Errorf("%q.Valid() = false, want true", status)
		}
		if string(status) != wire {
			t.Errorf("analysis status wire value = %q, want %q", status, wire)
		}
		if !AnalysisStatus(wire).Valid() {
			t.Errorf("AnalysisStatus(%q).Valid() = false, want true", wire)
		}
	}
	for _, wire := range []string{"", "pending", "Done"} {
		if AnalysisStatus(wire).Valid() {
			t.Errorf("AnalysisStatus(%q).Valid() = true, want false", wire)
		}
	}
}
