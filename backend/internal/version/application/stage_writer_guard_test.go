package versionapp_test

// v0.22 Task 25 fix round 1 (review finding 3, R-B4-1): StageService is the only
// v0.22 writer of version.stage. The legacy StageMachine convenience writers are
// kept for the pre-v0.22 call sites until Task 36/37 routes them through
// StageService, but the new surface must never grow a second convention: every
// StageService edge has to go through the same atomic StageMachine core
// (Transition/Suspend/resumeRecorded), never through the deprecated legacy
// methods that bypass the v0.22 orchestration.
//
// This is a compile-time-shaped guard implemented as an AST test so adding a
// legacy call inside StageService fails CI even though Go does not warn on
// deprecated-method use by itself.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// stageServiceLegacyWriters are the deprecated StageMachine methods. They are
// still called by the pre-v0.22 production sites documented for Task 36 in
// task-25-report.md; the v0.22 StageService must not call them.
var stageServiceLegacyWriters = map[string]bool{
	"StartClarification":     true,
	"ConvergeClarification":  true,
	"ConvergeDesign":         true,
	"OnRequirementConfirmed": true,
	"OnCasesConfirmed":       true,
	"OnWaveTerminal":         true,
	"Reject":                 true,
	"ResumeTo":               true,
	"RecordGateDenied":       true,
}

// stageServiceCoreMethods are the atomic StageMachine calls StageService may use:
// the same source-stage compare-and-swap path as the legacy machine.
var stageServiceCoreMethods = map[string]bool{
	"Stage":          true,
	"load":           true,
	"Transition":     true,
	"Suspend":        true,
	"Resume":         true,
	"resumeRecorded": true,
}

func TestStageServiceIsTheOnlyV022StageWriter(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	methods := 0
	coreCalls := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !stageServiceReceiver(fn.Recv) {
				continue
			}
			methods++
			// Direct repository writes would bypass the machine CAS entirely.
			if selectorCall(fn.Body, "s", "repo") {
				t.Fatalf("%s: StageService method %s writes the repository directly",
					name, fn.Name.Name)
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callName, ok := stageMachineCallName(call)
				if !ok {
					return true
				}
				if stageServiceLegacyWriters[callName] {
					t.Fatalf("%s: StageService method %s calls deprecated StageMachine.%s; "+
						"route it through the atomic core or the v0.22 orchestration",
						name, fn.Name.Name, callName)
				}
				if stageServiceCoreMethods[callName] {
					coreCalls++
				}
				return true
			})
		}
	}
	if methods < 8 {
		t.Fatalf("found %d StageService methods, want at least 8 (guard may be reading "+
			"the wrong package directory)", methods)
	}
	if coreCalls == 0 {
		t.Fatal("StageService never calls the atomic StageMachine core; the CAS wrapper is missing")
	}
}

func stageServiceReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) != 1 {
		return false
	}
	switch typ := recv.List[0].Type.(type) {
	case *ast.StarExpr:
		ident, ok := typ.X.(*ast.Ident)
		return ok && ident.Name == "StageService"
	case *ast.Ident:
		return typ.Name == "StageService"
	default:
		return false
	}
}

// stageMachineCallName recognizes s.machine.<Name>(...) calls.
func stageMachineCallName(call *ast.CallExpr) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	receiver, ok := selector.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	base, ok := receiver.X.(*ast.Ident)
	if !ok || base.Name != "s" || receiver.Sel.Name != "machine" {
		return "", false
	}
	return selector.Sel.Name, true
}

// selectorCall reports whether body contains <base>.<field>...("...").
func selectorCall(body *ast.BlockStmt, base, field string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := receiver.X.(*ast.Ident)
		if ok && ident.Name == base && receiver.Sel.Name == field {
			found = true
		}
		return true
	})
	return found
}
