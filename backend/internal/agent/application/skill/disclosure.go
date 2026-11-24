package skill

import (
	"context"
	"sort"
	"strings"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Progressive disclosure (v0.2.4-C T19, SC-C12, ARTEX T15 model): the run
// context carries only the METADATA layer of every enabled skill package
// (name + description, resident in the pinned facts so compaction never drops
// it); the BODY is read on demand through the skill.read tool. This keeps a
// 32k-rune SKILL.md body out of every prompt until the model actually needs it.
// Every read outcome — hit or miss — appends one skill_usage fact row.

// Metadata is the resident metadata layer of one skill package. It carries no
// body by construction: the disclosure path strips the body before the context
// assembler ever sees it (R-C11 bodies live only in skills/ files and the DB).
type Metadata struct {
	Name        string
	Description string
}

// Metadata returns the metadata layer of every enabled skill, sorted by name.
// Disabled and deleted packages are excluded (List already hides deletes).
func (s *Service) Metadata(ctx context.Context) ([]Metadata, error) {
	skills, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	metas := make([]Metadata, 0, len(skills))
	for _, skill := range skills {
		if !skill.Enabled {
			continue
		}
		metas = append(metas, Metadata{Name: skill.Name, Description: skill.Description})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return metas, nil
}

// RenderMetadata renders the resident skill context section: one
// name/description line per package plus the on-demand read pointer. An empty
// set renders no section at all (knowledge precedent: no empty heading).
func RenderMetadata(metas []Metadata) string {
	if len(metas) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# 技能\n")
	builder.WriteString("以下技能包可用（仅元数据层）。需要某技能的操作方法时调用 skill.read(name) 按需读取正文，再按正文执行；不要凭记忆猜测技能内容。技能是便捷知识不是能力上限，未覆盖的用通用执行通道。\n")
	for _, meta := range metas {
		builder.WriteString("- ")
		builder.WriteString(meta.Name)
		if description := strings.TrimSpace(meta.Description); description != "" {
			builder.WriteString(": ")
			builder.WriteString(description)
		}
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

// ReadForAgent is the skill.read on-demand body path: only ENABLED packages
// are served. A disabled package is a miss (found=false, no error) — the
// resident metadata layer already excludes disabled packages, so serving
// their bodies would contradict the disclosure contract. An unknown name
// surfaces Get's not_found error (the tool adapter maps it to a miss); an
// invalid name shape stays an argument error.
func (s *Service) ReadForAgent(ctx context.Context, name string) (agent.Skill, bool, error) {
	skill, err := s.Get(ctx, name)
	if err != nil {
		return agent.Skill{}, false, err
	}
	if !skill.Enabled {
		return agent.Skill{}, false, nil
	}
	return skill, true, nil
}

/* ----------------------------- skill_usage ledger ------------------------- */

// UsageRepository is the persistence port of the append-only skill_usage
// ledger. *agentpg.SkillRepository implements it; a Repository without this
// surface fails closed (the ledger is never half-wired).
type UsageRepository interface {
	// RecordSkillUsage appends one found/miss fact for the run and returns the
	// assigned per-run seq. The (run_id, seq) unique constraint makes a
	// replayed write a no-op.
	RecordSkillUsage(ctx context.Context, runID int64, skillName string, found bool) (int, error)
	// ListSkillUsage returns the run's fact rows in seq order (production read
	// side: the admin usage API).
	ListSkillUsage(ctx context.Context, runID int64) ([]agent.SkillUsage, error)
}

func (s *Service) usageRepo() (UsageRepository, error) {
	if s == nil || s.repo == nil {
		return nil, platform.Unavailable("skill repository is not configured")
	}
	repo, ok := s.repo.(UsageRepository)
	if !ok {
		return nil, platform.Unavailable("skill usage ledger is not configured")
	}
	return repo, nil
}

// RecordUsage appends one skill_usage fact for one skill.read outcome. The
// caller (the skill.read tool) treats a failure as a retryable tool error:
// a read whose fact could not be recorded never returns as if accounted for
// (constitution art.7.6, no silent degradation).
func (s *Service) RecordUsage(ctx context.Context, runID int64, skillName string, found bool) error {
	if runID <= 0 {
		return platform.Invalid("run_id", "must be a positive agent run id")
	}
	if err := agent.ValidateSkillName(skillName); err != nil {
		return err
	}
	repo, err := s.usageRepo()
	if err != nil {
		return err
	}
	_, err = repo.RecordSkillUsage(ctx, runID, skillName, found)
	return err
}

// Usage returns one run's append-only usage rows in seq order (admin read).
func (s *Service) Usage(ctx context.Context, runID int64) ([]agent.SkillUsage, error) {
	if runID <= 0 {
		return nil, platform.Invalid("run_id", "must be a positive agent run id")
	}
	repo, err := s.usageRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListSkillUsage(ctx, runID)
}
