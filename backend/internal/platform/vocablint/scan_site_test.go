package vocablint_test

// T11e-6 M5 + review Minor 3: the exemption registry must be 1:1 with the
// OCCURRENCES it adjudicates (not merely the lines), and a scan-surface floor must
// report a context nobody registered. Both are multi-state discriminating tests,
// so a check that silently stopped firing cannot pass.

import (
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// TestFragmentSiteProblemsEnforcesOnePerSite is the M5 pair. Two source lines of
// one file carry the same text (the real shape of
// versionadapter/requirements.go:50/:63): ONE fragment used to sanction both, so
// repaying either left the registry non-vacuous and the stale check quiet.
func TestFragmentSiteProblemsEnforcesOnePerSite(t *testing.T) {
	const file = "backend/internal/x/infrastructure/a.go"
	const text = `return tools.Summary{}, a.missing("requirements")`
	entry := func(copies int) map[string]vocablint.AllowEntry {
		texts := make([]string, copies)
		for i := range texts {
			texts[i] = text
		}
		return map[string]vocablint.AllowEntry{
			"requirements": {Reason: "a diagnostic label, not a status", Fragments: vocablint.FileFragments(file, texts...)},
		}
	}
	sites := func(lines ...int) map[vocablint.FragmentKey][]vocablint.OccurrenceSite {
		out := map[vocablint.FragmentKey][]vocablint.OccurrenceSite{}
		for i, line := range lines {
			key := vocablint.FragmentKey{Value: "requirements", Index: i}
			out[key] = []vocablint.OccurrenceSite{{Line: line}}
		}
		return out
	}

	// One fragment, two sites -> problem (the T11e-5 Minor 5 defect).
	problems := vocablint.FragmentSiteProblems(entry(1), map[vocablint.FragmentKey][]vocablint.OccurrenceSite{
		{Value: "requirements", Index: 0}: {{Line: 50}, {Line: 63}},
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "ONE FRAGMENT PER OCCURRENCE") {
		t.Fatalf("one fragment sanctioning two sites produced %v, want one 1:1 problem", problems)
	}
	if !strings.Contains(problems[0], "[50 63]") {
		t.Fatalf("the 1:1 problem does not name the sites it covers: %s", problems[0])
	}

	// Two fragments, two sites -> honest, no problem. The line NUMBERS are not
	// stored anywhere, so an unrelated edit that shifts them cannot stale the
	// table: the same declaration at lines 250/263 is equally honest.
	for _, lines := range [][]int{{50, 63}, {250, 263}} {
		if problems := vocablint.FragmentSiteProblems(entry(2), sites(lines...)); len(problems) != 0 {
			t.Fatalf("two fragments for two sites %v reported %v", lines, problems)
		}
	}

	// Two fragments, one site (one of the twin sites was repaid) -> problem
	// again: this is the direction the old text-keyed stale check missed.
	problems = vocablint.FragmentSiteProblems(entry(2), map[vocablint.FragmentKey][]vocablint.OccurrenceSite{
		{Value: "requirements", Index: 0}: {{Line: 50}},
		{Value: "requirements", Index: 1}: {{Line: 50}},
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "declares 2 fragment(s)") {
		t.Fatalf("a repaid twin site produced %v, want one 1:1 problem naming the surplus fragment", problems)
	}

	// A vacuous fragment is the STALE check's business, not this one: reporting
	// it twice would bury the actionable diagnostic.
	if problems := vocablint.FragmentSiteProblems(entry(1), map[vocablint.FragmentKey][]vocablint.OccurrenceSite{}); len(problems) != 0 {
		t.Fatalf("a fragment that sanctions nothing was reported by the 1:1 check: %v", problems)
	}
}

// TestFragmentSiteProblemsIsPerOccurrenceNotPerLine is the review Minor 3 pair: a
// frozen template line carries the value TWICE, so ONE whole-line fragment spans
// two occurrences. Counting lines would call that honest; counting occurrences
// demands two fragments, so repaying either attribute is detected.
func TestFragmentSiteProblemsIsPerOccurrenceNotPerLine(t *testing.T) {
	const file = "backend/internal/delivery/infrastructure/render/template.go"
	const text = `<section id="issues" data-section="issues">`
	entry := func(copies int) map[string]vocablint.AllowEntry {
		texts := make([]string, copies)
		for i := range texts {
			texts[i] = text
		}
		return map[string]vocablint.AllowEntry{
			"issues": {Reason: "a frozen report section anchor", Fragments: vocablint.FileFragments(file, texts...)},
		}
	}
	bothOccurrences := map[vocablint.FragmentKey][]vocablint.OccurrenceSite{
		{Value: "issues", Index: 0}: {{Line: 123, Index: 0}, {Line: 123, Index: 1}},
	}
	problems := vocablint.FragmentSiteProblems(entry(1), bothOccurrences)
	if len(problems) != 1 || !strings.Contains(problems[0], "2 occurrence(s)") {
		t.Fatalf("one fragment spanning two occurrences on one line produced %v, want one 1:1 problem", problems)
	}
	if !strings.Contains(problems[0], "123#1") {
		t.Fatalf("the problem does not name the second occurrence: %s", problems[0])
	}
	twoFragments := map[vocablint.FragmentKey][]vocablint.OccurrenceSite{
		{Value: "issues", Index: 0}: {{Line: 123, Index: 0}, {Line: 123, Index: 1}},
		{Value: "issues", Index: 1}: {{Line: 123, Index: 0}, {Line: 123, Index: 1}},
	}
	if problems := vocablint.FragmentSiteProblems(entry(2), twoFragments); len(problems) != 0 {
		t.Fatalf("two fragments for two occurrences on one line reported %v", problems)
	}
	// One of the two attributes repaid -> two fragments, one occurrence -> loud.
	repaid := map[vocablint.FragmentKey][]vocablint.OccurrenceSite{
		{Value: "issues", Index: 0}: {{Line: 123, Index: 0}},
		{Value: "issues", Index: 1}: {{Line: 123, Index: 0}},
	}
	if problems := vocablint.FragmentSiteProblems(entry(2), repaid); len(problems) != 1 {
		t.Fatalf("a repaid occurrence on a two-occurrence line produced %v, want one problem", problems)
	}
}

// TestOccurrenceRanksSeparateQuoteForms proves the site identity does not collapse
// a line carrying BOTH quote forms of one value: 'issues' and "issues" on the same
// line are two distinct occurrences, not one.
func TestOccurrenceRanksSeparateQuoteForms(t *testing.T) {
	const file = "backend/internal/x/infrastructure/a.go"
	src := "package p\nvar q = `SELECT 1 WHERE a = 'issues' AND b = \"issues\"`\n"
	allow := map[string]vocablint.AllowEntry{
		"issues": {Reason: "two adjudicated occurrences", Fragments: vocablint.FileFragments(file,
			`var q = `+"`SELECT 1 WHERE a = 'issues' AND b = \"issues\"`")},
	}
	scan := vocablint.ScanLiterals(file, src, map[string][]string{"issues": {"x.Issues"}}, allow)
	seen := map[vocablint.OccurrenceSite]bool{}
	for _, sites := range scan.FragmentSites {
		for _, s := range sites {
			seen[s] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("a line with both quote forms produced %d distinct sites (%v), want 2", len(seen), seen)
	}
	if !seen[vocablint.OccurrenceSite{Line: 2, Index: 0}] || !seen[vocablint.OccurrenceSite{Line: 2, Index: 1}] {
		t.Fatalf("the two quote forms did not get ranks 0 and 1: %v", seen)
	}
	// And the 1:1 check therefore demands two fragments for that one line.
	if problems := vocablint.FragmentSiteProblems(allow, scan.FragmentSites); len(problems) != 1 {
		t.Fatalf("one fragment for two quote-form occurrences produced %v, want one problem", problems)
	}
}

// TestFragmentSiteProblemsIsPerFileAndText proves the grouping key: the same text
// in TWO files is two independent groups (each 1:1), and two DIFFERENT texts
// covering one occurrence are not a surplus (the coverage algorithm legitimately
// lets a long and a short fragment overlap).
func TestFragmentSiteProblemsIsPerFileAndText(t *testing.T) {
	const a = "backend/internal/x/infrastructure/a.go"
	const b = "backend/internal/x/infrastructure/b.go"
	allow := map[string]vocablint.AllowEntry{
		"api": {
			Reason: "a wire kind, not a status",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments(a, `Kind: "api",`),
				vocablint.FileFragments(b, `Kind: "api",`),
			),
		},
	}
	sites := map[vocablint.FragmentKey][]vocablint.OccurrenceSite{
		{Value: "api", Index: 0}: {{Line: 113}},
		{Value: "api", Index: 1}: {{Line: 181}},
	}
	if problems := vocablint.FragmentSiteProblems(allow, sites); len(problems) != 0 {
		t.Fatalf("the same text in two files was treated as one group: %v", problems)
	}
	overlap := map[string]vocablint.AllowEntry{
		"api": {
			Reason: "a wire kind, not a status",
			Fragments: vocablint.FileFragments(a,
				`SUTID: sut.SUTID, ID: api.ID, Kind: "api", Name: api.Name,`,
				`Kind: "api",`),
		},
	}
	overlapSites := map[vocablint.FragmentKey][]vocablint.OccurrenceSite{
		{Value: "api", Index: 0}: {{Line: 113}},
		{Value: "api", Index: 1}: {{Line: 113}},
	}
	if problems := vocablint.FragmentSiteProblems(overlap, overlapSites); len(problems) != 0 {
		t.Fatalf("two different texts covering one occurrence were reported as a surplus: %v", problems)
	}
}

// TestOccurrenceCoverageDeduplicatesOverlappingIntervals is the round-2 nit: a
// fragment whose text matches the line at overlapping intervals must report each
// occurrence ONCE, so FragmentHits counts occurrences rather than match intervals.
func TestOccurrenceCoverageDeduplicatesOverlappingIntervals(t *testing.T) {
	// "aa" occurs at 0 and 1 in "aaa"; the token "a" occurs at 0, 1 and 2. The
	// interval [0,2) covers a@0,a@1 and [1,3) covers a@1,a@2 -- a@1 is covered by
	// BOTH intervals and must be reported once.
	line := `x = "aaa"`
	token := `"aaa"`
	// Use a fragment that itself overlaps: "aa" inside the quoted run.
	covered, per := vocablint.OccurrenceCoverage(line, token, []vocablint.Exemption{{Fragment: `"aaa"`, Reason: "r"}})
	if len(covered) != 1 || !covered[0] {
		t.Fatalf("coverage = %v, want the single occurrence covered", covered)
	}
	if got := len(per[0]); got != 1 {
		t.Fatalf("a single-interval fragment reported %d occurrence indices, want 1", got)
	}
	// Now the overlapping case proper: token 'a' (unquoted form is not what the
	// scanner uses, so exercise the algorithm directly with a short token).
	covered, per = vocablint.OccurrenceCoverage("aaa", "a", []vocablint.Exemption{{Fragment: "aa", Reason: "r"}})
	if len(covered) != 3 || !covered[0] || !covered[1] || !covered[2] {
		t.Fatalf("overlapping intervals did not cover all three occurrences: %v", covered)
	}
	seen := map[int]int{}
	for _, j := range per[0] {
		seen[j]++
	}
	for j, n := range seen {
		if n != 1 {
			t.Fatalf("occurrence %d reported %d times by one fragment; hits would inflate", j, n)
		}
	}
	if len(per[0]) != 3 {
		t.Fatalf("fragment spans %d distinct occurrences, want 3: %v", len(per[0]), per[0])
	}
}

// TestSurfaceFloorProblemsReportsUnregisteredContext is the scan-surface twin of
// TestTypeFloorProblemsReportsUnregisteredType: a context that appears in the
// derived surface with no floor entry has no anti-vacuity protection at all, so
// it must be reported (H-A25: a twentieth context cannot join unnoticed).
func TestSurfaceFloorProblemsReportsUnregisteredContext(t *testing.T) {
	counts := map[string]int{"agent": 31, "newcontext": 2}
	floors := map[string]int{"agent": 31}
	problems := vocablint.SurfaceFloorProblems(counts, floors)
	if len(problems) != 1 || !strings.Contains(problems[0], `bounded context "newcontext"`) ||
		!strings.Contains(problems[0], "no anti-vacuity floor entry") {
		t.Fatalf("unregistered context problems = %v, want one naming newcontext", problems)
	}
	floors["newcontext"] = 2
	if problems := vocablint.SurfaceFloorProblems(counts, floors); len(problems) != 0 {
		t.Fatalf("registered and satisfied floors reported %v", problems)
	}
	floors["newcontext"] = 5
	problems = vocablint.SurfaceFloorProblems(counts, floors)
	if len(problems) != 1 || !strings.Contains(problems[0], "only 2 newcontext infrastructure production files derived (want >= 5)") {
		t.Fatalf("below-floor problems = %v, want one naming the shortfall", problems)
	}
}
