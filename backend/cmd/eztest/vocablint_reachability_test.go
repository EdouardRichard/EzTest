package main

// Vocabulary-machinery reachability smoke test (v0.2.4-H T11e-0).
//
// The repository-level status-vocabulary gate (T11e-5) must live in the
// composition root, because only here can one gate see every context. Until
// T11e-0 that was physically impossible: the shared resolver lived at
// version/internal/govocab, and Go's internal-package rule rejects an import of
// it from cmd/eztest ("use of internal package ... not allowed", measured in
// task-11d-report.md §5). The scan/exemption machinery was worse — it lived
// inside _test.go files, which no package can import at all.
//
// This test is the permanent proof that the barrier is gone: it imports BOTH
// platform packages from the composition root and exercises them on real
// repository source. If either package is ever moved back under a context's
// internal/ directory (or back into a _test.go file), this file stops compiling
// and the repository-level gate is unbuildable again — which is exactly the
// regression it exists to catch.
//
// It is a REACHABILITY proof, not the repository-level guard itself: it asserts
// the machinery works from here, not that the whole repository is clean. The
// 331 literals enumerated in task-11d-report.md are repaid by T11e-1..4 and
// guarded by T11e-5.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestVocabularyMachineryIsReachableFromCompositionRoot derives a real domain
// vocabulary through platform/govocab + platform/vocablint and runs the shared
// scanner and sanction algorithm over it, all from cmd/eztest.
func TestVocabularyMachineryIsReachableFromCompositionRoot(t *testing.T) {
	// This package is backend/cmd/eztest, so the module root is two levels up.
	resolver, err := govocab.New("../..")
	if err != nil {
		t.Fatalf("govocab resolver from the composition root: %v", err)
	}
	derived := vocablint.DeriveVocabulary(resolver,
		[]string{"../../internal/version/domain/requirement.go"}, nil)
	if len(derived.Problems) != 0 {
		t.Fatalf("vocabulary derivation problems: %v", derived.Problems)
	}
	// The expectation comes from the AUTHORITY, not a hand-copied value table.
	for _, want := range []string{
		string(versiondomain.RequirementDraft), string(versiondomain.RequirementClarified),
		string(versiondomain.RequirementClosed), string(versiondomain.RequirementObsolete),
	} {
		if _, ok := derived.Values[want]; !ok {
			t.Fatalf("requirement status %q was not derived from the composition root: %v", want, derived.Values)
		}
	}

	// The scanner fires on a real hardcoded literal of a derived value...
	value := string(versiondomain.RequirementClosed)
	bad := "package x\nfunc f() { q := `SELECT 1 FROM requirement WHERE status = '" + value + "'` }\n"
	if got := vocablint.ScanLiterals("bad.go", bad, derived.Values, nil).Findings; len(got) == 0 {
		t.Fatal("the shared scanner did not fire from the composition root; the import would be vacuous")
	}
	// ...and a fragment-scoped, file-bound exemption sanctions only its own
	// occurrence in its own file (T11e-5 Part A1).
	allow := map[string]vocablint.AllowEntry{
		value: {Reason: "smoke-test adjudication", Fragments: vocablint.FileFragments("r.go", `status = '`+value+`'`)},
	}
	rogue := "package x\nvar q = `WHERE status = '" + value + "' OR label = '" + value + "'`\n"
	if got := vocablint.ScanLiterals("r.go", rogue, derived.Values, allow).Findings; len(got) != 1 {
		t.Fatalf("occurrence-scoped sanction from the composition root: findings = %+v, want the rogue only", got)
	}

	// The elision-aware const walk and the AST site collector are reachable too.
	// The FileSet that parses the file MUST be the one handed to ASTSites: a
	// foreign/empty FileSet makes every Position the zero value (Line 0), which
	// would turn the site assertion below into garbage (review Important #1).
	path := "../../internal/version/domain/requirement.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse requirement authority: %v", err)
	}
	found := 0
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		if spec.Type != "RequirementStatus" {
			continue
		}
		for _, expr := range spec.Exprs {
			if _, isString, err := vocablint.StringValue(expr); err == nil && isString {
				found++
			}
		}
	}
	if found == 0 {
		t.Fatal("the shared const walk derived no RequirementStatus value from the composition root")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read requirement authority: %v", err)
	}
	lines := vocablint.SourceLines(string(src))
	sites, problems := vocablint.ASTSites(path, file, fset, lines, []string{value})
	if len(problems) != 0 {
		t.Fatalf("AST site collection problems: %v", problems)
	}
	if len(sites) == 0 {
		t.Fatal("the shared AST site collector found no authority definition site from the composition root")
	}
	// The expected line is derived from the SOURCE TEXT, independently of any
	// FileSet, so a mis-wired fset cannot satisfy this assertion with Line 0/-1.
	wantLine := -1
	for i, line := range lines {
		if strings.Contains(line, "RequirementClosed") && strings.Contains(line, `"`+value+`"`) {
			wantLine = i
			break
		}
	}
	if wantLine < 0 {
		t.Fatalf("could not locate the RequirementClosed authority definition line in %s; the expectation itself is broken", path)
	}
	want := vocablint.Site{Line: wantLine, Token: `"` + value + `"`}
	hit := false
	for _, s := range sites {
		if s == want {
			hit = true
		}
		if s.Line < 0 || s.Line >= len(lines) {
			t.Fatalf("site %+v has a line outside the file (%d lines): the FileSet is mis-wired", s, len(lines))
		}
	}
	if !hit {
		t.Fatalf("no site at the authority definition %+v (source line %d); got %+v", want, wantLine+1, sites)
	}
}

// TestRepositoryScanSurfaceIsDerivableFromCompositionRoot proves the other half
// of what T11e-5 needs: a repository-wide guarded-file surface can be DERIVED
// from here (no hand-written file list), spanning every bounded context.
func TestRepositoryScanSurfaceIsDerivableFromCompositionRoot(t *testing.T) {
	files, err := vocablint.GoFilesUnder(filepath.Join("..", "..", "internal"))
	if err != nil {
		t.Fatalf("derive repository scan surface: %v", err)
	}
	// Anti-vacuity floor: the repository has 20 bounded contexts; a surface this
	// small means the derivation broke, not that the repository shrank.
	if problem := vocablint.CountFloorProblem("repository Go files", len(files), 500); problem != "" {
		t.Fatalf("%s", problem)
	}
	contexts := map[string]bool{}
	for _, path := range files {
		rel, relErr := filepath.Rel(filepath.Join("..", "..", "internal"), path)
		if relErr != nil {
			t.Fatalf("relative path for %s: %v", path, relErr)
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) > 1 {
			contexts[parts[0]] = true
		}
		if strings.HasSuffix(path, "_test.go") {
			t.Fatalf("test file in the derived production surface: %s", path)
		}
	}
	if problem := vocablint.CountFloorProblem("bounded contexts in the scan surface", len(contexts), 20); problem != "" {
		t.Fatalf("%s", problem)
	}
}
