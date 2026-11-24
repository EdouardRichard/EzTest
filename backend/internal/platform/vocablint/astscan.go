package vocablint

// The AST side of the machinery: the elision-aware const/var group walk (the
// single authority for type/value elision, which four gates used to re-implement
// locally) and the string-literal site collector those gates scan with.

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// ConstSpec is one spec of a const/var group, with the two inheritances Go
// applies inside a group already resolved:
//
//   - Type is the DECLARED type name, inherited from the previous spec when this
//     spec elides it (H-F2: a status written with an elided type still joins the
//     guarded set). It is "" when the group has no type yet or the type is not a
//     plain identifier.
//   - Exprs is the EFFECTIVE value expression list: a value-elided spec repeats
//     the previous spec's expressions.
//
// Names and Exprs are parallel only when the spec spells its own values; a
// value-elided spec may declare more names than the inherited list has
// expressions. Callers that iterate names must bound the index themselves;
// callers that filter by type alone may iterate Exprs directly. Both shapes are
// preserved here so each gate keeps the exact iteration it had.
type ConstSpec struct {
	// Group is the 0-based index of the const/var declaration GROUP this spec
	// belongs to, counted among the groups of the walked token that declare at
	// least one value spec. R3's group inheritance needs the group boundary, and
	// carrying it here is what lets ConstGroups be the single slicing authority:
	// before T11e-5 four gates each re-sliced ConstSpecs against the AST GenDecl
	// list by hand (H-A21③/⑤, T11e-4 §12.5).
	Group int
	Type  string
	Names []string
	Exprs []ast.Expr
}

// ConstSpecs walks every const (tok == token.CONST) or var (tok == token.VAR)
// group of file in source order, resolving type and value elision per group and
// tagging each spec with its Group index.
func ConstSpecs(file *ast.File, tok token.Token) []ConstSpec {
	var out []ConstSpec
	group := 0
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != tok {
			continue
		}
		start := len(out)
		currentType := ""
		var prevValues []ast.Expr
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				if ident, ok := vs.Type.(*ast.Ident); ok {
					currentType = ident.Name
				} else {
					currentType = ""
				}
			}
			exprs := vs.Values
			if len(exprs) == 0 {
				exprs = prevValues
			}
			names := make([]string, 0, len(vs.Names))
			for _, name := range vs.Names {
				names = append(names, name.Name)
			}
			out = append(out, ConstSpec{Group: group, Type: currentType, Names: names, Exprs: exprs})
			if len(vs.Values) > 0 {
				prevValues = vs.Values
			}
		}
		if len(out) > start {
			group++
		}
	}
	return out
}

// ConstGroups slices ConstSpecs into its const/var GROUPS by the Group index the
// walk already resolved. It is the SINGLE group-slicing authority: a gate never
// re-walks the AST GenDecl list to find group boundaries (four hand-written
// copies existed before T11e-5). Groups are returned in source order and every
// group is non-empty.
func ConstGroups(file *ast.File, tok token.Token) [][]ConstSpec {
	var groups [][]ConstSpec
	for _, spec := range ConstSpecs(file, tok) {
		for len(groups) <= spec.Group {
			groups = append(groups, nil)
		}
		groups[spec.Group] = append(groups[spec.Group], spec)
	}
	return groups
}

// StringValue returns the compile-time value of a string literal expression and
// reports whether the expression IS a string literal. A non-literal expression
// (an alias, a conversion, a cross-package reference) returns ok=false so the
// caller can fail loud or classify it — never silently skip it. An unquote
// failure on a parsed string literal is returned as an error: it means the
// derivation is broken, not that the value is absent.
func StringValue(expr ast.Expr) (string, bool, error) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false, nil
	}
	text, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", true, err
	}
	return text, true, nil
}

// SourceLines splits source into lines with CRLF normalized, so a gate's line
// indices and its fragment matching agree regardless of the file's line endings.
func SourceLines(src string) []string {
	return strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
}

// Site is one unique (line, token) pair a file contributes. Line is 0-based into
// SourceLines(src); Token is the raw source token ("value" or 'value').
//
// Uniqueness matters: occurrence-scoped counting already accounts for EVERY
// occurrence of that token on the line, so evaluating the line once per token is
// both correct and non-double-counting.
type Site struct {
	Line  int
	Token string
}

// ASTSites collects the sites where a guarded value appears in a parsed Go file:
// either as a Go string literal equal to the value, or as the SQL single-quoted
// form 'value' inside a (possibly multi-line) string literal — in which case every
// carrying line is evaluated, so a fragment exemption applies to the predicate
// line rather than the query's opening line.
//
// Struct-tag literals are stripped mechanically (a tag is a wire name, not a
// status predicate). name is used only in the returned problems.
//
// fset MUST be the FileSet that parsed file. A foreign or empty FileSet makes
// token.FileSet.Position return the ZERO Position (Line 0) for every Pos, which
// would silently produce Site{Line:-1} and could index lines[-1]. That wiring
// error is reported as a problem instead (fail closed), never as garbage sites.
func ASTSites(name string, file *ast.File, fset *token.FileSet, lines []string, values []string) ([]Site, []string) {
	tagPos := map[token.Pos]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if field, ok := n.(*ast.Field); ok && field.Tag != nil {
			tagPos[field.Tag.Pos()] = true
		}
		return true
	})
	seen := map[Site]bool{}
	var sites []Site
	var problems []string
	addSite := func(line int, tok string) {
		if line < 0 || line >= len(lines) {
			problems = append(problems, fmt.Sprintf("literal at %s resolved to source line %d, outside the scanned file's %d lines: "+
				"the FileSet passed to ASTSites did not parse this file (derivation broken; the gate would go vacuous)",
				name, line+1, len(lines)))
			return
		}
		s := Site{Line: line, Token: tok}
		if !seen[s] {
			seen[s] = true
			sites = append(sites, s)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || tagPos[lit.Pos()] {
			return true
		}
		text, unquoteErr := strconv.Unquote(lit.Value)
		if unquoteErr != nil {
			// A parsed Go string literal always unquotes; a failure means the
			// derivation is broken — report it loud, never silently skip.
			problems = append(problems, fmt.Sprintf("unquote literal at %s:%d: %v (derivation broken; the gate would go vacuous)",
				name, fset.Position(lit.Pos()).Line, unquoteErr))
			return true
		}
		for _, value := range values {
			if text == value {
				addSite(fset.Position(lit.Pos()).Line-1, lit.Value)
			} else if strings.Contains(text, "'"+value+"'") {
				start := fset.Position(lit.Pos()).Line - 1
				end := fset.Position(lit.End()).Line - 1
				// Guard BEFORE indexing: a foreign FileSet yields start = -1 and
				// lines[-1] would panic rather than report.
				if start < 0 || end < start {
					problems = append(problems, fmt.Sprintf("SQL literal at %s resolved to source lines %d..%d: "+
						"the FileSet passed to ASTSites did not parse this file (derivation broken; the gate would go vacuous)",
						name, start+1, end+1))
					continue
				}
				for i := start; i < len(lines) && i <= end; i++ {
					if strings.Contains(lines[i], "'"+value+"'") {
						addSite(i, "'"+value+"'")
					}
				}
			}
		}
		return true
	})
	return sites, problems
}
