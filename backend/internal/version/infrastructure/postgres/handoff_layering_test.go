package versionpg_test

// H-B1 layering guard: the handoff resource ASSEMBLY belongs to the version
// application service. The adapter implements the application's ports and reads
// version-owned rows; it must not invoke application code (that was the shape
// where an infrastructure adapter decided another context's effective scope).
//
// Both the file set and the call detection are DERIVED: a file participates in the
// handoff adapter when it mentions a versionapp.Handoff* type, so a new adapter
// file is covered without editing a list.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// handoffAdapterFiles derives the versionpg production files that participate in
// the handoff adapter.
func handoffAdapterFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, path := range versionpgProductionFiles(t) {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		local := versionappImportName(file)
		if local == "" {
			continue
		}
		mentions := false
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == local &&
					strings.HasPrefix(sel.Sel.Name, "Handoff") {
					mentions = true
				}
			}
			return true
		})
		if mentions {
			files = append(files, path)
		}
	}
	if len(files) == 0 {
		t.Fatal("no handoff adapter file derived; the derivation is broken and the check would go vacuous")
	}
	return files
}

// versionappImportName returns the local name of the version application import.
func versionappImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasSuffix(path, "/version/application") {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "versionapp"
	}
	return ""
}

// applicationCalls returns every call into the version APPLICATION package: the
// adapter may reference application types/constants (it implements their ports)
// but must not INVOKE application code. Two invocation shapes are detected
// (T7 M2): package-qualified calls (versionapp.Foo(...)) AND method calls on a
// receiver bound to a versionapp type — a struct field / parameter / var declared
// as versionapp.T (or *versionapp.T), including a selector chain like r.svc.Foo().
// The receiver detection is name-based (no type checker): a field/param/var whose
// declared type is versionapp.* binds its name, and a method call on that name (or
// a chain ending in it) fires. This is fail-loud by design; its bounded blind spot
// (a deeper chain whose intermediate field is not itself versionapp-typed, or a
// same-named local of another type) is disclosed, not silently skipped.
func applicationCalls(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	local := versionappImportName(file)
	if local == "" {
		return nil
	}
	bound := versionappBoundNames(file, local)
	var calls []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		line := strconv.Itoa(fset.Position(call.Pos()).Line)
		if ident, ok := sel.X.(*ast.Ident); ok {
			if ident.Name == local {
				calls = append(calls, path+":"+line+" calls "+local+"."+sel.Sel.Name)
				return true
			}
			if bound[ident.Name] {
				calls = append(calls, path+":"+line+" calls "+ident.Name+"."+sel.Sel.Name+" (versionapp-typed receiver)")
				return true
			}
		}
		if inner, ok := sel.X.(*ast.SelectorExpr); ok && bound[inner.Sel.Name] {
			calls = append(calls, path+":"+line+" calls ..."+inner.Sel.Name+"."+sel.Sel.Name+" (versionapp-typed receiver chain)")
		}
		return true
	})
	return calls
}

// versionappBoundNames collects the identifiers declared with a versionapp type
// (struct fields, function parameters, var specs), unwrapping a pointer type.
func versionappBoundNames(file *ast.File, local string) map[string]bool {
	bound := map[string]bool{}
	isVersionappType := func(expr ast.Expr) bool {
		if star, ok := expr.(*ast.StarExpr); ok {
			expr = star.X
		}
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		ident, ok := sel.X.(*ast.Ident)
		return ok && ident.Name == local
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.Field:
			if isVersionappType(d.Type) {
				for _, name := range d.Names {
					bound[name.Name] = true
				}
			}
		case *ast.ValueSpec:
			if isVersionappType(d.Type) {
				for _, name := range d.Names {
					bound[name.Name] = true
				}
			}
		}
		return true
	})
	return bound
}

// TestHandoffAdapterDoesNotCallApplication pins the layering direction of the
// handoff read path.
func TestHandoffAdapterDoesNotCallApplication(t *testing.T) {
	var calls []string
	for _, path := range handoffAdapterFiles(t) {
		calls = append(calls, applicationCalls(t, path)...)
	}
	for _, call := range calls {
		t.Errorf("H-B1 layering: %s -- the handoff assembly belongs to the version application service", call)
	}
	if len(calls) > 0 {
		t.Fatalf("%d application call(s) in the handoff adapter", len(calls))
	}
}

// TestHandoffLayeringGuardFires proves the check distinguishes a real application
// call from a type reference or a constant read.
func TestHandoffLayeringGuardFires(t *testing.T) {
	write := func(t *testing.T, src string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "sample.go")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write sample: %v", err)
		}
		return path
	}
	const importLine = "import versionapp \"example.test/mod/internal/version/application\"\n"
	calling := "package p\n" + importLine + "func f() { _ = versionapp.HandoffRequirementStatusIncluded(\"draft\") }\n"
	if got := applicationCalls(t, write(t, calling)); len(got) != 1 {
		t.Fatalf("layering guard did not fire on an application call: %v", got)
	}
	typing := "package p\n" + importLine + "func f() versionapp.HandoffResources { return versionapp.HandoffResources{} }\n"
	if got := applicationCalls(t, write(t, typing)); len(got) != 0 {
		t.Fatalf("layering guard fired on a type reference: %v", got)
	}
	constant := "package p\n" + importLine + "const source = versionapp.HandoffSourceQA\n"
	if got := applicationCalls(t, write(t, constant)); len(got) != 0 {
		t.Fatalf("layering guard fired on a constant read: %v", got)
	}
	// T7 M2: a method call on a versionapp-typed receiver fires (field chain,
	// parameter, and var), not just a package-qualified call.
	fieldChain := "package p\n" + importLine + "type adapter struct { svc *versionapp.HandoffService }\nfunc (a *adapter) f() { a.svc.Generate() }\n"
	if got := applicationCalls(t, write(t, fieldChain)); len(got) != 1 {
		t.Fatalf("layering guard missed a versionapp-typed field receiver call: %v", got)
	}
	param := "package p\n" + importLine + "func f(svc versionapp.HandoffService) { svc.Generate() }\n"
	if got := applicationCalls(t, write(t, param)); len(got) != 1 {
		t.Fatalf("layering guard missed a versionapp-typed parameter receiver call: %v", got)
	}
	varRecv := "package p\n" + importLine + "var svc *versionapp.HandoffService\nfunc f() { svc.Generate() }\n"
	if got := applicationCalls(t, write(t, varRecv)); len(got) != 1 {
		t.Fatalf("layering guard missed a versionapp-typed var receiver call: %v", got)
	}
	// A receiver NOT bound to a versionapp type does not fire.
	unrelated := "package p\n" + importLine + "type other struct{}\nfunc (o other) f() { o.Generate() }\n"
	if got := applicationCalls(t, write(t, unrelated)); len(got) != 0 {
		t.Fatalf("layering guard fired on an unrelated receiver: %v", got)
	}
}
