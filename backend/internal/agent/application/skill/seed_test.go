package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
)

// writeSkillFile lays out one skills/<name>/SKILL.md seed package.
func writeSkillFile(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestParseSkillMarkdown(t *testing.T) {
	raw := "---\nname: api-recon\ndescription: Probe an HTTP API surface\n---\n# API recon\n\nStep one.\n"
	skill, err := skillapp.ParseSkillMarkdown("api-recon", []byte(raw))
	if err != nil {
		t.Fatalf("ParseSkillMarkdown: %v", err)
	}
	if skill.Name != "api-recon" || skill.Description != "Probe an HTTP API surface" {
		t.Fatalf("parsed = %#v", skill)
	}
	if skill.Body != "# API recon\n\nStep one." {
		t.Fatalf("body = %q", skill.Body)
	}
	if !skill.Enabled {
		t.Fatal("seeded skills must default to enabled")
	}
}

func TestParseSkillMarkdownAcceptsCRLF(t *testing.T) {
	raw := "---\r\nname: api-recon\r\ndescription: d\r\n---\r\nbody line\r\n"
	skill, err := skillapp.ParseSkillMarkdown("api-recon", []byte(raw))
	if err != nil {
		t.Fatalf("CRLF seed rejected: %v", err)
	}
	if skill.Body != "body line" {
		t.Fatalf("body = %q", skill.Body)
	}
}

func TestParseSkillMarkdownFailsClosed(t *testing.T) {
	cases := map[string]string{
		"no frontmatter":        "# just markdown\n",
		"unterminated":          "---\nname: api-recon\ndescription: d\n",
		"missing name":          "---\ndescription: d\n---\nbody\n",
		"missing description":   "---\nname: api-recon\n---\nbody\n",
		"name mismatch":         "---\nname: other\ndescription: d\n---\nbody\n",
		"unknown key":           "---\nname: api-recon\ndescription: d\nextra: x\n---\nbody\n",
		"empty body":            "---\nname: api-recon\ndescription: d\n---\n\n",
		"invalid name":          "---\nname: Bad_Name!\ndescription: d\n---\nbody\n",
		"duplicate frontmatter": "---\nname: api-recon\nname: twice\ndescription: d\n---\nbody\n",
	}
	for label, raw := range cases {
		dirName := "api-recon"
		if label == "invalid name" {
			dirName = "Bad_Name!"
		}
		if _, err := skillapp.ParseSkillMarkdown(dirName, []byte(raw)); err == nil {
			t.Fatalf("%s: accepted", label)
		}
	}
}

func TestLoadSeedSkillsSortsAndRequiresSkillFile(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "zeta-skill", "---\nname: zeta-skill\ndescription: z\n---\nz body\n")
	writeSkillFile(t, dir, "alpha-skill", "---\nname: alpha-skill\ndescription: a\n---\na body\n")
	// Loose files at the root (e.g. README.md) are not packages.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("docs"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, err := skillapp.LoadSeedSkills(dir)
	if err != nil {
		t.Fatalf("LoadSeedSkills: %v", err)
	}
	if len(skills) != 2 || skills[0].Name != "alpha-skill" || skills[1].Name != "zeta-skill" {
		t.Fatalf("skills = %#v, want sorted [alpha-skill, zeta-skill]", skills)
	}

	// A package directory without SKILL.md fails closed instead of skipping.
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := skillapp.LoadSeedSkills(dir); err == nil {
		t.Fatal("directory without SKILL.md accepted silently")
	}
}

/* --------------------- R-C11: body never hardcoded in Go ------------------ */

// goSourcesWithText reports whether any non-test .go file under root contains
// text. It is the mechanism behind the "skill bodies live only in skills/
// files and the DB" assertion.
func goSourcesWithText(t *testing.T, root, text string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), text) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}

// TestSkillBodiesNotInGoSources is the R-C11 grep assertion: every SKILL.md
// body shipped in skills/ must be absent from production Go source. Skill
// content lives only in the seed files and the runtime DB.
func TestSkillBodiesNotInGoSources(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..", "..", "..")
	skillsDir := filepath.Join(repoRoot, "skills")
	agentDir := filepath.Join(repoRoot, "backend", "internal", "agent")
	if _, err := os.Stat(skillsDir); err != nil {
		t.Fatalf("skills/ seed directory missing: %v", err)
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("read skills/: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(skillsDir, entry.Name(), "SKILL.md"))
		if err != nil {
			t.Fatalf("read %s/SKILL.md: %v", entry.Name(), err)
		}
		parsed, err := skillapp.ParseSkillMarkdown(entry.Name(), raw)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		if hits := goSourcesWithText(t, agentDir, parsed.Body); len(hits) > 0 {
			t.Fatalf("skill %q body is hardcoded in Go source: %v", entry.Name(), hits)
		}
	}
}

// TestGoSourceScanDetectsHardcodedBody is the positive control: the scan above
// must actually fire when a body IS present in Go source (a guard that has
// never failed on a real bad sample proves nothing).
func TestGoSourceScanDetectsHardcodedBody(t *testing.T) {
	dir := t.TempDir()
	marker := "UNIQUE-HARDCODED-SKILL-BODY-9f2c"
	if err := os.WriteFile(filepath.Join(dir, "hardcoded.go"), []byte("package x\n\nconst body = \""+marker+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hardcoded_test.go"), []byte("package x\n// "+marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := goSourcesWithText(t, dir, marker)
	if len(hits) != 1 || !strings.HasSuffix(hits[0], "hardcoded.go") {
		t.Fatalf("hits = %v, want exactly the production file", hits)
	}
}

// TestSeedReadsFilesNotConstants proves the seed path consumes file content:
// changing the file and seeding into a fresh store yields the new content
// without touching any Go code.
func TestSeedReadsFilesNotConstants(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "file-driven", "---\nname: file-driven\ndescription: d\n---\nfirst content\n")
	repo := newFakeRepo()
	svc := skillapp.NewService(repo, nil)
	if _, err := svc.Seed(context.Background(), dir); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, err := svc.Get(context.Background(), "file-driven")
	if err != nil || got.Body != "first content" {
		t.Fatalf("Get = %#v, %v", got, err)
	}

	writeSkillFile(t, dir, "file-driven", "---\nname: file-driven\ndescription: d\n---\nsecond content\n")
	fresh := skillapp.NewService(newFakeRepo(), nil)
	if _, err := fresh.Seed(context.Background(), dir); err != nil {
		t.Fatalf("Seed fresh: %v", err)
	}
	got, err = fresh.Get(context.Background(), "file-driven")
	if err != nil || got.Body != "second content" {
		t.Fatalf("fresh Get = %#v, %v; seed must read the file, not a constant", got, err)
	}
}
