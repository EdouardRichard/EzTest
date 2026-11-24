package vocablint_test

// T11e-5 Part A precision discriminations: the file-bound fragment (A1), the
// index-keyed stale check (A2), the mandatory file binding, and the temporary
// exposure registry's route marker. Each is a real bad sample that the
// pre-T11e-5 machinery passed silently.

import (
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// TestScanLiteralsFragmentIsFileScoped is the Part A1 discrimination (H-A22① +
// T11e-4 M2): a SHORT fragment used to sanction the same text in EVERY scanned
// file, and because the original site still matched, the stale check stayed quiet
// too. Copying the sanctioned site into another file must now FAIL.
func TestScanLiteralsFragmentIsFileScoped(t *testing.T) {
	values := map[string][]string{"failed": {"execution.CaseRunFailed"}}
	allow := map[string]vocablint.AllowEntry{
		"failed": {
			Reason:    "the OpenAI Responses wire status, not an EzTest entity status",
			Fragments: vocablint.FileFragments("llm/responses.go", `case "failed":`),
		},
	}
	// The adjudicated site in its own file stays sanctioned.
	own := "package x\nfunc f() {\n\tswitch s {\n\tcase \"failed\":\n\t\treturn 1\n\t}\n}\n"
	if got := vocablint.ScanLiterals("llm/responses.go", own, values, allow).Findings; len(got) != 0 {
		t.Fatalf("the fragment did not sanction its own file: %+v", got)
	}
	// The SAME TEXT copied into another file is a real status switch: it must fire.
	copied := "package y\nfunc g() {\n\tswitch status {\n\tcase \"failed\":\n\t\treturn 2\n\t}\n}\n"
	got := vocablint.ScanLiterals("execution/case_run.go", copied, values, allow).Findings
	if len(got) != 1 {
		t.Fatalf("a short fragment sanctioned the same text in ANOTHER file (the H-A22① hole): %+v", got)
	}
	if got[0].File != "execution/case_run.go" || got[0].Line != 4 {
		t.Fatalf("cross-file finding is mislocated: %+v", got[0])
	}
	// The live exemption is not reported stale just because a copy exists elsewhere.
	scan := vocablint.ScanLiterals("llm/responses.go", own, values, allow)
	if problems := vocablint.StaleFragmentProblems(allow, scan.FragmentHits); len(problems) != 0 {
		t.Fatalf("the live fragment was reported stale: %v", problems)
	}
}

// TestStaleFragmentProblemsIsIndexKeyed is the Part A2 discrimination (H-A22②):
// two fragments with IDENTICAL text must not share a hit count. The text-keyed map
// let an idle exemption ride on its twin's hits.
func TestStaleFragmentProblemsIsIndexKeyed(t *testing.T) {
	values := map[string][]string{"text": {"version.MessagePartText"}}
	fragment := `case "text":`
	allow := map[string]vocablint.AllowEntry{
		"text": {
			Reason: "two adjudicated files, one of them repaid",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("a.go", fragment),
				vocablint.FileFragments("b.go", fragment),
			),
		},
	}
	src := "package x\nfunc f() {\n\tswitch t {\n\tcase \"text\":\n\t\treturn\n\t}\n}\n"
	hits := map[vocablint.FragmentKey]int{}
	scan := vocablint.ScanLiterals("a.go", src, values, allow)
	for key, count := range scan.FragmentHits {
		hits[key] += count
	}
	problems := vocablint.StaleFragmentProblems(allow, hits)
	if len(problems) != 1 || !strings.Contains(problems[0], "b.go") {
		t.Fatalf("the idle twin was hidden by the shared fragment text: %v", problems)
	}
	// Two entries for DIFFERENT values sharing one text are tracked separately too.
	shared := map[string]vocablint.AllowEntry{
		"text":     {Reason: "a", Fragments: vocablint.FileFragments("a.go", fragment)},
		"thinking": {Reason: "b", Fragments: vocablint.FileFragments("a.go", fragment)},
	}
	both := map[string][]string{"text": {"version.MessagePartText"}, "thinking": {"version.MessagePartThinking"}}
	scan = vocablint.ScanLiterals("a.go", src, both, shared)
	if len(vocablint.StaleFragmentProblems(shared, scan.FragmentHits)) != 1 {
		t.Fatalf("cross-value fragment sharing was not tracked per value: %v", scan.FragmentHits)
	}
}

// TestAllowlistShapeRejectsUnboundFragment pins the shape rule that makes the file
// binding mandatory: an entry whose fragment carries no file is ill-formed.
func TestAllowlistShapeRejectsUnboundFragment(t *testing.T) {
	bad := map[string]vocablint.AllowEntry{
		"text": {Reason: "why", Fragments: []vocablint.Fragment{{Text: `case "text":`}}},
	}
	problems := vocablint.AllowlistShapeProblems(bad)
	if len(problems) != 1 || !strings.Contains(problems[0], "file binding") {
		t.Fatalf("an unbound fragment was accepted: %v", problems)
	}
}

// TestMergeAllowlistsKeepsBothAdjudications proves the temporary registry cannot
// erase a permanent one: both reasons and both fragment tables survive, and the
// base indices are unchanged (so the base's stale tracking still addresses it).
func TestMergeAllowlistsKeepsBothAdjudications(t *testing.T) {
	base := map[string]vocablint.AllowEntry{
		"text": {Reason: "permanent", Fragments: vocablint.FileFragments("a.go", `case "text":`)},
	}
	pending := map[string]vocablint.AllowEntry{
		"text":  {Reason: "PENDING T11e-6", Fragments: vocablint.FileFragments("b.go", `case "text":`)},
		"other": {Reason: "PENDING T11e-6", Fragments: vocablint.FileFragments("c.go", `x`)},
	}
	merged := vocablint.MergeAllowlists(base, pending)
	if len(merged["text"].Fragments) != 2 || merged["text"].Fragments[0].File != "a.go" {
		t.Fatalf("merge lost or reordered fragments: %+v", merged["text"])
	}
	if !strings.Contains(merged["text"].Reason, "permanent") || !strings.Contains(merged["text"].Reason, "PENDING") {
		t.Fatalf("merge lost an adjudication: %q", merged["text"].Reason)
	}
	if len(base["text"].Fragments) != 1 {
		t.Fatal("merge mutated the base table")
	}
	if len(merged) != 2 {
		t.Fatalf("merge dropped an entry: %+v", merged)
	}
}
