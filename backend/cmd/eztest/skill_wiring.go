package main

// Skill library composition (v0.2.4-C T18): the DB-authoritative runtime
// skill store with file seeding and the admin CRUD surface. Skills are an
// agent capability, so the service lives inside the agent context.

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5/pgxpool"
)

// skillRuntime is the constructed skill-library surface.
type skillRuntime struct {
	Service *skillapp.Service
	Handler *agentapi.SkillHandler
	// ToolReader adapts the service onto the skill.read tool port (T19).
	ToolReader skillToolReader
}

// newSkillRuntime builds the skill service and admin handler.
func newSkillRuntime(pool *pgxpool.Pool, ids platformid.Generator, audit auditapp.Recorder) *skillRuntime {
	repo := agentpg.NewSkillRepository(pool, ids)
	service := skillapp.NewService(repo, audit)
	return &skillRuntime{
		Service:    service,
		Handler:    agentapi.NewSkillHandler(service),
		ToolReader: skillToolReader{service: service},
	}
}

// skillToolReader adapts the skill service onto tools.SkillReader: an unknown
// package is a miss (found=false, no error) so the tool can record the miss
// fact; classified argument errors and internal failures pass through for
// mapPortError (the outcome is unknown, not a miss).
type skillToolReader struct{ service *skillapp.Service }

func (a skillToolReader) ReadSkill(ctx context.Context, name string) (tools.SkillContent, bool, error) {
	skill, found, err := a.service.ReadForAgent(ctx, name)
	if err != nil {
		var apiErr *platform.Error
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return tools.SkillContent{}, false, nil
		}
		return tools.SkillContent{}, false, err
	}
	if !found {
		return tools.SkillContent{}, false, nil
	}
	return tools.SkillContent{
		Name:        skill.Name,
		Description: skill.Description,
		Body:        skill.Body,
		Version:     skill.Version,
	}, true, nil
}

// seedSkills runs the idempotent first-start file seeding. The skills/
// directory lives at the repository root; the API process normally runs
// from backend/, so both locations are probed. A missing directory is
// logged explicitly (never a silent skip) and leaves the runtime library
// untouched — the DB stays authoritative either way.
func seedSkills(ctx context.Context, service *skillapp.Service) {
	dir := resolveSkillsSeedDir()
	if dir == "" {
		slog.Warn("skills seed directory not found; file seeding skipped (runtime skill library unchanged)")
		return
	}
	result, err := service.Seed(ctx, dir)
	if err != nil {
		// Fail closed like EnsureProfiles: a broken seed package must not
		// silently disappear.
		log.Fatalf("eztest: seed skills: %v", err)
	}
	slog.Info("skill seeding complete", "dir", dir, "seeded", result.Seeded, "skipped", result.Skipped)
}

func resolveSkillsSeedDir() string {
	for _, candidate := range []string{"skills", "../skills"} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}
