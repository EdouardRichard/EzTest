package vocablint_test

// Scanner properties migrated verbatim from TestHandoffLiteralGuardFires
// (version/infrastructure/postgres/handoff_repo_lint_test.go) plus the
// comment/struct-tag stripping and the fragment-level stale/shape rules.
// No assertion is weakened.

import (
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// TestScanLiteralsGuardFires proves the guard's properties in their strong forms.
func TestScanLiteralsGuardFires(t *testing.T) {
	values := map[string][]string{"approved": {"resource.SUTStatusApproved"}, "text": {"version.MessagePartText"}}

	// Fires on a real hardcoded literal.
	bad := "package x\nfunc f() { q := `SELECT 1 FROM t WHERE status = 'approved'` }\n"
	if got := vocablint.ScanLiterals("bad.go", bad, values, nil).Findings; len(got) != 1 {
		t.Fatalf("guard did not fire on a real hardcoded literal: %+v", got)
	}

	// Ignores comments.
	commentOnly := "package x\n// status = 'approved' is a documented counter-example\nfunc f() {}\n"
	if got := vocablint.ScanLiterals("c.go", commentOnly, values, nil).Findings; len(got) != 0 {
		t.Fatalf("guard fired on a comment: %+v", got)
	}

	// Ignores struct tags (the json:"..." collision class).
	tag := "package x\ntype T struct { F string `json:\"text\"` }\n"
	if got := vocablint.ScanLiterals("t.go", tag, values, nil).Findings; len(got) != 0 {
		t.Fatalf("guard fired on a struct tag: %+v", got)
	}

	// Fragment-scoped allowlist: exempt in the listed context...
	allow := map[string]vocablint.AllowEntry{
		"text": {Reason: "payload key list", Fragments: vocablint.FileFragments("l.go", `"summary", "text"`)},
	}
	listed := "package x\nvar keys = []string{\"summary\", \"text\"}\n"
	if scan := vocablint.ScanLiterals("l.go", listed, values, allow); len(scan.Findings) != 0 || !scan.SeenValues["text"] {
		t.Fatalf("guard did not exempt the listed fragment: %+v seen=%v", scan.Findings, scan.SeenValues)
	}
	// ...but the SAME allowlisted value in a NON-listed context still fires.
	nonListed := "package x\nfunc f() { if part.Type != \"text\" { return } }\n"
	if got := vocablint.ScanLiterals("n.go", nonListed, values, allow).Findings; len(got) != 1 {
		t.Fatalf("guard exempted an allowlisted value in a non-listed context: %+v", got)
	}

	// Self-tracking: after a rename the stale value stops matching and the new
	// value starts matching (both directions).
	renamed := map[string][]string{"ruling": {"resource.SUTStatusApproved"}}
	stale := "package x\nvar s = \"approved\"\n"
	if got := vocablint.ScanLiterals("s.go", stale, renamed, nil).Findings; len(got) != 0 {
		t.Fatalf("guard matched a stale value after the constant renamed: %+v", got)
	}
	fresh := "package x\nvar s = \"ruling\"\n"
	if got := vocablint.ScanLiterals("f.go", fresh, renamed, nil).Findings; len(got) != 1 {
		t.Fatalf("guard did not track the renamed constant value: %+v", got)
	}

	// A value defined by several declarations names ALL of them in the hint, not
	// just the last writer.
	shared := map[string][]string{"requirement": {"version.ConfirmationScopeRequirement", "version.MessageRefKindRequirement"}}
	dup := "package x\nvar s = \"requirement\"\n"
	got := vocablint.ScanLiterals("d.go", dup, shared, nil).Findings
	if len(got) != 1 ||
		!strings.Contains(got[0].Name, "ConfirmationScopeRequirement") || !strings.Contains(got[0].Name, "MessageRefKindRequirement") {
		t.Fatalf("guard hint did not accumulate all constant names: %+v", got)
	}
}

// TestScanLiteralsFragmentHitsAreFragmentKeyed pins the handoff gate's
// accumulation: a fragment shared by two values counts once per sanctioned site,
// and a fragment that sanctions nothing stays at zero (so the stale check fires).
func TestScanLiteralsFragmentHitsAreFragmentKeyed(t *testing.T) {
	values := map[string][]string{
		"decision": {"version.FactKindDecision"},
		"text":     {"version.MessagePartText"},
	}
	fragment := `"summary", "text", "value", "decision", "title"`
	allow := map[string]vocablint.AllowEntry{
		"decision": {Reason: "fact-payload summary key list", Fragments: vocablint.FileFragments("k.go", fragment)},
		"text":     {Reason: "fact-payload summary key list", Fragments: vocablint.FileFragments("k.go", fragment)},
	}
	src := "package x\nvar keys = []string{\"summary\", \"text\", \"value\", \"decision\", \"title\"}\n"
	scan := vocablint.ScanLiterals("k.go", src, values, allow)
	if len(scan.Findings) != 0 {
		t.Fatalf("shared fragment did not sanction both values: %+v", scan.Findings)
	}
	if scan.FragmentHits[vocablint.FragmentKey{Value: "decision", Index: 0}] != 1 ||
		scan.FragmentHits[vocablint.FragmentKey{Value: "text", Index: 0}] != 1 {
		t.Fatalf("fragment hits = %v, want one per value whose site it sanctions", scan.FragmentHits)
	}
	// A second fragment that matches nothing is stale even though the value it
	// belongs to still appears elsewhere.
	allow["text"] = vocablint.AllowEntry{
		Reason:    "two fragments, one stale",
		Fragments: vocablint.FileFragments("k.go", fragment, `part.Type == "text"`),
	}
	scan = vocablint.ScanLiterals("k.go", src, values, allow)
	problems := vocablint.StaleFragmentProblems(allow, scan.FragmentHits)
	if len(problems) != 1 || !strings.Contains(problems[0], `part.Type ==`) {
		t.Fatalf("stale fragment problems = %v, want exactly the unused fragment", problems)
	}
}

// TestScanLiteralsRogueSharingLineStillFires is minor③ in scanner form: a
// sanctioned occurrence and a rogue occurrence of the same value on ONE line must
// still produce a finding.
func TestScanLiteralsRogueSharingLineStillFires(t *testing.T) {
	values := map[string][]string{"approved": {"resource.SUTStatusApproved"}}
	allow := map[string]vocablint.AllowEntry{
		"approved": {Reason: "the summary key list", Fragments: vocablint.FileFragments("r.go", `"summary", "approved"`)},
	}
	src := "package x\nvar keys = []string{\"summary\", \"approved\"}; var rogue = \"approved\"\n"
	got := vocablint.ScanLiterals("r.go", src, values, allow).Findings
	if len(got) != 1 {
		t.Fatalf("rogue occurrence sharing a sanctioned line was hidden: %+v", got)
	}
	if got[0].Line != 2 {
		t.Fatalf("finding line = %d, want 2 (byte positions preserved by stripping)", got[0].Line)
	}
}

func TestStripCommentsAndTagsPreservesPositions(t *testing.T) {
	src := "package x\n// 'approved' in a line comment\n/* 'approved'\n   still a comment */\ntype T struct{ F string `json:\"approved\"` }\nvar s = \"approved\"\n"
	stripped := vocablint.StripCommentsAndTags(src)
	if len(stripped) != len(src) {
		t.Fatalf("stripping changed the byte length: %d vs %d", len(stripped), len(src))
	}
	if strings.Count(stripped, "\n") != strings.Count(src, "\n") {
		t.Fatal("stripping changed the line count; line numbers would drift")
	}
	if strings.Contains(stripped, "line comment") || strings.Contains(stripped, "still a comment") {
		t.Fatalf("comments were not blanked: %q", stripped)
	}
	if strings.Contains(stripped, `json:`) {
		t.Fatalf("struct tag was not blanked: %q", stripped)
	}
	if !strings.Contains(stripped, `var s = "approved"`) {
		t.Fatalf("a real code literal was blanked: %q", stripped)
	}
	// A raw literal that is NOT a struct tag stays (a SQL query is a raw literal).
	query := "package x\nvar q = `SELECT 1 WHERE status = 'approved'`\n"
	if !strings.Contains(vocablint.StripCommentsAndTags(query), "'approved'") {
		t.Fatal("a non-tag raw literal was blanked; SQL predicates would escape the guard")
	}
}

func TestAllowlistShapeProblems(t *testing.T) {
	good := map[string]vocablint.AllowEntry{
		"text": {Reason: "payload key", Fragments: vocablint.FileFragments("l.go", `"summary", "text"`)},
	}
	if problems := vocablint.AllowlistShapeProblems(good); len(problems) != 0 {
		t.Fatalf("well-formed allowlist reported problems: %v", problems)
	}
	bad := map[string]vocablint.AllowEntry{
		"noReason":   {Reason: "  ", Fragments: vocablint.FileFragments("l.go", `x`)},
		"noFragment": {Reason: "why", Fragments: nil},
	}
	problems := vocablint.AllowlistShapeProblems(bad)
	if len(problems) != 2 {
		t.Fatalf("shape problems = %v, want both ill-formed entries", problems)
	}
	for _, p := range problems {
		if !strings.Contains(p, "needs a non-empty reason and at least one fragment") {
			t.Fatalf("unexpected shape problem wording: %q", p)
		}
	}
}
