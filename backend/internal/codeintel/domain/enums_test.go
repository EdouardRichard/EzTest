package codeintel

import "testing"

// TestStatusEnums pins every persisted enum of spec §3.2–§3.4 and rejects
// unknown values.
func TestStatusEnums(t *testing.T) {
	assertEnum(t, "IndexStatus",
		[]IndexStatus{IndexQueued, IndexRunning, IndexDone, IndexFailed},
		[]string{"queued", "running", "done", "failed"},
		IndexStatus.Valid, IndexStatus("bogus"))
	assertEnum(t, "AnalysisStatus",
		[]AnalysisStatus{AnalysisQueued, AnalysisRunning, AnalysisDone, AnalysisFailed},
		[]string{"queued", "running", "done", "failed"},
		AnalysisStatus.Valid, AnalysisStatus("bogus"))
	assertEnum(t, "EntityKind",
		[]EntityKind{EntityFile, EntityModule, EntityRoute, EntityAPI, EntityClientCall, EntityComponent, EntityPage, EntitySymbol},
		[]string{"file", "module", "route", "api", "client_call", "component", "page", "symbol"},
		EntityKind.Valid, EntityKind("bogus"))
	assertEnum(t, "EdgeRel",
		[]EdgeRel{EdgeContains, EdgeImports, EdgeDefines, EdgeCalls, EdgeRenders, EdgeRouteToAPI},
		[]string{"contains", "imports", "defines", "calls", "renders", "route_to_api"},
		EdgeRel.Valid, EdgeRel("bogus"))
	assertEnum(t, "Confidence",
		[]Confidence{ConfidenceLow, ConfidenceMedium, ConfidenceHigh},
		[]string{"low", "medium", "high"},
		Confidence.Valid, Confidence("bogus"))
	assertEnum(t, "CandidateSource",
		[]CandidateSource{CandidateDirect, CandidateContract, CandidateRelation, CandidateAI},
		[]string{"direct", "contract", "relation", "ai"},
		CandidateSource.Valid, CandidateSource("bogus"))
	assertEnum(t, "SubjectKind",
		[]SubjectKind{SubjectRequirement, SubjectRequirementVersion, SubjectCaseVersion, SubjectSUTPage, SubjectSUTAPI},
		[]string{"requirement", "requirement_version", "case_version", "sut_page", "sut_api"},
		SubjectKind.Valid, SubjectKind("bogus"))
	assertEnum(t, "AnchorSubject",
		[]AnchorSubject{AnchorSubjectVersion, AnchorSubjectRequirement, AnchorSubjectRequirementVersion, AnchorSubjectCaseVersion, AnchorSubjectIssue},
		[]string{"version", "requirement", "requirement_version", "case_version", "issue"},
		AnchorSubject.Valid, AnchorSubject("bogus"))
	assertEnum(t, "AnchorStatus",
		[]AnchorStatus{AnchorValid, AnchorStale, AnchorBroken},
		[]string{"valid", "stale", "broken"},
		AnchorStatus.Valid, AnchorStatus("bogus"))
	assertEnum(t, "ReviewVerdict",
		[]ReviewVerdict{ReviewCorrect, ReviewIncorrect},
		[]string{"correct", "incorrect"},
		ReviewVerdict.Valid, ReviewVerdict("bogus"))
}

func assertEnum[T ~string](t *testing.T, name string, values []T, want []string, valid func(T) bool, invalid T) {
	t.Helper()
	if len(values) != len(want) {
		t.Fatalf("%s has %d values, want %d", name, len(values), len(want))
	}
	for i, value := range values {
		if string(value) != want[i] {
			t.Fatalf("%s[%d] = %q, want %q", name, i, string(value), want[i])
		}
		if !valid(value) {
			t.Fatalf("%s value %q must be valid", name, string(value))
		}
	}
	if valid(invalid) {
		t.Fatalf("%s accepted invalid value %q", name, string(invalid))
	}
}
