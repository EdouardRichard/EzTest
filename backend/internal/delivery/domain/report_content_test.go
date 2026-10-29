package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// reportTestHash is a valid lowercase sha256 hex placeholder.
const reportTestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// reportTestContent builds a deliberately unsorted content snapshot so every
// ordering rule of spec §5.5 is exercised.
func reportTestContent() ReportContent {
	return ReportContent{
		Schema: ReportSchemaV1,
		Version: ReportVersion{
			ID:                 "10",
			Name:               "结算系统",
			Label:              "v1.2",
			Status:             "executing",
			CodeBaselineCommit: "",
		},
		Batch: ReportBatch{
			ID:             "20",
			BatchNo:        2,
			Kind:           "incremental",
			Status:         "done",
			BaselineHash:   strings.Repeat("a", 64),
			GraphHash:      strings.Repeat("b", 64),
			CodeBaseCommit: "",
			HeadCommit:     "",
			StartedAt:      "",
			FinishedAt:     "2026-10-28T10:00:00Z",
		},
		Summary: ReportSummary{
			CaseCount: 2,
			Passed:    1,
			Failed:    1,
		},
		Cases: []ReportCase{
			{CaseRunID: "102", CaseNodeID: "12", Title: "结算失败", Source: "baseline", Status: "failed", FailureClass: "application", Attempt: 1, StepCount: 3, AssertionCount: 2, EvidenceCount: 4},
			{CaseRunID: "101", CaseNodeID: "11", Title: "结算成功", Source: "ai_supplement", Status: "passed", Attempt: 1, StepCount: 2, AssertionCount: 1, EvidenceCount: 1},
		},
		Requirements: []ReportRequirement{
			{RequirementID: "32", Title: "退款需求", Status: "clarified", CaseCount: 1, Passed: 1},
			{RequirementID: "31", Title: "结算需求", Status: "clarified", CaseCount: 1, Failed: 1},
		},
		Issues: []ReportIssue{
			{IssueID: "41", Title: "轻微问题", Severity: "minor", Status: "open", Confidence: "medium", Category: "defect"},
			{IssueID: "43", Title: "另一个轻微问题", Severity: "minor", Status: "open", Confidence: "low", Category: "defect"},
			{
				IssueID:    "42",
				Title:      "阻塞问题",
				Severity:   "blocker",
				Status:     "confirmed",
				Confidence: "high",
				Category:   "defect",
				CaseNodeID: "12",
				CodeRefs: []ReportCodeRef{
					{Path: "src/b.ts", Line: 20},
					{Path: "src/a.ts", Line: 10},
				},
			},
		},
		Risks:    []string{"存在阻塞问题"},
		Degraded: true,
		Appendix: ReportAppendix{
			Environment: "staging",
			System:      "payment",
			Commit:      "",
			GeneratedAt: "2026-10-28T10:00:00Z",
		},
	}
}

// TestReportContentCanonicalJSONSortsDeterministically pins the deterministic
// ordering and byte-stability rules of spec §5.5: cases by case_node_id,
// requirements by requirement id, issues by severity weight then id desc, code
// refs by path then line, and empty arrays instead of null.
func TestReportContentCanonicalJSONSortsDeterministically(t *testing.T) {
	content := reportTestContent()
	first, err := content.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	second, err := content.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json second: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical json is not byte-stable:\n%s\n%s", first, second)
	}

	var normalized ReportContent
	if err := json.Unmarshal(first, &normalized); err != nil {
		t.Fatalf("unmarshal canonical content: %v", err)
	}
	if got, want := normalized.Cases[0].CaseNodeID, "11"; got != want {
		t.Fatalf("cases[0].caseNodeId = %q, want %q (ascending case_node_id)", got, want)
	}
	if got, want := normalized.Requirements[0].RequirementID, "31"; got != want {
		t.Fatalf("requirements[0].requirementId = %q, want %q (ascending requirement id)", got, want)
	}
	wantIssueOrder := []string{"42", "43", "41"}
	for index, want := range wantIssueOrder {
		if got := normalized.Issues[index].IssueID; got != want {
			t.Fatalf("issues[%d].issueId = %q, want %q (severity then id desc)", index, got, want)
		}
	}
	codeRefs := normalized.Issues[0].CodeRefs
	if len(codeRefs) != 2 || codeRefs[0].Path != "src/a.ts" || codeRefs[1].Path != "src/b.ts" {
		t.Fatalf("code refs are not sorted by path: %+v", codeRefs)
	}

	minimal := ReportContent{
		Schema:       ReportSchemaV1,
		Version:      ReportVersion{ID: "1"},
		Batch:        ReportBatch{ID: "2", BatchNo: 1},
		Summary:      ReportSummary{},
		Cases:        nil,
		Requirements: nil,
		Issues:       nil,
		Risks:        nil,
		Appendix:     ReportAppendix{GeneratedAt: "2026-10-28T10:00:00Z"},
	}
	minimalJSON, err := minimal.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json minimal: %v", err)
	}
	for _, array := range []string{`"cases":[]`, `"requirements":[]`, `"issues":[]`, `"risks":[]`} {
		if !strings.Contains(string(minimalJSON), array) {
			t.Fatalf("minimal content must encode %s, got %s", array, minimalJSON)
		}
	}
}

// TestReportContentValidate pins the structural checks of the content snapshot.
func TestReportContentValidate(t *testing.T) {
	if err := reportTestContent().Validate(); err != nil {
		t.Fatalf("valid content rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ReportContent)
	}{
		{"schema", func(c *ReportContent) { c.Schema = "other" }},
		{"version id", func(c *ReportContent) { c.Version.ID = "" }},
		{"batch id", func(c *ReportContent) { c.Batch.ID = "not-an-id" }},
		{"batch no", func(c *ReportContent) { c.Batch.BatchNo = 0 }},
		{"case id", func(c *ReportContent) { c.Cases[0].CaseNodeID = "-1" }},
		{"case status", func(c *ReportContent) { c.Cases[0].Status = "exploded" }},
		{"case attempt", func(c *ReportContent) { c.Cases[0].Attempt = 0 }},
		{"requirement id", func(c *ReportContent) { c.Requirements[0].RequirementID = "" }},
		{"requirement counts", func(c *ReportContent) { c.Requirements[0].Passed = -1 }},
		{"issue severity", func(c *ReportContent) { c.Issues[0].Severity = "urgent" }},
		{"issue category", func(c *ReportContent) { c.Issues[0].Category = "wish" }},
		{"issue status", func(c *ReportContent) { c.Issues[0].Status = "reopened" }},
		{"issue code line", func(c *ReportContent) { c.Issues[2].CodeRefs[0].Line = -1 }},
		{"generated at", func(c *ReportContent) { c.Appendix.GeneratedAt = "yesterday" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := reportTestContent()
			test.mutate(&content)
			if err := content.Validate(); err == nil {
				t.Fatalf("content with invalid %s was accepted", test.name)
			}
		})
	}
}

// TestReportFingerprintStableAndSensitive pins the input fingerprint of spec
// §5.5: canonical JSON, stable under input order, sensitive to reruns and issue
// revisions.
func TestReportFingerprintStableAndSensitive(t *testing.T) {
	finishedAt := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	updatedRun := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	updatedIssue := time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)
	base := ReportFingerprintInput{
		Batch: ReportFingerprintBatch{
			ID:         "20",
			Status:     "done",
			FinishedAt: reportTestTime(finishedAt),
			Counts: ReportFingerprintCounts{
				CaseCount: 2, Passed: 1, Failed: 1,
			},
		},
		LatestCaseRuns: []ReportFingerprintCaseRun{
			{CaseRunID: "102", Attempt: 1, Status: "failed", FailureClass: "application", UpdatedAt: reportTestTime(updatedRun)},
			{CaseRunID: "101", Attempt: 1, Status: "passed", UpdatedAt: reportTestTime(updatedRun)},
		},
		Issues: []ReportFingerprintIssue{
			{IssueID: "41", Status: "open", Revision: 1, UpdatedAt: reportTestTime(updatedIssue)},
		},
	}
	first, err := base.Compute()
	if err != nil {
		t.Fatalf("compute fingerprint: %v", err)
	}
	if len(first) != 64 || strings.ToLower(first) != first {
		t.Fatalf("fingerprint = %q, want 64 lowercase hex chars", first)
	}
	again, err := base.Compute()
	if err != nil {
		t.Fatalf("compute fingerprint again: %v", err)
	}
	if first != again {
		t.Fatalf("fingerprint is not stable: %s != %s", first, again)
	}

	shuffled := base
	shuffled.LatestCaseRuns = []ReportFingerprintCaseRun{base.LatestCaseRuns[1], base.LatestCaseRuns[0]}
	shuffled.Issues = []ReportFingerprintIssue{base.Issues[0]}
	same, err := shuffled.Compute()
	if err != nil {
		t.Fatalf("compute shuffled fingerprint: %v", err)
	}
	if same != first {
		t.Fatalf("fingerprint depends on input order: %s != %s", same, first)
	}

	tests := []struct {
		name   string
		mutate func(*ReportFingerprintInput)
	}{
		{"rerun status", func(in *ReportFingerprintInput) { in.LatestCaseRuns[0].Status = "passed" }},
		{"rerun time", func(in *ReportFingerprintInput) {
			in.LatestCaseRuns[0].UpdatedAt = reportTestTime(updatedRun.Add(time.Minute))
		}},
		{"issue revision", func(in *ReportFingerprintInput) { in.Issues[0].Revision = 2 }},
		{"batch counts", func(in *ReportFingerprintInput) { in.Batch.Counts.Failed = 2 }},
		{"batch finish", func(in *ReportFingerprintInput) {
			in.Batch.FinishedAt = reportTestTime(finishedAt.Add(time.Minute))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.LatestCaseRuns = append([]ReportFingerprintCaseRun(nil), base.LatestCaseRuns...)
			changed.Issues = append([]ReportFingerprintIssue(nil), base.Issues...)
			test.mutate(&changed)
			digest, err := changed.Compute()
			if err != nil {
				t.Fatalf("compute changed fingerprint: %v", err)
			}
			if digest == first {
				t.Fatalf("fingerprint did not change after %s", test.name)
			}
		})
	}
}

// TestReportContentD49BatchFields pins the §7.3 snapshot extension: a newly
// generated snapshot records the batch base-fallback reason and the
// history_rewritten flag, while a legacy snapshot that predates the fields keeps
// them absent (nil) so history is never rewritten on read.
func TestReportContentD49BatchFields(t *testing.T) {
	legacy := reportTestContent()
	encoded, err := legacy.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical legacy content: %v", err)
	}
	for _, key := range []string{`"baseFallbackReason"`, `"historyRewritten"`} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("legacy snapshot unexpectedly carries %s: %s", key, encoded)
		}
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy snapshot without the D49 fields must stay valid: %v", err)
	}

	reason := "history_rewritten"
	rewritten := true
	content := reportTestContent()
	content.Batch.BaseFallbackReason = &reason
	content.Batch.HistoryRewritten = &rewritten
	if err := content.Validate(); err != nil {
		t.Fatalf("recorded D49 snapshot rejected: %v", err)
	}
	encoded, err = content.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical recorded content: %v", err)
	}
	for _, needle := range []string{
		`"baseFallbackReason":"history_rewritten"`,
		`"historyRewritten":true`,
	} {
		if !strings.Contains(string(encoded), needle) {
			t.Fatalf("recorded snapshot missing %s: %s", needle, encoded)
		}
	}

	// The flag and its reason must never disagree.
	broken := content
	broken.Batch.BaseFallbackReason = nil
	if err := broken.Validate(); err == nil {
		t.Fatal("snapshot with historyRewritten but no baseFallbackReason was accepted")
	}
}

// TestReportFingerprintIncludesEvidenceAndFailureClassCounts pins the §7.3 stale
// linkage: a per-case evidence count or a failure-class tally change must alter
// the fingerprint even when statuses and timestamps are unchanged.
func TestReportFingerprintIncludesEvidenceAndFailureClassCounts(t *testing.T) {
	base := ReportFingerprintInput{
		Batch: ReportFingerprintBatch{
			ID:     "20",
			Status: "done",
			FailureClasses: ReportFingerprintFailureClasses{
				Application: 1,
			},
		},
		LatestCaseRuns: []ReportFingerprintCaseRun{
			{
				CaseRunID:     "102",
				Attempt:       1,
				Status:        "failed",
				FailureClass:  "application",
				EvidenceCount: 1,
			},
		},
	}
	first, err := base.Compute()
	if err != nil {
		t.Fatalf("compute fingerprint: %v", err)
	}

	evidenceChanged := base
	evidenceChanged.LatestCaseRuns = append([]ReportFingerprintCaseRun(nil), base.LatestCaseRuns...)
	evidenceChanged.LatestCaseRuns[0].EvidenceCount = 2
	evidenceDigest, err := evidenceChanged.Compute()
	if err != nil {
		t.Fatalf("compute evidence-changed fingerprint: %v", err)
	}
	if evidenceDigest == first {
		t.Fatal("fingerprint ignored a per-case evidence count change")
	}

	classChanged := base
	classChanged.Batch.FailureClasses.Application = 2
	classDigest, err := classChanged.Compute()
	if err != nil {
		t.Fatalf("compute failure-class-changed fingerprint: %v", err)
	}
	if classDigest == first {
		t.Fatal("fingerprint ignored a failure-class count change")
	}
}

// reportTestTime formats t the way the report fingerprint does.
func reportTestTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
