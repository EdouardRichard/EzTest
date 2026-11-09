package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestActionsCatalogIsUniqueAndWellFormed(t *testing.T) {
	if len(Actions) < 50 {
		t.Fatalf("catalog has %d actions, want the full audit surface", len(Actions))
	}
	names := make(map[string]bool, len(Actions))
	aliases := make(map[string]bool)
	for _, action := range Actions {
		if action.Name == "" || action.Label == "" {
			t.Fatalf("action %+v is missing name/label", action)
		}
		if names[action.Name] {
			t.Fatalf("duplicate action %q", action.Name)
		}
		names[action.Name] = true
		for _, alias := range action.Aliases {
			if alias == "" || alias == action.Name {
				t.Fatalf("action %q has an empty/self alias %q", action.Name, alias)
			}
			if aliases[alias] {
				t.Fatalf("duplicate alias %q", alias)
			}
			aliases[alias] = true
		}
	}
	for _, prefix := range []string{
		"auth.", "user.", "settings.", "llm.", "resource.", "version.", "agent.",
	} {
		found := false
		for name := range names {
			if strings.HasPrefix(name, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("catalog has no %q action", prefix)
		}
	}
}

func TestHasAction(t *testing.T) {
	for _, name := range []string{
		"auth.login", "settings.updated", "version.stage_decision", "agent.run.failed",
	} {
		if !HasAction(name) {
			t.Fatalf("HasAction(%q) = false, want true", name)
		}
	}
	if HasAction("settings.does_not_exist") {
		t.Fatal("HasAction accepted an unknown action")
	}
}

func TestResolveAlias(t *testing.T) {
	if got := ResolveAlias("setting.update"); got != "settings.updated" {
		t.Fatalf("ResolveAlias(setting.update) = %q, want settings.updated", got)
	}
	if got := ResolveAlias("SETTING.UPDATE"); got != "settings.updated" {
		t.Fatalf("alias resolution must be case-insensitive, got %q", got)
	}
	if got := ResolveAlias("settings.updated"); got != "settings.updated" {
		t.Fatalf("canonical action changed to %q", got)
	}
	if got := ResolveAlias("auth.log"); got != "auth.log" {
		t.Fatalf("unknown action changed to %q", got)
	}
}

// TestFrontendCatalogMatchesBackend keeps the static frontend candidate list in
// lockstep with this catalog. There is deliberately no candidate endpoint, so a
// drift here would silently hide actions from the AuditPanel datalist.
func TestFrontendCatalogMatchesBackend(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "frontend", "src", "features", "settings", "audit-actions.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frontend audit catalog: %v", err)
	}
	pattern := regexp.MustCompile(`\{\s*name:\s*"([^"]+)",\s*label:\s*"([^"]+)",\s*aliases:\s*\[([^\]]*)\]\s*\}`)
	matches := pattern.FindAllStringSubmatch(string(raw), -1)
	if len(matches) != len(Actions) {
		t.Fatalf("frontend catalog has %d actions, backend has %d", len(matches), len(Actions))
	}
	aliasPattern := regexp.MustCompile(`"([^"]+)"`)
	for i, match := range matches {
		name, label, rawAliases := match[1], match[2], match[3]
		want := Actions[i]
		if name != want.Name || label != want.Label {
			t.Fatalf("entry %d = (%q, %q), want (%q, %q)", i, name, label, want.Name, want.Label)
		}
		gotAliases := aliasPattern.FindAllStringSubmatch(rawAliases, -1)
		if len(gotAliases) != len(want.Aliases) {
			t.Fatalf("%q aliases = %v, want %v", name, rawAliases, want.Aliases)
		}
		for j, alias := range gotAliases {
			if alias[1] != want.Aliases[j] {
				t.Fatalf("%q alias %d = %q, want %q", name, j, alias[1], want.Aliases[j])
			}
		}
	}
}
