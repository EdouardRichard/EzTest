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
// edit, confirm, reopen and delete. Requirement operations do not change
// Version.status (INV-4/D31), with the single D43 exception: a successful create
// on a completed version moves it to continuing inside the same transaction.
type RequirementService struct {
	repo     RequirementRepository
	versions VersionRepository
	events   EventPublisher
	audit    VersionAuditSink
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

// WithRequirementAuditSink injects the version lifecycle audit sink used by the
// D43 continuing trigger (spec §11.2 version.continued). Audit failures are
// logged and swallowed.
func WithRequirementAuditSink(sink VersionAuditSink) RequirementServiceOption {
	return func(s *RequirementService) {
		if sink != nil {
			s.audit = sink
		}
	}
}

// requirementContinuingRepository is the optional same-transaction port of the
// D43 trigger (spec §5.1). *postgres.VersionRepository implements it: the
// requirement insert and the completed -> continuing CAS share one transaction,
// and the boolean reports whether this call performed the transition.
type requirementContinuingRepository interface {
	CreateRequirementContinuing(ctx context.Context, requirement *Requirement, actor int64) (bool, error)
}

// continuingPhaseRepository is the atomic phase refresh used after a
// requirement write (spec §5.1: all non-draft -> designing, otherwise
// clarifying). The repository derives the target from the requirement rows and
// preserves phase=executing inside one UPDATE, so a concurrent incremental batch
// create cannot be overwritten by a stale pre-insert read, and two concurrent
// confirmations can never leave a fully confirmed version stuck in clarifying.
// It reports whether the phase actually changed.
type continuingPhaseRepository interface {
	RefreshContinuePhase(ctx context.Context, versionID int64, actor int64) (*Version, bool, error)
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

// Create validates and inserts a new draft requirement. In every P3 status
// except archived the requirement is created without touching the version
// (D31); archived versions are rejected with version_not_continuable. On a
// completed version the D43 trigger applies: the requirement insert and the
// completed -> continuing/clarifying CAS share one transaction, and
// version.status_changed + version.continued are published afterwards. On a
// continuing version the phase is re-derived atomically after the insert (any
// draft -> clarifying) without interrupting an active incremental batch
// (phase=executing). A successful create publishes requirement.added in every
// case.
func (s *RequirementService) Create(ctx context.Context, versionID int64, cmd CreateRequirementCommand, actor int64) (*Requirement, error) {
	if s.versions == nil {
		return nil, errors.New("version: version repository is not configured")
	}
	// The version is the path parent: a missing one is a 404 and no requirement
	// may be created for it. Archived versions reject requirement creation (spec
	// §5.1 rule 6).
	current, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if current.Status == domain.VersionArchived {
		return nil, versionNotContinuableError(versionID, current.Status)
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

	transitioned := false
	if current.Status == domain.VersionCompleted {
		repo, ok := s.versions.(requirementContinuingRepository)
		if !ok {
			return nil, errors.New("version: repository does not support the continuing requirement transaction")
		}
		transitioned, err = repo.CreateRequirementContinuing(ctx, requirement, actor)
		if err != nil {
			return nil, err
		}
	} else if err := s.repo.Create(ctx, requirement); err != nil {
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
	// Audit coverage (spec §10.4): the entry names the written requirement and
	// its status only. The title, description and acceptance criteria must never
	// enter the audit log.
	s.recordAudit(ctx, VersionAuditEvent{
		Action:     "version.requirement_created",
		VersionID:  versionID,
		ActorID:    actor,
		TargetType: "requirement",
		TargetID:   fmt.Sprintf("%d", requirement.ID),
		Metadata:   map[string]any{"status": requirement.Status},
	})
	if transitioned {
		s.publish(ctx, VersionEvent{
			VersionID: versionID,
			Type:      "version.status_changed",
			Summary:   "version status changed from completed to continuing",
			ActorID:   actor,
			Source:    requirement.Source,
			Detail: map[string]any{
				"from":   domain.VersionCompleted,
				"to":     domain.VersionContinuing,
				"phase":  domain.ContinuePhaseClarifying,
				"reason": continueReasonNewRequirement,
			},
		})
		s.recordAudit(ctx, VersionAuditEvent{
			Action:    "version.continued",
			VersionID: versionID,
			From:      domain.VersionCompleted,
			To:        domain.VersionContinuing,
			Phase:     domain.ContinuePhaseClarifying,
			ActorID:   actor,
		})
	} else if current.Status == domain.VersionContinuing {
		// Spec §6.1 step 9: a new requirement returns a non-executing phase to
		// clarifying; the refresh derives the phase from the committed rows and
		// never overrides a concurrent phase=executing (active incremental batch).
		s.refreshContinuePhase(ctx, versionID, continueReasonNewRequirement, actor)
	}
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
	s.recordAudit(ctx, VersionAuditEvent{
		Action:     "version.requirement_updated",
		VersionID:  versionID,
		ActorID:    actor,
		TargetType: "requirement",
		TargetID:   fmt.Sprintf("%d", reqID),
		Metadata:   map[string]any{"status": candidate.Status, "revision": candidate.Revision},
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
	s.recordAudit(ctx, VersionAuditEvent{
		Action:     "version.requirement_deleted",
		VersionID:  versionID,
		ActorID:    actor,
		TargetType: "requirement",
		TargetID:   fmt.Sprintf("%d", reqID),
		Metadata:   map[string]any{"status": current.Status},
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
	// Spec §5.1: in a continuing version, confirming requirements moves the phase
	// to designing once every requirement is confirmed, and back to clarifying
	// while drafts remain. The refresh is atomic and keeps phase=executing.
	// Failures here never fail the committed confirmation.
	s.recordAudit(ctx, VersionAuditEvent{
		Action:     "version.requirement_confirmed",
		VersionID:  versionID,
		ActorID:    actor,
		TargetType: "requirement",
		TargetID:   fmt.Sprintf("%d", reqID),
		Metadata:   map[string]any{"status": updated.Status},
	})
	s.refreshContinuePhase(ctx, versionID, "", actor)
	return updated, nil
}

// refreshContinuePhase atomically re-derives the continue phase of a continuing
// version from the committed requirement rows (spec §5.1/§6.1 step 9): any draft
// keeps/moves it to clarifying, otherwise designing. The repository performs the
// derivation and the executing guard inside one UPDATE, so a stale pre-write read
// can never overwrite a concurrent phase=executing, and a version that is no
// longer continuing is untouched. A real phase change publishes
// version.status_changed with the optional reason. Failures are logged and never
// fail the committed requirement write.
func (s *RequirementService) refreshContinuePhase(ctx context.Context, versionID int64, reason string, actor int64) {
	if s.versions == nil {
		return
	}
	repo, ok := s.versions.(continuingPhaseRepository)
	if !ok {
		return
	}
	updated, changed, err := repo.RefreshContinuePhase(ctx, versionID, actor)
	if err != nil {
		slog.WarnContext(ctx, "version continue phase refresh failed",
			"version_id", versionID,
			"error", err,
		)
		return
	}
	if !changed || updated == nil {
		return
	}
	s.publishContinuePhase(ctx, updated.ID, updated.ContinuePhase, reason, actor)
}

// publishContinuePhase emits the version.status_changed event of a phase update
// (spec §6.1 step 2/§8.1: detail carries the phase and an optional reason).
func (s *RequirementService) publishContinuePhase(ctx context.Context, versionID int64, phase, reason string, actor int64) {
	detail := map[string]any{
		"from":  domain.VersionContinuing,
		"to":    domain.VersionContinuing,
		"phase": phase,
	}
	if reason != "" {
		detail["reason"] = reason
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "version.status_changed",
		Summary:   "version continue phase changed to " + phase,
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    detail,
	})
}

// recordAudit records one version lifecycle audit entry for the D43 trigger.
// Failures are logged and swallowed (spec §11.2).
func (s *RequirementService) recordAudit(ctx context.Context, event VersionAuditEvent) {
	if s.audit == nil {
		return
	}
	if err := s.audit.Record(ctx, event); err != nil {
		slog.WarnContext(ctx, "version audit record failed",
			"action", event.Action,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}

// versionNotContinuableError builds the spec §7.11 409 of the D43 trigger for a
// version whose status cannot enter continuing (archived, or any non-completed
// status observed at write time).
func versionNotContinuableError(versionID int64, status string) error {
	return domain.NewStateConflict("version_not_continuable",
		fmt.Sprintf("version %d is %s, requirements can only trigger continuing on a completed version", versionID, status))
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
	s.recordAudit(ctx, VersionAuditEvent{
		Action:     "version.requirement_reopened",
		VersionID:  versionID,
		ActorID:    actor,
		TargetType: "requirement",
		TargetID:   fmt.Sprintf("%d", reqID),
		Metadata:   map[string]any{"status": updated.Status},
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
