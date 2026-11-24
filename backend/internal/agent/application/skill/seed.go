package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// SeedResult reports what one seeding pass did. Skipped names already exist
// in the runtime library (admin edits and deletions are never overwritten).
type SeedResult struct {
	Seeded  []string
	Skipped []string
}

// Seed reads every skills/<name>/SKILL.md package under dir and inserts the
// ones the runtime library has never seen. The DB is authoritative: existing
// rows — including administrator edits and deletions — are left untouched
// (EnsureProfiles precedent). Every file is parsed and validated before the
// first write so a broken package fails the pass closed.
func (s *Service) Seed(ctx context.Context, dir string) (SeedResult, error) {
	if s == nil || s.repo == nil {
		return SeedResult{}, platform.Unavailable("skill repository is not configured")
	}
	seeds, err := LoadSeedSkills(dir)
	if err != nil {
		return SeedResult{}, err
	}
	result := SeedResult{Seeded: []string{}, Skipped: []string{}}
	for _, seed := range seeds {
		_, created, err := s.repo.EnsureSkill(ctx, seed)
		if err != nil {
			return SeedResult{}, fmt.Errorf("seed skill %q: %w", seed.Name, err)
		}
		if created {
			result.Seeded = append(result.Seeded, seed.Name)
		} else {
			result.Skipped = append(result.Skipped, seed.Name)
		}
	}
	return result, nil
}

// LoadSeedSkills reads and validates every package directory under dir,
// sorted by name. Loose files (e.g. README.md) are ignored; a package
// directory without SKILL.md is an error, not a silent skip.
func LoadSeedSkills(dir string) ([]agent.Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills seed directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	skills := make([]agent.Skill, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("skill package %q: %w", name, err)
		}
		parsed, err := ParseSkillMarkdown(name, raw)
		if err != nil {
			return nil, fmt.Errorf("skill package %q: %w", name, err)
		}
		skills = append(skills, parsed)
	}
	return skills, nil
}

// ParseSkillMarkdown parses one SKILL.md seed file: a YAML-style frontmatter
// block with exactly name and description, then the markdown body. The
// frontmatter name must equal the package directory name (single source for
// the skill identity). Content is data, never code: nothing here is embedded
// into the binary.
func ParseSkillMarkdown(dirName string, data []byte) (agent.Skill, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return agent.Skill{}, fmt.Errorf("SKILL.md must start with a --- frontmatter block")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	bodyStart := end + len("\n---\n")
	if end < 0 {
		// A closing fence at EOF without a trailing newline is still a fence.
		if strings.HasSuffix(rest, "\n---") {
			end = len(rest) - len("\n---")
			bodyStart = len(rest)
		} else {
			return agent.Skill{}, fmt.Errorf("SKILL.md frontmatter is not terminated by ---")
		}
	}
	frontmatter := rest[:end]
	body := strings.TrimSpace(rest[bodyStart:])

	var name, description string
	seen := map[string]bool{}
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return agent.Skill{}, fmt.Errorf("frontmatter line %q is not key: value", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seen[key] {
			return agent.Skill{}, fmt.Errorf("duplicate frontmatter key %q", key)
		}
		seen[key] = true
		switch key {
		case "name":
			name = value
		case "description":
			description = value
		default:
			return agent.Skill{}, fmt.Errorf("unknown frontmatter key %q (only name and description are supported)", key)
		}
	}
	if name == "" {
		return agent.Skill{}, fmt.Errorf("frontmatter name is required")
	}
	if description == "" {
		return agent.Skill{}, fmt.Errorf("frontmatter description is required")
	}
	if name != dirName {
		return agent.Skill{}, fmt.Errorf("frontmatter name %q does not match package directory %q", name, dirName)
	}
	skill := agent.Skill{
		Name:        name,
		Description: description,
		Body:        body,
		Enabled:     true,
	}
	if err := validateSkillContent(skill); err != nil {
		return agent.Skill{}, err
	}
	return skill, nil
}
