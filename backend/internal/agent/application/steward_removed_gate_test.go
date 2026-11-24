package agentapp_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// stewardGateRoot walks up from this test's source file to the repository root
// (the directory containing backend/go.mod), so the gate works regardless of the
// test's working directory.
func stewardGateRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate steward gate test source")
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
	t.Fatal("repo root containing backend/go.mod not found from " + file)
	return ""
}

// retiredCloseWorkVocabularyFile is the SINGLE production file allowed to carry
// the retired requirement.close/version.close work-type strings: the startup
// disposal vocabulary (GD-17 / SC-G10 existing-row disposal). The strings exist
// there only so in-flight legacy rows can be terminalized; they have no producer
// or consumer. The gate exempts exactly the RetiredCloseWorkTypes declaration in
// this file (fragment-level, not a value-level blanket exemption).
const retiredCloseWorkVocabularyFile = "backend/internal/agent/application/retired_close_work.go"

// TestStewardFullyRemoved is the GD-17 / SC-G10 no-dangling gate (constitution
// 三.7): the removed Version Steward profile, team room, close work types and
// steward tools must have ZERO residual production read/write. It scans every
// non-test Go file under backend/internal and backend/cmd for the forbidden
// identifiers and string literals. The only sanctioned occurrence of the retired
// close work-type strings is the startup disposal vocabulary declaration.
func TestStewardFullyRemoved(t *testing.T) {
	root := stewardGateRoot(t)

	forbiddenIdents := map[string]bool{
		"TeamSteward":              true,
		"ProfileKeyVersionSteward": true,
		"WorkTypeRequirementClose": true,
		"WorkTypeVersionClose":     true,
		"ToolRequirementGoalMet":   true,
		"ToolVersionCloseRead":     true,
		"ToolVersionClosePropose":  true,
	}
	retiredCloseTypes := map[string]bool{
		"requirement.close": true,
		"version.close":     true,
	}

	var hits []string
	exemptionHits := make(map[int]int)
	for _, sub := range []string{"backend/internal", "backend/cmd"} {
		base := filepath.Join(root, filepath.FromSlash(sub))
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", rel, parseErr)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.Ident:
					if forbiddenIdents[node.Name] {
						hits = append(hits, rel+":"+strconv.Itoa(fset.Position(node.Pos()).Line)+" identifier "+node.Name)
					}
				case *ast.BasicLit:
					if node.Kind != token.STRING {
						return true
					}
					value := strings.Trim(node.Value, "`\"")
					line := strconv.Itoa(fset.Position(node.Pos()).Line)
					// Substring matching (H-G4 T5): a suffixed resurrection such as
					// "version_steward_x" still carries the forbidden vocabulary and
					// must fire — exact whole-string matching was blind to it. Each
					// occurrence is exempt only when a sanctioned fragment covers it
					// (occurrence-scoped, fragment-level, with a reason).
					for _, forbidden := range stewardForbiddenHits(value, stewardGateExemptions, exemptionHits) {
						hits = append(hits, rel+":"+line+" literal "+value+" carries forbidden vocabulary "+forbidden)
					}
					if retiredCloseTypes[value] {
						if rel != retiredCloseWorkVocabularyFile || !inRetiredCloseWorkTypesDecl(file, node.Pos()) {
							hits = append(hits, rel+":"+line+" retired close work type "+value+" outside the disposal vocabulary")
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	// Non-vacuous, well-formed exemptions (lockstep): a stale or empty exemption
	// fails the gate so a repaid debt forces same-change-set cleanup.
	for i, ex := range stewardGateExemptions {
		if strings.TrimSpace(ex.fragment) == "" || strings.TrimSpace(ex.reason) == "" {
			t.Errorf("GD-17: steward gate exemption #%d has an empty fragment or reason", i)
		}
		if exemptionHits[i] == 0 {
			t.Errorf("GD-17: steward gate exemption %q (%s) sanctioned nothing; remove the stale exemption in the same change set", ex.fragment, ex.reason)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("GD-17: Steward/close residual production references found (%d):\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}

// stewardGateExemption sanctions the forbidden-vocabulary occurrences carried by
// fragment inside a string literal (occurrence-scoped, fragment-level). reason
// documents why those occurrences are not a Steward capability resurrection.
type stewardGateExemption struct {
	fragment string
	reason   string
}

// stewardGateExemptions: the only production literal that legitimately carries a
// forbidden value is the GD-17 startup disposal log, which describes the retired
// legacy rows being terminalized (the same sanctioned category as the disposal
// vocabulary declaration). A future legitimate collision adds a fragment that
// carries exactly its own occurrences, with a reason.
var stewardGateExemptions = []stewardGateExemption{
	{`retired steward close work item(s)`, "GD-17 startup disposal log (cmd/eztest/main.go): describes the retired legacy rows being terminalized, not a Steward capability"},
}

// stewardForbiddenHits returns the forbidden vocabulary values whose occurrences
// in text are NOT fully covered by a sanctioned fragment, recording which
// exemption indices sanctioned at least one occurrence (non-vacuous check). A
// value is exempt only when EVERY occurrence of it in the literal is spanned by
// some fragment; a rogue occurrence sharing the literal with a sanctioned one is
// still flagged (occurrence-scoped, position coverage — same semantics as the
// status vocabulary gates).
func stewardForbiddenHits(text string, exemptions []stewardGateExemption, hits map[int]int) []string {
	var found []string
	for forbidden := range stewardForbiddenVocabulary {
		starts := stewardAllIndices(text, forbidden)
		if len(starts) == 0 {
			continue
		}
		covered := make([]bool, len(starts))
		for i, ex := range exemptions {
			if ex.fragment == "" || !strings.Contains(ex.fragment, forbidden) {
				continue
			}
			fragHit := false
			for _, fs := range stewardAllIndices(text, ex.fragment) {
				fe := fs + len(ex.fragment)
				for j, ts := range starts {
					if ts >= fs && ts+len(forbidden) <= fe {
						covered[j] = true
						fragHit = true
					}
				}
			}
			if fragHit {
				hits[i]++
			}
		}
		for _, c := range covered {
			if !c {
				found = append(found, forbidden)
				break
			}
		}
	}
	return found
}

// stewardForbiddenVocabulary is the forbidden literal vocabulary the gate scans
// for (substring matching). It is the single source shared by the real-file gate
// and the synthetic proof below.
var stewardForbiddenVocabulary = map[string]bool{
	"version_steward":       true,
	"steward":               true,
	"requirement.goal_met":  true,
	"version.close_read":    true,
	"version.close_propose": true,
}

// stewardAllIndices delegates to the shared substring-index authority
// (platform/vocablint). It used to be a sixth local copy of the same algorithm
// the vocabulary gates kept (T11e-0, constitution 五.1/五.3); the semantics are
// unchanged — every start index of sub in s, advancing one byte past each match
// so overlapping matches are all found.
func stewardAllIndices(s, sub string) []int {
	return vocablint.AllIndices(s, sub)
}

// TestStewardGateSubstringAndExemption is permanent deterministic proof of the
// H-G4 T5 hardening: a suffixed resurrection fires (exact matching was blind),
// the sanctioned disposal-log literal is exempt, and a rogue occurrence sharing
// a literal with a sanctioned one is still flagged.
func TestStewardGateSubstringAndExemption(t *testing.T) {
	hits := make(map[int]int)
	// Suffixed resurrection: both "version_steward" and its "steward" substring fire.
	got := stewardForbiddenHits("version_steward_x", stewardGateExemptions, hits)
	if len(got) == 0 {
		t.Fatal("substring resurrection version_steward_x did not fire; the gate is blind to suffixed variants")
	}
	// The sanctioned disposal-log literal is exempt for "steward".
	hits = make(map[int]int)
	disposal := "eztest: disposed %d retired steward close work item(s) (GD-17 deterministic closure)"
	if got := stewardForbiddenHits(disposal, stewardGateExemptions, hits); len(got) != 0 {
		t.Fatalf("sanctioned disposal log literal flagged: %v", got)
	}
	if hits[0] == 0 {
		t.Fatal("disposal-log exemption sanctioned nothing; the non-vacuous check would fail")
	}
	// A rogue occurrence sharing the literal with the sanctioned fragment fires.
	hits = make(map[int]int)
	mixed := "retired steward close work item(s) and version_steward reborn"
	got = stewardForbiddenHits(mixed, stewardGateExemptions, hits)
	if len(got) == 0 {
		t.Fatal("rogue version_steward hidden behind a sanctioned fragment in the same literal")
	}
	// An unrelated literal is clean.
	hits = make(map[int]int)
	if got := stewardForbiddenHits("executor session ready", stewardGateExemptions, hits); len(got) != 0 {
		t.Fatalf("unrelated literal flagged: %v", got)
	}
}

// inRetiredCloseWorkTypesDecl reports whether pos lies inside the package-level
// RetiredCloseWorkTypes variable declaration (the sanctioned disposal vocabulary).
func inRetiredCloseWorkTypesDecl(file *ast.File, pos token.Pos) bool {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range value.Names {
				if name.Name == "RetiredCloseWorkTypes" && pos >= value.Pos() && pos <= value.End() {
					return true
				}
			}
		}
	}
	return false
}

// TestProfileNetCount11 pins design §1.4 (SC-G10 M1): after removing
// version_steward and adding design_judge (E), the seed profile catalog nets 11.
func TestProfileNetCount11(t *testing.T) {
	profiles := agentapp.Profiles()
	if len(profiles) != 11 {
		keys := make([]string, 0, len(profiles))
		for _, p := range profiles {
			keys = append(keys, p.Key)
		}
		t.Fatalf("seed profile count = %d, want 11 (original 11 - version_steward + design_judge): %v",
			len(profiles), keys)
	}
	hasJudge := false
	for _, p := range profiles {
		if p.Key == "version_steward" {
			t.Fatal("version_steward profile is still seeded")
		}
		if p.Key == "design_judge" {
			hasJudge = true
		}
	}
	if !hasJudge {
		t.Fatal("design_judge profile is missing (E increment)")
	}
}

// TestRetiredCloseWorkTypesFailClosed pins that the retired close work types no
// longer resolve to any profile: an upgraded in-flight row that is somehow
// claimed fails closed (invalid_work_type) instead of running a deleted Steward.
func TestRetiredCloseWorkTypesFailClosed(t *testing.T) {
	for _, workType := range []string{"requirement.close", "version.close"} {
		if key, ok := agentapp.ProfileKeyForWorkType(workType); ok {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q, want fail-closed (retired GD-17 work type)", workType, key)
		}
	}
}
