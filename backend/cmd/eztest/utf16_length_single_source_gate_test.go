package main

// The utf16Length single-source gate (v0.2.4-H T14, H-E1 / R-H9).
//
// textunit.UTF16Length (platform/textunit) is the ONE authority for the streaming
// append offset shared by the agent transcript (agent/application) and the version
// conversation (version/application). Before T14 each context carried its own
// byte-identical copy (transcriptUTF16Length / utf16Length); this gate keeps the
// dedup closed so a second implementation cannot silently reappear. Constitution
// 五.1/五.3/五.4 (single authority, no hand-written second truth, no bypass).
//
// # Two DERIVED predicates (no hand-written file list)
//
// The scan surface is derived by walking backend/internal and backend/cmd and
// collecting every non-test .go file. A new context, subdirectory or production
// file joins the net automatically — there is no closed file table to drift.
//
//  1. import predicate: any production file importing "unicode/utf16" must live
//     in the authority package (platform/textunit). _test.go files are EXCLUDED
//     on purpose: a test must compute its expected offset INDEPENDENTLY (e.g.
//     runner_transcript_stream_test.go computes len(utf16.Encode([]rune(text)))
//     itself to check the published offset); forcing tests onto the authority
//     would make the gate born-green.
//  2. name predicate: any production FuncDecl whose name matches (?i)utf16len
//     must be exactly the one authority. This catches a hand-written proxy that
//     reimplements the encoding under a utf16len-ish name without importing
//     unicode/utf16 (dodging the import predicate).
//
// # Anti-vacuity (an empty scan must not pass)
//
//   - the derived surface must be non-empty and must cover BOTH the agent and the
//     version context (the two consumers);
//   - each predicate must actually FIND the authority itself: zero importers of
//     unicode/utf16, or zero FuncDecls matching the name pattern, is a loud FAIL
//     (the authority vanished or was renamed), never "no duplicates = pass".
//
// This gate is INDEPENDENT of the W1 status-vocabulary gate: it derives its own
// surface and does not import or modify platform/govocab, platform/vocablint or
// testutil/vocabscope.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/textunit"
)

// utf16AuthorityDir is the ONE package allowed to import unicode/utf16 and to
// declare the utf16len-named authority. It is a platform shared kernel, not a
// bounded context.
const utf16AuthorityDir = "backend/internal/platform/textunit/"

// utf16NamePattern matches any FuncDecl name that claims to measure UTF-16 length.
var utf16NamePattern = regexp.MustCompile(`(?i)utf16len`)

// utf16GateRepoRoot walks up from this test's source file to the repository root
// (the directory containing backend/go.mod), so the gate works regardless of the
// test's working directory. Independent of the vocabulary gate's root helper.
func utf16GateRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the utf16Length single-source gate source")
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
	t.Fatalf("repo root containing backend/go.mod not found from %s", file)
	return ""
}

// utf16GateSurface derives the production scan surface: every non-test .go file
// under backend/internal and backend/cmd, repository-relative (slash paths).
func utf16GateSurface(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for _, base := range []string{"backend/internal", "backend/cmd"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(base)), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("T14: derive utf16Length scan surface under %s: %v", base, err)
		}
	}
	return files
}

type utf16Site struct {
	File string
	Line int
	Name string
}

// utf16Scan parses every surface file once and collects (a) the production files
// importing unicode/utf16 and (b) the production FuncDecls whose name matches the
// utf16len pattern. Both are derived from the AST, never from a closed list.
func utf16Scan(t *testing.T, root string, surface []string) (importers, named []utf16Site) {
	t.Helper()
	fset := token.NewFileSet()
	for _, rel := range surface {
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
		if err != nil {
			t.Fatalf("T14: parse %s: %v", rel, err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if p == "unicode/utf16" {
				importers = append(importers, utf16Site{File: rel, Line: fset.Position(imp.Pos()).Line})
			}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil {
				continue
			}
			if utf16NamePattern.MatchString(fn.Name.Name) {
				named = append(named, utf16Site{File: rel, Line: fset.Position(fn.Pos()).Line, Name: fn.Name.Name})
			}
		}
	}
	return importers, named
}

// TestUTF16LengthSingleSource is the guard: the import predicate and the name
// predicate, each with its own anti-vacuity floor.
func TestUTF16LengthSingleSource(t *testing.T) {
	root := utf16GateRepoRoot(t)
	surface := utf16GateSurface(t, root)

	// Anti-vacuity ①: the derived surface is non-empty and covers both consumers.
	if len(surface) == 0 {
		t.Fatal("T14: derived utf16Length scan surface is empty; the gate would be vacuous")
	}
	covered := map[string]bool{}
	for _, f := range surface {
		for _, ctx := range []string{"agent", "version"} {
			if strings.HasPrefix(f, "backend/internal/"+ctx+"/") {
				covered[ctx] = true
			}
		}
	}
	for _, ctx := range []string{"agent", "version"} {
		if !covered[ctx] {
			t.Fatalf("T14: derived scan surface does not cover the %s context (a consumer); the gate would be vacuous", ctx)
		}
	}
	t.Logf("T14: derived utf16Length scan surface = %d production files (agent+version covered)", len(surface))

	importers, named := utf16Scan(t, root, surface)

	// Anti-vacuity ②a: the authority itself must import unicode/utf16. Zero
	// importers means the authority vanished — a loud FAIL, not a clean pass.
	if len(importers) == 0 {
		t.Fatal("T14: no production file imports unicode/utf16; the UTF16Length authority itself was not found (anti-vacuity)")
	}
	// Import predicate: every importer must be inside the authority package.
	for _, s := range importers {
		if !strings.HasPrefix(s.File, utf16AuthorityDir) {
			t.Errorf("T14: %s:%d imports unicode/utf16 outside the authority package %s — route the offset through textunit.UTF16Length (single source, R-H9)",
				s.File, s.Line, utf16AuthorityDir)
		}
	}

	// Anti-vacuity ②b: the authority itself must be found by the name predicate.
	// Zero matches means it was renamed/removed — a loud FAIL, not a clean pass.
	if len(named) == 0 {
		t.Fatal("T14: no production FuncDecl matches (?i)utf16len; the UTF16Length authority itself was not found (anti-vacuity)")
	}
	// Name predicate: exactly one match, and it is the authority.
	if len(named) != 1 {
		for _, s := range named {
			t.Errorf("T14: %s:%d declares %q matching (?i)utf16len — only textunit.UTF16Length may exist (single source, R-H9)", s.File, s.Line, s.Name)
		}
		t.Fatalf("T14: %d utf16len-named FuncDecls found, want exactly 1 (the authority)", len(named))
	}
	if named[0].Name != "UTF16Length" || !strings.HasPrefix(named[0].File, utf16AuthorityDir) {
		t.Fatalf("T14: the sole utf16len-named FuncDecl is %q at %s:%d, want UTF16Length in %s",
			named[0].Name, named[0].File, named[0].Line, utf16AuthorityDir)
	}
}

// TestUTF16LengthAuthorityAstralPlane pins the authority's SEMANTICS (not the copy
// count): an astral rune is 2 UTF-16 code units, never the rune count (1) nor the
// byte count (4). It stays green under an m1-type regression (a re-added duplicate
// with the same correct body) because it pins meaning, not how many copies exist.
// The two call-site behavioral legs live in the consumers' own test files
// (runner_transcript_stream_test.go / conversation_stream_test.go) and assert the
// published offset agrees with this authority.
func TestUTF16LengthAuthorityAstralPlane(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"你好", 2},    // two BMP runes: 2 units, 6 bytes
		{"😀", 2},     // one astral rune: 2 units, 1 rune, 4 bytes
		{"你好😀", 4},   // 2 BMP + 1 astral = 4 units
		{"a😀b", 4},   // 1 + 2 + 1 = 4 units
		{"👨‍👩‍👧", 8}, // family ZWJ sequence: 5 runes, 8 units
	}
	for _, c := range cases {
		if got := textunit.UTF16Length(c.text); got != c.want {
			t.Errorf("textunit.UTF16Length(%q) = %d, want %d UTF-16 code units", c.text, got, c.want)
		}
	}
	// The astral discriminator: 2 units != 1 rune != 4 bytes.
	const astral = "😀"
	if units := textunit.UTF16Length(astral); units != 2 {
		t.Fatalf("astral rune UTF-16 units = %d, want 2", units)
	}
	if runes := len([]rune(astral)); runes == textunit.UTF16Length(astral) {
		t.Fatalf("UTF16Length must differ from the rune count for an astral rune (both %d)", runes)
	}
	if bytes := len(astral); bytes == textunit.UTF16Length(astral) {
		t.Fatalf("UTF16Length must differ from the byte count for an astral rune (both %d)", bytes)
	}
}
