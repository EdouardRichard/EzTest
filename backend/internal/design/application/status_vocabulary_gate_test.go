package designapp_test

// V-0014 / V-0030 (E share + H-A9) vocabulary gate: the design context must read
// every status vocabulary through its domain constants (single source,
// constitution 五.1/五.4), never through hand-written literals -- neither Go
// comparison literals nor SQL-inline quoted literals. B's downstream invariant
// (19 号 §4 invariant 1) forbids SQL-inline status predicates even when they are
// equal today: a constant rename or a new status value would leave the SQL and
// its literal-seeded tests green while the results silently diverge.
//
// The guarded value set is DERIVED, not hand-copied (方法纪律 ④/⑤/⑧):
//   - version statuses come from version.Statuses() (the V-0017 authority);
//   - requirement statuses are parsed from the RequirementStatus constant block
//     in version/domain/requirement.go through platform/vocablint's elision-aware
//     const walk, honoring type-elided and value-elided const specs (H-F2) so a
//     NEW requirement status constant joins the guarded set automatically even
//     when its type is inherited;
//   - design-own statuses (CaseStatus*/ScopeStatus*/RoundStatus*/DesignStatus*)
//     are parsed from design/domain by NAME PREFIX (they are untyped string
//     constants), so a new design status constant joins the set automatically.
//
// A literal is flagged when it equals a guarded value exactly (Go predicate
// literal) or contains the SQL single-quoted form 'value' (SQL-inline
// predicate). Struct tags are stripped mechanically (json:"draft" is a wire
// name, not a status predicate).
//
// Exemptions are OCCURRENCE-scoped with a reason (方法纪律 ⑥, H-F1): a fragment
// sanctions only the guarded-token occurrences it actually carries, so a rogue
// predicate sharing the same value on the same line (sanctioned + rogue) is
// still flagged -- it cannot hide behind a value-level or line-level allowance.
// Every exemption must sanction at least one real occurrence (non-vacuous):
// when a debt is repaid the stale exemption fails the gate and must be removed
// in the same change set (lockstep).
//
// T11e-0: the ALGORITHM (occurrence sanction, AST site collection, elision-aware
// const walk, scan-surface derivation, anti-vacuity floors) now lives in
// platform/vocablint as the single authority every context's gate calls; this
// file keeps design's SCOPE and its adjudicated exemptions. The gate-local
// copies of occurrenceSanction/allIndices and of the two AST value walks are
// gone (constitution 五.1/五.3 存量违例清偿).
//
// Mutation evidence: this gate was written BEFORE the T20 literal->constant
// repayment and failed on the then-live literals; H-A9 extended it to the
// design-own case/scope/round/graph vocabulary and failed on the 31 live SQL
// literals + 2 graph projection literals before they were parameterized.
// Re-injecting any literal reproduces the failure (see the occurrence-scoped and
// type-elided unit tests below for permanent deterministic proof).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// After H-A9 every design-own case/scope/round status SQL mirror is parameterized
// (domain constant bound as a query argument) and the graph projection reads the
// DesignStatus* constants, so the only remaining sanctioned occurrences are:
//   - the authority DEFINITIONS themselves (each spells its value once);
//   - non-status vocabulary that happens to share a guarded value (the review
//     state "pending" and the task-creation reason label "pending").
//
// The SQL-mirror fragment exemptions that used to live here were removed in the
// same change set as the parameterization (the non-vacuous check below would fail
// on a stale exemption, forcing lockstep).
//
// Each fragment MUST be the exact text of ONE source line: the sanction
// algorithm evaluates one whitespace-normalized line at a time (T2 minor③).
var statusVocabularyExemptions = []vocablint.Exemption{
	{Fragment: `CaseStatusDraft = "draft"`, Reason: "design case-status authority definition (design/domain/case.go)"},
	{Fragment: `CaseStatusConfirmed = "confirmed"`, Reason: "design case-status authority definition (design/domain/case.go)"},
	{Fragment: `CaseStatusStale = "stale"`, Reason: "design case-status authority definition (design/domain/case.go)"},
	{Fragment: `CaseStatusSuperseded = "superseded"`, Reason: "design case-status authority definition (design/domain/case.go)"},
	{Fragment: `RoundStatusActive = "active"`, Reason: "design round-status authority definition (design/domain/round.go)"},
	{Fragment: `RoundStatusCompleted = "completed"`, Reason: "design round-status authority definition (design/domain/round.go)"},
	{Fragment: `ScopeStatusActive = "active"`, Reason: "design scope-status authority definition (design/domain/round.go)"},
	{Fragment: `ScopeStatusClosed = "closed"`, Reason: "design scope-status authority definition (design/domain/round.go)"},
	{Fragment: `DesignStatusPending = "pending"`, Reason: "design graph-projection authority definition (design/domain/repository.go), H-A9"},
	{Fragment: `DesignStatusInDesign = "in_design"`, Reason: "design graph-projection authority definition (design/domain/repository.go), H-A9"},
	{Fragment: `ReviewStatePending ReviewState = "pending"`, Reason: "design review-state authority definition (design/domain/judgment.go); shares the value pending but is not a case/scope/round/graph status"},
	{Fragment: `ensureRequirementTasks(ctx, versionID, pending, "pending", conversationID, round.ID)`, Reason: "design task-creation reason label (application/planner.go); a reason string, not a status predicate"},
	{Fragment: `ensureRequirementTasks(ctx, versionID, pending, "pending", latestConversation, round.ID)`, Reason: "design task-creation reason label (application/reconciler.go); a reason string, not a status predicate"},
}

func TestRequirementStatusReadsUseDomainConstant(t *testing.T) {
	root := statusGateRepoRoot(t)
	values := guardedStatusValues(t, root)
	if len(values) < 13 {
		t.Fatalf("guarded value set suspiciously small: %v (derivation broken?)", values)
	}

	type finding struct{ site, line, value string }
	var findings []finding
	exemptionHits := make(map[int]int)

	designDir := filepath.Join(root, "backend", "internal", "design")
	files, err := vocablint.GoFilesUnder(designDir)
	if err != nil {
		t.Fatalf("scan design context: %v", err)
	}
	for _, path := range files {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("relative path for %s: %v", path, relErr)
		}
		rel = filepath.ToSlash(rel)
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", rel, readErr)
		}
		lines := vocablint.SourceLines(string(src))
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", rel, parseErr)
		}
		sites, problems := vocablint.ASTSites(rel, file, fset, lines, values)
		for _, problem := range problems {
			t.Fatalf("%s", problem)
		}
		for _, s := range sites {
			remaining, hits := vocablint.OccurrenceSanction(lines[s.Line], s.Token, statusVocabularyExemptions)
			for _, h := range hits {
				exemptionHits[h]++
			}
			if remaining > 0 {
				findings = append(findings, finding{
					site:  rel + ":" + strconv.Itoa(s.Line+1),
					line:  strings.TrimSpace(lines[s.Line]),
					value: s.Token,
				})
			}
		}
	}

	for _, f := range findings {
		t.Errorf("V-0014/V-0030: design reads status value %s as a hand-written literal at %s: %s -- use the domain constant (Go predicate) or pass it as a query parameter (SQL)", f.value, f.site, f.line)
	}
	// Non-vacuous, well-formed exemptions: a stale exemption means the exempted
	// occurrence was repaid (or moved) and the registry must be updated in the
	// same change set. Hits are keyed by exemption INDEX, so two exemptions with
	// the same fragment text never share a count (T4 minor③ shape).
	for _, problem := range vocablint.ExemptionShapeProblems(statusVocabularyExemptions) {
		t.Errorf("V-0014/V-0030: %s", problem)
	}
	for _, problem := range vocablint.StaleExemptionProblems(statusVocabularyExemptions, exemptionHits) {
		t.Errorf("V-0014/V-0030: %s", problem)
	}
}

// guardedStatusValues derives the version + requirement + design-own status value
// set from the domain authorities, deduplicated. Version statuses come from
// Statuses(); requirement statuses from the RequirementStatus const block
// (type/value-elided aware, H-F2); design statuses from the design/domain const
// blocks by name prefix. A new constant in any authority joins the set
// automatically, so the gate cannot go vacuous when the vocabulary grows.
func guardedStatusValues(t *testing.T, root string) []string {
	t.Helper()
	set := map[string]bool{}
	var values []string
	add := func(v string) {
		if !set[v] {
			set[v] = true
			values = append(values, v)
		}
	}
	for _, v := range version.Statuses() {
		add(v)
	}

	reqPath := filepath.Join(root, "backend", "internal", "version", "domain", "requirement.go")
	reqFile, err := parser.ParseFile(token.NewFileSet(), reqPath, nil, 0)
	if err != nil {
		t.Fatalf("parse requirement status authority: %v", err)
	}
	reqValues := deriveRequirementStatuses(t, reqFile)
	if len(reqValues) == 0 {
		t.Fatal("no RequirementStatus constants found in version/domain/requirement.go; the derivation is broken and the gate would go vacuous")
	}
	for _, v := range reqValues {
		add(v)
	}

	designDir := filepath.Join(root, "backend", "internal", "design", "domain")
	entries, err := os.ReadDir(designDir)
	if err != nil {
		t.Fatalf("read design domain dir: %v", err)
	}
	designFound := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(designDir, e.Name())
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse design domain %s: %v", e.Name(), parseErr)
		}
		for _, v := range deriveDesignStatuses(t, file) {
			add(v)
			designFound++
		}
	}
	// 4 case + 2 round + 2 scope + 2 graph projection = 10 design status constants.
	if designFound < 10 {
		t.Fatalf("only %d design status constants derived (want >= 10); the derivation is broken and the gate would go vacuous", designFound)
	}
	return values
}

// deriveRequirementStatuses reads the RequirementStatus const block through the
// shared elision-aware walk: a spec with no type inherits the previous spec's type
// within the same const group, and a spec with no values repeats the previous
// expression list. H-F2: a requirement status written with an elided type must
// still join the guarded set.
func deriveRequirementStatuses(t *testing.T, file *ast.File) []string {
	t.Helper()
	var values []string
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		if spec.Type != "RequirementStatus" {
			continue
		}
		for _, expr := range spec.Exprs {
			text, isString, err := vocablint.StringValue(expr)
			if err != nil {
				t.Fatalf("unquote RequirementStatus literal: %v (derivation broken; the gate would go vacuous)", err)
			}
			if isString {
				values = append(values, text)
			}
		}
	}
	return values
}

// deriveDesignStatuses collects the string values of design-own status constants
// by NAME PREFIX (CaseStatus/ScopeStatus/RoundStatus/DesignStatus). These are
// untyped string constants, so the type cannot be used as the filter; the name
// prefix is the authority marker. Value-elided specs inherit the previous value
// through the same shared walk.
func deriveDesignStatuses(t *testing.T, file *ast.File) []string {
	t.Helper()
	var values []string
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		for i, name := range spec.Names {
			if !isDesignStatusName(name) {
				continue
			}
			if i >= len(spec.Exprs) {
				continue
			}
			text, isString, err := vocablint.StringValue(spec.Exprs[i])
			if err != nil {
				t.Fatalf("unquote design status literal (%s): %v (derivation broken; the gate would go vacuous)", name, err)
			}
			if isString {
				values = append(values, text)
			}
		}
	}
	return values
}

func isDesignStatusName(name string) bool {
	for _, prefix := range []string{"CaseStatus", "ScopeStatus", "RoundStatus", "DesignStatus"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// TestStatusVocabularyExemptionIsOccurrenceScoped is permanent deterministic
// proof of H-F1: a line carrying a sanctioned fragment AND a rogue occurrence of
// the same guarded value flags the rogue (only the sanctioned occurrence is
// exempted), while a line carrying only the sanctioned fragment is clean.
func TestStatusVocabularyExemptionIsOccurrenceScoped(t *testing.T) {
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

// TestStatusVocabularyExemptionOverlapPositionCoverage locks the position-coverage
// semantics against the counting+clamp failure class (review Important #1): when
// two exemption fragments overlap on the SAME sanctioned occurrence (one a
// sub-string of the other), a rogue occurrence of the same value elsewhere on the
// line must still be flagged -- the overlap must not double-count the sanctioned
// occurrence and clamp the rogue away. It also locks the reverse: a fragment that
// legitimately appears twice on a line sanctions both occurrences (no false
// positive). Both assertions FAIL under the old sum-and-clamp arithmetic.
func TestStatusVocabularyExemptionOverlapPositionCoverage(t *testing.T) {
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

// TestStatusVocabularyDerivationHandlesTypeElidedConst is permanent deterministic
// proof of H-F2: a requirement status const written with an elided (inherited)
// type, and one with an elided value, still join the derived guarded set.
func TestStatusVocabularyDerivationHandlesTypeElidedConst(t *testing.T) {
	src := `package p
type RequirementStatus string
const (
	RequirementDraft     RequirementStatus = "draft"
	RequirementClarified                   = "clarified"
	RequirementClosed                      = "closed"
)
const (
	RequirementObsolete RequirementStatus = "obsolete"
	RequirementArchived
)
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	got := deriveRequirementStatuses(t, file)
	want := []string{"draft", "clarified", "closed", "obsolete", "obsolete"}
	if len(got) != len(want) {
		t.Fatalf("type-elided derivation = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("type-elided derivation = %v, want %v", got, want)
		}
	}
}

// statusGateRepoRoot walks up from this test's source file to the repository
// root (the directory containing backend/go.mod), so the gate works regardless
// of the test's working directory.
func statusGateRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate status vocabulary gate source")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("repo root containing backend/go.mod not found from " + file)
	return ""
}
