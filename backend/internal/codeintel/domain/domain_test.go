package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestFactKey pins the fact identity rules from design spec §6.2.
func TestFactKey(t *testing.T) {
	cases := []struct {
		kind string
		name string
		path string
		want string
	}{
		{KindRoute, "orders", "", "route:/orders"},
		{KindRoute, "/orders", "", "route:/orders"},
		{KindPage, "OrdersPage", "", "page:OrdersPage"},
		{KindAPI, "post /api/refund", "", "api:POST /api/refund"},
		{KindAPI, "GET /api/orders/{id}", "", "api:GET /api/orders/{}"},
		{KindClientCall, "post https://x.test/api/refund/42?x=1", "", "client:POST /api/refund/{}"},
		{KindClientCall, "post https://x.test?q=1", "", "client:POST /"},
		{KindComponent, "OrdersPage", "src/pages/OrdersPage.tsx", "component:src/pages/OrdersPage.tsx#OrdersPage"},
		{KindModule, "refund", "server/refund.go", "symbol:server/refund.go#refund"},
		{KindSymbol, "Refund", "src/refund.ts", "symbol:src/refund.ts#Refund"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+" "+tc.name, func(t *testing.T) {
			got, err := FactKey(tc.kind, tc.name, tc.path)
			if err != nil {
				t.Fatalf("FactKey(%q, %q, %q) error: %v", tc.kind, tc.name, tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("FactKey(%q, %q, %q)=%q want %q", tc.kind, tc.name, tc.path, got, tc.want)
			}
		})
	}
}

func TestFactKeyRejectsUnkeyableFacts(t *testing.T) {
	cases := []struct {
		name string
		kind string
		fact string
		path string
	}{
		{"empty name", KindRoute, "", ""},
		{"empty page", KindPage, "   ", ""},
		{"file is not a fact", KindFile, "src/a.ts", "src/a.ts"},
		{"unknown kind", "widget", "x", ""},
		{"api without method", KindAPI, "/api/refund", ""},
		{"api without path", KindAPI, "POST", ""},
		{"client_call without method", KindClientCall, "/api/refund", ""},
		{"component without path", KindComponent, "OrdersPage", ""},
		{"module without name", KindModule, "", "src/a.ts"},
		{"absolute path", KindSymbol, "x", "/etc/passwd"},
		{"path escape", KindSymbol, "x", "../secret.ts"},
		{"too long", KindRoute, "/" + strings.Repeat("a", MaxFactKeyChars), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FactKey(tc.kind, tc.fact, tc.path); err == nil {
				t.Fatalf("FactKey(%q, %q, %q) accepted an unkeyable fact", tc.kind, tc.fact, tc.path)
			}
		})
	}
}

// TestFactKeyLengthBoundary pins MaxFactKeyChars as an inclusive upper bound: a
// key of exactly 512 characters is accepted, one character more is rejected.
func TestFactKeyLengthBoundary(t *testing.T) {
	// route: + "/" + name -> 6 + 1 + len(name); 505 keeps the key at 512.
	name := strings.Repeat("a", MaxFactKeyChars-len("route:")-1)
	key, err := FactKey(KindRoute, name, "")
	if err != nil {
		t.Fatalf("FactKey rejected a %d-character key at the limit: %v", MaxFactKeyChars, err)
	}
	if len(key) != MaxFactKeyChars {
		t.Fatalf("key length=%d want %d", len(key), MaxFactKeyChars)
	}

	if _, err := FactKey(KindRoute, name+"a", ""); err == nil {
		t.Fatalf("FactKey accepted a %d-character key beyond the limit", MaxFactKeyChars+1)
	}
}

func TestFactKeyIsDeterministic(t *testing.T) {
	first, err := FactKey(KindAPI, "POST https://x.test/api//orders/42/?q=1", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := FactKey(KindAPI, "post /api/orders/{id}", "")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent facts produced different keys: %q != %q", first, second)
	}
}

// TestNormalizeMethodPath pins the cross-stack contract normalisation from
// design spec §6.5.
func TestNormalizeMethodPath(t *testing.T) {
	cases := []struct {
		method     string
		path       string
		wantMethod string
		wantPath   string
	}{
		{"POST", "https://x.test/api/orders/42?q=1", "POST", "/api/orders/{}"},
		{"post", "/api/refund/{id}", "POST", "/api/refund/{}"},
		{"GET", "/api//refund/", "GET", "/api/refund"},
		{"get", "/api/users/3f2a1c4e-9b7d-4e2f-8a61-2c3d4e5f6a7b", "GET", "/api/users/{}"},
		{"DELETE", "/api/orders/:id", "DELETE", "/api/orders/{}"},
		{"PUT", "/api/orders/<orderId>", "PUT", "/api/orders/{}"},
		{"", "post /api/refund/{id}", "POST", "/api/refund/{}"},
		{"", "GET /api//refund/", "GET", "/api/refund"},
		{"", "get /api/users/<id>", "GET", "/api/users/{}"},
		{"GET", "https://x.test:8443/a/b#frag", "GET", "/a/b"},
		{"GET", "https://x.test", "GET", "/"},
		{"GET", "https://x.test?q=1", "GET", "/"},
		{"", "post https://x.test#frag", "POST", "/"},
		{"GET", "/", "GET", "/"},
		{"GET", "/Orders/42", "GET", "/Orders/{}"},
		{"GET", "orders", "GET", "/orders"},
		{"", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			gotMethod, gotPath := NormalizeMethodPath(tc.method, tc.path)
			if gotMethod != tc.wantMethod || gotPath != tc.wantPath {
				t.Fatalf("NormalizeMethodPath(%q, %q)=(%q, %q) want (%q, %q)",
					tc.method, tc.path, gotMethod, gotPath, tc.wantMethod, tc.wantPath)
			}
		})
	}
}

// TestCandidateOrderAndDefaultSelection pins design spec §6.6 steps 5 and 6.
func TestCandidateOrderAndDefaultSelection(t *testing.T) {
	candidates := []ImpactCandidate{
		{ID: 9, Source: CandidateAI, Confidence: ConfidenceLow, SubjectKind: CandidateRequirement, SubjectID: 3},
		{ID: 8, Source: CandidateRelation, Confidence: ConfidenceMedium, SubjectKind: CandidateRequirement, SubjectID: 2},
		{ID: 7, Source: CandidateContract, Confidence: ConfidenceHigh, SubjectKind: CandidateRequirement, SubjectID: 1},
		{ID: 6, Source: CandidateDirect, Confidence: ConfidenceHigh, SubjectKind: CandidateRequirement, SubjectID: 5},
		{ID: 5, Source: CandidateDirect, Confidence: ConfidenceHigh, SubjectKind: CandidateAppModel, SubjectID: 1},
		{ID: 4, Source: CandidateDirect, Confidence: ConfidenceMedium, SubjectKind: CandidateRequirement, SubjectID: 1},
	}
	SortCandidates(candidates)

	wantIDs := []int64{6, 5, 4, 7, 8, 9}
	for i, want := range wantIDs {
		if candidates[i].ID != want {
			t.Fatalf("candidates[%d].ID=%d want %d (order=%v)", i, candidates[i].ID, want, candidateIDs(candidates))
		}
	}

	// Sorting again must not change the order (INV-4 determinism).
	SortCandidates(candidates)
	for i, want := range wantIDs {
		if candidates[i].ID != want {
			t.Fatalf("second sort changed candidates[%d].ID=%d want %d", i, candidates[i].ID, want)
		}
	}

	if !DefaultSelected(ConfidenceHigh) || !DefaultSelected(ConfidenceMedium) {
		t.Fatal("high/medium candidates must be selected by default")
	}
	if DefaultSelected(ConfidenceLow) {
		t.Fatal("low candidates must stay unselected by default")
	}
}

func candidateIDs(candidates []ImpactCandidate) []int64 {
	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	return ids
}

// TestCandidateOrderTieBreak pins the deterministic tie-breaks below the four
// spec keys: candidates sharing source, confidence and subject kind order by
// subject ID, and full ties order by candidate ID (INV-4).
func TestCandidateOrderTieBreak(t *testing.T) {
	candidates := []ImpactCandidate{
		{ID: 30, Source: CandidateDirect, Confidence: ConfidenceHigh, SubjectKind: CandidateRequirement, SubjectID: 7},
		{ID: 10, Source: CandidateDirect, Confidence: ConfidenceHigh, SubjectKind: CandidateRequirement, SubjectID: 2},
		{ID: 20, Source: CandidateDirect, Confidence: ConfidenceHigh, SubjectKind: CandidateRequirement, SubjectID: 7},
	}
	SortCandidates(candidates)

	wantIDs := []int64{10, 20, 30}
	for i, want := range wantIDs {
		if candidates[i].ID != want {
			t.Fatalf("candidates[%d].ID=%d want %d (order=%v)", i, candidates[i].ID, want, candidateIDs(candidates))
		}
	}
}

func TestValidateSubjectKind(t *testing.T) {
	for _, kind := range []string{"version", "requirement", "app_model", "case", "issue"} {
		if err := ValidateSubjectKind(kind); err != nil {
			t.Fatalf("ValidateSubjectKind(%q) rejected a supported subject: %v", kind, err)
		}
	}
	for _, kind := range []string{"", "Issue", "user"} {
		err := ValidateSubjectKind(kind)
		if err == nil {
			t.Fatalf("ValidateSubjectKind(%q) accepted an unsupported subject", kind)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("ValidateSubjectKind(%q) error %v does not wrap ErrInvalid", kind, err)
		}
		if got := Reason(err); got != "unsupported_subject_kind" {
			t.Fatalf("ValidateSubjectKind(%q) reason=%q want unsupported_subject_kind", kind, got)
		}
	}
}

// TestAnchorSubjectCaseAccepted pins D37: case is a first-class anchor subject.
func TestAnchorSubjectCaseAccepted(t *testing.T) {
	if SubjectCase != "case" {
		t.Fatalf("SubjectCase=%q want case", SubjectCase)
	}
	if err := ValidateSubjectKind(SubjectCase); err != nil {
		t.Fatalf("ValidateSubjectKind(SubjectCase) = %v, want nil", err)
	}
}

// TestAnchorSubjectIssueAccepted pins P7 design spec §7.9: issue is a
// first-class anchor subject now that the code_anchor CHECK is widened.
func TestAnchorSubjectIssueAccepted(t *testing.T) {
	if SubjectIssue != "issue" {
		t.Fatalf("SubjectIssue=%q want issue", SubjectIssue)
	}
	if err := ValidateSubjectKind(SubjectIssue); err != nil {
		t.Fatalf("ValidateSubjectKind(SubjectIssue) = %v, want nil", err)
	}
}

// TestAnchorSubjectUnknownRejected pins the stable unsupported_subject_kind
// reason and the five-kind message for an unknown anchor subject.
func TestAnchorSubjectUnknownRejected(t *testing.T) {
	for _, kind := range []string{"", "Issue", "Case", "user"} {
		err := ValidateSubjectKind(kind)
		if err == nil {
			t.Fatalf("ValidateSubjectKind(%q) accepted an unknown subject", kind)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("ValidateSubjectKind(%q) error %v does not wrap ErrInvalid", kind, err)
		}
		if got := Reason(err); got != "unsupported_subject_kind" {
			t.Fatalf("ValidateSubjectKind(%q) reason=%q want unsupported_subject_kind", kind, got)
		}
		if !strings.Contains(err.Error(), "version, requirement, app_model, case or issue") {
			t.Fatalf("ValidateSubjectKind(%q) message=%q does not list the five kinds", kind, err.Error())
		}
	}
}

func TestValidateEvidenceRange(t *testing.T) {
	valid := []struct{ start, end, total, max int }{
		{1, 5, 10, 40},
		{5, 5, 5, 40},
		{1, 40, 100, 40},
		{1, 200, 1000, 0}, // maxLines=0 disables the span check
		{1, 5, 0, 40},     // totalLines=0 means "unknown", skip the upper bound
	}
	for _, tc := range valid {
		if err := ValidateEvidenceRange(tc.start, tc.end, tc.total, tc.max); err != nil {
			t.Fatalf("ValidateEvidenceRange(%d,%d,%d,%d) rejected a valid range: %v", tc.start, tc.end, tc.total, tc.max, err)
		}
	}

	invalid := []struct {
		name                   string
		start, end, total, max int
	}{
		{"start zero", 0, 5, 10, 40},
		{"negative start", -1, 5, 10, 40},
		{"end before start", 6, 5, 10, 40},
		{"end beyond total", 1, 11, 10, 40},
		{"span beyond max", 1, 41, 100, 40},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEvidenceRange(tc.start, tc.end, tc.total, tc.max)
			if err == nil {
				t.Fatalf("ValidateEvidenceRange(%d,%d,%d,%d) accepted an invalid range", tc.start, tc.end, tc.total, tc.max)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}

// TestLimits pins the P4 domain constants from design spec §4.2.
func TestLimits(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"MaxFactsPerFile", MaxFactsPerFile, 200},
		{"MaxEdgesPerFile", MaxEdgesPerFile, 200},
		{"MaxEvidencePerFact", MaxEvidencePerFact, 8},
		{"MaxExcerptLines", MaxExcerptLines, 200},
		{"MaxFactKeyChars", MaxFactKeyChars, 512},
		{"MaxReasonChars", MaxReasonChars, 2000},
		{"MaxPathChars", MaxPathChars, 4096},
		{"MaxUnmappedEntries", MaxUnmappedEntries, 200},
		{"MaxChangedFilesStored", MaxChangedFilesStored, 2000},
		{"MaxTreeEntriesPerPage", MaxTreeEntriesPerPage, 200},
		{"MaxReadLines", MaxReadLines, 400},
		{"MaxRequirementsInPrompt", MaxRequirementsInPrompt, 100},
		{"MaxAnalysisErrors", MaxAnalysisErrors, 20},
		{"MaxFactVersionsSnapshot", MaxFactVersionsSnapshot, 500},
		{"MaxFileReadBytes", MaxFileReadBytes, 64 << 10},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s=%d want %d", tc.name, tc.got, tc.want)
		}
	}
}
