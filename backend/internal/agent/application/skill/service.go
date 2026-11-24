// Package skill implements the runtime skill library: a DB-authoritative
// store of SKILL.md packages with file seeding, append-only versions and an
// admin editing surface. Skills are an agent capability, so the package lives
// inside the agent context (no new bounded context).
package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"unicode/utf8"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Audit actions of every skill write. Metadata never contains the body.
const (
	ActionSkillCreated = "skill.created"
	ActionSkillUpdated = "skill.updated"
	ActionSkillDeleted = "skill.deleted"
)

// Repository is the persistence port of the skill library. The concrete
// agentpg.SkillRepository implements it.
type Repository interface {
	ListSkills(ctx context.Context) ([]agent.Skill, error)
	GetSkill(ctx context.Context, name string) (agent.Skill, error)
	// CreateSkill inserts a new skill plus its first admin revision in one
	// transaction. A duplicate active name returns agent.ErrConflict.
	CreateSkill(ctx context.Context, skill agent.Skill, actorID *int64, note string) (agent.Skill, error)
	// EnsureSkill seeds one package only when no row with that name has
	// ever existed (including deleted rows): restarts never overwrite or
	// resurrect administrator edits.
	EnsureSkill(ctx context.Context, seed agent.Skill) (agent.Skill, bool, error)
	// UpdateSkill applies a partial edit under a row lock, appends one
	// admin revision when anything changed and honours ExpectedVersion.
	UpdateSkill(ctx context.Context, name string, update agent.SkillUpdate, actorID *int64, note string) (agent.Skill, bool, error)
	// DeleteSkill hides the skill (soft delete) and keeps its version rows.
	DeleteSkill(ctx context.Context, name string) (agent.Skill, error)
	ListSkillVersions(ctx context.Context, name string, limit int, beforeVersion *int) ([]agent.SkillVersion, bool, error)
}

// Service implements the skill library use cases. Writes require the admin
// role (management surface, profile_admin precedent); reads are gated by the
// admin route middleware.
type Service struct {
	repo  Repository
	audit auditapp.Recorder
}

// NewService wires the skill service. A nil repository answers 503; a nil
// audit sink keeps writes successful (best-effort audit precedent).
func NewService(repo Repository, audit auditapp.Recorder) *Service {
	return &Service{repo: repo, audit: audit}
}

// List returns every active skill ordered by name.
func (s *Service) List(ctx context.Context) ([]agent.Skill, error) {
	if s == nil || s.repo == nil {
		return nil, platform.Unavailable("skill repository is not configured")
	}
	return s.repo.ListSkills(ctx)
}

// Get returns one active skill including its body.
func (s *Service) Get(ctx context.Context, name string) (agent.Skill, error) {
	if err := agent.ValidateSkillName(name); err != nil {
		return agent.Skill{}, err
	}
	if s == nil || s.repo == nil {
		return agent.Skill{}, platform.Unavailable("skill repository is not configured")
	}
	skill, err := s.repo.GetSkill(ctx, name)
	if err != nil {
		return agent.Skill{}, skillRepoError(err)
	}
	return skill, nil
}

// Create inserts a new skill at version 1 (admin).
func (s *Service) Create(ctx context.Context, actor platform.Actor, input agent.Skill, note string) (agent.Skill, error) {
	if err := requireAdmin(actor); err != nil {
		return agent.Skill{}, err
	}
	if err := validateSkillContent(input); err != nil {
		return agent.Skill{}, err
	}
	if s == nil || s.repo == nil {
		return agent.Skill{}, platform.Unavailable("skill repository is not configured")
	}
	stored, err := s.repo.CreateSkill(ctx, input, actorID(actor), note)
	if err != nil {
		if errors.Is(err, agent.ErrConflict) {
			return agent.Skill{}, platform.Conflict("a skill with this name already exists")
		}
		return agent.Skill{}, skillRepoError(err)
	}
	s.recordAudit(ctx, actor, ActionSkillCreated, stored, nil)
	return stored, nil
}

// Update applies a runtime edit and appends one append-only version when
// anything changed. The next Get observes the new content immediately — no
// rebuild, restart or re-seed (R-C11).
func (s *Service) Update(ctx context.Context, actor platform.Actor, name string, update agent.SkillUpdate, note string) (agent.Skill, bool, error) {
	if err := requireAdmin(actor); err != nil {
		return agent.Skill{}, false, err
	}
	if err := agent.ValidateSkillName(name); err != nil {
		return agent.Skill{}, false, err
	}
	fields, err := validateSkillUpdate(update)
	if err != nil {
		return agent.Skill{}, false, err
	}
	if s == nil || s.repo == nil {
		return agent.Skill{}, false, platform.Unavailable("skill repository is not configured")
	}
	if update.Empty() {
		current, err := s.repo.GetSkill(ctx, name)
		if err != nil {
			return agent.Skill{}, false, skillRepoError(err)
		}
		return current, false, nil
	}
	stored, changed, err := s.repo.UpdateSkill(ctx, name, update, actorID(actor), note)
	if err != nil {
		switch {
		case errors.Is(err, agent.ErrSkillVersionConflict):
			return agent.Skill{}, false, platform.StateConflict("skill was changed by another writer")
		default:
			return agent.Skill{}, false, skillRepoError(err)
		}
	}
	if changed {
		s.recordAudit(ctx, actor, ActionSkillUpdated, stored, fields)
	}
	return stored, changed, nil
}

// Delete hides a skill. Its append-only version history is preserved.
func (s *Service) Delete(ctx context.Context, actor platform.Actor, name string) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	if err := agent.ValidateSkillName(name); err != nil {
		return err
	}
	if s == nil || s.repo == nil {
		return platform.Unavailable("skill repository is not configured")
	}
	deleted, err := s.repo.DeleteSkill(ctx, name)
	if err != nil {
		return skillRepoError(err)
	}
	s.recordAudit(ctx, actor, ActionSkillDeleted, deleted, nil)
	return nil
}

// Versions returns one version DESC page of the append-only history. The
// skill must exist (404 otherwise); limit normalizes to 1–100, default 20.
func (s *Service) Versions(ctx context.Context, name string, limit int, beforeVersion *int) ([]agent.SkillVersion, bool, error) {
	if err := agent.ValidateSkillName(name); err != nil {
		return nil, false, err
	}
	if s == nil || s.repo == nil {
		return nil, false, platform.Unavailable("skill repository is not configured")
	}
	if beforeVersion != nil && *beforeVersion <= 0 {
		return nil, false, platform.Invalid("before_version", "must be a positive integer")
	}
	if _, err := s.repo.GetSkill(ctx, name); err != nil {
		return nil, false, skillRepoError(err)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return s.repo.ListSkillVersions(ctx, name, limit, beforeVersion)
}

/* -------------------------------- helpers -------------------------------- */

func requireAdmin(actor platform.Actor) error {
	if !actor.IsAdmin() {
		return platform.Forbidden()
	}
	return nil
}

func validateSkillContent(input agent.Skill) error {
	if err := agent.ValidateSkillName(input.Name); err != nil {
		return err
	}
	if err := agent.ValidateSkillDescription(input.Description); err != nil {
		return err
	}
	if err := agent.ValidateSkillBody(input.Body); err != nil {
		return err
	}
	return agent.ValidateSkillMetadata(input.Metadata)
}

// validateSkillUpdate checks the provided fields and returns the changed
// field names for the audit record.
func validateSkillUpdate(update agent.SkillUpdate) ([]string, error) {
	fields := make([]string, 0, 4)
	if update.Description != nil {
		if err := agent.ValidateSkillDescription(*update.Description); err != nil {
			return nil, err
		}
		fields = append(fields, "description")
	}
	if update.Body != nil {
		if err := agent.ValidateSkillBody(*update.Body); err != nil {
			return nil, err
		}
		fields = append(fields, "body")
	}
	if update.Metadata != nil {
		if err := agent.ValidateSkillMetadata(update.Metadata); err != nil {
			return nil, err
		}
		fields = append(fields, "metadata")
	}
	if update.Enabled != nil {
		fields = append(fields, "enabled")
	}
	if update.ExpectedVersion != nil && *update.ExpectedVersion <= 0 {
		return nil, platform.Invalid("expected_version", "must be a positive integer")
	}
	return fields, nil
}

func skillRepoError(err error) error {
	if errors.Is(err, agent.ErrNotFound) {
		return platform.NotFound("skill")
	}
	return err
}

func actorID(actor platform.Actor) *int64 {
	if actor.ID <= 0 {
		return nil
	}
	id := actor.ID
	return &id
}

// recordAudit writes one skill audit event. Metadata is limited to identity,
// changed fields, version and a body digest; the body never enters the log
// (agent.profile.updated precedent).
func (s *Service) recordAudit(ctx context.Context, actor platform.Actor, action string, skill agent.Skill, fields []string) {
	if s == nil || s.audit == nil {
		return
	}
	digest := sha256.Sum256([]byte(skill.Body))
	metadata := map[string]any{
		"name":        skill.Name,
		"version":     skill.Version,
		"body_sha256": hex.EncodeToString(digest[:]),
		"body_length": utf8.RuneCountInString(skill.Body),
	}
	if fields != nil {
		metadata["fields"] = fields
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actorID(actor),
		Action:     action,
		TargetKind: "skill",
		TargetID:   strconv.FormatInt(skill.ID, 10),
		Metadata:   metadata,
	})
}
