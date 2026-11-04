package delivery

// Task 118 (Release-T6): the report input fingerprint must include the issue
// triage projection, otherwise a report published before the triage finished is
// never detected as stale and never regenerated with the flaky conclusion.

import (
	"encoding/json"
	"testing"
)

func TestTriageOutputClassification(t *testing.T) {
	run := AnalysisRun{Output: json.RawMessage(`{"classification":"flaky","confidence":0.8}`)}
	if got := TriageOutputClassification(run); got != TriageClassificationFlaky {
		t.Fatalf("classification = %q, want flaky", got)
	}
	for _, malformed := range []json.RawMessage{
		nil, json.RawMessage(`[]`), json.RawMessage(`{"classification":3}`), json.RawMessage(`not-json`),
	} {
		if got := TriageOutputClassification(AnalysisRun{Output: malformed}); got != "" {
			t.Fatalf("malformed output %s decoded as %q, want empty", malformed, got)
		}
	}
}

func TestReportFingerprintChangesWithIssueTriage(t *testing.T) {
	before := Fingerprint(sampleReportSnapshot())
	changed := sampleReportSnapshot()
	changed.Issues[0].TriageStatus = string(AnalysisStatusDone)
	changed.Issues[0].TriageClassification = TriageClassificationFlaky
	if after := Fingerprint(changed); after == before {
		t.Fatal("fingerprint did not change when the issue triage classification changed")
	}
}

func TestReportFingerprintChangesWithRunTriage(t *testing.T) {
	before := Fingerprint(sampleReportSnapshot())
	changed := sampleReportSnapshot()
	changed.CaseRuns[0].TriageClassification = TriageClassificationFlaky
	after := Fingerprint(changed)
	if after == before {
		t.Fatal("fingerprint did not change when the run triage classification changed")
	}
	if after == "" {
		t.Fatal("fingerprint is empty for a triaged run")
	}
}
