package domain

import (
	"reflect"
	"testing"
)

// reportDiffBaseContent is the older batch snapshot of
// TestBuildReportDiffClassifies.
func reportDiffBaseContent() ReportContent {
	content := reportTestContent()
	content.Batch = ReportBatch{ID: "19", BatchNo: 1, Kind: "baseline", Status: "done"}
	content.Cases = []ReportCase{
		{CaseRunID: "201", CaseNodeID: "101", Title: "A", Source: "baseline", Status: "passed", Attempt: 1},
		{CaseRunID: "202", CaseNodeID: "102", Title: "B", Source: "baseline", Status: "failed", FailureClass: "application", Attempt: 1},
		{CaseRunID: "203", CaseNodeID: "103", Title: "C", Source: "baseline", Status: "passed", Attempt: 1},
		{CaseRunID: "204", CaseNodeID: "104", Title: "D", Source: "baseline", Status: "blocked", FailureClass: "environment", Attempt: 1},
		{CaseRunID: "205", CaseNodeID: "105", Title: "E", Source: "baseline", Status: "skipped", Attempt: 1},
		{CaseRunID: "207", CaseNodeID: "107", Title: "G", Source: "baseline", Status: "skipped", Attempt: 1},
	}
	content.Issues = []ReportIssue{
		{IssueID: "201", Title: "旧问题", Severity: "major", Status: "open", Confidence: "high", Category: "defect"},
		{IssueID: "202", Title: "需求缺口", Severity: "minor", Status: "confirmed", Confidence: "medium", Category: "requirement_gap"},
		{IssueID: "203", Title: "已驳回", Severity: "info", Status: "dismissed", Confidence: "low", Category: "other"},
	}
	return content
}

// reportDiffTargetContent is the newer batch snapshot of
// TestBuildReportDiffClassifies.
func reportDiffTargetContent() ReportContent {
	content := reportTestContent()
	content.Batch = ReportBatch{ID: "20", BatchNo: 2, Kind: "incremental", Status: "done"}
	content.Cases = []ReportCase{
		{CaseRunID: "301", CaseNodeID: "101", Title: "A", Source: "baseline", Status: "passed", Attempt: 1},
		{CaseRunID: "302", CaseNodeID: "102", Title: "B", Source: "baseline", Status: "passed", Attempt: 1},
		{CaseRunID: "303", CaseNodeID: "103", Title: "C", Source: "baseline", Status: "failed", FailureClass: "application", Attempt: 1},
		{CaseRunID: "304", CaseNodeID: "104", Title: "D", Source: "baseline", Status: "skipped", Attempt: 1},
		{CaseRunID: "306", CaseNodeID: "106", Title: "F", Source: "ai_supplement", Status: "passed", Attempt: 1},
		{CaseRunID: "307", CaseNodeID: "107", Title: "G", Source: "baseline", Status: "passed", Attempt: 1},
	}
	content.Issues = []ReportIssue{
		{IssueID: "202", Title: "需求缺口", Severity: "minor", Status: "dismissed", Confidence: "medium", Category: "requirement_gap"},
		{IssueID: "205", Title: "新问题", Severity: "blocker", Status: "open", Confidence: "high", Category: "defect", CaseNodeID: "106"},
	}
	return content
}

// TestBuildReportDiffClassifies pins the classification rules of spec §5.5:
// new/removed cases, regressed/recovered/changed statuses and
// added/fixed issues (a dismissed issue never reappears).
func TestBuildReportDiffClassifies(t *testing.T) {
	diff := BuildReportDiff(reportDiffBaseContent(), reportDiffTargetContent())

	if got, want := diff.BaseBatchNo, 1; got != want {
		t.Fatalf("baseBatchNo = %d, want %d", got, want)
	}
	if got, want := diff.TargetBatchNo, 2; got != want {
		t.Fatalf("targetBatchNo = %d, want %d", got, want)
	}
	wantSummary := ReportDiffSummary{
		AddedIssues:  1,
		FixedIssues:  2,
		Regressed:    1,
		Recovered:    1,
		NewCases:     1,
		RemovedCases: 1,
		Changed:      2,
	}
	if diff.Summary != wantSummary {
		t.Fatalf("summary = %+v, want %+v", diff.Summary, wantSummary)
	}
	if got, want := diff.RegressedCases, []ReportDiffCase{{CaseNodeID: 103, Title: "C", From: "passed", To: "failed"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("regressed = %+v, want %+v", got, want)
	}
	if got, want := diff.RecoveredCases, []ReportDiffCase{{CaseNodeID: 102, Title: "B", From: "failed", To: "passed"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered = %+v, want %+v", got, want)
	}
	wantChanged := []ReportDiffCase{
		{CaseNodeID: 104, Title: "D", From: "blocked", To: "skipped"},
		{CaseNodeID: 107, Title: "G", From: "skipped", To: "passed"},
	}
	if !reflect.DeepEqual(diff.ChangedCases, wantChanged) {
		t.Fatalf("changed = %+v, want %+v", diff.ChangedCases, wantChanged)
	}
	if got, want := diff.NewCases, []ReportDiffNewCase{{CaseNodeID: 106, Title: "F", Source: "ai_supplement"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new cases = %+v, want %+v", got, want)
	}
	if got, want := diff.RemovedCases, []ReportDiffNewCase{{CaseNodeID: 105, Title: "E", Source: "baseline"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed cases = %+v, want %+v", got, want)
	}
	wantFixed := []ReportDiffIssue{
		{IssueID: 201, Title: "旧问题", Severity: "major"},
		{IssueID: 202, Title: "需求缺口", Severity: "minor"},
	}
	if !reflect.DeepEqual(diff.FixedIssues, wantFixed) {
		t.Fatalf("fixed issues = %+v, want %+v", diff.FixedIssues, wantFixed)
	}
	wantAdded := []ReportDiffIssue{{IssueID: 205, Title: "新问题", Severity: "blocker"}}
	if !reflect.DeepEqual(diff.AddedIssues, wantAdded) {
		t.Fatalf("added issues = %+v, want %+v", diff.AddedIssues, wantAdded)
	}

	again := BuildReportDiff(reportDiffBaseContent(), reportDiffTargetContent())
	if !reflect.DeepEqual(diff, again) {
		t.Fatal("diff is not deterministic")
	}
}

// TestBuildReportDiffIdenticalContent ensures two identical snapshots produce an
// empty diff.
func TestBuildReportDiffIdenticalContent(t *testing.T) {
	diff := BuildReportDiff(reportDiffTargetContent(), reportDiffTargetContent())
	if diff.Summary != (ReportDiffSummary{}) {
		t.Fatalf("identical reports produced a non-empty diff: %+v", diff.Summary)
	}
	for name, length := range map[string]int{
		"addedIssues": len(diff.AddedIssues), "fixedIssues": len(diff.FixedIssues),
		"regressed": len(diff.RegressedCases), "recovered": len(diff.RecoveredCases),
		"changed": len(diff.ChangedCases), "newCases": len(diff.NewCases),
		"removedCases": len(diff.RemovedCases),
	} {
		if length != 0 {
			t.Fatalf("%s = %d entries, want 0", name, length)
		}
	}
}
