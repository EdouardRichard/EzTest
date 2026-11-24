package agentapp

// V-0017 team-map single-source gate (v0.2.4-H H-A3, constitution art.5; T8 fix
// round 1 Important #1 — team-side discrimination closed, method discipline ⑧).
//
// Two profile→team maps exist by design and are DIFFERENT concepts (R-H9 pure
// vocabulary reuse recognition):
//   - teamForProfile (runner.go) selects the COLLABORATION TEAM ROOM a profile
//     posts to (agent.Team: design / execution / delivery / clarification); it
//     drives work-item dispatch and team-room posts.
//   - agentCenterTeams (agent_center.go) is the AGENT CENTER UI CATALOG grouping
//     (conversation / execution) — a display projection, not the dispatch team.
//
// Both enumerate profile keys by hand, so a newly seeded profile could be
// forgotten in one map (silent drift). This gate derives the authoritative profile
// key set from Profiles() (the seed registry, single source) and asserts BOTH maps
// cover exactly that set, with REAL discrimination on each side:
//
//   - catalog side: every seeded profile has an agentCenterTeams entry
//     (catalogTeamForProfile ok=true), every catalog key is seeded, no duplicates,
//     and the catalog count equals the seed count.
//   - team side: teamForProfile has a `default: return agent.TeamClarification`,
//     so merely calling it can NEVER detect a profile forgotten by the switch (it
//     silently lands in the clarification room). Instead this gate AST-enumerates
//     the switch's explicit case keys (resolved from the package's ProfileKey*
//     string constants) and requires every seeded profile to be EITHER an explicit
//     case key OR a member of the clarification-default allowlist below — exact
//     coverage, no overlap. A new seeded profile that the switch forgets AND that
//     is not allowlisted fails here; deleting a case key drops it from the explicit
//     set and (not being allowlisted) fails here too (team-side mutation-verified).
//
// The clarification-default allowlist is fragment-level (per key, with a reason),
// non-empty validated, and stale-key checked: it is a conscious classification of
// the profiles that intentionally keep the clarification default, NOT a blanket
// value-level pass. The default itself is intentional production behavior (the
// switch comment: "every P1 profile keeps the clarification team room"); this gate
// does not change it, only makes the coverage claim match the mechanism (art.5.12).
//
// Invoker chain (discipline ⑪): CI `test` job → `go test -count=1 ./...` → this
// test (agent/application).

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

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// clarificationDefaultProfiles is the fragment-level allowlist of seeded profiles
// that intentionally resolve to the clarification team room via teamForProfile's
// default branch (i.e. are NOT in an explicit switch case). Each entry carries a
// reason; the gate validates the list is non-empty, every key is a seeded profile,
// and no key also appears in an explicit case (overlap = stale classification).
var clarificationDefaultProfiles = map[string]string{
	ProfileKeyClarificationHost: "P1 clarification Host: the clarification team room is its native channel (teamForProfile default by design).",
	ProfileKeyDesignJudge:       "independent design judge: deliberately kept out of the design-room case for judge independence; clarification default consciously accepted (production behavior unchanged, T8 fix round 1).",
	ProfileKeyKnowledgeCurator:  "P7 knowledge_curator role profile: no dedicated team room; clarification default by design.",
}

func TestProfileTeamMapsCoverSeededProfiles(t *testing.T) {
	profiles := Profiles()
	if len(profiles) == 0 {
		t.Fatal("Profiles() returned no seeded profiles")
	}
	seeded := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		if seeded[p.Key] {
			t.Fatalf("Profiles() has duplicate key %q", p.Key)
		}
		seeded[p.Key] = true
	}

	// ---- catalog side (agentCenterTeams): exact coverage of the seed set ----
	for key := range seeded {
		if _, _, ok := catalogTeamForProfile(key); !ok {
			t.Errorf("seeded profile %q has no agentCenterTeams catalog entry (UI grouping drift)", key)
		}
	}
	catalogKeys := make(map[string]bool)
	for _, def := range agentCenterTeams {
		for _, key := range def.keys {
			if !seeded[key] {
				t.Errorf("agentCenterTeams lists %q which is not a seeded profile (stale catalog entry)", key)
			}
			if catalogKeys[key] {
				t.Errorf("agentCenterTeams lists %q twice", key)
			}
			catalogKeys[key] = true
		}
	}
	if len(catalogKeys) != len(seeded) {
		t.Errorf("agentCenterTeams covers %d profiles, seed registry has %d (coverage drift)", len(catalogKeys), len(seeded))
	}

	// ---- team side (teamForProfile): AST explicit-case keys + allowlist == seed set ----
	caseKeys := teamForProfileExplicitCaseKeys(t)
	if len(caseKeys) == 0 {
		t.Fatal("AST found no explicit case keys in teamForProfile (gate would be vacuous)")
	}
	if len(clarificationDefaultProfiles) == 0 {
		t.Fatal("clarification-default allowlist is empty (non-empty validation)")
	}
	// Every explicit case key must be a seeded profile (no orphan case), and must
	// not also be allowlisted as clarification-default (overlap = stale class).
	for key := range caseKeys {
		if !seeded[key] {
			t.Errorf("teamForProfile explicit case key %q is not a seeded profile (orphan case)", key)
		}
		if _, overlap := clarificationDefaultProfiles[key]; overlap {
			t.Errorf("profile %q is both an explicit teamForProfile case and clarification-default allowlisted (stale classification)", key)
		}
	}
	// Every allowlisted key must be seeded and carry a non-empty reason.
	for key, reason := range clarificationDefaultProfiles {
		if !seeded[key] {
			t.Errorf("clarification-default allowlist key %q is not a seeded profile (stale allowlist entry)", key)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("clarification-default allowlist key %q has no reason", key)
		}
	}
	// Exact coverage: explicit case keys UNION allowlist == seed set. A seeded
	// profile in neither set is a forgotten classification (the silent-default
	// defect this gate exists to catch); a key in the union but not seeded is an
	// orphan. This is the team-side discrimination the default branch alone lacks.
	for key := range seeded {
		_, inCase := caseKeys[key]
		_, inDefault := clarificationDefaultProfiles[key]
		if !inCase && !inDefault {
			t.Errorf("seeded profile %q is neither an explicit teamForProfile case nor clarification-default allowlisted: it would silently fall to the clarification room (team-side漏配)", key)
		}
		if team := teamForProfile(key); team == "" {
			t.Errorf("seeded profile %q has no collaboration team", key)
		} else if !inCase && team != agent.TeamClarification {
			// An allowlisted default profile must actually resolve to clarification.
			t.Errorf("allowlisted clarification-default profile %q resolves to %q, not clarification", key, team)
		}
	}

	// The collaboration team map must only emit valid domain teams.
	for key := range seeded {
		switch team := teamForProfile(key); team {
		case agent.TeamDesign, agent.TeamExecution, agent.TeamDelivery, agent.TeamClarification:
		default:
			t.Errorf("teamForProfile(%q) = %q, not a valid agent.Team", key, team)
		}
	}
}

// teamForProfileExplicitCaseKeys AST-parses the agent/application package, resolves
// the ProfileKey* string constants, and returns the set of profile-key VALUES that
// appear as explicit case labels in teamForProfile's switch (excluding the default
// branch). Resolution is value-derived: a case label that is not a resolvable
// string constant fails loud rather than being silently skipped (discipline ⑤/⑧).
func teamForProfileExplicitCaseKeys(t *testing.T) map[string]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate profile_team_consistency_test.go")
	}
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	constValues := map[string]string{}
	var files []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		files = append(files, f)
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					constValues[name.Name] = value
				}
			}
		}
	}

	keys := map[string]bool{}
	found := false
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "teamForProfile" || fn.Body == nil {
				continue
			}
			found = true
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sw, ok := n.(*ast.SwitchStmt)
				if !ok {
					return true
				}
				for _, stmt := range sw.Body.List {
					cc, ok := stmt.(*ast.CaseClause)
					if !ok || cc.List == nil {
						continue // default branch: handled by the allowlist
					}
					for _, expr := range cc.List {
						switch e := expr.(type) {
						case *ast.Ident:
							value, resolved := constValues[e.Name]
							if !resolved {
								t.Fatalf("teamForProfile case label %s is not a resolvable string constant (loud, not skipped)", e.Name)
							}
							keys[value] = true
						case *ast.BasicLit:
							if e.Kind == token.STRING {
								value, err := strconv.Unquote(e.Value)
								if err == nil {
									keys[value] = true
								}
							}
						default:
							t.Fatalf("teamForProfile case label has unsupported expression %T (loud, not skipped)", expr)
						}
					}
				}
				return true
			})
		}
	}
	if !found {
		t.Fatal("teamForProfile function not found by AST")
	}
	return keys
}
