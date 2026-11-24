package skill_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
)

// repoSkillsDir resolves the repository-root skills/ seed directory from this
// package (backend/internal/agent/application/skill → five levels up).
func repoSkillsDir() string {
	return filepath.Join("..", "..", "..", "..", "..", "skills")
}

// TestBuiltinSkillPackagesLoad pins the two built-in packages (SC-C12): the
// repository ships skills/playwright-cli and skills/api-recon as valid SKILL.md
// packages, and seeding them into a fresh runtime library makes their bodies
// readable on demand. The bodies are the ARTEX applicable subset rewritten for
// EzTest (R-C10/R-C13); pentest-specific content is out of scope.
func TestBuiltinSkillPackagesLoad(t *testing.T) {
	seeds, err := skillapp.LoadSeedSkills(repoSkillsDir())
	if err != nil {
		t.Fatalf("LoadSeedSkills(skills/): %v", err)
	}
	byName := map[string]bool{}
	for _, seed := range seeds {
		byName[seed.Name] = true
		if strings.TrimSpace(seed.Description) == "" {
			t.Fatalf("built-in package %q has an empty description (metadata layer)", seed.Name)
		}
		if strings.TrimSpace(seed.Body) == "" {
			t.Fatalf("built-in package %q has an empty body", seed.Name)
		}
	}
	for _, want := range []string{"playwright-cli", "api-recon"} {
		if !byName[want] {
			t.Fatalf("built-in package %q is missing from skills/ (loaded: %v)", want, byName)
		}
	}

	// Seeded packages are readable through the runtime library surface.
	svc := skillapp.NewService(newFakeRepo(), nil)
	if _, err := svc.Seed(context.Background(), repoSkillsDir()); err != nil {
		t.Fatalf("Seed(skills/): %v", err)
	}
	cli, err := svc.Get(context.Background(), "playwright-cli")
	if err != nil {
		t.Fatalf("Get(playwright-cli): %v", err)
	}
	// The CLI skill teaches the browser.cli passthrough and the run-code escape
	// valve (T1 measured command face), not a fixed action enumeration.
	for _, marker := range []string{"browser.cli", "snapshot", "run-code"} {
		if !strings.Contains(cli.Body, marker) {
			t.Fatalf("playwright-cli body does not teach %q", marker)
		}
	}
	recon, err := svc.Get(context.Background(), "api-recon")
	if err != nil {
		t.Fatalf("Get(api-recon): %v", err)
	}
	if !strings.Contains(recon.Body, "http.request") {
		t.Fatal("api-recon body does not teach the http.request workflow")
	}
}
