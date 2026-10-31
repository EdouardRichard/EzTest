// Package versionapp implements the version, requirement, conversation and
// attachment use cases.
package versionapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// RequirementService implements requirement draft lifecycle, immutable
// revisions, single/batch confirmation, rollback and relations.
type RequirementService struct {
	repo      version.RequirementRepository
	versions  VersionReader
	resources ResourceCatalog
	stale     CaseStaleHook
	audit     auditapp.Recorder
	events    eventingapp.Publisher
	ids       platformid.Generator
	clock     clock.Clock
}

// NewRequirementService wires the requirement use cases. audit and events may
// be nil in unit tests; stale defaults to the P1 no-op hook.
func NewRequirementService(
	repo version.RequirementRepository,
	versions VersionReader,
	resources ResourceCatalog,
	stale CaseStaleHook,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
	ids platformid.Generator,
	clk clock.Clock,
) *RequirementService {
	if stale == nil {
		stale = NoopCaseStaleHook{}
	}
	return &RequirementService{
		repo: repo, versions: versions, resources: resources, stale: stale,
		audit: audit, events: events, ids: ids, clock: clk,
	}
}

// Create persists a new draft requirement with revision 1 and projects the
// version to running (idempotent, best effort).
func (s *RequirementService) Create(ctx context.Context, versionID int64, in RequirementDraft, actorID int64) (RequirementDetail, error) {
	if versionID <= 0 {
		return RequirementDetail{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	versionView, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return RequirementDetail{}, err
	}
	revision, err := normalizeDraft(in)
	if err != nil {
		return RequirementDetail{}, err
	}
	if err := s.validateTargets(ctx, versionView, revision); err != nil {
		return RequirementDetail{}, err
	}

	now := s.clock.Now()
	requirementID := s.ids.Next()
	revisionID := s.ids.Next()
	revision.ID = revisionID
	revision.RequirementID = requirementID
	revision.Revision = 1
	revision.CreatedBy = optionalActor(actorID)
	revision.CreatedAt = now

	requirement := version.Requirement{
		ID:                  requirementID,
		VersionID:           versionID,
		CurrentVersionID:    &revisionID,
		Status:              version.RequirementDraft,
		TargetVersionSUTIDs: append([]string{}, revision.TargetVersionSUTs...),
		CreatedBy:           optionalActor(actorID),
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.repo.Create(ctx, requirement, revision); err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirementID)
	revisionIDString := platformid.Format(revisionID)
	s.publishEvent(ctx, versionID, "requirement.created", map[string]any{
		"requirement_id": requirementIDString,
		"status":         string(version.RequirementDraft),
		"title":          revision.Title,
	}, map[string]any{"requirement_id": requirementIDString})
	s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"status":                 string(version.RequirementDraft),
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.created", "requirement", requirementIDString, map[string]any{
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"status":                 string(version.RequirementDraft),
		"title":                  revision.Title,
	})
	s.markRunning(ctx, versionID)
	return s.Get(ctx, versionID, requirementID)
}

// Update always creates a new immutable revision (revision+1). Editing a
// clarified requirement reopens it as draft; the old confirmation stays bound
// to the old revision.
func (s *RequirementService) Update(ctx context.Context, versionID, requirementID int64, in RequirementDraft, reason string, actorID int64) (RequirementDetail, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return RequirementDetail{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := validationStringMax("reason", reason, version.RequirementReasonMax); err != nil {
		return RequirementDetail{}, err
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	requirement := aggregate.Requirement
	if requirement.Status != version.RequirementDraft && requirement.Status != version.RequirementClarified {
		return RequirementDetail{}, platform.StateConflict("requirement is not editable")
	}
	current, ok := currentVersion(aggregate)
	if !ok {
		return RequirementDetail{}, platform.StateConflict("requirement has no current revision")
	}
	versionView, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return RequirementDetail{}, err
	}
	revision, err := normalizeDraft(in)
	if err != nil {
		return RequirementDetail{}, err
	}
	if err := s.validateTargets(ctx, versionView, revision); err != nil {
		return RequirementDetail{}, err
	}

	now := s.clock.Now()
	revisionID := s.ids.Next()
	revision.ID = revisionID
	revision.RequirementID = requirementID
	revision.Revision = current.Revision + 1
	revision.CreatedBy = optionalActor(actorID)
	revision.CreatedAt = now

	requirement.Status = version.RequirementDraft
	requirement.CurrentVersionID = &revisionID
	requirement.TargetVersionSUTIDs = append([]string{}, revision.TargetVersionSUTs...)
	requirement.UpdatedAt = now
	if err := s.repo.AddRevision(ctx, requirement, revision); err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirementID)
	revisionIDString := platformid.Format(revisionID)
	s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"status":                 string(version.RequirementDraft),
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.updated", "requirement", requirementIDString, map[string]any{
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"reason":                 reason,
	})
	return s.Get(ctx, versionID, requirementID)
}

// Get returns the full requirement detail.
func (s *RequirementService) Get(ctx context.Context, versionID, requirementID int64) (RequirementDetail, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return RequirementDetail{}, err
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	return s.detailFromAggregate(aggregate)
}

// List returns one id-descending page of current-revision requirement
// summaries.
func (s *RequirementService) List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]RequirementView, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("id", "must be a positive decimal ID")
	}
	statusFilter, err := requirementStatusFilter(status)
	if err != nil {
		return nil, false, err
	}
	limit = normalizeLimit(limit)
	items, err := s.repo.List(ctx, versionID, statusFilter, limit+1, beforeID)
	if err != nil {
		return nil, false, mapRequirementRepositoryError(err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	views := make([]RequirementView, 0, len(items))
	for _, item := range items {
		views = append(views, requirementViewFromListItem(item))
	}
	return views, hasMore, nil
}

// Confirm freezes the current revision. Repeated confirmation of the same
// revision is idempotent and reports confirmed=false.
func (s *RequirementService) Confirm(ctx context.Context, versionID, requirementID, actorID int64) (ConfirmResult, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return ConfirmResult{}, err
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return ConfirmResult{}, mapRequirementRepositoryError(err)
	}
	requirement := aggregate.Requirement
	current, ok := currentVersion(aggregate)
	if !ok {
		return ConfirmResult{}, platform.StateConflict("requirement has no current revision")
	}

	if requirement.Status == version.RequirementClarified {
		for _, confirmation := range aggregate.Confirmations {
			if confirmation.RequirementVersionID == current.ID {
				detail, err := s.detailFromAggregate(aggregate)
				if err != nil {
					return ConfirmResult{}, err
				}
				return ConfirmResult{Confirmed: false, Requirement: detail}, nil
			}
		}
		return ConfirmResult{}, platform.StateConflict("requirement is already clarified")
	}
	if requirement.Status != version.RequirementDraft {
		return ConfirmResult{}, platform.StateConflict("requirement is not confirmable")
	}
	if err := current.ValidateConfirmed(); err != nil {
		return ConfirmResult{}, err
	}
	versionView, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return ConfirmResult{}, err
	}
	if err := s.validateTargets(ctx, versionView, current); err != nil {
		return ConfirmResult{}, err
	}

	now := s.clock.Now()
	confirmation := version.Confirmation{
		ID:                     s.ids.Next(),
		RequirementVersionID:   current.ID,
		Scope:                  version.ConfirmationScopeRequirement,
		ActorID:                actorID,
		ConfirmedAt:            now,
		AffectedRequirementIDs: []string{},
	}
	requirement.Status = version.RequirementClarified
	requirement.UpdatedAt = now
	if err := s.repo.Confirm(ctx, requirement, confirmation); err != nil {
		return ConfirmResult{}, mapRequirementRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirementID)
	revisionIDString := platformid.Format(current.ID)
	s.publishEvent(ctx, versionID, "requirement.clarified", map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": revisionIDString,
		"scope":                  version.ConfirmationScopeRequirement,
		"batch_id":               nil,
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.confirmed", "requirement", requirementIDString, map[string]any{
		"requirement_version_id": revisionIDString,
		"scope":                  version.ConfirmationScopeRequirement,
	})
	s.markRunning(ctx, versionID)

	detail, err := s.Get(ctx, versionID, requirementID)
	if err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{Confirmed: true, Requirement: detail}, nil
}

// BatchConfirm validates every draft in the batch inside one transaction and
// skips already-confirmed requirements. Any invalid draft fails the whole
// batch (P1-D8).
func (s *RequirementService) BatchConfirm(ctx context.Context, versionID int64, requirementIDs []int64, actorID int64) (BatchConfirmResult, error) {
	if versionID <= 0 {
		return BatchConfirmResult{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	ids, err := dedupeRequirementIDs(requirementIDs)
	if err != nil {
		return BatchConfirmResult{}, err
	}
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return BatchConfirmResult{}, err
	}
	items, err := s.repo.GetMany(ctx, versionID, ids)
	if err != nil {
		return BatchConfirmResult{}, mapRequirementRepositoryError(err)
	}

	now := s.clock.Now()
	versionView, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return BatchConfirmResult{}, err
	}
	confirmed := make([]int64, 0, len(items))
	skipped := make([]int64, 0, len(items))
	toConfirm := make([]version.Requirement, 0, len(items))
	toConfirmIDs := make([]string, 0, len(items))
	for _, item := range items {
		switch item.Requirement.Status {
		case version.RequirementClarified:
			skipped = append(skipped, item.Requirement.ID)
		case version.RequirementDraft:
			if err := item.Current.ValidateConfirmed(); err != nil {
				return BatchConfirmResult{}, withRequirementID(err, item.Requirement.ID)
			}
			if err := s.validateTargets(ctx, versionView, item.Current); err != nil {
				return BatchConfirmResult{}, withRequirementID(err, item.Requirement.ID)
			}
			requirement := item.Requirement
			requirement.Status = version.RequirementClarified
			requirement.UpdatedAt = now
			toConfirm = append(toConfirm, requirement)
			confirmed = append(confirmed, item.Requirement.ID)
			toConfirmIDs = append(toConfirmIDs, platformid.Format(item.Requirement.ID))
		default:
			return BatchConfirmResult{}, withRequirementID(platform.StateConflict("requirement is not confirmable"), item.Requirement.ID)
		}
	}

	batchID := platformid.Format(s.ids.Next())
	confirmations := make([]version.Confirmation, 0, len(toConfirm))
	affected := append([]string{}, toConfirmIDs...)
	for _, requirement := range toConfirm {
		var revisionID int64
		for _, item := range items {
			if item.Requirement.ID == requirement.ID {
				revisionID = item.Current.ID
				break
			}
		}
		batchIDValue := batchID
		confirmations = append(confirmations, version.Confirmation{
			ID:                     s.ids.Next(),
			RequirementVersionID:   revisionID,
			Scope:                  version.ConfirmationScopeBatch,
			BatchID:                &batchIDValue,
			ActorID:                actorID,
			ConfirmedAt:            now,
			AffectedRequirementIDs: append([]string{}, affected...),
		})
	}

	if len(toConfirm) > 0 {
		if err := s.repo.BatchConfirm(ctx, toConfirm, confirmations); err != nil {
			return BatchConfirmResult{}, mapRequirementRepositoryError(err)
		}
		for _, requirement := range toConfirm {
			requirementIDString := platformid.Format(requirement.ID)
			revisionIDString := platformid.Format(*requirement.CurrentVersionID)
			s.publishEvent(ctx, versionID, "requirement.clarified", map[string]any{
				"requirement_id":         requirementIDString,
				"requirement_version_id": revisionIDString,
				"scope":                  version.ConfirmationScopeBatch,
				"batch_id":               batchID,
			}, map[string]any{"requirement_id": requirementIDString})
			s.recordAudit(ctx, actorID, "requirement.confirmed", "requirement", requirementIDString, map[string]any{
				"requirement_version_id": revisionIDString,
				"scope":                  version.ConfirmationScopeBatch,
				"batch_id":               batchID,
			})
		}
		s.markRunning(ctx, versionID)
	}

	if confirmed == nil {
		confirmed = []int64{}
	}
	if skipped == nil {
		skipped = []int64{}
	}
	return BatchConfirmResult{BatchID: batchID, Confirmed: confirmed, Skipped: skipped}, nil
}

// Rollback copies the confirmed revision into a new draft revision, calls the
// P2 stale hook and publishes requirement.rolled_back plus
// requirement.version_created.
func (s *RequirementService) Rollback(ctx context.Context, versionID, requirementID int64, reason string, proposalMessageID *int64, actorID int64) (RequirementDetail, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return RequirementDetail{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := validationStringMax("reason", reason, version.RequirementReasonMax); err != nil {
		return RequirementDetail{}, err
	}
	if proposalMessageID != nil && *proposalMessageID <= 0 {
		return RequirementDetail{}, platform.Invalid("proposal_message_id", "must be a positive decimal ID")
	}
	aggregate, err := s.repo.Get(ctx, versionID, requirementID)
	if err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}
	requirement := aggregate.Requirement
	if requirement.Status != version.RequirementClarified {
		return RequirementDetail{}, platform.StateConflict("only a clarified requirement can be rolled back")
	}
	current, ok := currentVersion(aggregate)
	if !ok {
		return RequirementDetail{}, platform.StateConflict("requirement has no current revision")
	}

	now := s.clock.Now()
	revisionID := s.ids.Next()
	revision := version.RequirementVersion{
		ID:                 revisionID,
		RequirementID:      requirementID,
		Revision:           current.Revision + 1,
		Title:              current.Title,
		Description:        current.Description,
		AcceptanceCriteria: current.AcceptanceCriteria,
		Priority:           current.Priority,
		Constraints:        current.Constraints,
		TargetVersionSUTs:  append([]string{}, current.TargetVersionSUTs...),
		TargetPages:        append([]string{}, current.TargetPages...),
		TargetAPIs:         append([]string{}, current.TargetAPIs...),
		CreatedBy:          optionalActor(actorID),
		CreatedAt:          now,
	}
	contentHash, err := revision.ComputeContentHash()
	if err != nil {
		return RequirementDetail{}, err
	}
	revision.ContentHash = contentHash

	requirement.Status = version.RequirementDraft
	requirement.CurrentVersionID = &revisionID
	requirement.UpdatedAt = now
	if err := s.repo.AddRevision(ctx, requirement, revision); err != nil {
		return RequirementDetail{}, mapRequirementRepositoryError(err)
	}

	staleHook := s.stale
	if staleHook == nil {
		staleHook = NoopCaseStaleHook{}
	}
	affected, err := staleHook.MarkRequirementChanged(ctx, versionID, requirementID, reason, revision.Revision)
	if err != nil {
		return RequirementDetail{}, err
	}
	if affected == nil {
		affected = []string{}
	}

	requirementIDString := platformid.Format(requirementID)
	fromRevisionIDString := platformid.Format(current.ID)
	revisionIDString := platformid.Format(revisionID)
	s.publishEvent(ctx, versionID, "requirement.rolled_back", map[string]any{
		"requirement_id":            requirementIDString,
		"from_version_id":           fromRevisionIDString,
		"new_version_id":            revisionIDString,
		"reason":                    reason,
		"affected_case_version_ids": affected,
	}, map[string]any{"requirement_id": requirementIDString})
	s.publishEvent(ctx, versionID, "requirement.version_created", map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": revisionIDString,
		"revision":               revision.Revision,
		"status":                 string(version.RequirementDraft),
	}, map[string]any{"requirement_id": requirementIDString})
	s.recordAudit(ctx, actorID, "requirement.rolled_back", "requirement", requirementIDString, map[string]any{
		"from_version_id":           fromRevisionIDString,
		"new_version_id":            revisionIDString,
		"reason":                    reason,
		"affected_case_version_ids": affected,
	})
	if proposalMessageID != nil {
		// proposal message is metadata only; AI never changes state directly.
		s.recordAudit(ctx, actorID, "requirement.rollback_proposal_used", "requirement", requirementIDString, map[string]any{
			"proposal_message_id": platformid.Format(*proposalMessageID),
		})
	}
	return s.Get(ctx, versionID, requirementID)
}

// CreateRelation persists an AI-proposed relation after validating scope.
func (s *RequirementService) CreateRelation(ctx context.Context, versionID int64, in RelationInput, actorID int64) (RelationView, error) {
	if versionID <= 0 {
		return RelationView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if in.FromRequirementID <= 0 {
		return RelationView{}, platform.Invalid("from_requirement_id", "must be a positive decimal ID")
	}
	if in.ToRequirementID <= 0 {
		return RelationView{}, platform.Invalid("to_requirement_id", "must be a positive decimal ID")
	}
	if in.FromRequirementID == in.ToRequirementID {
		return RelationView{}, platform.Invalid("to_requirement_id", "a requirement cannot relate to itself")
	}
	if !version.ValidRelationKind(in.Kind) {
		return RelationView{}, platform.Invalid("kind", "must be one of depends_on, conflicts_with, overlaps, shares_scope")
	}
	description := strings.TrimSpace(in.Description)
	if err := validationStringMax("description", description, 2000); err != nil {
		return RelationView{}, err
	}
	if in.SourceRevisionID != nil && *in.SourceRevisionID <= 0 {
		return RelationView{}, platform.Invalid("source_revision_id", "must be a positive decimal ID")
	}
	if _, err := s.repo.Get(ctx, versionID, in.FromRequirementID); err != nil {
		return RelationView{}, mapRequirementRepositoryError(err)
	}
	if _, err := s.repo.Get(ctx, versionID, in.ToRequirementID); err != nil {
		return RelationView{}, mapRequirementRepositoryError(err)
	}

	now := s.clock.Now()
	relation := version.Relation{
		ID:                s.ids.Next(),
		VersionID:         versionID,
		FromRequirementID: in.FromRequirementID,
		ToRequirementID:   in.ToRequirementID,
		Kind:              in.Kind,
		Description:       description,
		SourceRevisionID:  in.SourceRevisionID,
		CreatedBy:         optionalActor(actorID),
		CreatedAt:         now,
	}
	if err := s.repo.CreateRelation(ctx, relation); err != nil {
		return RelationView{}, mapRequirementRepositoryError(err)
	}

	fromString := platformid.Format(in.FromRequirementID)
	toString := platformid.Format(in.ToRequirementID)
	s.publishEvent(ctx, versionID, "requirement.relation_created", map[string]any{
		"from_requirement_id": fromString,
		"to_requirement_id":   toString,
		"kind":                in.Kind,
	}, map[string]any{"requirement_id": fromString})
	s.recordAudit(ctx, actorID, "requirement.relation_created", "requirement_relation", platformid.Format(relation.ID), map[string]any{
		"from_requirement_id": fromString,
		"to_requirement_id":   toString,
		"kind":                in.Kind,
	})
	return toRelationView(relation), nil
}

// ListRelations returns the relations touching one requirement.
func (s *RequirementService) ListRelations(ctx context.Context, versionID, requirementID int64) ([]RelationView, error) {
	if err := validateIDs(versionID, requirementID); err != nil {
		return nil, err
	}
	if _, err := s.repo.Get(ctx, versionID, requirementID); err != nil {
		return nil, mapRequirementRepositoryError(err)
	}
	relations, err := s.repo.ListRelations(ctx, versionID, requirementID)
	if err != nil {
		return nil, mapRequirementRepositoryError(err)
	}
	views := make([]RelationView, 0, len(relations))
	for _, relation := range relations {
		views = append(views, toRelationView(relation))
	}
	return views, nil
}

// ---------- helpers ----------

func normalizeDraft(in RequirementDraft) (version.RequirementVersion, error) {
	revision := version.RequirementVersion{
		Title:              strings.TrimSpace(in.Title),
		Description:        in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria,
		Priority:           in.Priority,
		Constraints:        in.Constraints,
		TargetVersionSUTs:  in.TargetVersionSUTs,
		TargetPages:        in.TargetPages,
		TargetAPIs:         in.TargetAPIs,
	}
	priority, err := version.NormalizePriority(revision.Priority)
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.Priority = priority
	constraints, err := version.NormalizeConstraints(revision.Constraints)
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.Constraints = constraints
	targetSUTs, err := version.NormalizeTargetIDs("targets.version_suts", revision.TargetVersionSUTs)
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.TargetVersionSUTs = targetSUTs
	targetPages, err := version.NormalizeTargetIDs("targets.pages", revision.TargetPages)
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.TargetPages = targetPages
	targetAPIs, err := version.NormalizeTargetIDs("targets.apis", revision.TargetAPIs)
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.TargetAPIs = targetAPIs
	if err := revision.ValidateDraft(); err != nil {
		return version.RequirementVersion{}, err
	}
	hash, err := revision.ComputeContentHash()
	if err != nil {
		return version.RequirementVersion{}, err
	}
	revision.ContentHash = hash
	return revision, nil
}

func (s *RequirementService) validateTargets(ctx context.Context, versionView VersionView, revision version.RequirementVersion) error {
	boundSUTs := make(map[string]struct{}, len(versionView.Bindings))
	pages := make(map[string]struct{})
	apis := make(map[string]struct{})
	for _, binding := range versionView.Bindings {
		boundSUTs[platformid.Format(binding.ID)] = struct{}{}
		for _, page := range binding.Pages {
			pages[page.ID] = struct{}{}
		}
		for _, api := range binding.APIs {
			apis[api.ID] = struct{}{}
		}
	}
	for _, target := range revision.TargetVersionSUTs {
		if _, ok := boundSUTs[target]; !ok {
			return platform.Invalid("targets.version_suts", fmt.Sprintf("version_sut %s is not bound to the version", target))
		}
	}
	for _, target := range revision.TargetPages {
		if _, ok := pages[target]; ok {
			continue
		}
		if s.resources != nil {
			id, _ := platformid.Parse(target)
			if _, err := s.resources.GetPage(ctx, id); err != nil {
				return targetCatalogError(err, "targets.pages", "page", target)
			}
		}
		return platform.Invalid("targets.pages", fmt.Sprintf("page %s is outside the version scope", target))
	}
	for _, target := range revision.TargetAPIs {
		if _, ok := apis[target]; ok {
			continue
		}
		if s.resources != nil {
			id, _ := platformid.Parse(target)
			if _, err := s.resources.GetAPI(ctx, id); err != nil {
				return targetCatalogError(err, "targets.apis", "api", target)
			}
		}
		return platform.Invalid("targets.apis", fmt.Sprintf("api %s is outside the version scope", target))
	}
	return nil
}

func targetCatalogError(err error, field, kind, rawID string) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return platform.Invalid(field, fmt.Sprintf("%s %s does not exist", kind, rawID))
	}
	return err
}

func (s *RequirementService) detailFromAggregate(aggregate version.RequirementAggregate) (RequirementDetail, error) {
	requirement := aggregate.Requirement
	current, ok := currentVersion(aggregate)
	if !ok {
		return RequirementDetail{}, platform.StateConflict("requirement has no current revision")
	}
	detail := RequirementDetail{
		RequirementView: requirementViewFromRequirement(requirement, current, aggregate.Confirmations),
		CurrentVersion:  toRequirementVersionView(current),
		History:         make([]RequirementVersionView, 0, len(aggregate.Versions)),
		Confirmations:   make([]ConfirmationView, 0, len(aggregate.Confirmations)),
		Relations:       make([]RelationView, 0, len(aggregate.Relations)),
		Attachments:     make([]AttachmentView, 0, len(aggregate.Attachments)),
	}
	for _, revision := range aggregate.Versions {
		if revision.ID == current.ID {
			continue
		}
		detail.History = append(detail.History, toRequirementVersionView(revision))
	}
	sort.Slice(detail.History, func(i, j int) bool { return detail.History[i].Revision > detail.History[j].Revision })
	confirmations := append([]version.Confirmation{}, aggregate.Confirmations...)
	sort.Slice(confirmations, func(i, j int) bool { return confirmations[i].ID > confirmations[j].ID })
	for _, confirmation := range confirmations {
		detail.Confirmations = append(detail.Confirmations, toConfirmationView(confirmation))
	}
	for _, relation := range aggregate.Relations {
		detail.Relations = append(detail.Relations, toRelationView(relation))
	}
	for _, attachment := range aggregate.Attachments {
		detail.Attachments = append(detail.Attachments, toAttachmentView(attachment))
	}
	return detail, nil
}

func requirementViewFromListItem(item version.RequirementListItem) RequirementView {
	view := requirementViewFromRequirement(item.Requirement, item.Current, nil)
	if item.ConfirmedAt != nil {
		confirmedAt := *item.ConfirmedAt
		view.ConfirmedAt = &confirmedAt
	}
	return view
}

func requirementViewFromRequirement(requirement version.Requirement, current version.RequirementVersion, confirmations []version.Confirmation) RequirementView {
	view := RequirementView{
		ID:                requirement.ID,
		CurrentVersionID:  current.ID,
		Revision:          current.Revision,
		Title:             current.Title,
		Status:            string(requirement.Status),
		Priority:          current.Priority,
		TargetVersionSUTs: nonNilStrings(current.TargetVersionSUTs),
		TargetPages:       nonNilStrings(current.TargetPages),
		TargetAPIs:        nonNilStrings(current.TargetAPIs),
		UpdatedAt:         requirement.UpdatedAt,
	}
	for _, confirmation := range confirmations {
		if confirmation.RequirementVersionID == current.ID {
			confirmedAt := confirmation.ConfirmedAt
			view.ConfirmedAt = &confirmedAt
			break
		}
	}
	return view
}

func currentVersion(aggregate version.RequirementAggregate) (version.RequirementVersion, bool) {
	if aggregate.Requirement.CurrentVersionID == nil {
		return version.RequirementVersion{}, false
	}
	currentID := *aggregate.Requirement.CurrentVersionID
	for _, revision := range aggregate.Versions {
		if revision.ID == currentID {
			return revision, true
		}
	}
	return version.RequirementVersion{}, false
}

func mapRequirementRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrRequirementNotFound):
		return platform.NotFound("requirement")
	case errors.Is(err, version.ErrStateConflict):
		return platform.StateConflict("requirement is not in a mutable state")
	case errors.Is(err, version.ErrRelationDuplicate):
		return platform.Conflict("requirement relation already exists")
	default:
		return err
	}
}

func (s *RequirementService) markRunning(ctx context.Context, versionID int64) {
	// The reconciler is the fallback if this best-effort projection fails.
	_ = s.versions.MarkRunning(ctx, versionID)
}

func (s *RequirementService) publishEvent(ctx context.Context, versionID int64, eventType string, payload, scope map[string]any) {
	if s.events == nil {
		return
	}
	mergedScope := map[string]any{"version_id": platformid.Format(versionID)}
	for key, value := range scope {
		mergedScope[key] = value
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     mergedScope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

func (s *RequirementService) recordAudit(ctx context.Context, actorID int64, action, targetKind, targetID string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	actor := actorID
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    &actor,
		Action:     action,
		TargetKind: targetKind,
		TargetID:   targetID,
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}

func optionalActor(actorID int64) *int64 {
	if actorID <= 0 {
		return nil
	}
	value := actorID
	return &value
}

func validateIDs(versionID, requirementID int64) error {
	if versionID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if requirementID <= 0 {
		return platform.Invalid("rid", "must be a positive decimal ID")
	}
	return nil
}

func requirementStatusFilter(raw string) (version.RequirementStatus, error) {
	status := version.RequirementStatus(strings.TrimSpace(raw))
	switch status {
	case "":
		return "", nil
	case version.RequirementDraft, version.RequirementClarified, version.RequirementClosed, version.RequirementObsolete:
		return status, nil
	default:
		return "", platform.Invalid("status", "must be one of draft, clarified, closed, obsolete")
	}
}

func dedupeRequirementIDs(raw []int64) ([]int64, error) {
	if len(raw) == 0 {
		return nil, platform.Invalid("requirement_ids", "must contain at least one requirement ID")
	}
	if len(raw) > version.MaxBatchConfirm {
		return nil, platform.Invalid("requirement_ids", fmt.Sprintf("must contain at most %d requirement IDs", version.MaxBatchConfirm))
	}
	ids := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for _, id := range raw {
		if id <= 0 {
			return nil, platform.Invalid("requirement_ids", "must contain positive decimal IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func withRequirementID(err error, requirementID int64) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		if apiErr.Details == nil {
			apiErr.Details = map[string]any{}
		}
		apiErr.Details["requirement_id"] = platformid.Format(requirementID)
		return apiErr
	}
	return err
}

func validationStringMax(field, value string, max int) error {
	if len([]rune(value)) > max {
		return platform.Invalid(field, fmt.Sprintf("must be at most %d characters", max))
	}
	return nil
}
