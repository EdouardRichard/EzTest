// Package govocab derives the compile-time string values of Go const AND var
// declarations from source, for the repository's vocabulary gates
// (version/domain/plan_item_test.go,
// version/infrastructure/postgres/handoff_repo_lint_test.go and the context
// status-vocabulary gates that share platform/vocablint).
//
// It is TEST SUPPORT: only _test.go files import it. It lives in the platform
// shared kernel so EVERY context's gates share ONE derivation authority instead
// of each keeping a second copy of the resolver (constitution 五.1/五.3). It is
// pure vocabulary derivation — no entity state, no side effects beyond reading
// source files — which is what makes platform the right home for it.
//
// History (T11e-0): it used to live in version/internal/govocab, where Go's
// internal-package rule made it physically unimportable from any other context
// or from cmd/eztest. That visibility limit had already forced the agent context
// to hand-write a second AST value walk
// (agent/infrastructure/versionadapter/briefing_sut_gate_pin_integration_test.go).
// Moving the package here is the root-cause fix for that duplication; the
// exported API and the resolution semantics are unchanged.
//
// # Why hand-written and not golang.org/x/tools/go/packages
//
// The gates need constant STRING VALUES, not type checking. go/packages shells
// out to `go list -export` and would add golang.org/x/tools (+ its transitive
// modules) as a new direct dependency of a self-hosted single binary, which
// constitution Constraints C.3 requires justifying, licensing and auditing. A
// go/ast + go/constant resolver over the module's own source covers every shape
// the vocabulary uses at a fraction of that cost, and its coverage claim is
// explicit (below) rather than implied.
//
// # What it resolves (its coverage claim, 方法纪律 ⑧)
//
//   - string literals, including the members of a []string / [N]string set and
//     the string KEYS of a map set — a var set is a vocabulary too;
//   - same-package identifier aliases (across every file of the package),
//     string(x) conversions, conversions to a NAMED string type (the callee is
//     recognised as a type from the package's type declarations, so a function
//     call is never mistaken for one), `+` concatenation and parenthesised
//     expressions;
//   - cross-package constant references (pkg.Name) whose import path is inside
//     this module — resolved by parsing that package's source;
//   - value-elided const specs (`const ( A = "x"; B )`), which repeat the
//     previous spec's expression.
//
// A declaration whose expression is NOT a compile-time string (a numeric
// literal/expression, iota/true/false/nil, errors.New(...), regexp.MustCompile(...),
// a struct literal, ...) is reported with IsString=false and Unresolved=false: it
// cannot be a vocabulary value, so the caller skips it without an allowlist entry.
//
// A declaration whose expression IS string-shaped (identifier, selector,
// conversion, concatenation, index/slice, dereference, type assertion, channel
// receive) but cannot be evaluated — an external-module reference, a missing
// identifier, a cycle — is reported Unresolved=true. The caller MUST fail loud or
// classify it explicitly; silently dropping it is the alias blind spot this
// package exists to close. The same holds for an unresolvable MEMBER of a set:
// the set then carries IsString=true (its resolved members) AND Unresolved=true,
// and the caller must do both — protect the values and classify the declaration.
// Values computed at runtime (a function call returning a string) are out of
// scope by construction: they are not compile-time vocabulary.
package govocab

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Decl is one derived const/var declaration.
type Decl struct {
	Name     string
	Pkg      string
	File     string
	Line     int
	Tok      token.Token // token.CONST or token.VAR
	Exported bool
	// Values holds every compile-time string value the declaration produces: one
	// for a scalar, several for a set (slice/array elements, map keys).
	Values []string
	// IsString reports that Values is populated.
	IsString bool
	// Unresolved reports a string-shaped expression (or a set MEMBER) the
	// resolver could not evaluate. It is independent of IsString: a partially
	// resolvable set has BOTH set, so the caller protects the resolved values
	// and still classifies the declaration loudly. The caller must never treat
	// IsString alone as "fully covered".
	Unresolved bool
	// Expr is the original value expression, for diagnostics.
	Expr ast.Expr
}

// Resolver parses packages once and resolves values across them.
type Resolver struct {
	backendDir string
	modulePath string
	fset       *token.FileSet
	packages   map[string]*packageInfo
}

type packageInfo struct {
	name  string
	decls map[string]valueRef
	types map[string]bool
}

type valueRef struct {
	file *parsedFile
	expr ast.Expr
}

type parsedFile struct {
	path    string
	file    *ast.File
	imports map[string]string // local name (alias or package name) -> import path
}

// New reads the module path from <backendDir>/go.mod so no import path prefix is
// hardcoded, and returns a resolver rooted at that directory.
func New(backendDir string) (*Resolver, error) {
	raw, err := os.ReadFile(filepath.Join(backendDir, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	modulePath := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	if modulePath == "" {
		return nil, fmt.Errorf("no module directive in %s/go.mod", backendDir)
	}
	abs, err := filepath.Abs(backendDir)
	if err != nil {
		return nil, err
	}
	return &Resolver{
		backendDir: abs,
		modulePath: modulePath,
		fset:       token.NewFileSet(),
		packages:   map[string]*packageInfo{},
	}, nil
}

// Decls derives every const/var declaration of one file. With exportedOnly the
// unexported names are skipped (the plan-status gate guards the exported write
// API; the handoff gate guards the whole vocabulary, sets included).
func (r *Resolver) Decls(file string, exportedOnly bool) ([]Decl, error) {
	parsed, err := r.parseFile(file)
	if err != nil {
		return nil, err
	}
	var out []Decl
	for _, decl := range parsed.file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		var previous []ast.Expr
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			exprs := value.Values
			if len(exprs) == 0 {
				// Value-elided spec: Go repeats the previous spec's expressions.
				exprs = previous
			} else {
				previous = exprs
			}
			for i, name := range value.Names {
				if i >= len(exprs) {
					continue
				}
				if exportedOnly && !ast.IsExported(name.Name) {
					continue
				}
				values, isString, unresolved := r.resolve(exprs[i], parsed, map[string]bool{})
				out = append(out, Decl{
					Name:       name.Name,
					Pkg:        parsed.file.Name.Name,
					File:       parsed.path,
					Line:       r.fset.Position(name.Pos()).Line,
					Tok:        gen.Tok,
					Exported:   ast.IsExported(name.Name),
					Values:     values,
					IsString:   isString,
					Unresolved: unresolved,
					Expr:       exprs[i],
				})
			}
		}
	}
	return out, nil
}

// resolve evaluates one expression to its compile-time string values. The three
// results are (values, isString, unresolved); see the package doc for the rule.
func (r *Resolver) resolve(expr ast.Expr, file *parsedFile, visiting map[string]bool) ([]string, bool, bool) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return nil, false, false
		}
		value := constant.MakeFromLiteral(node.Value, node.Kind, 0)
		if value.Kind() != constant.String {
			return nil, false, false
		}
		return []string{constant.StringVal(value)}, true, false

	case *ast.ParenExpr:
		return r.resolve(node.X, file, visiting)

	case *ast.Ident:
		// Predeclared non-string constants: provably not vocabulary values.
		switch node.Name {
		case "iota", "true", "false", "nil":
			return nil, false, false
		}
		return r.resolveName(node.Name, file, visiting)

	case *ast.SelectorExpr:
		pkgIdent, ok := node.X.(*ast.Ident)
		if !ok {
			return nil, false, true
		}
		importPath, ok := file.imports[pkgIdent.Name]
		if !ok {
			// The import's directory element may differ from its real package
			// name (internal/resource/infrastructure is package resourcepg):
			// fall back to matching the declared package name.
			importPath, ok = r.importByPackageName(pkgIdent.Name, file)
		}
		if !ok {
			return nil, false, true
		}
		dir, ok := r.moduleDir(importPath)
		if !ok {
			// Outside this module (stdlib / third party): not derivable here.
			return nil, false, true
		}
		info, err := r.loadPackage(dir)
		if err != nil || info == nil {
			return nil, false, true
		}
		ref, ok := info.decls[node.Sel.Name]
		if !ok {
			return nil, false, true
		}
		return r.resolveExprRef(ref, visiting)

	case *ast.CallExpr:
		if len(node.Args) != 1 {
			return nil, false, false
		}
		// string(x) is a conversion, not a call: it preserves the value.
		if fun, ok := node.Fun.(*ast.Ident); ok && fun.Name == "string" {
			return r.resolve(node.Args[0], file, visiting)
		}
		// A conversion to a NAMED string type (MyStatus("x") / pkg.MyStatus("x"))
		// also preserves the value. Whether the callee is a type is DERIVED from
		// the package's type declarations, not guessed from the name.
		if r.calleeIsType(node.Fun, file) {
			return r.resolve(node.Args[0], file, visiting)
		}
		// Any other call produces a runtime value (errors.New, fmt.Errorf,
		// regexp.MustCompile, ...): not a compile-time string.
		return nil, false, false

	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return nil, false, false
		}
		left, leftString, leftUnresolved := r.resolve(node.X, file, visiting)
		right, rightString, rightUnresolved := r.resolve(node.Y, file, visiting)
		if leftUnresolved || rightUnresolved {
			return nil, false, true
		}
		if !leftString || !rightString {
			return nil, false, false
		}
		var out []string
		for _, l := range left {
			for _, rr := range right {
				out = append(out, l+rr)
			}
		}
		return out, true, false

	case *ast.CompositeLit:
		// A set literal: []string{...} / [N]string{...} elements, or the string
		// KEYS of map[string]...{...}. A struct literal is not a vocabulary set.
		var elements []ast.Expr
		switch node.Type.(type) {
		case *ast.ArrayType:
			elements = node.Elts
		case *ast.MapType:
			for _, elt := range node.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					elements = append(elements, kv.Key)
				}
			}
		case nil:
			// An elided type inside another composite literal: treat the
			// elements as candidates (a nested set of strings).
			elements = node.Elts
		default:
			return nil, false, false
		}
		var out []string
		unresolved := false
		for _, elt := range elements {
			values, isString, isUnresolved := r.resolve(elt, file, visiting)
			if isUnresolved {
				unresolved = true
			}
			if isString {
				out = append(out, values...)
			}
		}
		// I1: a PARTIALLY resolvable set keeps BOTH signals. Returning
		// unresolved=false just because one member was a literal would silently
		// drop every other member — the alias blind spot in set form. The caller
		// protects the resolved values AND classifies the declaration loudly.
		return out, len(out) > 0, unresolved

	case *ast.IndexExpr:
		// Set[i] / str[i] can yield a string, but the element is not a
		// compile-time constant: report it so the caller classifies it.
		return nil, false, true

	case *ast.SliceExpr:
		// Set[a:b] / str[a:b] likewise.
		return nil, false, true

	case *ast.StarExpr:
		// *p may dereference a string.
		return nil, false, true

	case *ast.TypeAssertExpr:
		// x.(string) may yield a string.
		return nil, false, true

	case *ast.UnaryExpr:
		// <-ch may receive a string; the other unary operators provably cannot
		// (! bool, -/^ numeric, & pointer).
		if node.Op == token.ARROW {
			return nil, false, true
		}
		return nil, false, false

	default:
		return nil, false, false
	}
}

// calleeIsType reports whether a conversion callee names a TYPE declared in the
// same package or in a module-local imported package (derived from the type
// declarations, so a function call is never mistaken for a conversion).
func (r *Resolver) calleeIsType(fun ast.Expr, file *parsedFile) bool {
	switch callee := fun.(type) {
	case *ast.Ident:
		info, err := r.loadPackage(filepath.Dir(file.path))
		if err != nil || info == nil {
			return false
		}
		return info.types[callee.Name]
	case *ast.SelectorExpr:
		pkgIdent, ok := callee.X.(*ast.Ident)
		if !ok {
			return false
		}
		importPath, ok := file.imports[pkgIdent.Name]
		if !ok {
			importPath, ok = r.importByPackageName(pkgIdent.Name, file)
		}
		if !ok {
			return false
		}
		dir, ok := r.moduleDir(importPath)
		if !ok {
			return false
		}
		info, err := r.loadPackage(dir)
		if err != nil || info == nil {
			return false
		}
		return info.types[callee.Sel.Name]
	default:
		return false
	}
}

// resolveName looks a bare identifier up in the whole package (any file).
func (r *Resolver) resolveName(name string, file *parsedFile, visiting map[string]bool) ([]string, bool, bool) {
	info, err := r.loadPackage(filepath.Dir(file.path))
	if err != nil || info == nil {
		return nil, false, true
	}
	ref, ok := info.decls[name]
	if !ok {
		return nil, false, true
	}
	return r.resolveExprRef(ref, visiting)
}

// resolveExprRef resolves a named declaration, guarding against cycles.
func (r *Resolver) resolveExprRef(ref valueRef, visiting map[string]bool) ([]string, bool, bool) {
	key := ref.file.path + ":" + strconv.Itoa(r.fset.Position(ref.expr.Pos()).Line) + ":" + strconv.Itoa(r.fset.Position(ref.expr.Pos()).Column)
	if visiting[key] {
		return nil, false, true
	}
	visiting[key] = true
	defer delete(visiting, key)
	return r.resolve(ref.expr, ref.file, visiting)
}

// moduleDir maps a module-local import path to its directory.
func (r *Resolver) moduleDir(importPath string) (string, bool) {
	if importPath != r.modulePath && !strings.HasPrefix(importPath, r.modulePath+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(importPath, r.modulePath), "/")
	return filepath.Join(r.backendDir, filepath.FromSlash(rel)), true
}

// importByPackageName resolves a qualifier that names the imported package's
// declared name rather than its directory element.
func (r *Resolver) importByPackageName(name string, file *parsedFile) (string, bool) {
	for _, importPath := range file.imports {
		dir, ok := r.moduleDir(importPath)
		if !ok {
			continue
		}
		info, err := r.loadPackage(dir)
		if err != nil || info == nil {
			continue
		}
		if info.name == name {
			return importPath, true
		}
	}
	return "", false
}

// parseFile parses one file and records its import names.
func (r *Resolver) parseFile(path string) (*parsedFile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(r.fset, abs, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	parsed := &parsedFile{path: abs, file: file, imports: map[string]string{}}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path2pkg(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		parsed.imports[name] = importPath
	}
	return parsed, nil
}

// path2pkg guesses the package name of an import path (its last element). It is
// only a default: an explicit alias always wins, and a package whose name differs
// from its directory is resolved through loadPackage's actual package name.
func path2pkg(importPath string) string {
	parts := strings.Split(importPath, "/")
	return parts[len(parts)-1]
}

// loadPackage parses every non-test Go file of a directory once and indexes its
// const/var declarations by name.
func (r *Resolver) loadPackage(dir string) (*packageInfo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if cached, ok := r.packages[abs]; ok {
		return cached, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	info := &packageInfo{decls: map[string]valueRef{}, types: map[string]bool{}}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := r.parseFile(filepath.Join(abs, name))
		if err != nil {
			return nil, err
		}
		info.name = parsed.file.Name.Name
		for _, decl := range parsed.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			if gen.Tok == token.TYPE {
				for _, spec := range gen.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						info.types[ts.Name.Name] = true
					}
				}
				continue
			}
			if gen.Tok != token.CONST && gen.Tok != token.VAR {
				continue
			}
			var previous []ast.Expr
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				exprs := value.Values
				if len(exprs) == 0 {
					exprs = previous
				} else {
					previous = exprs
				}
				for i, name := range value.Names {
					if i >= len(exprs) {
						continue
					}
					if _, exists := info.decls[name.Name]; !exists {
						info.decls[name.Name] = valueRef{file: parsed, expr: exprs[i]}
					}
				}
			}
		}
	}
	// Cache only after EVERY file parsed successfully (T7 M4): a mid-loop parse
	// failure must not leave a partially populated package in the cache, which a
	// later call would silently reuse as if complete.
	r.packages[abs] = info
	return info, nil
}
