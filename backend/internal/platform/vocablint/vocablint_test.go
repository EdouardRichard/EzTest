package vocablint_test

// Discriminative properties of the occurrence-scoped sanction algorithm, migrated
// verbatim from the gate-local copies (design T2 H-F1 + review Important #1,
// agent T4 minor③, execution H-F1 share, handoff T3 minor③). Every assertion the
// five copies made is kept here; none is weakened.

import (
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// TestOccurrenceSanctionIsOccurrenceScoped is permanent deterministic proof of
// H-F1: a line carrying a sanctioned fragment AND a rogue occurrence of the same
// guarded value flags the rogue (only the sanctioned occurrence is exempted),
// while a line carrying only the sanctioned fragment is clean.
func TestOccurrenceSanctionIsOccurrenceScoped(t *testing.T) {
	exemptions := []vocablint.Exemption{
		{Fragment: `cv.status = 'draft'`, Reason: "design-own case status predicate"},
	}
	// Sanctioned only: clean.
	if remaining, _ := vocablint.OccurrenceSanction(`WHERE cv.status = 'draft'`, `'draft'`, exemptions); remaining != 0 {
		t.Fatalf("sanctioned-only line: remaining = %d, want 0", remaining)
	}
	// Sanctioned + rogue same value on one line: the rogue must still be flagged.
	rogue := `WHERE cv.status = 'draft' AND r.status = 'draft'`
	if remaining, _ := vocablint.OccurrenceSanction(rogue, `'draft'`, exemptions); remaining != 1 {
		t.Fatalf("sanctioned+rogue line: remaining = %d, want 1 (rogue requirement predicate hidden by line-level exemption)", remaining)
	}
	// A fragment that does not appear on the line sanctions nothing.
	if remaining, _ := vocablint.OccurrenceSanction(`WHERE r.status = 'draft'`, `'draft'`, exemptions); remaining != 1 {
		t.Fatalf("rogue-only line: remaining = %d, want 1", remaining)
	}
}

// TestOccurrenceSanctionOverlapPositionCoverage locks the position-coverage
// semantics against the counting+clamp failure class (review Important #1): when
// two exemption fragments overlap on the SAME sanctioned occurrence (one a
// sub-string of the other), a rogue occurrence of the same value elsewhere on the
// line must still be flagged -- the overlap must not double-count the sanctioned
// occurrence and clamp the rogue away. It also locks the reverse: a fragment that
// legitimately appears twice on a line sanctions both occurrences (no false
// positive). Both assertions FAIL under the old sum-and-clamp arithmetic.
func TestOccurrenceSanctionOverlapPositionCoverage(t *testing.T) {
	// Overlapping fragments (cv.status = 'draft' is a sub-string of
	// WHERE cv.status = 'draft'); both cover only the first 'draft'. The rogue
	// r.status = 'draft' must remain flagged.
	overlap := []vocablint.Exemption{
		{Fragment: `cv.status = 'draft'`, Reason: "design-own case status predicate"},
		{Fragment: `WHERE cv.status = 'draft'`, Reason: "super-string fragment overlapping the first"},
	}
	rogue := `WHERE cv.status = 'draft' AND r.status = 'draft'`
	if remaining, _ := vocablint.OccurrenceSanction(rogue, `'draft'`, overlap); remaining != 1 {
		t.Fatalf("overlapping fragments: remaining = %d, want 1 (rogue hidden by double-count + clamp)", remaining)
	}
	// The same sanctioned fragment appearing twice on a line sanctions both
	// occurrences: clean, not a false positive.
	repeated := []vocablint.Exemption{
		{Fragment: `cv.status = 'draft'`, Reason: "design-own case status predicate"},
	}
	twoSanctioned := `WHERE cv.status = 'draft' OR cv.status = 'draft'`
	if remaining, _ := vocablint.OccurrenceSanction(twoSanctioned, `'draft'`, repeated); remaining != 0 {
		t.Fatalf("repeated sanctioned fragment: remaining = %d, want 0 (false positive on a twice-sanctioned line)", remaining)
	}
	// One sanctioned occurrence plus one rogue, with the repeated fragment also
	// present: only the rogue remains.
	mixed := `WHERE cv.status = 'draft' OR cv.status = 'draft' AND r.status = 'draft'`
	if remaining, _ := vocablint.OccurrenceSanction(mixed, `'draft'`, repeated); remaining != 1 {
		t.Fatalf("repeated sanctioned + rogue: remaining = %d, want 1", remaining)
	}
}

// TestOccurrenceSanctionWorkQueueAndWaveShapes keeps the agent work-queue and
// execution wave/case_run forms of the same proof (they used the running value
// and a label-shaped rogue that shares ONLY the guarded token).
func TestOccurrenceSanctionWorkQueueAndWaveShapes(t *testing.T) {
	exemptions := []vocablint.Exemption{
		{Fragment: `WHERE status = 'running'`, Reason: "sanctioned work-item / wave predicate"},
	}
	if remaining, _ := vocablint.OccurrenceSanction(`WHERE status = 'running'`, `'running'`, exemptions); remaining != 0 {
		t.Fatalf("sanctioned-only line: remaining = %d, want 0", remaining)
	}
	for _, rogue := range []string{
		`WHERE status = 'running' AND attempt.status = 'running'`,
		`WHERE status = 'running' AND run.status = 'running'`,
	} {
		if remaining, _ := vocablint.OccurrenceSanction(rogue, `'running'`, exemptions); remaining != 1 {
			t.Fatalf("sanctioned+rogue line %q: remaining = %d, want 1 (rogue hidden by line-level exemption)", rogue, remaining)
		}
	}
	for _, rogueOnly := range []string{`WHERE attempt.status = 'running'`, `WHERE run.status = 'running'`} {
		if remaining, _ := vocablint.OccurrenceSanction(rogueOnly, `'running'`, exemptions); remaining != 1 {
			t.Fatalf("rogue-only line %q: remaining = %d, want 1", rogueOnly, remaining)
		}
	}
	overlap := []vocablint.Exemption{
		{Fragment: `status = 'running'`, Reason: "sub-string fragment"},
		{Fragment: `WHERE status = 'running'`, Reason: "super-string fragment overlapping the first"},
	}
	// The rogue occurrence must NOT contain the sub-fragment itself (a fragment
	// that genuinely matches the rogue sanctions it by design); label = 'running'
	// shares only the guarded token.
	overlapRogue := `WHERE status = 'running' AND label = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(overlapRogue, `'running'`, overlap); remaining != 1 {
		t.Fatalf("overlapping fragments: remaining = %d, want 1 (rogue hidden by double-count + clamp)", remaining)
	}
	repeatedEx := []vocablint.Exemption{
		{Fragment: `status = 'running'`, Reason: "sanctioned work-item / wave predicate"},
	}
	repeated := `WHERE status = 'running' OR status = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(repeated, `'running'`, repeatedEx); remaining != 0 {
		t.Fatalf("repeated sanctioned fragment: remaining = %d, want 0 (false positive)", remaining)
	}
}

// TestOccurrenceSanctionAuthorityDefinitionShape keeps the execution version-status
// gate's proof: an authority DEFINITION line is sanctioned, a rogue occurrence
// appended to that same line is not.
func TestOccurrenceSanctionAuthorityDefinitionShape(t *testing.T) {
	exemptions := []vocablint.Exemption{
		{Fragment: `WaveRunning WaveStatus = "running"`, Reason: "wave-status authority definition (execution/domain/wave.go), not a version status"},
	}
	definition := `WaveRunning WaveStatus = "running"`
	if remaining, _ := vocablint.OccurrenceSanction(definition, `"running"`, exemptions); remaining != 0 {
		t.Fatalf("sanctioned definition line: remaining = %d, want 0", remaining)
	}
	rogue := `WaveRunning WaveStatus = "running"; var rogue = "running"`
	if remaining, _ := vocablint.OccurrenceSanction(rogue, `"running"`, exemptions); remaining != 1 {
		t.Fatalf("definition+rogue line: remaining = %d, want 1 (rogue hidden by line-level exemption)", remaining)
	}
	if remaining, _ := vocablint.OccurrenceSanction(`var rogue = "running"`, `"running"`, exemptions); remaining != 1 {
		t.Fatalf("rogue-only line: remaining = %d, want 1", remaining)
	}
}

// TestOccurrenceSanctionHitsAreIndexKeyed is the T4 minor③ property: two
// exemptions carrying the SAME fragment text must not share a hit count, so a
// stale duplicate is still reported. It also pins that a fragment which sanctions
// nothing produces no hit, and that an empty fragment is ignored.
func TestOccurrenceSanctionHitsAreIndexKeyed(t *testing.T) {
	line := `WHERE cv.status = 'draft'`
	exemptions := []vocablint.Exemption{
		{Fragment: `cv.status = 'draft'`, Reason: "first adjudication"},
		{Fragment: `r.status = 'draft'`, Reason: "a fragment absent from this line"},
		{Fragment: `cv.status = 'draft'`, Reason: "duplicate text, separate adjudication"},
		{Fragment: `   `, Reason: "whitespace-only fragment must be ignored"},
	}
	remaining, hits := vocablint.OccurrenceSanction(line, `'draft'`, exemptions)
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0", remaining)
	}
	if len(hits) != 2 || hits[0] != 0 || hits[1] != 2 {
		t.Fatalf("hits = %v, want [0 2] (index-keyed: the absent fragment #1 and the blank #3 sanction nothing)", hits)
	}
	// A line with no occurrence of the token sanctions nothing at all.
	if remaining, hits := vocablint.OccurrenceSanction(`WHERE x = 1`, `'draft'`, exemptions); remaining != 0 || hits != nil {
		t.Fatalf("token absent: remaining=%d hits=%v, want 0/nil", remaining, hits)
	}
}

// TestOccurrenceSanctionNormalizesWhitespace pins that gofmt alignment of an
// authority definition does not change what a fragment sanctions.
func TestOccurrenceSanctionNormalizesWhitespace(t *testing.T) {
	exemptions := []vocablint.Exemption{
		{Fragment: `WaveRunning WaveStatus = "running"`, Reason: "authority definition"},
	}
	aligned := "WaveRunning\tWaveStatus   =  \"running\""
	if remaining, _ := vocablint.OccurrenceSanction(aligned, `"running"`, exemptions); remaining != 0 {
		t.Fatalf("gofmt-aligned definition: remaining = %d, want 0", remaining)
	}
}

func TestAllIndicesFindsOverlappingMatches(t *testing.T) {
	if got := vocablint.AllIndices("aaaa", "aa"); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("overlapping matches = %v, want [0 1 2]", got)
	}
	if got := vocablint.AllIndices("abc", ""); got != nil {
		t.Fatalf("empty sub = %v, want nil", got)
	}
	if got := vocablint.AllIndices("abc", "z"); got != nil {
		t.Fatalf("absent sub = %v, want nil", got)
	}
}

func TestExemptionsPreservesFragmentOrder(t *testing.T) {
	fragments := []string{`"summary", "text"`, `Kind: "question"`}
	got := vocablint.Exemptions(fragments, "one reason")
	if len(got) != 2 {
		t.Fatalf("exemptions = %+v, want 2", got)
	}
	for i, ex := range got {
		if ex.Fragment != fragments[i] || ex.Reason != "one reason" {
			t.Fatalf("exemption #%d = %+v, want fragment %q with the shared reason", i, ex, fragments[i])
		}
	}
	if len(vocablint.Exemptions(nil, "")) != 0 {
		t.Fatal("no fragments must produce no exemptions")
	}
}

// TestExemptionShapeAndStaleProblems pins the two registry rules the gates apply
// to an index-keyed exemption table: an empty fragment/reason is rejected, and an
// exemption that sanctioned nothing is stale (lockstep removal).
func TestExemptionShapeAndStaleProblems(t *testing.T) {
	exemptions := []vocablint.Exemption{
		{Fragment: `cv.status = 'draft'`, Reason: "adjudicated"},
		{Fragment: "", Reason: "no fragment"},
		{Fragment: `r.status = 'draft'`, Reason: "   "},
	}
	problems := vocablint.ExemptionShapeProblems(exemptions)
	if len(problems) != 2 || !strings.Contains(problems[0], "#1") || !strings.Contains(problems[1], "#2") {
		t.Fatalf("shape problems = %v, want the two ill-formed entries by index", problems)
	}
	hits := map[int]int{0: 1}
	stale := vocablint.StaleExemptionProblems(exemptions, hits)
	if len(stale) != 2 {
		t.Fatalf("stale problems = %v, want the two entries that sanctioned nothing", stale)
	}
	if !strings.Contains(stale[0], "sanctioned nothing") {
		t.Fatalf("stale problem wording = %q", stale[0])
	}
	if len(vocablint.StaleExemptionProblems(exemptions, map[int]int{0: 1, 1: 1, 2: 1})) != 0 {
		t.Fatal("a fully used exemption table must report no stale entry")
	}
}
