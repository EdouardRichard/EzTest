package main

// v0.2.4-H T10 (H-F10 T13) → T11b (H-F10b #27, thorough fix).
//
// The design.judgment_submit tool used to mirror the design payload bounds as
// PLAIN LITERALS. T10 added a source-scanning value pin because the stronger fix
// touched the agent context (outside its write scope). T11b applied the stronger
// fix: the bounds now live in design/domain (the shared, dependency-free
// vocabulary layer), design/application aliases them under its existing exported
// names, and the tool aliases the same domain constants — so they cannot drift at
// all (constitution V.1/V.4: no second truth, no sync mechanism needed).
//
// The authority is in the DOMAIN, not the application package, because the tool
// cannot import design/application: version/application imports agent tools and
// design/application's internal tests import version/application, so that edge
// closes a test import cycle (`go vet ./...` catches it).
//
// This guard pins the ALIAS SHAPE rather than a value copy: each bound must
// still be declared as a reference to its design domain authority. A contributor
// who re-hardcodes a literal (or points the alias elsewhere) fails here. A value
// comparison would be tautological once the alias exists; the shape check is the
// one with discriminating power. BOTH alias surfaces are pinned: the agent tool
// (TestJudgmentToolBoundsAliasDesignAuthority) and the design application's
// exported names (TestJudgmentAppBoundsAliasDesignAuthority, T11c M2) — guarding
// only the tool side let a re-hardcoded application bound silently fork the
// settling service from the tool while the tool pin stayed green.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// judgmentBoundExprs reads the value expression of each judgment bound constant
// of the given source file straight from its source (the tool constants are
// unexported; the design application aliases are exported). Shared by the tool
// side and the design application side so both alias shapes are pinned by the
// same machinery (T11c M2: the designapp aliases were previously unguarded).
func judgmentBoundExprs(t *testing.T, path string) map[string]ast.Expr {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse the judgment bound source %s: %v", path, err)
	}
	exprs := map[string]ast.Expr{}
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				exprs[name.Name] = value.Values[i]
			}
		}
	}
	return exprs
}

// toolJudgmentBoundExprs reads the agent judgment tool's bound constants.
func toolJudgmentBoundExprs(t *testing.T) map[string]ast.Expr {
	t.Helper()
	return judgmentBoundExprs(t, filepath.Join("..", "..", "internal", "agent", "application", "tools", "judgment.go"))
}

// TestJudgmentToolBoundsAliasDesignAuthority pins that every mirrored bound is an
// ALIAS of its design domain authority, not a re-hardcoded literal. A missing
// constant is a failure too: a rename must not silently drop the pin.
func TestJudgmentToolBoundsAliasDesignAuthority(t *testing.T) {
	exprs := toolJudgmentBoundExprs(t)
	for _, tc := range []struct {
		toolConstant string
		authority    string
	}{
		{"maxJudgmentSubmitFindings", "MaxJudgmentFindings"},
		{"judgmentDimensionMax", "JudgmentDimensionMax"},
		{"judgmentMessageMax", "JudgmentMessageMax"},
	} {
		expr, ok := exprs[tc.toolConstant]
		if !ok {
			t.Fatalf("the judgment tool no longer declares %s; the bound lost its single-source alias", tc.toolConstant)
		}
		selector, ok := expr.(*ast.SelectorExpr)
		if !ok {
			t.Fatalf("tool bound %s is no longer an alias of design.%s (expression %T); a re-hardcoded literal is a second truth (H-F10b #27)",
				tc.toolConstant, tc.authority, expr)
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "design" || selector.Sel.Name != tc.authority {
			t.Fatalf("tool bound %s aliases %s.%s, want design.%s",
				tc.toolConstant, exprString(selector.X), selector.Sel.Name, tc.authority)
		}
	}
}

func exprString(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return "<expr>"
}

// TestJudgmentAppBoundsAliasDesignAuthority pins the DESIGN APPLICATION side of
// the same single-source invariant (T11c M2). T11b moved the authority into
// design/domain and left the application's exported names as aliases, but only
// the TOOL side aliases were guarded: re-hardcoding e.g. designapp's
// MaxJudgmentFindings to a literal would silently fork the settling service from
// the tool while the tool-side pin stayed green. This pin requires all four
// exported application bounds to remain design.<Authority> selector expressions,
// so a re-hardcoded literal (or a redirect to another package) fails here too.
func TestJudgmentAppBoundsAliasDesignAuthority(t *testing.T) {
	exprs := judgmentBoundExprs(t, filepath.Join("..", "..", "internal", "design", "application", "judgment.go"))
	for _, authority := range []string{
		"MaxJudgmentFindings",
		"JudgmentDimensionMax",
		"JudgmentMessageMax",
		"OverrideReasonMax",
	} {
		expr, ok := exprs[authority]
		if !ok {
			t.Fatalf("design/application/judgment.go no longer declares %s; the bound lost its single-source alias", authority)
		}
		selector, ok := expr.(*ast.SelectorExpr)
		if !ok {
			t.Fatalf("design application bound %s is no longer an alias of design.%s (expression %T); a re-hardcoded literal forks the service from the tool (T11c M2)",
				authority, authority, expr)
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "design" || selector.Sel.Name != authority {
			t.Fatalf("design application bound %s aliases %s.%s, want design.%s",
				authority, exprString(selector.X), selector.Sel.Name, authority)
		}
	}
}

// TestJudgmentToolBatchCapIsItsOwnAuthority pins the one bound that is NOT a
// mirror: the per-call batch cap belongs to the tool surface (the design service
// settles one case at a time). It must stay a plain literal here — aliasing it to
// a design constant would invent an authority the design context does not have.
func TestJudgmentToolBatchCapIsItsOwnAuthority(t *testing.T) {
	expr, ok := toolJudgmentBoundExprs(t)["maxJudgmentSubmitCases"]
	if !ok {
		t.Fatal("the judgment tool no longer declares its per-call batch cap")
	}
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		t.Fatalf("maxJudgmentSubmitCases must stay the tool's own integer literal, got %T", expr)
	}
}
