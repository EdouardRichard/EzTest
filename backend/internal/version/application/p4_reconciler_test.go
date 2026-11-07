package versionapp_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestP4ReconcilerListRunningVersions(t *testing.T) {
	env := newTestEnv(t)
	for id, status := range map[int64]string{
		700: version.StatusCompleted,
		701: version.StatusRunning,
		702: version.StatusInitial,
		703: version.StatusRunning,
	} {
		env.repo.versions[id] = version.Version{ID: id, Title: "v", Status: status}
	}

	ids, err := env.svc.ListRunningVersions(requestContext(), 100)
	if err != nil {
		t.Fatalf("ListRunningVersions: %v", err)
	}
	// Newest first; completed and initial versions never reach the P4 ensure
	// rules (their reopen scan is owned by the P4 event hooks).
	if want := []int64{703, 701}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ListRunningVersions = %v, want %v", ids, want)
	}

	limited, err := env.svc.ListRunningVersions(requestContext(), 1)
	if err != nil {
		t.Fatalf("ListRunningVersions(limit=1): %v", err)
	}
	if want := []int64{703}; !reflect.DeepEqual(limited, want) {
		t.Fatalf("ListRunningVersions(limit=1) = %v, want %v", limited, want)
	}

	// A version list without running versions yields an empty result, so the
	// reconciler performs no P4 ensure calls at all.
	env.repo.versions = map[int64]version.Version{
		704: {ID: 704, Status: version.StatusCompleted},
	}
	none, err := env.svc.ListRunningVersions(requestContext(), 10)
	if err != nil {
		t.Fatalf("ListRunningVersions(no running): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListRunningVersions(no running) = %v, want empty", none)
	}
}

// TestReconcileDocumentRHeadingScheme is the RED test for the FA-5 defect: the
// FA-5 input declares 12 requirements as R01..R12 Markdown headings (each with
// AC-Rxx-yy sub-items), so the declared/expected side must count 12, not 0,
// while FR-### and the V1/V2 partition keep their existing behavior.
func TestReconcileDocumentRHeadingScheme(t *testing.T) {
	document := rHeadingRequirementDocument()
	extracted := make([]string, 0, 12)
	for index := 1; index <= 12; index++ {
		extracted = append(extracted, fmt.Sprintf("R%02d", index))
	}

	report := versionapp.ReconcileDocument(document, versionapp.ReconciliationInput{ExtractedIDs: extracted})
	if report.Expected != 12 || report.Extracted != 12 || report.Missing != 0 {
		t.Fatalf("report = %+v, want expected=12 extracted=12 missing=0", report)
	}
	if len(report.CurrentIDs) != 12 {
		t.Fatalf("current_ids = %v, want 12 R ids", report.CurrentIDs)
	}
	for index, id := range report.CurrentIDs {
		want := fmt.Sprintf("R%02d", index+1)
		if id != want {
			t.Fatalf("current_ids[%d] = %q, want %q", index, id, want)
		}
		if strings.HasPrefix(id, "AC-") {
			t.Fatalf("AC sub-item %q was declared as a standalone requirement", id)
		}
	}
	if report.PlannedCount != 0 || len(report.PlannedIDs) != 0 {
		t.Fatalf("planned = %v (count %d), want none", report.PlannedIDs, report.PlannedCount)
	}

	// Missing is reported against the R ids in document order.
	partial := versionapp.ReconcileDocument(document, versionapp.ReconciliationInput{ExtractedIDs: extracted[1:]})
	if partial.Expected != 12 || partial.Extracted != 11 || partial.Missing != 1 ||
		len(partial.MissingIDs) != 1 || partial.MissingIDs[0] != "R01" {
		t.Fatalf("partial report = %+v, want R01 missing", partial)
	}

	// FR-### and the R heading scheme coexist (deduplicated).
	mixed := "## 功能需求\n\nFR-001 登录\nFR-002 退出\n\n### R01 账号密码登录\n\n### R02 页面访问控制\n"
	mixedReport := versionapp.ReconcileDocument(mixed, versionapp.ReconciliationInput{
		ExtractedIDs: []string{"FR-001", "R01", "R02"},
	})
	if mixedReport.Expected != 4 || mixedReport.Extracted != 3 || mixedReport.Missing != 1 ||
		len(mixedReport.MissingIDs) != 1 || mixedReport.MissingIDs[0] != "FR-002" {
		t.Fatalf("mixed FR/R report = %+v, want FR-002 missing", mixedReport)
	}

	// The V1/V2 partition still applies to R headings.
	planned := "## V1 当前需求\n\n### R01 登录\n\n## V2 规划需求\n\n### R02 后续能力\n"
	plannedReport := versionapp.ReconcileDocument(planned, versionapp.ReconciliationInput{
		ExtractedIDs: []string{"R01"},
	})
	if plannedReport.Expected != 1 || plannedReport.Extracted != 1 || plannedReport.Missing != 0 ||
		plannedReport.PlannedCount != 1 || len(plannedReport.PlannedIDs) != 1 || plannedReport.PlannedIDs[0] != "R02" {
		t.Fatalf("planned R report = %+v, want R01 current and R02 planned", plannedReport)
	}
}
