package delivery

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validCandidateInput() CandidateInput {
	versionSUTID := int64(55)
	waveID := int64(66)
	return CandidateInput{
		VersionID:                1,
		CaseRunID:                2,
		CaseVersionID:            3,
		RequirementID:            77,
		CaseTitle:                "Checkout coupon",
		CaseRunSummary:           "total mismatch",
		StepSeq:                  4,
		AssertionSeq:             2,
		FailedStepResultID:       201,
		FailedAssertionResultIDs: []int64{301, 302},
		FailureClass:             "application",
		Category:                 "ui",
		Severity:                 "major",
		FailedExpected:           "HTTP 200",
		FailedActual:             "HTTP 500",
		FailedReason:             "status mismatch",
		VersionSUTID:             &versionSUTID,
		WaveID:                   &waveID,
		EvidenceIDs:              []int64{101, 102},
	}
}

func TestDedupeKeyMatchesP3Algorithm(t *testing.T) {
	// Literal golden: sha256("11|22|3|4|application") hex-truncated to 32 chars
	// (spec §5.1 P3 algorithm). Pinned so a payload or truncation change fails.
	const want = "3fe9019f27a6380fa0b193b17845060c"
	if got := DeriveDedupeKey(11, 22, 3, 4, "application", ""); got != want {
		t.Fatalf("DeriveDedupeKey = %q, want the pinned P3 key %q", got, want)
	}
	if len(want) != 32 {
		t.Fatalf("pinned dedupe key length = %d, want 32", len(want))
	}
}

func TestDedupeKeyStableAcrossWaves(t *testing.T) {
	firstWave := DeriveDedupeKey(1, 3, 4, 2, "application", "")
	secondWave := DeriveDedupeKey(1, 3, 4, 2, "application", "")
	if firstWave != secondWave {
		t.Fatalf("same case_version across waves must dedupe, got %q and %q", firstWave, secondWave)
	}

	if DeriveDedupeKey(1, 4, 4, 2, "application", "") == firstWave {
		t.Fatal("a new case_version must produce a new dedupe key")
	}
	if DeriveDedupeKey(1, 3, 5, 2, "application", "") == firstWave {
		t.Fatal("a new step_seq must produce a new dedupe key")
	}
	if DeriveDedupeKey(1, 3, 4, 3, "application", "") == firstWave {
		t.Fatal("a new assertion_seq must produce a new dedupe key")
	}
	if DeriveDedupeKey(1, 3, 4, 2, "application", "") == DeriveDedupeKey(2, 3, 4, 2, "application", "") {
		t.Fatal("a new version_id must produce a new dedupe key")
	}
}

func TestDedupeKeyPrefersValidHint(t *testing.T) {
	validHint := strings.Repeat("ab", 16)
	if got := DeriveDedupeKey(1, 3, 4, 2, "application", validHint); got != validHint {
		t.Fatalf("valid hint = %q, want it returned as-is", got)
	}
	if got := DeriveDedupeKey(9, 9, 9, 9, "application", validHint); got != validHint {
		t.Fatalf("valid hint must win regardless of the other inputs, got %q", got)
	}

	invalidHints := []string{
		"",
		strings.ToUpper(validHint),
		validHint[:31],
		validHint + "a",
		strings.Repeat("g", 32),
		" " + validHint,
		validHint[:31] + "-",
	}
	// Literal golden: sha256("1|3|4|2|application") hex-truncated to 32 chars.
	const fallback = "d636156f26735cc17ea3e0806ea2f0b4"
	for _, hint := range invalidHints {
		if got := DeriveDedupeKey(1, 3, 4, 2, "application", hint); got != fallback {
			t.Errorf("invalid hint %q produced %q, want the recomputed P3 key %q", hint, got, fallback)
		}
	}
}

func TestCandidateInputValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CandidateInput)
	}{
		{"missing version", func(in *CandidateInput) { in.VersionID = 0 }},
		{"negative version", func(in *CandidateInput) { in.VersionID = -1 }},
		{"missing case run", func(in *CandidateInput) { in.CaseRunID = 0 }},
		{"missing case version", func(in *CandidateInput) { in.CaseVersionID = 0 }},
		{"negative case version", func(in *CandidateInput) { in.CaseVersionID = -3 }},
		{"negative step seq", func(in *CandidateInput) { in.StepSeq = -1 }},
		{"negative assertion seq", func(in *CandidateInput) { in.AssertionSeq = -2 }},
		{"non positive wave", func(in *CandidateInput) { in.WaveID = int64Pointer(0) }},
		{"non positive version SUT", func(in *CandidateInput) { in.VersionSUTID = int64Pointer(-55) }},
		{"negative requirement", func(in *CandidateInput) { in.RequirementID = -1 }},
		{"negative failed step result", func(in *CandidateInput) { in.FailedStepResultID = -1 }},
		{"non positive failed assertion", func(in *CandidateInput) { in.FailedAssertionResultIDs = []int64{301, 0} }},
		{"non positive evidence", func(in *CandidateInput) { in.EvidenceIDs = []int64{101, 0} }},
		{"missing category", func(in *CandidateInput) { in.Category = "" }},
		{"unknown category", func(in *CandidateInput) { in.Category = "other" }},
		{"missing severity", func(in *CandidateInput) { in.Severity = "" }},
		{"unknown severity", func(in *CandidateInput) { in.Severity = "blocker" }},
		{"missing failure class", func(in *CandidateInput) { in.FailureClass = "" }},
		{"non application failure class", func(in *CandidateInput) { in.FailureClass = "interrupted" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validCandidateInput()
			tc.mutate(&in)
			issue, refs, err := NewIssueCandidate(in)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
			if refs != nil {
				t.Fatalf("refs = %v, want nil on invalid input", refs)
			}
			if !reflect.DeepEqual(issue, Issue{}) {
				t.Fatalf("issue = %+v, want the zero value on invalid input", issue)
			}
		})
	}

	if _, _, err := NewIssueCandidate(validCandidateInput()); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
}

func TestCandidateDerivation(t *testing.T) {
	in := validCandidateInput()
	in.DedupeHint = strings.Repeat("cd", 16)
	issue, refs, err := NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}

	if issue.ID != 0 {
		t.Fatalf("deterministic candidate must leave the ID to the repository, got %d", issue.ID)
	}
	if issue.VersionID != in.VersionID || issue.Category != in.Category || issue.Severity != in.Severity {
		t.Fatalf("candidate header = %+v", issue)
	}
	if issue.Status != IssueStatusOpen {
		t.Fatalf("status = %q, want open", issue.Status)
	}
	if issue.Source != "failure_candidate" {
		t.Fatalf("source = %q, want failure_candidate", issue.Source)
	}
	if issue.ExpectedRevision != 1 {
		t.Fatalf("expected_revision = %d, want 1", issue.ExpectedRevision)
	}
	if issue.ReopenCount != 0 {
		t.Fatalf("reopen_count = %d, want 0 for a new issue", issue.ReopenCount)
	}
	if issue.CreatedByRunID != nil {
		t.Fatalf("created_by_run_id = %v, want nil for deterministic creation", issue.CreatedByRunID)
	}
	if issue.Title != "Checkout coupon · step 4: ui failure" {
		t.Fatalf("title = %q", issue.Title)
	}
	if issue.Summary != in.CaseRunSummary {
		t.Fatalf("summary = %q, want %q", issue.Summary, in.CaseRunSummary)
	}
	if issue.DedupeKey != in.DedupeHint {
		t.Fatalf("dedupe_key = %q, want the valid hint %q", issue.DedupeKey, in.DedupeHint)
	}
	if len(issue.Fingerprint) != 64 {
		t.Fatalf("fingerprint length = %d, want 64", len(issue.Fingerprint))
	}

	second, secondRefs, err := NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate (second call): %v", err)
	}
	if !reflect.DeepEqual(issue, second) {
		t.Fatal("candidate derivation is not deterministic")
	}
	if !reflect.DeepEqual(refs, secondRefs) {
		t.Fatal("candidate refs derivation is not deterministic")
	}
}

func TestCandidateTitleFormat(t *testing.T) {
	withStep := validCandidateInput()
	withStep.CaseTitle = "Login"
	withStep.Category = "api"
	withStep.StepSeq = 7
	issue, _, err := NewIssueCandidate(withStep)
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	if want := "Login · step 7: api failure"; issue.Title != want {
		t.Fatalf("title = %q, want %q", issue.Title, want)
	}

	withoutStep := withStep
	withoutStep.StepSeq = 0
	issue, _, err = NewIssueCandidate(withoutStep)
	if err != nil {
		t.Fatalf("NewIssueCandidate (no step): %v", err)
	}
	if want := "Login · api failure"; issue.Title != want {
		t.Fatalf("title = %q, want %q", issue.Title, want)
	}
}

func TestCandidateTruncation(t *testing.T) {
	in := validCandidateInput()
	in.CaseTitle = strings.Repeat("测", IssueTitleMax+100)
	in.StepSeq = 0
	issue, _, err := NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	if runes := []rune(issue.Title); len(runes) != IssueTitleMax {
		t.Fatalf("title rune length = %d, want %d", len(runes), IssueTitleMax)
	}

	in = validCandidateInput()
	in.CaseRunSummary = strings.Repeat("s", IssueSummaryMax+200)
	issue, _, err = NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate (long summary): %v", err)
	}
	if runes := []rune(issue.Summary); len(runes) != IssueSummaryMax {
		t.Fatalf("summary rune length = %d, want %d", len(runes), IssueSummaryMax)
	}
}

func TestCandidateBounds(t *testing.T) {
	if IssueTitleMax != 500 {
		t.Fatalf("IssueTitleMax = %d, want 500", IssueTitleMax)
	}
	if IssueSummaryMax != 4000 {
		t.Fatalf("IssueSummaryMax = %d, want 4000", IssueSummaryMax)
	}

	// A title of exactly the bound is preserved untouched; one rune more is cut
	// at the bound. The suffix length is derived from the frozen title format.
	suffix := " · ui failure"
	atTitleBound := validCandidateInput()
	atTitleBound.StepSeq = 0
	atTitleBound.CaseTitle = strings.Repeat("a", IssueTitleMax-len([]rune(suffix)))
	issue, _, err := NewIssueCandidate(atTitleBound)
	if err != nil {
		t.Fatalf("NewIssueCandidate (title at bound): %v", err)
	}
	if runes := []rune(issue.Title); len(runes) != IssueTitleMax {
		t.Fatalf("title at bound rune length = %d, want %d", len(runes), IssueTitleMax)
	}
	if !strings.HasSuffix(issue.Title, suffix) {
		t.Fatalf("title at bound was truncated: %q", issue.Title)
	}

	overTitleBound := atTitleBound
	overTitleBound.CaseTitle += "a"
	issue, _, err = NewIssueCandidate(overTitleBound)
	if err != nil {
		t.Fatalf("NewIssueCandidate (title over bound): %v", err)
	}
	if runes := []rune(issue.Title); len(runes) != IssueTitleMax {
		t.Fatalf("title over bound rune length = %d, want %d", len(runes), IssueTitleMax)
	}
	if strings.HasSuffix(issue.Title, suffix) {
		t.Fatalf("title over bound must be truncated, got %q", issue.Title)
	}

	// A summary of exactly the bound is preserved; one rune more loses only the
	// final rune.
	atSummaryBound := validCandidateInput()
	atSummaryBound.CaseRunSummary = strings.Repeat("s", IssueSummaryMax-1) + "x"
	issue, _, err = NewIssueCandidate(atSummaryBound)
	if err != nil {
		t.Fatalf("NewIssueCandidate (summary at bound): %v", err)
	}
	if issue.Summary != atSummaryBound.CaseRunSummary {
		t.Fatalf("summary at bound = %q, want it preserved", issue.Summary)
	}

	overSummaryBound := atSummaryBound
	overSummaryBound.CaseRunSummary += "y"
	issue, _, err = NewIssueCandidate(overSummaryBound)
	if err != nil {
		t.Fatalf("NewIssueCandidate (summary over bound): %v", err)
	}
	if issue.Summary != atSummaryBound.CaseRunSummary {
		t.Fatalf("summary over bound = %q, want the first %d runes preserved", issue.Summary, IssueSummaryMax)
	}
}

func TestCandidateSummaryFallback(t *testing.T) {
	in := validCandidateInput()
	in.CaseRunSummary = ""
	issue, _, err := NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	want := "expected: HTTP 200\nactual: HTTP 500\nreason: status mismatch"
	if issue.Summary != want {
		t.Fatalf("fallback summary = %q, want %q", issue.Summary, want)
	}

	in.CaseRunSummary = "verdict: total mismatch"
	issue, _, err = NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate (case_run summary): %v", err)
	}
	if issue.Summary != in.CaseRunSummary {
		t.Fatalf("summary = %q, want the case_run summary %q", issue.Summary, in.CaseRunSummary)
	}

	whitespaceOnly := validCandidateInput()
	whitespaceOnly.CaseRunSummary = " \n\t "
	issue, _, err = NewIssueCandidate(whitespaceOnly)
	if err != nil {
		t.Fatalf("NewIssueCandidate (whitespace summary): %v", err)
	}
	want = "expected: HTTP 200\nactual: HTTP 500\nreason: status mismatch"
	if issue.Summary != want {
		t.Fatalf("whitespace-only case_run summary must fall back, got %q want %q", issue.Summary, want)
	}

	in.CaseRunSummary = ""
	in.FailedActual = ""
	issue, _, err = NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate (partial detail): %v", err)
	}
	want = "expected: HTTP 200\nreason: status mismatch"
	if issue.Summary != want {
		t.Fatalf("partial fallback summary = %q, want %q", issue.Summary, want)
	}

	in.FailedExpected = "   "
	in.FailedReason = ""
	issue, _, err = NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate (no detail): %v", err)
	}
	if issue.Summary != "" {
		t.Fatalf("summary = %q, want empty when there is no detail", issue.Summary)
	}
}

func TestCandidateRefs(t *testing.T) {
	in := validCandidateInput()
	// A duplicate appears inside the first five evidence ids, so the expected
	// refs below only hold when de-duplication runs before the cap.
	in.EvidenceIDs = []int64{101, 101, 102, 103, 104, 105, 106}
	_, refs, err := NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	wantKinds := []string{
		IssueRefKindCaseRun,
		IssueRefKindCaseVersion,
		IssueRefKindWave,
		IssueRefKindStepResult,
		IssueRefKindAssertionResult,
		IssueRefKindAssertionResult,
		IssueRefKindEvidence,
		IssueRefKindEvidence,
		IssueRefKindEvidence,
		IssueRefKindEvidence,
		IssueRefKindEvidence,
		IssueRefKindRequirement,
	}
	wantIDs := []int64{2, 3, 66, 201, 301, 302, 101, 102, 103, 104, 105, 77}
	if len(refs) != len(wantKinds) {
		t.Fatalf("refs = %+v, want %d refs", refs, len(wantKinds))
	}
	for i, ref := range refs {
		if ref.Kind != wantKinds[i] || ref.RefID != wantIDs[i] {
			t.Fatalf("ref[%d] = {%s %d}, want {%s %d}", i, ref.Kind, ref.RefID, wantKinds[i], wantIDs[i])
		}
		if ref.Role != IssueRefRoleSource {
			t.Fatalf("ref[%d] role = %q, want source", i, ref.Role)
		}
		if ref.VersionSUTID == nil || *ref.VersionSUTID != *in.VersionSUTID {
			t.Fatalf("ref[%d] version_sut_id = %v, want %d", i, ref.VersionSUTID, *in.VersionSUTID)
		}
	}

	withoutWave := validCandidateInput()
	withoutWave.WaveID = nil
	withoutWave.EvidenceIDs = nil
	_, refs, err = NewIssueCandidate(withoutWave)
	if err != nil {
		t.Fatalf("NewIssueCandidate (no wave): %v", err)
	}
	if len(refs) != 6 {
		t.Fatalf("refs = %+v, want 6 refs without wave/evidence", refs)
	}
	for _, ref := range refs {
		if ref.Kind == IssueRefKindWave || ref.Kind == IssueRefKindEvidence {
			t.Fatalf("absent optional refs must be omitted: %+v", refs)
		}
	}
}

func TestCandidateResultRefs(t *testing.T) {
	in := validCandidateInput()
	in.WaveID = nil
	in.EvidenceIDs = nil
	// A duplicate appears inside the first three assertion ids, so the expected
	// refs below only hold when de-duplication runs before the cap.
	in.FailedAssertionResultIDs = []int64{301, 301, 302, 303, 304}
	_, refs, err := NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	wantKinds := []string{
		IssueRefKindCaseRun,
		IssueRefKindCaseVersion,
		IssueRefKindStepResult,
		IssueRefKindAssertionResult,
		IssueRefKindAssertionResult,
		IssueRefKindAssertionResult,
		IssueRefKindRequirement,
	}
	wantIDs := []int64{2, 3, 201, 301, 302, 303, 77}
	if len(refs) != len(wantKinds) {
		t.Fatalf("refs = %+v, want %d refs (assertions capped at %d)", refs, len(wantKinds), 3)
	}
	for i, ref := range refs {
		if ref.Kind != wantKinds[i] || ref.RefID != wantIDs[i] {
			t.Fatalf("ref[%d] = {%s %d}, want {%s %d}", i, ref.Kind, ref.RefID, wantKinds[i], wantIDs[i])
		}
	}

	coreOnly := validCandidateInput()
	coreOnly.WaveID = nil
	coreOnly.EvidenceIDs = nil
	coreOnly.RequirementID = 0
	coreOnly.FailedStepResultID = 0
	coreOnly.FailedAssertionResultIDs = nil
	_, refs, err = NewIssueCandidate(coreOnly)
	if err != nil {
		t.Fatalf("NewIssueCandidate (core only): %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want case_run and case_version only", refs)
	}
}

func TestCandidateRefsCap(t *testing.T) {
	if IssueRefsMax != 20 {
		t.Fatalf("IssueRefsMax = %d, want 20", IssueRefsMax)
	}
	refs := make([]IssueRef, 25)
	for i := range refs {
		refs[i] = IssueRef{Kind: IssueRefKindEvidence, RefID: int64(i + 1)}
	}
	capped := capIssueRefs(refs)
	if len(capped) != IssueRefsMax {
		t.Fatalf("capped refs = %d, want %d", len(capped), IssueRefsMax)
	}
	for i, ref := range capped {
		if ref.RefID != int64(i+1) {
			t.Fatalf("capped[%d] = %+v, want insertion order preserved", i, ref)
		}
	}
}

func TestCandidateFingerprint(t *testing.T) {
	base, _, err := NewIssueCandidate(validCandidateInput())
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	for i := 0; i < len(base.Fingerprint); i++ {
		c := base.Fingerprint[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("fingerprint %q is not lowercase hex", base.Fingerprint)
		}
	}

	variants := []struct {
		name   string
		mutate func(*CandidateInput)
	}{
		{"case title", func(in *CandidateInput) { in.CaseTitle += " v2" }},
		{"category", func(in *CandidateInput) { in.Category = "api" }},
		{"severity", func(in *CandidateInput) { in.Severity = "minor" }},
		{"step seq", func(in *CandidateInput) { in.StepSeq++ }},
		{"assertion seq", func(in *CandidateInput) { in.AssertionSeq++ }},
		{"case run", func(in *CandidateInput) { in.CaseRunID++ }},
	}
	for _, variant := range variants {
		in := validCandidateInput()
		variant.mutate(&in)
		other, _, err := NewIssueCandidate(in)
		if err != nil {
			t.Fatalf("%s: NewIssueCandidate: %v", variant.name, err)
		}
		if other.Fingerprint == base.Fingerprint {
			t.Errorf("%s change must change the fingerprint", variant.name)
		}
	}
}

func TestCandidateFingerprintGolden(t *testing.T) {
	in := CandidateInput{
		VersionID:     11,
		CaseRunID:     22,
		CaseVersionID: 33,
		CaseTitle:     "Login",
		StepSeq:       3,
		AssertionSeq:  4,
		FailureClass:  "application",
		Category:      "api",
		Severity:      "major",
		DedupeHint:    "0123456789abcdef0123456789abcdef",
	}
	issue, _, err := NewIssueCandidate(in)
	if err != nil {
		t.Fatalf("NewIssueCandidate: %v", err)
	}
	if want := "Login · step 3: api failure"; issue.Title != want {
		t.Fatalf("title = %q, want %q", issue.Title, want)
	}
	if issue.DedupeKey != in.DedupeHint {
		t.Fatalf("dedupe_key = %q, want the hint %q", issue.DedupeKey, in.DedupeHint)
	}
	// Literal golden: sha256 of the canonical JSON
	// {"version_id":11,"case_run_id":22,"case_version_id":33,"step_seq":3,
	//  "assertion_seq":4,"dedupe_key":"...","title":"Login · step 3: api failure",
	//  "category":"api","severity":"major"} (spec §5.1). Pinned so any field,
	// order or encoding change of the fingerprint input fails.
	const wantFingerprint = "fd6d1c2b3eb8c86450d71496d5f291c4c92c74dfdf06e0bb8781338c2f404985"
	if issue.Fingerprint != wantFingerprint {
		t.Fatalf("fingerprint = %q, want the pinned canonical-JSON hash %q", issue.Fingerprint, wantFingerprint)
	}
}
