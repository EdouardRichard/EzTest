package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// resourceStatusActive is the P2 lifecycle status of a usable resource. P3
// does not import the P2 domain, so the value is repeated here as a string.
const resourceStatusActive = "active"

// CreateVersionCommand is the create-version request (spec §7.2). All binding
// fields are optional; a repository binding without codeBranch uses the
// repository's fixedBranch. The branch is always resolved through GitReader to
// validate it; an explicit codeBaselineCommit is kept as the snapshot instead
// of the resolved head.
type CreateVersionCommand struct {
	Name               string
	Label              string
	Description        string
	SystemID           *int64
	EnvironmentID      *int64
	CodeRepositoryID   *int64
	CodeBranch         *string
	CodeBaselineCommit *string
}

// VersionService implements the version use cases: create/list/get, metadata
// and binding updates, and the two P3 state actions (start-clarification and
// enter-design). Requirement operations never call into this service to change
// a status (spec §6.1/INV-2/INV-4).
type VersionService struct {
	repo      VersionRepository
	resources ResourceCatalog
	git       GitReader
	events    EventPublisher
	ids       IDGenerator
}

// VersionServiceOption customises the service dependencies. Production wires
// the process-wide Snowflake generator; tests inject a fixed one.
type VersionServiceOption func(*VersionService)

// WithIDGenerator injects the Snowflake ID generator. P3 ids are generated in
// the application layer and passed explicitly to the repository (spec §5.4).
// The composition root should inject one shared id.NewSnowflake(cfg.SnowflakeNodeID)
// for the whole process (T16); without it, the package-wide DefaultIDGenerator
// is used so services in one process cannot mint colliding ids.
func WithIDGenerator(ids IDGenerator) VersionServiceOption {
	return func(s *VersionService) {
		if ids != nil {
			s.ids = ids
		}
	}
}

// defaultIDGenerator is the process-wide fallback Snowflake generator. It is a
// sync.OnceValue so every VersionService constructed without WithIDGenerator
// shares one generator, and therefore one node/sequence state. A per-service
// fallback would let two services started in the same millisecond mint
// identical ids (both begin at node 1, sequence 0).
var defaultIDGenerator = sync.OnceValue(func() IDGenerator {
	generator, err := id.NewSnowflake(1)
	if err != nil {
		panic(fmt.Sprintf("version: default snowflake generator: %v", err))
	}
	return generator
})

// DefaultIDGenerator returns the process-wide fallback Snowflake generator
// (node 1). It is shared by every VersionService created without
// WithIDGenerator. T9/T10 services may use it the same way.
//
// Production composition (T16) must replace it with one shared
// id.NewSnowflake(cfg.SnowflakeNodeID) injected through WithIDGenerator: node 1
// is only a safe fallback for a single process, while multi-instance
// deployments need a node id unique per running instance.
func DefaultIDGenerator() IDGenerator {
	return defaultIDGenerator()
}

// NewVersionService wires the version use cases to their repository, the P2
// resource catalog, the Git reader and the event publisher. The optional
// WithIDGenerator injects the process-wide Snowflake generator; T16 must inject
// one shared id.NewSnowflake(cfg.SnowflakeNodeID) so every service and every
// instance uses a distinct node id.
func NewVersionService(repo VersionRepository, resources ResourceCatalog, git GitReader, events EventPublisher, opts ...VersionServiceOption) *VersionService {
	service := &VersionService{
		repo:      repo,
		resources: resources,
		git:       git,
		events:    events,
		ids:       DefaultIDGenerator(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Create validates the optional bindings, snapshots the code baseline and
// inserts a new draft version (spec §3.3/§6.8). A successful create publishes
// version.created.
func (s *VersionService) Create(ctx context.Context, cmd CreateVersionCommand, actor int64) (*Version, error) {
	bindings, err := s.resolveBindings(ctx, VersionBindings{
		SystemID:           cmd.SystemID,
		EnvironmentID:      cmd.EnvironmentID,
		CodeRepositoryID:   cmd.CodeRepositoryID,
		CodeBranch:         cmd.CodeBranch,
		CodeBaselineCommit: cmd.CodeBaselineCommit,
	})
	if err != nil {
		return nil, err
	}

	version := &Version{
		ID:                 s.ids.Next(),
		Name:               cmd.Name,
		Label:              cmd.Label,
		Description:        cmd.Description,
		Status:             domain.VersionDraft,
		SystemID:           bindings.SystemID,
		EnvironmentID:      bindings.EnvironmentID,
		CodeRepositoryID:   bindings.CodeRepositoryID,
		CodeBranch:         bindings.CodeBranch,
		CodeBaselineCommit: bindings.CodeBaselineCommit,
		Revision:           1,
		CreatedBy:          actor,
		UpdatedBy:          actor,
	}
	if version.ID <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}
	if err := version.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, version); err != nil {
		return nil, err
	}

	s.publish(ctx, VersionEvent{
		VersionID: version.ID,
		Type:      "version.created",
		Summary:   "version created",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"status": version.Status},
	})
	return version, nil
}

// resolveBindings validates and completes a binding set: it checks the P2
// resources, applies the repository's fixed branch when the request omits one,
// validates the branch through the Git reader, keeps an explicit baseline
// commit (otherwise snapshots the resolved head), and finally enforces the
// domain binding rules.
func (s *VersionService) resolveBindings(ctx context.Context, b VersionBindings) (VersionBindings, error) {
	// Structural rules that do not need external lookups.
	if b.EnvironmentID != nil && b.SystemID == nil {
		return b, domain.NewInvalid("environmentId", "environment_system_mismatch",
			"an environment requires a system binding")
	}
	if b.CodeRepositoryID == nil && (b.CodeBranch != nil || b.CodeBaselineCommit != nil) {
		return b, domain.NewInvalid("codeRepositoryId", "invalid_bindings",
			"codeRepositoryId, codeBranch and codeBaselineCommit must be set or cleared together")
	}

	if b.SystemID != nil {
		if err := s.checkSystem(ctx, *b.SystemID); err != nil {
			return b, err
		}
	}
	if b.EnvironmentID != nil {
		if err := s.checkEnvironment(ctx, *b.EnvironmentID, b.SystemID); err != nil {
			return b, err
		}
	}
	if b.CodeRepositoryID != nil {
		resolved, err := s.resolveRepositoryBinding(ctx, *b.CodeRepositoryID, b.CodeBranch, b.CodeBaselineCommit)
		if err != nil {
			return b, err
		}
		b.CodeBranch = resolved.CodeBranch
		b.CodeBaselineCommit = resolved.CodeBaselineCommit
	}

	if err := domain.ValidateBindings(b.SystemID, b.EnvironmentID, b.CodeRepositoryID, b.CodeBranch, b.CodeBaselineCommit); err != nil {
		return b, err
	}
	return b, nil
}

// checkSystem verifies that a bound system exists and is active.
func (s *VersionService) checkSystem(ctx context.Context, systemID int64) error {
	if s.resources == nil {
		return errors.New("version: resource catalog is not configured")
	}
	ref, err := s.resources.GetSystem(ctx, systemID)
	if err != nil {
		return mapResourceLookupError("systemId", err)
	}
	if !ref.Available {
		return domain.NewInvalid("systemId", "system_not_found",
			fmt.Sprintf("system %d not found", systemID))
	}
	if ref.Status != "" && ref.Status != resourceStatusActive {
		return domain.NewInvalid("systemId", "system_disabled",
			fmt.Sprintf("system %d is disabled", systemID))
	}
	return nil
}

// checkEnvironment verifies that a bound environment exists, is active and
// belongs to the bound system.
func (s *VersionService) checkEnvironment(ctx context.Context, environmentID int64, systemID *int64) error {
	if s.resources == nil {
		return errors.New("version: resource catalog is not configured")
	}
	ref, err := s.resources.GetEnvironment(ctx, environmentID)
	if err != nil {
		return mapResourceLookupError("environmentId", err)
	}
	if !ref.Available {
		return domain.NewInvalid("environmentId", "environment_not_found",
			fmt.Sprintf("environment %d not found", environmentID))
	}
	if ref.Status != "" && ref.Status != resourceStatusActive {
		return domain.NewInvalid("environmentId", "environment_not_found",
			fmt.Sprintf("environment %d is not active", environmentID))
	}
	// Fail closed: the catalog adapter must report the environment's owning
	// system. An environment whose ownership cannot be verified is rejected
	// instead of being bound to an arbitrary system, and a reported owner must
	// match the bound system (spec §6.8).
	if ref.SystemID == nil {
		return domain.NewInvalid("environmentId", "environment_system_mismatch",
			fmt.Sprintf("environment %d does not report its owning system", environmentID))
	}
	if systemID == nil || *ref.SystemID != *systemID {
		return domain.NewInvalid("environmentId", "environment_system_mismatch",
			fmt.Sprintf("environment %d does not belong to the bound system", environmentID))
	}
	return nil
}

// resolveRepositoryBinding verifies a bound repository and returns the branch
// and baseline commit snapshot of the binding.
func (s *VersionService) resolveRepositoryBinding(ctx context.Context, repositoryID int64, branch, commit *string) (VersionBindings, error) {
	if s.resources == nil {
		return VersionBindings{}, errors.New("version: resource catalog is not configured")
	}
	ref, err := s.resources.GetCodeRepository(ctx, repositoryID)
	if err != nil {
		return VersionBindings{}, mapResourceLookupError("codeRepositoryId", err)
	}
	if !ref.Available {
		return VersionBindings{}, domain.NewInvalid("codeRepositoryId", "code_repository_not_found",
			fmt.Sprintf("code repository %d not found", repositoryID))
	}
	if ref.Status != "" && ref.Status != resourceStatusActive {
		return VersionBindings{}, domain.NewInvalid("codeRepositoryId", "code_repository_disabled",
			fmt.Sprintf("code repository %d is disabled", repositoryID))
	}

	resolvedBranch := ref.FixedBranch
	if branch != nil && strings.TrimSpace(*branch) != "" {
		resolvedBranch = *branch
	}
	if strings.TrimSpace(resolvedBranch) == "" {
		return VersionBindings{}, domain.NewInvalid("codeBranch", "branch_not_found",
			"a repository binding requires a codeBranch or a fixed branch")
	}

	resolvedCommit := commit
	if s.git == nil {
		return VersionBindings{}, errors.New("version: git reader is not configured")
	}
	head, err := s.git.ResolveCommit(ctx, ref.LocalPath, resolvedBranch)
	if err != nil {
		return VersionBindings{}, mapGitError(err)
	}
	if resolvedCommit == nil {
		resolvedCommit = &head
	}

	return VersionBindings{
		CodeBranch:         &resolvedBranch,
		CodeBaselineCommit: resolvedCommit,
	}, nil
}

// mapResourceLookupError translates a ResourceCatalog adapter error into the
// version domain's 422 validation error, preserving the stable reason. Unknown
// errors are returned unchanged so they surface as internal failures.
func mapResourceLookupError(field string, err error) error {
	var resourceErr *ResourceError
	if errors.As(err, &resourceErr) && resourceErr.Reason != "" {
		return domain.NewInvalid(field, resourceErr.Reason, resourceErr.Error())
	}
	return err
}

// mapGitError translates a GitReader adapter error into the version domain's
// 422 validation error, preserving the stable branch/path reason.
func mapGitError(err error) error {
	var gitErr *GitError
	if errors.As(err, &gitErr) && gitErr.Reason != "" {
		field := "codeRepositoryId"
		if gitErr.Reason == "branch_not_found" {
			field = "codeBranch"
		}
		return domain.NewInvalid(field, gitErr.Reason, gitErr.Error())
	}
	return err
}

// Get returns one version by id.
func (s *VersionService) Get(ctx context.Context, id int64) (*Version, error) {
	return s.repo.Get(ctx, id)
}

// List returns one page of versions ordered by updated_at DESC, id DESC (spec
// §7.2). Pagination and status filtering are the repository's job.
func (s *VersionService) List(ctx context.Context, filter VersionFilter) ([]Version, bool, error) {
	return s.repo.List(ctx, filter)
}

// UpdateMetadata rewrites name/label/description under the optimistic revision
// lock (spec §6.1/§6.10). Any status allows a metadata edit; the status itself
// never changes here.
func (s *VersionService) UpdateMetadata(ctx context.Context, id int64, expected int, name, label, desc string, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Revision != expected {
		return nil, revisionConflictError(id, expected, current.Revision)
	}

	candidate := *current
	candidate.Name, candidate.Label, candidate.Description = name, label, desc
	if err := candidate.Validate(); err != nil {
		return nil, err
	}

	updated, err := s.repo.UpdateMetadata(ctx, id, expected, name, label, desc, actor)
	if err != nil {
		return nil, err
	}
	changed := make([]string, 0, 3)
	if current.Name != updated.Name {
		changed = append(changed, "name")
	}
	if current.Label != updated.Label {
		changed = append(changed, "label")
	}
	if current.Description != updated.Description {
		changed = append(changed, "description")
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.updated",
		Summary:   "version metadata updated",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"changed": changed},
	})
	return updated, nil
}

// UpdateBindings replaces the whole binding set under the optimistic revision
// lock (spec §6.1/§7.2). Bindings are mutable only while the version is draft or
// clarifying; designing returns a version_state_conflict and never changes the
// baseline snapshot in place (INV-13).
func (s *VersionService) UpdateBindings(ctx context.Context, id int64, expected int, bindings VersionBindings, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Revision != expected {
		return nil, revisionConflictError(id, expected, current.Revision)
	}
	if current.Status != domain.VersionDraft && current.Status != domain.VersionClarifying {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, bindings can only change in draft or clarifying", id, current.Status))
	}

	resolved, err := s.resolveBindings(ctx, bindings)
	if err != nil {
		return nil, err
	}

	updated, err := s.repo.UpdateBindings(ctx, id, expected, resolved, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.updated",
		Summary:   "version bindings updated",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"changed": changedBindingFields(current, updated)},
	})
	return updated, nil
}

// revisionConflictError builds the spec §7.1 revision_conflict 409.
func revisionConflictError(id int64, expected, actual int) error {
	return domain.NewStateConflict("revision_conflict",
		fmt.Sprintf("version %d revision is %d, expected %d", id, actual, expected))
}

// changedBindingFields lists the binding columns a whole-set replacement
// actually changed, for the version.updated event's changed detail.
func changedBindingFields(before, after *Version) []string {
	changed := make([]string, 0, 5)
	if !sameInt64(before.SystemID, after.SystemID) {
		changed = append(changed, "systemId")
	}
	if !sameInt64(before.EnvironmentID, after.EnvironmentID) {
		changed = append(changed, "environmentId")
	}
	if !sameInt64(before.CodeRepositoryID, after.CodeRepositoryID) {
		changed = append(changed, "codeRepositoryId")
	}
	if !sameString(before.CodeBranch, after.CodeBranch) {
		changed = append(changed, "codeBranch")
	}
	if !sameString(before.CodeBaselineCommit, after.CodeBaselineCommit) {
		changed = append(changed, "codeBaselineCommit")
	}
	return changed
}

// sameInt64 compares two optional int64 values.
func sameInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// sameString compares two optional string values.
func sameString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// UnclarifiedRequirementsError is the enter-design soft gate error (HTTP 409
// reason unclarified_requirements). It wraps the domain error (so
// errors.As(err, **domain.Error) still works) and carries the warnings the HTTP
// layer renders in details.unclarifiedRequirementIds/openQuestionCount.
type UnclarifiedRequirementsError struct {
	Cause    *domain.Error
	Warnings Warnings
}

// Error implements error.
func (e *UnclarifiedRequirementsError) Error() string {
	if e.Cause == nil {
		return "unclarified requirements"
	}
	return e.Cause.Error()
}

// Unwrap exposes the wrapped domain error to errors.As/errors.Is.
func (e *UnclarifiedRequirementsError) Unwrap() error { return e.Cause }

// Details returns the spec §7.2 details payload of the soft gate. P3-owned ids
// are decimal strings in JSON (spec §7.1), so the helper is directly
// JSON-ready for the HTTP layer.
func (e *UnclarifiedRequirementsError) Details() map[string]any {
	ids := make([]string, len(e.Warnings.UnclarifiedRequirementIDs))
	for i, id := range e.Warnings.UnclarifiedRequirementIDs {
		ids[i] = fmt.Sprintf("%d", id)
	}
	return map[string]any{
		"unclarifiedRequirementIds": ids,
		"openQuestionCount":         e.Warnings.OpenQuestionCount,
	}
}

// StartClarification moves a draft version to clarifying (spec §6.1). At least
// one requirement must already exist; requirement operations never call this
// action themselves (INV-4).
func (s *VersionService) StartClarification(ctx context.Context, id int64, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.VersionDraft {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, start-clarification requires draft", id, current.Status))
	}
	counts, err := s.repo.Counts(ctx, id)
	if err != nil {
		return nil, err
	}
	if counts.RequirementCount < 1 {
		return nil, domain.NewInvalid("", "requirements_required",
			"start-clarification requires at least one requirement")
	}

	updated, err := s.repo.UpdateStatus(ctx, id, domain.VersionDraft, domain.VersionClarifying, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.status_changed",
		Summary:   "version status changed from draft to clarifying",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"from": domain.VersionDraft, "to": domain.VersionClarifying},
	})
	return updated, nil
}

// EnterDesign moves a clarifying version to designing (spec §6.1). Unclarified
// requirements or version-level open questions are a soft gate: the action is
// rejected unless acknowledge is true, and the warnings are returned either
// way. Requirement operations never change the version status themselves.
func (s *VersionService) EnterDesign(ctx context.Context, id int64, acknowledge bool, actor int64) (*Version, Warnings, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, Warnings{}, err
	}
	if current.Status != domain.VersionClarifying {
		return nil, Warnings{}, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, enter-design requires clarifying", id, current.Status))
	}

	unclarified, openQuestions, err := s.repo.ListUnclarified(ctx, id)
	if err != nil {
		return nil, Warnings{}, err
	}
	warnings := Warnings{
		UnclarifiedRequirementIDs: unclarified,
		OpenQuestionCount:         openQuestions,
	}
	if (len(warnings.UnclarifiedRequirementIDs) > 0 || warnings.OpenQuestionCount > 0) && !acknowledge {
		return nil, warnings, &UnclarifiedRequirementsError{
			Cause: domain.NewStateConflict("unclarified_requirements",
				"unclarified requirements or open questions must be acknowledged"),
			Warnings: warnings,
		}
	}

	updated, err := s.repo.UpdateStatus(ctx, id, domain.VersionClarifying, domain.VersionDesigning, actor)
	if err != nil {
		return nil, warnings, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.status_changed",
		Summary:   "version status changed from clarifying to designing",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"from": domain.VersionClarifying, "to": domain.VersionDesigning},
	})
	return updated, warnings, nil
}

// publish emits one event after a committed write. Publication failures are
// logged and swallowed: clients read the authoritative state from the API and
// the event stream self-heals on refresh (spec §6.10).
func (s *VersionService) publish(ctx context.Context, event VersionEvent) {
	if s.events == nil {
		return
	}
	if err := s.events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "version event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}
