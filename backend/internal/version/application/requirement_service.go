package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Requirement is a requirement aggregate.
type Requirement = domain.Requirement

// RequirementRepository is the persistence port for requirements, their
// relations and their attachment metadata (implemented by the postgres
// repository; consumed by T10/T11 as well).
type RequirementRepository = domain.RequirementRepository

// CreateRequirementCommand is the create-requirement request (spec §7.3). Only
// title is required; priority defaults to should. Source records the caller:
// user for the HTTP API, ai for agent tools (T13).
type CreateRequirementCommand struct {
	Title              string
	Description        string
	AcceptanceCriteria string
	Priority           string
	Source             string
}

// UpdateRequirementCommand is the partial edit of a requirement (spec §7.3/
// §6.2). Nil fields keep the current value; Revision is the optimistic lock the
// caller last saw. Source records the caller of this update.
type UpdateRequirementCommand struct {
	Revision           int
	Title              *string
	Description        *string
	AcceptanceCriteria *string
	Priority           *string
	Source             string
}

// RequirementService implements the requirement pipeline of spec §6.2: create,
// edit, confirm, reopen and delete. It never changes Version.status (INV-4); the
// version repository is only used to reject a create for a missing version.
type RequirementService struct {
	repo     RequirementRepository
	versions VersionRepository
	events   EventPublisher
	ids      IDGenerator
}

// RequirementServiceOption customises the requirement service dependencies.
// Production (T16) injects the process-wide Snowflake generator so requirement
// and version ids come from the same node sequence.
type RequirementServiceOption func(*RequirementService)

// WithRequirementIDGenerator injects the Snowflake id generator. Without it the
// package-wide DefaultIDGenerator is used, which is safe for a single process.
func WithRequirementIDGenerator(ids IDGenerator) RequirementServiceOption {
	return func(s *RequirementService) {
		if ids != nil {
			s.ids = ids
		}
	}
}

// NewRequirementService wires the requirement use cases to their repository, the
// version repository (missing-version check) and the event publisher.
func NewRequirementService(repo RequirementRepository, versions VersionRepository, events EventPublisher, opts ...RequirementServiceOption) *RequirementService {
	service := &RequirementService{
		repo:     repo,
		versions: versions,
		events:   events,
		ids:      DefaultIDGenerator(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Create validates and inserts a new draft requirement in any P3 version status
// (spec §6.2). The version status and revision are never touched. A successful
// create publishes requirement.added.
func (s *RequirementService) Create(ctx context.Context, versionID int64, cmd CreateRequirementCommand, actor int64) (*Requirement, error) {
	if s.versions == nil {
		return nil, errors.New("version: version repository is not configured")
	}
	// The version is the path parent: a missing one is a 404 and no requirement
	// may be created for it. Its status is deliberately not a gate (spec §6.1).
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return nil, err
	}

	source := requirementSourceOrDefault(cmd.Source)
	requirement := &Requirement{
		ID:                 s.ids.Next(),
		VersionID:          versionID,
		Title:              cmd.Title,
		Description:        cmd.Description,
		AcceptanceCriteria: cmd.AcceptanceCriteria,
		Priority:           cmd.Priority,
		Status:             domain.RequirementDraft,
		Source:             source,
		LastUpdateSource:   source,
		Revision:           1,
		CreatedBy:          actor,
		UpdatedBy:          actor,
	}
	if requirement.Priority == "" {
		requirement.Priority = domain.PriorityShould
	}
	if requirement.ID <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}
	if err := requirement.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, requirement); err != nil {
		return nil, err
	}

	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.added",
		Summary:          "requirement added",
		RefRequirementID: requirementIDPtr(requirement.ID),
		ActorID:          actor,
		Source:           requirement.Source,
		Detail:           map[string]any{"status": requirement.Status, "source": requirement.Source},
	})
	return requirement, nil
}

// Update applies a partial edit to a draft requirement under the optimistic
// revision lock. Clarified requirements are read-only until reopened (INV-9) and
// a stale revision is a revision_conflict. last_update_source records the caller;
// source is immutable.
func (s *RequirementService) Update(ctx context.Context, versionID, reqID int64, cmd UpdateRequirementCommand, actor int64) (*Requirement, error) {
	current, err := s.load(ctx, versionID, reqID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.RequirementDraft {
		return nil, requirementStateConflictError(reqID, current.Status)
	}
	if current.Revision != cmd.Revision {
		return nil, requirementRevisionConflictError(reqID, cmd.Revision, current.Revision)
	}

	candidate := *current
	if cmd.Title != nil {
		candidate.Title = *cmd.Title
	}
	if cmd.Description != nil {
		candidate.Description = *cmd.Description
	}
	if cmd.AcceptanceCriteria != nil {
		candidate.AcceptanceCriteria = *cmd.AcceptanceCriteria
	}
	if cmd.Priority != nil {
		candidate.Priority = *cmd.Priority
	}
	candidate.LastUpdateSource = requirementSourceOrDefault(cmd.Source)
	candidate.UpdatedBy = actor
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	changed := changedRequirementFields(current, &candidate)

	if err := s.repo.Update(ctx, &candidate, cmd.Revision); err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.updated",
		Summary:          "requirement updated",
		RefRequirementID: requirementIDPtr(reqID),
		ActorID:          actor,
		Source:           candidate.LastUpdateSource,
		Detail:           map[string]any{"changed": changed, "source": candidate.LastUpdateSource},
	})
	return &candidate, nil
}

// Delete hard-deletes a draft requirement. The repository cascades relations,
// questions, decisions and attachment metadata; clarified requirements are a
// requirement_state_conflict (spec §6.2).
func (s *RequirementService) Delete(ctx context.Context, versionID, reqID int64, actor int64) error {
	current, err := s.load(ctx, versionID, reqID)
	if err != nil {
		return err
	}
	if current.Status != domain.RequirementDraft {
		return requirementStateConflictError(reqID, current.Status)
	}
	if err := s.repo.Delete(ctx, reqID); err != nil {
		return err
	}
	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.deleted",
		Summary:          "requirement deleted",
		RefRequirementID: requirementIDPtr(reqID),
		ActorID:          actor,
		Source:           domain.SourceUser,
		Detail:           map[string]any{"status": current.Status},
	})
	return nil
}

// Confirm moves a draft requirement to clarified. The requirement's own open
// questions must be zero (INV-3); version-level questions do not block it. The
// repository enforces the same gate inside the status CAS so a concurrently
// opened question cannot be confirmed over.
func (s *RequirementService) Confirm(ctx context.Context, versionID, reqID int64, actor int64) (*Requirement, error) {
	current, err := s.load(ctx, versionID, reqID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.RequirementDraft {
		return nil, requirementStateConflictError(reqID, current.Status)
	}
	open, err := s.repo.CountOpenQuestions(ctx, reqID)
	if err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, domain.NewStateConflict("open_questions_remaining",
			fmt.Sprintf("requirement %d still has %d open question(s)", reqID, open))
	}

	updated, err := s.repo.UpdateStatus(ctx, reqID, domain.RequirementDraft, domain.RequirementClarified, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.clarified",
		Summary:          "requirement clarified",
		RefRequirementID: requirementIDPtr(reqID),
		ActorID:          actor,
		Source:           domain.SourceUser,
		Detail:           map[string]any{"confirmedBy": actor},
	})
	return updated, nil
}

// Reopen moves a clarified requirement back to draft in any version status and
// clears the confirmation fields. Questions and decisions are preserved (spec
// §6.2).
func (s *RequirementService) Reopen(ctx context.Context, versionID, reqID int64, actor int64) (*Requirement, error) {
	current, err := s.load(ctx, versionID, reqID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.RequirementClarified {
		return nil, requirementStateConflictError(reqID, current.Status)
	}

	updated, err := s.repo.UpdateStatus(ctx, reqID, domain.RequirementClarified, domain.RequirementDraft, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.reopened",
		Summary:          "requirement reopened",
		RefRequirementID: requirementIDPtr(reqID),
		ActorID:          actor,
		Source:           domain.SourceUser,
		Detail:           map[string]any{"status": updated.Status},
	})
	return updated, nil
}

// Get returns one requirement scoped to versionID.
func (s *RequirementService) Get(ctx context.Context, versionID, reqID int64) (*Requirement, error) {
	return s.load(ctx, versionID, reqID)
}

// List returns one page of a version's requirements ordered by created_at, id
// ascending (spec §6.2). DB-level filtering is the repository's job.
func (s *RequirementService) List(ctx context.Context, versionID int64, status string, page domain.Page) ([]Requirement, bool, error) {
	return s.repo.List(ctx, versionID, status, page)
}

// load fetches a requirement and rejects one that belongs to another version: a
// cross-version path id is indistinguishable from a missing requirement.
func (s *RequirementService) load(ctx context.Context, versionID, reqID int64) (*Requirement, error) {
	requirement, err := s.repo.Get(ctx, reqID)
	if err != nil {
		return nil, err
	}
	if requirement.VersionID != versionID {
		return nil, domain.NewNotFound("requirement", reqID)
	}
	return requirement, nil
}

// publish emits one requirement event after a committed write. Publication
// failures are logged and swallowed (spec §6.10).
func (s *RequirementService) publish(ctx context.Context, event VersionEvent) {
	if s.events == nil {
		return
	}
	if err := s.events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "requirement event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}

// requirementIDPtr returns a pointer to id for a VersionEvent reference.
func requirementIDPtr(id int64) *int64 { return &id }

// requirementSourceOrDefault maps an empty caller source to user. Invalid values
// are kept so domain validation rejects them instead of silently rewriting them.
func requirementSourceOrDefault(source string) string {
	if source == "" {
		return domain.SourceUser
	}
	return source
}

// changedRequirementFields lists the requirement columns an edit actually
// changed, for the requirement.updated event's changed detail.
func changedRequirementFields(before, after *Requirement) []string {
	changed := make([]string, 0, 4)
	if before.Title != after.Title {
		changed = append(changed, "title")
	}
	if before.Description != after.Description {
		changed = append(changed, "description")
	}
	if before.AcceptanceCriteria != after.AcceptanceCriteria {
		changed = append(changed, "acceptanceCriteria")
	}
	if before.Priority != after.Priority {
		changed = append(changed, "priority")
	}
	return changed
}

// requirementRevisionConflictError builds the spec §7.1 revision_conflict 409 for
// requirements.
func requirementRevisionConflictError(id int64, expected, actual int) error {
	return domain.NewStateConflict("revision_conflict",
		fmt.Sprintf("requirement %d revision is %d, expected %d", id, actual, expected))
}

// requirementStateConflictError builds the spec §7.1 requirement_state_conflict
// 409 for an action a requirement's status does not allow.
func requirementStateConflictError(id int64, status string) error {
	return domain.NewStateConflict("requirement_state_conflict",
		fmt.Sprintf("requirement %d is %s, which does not allow this action", id, status))
}
