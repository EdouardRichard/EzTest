package version_test

// V-0017 enum authority ROOT guard (Task 28). The const blocks in stage.go and
// version.go are the single authority for the stage/status enums; Stages() and
// Statuses() are the derived lists every other copy (DB CHECK, frontend union,
// OpenAPI) is validated against. Before this guard, Stages() re-listed the const
// block BY HAND ~90 lines below it, and enum_authority_test.go compared that
// hand-copy to yet another hand-copy — so adding a stage constant without updating
// Stages() fired nothing at build or test time (the frozen DB CHECK then rejected
// the first write at runtime). This test parses the const blocks with go/ast and
// asserts SET EQUALITY with the names Stages()/Statuses() actually reference, so a
// constant added to (or removed from) either side fails immediately. It reuses the
// go/ast + go/constant derivation shape of plan_item_test.go.

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"sort"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// parseDomainSource parses a domain source file relative to this package dir
// (internal/version/domain).
func parseDomainSource(t *testing.T, name string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

// enumConstants returns every constant in the file's const blocks mapped name ->
// string value. When typeName is non-empty only constants of that declared type
// are collected (the Stage enum); when empty, every string-literal constant is
// collected (the untyped status strings). The declared type is tracked across
// specs so an inherited type (type stated only on the first spec) is honoured.
func enumConstants(t *testing.T, f *ast.File, typeName string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		var currentType ast.Expr
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				currentType = vs.Type
			}
			if typeName != "" {
				ident, ok := currentType.(*ast.Ident)
				if !ok || ident.Name != typeName {
					continue
				}
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				cv := constant.MakeFromLiteral(lit.Value, lit.Kind, 0)
				if cv.Kind() != constant.String {
					continue
				}
				out[name.Name] = constant.StringVal(cv)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("derived no %q constants from %s; parsing is broken", typeName, f.Name.Name)
	}
	return out
}

// enumListedNames returns the set of constant identifiers referenced in the return
// composite literal of the named list function (Stages / Statuses). It reads the
// actual source, so it sees exactly what the hand-written list enumerates.
func enumListedNames(t *testing.T, f *ast.File, funcName string) map[string]bool {
	t.Helper()
	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == funcName {
			fn = fd
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatalf("function %s not found in %s", funcName, f.Name.Name)
	}
	out := make(map[string]bool)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, res := range ret.Results {
			comp, ok := res.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range comp.Elts {
				if ident, ok := elt.(*ast.Ident); ok {
					out[ident.Name] = true
				}
			}
		}
		return true
	})
	if len(out) == 0 {
		t.Fatalf("%s lists no constant identifiers; parsing is broken", funcName)
	}
	return out
}

// missingFrom reports the names present in a but absent from b, sorted. The set
// difference is the whole guard: a non-empty result on either side is drift.
func missingFrom(a map[string]bool, b map[string]bool) []string {
	var out []string
	for name := range a {
		if !b[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func nameSet(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for name := range m {
		out[name] = true
	}
	return out
}

// assertEnumRootDerived pins that the const block (authority root) and the list
// function (derived copy) enumerate exactly the same constant NAMES, and that the
// runtime list values equal the const-block values. Name-set equality catches a
// constant added to the block but forgotten in the list (the V-0017 mutation) even
// if two constants ever shared a value; the value cross-check ties the AST to the
// actual runtime authority.
func assertEnumRootDerived(t *testing.T, file, typeName, funcName string, runtimeValues []string) {
	t.Helper()
	f := parseDomainSource(t, file)
	defined := enumConstants(t, f, typeName)
	listed := enumListedNames(t, f, funcName)

	for _, name := range missingFrom(nameSet(defined), listed) {
		t.Errorf("%s: constant %s (%q) is defined in the const block but %s() does not list it", file, name, defined[name], funcName)
	}
	for _, name := range missingFrom(listed, nameSet(defined)) {
		t.Errorf("%s: %s() lists %s which is not a defined constant in the const block", file, funcName, name)
	}

	// Runtime cross-check: the list function's values are exactly the const values.
	wantValues := make(map[string]bool, len(defined))
	for _, v := range defined {
		wantValues[v] = true
	}
	gotValues := make(map[string]bool, len(runtimeValues))
	for _, v := range runtimeValues {
		gotValues[v] = true
	}
	for _, v := range missingFrom(wantValues, gotValues) {
		t.Errorf("%s: const value %q is not returned by %s()", file, v, funcName)
	}
	for _, v := range missingFrom(gotValues, wantValues) {
		t.Errorf("%s: %s() returns %q which is not a defined const value", file, funcName, v)
	}
	if len(runtimeValues) != len(defined) {
		t.Errorf("%s: %s() returns %d values, const block defines %d", file, funcName, len(runtimeValues), len(defined))
	}
}

func TestStageEnumRootIsDerived(t *testing.T) {
	stages := version.Stages()
	values := make([]string, 0, len(stages))
	for _, s := range stages {
		values = append(values, string(s))
	}
	assertEnumRootDerived(t, "stage.go", "Stage", "Stages", values)
}

func TestStatusEnumRootIsDerived(t *testing.T) {
	assertEnumRootDerived(t, "version.go", "", "Statuses", version.Statuses())
}

// TestEnumRootGuardFires proves the set-difference guard in its strong form: a
// constant added to the authority root but absent from the derived list (the exact
// V-0017 mutation) is reported, and so is the reverse. A closed hand-copy compared
// to another hand-copy could never see an added constant; this can.
func TestEnumRootGuardFires(t *testing.T) {
	defined := map[string]bool{"StageCreated": true, "StageArchived": true}
	listed := map[string]bool{"StageCreated": true}
	if got := missingFrom(defined, listed); len(got) != 1 || got[0] != "StageArchived" {
		t.Fatalf("guard did not report a constant added to the root but missing from the list: %v", got)
	}
	if got := missingFrom(listed, defined); len(got) != 0 {
		t.Fatalf("guard reported a spurious missing name: %v", got)
	}
	// The reverse direction (list references a name the root no longer defines).
	if got := missingFrom(listed, map[string]bool{"StageCreated": true, "StageRemoved": true}); len(got) != 0 {
		t.Fatalf("reverse set difference is wrong: %v", got)
	}
	orphanList := map[string]bool{"StageCreated": true, "StageGhost": true}
	if got := missingFrom(orphanList, defined); len(got) != 1 || got[0] != "StageGhost" {
		t.Fatalf("guard did not report a list name absent from the root: %v", got)
	}
}
