package versionpg_test

// H-B1 cross-context boundary guard (architecture §3.2 / AGENTS §2.1: a context
// reaches another context through its application ports, never by reading its
// tables).
//
// The handoff read model used to assemble the resources block with its own pool
// queries over sut / sut_page / sut_api / target_database / test_account /
// sut_repository / code_repository / environment — eight resource-owned tables.
// That made the effective-scope RULE live in a second place (SQL status
// predicates mirroring resourceapp.SUTView.IsApproved / EffectiveAssets) and let
// the version adapter decide what is inside a version's resource scope.
//
// The assembly now lives in the version APPLICATION service, which consumes
// resourceapp.HandoffResourceReader; this package only reads version-owned rows.
// This guard keeps it that way.
//
// Both sides are DERIVED, not hand-listed (方法纪律 ⑧):
//   - the resource-owned table set comes from the resource context's WRITE side
//     (INSERT INTO / UPDATE ... SET / DELETE FROM in its infrastructure), which is
//     what makes a table belong to a context. It is deliberately not taken from
//     the migration filenames: sut_repository was created by version_0010 yet is
//     owned (written) by resource, so a filename-prefix rule would miss it;
//   - the scanned file set is a walk of this package's production files, so a NEW
//     handoff adapter file is covered without editing a list.
//
// Scope note (disclosed, not silently skipped): the resource delete-guard
// predicates (SUTInUse/PageInUse/.../ReferencingVersions) live in the OTHER
// versionpg package (internal/version/infrastructure) and implement
// resourceapp.UsageChecker by joining resource tables — the registered pattern
// H-B14/T3 adjudicated. They are pinned per-status against IsApproved by
// TestVersionSUTGateMirrorsIsApproved instead of being covered here.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sqlWriteTables matches the SQL statements that make a table belong to a
// context. UPDATE requires the trailing SET so `ON CONFLICT ... DO UPDATE SET`
// (whose next token is SET, not a table) cannot be mistaken for a table name.
var sqlWriteTables = regexp.MustCompile(`(?is)\b(?:INSERT\s+INTO|DELETE\s+FROM)\s+([a-z_][a-z0-9_]*)|\bUPDATE\s+([a-z_][a-z0-9_]*)\s+SET\b`)

// sqlReadTables matches the table positions of a read: FROM and JOIN, including a
// quoted identifier ("sut") and a schema-qualified name (public.sut) so those
// spellings cannot slip past the guard.
var sqlReadTables = regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s+(?:[a-z_][a-z0-9_]*\.)?"?([a-z_][a-z0-9_]*)"?`)

// goStringLiterals returns every string literal of a Go source file (raw and
// interpreted), with the line each literal starts on. Comments are not literals,
// so a documented counter-example ("... FROM sut ...") never fires; prose inside
// a literal is scanned, but a table reference requires the SQL keyword position.
func goStringLiterals(t *testing.T, path string) []struct {
	text string
	line int
} {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []struct {
		text string
		line int
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		out = append(out, struct {
			text string
			line int
		}{text: text, line: fset.Position(lit.Pos()).Line})
		return true
	})
	return out
}

// matchTableNames returns the captured table names of one regexp over src. The
// alternation has two capture groups (the INSERT/DELETE arm and the UPDATE arm);
// exactly one participates per match.
func matchTableNames(re *regexp.Regexp, src string) []string {
	var names []string
	for _, match := range re.FindAllStringSubmatch(src, -1) {
		for _, group := range match[1:] {
			if group != "" {
				names = append(names, strings.ToLower(group))
			}
		}
	}
	return names
}

// resourceOwnedTables derives the resource context's tables from its write side.
func resourceOwnedTables(t *testing.T) map[string]bool {
	t.Helper()
	const resourceInfra = "../../../resource/infrastructure"
	owned := map[string]bool{}
	err := filepath.WalkDir(resourceInfra, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, lit := range goStringLiterals(t, path) {
			for _, name := range matchTableNames(sqlWriteTables, lit.text) {
				owned[name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("derive resource-owned tables: %v", err)
	}
	// Derivation sanity floor: these are the tables the handoff used to read. If
	// the derivation stops seeing them it is broken, and the guard would go
	// vacuous rather than pass (方法纪律 ⑧).
	for _, want := range []string{
		"sut", "sut_page", "sut_api", "target_database",
		"test_account", "sut_repository", "code_repository", "environment",
	} {
		if !owned[want] {
			t.Fatalf("resource-owned table derivation lost %q (derived %v); the write-side scan is broken", want, sortedKeys(owned))
		}
	}
	return owned
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// versionpgProductionFiles walks this package's production files (derived set).
func versionpgProductionFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no production files found in the versionpg handoff package; the scan would go vacuous")
	}
	return files
}

// findResourceTableAccesses reports every READ or WRITE of a resource-owned table
// in one source file's string literals. Writes are included (T7 M3): an adapter
// that INSERTs/UPDATEs/DELETEs a resource table breaches the boundary just as a
// read does — the resource context owns its write side.
func findResourceTableAccesses(t *testing.T, path string, owned map[string]bool) []string {
	t.Helper()
	var findings []string
	for _, lit := range goStringLiterals(t, path) {
		for _, name := range matchTableNames(sqlReadTables, lit.text) {
			if owned[name] {
				findings = append(findings, path+":"+strconv.Itoa(lit.line)+" reads resource-owned table "+name)
			}
		}
		for _, name := range matchTableNames(sqlWriteTables, lit.text) {
			if owned[name] {
				findings = append(findings, path+":"+strconv.Itoa(lit.line)+" writes resource-owned table "+name)
			}
		}
	}
	return findings
}

// TestHandoffReaderReadsNoResourceTable is the boundary guard: no production file
// of the version handoff adapter package may read a resource-owned table. The
// resources block is assembled in versionapp from resourceapp.HandoffResourceReader.
func TestHandoffReaderReadsNoResourceTable(t *testing.T) {
	owned := resourceOwnedTables(t)
	var findings []string
	for _, file := range versionpgProductionFiles(t) {
		findings = append(findings, findResourceTableAccesses(t, file, owned)...)
	}
	for _, finding := range findings {
		t.Errorf("H-B1 cross-context boundary: %s -- reach the resource context through resourceapp.HandoffResourceReader instead", finding)
	}
	if len(findings) > 0 {
		t.Fatalf("%d resource-table read(s) in the version handoff adapter package", len(findings))
	}
}

// TestHandoffBoundaryGuardFires proves the guard's properties on real bad samples
// (禁 born-green): a resource read fires, a version-owned read does not, a comment
// mentioning the table does not, and the write-side derivation really is what
// decides ownership (sut_repository is resource-owned despite its version_0010
// CREATE TABLE).
func TestHandoffBoundaryGuardFires(t *testing.T) {
	owned := resourceOwnedTables(t)
	if !owned["sut_repository"] {
		t.Fatal("sut_repository must be derived as resource-owned from the resource write side")
	}
	if owned["version_plan_item"] || owned["version_account"] || owned["requirement"] {
		t.Fatalf("version-owned tables leaked into the resource-owned set: %v", sortedKeys(owned))
	}

	write := func(t *testing.T, src string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "sample.go")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write sample: %v", err)
		}
		return path
	}

	// Every read shape the removed adapter used must fire.
	for _, src := range []string{
		"package p\nfunc f() { q := `SELECT id FROM sut WHERE environment_id = $1` }\n",
		"package p\nfunc f() { q := `SELECT a.id FROM version_account va JOIN test_account a ON a.id = va.account_id` }\n",
		"package p\nfunc f() { q := `SELECT DISTINCT ON (r.id) r.id FROM sut s JOIN sut_repository sr ON sr.sut_id = s.id JOIN code_repository r ON r.id = sr.repo_id` }\n",
		"package p\nfunc f() { q := `SELECT v.environment_id FROM version v LEFT JOIN environment e ON e.id = v.environment_id` }\n",
		"package p\nfunc f() { q := \"SELECT id FROM sut_page WHERE sut_id = $1\" }\n",
		// Quoted and schema-qualified spellings must not slip past the guard.
		"package p\nfunc f() { q := `SELECT id FROM \"sut\" WHERE id = $1` }\n",
		"package p\nfunc f() { q := `SELECT id FROM public.sut_page WHERE id = $1` }\n",
		"package p\nfunc f() { q := `WITH s AS (SELECT id FROM sut) SELECT * FROM s` }\n",
		// Write shapes breach the boundary too (T7 M3): the resource context owns
		// its write side, so an adapter INSERT/UPDATE/DELETE must fire.
		"package p\nfunc f() { q := `INSERT INTO sut (id, name) VALUES ($1, $2)` }\n",
		"package p\nfunc f() { q := `UPDATE sut SET status = $2 WHERE id = $1` }\n",
		"package p\nfunc f() { q := `DELETE FROM sut_page WHERE id = $1` }\n",
		"package p\nfunc f() { q := `INSERT INTO test_account (id) VALUES ($1)` }\n",
	} {
		if got := findResourceTableAccesses(t, write(t, src), owned); len(got) == 0 {
			t.Fatalf("guard did not fire on a real resource access: %s", src)
		}
	}

	// Version-owned reads AND writes stay legal (the adapter owns its own tables).
	clean := "package p\nfunc f() { q := `SELECT stage FROM version WHERE id = $1`; r := `SELECT id FROM version_plan_item`; s := `SELECT account_id FROM version_account WHERE enabled`; u := `UPDATE version SET stage = $2 WHERE id = $1`; v := `INSERT INTO version_account (version_id, account_id) VALUES ($1, $2)` }\n"
	if got := findResourceTableAccesses(t, write(t, clean), owned); len(got) != 0 {
		t.Fatalf("guard fired on version-owned accesses: %v", got)
	}

	// A comment is not a literal: documented counter-examples never fire.
	commented := "package p\n// The old adapter read FROM sut here; it must not come back.\nfunc f() {}\n"
	if got := findResourceTableAccesses(t, write(t, commented), owned); len(got) != 0 {
		t.Fatalf("guard fired on a comment: %v", got)
	}

	// A bare prose mention without a SQL table position is not a read.
	prose := "package p\nfunc f() error { return fmt.Errorf(\"sut scope is resolved by the resource port\") }\n"
	if got := findResourceTableAccesses(t, write(t, prose), owned); len(got) != 0 {
		t.Fatalf("guard fired on prose: %v", got)
	}
}
