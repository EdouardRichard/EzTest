package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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

// executionStateRepository is the column-scoped P6 execution-state port used by
// MarkPaused, SetBatchNo, AllocateBatchNo, ReleaseBatchNo and CompleteExecution.
// Pausing, batch-number and completion writes are deliberately separate
// operations: each UPDATE is scoped to its own columns, so a stale read of one
// field can never roll back a concurrent write of another. The PostgreSQL
// repository implements this alongside the broader VersionRepository port.
type executionStateRepository interface {
	// SetPaused writes only paused and pause_mode.
	SetPaused(ctx context.Context, id int64, paused bool, pauseMode string, actor int64) (*Version, error)
	// SetBatchNo writes only batch_no under a monotonic SQL guard: a lower
	// number is rejected as a version_state_conflict.
	SetBatchNo(ctx context.Context, id int64, batchNo int, actor int64) (*Version, error)
	// AllocateBatchNo atomically increments the batch_no of an executing or
	// continuing version with a single UPDATE ... RETURNING and returns the new
	// number.
	AllocateBatchNo(ctx context.Context, id int64, actor int64) (int, error)
	// ReleaseBatchNo compensates a failed create: it decrements batch_no only
	// while it still equals allocated, and is a no-op otherwise.
	ReleaseBatchNo(ctx context.Context, id int64, allocated int, actor int64) error
	// CompleteExecution performs the executing -> completed compare-and-swap
	// and refuses a paused version.
	CompleteExecution(ctx context.Context, id int64, actor int64) (*Version, error)
}

// continuingStateRepository is the column-scoped P7 continuing port. It is
// detected from the wired VersionRepository at construction exactly like
// executionStateRepository, so adapters/fakes that predate P7 keep compiling.
// Each write is its own CAS and reports whether it actually transitioned:
//   - MarkContinuing: completed -> continuing/clarifying
//   - SetContinuePhase: continuing-only explicit phase write
//   - RefreshContinuePhase: atomic phase derivation from the requirement
//     statuses (all non-draft -> designing, otherwise clarifying), never
//     interrupting phase=executing
//   - CompleteContinuing: continuing -> completed
//   - Finalize: executing or continuing -> completed (spec §5.1 rule 5)
type continuingStateRepository interface {
	MarkContinuing(ctx context.Context, id int64, actor int64) (*Version, bool, error)
	SetContinuePhase(ctx context.Context, id int64, phase string, actor int64) (*Version, error)
	RefreshContinuePhase(ctx context.Context, id int64, actor int64) (*Version, bool, error)
	CompleteContinuing(ctx context.Context, id int64, actor int64) (*Version, bool, error)
	Finalize(ctx context.Context, id int64, actor int64) (*Version, bool, error)
}

// changeFeedRepository is the optional read-time derivation port of
// GET /versions/{id}/changes (spec §7.2/D50). It returns every derived item of
// the version; the service sorts and pages them.
type changeFeedRepository interface {
	Changes(ctx context.Context, versionID int64) ([]domain.VersionChange, error)
}

// FinalizeBatchState is the execution-side projection the finalize decision
// evaluates (spec §5.1/§6.2): total/active/unsettled/incremental batch counts.
// The version service consumes it through FinalizeBatchReader; the PostgreSQL
// version repository implements that port as a read-only run_batch projection,
// and the composition root may override it with an execution-context adapter.
type FinalizeBatchState struct {
	// TotalBatches counts every batch of the version.
	TotalBatches int
	// ActiveBatches counts planned/running batches.
	ActiveBatches int
	// UnsettledBatches counts terminal batches with autonomy_done=false.
	UnsettledBatches int
	// IncrementalBatches counts continuing-time incremental batches.
	IncrementalBatches int
}

// FinalizeBatchReader is the execution-side port of the finalize conditions.
type FinalizeBatchReader interface {
	FinalizeBatchState(ctx context.Context, versionID int64) (FinalizeBatchState, error)
}

// VersionAuditEvent is one version lifecycle audit entry (spec §11.2 D52). Only
// ids/status/phase are recorded; no request payload is ever included.
type VersionAuditEvent struct {
	// Action is the stable audit action name, for example version.continued.
	Action    string
	VersionID int64
	From      string
	To        string
	Phase     string
	ActorID   int64
	// TargetType and TargetID optionally name the written aggregate. When they
	// are empty the sink records the version itself.
	TargetType string
	TargetID   string
	// Metadata holds non-sensitive details such as status, revision or the
	// names of changed fields. It must never contain titles, descriptions,
	// prompts, files or credentials.
	Metadata map[string]any
}

// VersionAuditSink records version lifecycle audit entries. Like event
// publication, a failure is logged but never fails the committed business
// write. The concrete adapter is wired by the composition root.
type VersionAuditSink interface {
	Record(ctx context.Context, event VersionAuditEvent) error
}

// VersionService implements the version use cases: create/list/get, metadata
// and binding updates, the two P3 state actions (start-clarification and
// enter-design) and the P7 continuing lifecycle (D43). Requirement operations
// never call into this service to change a status (spec §6.1/INV-2/INV-4).
type VersionService struct {
	repo       VersionRepository
	execution  executionStateRepository
	continuing continuingStateRepository
	changes    changeFeedRepository
	finalizer  FinalizeBatchReader
	audit      VersionAuditSink
	resources  ResourceCatalog
	git        GitReader
	events     EventPublisher
	ids        IDGenerator
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

// WithFinalizeBatchReader overrides the execution-side finalize projection. The
// PostgreSQL repository implements FinalizeBatchReader itself, so production
// needs no extra wiring; adapters and tests use this option. TryFinalize fails
// closed when neither is available.
func WithFinalizeBatchReader(reader FinalizeBatchReader) VersionServiceOption {
	return func(s *VersionService) {
		if reader != nil {
			s.finalizer = reader
		}
	}
}

// WithVersionAuditSink injects the version lifecycle audit sink (spec §11.2).
// Audit failures are logged and swallowed (P6 semantics).
func WithVersionAuditSink(sink VersionAuditSink) VersionServiceOption {
	return func(s *VersionService) {
		if sink != nil {
			s.audit = sink
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
	if execution, ok := repo.(executionStateRepository); ok {
		service.execution = execution
	}
	if continuing, ok := repo.(continuingStateRepository); ok {
		service.continuing = continuing
	}
	if changes, ok := repo.(changeFeedRepository); ok {
		service.changes = changes
	}
	// The PostgreSQL repository implements the execution-side finalize projection
	// itself (read-only run_batch counts); the option below overrides it for
	// adapters and tests.
	if finalizer, ok := repo.(FinalizeBatchReader); ok {
		service.finalizer = finalizer
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// executionRepo returns the column-scoped execution-state port, or an error
// when the wired repository predates the split.
func (s *VersionService) executionRepo() (executionStateRepository, error) {
	if s.execution == nil {
		return nil, errors.New("version: repository does not support column-scoped execution-state writes")
	}
	return s.execution, nil
}

// continuingRepo returns the column-scoped continuing port, or an error when the
// wired repository predates P7.
func (s *VersionService) continuingRepo() (continuingStateRepository, error) {
	if s.continuing == nil {
		return nil, errors.New("version: repository does not support the continuing lifecycle writes")
	}
	return s.continuing, nil
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
		PauseMode:          domain.PauseModeGraceful,
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
	s.recordAudit(ctx, VersionAuditEvent{
		Action:     "version.created",
		VersionID:  version.ID,
		TargetType: "version",
		TargetID:   fmt.Sprintf("%d", version.ID),
		ActorID:    actor,
		Metadata:   map[string]any{"status": version.Status},
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
	s.recordAudit(ctx, VersionAuditEvent{
		Action:     "version.updated",
		VersionID:  id,
		TargetType: "version",
		TargetID:   fmt.Sprintf("%d", id),
		ActorID:    actor,
		Metadata:   map[string]any{"changed": changed, "revision": updated.Revision},
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

// EnterReview moves a designing version to reviewing (spec §6.3). It is called
// on a successful design generation (auto) or by submit-review (manual). When
// the version is already reviewing the call is idempotent: it returns the
// current version and must not publish a duplicate event. This includes losing
// the compare-and-swap to a concurrent reviewer, which is re-read and treated
// as the same idempotent success.
func (s *VersionService) EnterReview(ctx context.Context, id int64, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status == domain.VersionReviewing {
		return current, nil
	}
	if current.Status != domain.VersionDesigning {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, enter-review requires designing", id, current.Status))
	}

	updated, err := s.repo.UpdateStatus(ctx, id, domain.VersionDesigning, domain.VersionReviewing, actor)
	if err != nil {
		// The CAS may have lost to a concurrent reviewer that already committed
		// reviewing; re-read and return it instead of a spurious conflict.
		latest, readErr := s.repo.Get(ctx, id)
		if readErr == nil && latest.Status == domain.VersionReviewing {
			return latest, nil
		}
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.status_changed",
		Summary:   "version status changed from designing to reviewing",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"from": domain.VersionDesigning, "to": domain.VersionReviewing},
	})
	return updated, nil
}

// RejectReview returns a reviewing version to designing (spec §6.3). The
// mindmap context writes the review decision; this action only moves the
// version state so the graph stays editable.
func (s *VersionService) RejectReview(ctx context.Context, id int64, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.VersionReviewing {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, reject-review requires reviewing", id, current.Status))
	}

	updated, err := s.repo.UpdateStatus(ctx, id, domain.VersionReviewing, domain.VersionDesigning, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.status_changed",
		Summary:   "version status changed from reviewing to designing",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"from": domain.VersionReviewing, "to": domain.VersionDesigning},
	})
	return updated, nil
}

// ApproveBaseline moves a reviewing version to executing (spec §6.3/§6.4). The
// mindmap context freezes the case baseline around this call; this action only
// drives the version state and never updates the mindmap tables. Once the
// version is executing, a second approve is a version_state_conflict.
func (s *VersionService) ApproveBaseline(ctx context.Context, id int64, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.VersionReviewing {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, approve-baseline requires reviewing", id, current.Status))
	}

	updated, err := s.repo.UpdateStatus(ctx, id, domain.VersionReviewing, domain.VersionExecuting, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.status_changed",
		Summary:   "version status changed from reviewing to executing",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    map[string]any{"from": domain.VersionReviewing, "to": domain.VersionExecuting},
	})
	return updated, nil
}

// MarkPaused sets or clears the execution pause flag of an executing or
// continuing version (spec §5.3/§5.4). Pausing never changes the version
// status: only the batch drives case scheduling, the version flag mirrors it
// for the detail view. The write is scoped to paused/pause_mode and never
// touches batch_no, so a concurrent SetBatchNo cannot be rolled back by this
// action's earlier read. The action publishes the existing
// version.status_changed event with from/to pinned to the unchanged status and
// the new paused flag in detail; reason is carried for audit only and is not
// persisted on the version row. Resuming keeps the stored pause mode.
func (s *VersionService) MarkPaused(ctx context.Context, id int64, paused bool, mode, reason string, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.VersionExecuting && current.Status != domain.VersionContinuing {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, pause requires executing or continuing", id, current.Status))
	}

	pauseMode := current.PauseMode
	if pauseMode == "" {
		pauseMode = domain.PauseModeGraceful
	}
	if paused {
		if err := domain.ValidatePauseMode(mode); err != nil {
			return nil, err
		}
		pauseMode = mode
	}

	repo, err := s.executionRepo()
	if err != nil {
		return nil, err
	}
	updated, err := repo.SetPaused(ctx, id, paused, pauseMode, actor)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{
		"from":   current.Status,
		"to":     updated.Status,
		"paused": paused,
	}
	if pauseMode != "" {
		detail["pauseMode"] = pauseMode
	}
	if reason != "" {
		detail["reason"] = reason
	}
	summary := "version execution paused"
	if !paused {
		summary = "version execution resumed"
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.status_changed",
		Summary:   summary,
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail:    detail,
	})
	return updated, nil
}

// SetBatchNo records the latest batch number of an executing or continuing
// version. The write is scoped to batch_no and never touches paused/pause_mode,
// and the repository enforces the monotonic guard at write time (batch_no <=
// $new): a lower number is a version_state_conflict even when the service's
// earlier read saw a smaller value, so two racing batch writes can never move
// the column backwards. The same number is an idempotent no-op in the service
// (so a retried create is safe), and the action does not publish an event (batch
// events carry the batch lifecycle).
func (s *VersionService) SetBatchNo(ctx context.Context, id int64, batchNo int, actor int64) (*Version, error) {
	if batchNo < 0 {
		return nil, domain.NewInvalid("batchNo", "invalid_batch_no", "batchNo must not be negative")
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if batchNo < current.BatchNo {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d batchNo is %d, cannot decrease to %d", id, current.BatchNo, batchNo))
	}
	if batchNo == current.BatchNo {
		return current, nil
	}

	repo, err := s.executionRepo()
	if err != nil {
		return nil, err
	}
	return repo.SetBatchNo(ctx, id, batchNo, actor)
}

// AllocateBatchNo atomically increments an executing or continuing version's
// batch_no and returns the new number (spec §5.3). Both statuses allocate: a
// baseline/supplement batch belongs to an executing version and a P7 incremental
// batch is created on a continuing one. Unlike SetBatchNo it takes no actor: the
// execution context's native-allocator port has none, so the write is attributed
// to the system actor 0. The repository performs the whole increment in one
// UPDATE ... RETURNING under a status guard, so two concurrent batch creators
// always receive distinct numbers; a missing version is not_found and any other
// status (draft through completed) is a version_state_conflict. The action
// publishes no event (batch events carry the batch lifecycle).
func (s *VersionService) AllocateBatchNo(ctx context.Context, id int64) (int, error) {
	repo, err := s.executionRepo()
	if err != nil {
		return 0, err
	}
	return repo.AllocateBatchNo(ctx, id, 0)
}

// ReleaseBatchNo compensates a failed batch create by decrementing batch_no
// only while the stored value still equals allocated, so a concurrent creator's
// newer allocation is never rolled back. A missing row or a mismatched number is
// a tolerated no-op: numbers are monotonic rather than gapless. The action
// publishes no event.
func (s *VersionService) ReleaseBatchNo(ctx context.Context, id int64, allocated int) error {
	repo, err := s.executionRepo()
	if err != nil {
		return err
	}
	return repo.ReleaseBatchNo(ctx, id, allocated, 0)
}

// CompleteExecution moves an unpaused executing version to completed (spec
// §6.2). The continuing -> completed transition belongs to P7, so a continuing
// version is a version_state_conflict here; every other source state, including
// a paused executing version, is rejected the same way. The repository repeats
// the guard in SQL (status = executing AND paused = false), so a pause that
// commits between this read and the write also loses as a version_state_conflict
// rather than completing a paused version. The action publishes
// version.status_changed.
func (s *VersionService) CompleteExecution(ctx context.Context, id int64, actor int64) (*Version, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.VersionExecuting {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, complete-execution requires executing", id, current.Status))
	}
	if current.Paused {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is paused, complete-execution requires an unpaused executing version", id))
	}

	repo, err := s.executionRepo()
	if err != nil {
		return nil, err
	}
	updated, err := repo.CompleteExecution(ctx, id, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID: id,
		Type:      "version.status_changed",
		Summary:   "version status changed from executing to completed",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail: map[string]any{
			"from":   domain.VersionExecuting,
			"to":     domain.VersionCompleted,
			"paused": updated.Paused,
		},
	})
	return updated, nil
}

// MarkContinuing moves a completed version to continuing/clarifying (D43/spec
// §5.1). The repository performs the completed -> continuing CAS; losing it to a
// concurrent requirement create that already moved the version to continuing is
// an idempotent success with no event. Any other status is the 409
// version_not_continuable of spec §7.11. A real transition publishes
// version.status_changed and audits version.continued.
func (s *VersionService) MarkContinuing(ctx context.Context, versionID, actor int64) (Version, error) {
	repo, err := s.continuingRepo()
	if err != nil {
		return Version{}, err
	}
	updated, transitioned, err := repo.MarkContinuing(ctx, versionID, actor)
	if err != nil {
		return Version{}, err
	}
	if updated == nil {
		return Version{}, errors.New("version: mark-continuing returned no row")
	}
	if !transitioned {
		return *updated, nil
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "version.status_changed",
		Summary:   "version status changed from completed to continuing",
		ActorID:   actor,
		Source:    domain.SourceUser,
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
	return *updated, nil
}

// continueReasonNewRequirement is the spec §8.1 version.status_changed reason of
// the D43 requirement trigger.
const continueReasonNewRequirement = "new_requirement"

// sourceSystem marks events/audits produced by the system actor (the autonomy
// worker's finalize). User-triggered transitions carry the user source.
const sourceSystem = "system"

// SetContinuePhase writes the phase of a continuing version (spec §5.1/§7.2).
// Only the enum (or the empty string, which clears it) is accepted; any other
// phase is a 422 invalid_continue_phase. A non-continuing version is a
// version_state_conflict, and an unchanged phase is a silent no-op. The write
// has no actor in the shared contract, so it is attributed to the system actor
// 0. A phase change publishes version.status_changed.
func (s *VersionService) SetContinuePhase(ctx context.Context, versionID int64, phase string) error {
	if err := domain.ValidateContinuePhase(phase); err != nil {
		return err
	}
	current, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Status != domain.VersionContinuing {
		return domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, continue phase requires continuing", versionID, current.Status))
	}
	if current.ContinuePhase == phase {
		return nil
	}
	repo, err := s.continuingRepo()
	if err != nil {
		return err
	}
	if _, err := repo.SetContinuePhase(ctx, versionID, phase, 0); err != nil {
		return err
	}
	detail := map[string]any{
		"from": domain.VersionContinuing,
		"to":   domain.VersionContinuing,
	}
	summary := "version continue phase cleared"
	if phase != "" {
		detail["phase"] = phase
		summary = "version continue phase changed to " + phase
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "version.status_changed",
		Summary:   summary,
		ActorID:   0,
		Source:    sourceSystem,
		Detail:    detail,
	})
	return nil
}

// TryFinalize applies the finalize conditions of spec §5.1 and, when they hold,
// performs the executing/continuing -> completed CAS. A paused or already
// completed version is never finalized; the batch conditions come from the
// injected execution-side reader and fail closed when it is not configured.
// Losing the CAS to a concurrent finalizer is reported as not finalized, so the
// call is idempotent. A real completion publishes version.status_changed and
// audits version.completed.
func (s *VersionService) TryFinalize(ctx context.Context, versionID int64) (bool, error) {
	current, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return false, err
	}
	if current.Status != domain.VersionExecuting && current.Status != domain.VersionContinuing {
		return false, nil
	}
	if current.Paused {
		return false, nil
	}
	if s.finalizer == nil {
		return false, errors.New("version: finalize batch reader is not configured")
	}
	conditions, err := s.finalizer.FinalizeBatchState(ctx, versionID)
	if err != nil {
		return false, err
	}
	if conditions.ActiveBatches > 0 || conditions.UnsettledBatches > 0 || conditions.TotalBatches < 1 {
		return false, nil
	}
	if current.Status == domain.VersionContinuing {
		if conditions.IncrementalBatches < 1 {
			return false, nil
		}
		drafts, _, err := s.repo.ListUnclarified(ctx, versionID)
		if err != nil {
			return false, err
		}
		if len(drafts) > 0 {
			return false, nil
		}
	}

	repo, err := s.continuingRepo()
	if err != nil {
		return false, err
	}
	_, transitioned, err := repo.Finalize(ctx, versionID, 0)
	if err != nil {
		return false, err
	}
	if !transitioned {
		return false, nil
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "version.status_changed",
		Summary:   "version status changed from " + current.Status + " to completed",
		ActorID:   0,
		Source:    sourceSystem,
		Detail: map[string]any{
			"from": current.Status,
			"to":   domain.VersionCompleted,
		},
	})
	s.recordAudit(ctx, VersionAuditEvent{
		Action:    "version.completed",
		VersionID: versionID,
		From:      current.Status,
		To:        domain.VersionCompleted,
		ActorID:   0,
	})
	return true, nil
}

// CompleteContinuing performs the continuing -> completed transition (spec §5.1).
// It is idempotent on an already completed version and refuses any other source
// status. A real completion clears the phase, publishes version.status_changed
// and audits version.completed.
func (s *VersionService) CompleteContinuing(ctx context.Context, versionID, actor int64) (Version, error) {
	current, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return Version{}, err
	}
	if current.Status == domain.VersionCompleted {
		return *current, nil
	}
	if current.Status != domain.VersionContinuing {
		return Version{}, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, complete-continuing requires continuing", versionID, current.Status))
	}
	repo, err := s.continuingRepo()
	if err != nil {
		return Version{}, err
	}
	updated, transitioned, err := repo.CompleteContinuing(ctx, versionID, actor)
	if err != nil {
		return Version{}, err
	}
	if updated == nil {
		return Version{}, errors.New("version: complete-continuing returned no row")
	}
	if !transitioned {
		return *updated, nil
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "version.status_changed",
		Summary:   "version status changed from continuing to completed",
		ActorID:   actor,
		Source:    domain.SourceUser,
		Detail: map[string]any{
			"from": domain.VersionContinuing,
			"to":   domain.VersionCompleted,
		},
	})
	s.recordAudit(ctx, VersionAuditEvent{
		Action:    "version.completed",
		VersionID: versionID,
		From:      domain.VersionContinuing,
		To:        domain.VersionCompleted,
		ActorID:   actor,
	})
	return *updated, nil
}

// VersionChangePage is one page of the derived version change feed (spec §7.2).
type VersionChangePage struct {
	Items      []domain.VersionChange
	NextCursor string
	HasMore    bool
}

// Changes returns the read-time derived change feed of a version: requirement
// creations, finished batches, published reports and created issues, newest
// first (spec §7.2/D50). Items sharing an instant are ordered by kind then by
// refId, so the feed is deterministic. code_changed is intentionally absent: it
// is deferred to the P8 version_change projection, with P4's code tab/events as
// the visible source in P7. The cursor is an opaque offset: P7 keeps no snapshot
// of the feed (D50), so concurrent writes may shift a page.
func (s *VersionService) Changes(ctx context.Context, versionID int64, page domain.Page) (VersionChangePage, error) {
	if _, err := s.repo.Get(ctx, versionID); err != nil {
		return VersionChangePage{}, err
	}
	if s.changes == nil {
		return VersionChangePage{}, errors.New("version: change feed is not configured")
	}
	all, err := s.changes.Changes(ctx, versionID)
	if err != nil {
		return VersionChangePage{}, err
	}

	items := append([]domain.VersionChange(nil), all...)
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].At.Equal(items[j].At) {
			return items[i].At.After(items[j].At)
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].RefID > items[j].RefID
	})

	offset, err := platform.DecodeCursor(page.Cursor)
	if err != nil {
		return VersionChangePage{}, fmt.Errorf("version: decode changes cursor: %w", err)
	}
	limit := int64(page.Limit)
	if limit <= 0 {
		limit = int64(platform.DefaultLimit)
	}
	if offset > int64(len(items)) {
		offset = int64(len(items))
	}
	end := offset + limit
	if end > int64(len(items)) {
		end = int64(len(items))
	}
	result := VersionChangePage{
		Items:   make([]domain.VersionChange, 0, end-offset),
		HasMore: end < int64(len(items)),
	}
	result.Items = append(result.Items, items[offset:end]...)
	if result.HasMore {
		result.NextCursor = platform.EncodeCursor(end)
	}
	return result, nil
}

// recordAudit records one version lifecycle audit entry. Failures are logged and
// swallowed: the business write is already committed (spec §11.2/P6 semantics).
func (s *VersionService) recordAudit(ctx context.Context, event VersionAuditEvent) {
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
