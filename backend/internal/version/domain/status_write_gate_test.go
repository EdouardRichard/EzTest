package version_test

// V-0014 (B-share) consistency gate: the version status machine has exactly ONE
// sanctioned write entry per transition (constitution 三.6 终态唯一入口). The
// only production code allowed to write version.status is:
//
//   - Repository.Create            INSERT (initial row, status=initial)
//   - Repository.MarkRunning       UPDATE initial   -> running
//   - Repository.Close             UPDATE running   -> completed
//   - Repository.ReopenIfCompleted UPDATE completed -> running
//
// all in version/infrastructure/postgres.go. The legacy divergent reopen entry
// (Service.Reopen -> Repository.Reopen, which wrote status directly and published
// only the P3 version.status_changed projection, never version.reopened) was
// retired by T23b; the single production reopen writer is
// VersionCloseService.ReopenIfCompleted, which every trigger hook and the manual
// HTTP API delegate to.
//
// This is a real drift gate, not a documentation pin: it scans every non-test Go
// file under backend/internal and backend/cmd for SQL that writes the version
// table's status and asserts the found MULTISET equals the sanctioned set with
// each sanctioned key appearing EXACTLY ONCE. A new direct status write anywhere
// (any context, any method) is an extra site and fails the gate; a second status
// write co-located inside a sanctioned function (even before the sanctioned
// literal) is a multiplicity violation and fails it too — sites are counted, never
// deduplicated, so a same-key rogue write cannot hide behind the sanctioned one.
// A sanctioned write that disappears or stops being detected is a missing site
// and also fails it (so the gate cannot go vacuous). EVERY scanned UPDATE literal
// of a sanctioned entry must carry a WHERE status guard (the terminal CAS) — the
// check runs per literal, not per function, so it is order-independent. Test
// fixtures are excluded: seeding state in _test.go is legitimate.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// statusWriteSite is one production SQL site that writes version.status.
type statusWriteSite struct {
	file string // repo-relative slash path
	fn   string // enclosing function name
	kind string // "insert" or "update"
	sql  string // the matched SQL literal (for the CAS-guard check)
}

func (s statusWriteSite) key() string { return s.file + "|" + s.fn + "|" + s.kind }

// sanctionedStatusWrites is the exact allowlist of terminal-status write entries.
// The map value documents the transition each entry owns.
var sanctionedStatusWrites = map[string]string{
	"backend/internal/version/infrastructure/postgres.go|Create|insert":            "initial row creation (status=initial)",
	"backend/internal/version/infrastructure/postgres.go|MarkRunning|update":       "initial -> running",
	"backend/internal/version/infrastructure/postgres.go|Close|update":             "running -> completed",
	"backend/internal/version/infrastructure/postgres.go|ReopenIfCompleted|update": "completed -> running",
}

var (
	// insertVersionRe matches an INSERT into the version table (any column list).
	insertVersionRe = regexp.MustCompile(`(?i)insert\s+into\s+version\b`)
	// updateVersionSetRe captures the SET clause of an UPDATE on the version
	// table (optional alias), up to WHERE / RETURNING / end of statement.
	updateVersionSetRe = regexp.MustCompile(`(?is)update\s+version\s+(?:as\s+)?(?:[a-z_][a-z0-9_]*\s+)?set\s+(.*?)(?:\s+where\b|\s+returning\b|$)`)
	// statusAssignRe matches a status column assignment inside a SET clause.
	statusAssignRe = regexp.MustCompile(`(?i)(?:^|[,\s(])status\s*=`)
	// statusGuardRe matches a WHERE clause that pins status (the terminal CAS).
	statusGuardRe = regexp.MustCompile(`(?is)where\b.*\bstatus\s*=`)
)

// classifyVersionStatusWrite reports whether a SQL string writes version.status
// and returns its kind ("insert"/"update") plus the matched SQL.
func classifyVersionStatusWrite(sql string) (string, bool) {
	if insertVersionRe.MatchString(sql) {
		return "insert", true
	}
	if m := updateVersionSetRe.FindStringSubmatch(sql); m != nil {
		if statusAssignRe.MatchString(m[1]) {
			return "update", true
		}
	}
	return "", false
}

// TestVersionStatusSingleWriteEntry is the V-0014 drift gate: the multiset of
// production version.status write sites must equal the sanctioned allowlist with
// each sanctioned key appearing exactly once, and every scanned sanctioned
// UPDATE literal must carry a WHERE status CAS guard.
func TestVersionStatusSingleWriteEntry(t *testing.T) {
	root := repoRoot(t)
	sites := scanVersionStatusWrites(t, root)

	// Count sites per key. Do NOT deduplicate into a map[string]site: a second
	// status write co-located in a sanctioned function shares the same key, and
	// last-literal-wins dedup would silently drop it (evading both the extra-site
	// and the per-literal CAS check). Counting keeps every site observable.
	counts := make(map[string]int, len(sites))
	for _, site := range sites {
		counts[site.key()]++
	}

	// Extra sites: a direct status write outside the sanctioned entries.
	var extra []string
	for key := range counts {
		if _, ok := sanctionedStatusWrites[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		t.Errorf("V-0014: unsanctioned version.status write at %s (%d site(s)); status may only be written by Create/MarkRunning/Close/ReopenIfCompleted",
			key, counts[key])
	}

	// Multiplicity: each sanctioned key must appear EXACTLY once. More than one
	// status write in a sanctioned function is a co-located rogue write, even when
	// it precedes the sanctioned literal and shares its key.
	var duplicated []string
	for key := range sanctionedStatusWrites {
		if counts[key] > 1 {
			duplicated = append(duplicated, key)
		}
	}
	sort.Strings(duplicated)
	for _, key := range duplicated {
		t.Errorf("V-0014: sanctioned version.status write %q appears %d times; exactly one status write per sanctioned entry is allowed (co-located extra write)",
			key, counts[key])
	}

	// Missing sites: a sanctioned entry vanished or stopped being detected. The
	// gate fails closed rather than silently passing with an empty scan.
	var missing []string
	for key := range sanctionedStatusWrites {
		if counts[key] == 0 {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		t.Errorf("V-0014: sanctioned version.status write %q (%s) was not found; the gate cannot verify the single-entry invariant",
			key, sanctionedStatusWrites[key])
	}

	// CAS guard on EVERY scanned literal (not a deduped map): each sanctioned
	// UPDATE must pin status in its WHERE clause, independent of literal order.
	for _, site := range sites {
		if _, ok := sanctionedStatusWrites[site.key()]; !ok {
			continue
		}
		if site.kind == "update" && !statusGuardRe.MatchString(site.sql) {
			t.Errorf("V-0014: sanctioned write %s (func %s) has no WHERE status CAS guard", site.file, site.fn)
		}
	}
}

// scanVersionStatusWrites parses every non-test Go file under backend/internal
// and backend/cmd and collects the SQL literals that write version.status,
// tagged with their enclosing function.
func scanVersionStatusWrites(t *testing.T, root string) []statusWriteSite {
	t.Helper()
	var sites []statusWriteSite
	for _, sub := range []string{"backend/internal", "backend/cmd"} {
		base := filepath.Join(root, filepath.FromSlash(sub))
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			sites = append(sites, scanFileForStatusWrites(t, root, path)...)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	return sites
}

func scanFileForStatusWrites(t *testing.T, root, path string) []statusWriteSite {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("rel %s: %v", path, err)
	}
	rel = filepath.ToSlash(rel)

	var sites []statusWriteSite
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		sql, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		kind, isWrite := classifyVersionStatusWrite(sql)
		if !isWrite {
			return true
		}
		sites = append(sites, statusWriteSite{
			file: rel,
			fn:   enclosingFunc(file, lit.Pos()),
			kind: kind,
			sql:  sql,
		})
		return true
	})
	return sites
}

// enclosingFunc returns the name of the function whose body contains pos, or
// "<package>" for a package-level literal.
func enclosingFunc(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if pos >= fn.Body.Pos() && pos <= fn.Body.End() {
			return fn.Name.Name
		}
	}
	return "<package>"
}

// TestVersionLegacyReopenRetired pins the T23b retirement: the divergent legacy
// reopen entry must not come back. The version.Repository port, the postgres
// Repository and the version application Service must declare no Reopen method
// (the single reopen writer is VersionCloseService.ReopenIfCompleted). The
// requirement-closure Reopen lives in other files and is unrelated.
func TestVersionLegacyReopenRetired(t *testing.T) {
	root := repoRoot(t)

	// The version.Repository interface must not declare Reopen.
	repoFile := parseGateFile(t, root, "backend/internal/version/domain/repository.go")
	for _, decl := range repoFile.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Repository" {
				continue
			}
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}
			for _, method := range iface.Methods.List {
				for _, name := range method.Names {
					if name.Name == "Reopen" {
						t.Error("V-0014: version.Repository.Reopen was re-added; the legacy divergent reopen port must stay retired (use VersionCloseRepository.ReopenIfCompleted)")
					}
				}
			}
		}
	}

	// Neither the application Service nor the postgres Repository may declare a
	// Reopen method (these files have no other Reopen; requirement closure is
	// elsewhere).
	for _, rel := range []string{
		"backend/internal/version/application/service.go",
		"backend/internal/version/infrastructure/postgres.go",
	} {
		file := parseGateFile(t, root, rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "Reopen" {
				t.Errorf("V-0014: %s re-added a Reopen method; the single reopen writer is VersionCloseService.ReopenIfCompleted", rel)
			}
		}
	}
}

func parseGateFile(t *testing.T, root, rel string) *ast.File {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return file
}
