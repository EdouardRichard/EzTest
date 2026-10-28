package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// analysisTestDedupe is a valid sha256-shaped dedupe key.
const analysisTestDedupe = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// analysisTestPtrInt64 and analysisTestPtrString build the optional references
// of an IssueDraft.
func analysisTestPtrInt64(value int64) *int64 { return &value }

// analysisTestCandidate builds a valid application failure candidate.
func analysisTestCandidate(caseRunID, caseNodeID int64) FailureCandidate {
	return FailureCandidate{
		CaseRunID:    caseRunID,
		CaseNodeID:   caseNodeID,
		Title:        "checkout fails",
		Description:  "the order submission returns a nil pointer",
		Severity:     "major",
		Confidence:   "high",
		FailureClass: "application",
		EvidenceIDs:  []int64{701},
	}
}

// TestValidateFailureCandidatesEnforcesApplicationOwnershipAndProof pins the
// failure-analysis candidate contract of spec §5.6/§9.3: only application
// failures of the batch, with a valid confidence/severity and at least one
// evidence or code reference, survive validation; every dropped candidate yields
// a warning instead of failing the run.
func TestValidateFailureCandidatesEnforcesApplicationOwnershipAndProof(t *testing.T) {
	allowed := map[int64]int64{11: 101, 12: 102, 13: 103}

	validWithEvidence := analysisTestCandidate(11, 101)
	validWithCodeRef := analysisTestCandidate(12, 102)
	validWithCodeRef.EvidenceIDs = nil
	validWithCodeRef.CodeRefs = []CodeRef{{Path: "src/cart.go", Line: 42, Reason: "nil dereference"}}

	wrongClass := analysisTestCandidate(12, 102)
	wrongClass.FailureClass = "environment"
	wrongNode := analysisTestCandidate(12, 999)
	unknownCaseRun := analysisTestCandidate(404, 104)
	noProof := analysisTestCandidate(13, 103)
	noProof.EvidenceIDs = nil
	badConfidence := analysisTestCandidate(13, 103)
	badConfidence.Confidence = "certain"
	invalidRefsOnly := analysisTestCandidate(13, 103)
	invalidRefsOnly.EvidenceIDs = nil
	invalidRefsOnly.CodeRefs = []CodeRef{{Path: "src/cart.go", Line: 0, Reason: ""}}

	valid, warnings := ValidateFailureCandidates(allowed, nil, []FailureCandidate{
		validWithEvidence, validWithCodeRef, wrongClass, wrongNode, unknownCaseRun,
		noProof, badConfidence, invalidRefsOnly,
	})

	if len(valid) != 2 {
		t.Fatalf("valid candidates = %d, want 2: %+v", len(valid), valid)
	}
	if valid[0].CaseRunID != 11 || valid[1].CaseRunID != 12 {
		t.Fatalf("valid candidates = %d/%d, want 11/12", valid[0].CaseRunID, valid[1].CaseRunID)
	}
	if len(warnings) < 6 {
		t.Fatalf("warnings = %v, want at least one per dropped candidate", warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, needle := range []string{"failure_class", "case_run", "evidence"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("warnings %q do not mention %q", joined, needle)
		}
	}
}

// TestValidateFailureCandidatesDropsInvalidCodeRefsOnly pins the per-ref rule:
// a candidate with a usable evidence reference stays, but malformed code refs
// are removed with a warning; a candidate left without any proof is dropped.
func TestValidateFailureCandidatesDropsInvalidCodeRefsOnly(t *testing.T) {
	allowed := map[int64]int64{11: 101}
	candidate := analysisTestCandidate(11, 101)
	candidate.CodeRefs = []CodeRef{
		{Path: "src/cart.go", Line: 10, Reason: "nil dereference"},
		{Path: "", Line: 0, Reason: ""},
	}

	valid, warnings := ValidateFailureCandidates(allowed, nil, []FailureCandidate{candidate})
	if len(valid) != 1 {
		t.Fatalf("valid candidates = %d, want the candidate kept by its evidence", len(valid))
	}
	if len(valid[0].CodeRefs) != 1 || valid[0].CodeRefs[0].Path != "src/cart.go" {
		t.Fatalf("code refs = %+v, want only the well-formed ref", valid[0].CodeRefs)
	}
	if len(warnings) == 0 {
		t.Fatal("warnings = none, want a warning for the malformed code ref")
	}
}

// TestValidateFailureCandidatesCapsAtTwenty pins the §5.6 limit.
func TestValidateFailureCandidatesCapsAtTwenty(t *testing.T) {
	allowed := make(map[int64]int64, 25)
	candidates := make([]FailureCandidate, 0, 25)
	for index := int64(0); index < 25; index++ {
		caseRunID := 100 + index
		allowed[caseRunID] = 200 + index
		candidates = append(candidates, analysisTestCandidate(caseRunID, 200+index))
	}

	valid, _ := ValidateFailureCandidates(allowed, nil, candidates)
	if len(valid) != AnalysisMaxFailureCandidates {
		t.Fatalf("valid candidates = %d, want %d", len(valid), AnalysisMaxFailureCandidates)
	}
}

// TestValidateFailureCandidatesStripsUnknownEvidenceIDs pins the per-candidate
// evidence ownership rule of spec §5.6/§9.3: evidence ids that do not belong to
// a failed application case run of the batch are removed with a warning, while a
// candidate that keeps another proof (a code reference) survives.
func TestValidateFailureCandidatesStripsUnknownEvidenceIDs(t *testing.T) {
	allowed := map[int64]int64{11: 101, 12: 102}
	allowedEvidence := map[int64]struct{}{701: {}}

	withForeignEvidence := analysisTestCandidate(11, 101)
	withForeignEvidence.EvidenceIDs = []int64{701, 999}
	withForeignEvidence.CodeRefs = []CodeRef{{Path: "src/cart.go", Line: 42, Reason: "nil dereference"}}
	onlyForeignEvidence := analysisTestCandidate(12, 102)
	onlyForeignEvidence.EvidenceIDs = []int64{999}

	valid, warnings := ValidateFailureCandidates(allowed, allowedEvidence,
		[]FailureCandidate{withForeignEvidence, onlyForeignEvidence})

	if len(valid) != 1 {
		t.Fatalf("valid candidates = %d, want only the candidate with allowed evidence: %+v", len(valid), valid)
	}
	if got := valid[0].EvidenceIDs; len(got) != 1 || got[0] != 701 {
		t.Fatalf("evidence ids = %v, want [701]", got)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "evidence") || !strings.Contains(joined, "999") {
		t.Fatalf("warnings = %q, want the foreign evidence id reported", joined)
	}
}

// TestFailureDocumentsEncodeIDsAsDecimalStrings pins D29/§8.2: every id in the
// persisted analysis JSON is a decimal string, never a JSON number.
func TestFailureDocumentsEncodeIDsAsDecimalStrings(t *testing.T) {
	input := EncodeFailureInput(7, 9, []int64{101, 102})
	if !strings.Contains(string(input), `"versionId":"7"`) {
		t.Fatalf("failure input = %s, want decimal string versionId", input)
	}
	if strings.Contains(string(input), `"versionId":7`) {
		t.Fatalf("failure input = %s, want no numeric ids", input)
	}
	decoded, err := DecodeFailureInput(input)
	if err != nil {
		t.Fatalf("decode failure input: %v", err)
	}
	if decoded.VersionID != 7 || decoded.BatchID != 9 || len(decoded.CaseRunIDs) != 2 || decoded.CaseRunIDs[1] != 102 {
		t.Fatalf("decoded input = %+v", decoded)
	}

	candidate := analysisTestCandidate(101, 201)
	candidate.EvidenceIDs = []int64{701, 702}
	result := EncodeFailureResult([]FailureCandidate{candidate}, []string{"candidate 1: warning"})
	encoded := string(result)
	for _, needle := range []string{`"caseNodeId":"201"`, `"caseRunId":"101"`, `"evidenceIds":["701","702"]`, `"candidates":[`} {
		if !strings.Contains(encoded, needle) {
			t.Fatalf("failure result %s does not contain %q", encoded, needle)
		}
	}
	for _, numeric := range []string{`"caseNodeId":201`, `"caseRunId":101`} {
		if strings.Contains(encoded, numeric) {
			t.Fatalf("failure result %s contains a numeric id %q", encoded, numeric)
		}
	}
	decodedResult, err := DecodeFailureResult(result)
	if err != nil {
		t.Fatalf("decode failure result: %v", err)
	}
	if len(decodedResult.Candidates) != 1 || decodedResult.Candidates[0].CaseRunID != 101 ||
		len(decodedResult.Candidates[0].EvidenceIDs) != 2 {
		t.Fatalf("decoded candidates = %+v", decodedResult.Candidates)
	}
	if len(decodedResult.Warnings) != 1 {
		t.Fatalf("decoded warnings = %v", decodedResult.Warnings)
	}
}

// TestTriageDocumentsEncodeIssuesAndOpenIssues pins the triage input/result
// document shapes of spec §5.6/§9.4 and the decimal-string id rule.
func TestTriageDocumentsEncodeIssuesAndOpenIssues(t *testing.T) {
	input := EncodeTriageInput(
		7,
		9,
		[]FailureCandidate{analysisTestCandidate(101, 201)},
		[]OpenIssue{{IssueID: 55, Title: "old defect", Severity: "major"}},
	)
	if !strings.Contains(string(input), `"existingIssues":[`) || !strings.Contains(string(input), `"issueId":"55"`) {
		t.Fatalf("triage input = %s, want existingIssues with a decimal string issueId", input)
	}
	decodedInput, err := DecodeTriageInput(input)
	if err != nil {
		t.Fatalf("decode triage input: %v", err)
	}
	if len(decodedInput.Candidates) != 1 || len(decodedInput.ExistingIssues) != 1 || decodedInput.ExistingIssues[0].IssueID != 55 {
		t.Fatalf("decoded triage input = %+v", decodedInput)
	}

	draft := IssueDraft{
		DedupeKey:      analysisTestDedupe,
		Title:          "checkout fails",
		Description:    "submitting the order returns a nil pointer",
		ReproSteps:     "1. open cart\n2. submit",
		Expected:       "order created",
		Actual:         "nil pointer",
		Severity:       "major",
		Confidence:     "high",
		Category:       "defect",
		CaseRunID:      analysisTestPtrInt64(101),
		CaseNodeID:     analysisTestPtrInt64(201),
		EvidenceIDs:    []int64{701},
		CodeRefs:       []CodeRef{{Path: "src/cart.go", Line: 42, Reason: "nil dereference"}},
		RequirementIDs: nil,
	}
	result, err := EncodeTriageResult([]IssueDraft{draft}, []string{"warning"})
	if err != nil {
		t.Fatalf("encode triage result: %v", err)
	}
	encoded := string(result)
	for _, needle := range []string{`"issues":[`, `"dedupeKey":"` + analysisTestDedupe + `"`, `"caseRunId":"101"`, `"caseNodeId":"201"`, `"evidenceIds":["701"]`} {
		if !strings.Contains(encoded, needle) {
			t.Fatalf("triage result %s does not contain %q", encoded, needle)
		}
	}
	decodedResult, err := DecodeTriageResult(result)
	if err != nil {
		t.Fatalf("decode triage result: %v", err)
	}
	if len(decodedResult.Drafts) != 1 {
		t.Fatalf("decoded drafts = %+v", decodedResult.Drafts)
	}
	got := decodedResult.Drafts[0]
	if got.DedupeKey != draft.DedupeKey || got.Title != draft.Title || got.CaseRunID == nil || *got.CaseRunID != 101 ||
		got.CaseNodeID == nil || *got.CaseNodeID != 201 || len(got.EvidenceIDs) != 1 || len(got.CodeRefs) != 1 {
		t.Fatalf("decoded draft = %+v, want %+v", got, draft)
	}
}

// TestFallbackIssueDraftsDowngradeConfidence pins the triage-failure fallback of
// spec §5.6: one issue draft per failure candidate, confidence lowered one step,
// deterministic dedupe key, and the case/evidence/code references preserved.
func TestFallbackIssueDraftsDowngradeConfidence(t *testing.T) {
	high := analysisTestCandidate(11, 101)
	high.CodeRefs = []CodeRef{{Path: "src/cart.go", Line: 42, Reason: "nil dereference"}}
	medium := analysisTestCandidate(12, 102)
	medium.Title = ""
	medium.Description = "payment gateway timing out"
	medium.Severity = "blocker"
	medium.Confidence = "medium"
	medium.EvidenceIDs = nil
	low := analysisTestCandidate(13, 103)
	low.Confidence = "low"

	drafts := FallbackIssueDrafts(7, []FailureCandidate{high, medium, low})
	if len(drafts) != 3 {
		t.Fatalf("drafts = %d, want 3", len(drafts))
	}
	if drafts[0].Confidence != "medium" || drafts[1].Confidence != "low" || drafts[2].Confidence != "low" {
		t.Fatalf("confidences = %q/%q/%q, want medium/low/low",
			drafts[0].Confidence, drafts[1].Confidence, drafts[2].Confidence)
	}
	for index, draft := range drafts {
		if draft.Category != "defect" {
			t.Fatalf("draft %d category = %q, want defect", index, draft.Category)
		}
		if len(draft.DedupeKey) != 64 {
			t.Fatalf("draft %d dedupe key = %q, want a 64-hex key", index, draft.DedupeKey)
		}
		if draft.CaseNodeID == nil || draft.CaseRunID == nil {
			t.Fatalf("draft %d lost its case references: %+v", index, draft)
		}
	}
	if drafts[0].DedupeKey != FailureDedupeKey(7, 101, "application", high.Title) {
		t.Fatalf("dedupe key = %q, want the deterministic failure key", drafts[0].DedupeKey)
	}
	if !strings.Contains(drafts[1].Title, "payment gateway") {
		t.Fatalf("fallback title = %q, want the description excerpt", drafts[1].Title)
	}
	if drafts[1].Severity != "blocker" {
		t.Fatalf("fallback severity = %q, want the candidate severity", drafts[1].Severity)
	}
	if len(drafts[0].EvidenceIDs) != 1 || len(drafts[0].CodeRefs) != 1 {
		t.Fatalf("fallback refs = %+v/%+v, want evidence and code refs preserved",
			drafts[0].EvidenceIDs, drafts[0].CodeRefs)
	}
	if drafts[0].RequirementIDs != nil || drafts[0].MergeIntoIssueID != nil {
		t.Fatalf("fallback draft = %+v, want no requirement/merge references", drafts[0])
	}
}

// TestFailureDedupeKeyNormalizesReason pins the stable dedupe key of spec §5.4:
// case and failure class participate, reason normalization ignores case and
// whitespace, and distinct cases never share a key.
func TestFailureDedupeKeyNormalizesReason(t *testing.T) {
	first := FailureDedupeKey(7, 101, "application", "  Nil   Pointer  ")
	second := FailureDedupeKey(7, 101, "application", "nil pointer")
	if first != second {
		t.Fatalf("dedupe keys %q != %q, want normalized equality", first, second)
	}
	if other := FailureDedupeKey(7, 102, "application", "nil pointer"); other == first {
		t.Fatal("different case nodes share a dedupe key")
	}
	if len(first) != 64 {
		t.Fatalf("dedupe key = %q, want 64 hex chars", first)
	}
}

// TestValidateIssueDraftsEnforcesShapeAndReferences pins the draft validation of
// spec §5.4/§9.4: dedupe key/title/severity/category/reference rules apply, bad
// items are dropped with warnings (never failing the run), a defect needs a
// case reference, and a requirement gap needs requirement ids.
func TestValidateIssueDraftsEnforcesShapeAndReferences(t *testing.T) {
	valid := IssueDraft{
		DedupeKey:    analysisTestDedupe,
		Title:        "checkout fails",
		Description:  "nil pointer",
		Severity:     SeverityMajor,
		Confidence:   ConfidenceHigh,
		Category:     CategoryDefect,
		FailureClass: FailureClassApplication,
		CaseRunID:    analysisTestPtrInt64(11),
		CaseNodeID:   analysisTestPtrInt64(101),
		EvidenceIDs:  []int64{701},
	}
	gap := IssueDraft{
		DedupeKey:      strings.Repeat("b", 64),
		Title:          "requirement gap: retry",
		Description:    "no case covers retry",
		Severity:       SeverityMinor,
		Confidence:     ConfidenceMedium,
		Category:       CategoryRequirementGap,
		RequirementIDs: []int64{5},
	}
	emptyConfidence := valid
	emptyConfidence.DedupeKey = strings.Repeat("c", 64)
	emptyConfidence.Title = "empty confidence"
	emptyConfidence.Confidence = ""

	withBadRef := valid
	withBadRef.DedupeKey = strings.Repeat("d", 64)
	withBadRef.Title = "bad code ref"
	withBadRef.CodeRefs = []CodeRef{{Path: "", Line: 0, Reason: ""}}

	emptyDedupe := valid
	emptyDedupe.DedupeKey = ""
	shortDedupe := valid
	shortDedupe.DedupeKey = "abc"
	emptyTitle := valid
	emptyTitle.DedupeKey = strings.Repeat("e", 64)
	emptyTitle.Title = "   "
	badSeverity := valid
	badSeverity.DedupeKey = strings.Repeat("f", 64)
	badSeverity.Severity = "critical"
	badConfidence := valid
	badConfidence.DedupeKey = strings.Repeat("1", 64)
	badConfidence.Confidence = "certain"
	badCategory := valid
	badCategory.DedupeKey = strings.Repeat("2", 64)
	badCategory.Category = "weird"
	noCaseRef := IssueDraft{
		DedupeKey:    strings.Repeat("3", 64),
		Title:        "no case",
		Severity:     SeverityMinor,
		Confidence:   ConfidenceLow,
		Category:     CategoryDefect,
		FailureClass: FailureClassApplication,
	}
	gapWithoutRequirement := IssueDraft{
		DedupeKey:  strings.Repeat("4", 64),
		Title:      "gap without requirements",
		Severity:   SeverityMinor,
		Confidence: ConfidenceMedium,
		Category:   CategoryRequirementGap,
	}
	duplicate := valid
	duplicate.Title = "duplicate key"

	validDrafts, warnings := ValidateIssueDrafts([]IssueDraft{
		valid, gap, withBadRef, emptyDedupe, shortDedupe, emptyTitle,
		badSeverity, badConfidence, badCategory, noCaseRef, gapWithoutRequirement,
		emptyConfidence, duplicate,
	}, nil)

	if len(validDrafts) != 3 {
		t.Fatalf("valid drafts = %d, want valid/gap/withBadRef: %+v", len(validDrafts), validDrafts)
	}
	if len(warnings) < 10 {
		t.Fatalf("warnings = %d (%v), want one per dropped/invalid draft", len(warnings), warnings)
	}
	if len(validDrafts[2].CodeRefs) != 0 {
		t.Fatalf("withBadRef code refs = %+v, want the malformed ref removed", validDrafts[2].CodeRefs)
	}
	joined := strings.Join(warnings, "\n")
	for _, needle := range []string{"dedupe", "title", "severity", "confidence", "category", "case", "requirement"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("warnings %q do not mention %q", joined, needle)
		}
	}
}

// TestValidateIssueDraftsCapsAtTwenty pins the §9.4 draft cap.
func TestValidateIssueDraftsCapsAtTwenty(t *testing.T) {
	drafts := make([]IssueDraft, 0, 25)
	for index := 0; index < 25; index++ {
		drafts = append(drafts, IssueDraft{
			DedupeKey:    fmt.Sprintf("%064x", index+1),
			Title:        "draft",
			Severity:     SeverityMinor,
			Confidence:   ConfidenceLow,
			Category:     CategoryDefect,
			FailureClass: FailureClassApplication,
			CaseNodeID:   analysisTestPtrInt64(int64(100 + index)),
		})
	}
	valid, _ := ValidateIssueDrafts(drafts, nil)
	if len(valid) != AnalysisMaxIssueDrafts {
		t.Fatalf("valid drafts = %d, want %d", len(valid), AnalysisMaxIssueDrafts)
	}
}

// TestValidateIssueDraftsStripsUnknownEvidenceIDs pins the per-draft evidence
// whitelist of spec §5.6/§9.4: an evidence id that does not belong to a failed
// application case run of the batch is removed with a warning, and the draft
// still materializes instead of rejecting the whole submission.
func TestValidateIssueDraftsStripsUnknownEvidenceIDs(t *testing.T) {
	allowedEvidence := map[int64]struct{}{701: {}}
	draft := IssueDraft{
		DedupeKey:    analysisTestDedupe,
		Title:        "checkout fails",
		Severity:     SeverityMajor,
		Confidence:   ConfidenceHigh,
		Category:     CategoryDefect,
		FailureClass: FailureClassApplication,
		CaseNodeID:   analysisTestPtrInt64(101),
		EvidenceIDs:  []int64{701, 999},
	}

	valid, warnings := ValidateIssueDrafts([]IssueDraft{draft}, allowedEvidence)
	if len(valid) != 1 {
		t.Fatalf("valid drafts = %d, want the draft kept without the foreign evidence", len(valid))
	}
	if got := valid[0].EvidenceIDs; len(got) != 1 || got[0] != 701 {
		t.Fatalf("evidence ids = %v, want [701]", got)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "evidence") || !strings.Contains(joined, "999") {
		t.Fatalf("warnings = %q, want the foreign evidence id reported", joined)
	}
}

// TestAnalysisRunTracksActiveStatus pins the queued/running single-flight set.
func TestAnalysisRunTracksActiveStatus(t *testing.T) {
	for _, status := range []string{AnalysisQueued, AnalysisRunning} {
		if !(AnalysisRun{Status: status}).IsActive() {
			t.Fatalf("%s run is not active", status)
		}
	}
	for _, status := range []string{AnalysisDone, AnalysisFailed, AnalysisDegraded} {
		if (AnalysisRun{Status: status}).IsActive() {
			t.Fatalf("%s run is active", status)
		}
	}
}

// TestTriageResultDecodeRejectsMalformedDocuments keeps a corrupt result from
// being silently materialized.
func TestTriageResultDecodeRejectsMalformedDocuments(t *testing.T) {
	if _, err := DecodeTriageResult(json.RawMessage(`{"issues":`)); err == nil {
		t.Fatal("DecodeTriageResult accepted malformed JSON")
	}
	if _, err := DecodeFailureResult(json.RawMessage(`[]`)); err == nil {
		t.Fatal("DecodeFailureResult accepted a JSON array")
	}
}
