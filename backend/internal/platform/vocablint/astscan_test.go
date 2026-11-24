package vocablint_test

// Elision-aware const walk and AST site collector properties, migrated from the
// three gate-local type-elided proofs (design H-F2 RequirementStatus, agent
// WorkItemStatus/AgentRunStatus, execution WaveStatus) and from the struct-tag /
// SQL-multi-line handling the four AST scanners shared. No assertion is weakened.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

func parseSynthetic(t *testing.T, src string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	return file, fset
}

// typeValues reproduces the type-filtered derivation the design/execution gates
// perform over ConstSpecs (iterate the effective expression list).
func typeValues(t *testing.T, file *ast.File, typeName string) []string {
	t.Helper()
	var values []string
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		if spec.Type != typeName {
			continue
		}
		for _, expr := range spec.Exprs {
			text, isString, err := vocablint.StringValue(expr)
			if err != nil {
				t.Fatalf("unquote %s literal: %v (derivation broken; the gate would go vacuous)", typeName, err)
			}
			if isString {
				values = append(values, text)
			}
		}
	}
	return values
}

func assertValues(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("type-elided derivation = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("type-elided derivation = %v, want %v", got, want)
		}
	}
}

// TestConstSpecsHandlesTypeElidedConst is permanent deterministic proof of H-F2
// for all three gate shapes: a status const written with an elided (inherited)
// type, and one with an elided value, still join the derived guarded set.
func TestConstSpecsHandlesTypeElidedConst(t *testing.T) {
	cases := []struct {
		typeName string
		src      string
		want     []string
	}{
		{"RequirementStatus", `package p
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
`, []string{"draft", "clarified", "closed", "obsolete", "obsolete"}},
		{"WorkItemStatus", `package p
type WorkItemStatus string
const (
	WorkItemOpen    WorkItemStatus = "open"
	WorkItemClaimed                = "claimed"
	WorkItemRunning                = "running"
)
const (
	WorkItemDone WorkItemStatus = "done"
	WorkItemFailed
)
`, []string{"open", "claimed", "running", "done", "done"}},
		{"WaveStatus", `package p
type WaveStatus string
const (
	WavePlanned   WaveStatus = "planned"
	WaveRunning              = "running"
	WavePaused               = "paused"
)
const (
	WaveDone WaveStatus = "done"
	WaveFailed
)
`, []string{"planned", "running", "paused", "done", "done"}},
	}
	for _, c := range cases {
		file, _ := parseSynthetic(t, c.src)
		assertValues(t, typeValues(t, file, c.typeName), c.want)
	}
}

// TestConstSpecsNonLiteralIsVisibleNotSkipped is the agent T3 minor④ property: a
// guarded-type constant whose value is an alias/conversion must be VISIBLE to the
// caller (so it can fail loud or be classified), never silently dropped.
func TestConstSpecsNonLiteralIsVisibleNotSkipped(t *testing.T) {
	file, _ := parseSynthetic(t, `package p
type AgentRunStatus string
const (
	AgentRunRunning AgentRunStatus = "running"
	AgentRunAborted                = someAlias
)
`)
	var values []string
	nonLiteral := map[string]ast.Expr{}
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		if spec.Type != "AgentRunStatus" {
			continue
		}
		for i, name := range spec.Names {
			if i >= len(spec.Exprs) {
				continue
			}
			text, isString, err := vocablint.StringValue(spec.Exprs[i])
			if err != nil {
				t.Fatalf("unquote: %v", err)
			}
			if isString {
				values = append(values, text)
			} else {
				nonLiteral[name] = spec.Exprs[i]
			}
		}
	}
	assertValues(t, values, []string{"running"})
	expr, ok := nonLiteral["AgentRunAborted"]
	if !ok {
		t.Fatal("alias-defined AgentRunAborted was silently skipped; the gate must see it as non-literal")
	}
	if _, isString, _ := vocablint.StringValue(expr); isString {
		t.Fatal("an alias expression was reported as a string literal")
	}
	// A plain string literal never needs classification.
	if _, isString, err := vocablint.StringValue(&ast.BasicLit{Kind: token.STRING, Value: `"running"`}); !isString || err != nil {
		t.Fatalf("string-literal constant not recognised: isString=%v err=%v", isString, err)
	}
}

// TestConstSpecsNameFilterAndVarGroups pins the two remaining shapes the gates
// use: a NAME-PREFIX filter over untyped constants (design-own statuses, SUT
// registration statuses) and a var group (tok == token.VAR).
func TestConstSpecsNameFilterAndVarGroups(t *testing.T) {
	file, _ := parseSynthetic(t, `package p
const (
	CaseStatusDraft = "draft"
	CaseStatusConfirmed = "confirmed"
	Unrelated = "draft"
)
const (
	SUTStatusProposed = "proposed"
	SUTStatusApproved = "approved"
)
var (
	SetA = "a"
	SetB = "b"
)
`)
	var design []string
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		for i, name := range spec.Names {
			if !strings.HasPrefix(name, "CaseStatus") {
				continue
			}
			if i >= len(spec.Exprs) {
				continue
			}
			text, isString, err := vocablint.StringValue(spec.Exprs[i])
			if err != nil || !isString {
				t.Fatalf("design status literal: isString=%v err=%v", isString, err)
			}
			design = append(design, text)
		}
	}
	assertValues(t, design, []string{"draft", "confirmed"})

	var vars []string
	for _, spec := range vocablint.ConstSpecs(file, token.VAR) {
		for _, expr := range spec.Exprs {
			if text, isString, _ := vocablint.StringValue(expr); isString {
				vars = append(vars, text)
			}
		}
	}
	assertValues(t, vars, []string{"a", "b"})
	// A const walk must not see the var group, and vice versa: the var group has
	// exactly its own two specs, none of the three const specs above.
	if got := vocablint.ConstSpecs(file, token.VAR); len(got) != 2 {
		t.Fatalf("var group walk = %+v, want only its own 2 specs", got)
	}
}

// TestASTSitesStripsTagsAndCoversSQLLines pins the AST scanner's two collision
// rules and its multi-line SQL handling.
func TestASTSitesStripsTagsAndCoversSQLLines(t *testing.T) {
	src := "package p\n" +
		"type T struct{ F string `json:\"running\"` }\n" +
		"var goPredicate = \"running\"\n" +
		"var q = `SELECT 1\nFROM t\nWHERE status = 'running'\nAND x = 'running'`\n"
	file, fset := parseSynthetic(t, src)
	lines := vocablint.SourceLines(src)
	sites, problems := vocablint.ASTSites("synthetic.go", file, fset, lines, []string{"running"})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	seen := map[vocablint.Site]bool{}
	for _, s := range sites {
		if seen[s] {
			t.Fatalf("duplicate site %+v; occurrence counting would double-count", s)
		}
		seen[s] = true
	}
	// The struct tag on line 2 must NOT be a site.
	if seen[vocablint.Site{Line: 1, Token: `"running"`}] {
		t.Fatal("a struct-tag literal was scanned as a status predicate")
	}
	// The Go predicate on line 3 is a site.
	if !seen[vocablint.Site{Line: 2, Token: `"running"`}] {
		t.Fatalf("Go predicate literal missing: %+v", sites)
	}
	// Both SQL carrying lines (6 and 7) are sites, not the query's opening line 4.
	for _, line := range []int{5, 6} {
		if !seen[vocablint.Site{Line: line, Token: `'running'`}] {
			t.Fatalf("SQL carrying line %d missing: %+v", line+1, sites)
		}
	}
	if seen[vocablint.Site{Line: 3, Token: `'running'`}] {
		t.Fatal("the query's opening line was scanned instead of the predicate line")
	}
	if len(sites) != 3 {
		t.Fatalf("sites = %+v, want exactly 3 (one Go predicate + two SQL lines)", sites)
	}
}

// TestASTSitesRepeatedLiteralOnOneLineIsOneSite pins the (line, token) uniqueness
// the four scanners relied on: occurrence-scoped counting already accounts for
// every occurrence on the line.
func TestASTSitesRepeatedLiteralOnOneLineIsOneSite(t *testing.T) {
	src := "package p\nvar a, b = \"running\", \"running\"\n"
	file, fset := parseSynthetic(t, src)
	sites, problems := vocablint.ASTSites("synthetic.go", file, fset, vocablint.SourceLines(src), []string{"running"})
	if len(problems) != 0 || len(sites) != 1 {
		t.Fatalf("sites = %+v problems = %v, want one unique (line, token) site", sites, problems)
	}
	// The sanction algorithm still sees BOTH occurrences on that line.
	if remaining, _ := vocablint.OccurrenceSanction(vocablint.SourceLines(src)[sites[0].Line], sites[0].Token, nil); remaining != 2 {
		t.Fatalf("remaining = %d, want 2 (both occurrences on the line are unsanctioned)", remaining)
	}
}

func TestSourceLinesNormalizesCRLF(t *testing.T) {
	got := vocablint.SourceLines("a\r\nb\r\nc")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("CRLF normalization = %q, want [a b c]", got)
	}
}

// TestASTSitesForeignFileSetFailsClosed is the review Important #1 regression:
// token.FileSet.Position returns the ZERO Position (Line 0) for a Pos its set
// never saw, so handing ASTSites a different FileSet than the one that parsed
// the file used to yield Site{Line:-1} — garbage that a len(sites)!=0 assertion
// happily accepted, and a lines[-1] panic waiting to happen on the SQL branch.
// Both shapes must now be REPORTED, never silently produced.
func TestASTSitesForeignFileSetFailsClosed(t *testing.T) {
	// Go-literal shape: the mis-wired position must not become a site.
	goSrc := "package p\nvar s = \"running\"\n"
	goFile, goFset := parseSynthetic(t, goSrc)
	goLines := vocablint.SourceLines(goSrc)
	sites, problems := vocablint.ASTSites("foreign.go", goFile, token.NewFileSet(), goLines, []string{"running"})
	if len(problems) == 0 {
		t.Fatal("a foreign FileSet produced no problem; the wiring error is silent")
	}
	for _, s := range sites {
		if s.Line < 0 || s.Line >= len(goLines) {
			t.Fatalf("garbage site %+v escaped the fail-closed guard", s)
		}
	}
	if len(sites) != 0 {
		t.Fatalf("a foreign FileSet still produced sites: %+v", sites)
	}
	// The SAME file with its OWN FileSet is clean and lands on the real line.
	sites, problems = vocablint.ASTSites("own.go", goFile, goFset, goLines, []string{"running"})
	if len(problems) != 0 || len(sites) != 1 || sites[0] != (vocablint.Site{Line: 1, Token: `"running"`}) {
		t.Fatalf("own FileSet: sites=%+v problems=%v, want exactly line 2", sites, problems)
	}

	// SQL shape: start would be -1 and lines[-1] would PANIC without the guard.
	sqlSrc := "package p\nvar q = `SELECT 1\nWHERE status = 'running'`\n"
	sqlFile, sqlFset := parseSynthetic(t, sqlSrc)
	sqlLines := vocablint.SourceLines(sqlSrc)
	sites, problems = vocablint.ASTSites("foreign-sql.go", sqlFile, token.NewFileSet(), sqlLines, []string{"running"})
	if len(problems) == 0 {
		t.Fatal("a foreign FileSet on the SQL branch produced no problem (it used to be a lines[-1] panic)")
	}
	if len(sites) != 0 {
		t.Fatalf("a foreign FileSet still produced SQL sites: %+v", sites)
	}
	sites, problems = vocablint.ASTSites("own-sql.go", sqlFile, sqlFset, sqlLines, []string{"running"})
	if len(problems) != 0 || len(sites) != 1 || sites[0] != (vocablint.Site{Line: 2, Token: `'running'`}) {
		t.Fatalf("own FileSet SQL: sites=%+v problems=%v, want exactly line 3", sites, problems)
	}
}
