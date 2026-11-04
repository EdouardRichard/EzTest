package deliveryapp

// Task 118: the report writer's report.data_read projection must expose the
// triage-honoring classification and the flaky coverage gap, so the model
// quotes flaky instead of inventing a product defect.

import (
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

func flakyToolSnapshot() delivery.ReportSnapshot {
	snapshot := flakyTriageNarrativeSnapshot()
	snapshot.Kind = delivery.ReportKindWave
	snapshot.ReportID = 70
	return snapshot
}

func TestReportDataReadHonorsFlakyTriage(t *testing.T) {
	waveID := int64(12)
	report := delivery.Report{
		ID: 70, VersionID: 7, Kind: delivery.ReportKindWave, WaveID: &waveID,
	}
	summary := reportSnapshotSummary(report, 1, flakyToolSnapshot())

	caseRuns := summary["case_runs"].(map[string]any)
	byFailureClass := caseRuns["by_failure_class"].(map[string]int)
	if byFailureClass[delivery.FailureClassAutomation] != 1 {
		t.Fatalf("by_failure_class = %#v, want the flaky failure counted as automation", byFailureClass)
	}
	if byFailureClass["application"] != 0 {
		t.Fatalf("flaky failure still counted as application: %#v", byFailureClass)
	}

	caseResults := summary["case_results"].([]map[string]any)
	if len(caseResults) != 1 {
		t.Fatalf("case_results = %#v", caseResults)
	}
	result := caseResults[0]
	if result["failure_class"] != delivery.FailureClassAutomation {
		t.Fatalf("report-visible failure_class = %v, want automation", result["failure_class"])
	}
	if result["run_failure_class"] != "application" {
		t.Fatalf("raw run_failure_class = %v, want the preserved execution fact application", result["run_failure_class"])
	}
	if result["triage_classification"] != delivery.TriageClassificationFlaky {
		t.Fatalf("triage_classification = %v, want flaky", result["triage_classification"])
	}

	issues := summary["issues"].(map[string]any)
	byTriage := issues["by_triage"].(map[string]int)
	if byTriage[delivery.TriageClassificationFlaky] != 1 {
		t.Fatalf("issues.by_triage = %#v, want one flaky issue", byTriage)
	}

	coverage := summary["coverage"].(map[string]any)
	gapsByReason := coverage["gaps_by_reason"].(map[string]int)
	if gapsByReason[delivery.CoverageGapExecutionFlaky] != 1 {
		t.Fatalf("gaps_by_reason = %#v, want the flaky coverage gap", gapsByReason)
	}
	byRequirement := coverage["by_requirement"].([]map[string]any)
	if len(byRequirement) != 1 || byRequirement[0]["case_versions_flaky_coverage"] != 1 {
		t.Fatalf("by_requirement = %#v, want one flaky coverage", byRequirement)
	}
	if byRequirement[0]["case_versions_failed_coverage"] != 0 {
		t.Fatalf("flaky failure polluted the product failed coverage: %#v", byRequirement[0])
	}
}

// TestReportDataReadKeepsRealApplication pins that a run without a flaky triage
// is still exposed as an application failure with a product coverage gap.
func TestReportDataReadKeepsRealApplication(t *testing.T) {
	waveID := int64(12)
	report := delivery.Report{
		ID: 70, VersionID: 7, Kind: delivery.ReportKindWave, WaveID: &waveID,
	}
	snapshot := realApplicationNarrativeSnapshot()
	summary := reportSnapshotSummary(report, 1, snapshot)

	caseRuns := summary["case_runs"].(map[string]any)
	byFailureClass := caseRuns["by_failure_class"].(map[string]int)
	if byFailureClass["application"] != 1 {
		t.Fatalf("by_failure_class = %#v, want the real application failure", byFailureClass)
	}
	result := summary["case_results"].([]map[string]any)[0]
	if result["failure_class"] != "application" || result["triage_classification"] != "" {
		t.Fatalf("real application case result = %#v", result)
	}
	gapsByReason := summary["coverage"].(map[string]any)["gaps_by_reason"].(map[string]int)
	if gapsByReason["execution_failed_coverage"] != 1 || gapsByReason[delivery.CoverageGapExecutionFlaky] != 0 {
		t.Fatalf("gaps_by_reason = %#v, want the real failed-coverage gap", gapsByReason)
	}
}
