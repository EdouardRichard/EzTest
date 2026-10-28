package main

// This file pins the Task 16b composition-root wiring the scope review called
// out as optional but regression-prone: main.go has no runnable unit seam (it
// opens PostgreSQL and the HTTP server), so a missing RunDeps.Impact assignment
// silently disables the incremental-design preseed and only shows up as an
// impact_unavailable plan warning at runtime.
//
// The test parses the composition root and fails on the exact regression: the
// mindmap impact adapter is not constructed from codeIntelAnalysis, or the run
// coordinator's RunDeps.Impact field is not wired to it (spec §6.1 step
// 3b/§7.6).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCompositionRootWiresMindmapImpactSummary proves main.go passes the P4
// impact summary adapter into RunDeps.Impact.
func TestCompositionRootWiresMindmapImpactSummary(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	adapterWired := false
	depsWired := false
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			selector, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "NewImpactSummaryAdapter" {
				return true
			}
			if len(node.Args) != 1 {
				t.Fatalf("NewImpactSummaryAdapter has %d arguments, want the P4 analysis service", len(node.Args))
			}
			argument, ok := node.Args[0].(*ast.Ident)
			if !ok || argument.Name != "codeIntelAnalysis" {
				t.Fatalf("NewImpactSummaryAdapter argument = %T, want codeIntelAnalysis", node.Args[0])
			}
			adapterWired = true
		case *ast.CompositeLit:
			selector, ok := node.Type.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "RunDeps" {
				return true
			}
			for _, element := range node.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := pair.Key.(*ast.Ident)
				if !ok || key.Name != "Impact" {
					continue
				}
				value, ok := pair.Value.(*ast.Ident)
				if !ok || value.Name != "mindmapImpactSummary" {
					t.Fatalf("RunDeps.Impact must be the mindmapImpactSummary adapter, got %T", pair.Value)
				}
				depsWired = true
			}
		}
		return true
	})
	if !adapterWired {
		t.Fatal("main.go does not construct the P4 impact summary adapter with NewImpactSummaryAdapter(codeIntelAnalysis); " +
			"the incremental_design starter seeds no impact signal")
	}
	if !depsWired {
		t.Fatal("RunDeps.Impact is not wired in the composition root; the incremental_design starter seeds no impact signal")
	}
}
