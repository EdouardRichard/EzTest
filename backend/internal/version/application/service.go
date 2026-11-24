package versionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Service is the version application service. It implements
// resourceapp.UsageChecker for the resource delete guards and BindingReader for
// the downstream contexts that used to read the version_sut chain.
type Service struct {
	repo      version.Repository
	catalog   resourceapp.CatalogReader
	ids       platformid.Generator
	clock     clock.Clock
	audit     auditapp.Recorder
	events    eventingapp.Publisher
	metrics   DesignMetrics
	resolver  CodeBaselineResolver
	baselines resourceapp.SUTRepositoryBaselineWriter
}

// New wires a version service. audit and events may be nil in unit tests; the
// assembly root sets the real implementations. catalog is the resource
// application service (resourceapp.Service implements CatalogReader). resolver
// resolves the code baseline commit of the environment code pool (P5 §5.5); nil
// keeps baselines unresolved, which never blocks version creation.
func New(
	repo version.Repository,
	catalog resourceapp.CatalogReader,
	ids platformid.Generator,
	clk clock.Clock,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
	resolver CodeBaselineResolver,
) *Service {
	return &Service{repo: repo, catalog: catalog, ids: ids, clock: clk, audit: audit, events: events, resolver: resolver}
}

// SetDesignMetrics injects the design projection source. It is optional (P1
// tests and degraded wiring); a nil source keeps the clarified-count fallback.
// Returns the service for fluent wiring.
func (s *Service) SetDesignMetrics(metrics DesignMetrics) *Service {
	s.metrics = metrics
	return s
}

// SetBaselineWriter injects the resource-context write path for the resolved
// code baselines (sut_repository.code_baseline_commit). It is optional: a nil
// writer keeps the resolved commit in memory for the current read only, exactly
// like a nil resolver. The assembly root wires the resource application service.
// Returns the service for fluent wiring.
func (s *Service) SetBaselineWriter(writer resourceapp.SUTRepositoryBaselineWriter) *Service {
	s.baselines = writer
	return s
}

// CreateCommand is the environment-only version creation payload (R-C3-2/API-15,
// v0.2.4-B SC-B2). Pages, APIs, databases, accounts and repositories are not
// accepted here: they are all derived from the bound environment.
type CreateCommand struct {
	Title         string
	Description   string
	Goal          string
	EnvironmentID int64
}

// BindingPatch is the partial binding update of API-16 (R-C4-2). Nil fields are
// untouched; an explicit empty account slice clears the selection. The
// repository selection and the scope exclusions of v0.22 are gone with the
// version_sut chain: the code pool is derived from the environment and every
// approved asset of its SUTs is in scope.
type BindingPatch struct {
	EnvironmentID *int64
	AccountIDs    *[]int64
}

// Patch is a partial version update; nil fields are untouched. Status is not
// patchable in P0.
type Patch struct {
	Title                     *string
	AutoDesignNewRequirements *bool
}

// Create validates, persists and returns a new version bound to one environment
// (v0.2.4-B SC-B2). Nothing is derived into a per-version SUT row any more: the
// environment_id is written on the version, every active account of the global
// test-account pool is bound (R-C3-3), and the code baselines of the
// environment's repository pool are resolved on a best-effort basis. A
// version.created event is published to version:{id} and system and an audit
// entry is recorded.
func (s *Service) Create(ctx context.Context, cmd CreateCommand, actorID int64) (VersionView, error) {
	now := s.clock.Now()
	entity := version.Version{
		Title:         strings.TrimSpace(cmd.Title),
		Description:   strings.TrimSpace(cmd.Description),
		Goal:          strings.TrimSpace(cmd.Goal),
		Status:        version.StatusInitial,
		EnvironmentID: cmd.EnvironmentID,
		CreatedBy:     actorID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := entity.Validate(); err != nil {
		return VersionView{}, err
	}
	if _, err := s.catalog.GetEnvironment(ctx, cmd.EnvironmentID); err != nil {
		return VersionView{}, invalidCatalogLookup(err, "environment_id", "environment does not exist")
	}

	accounts, err := s.defaultAccounts(ctx)
	if err != nil {
		return VersionView{}, err
	}
	if err := version.ValidateAccounts(accounts); err != nil {
		return VersionView{}, err
	}

	entity.ID = s.ids.Next()
	if err := s.repo.Create(ctx, entity, accounts); err != nil {
		return VersionView{}, mapRepositoryError(err)
	}
	// Resolve the code baselines of the environment pool after the version row
	// exists (P5 §5.5). A failure is audited as a warning and never blocks
	// Create; an already pinned baseline is never overwritten.
	warnings := s.resolveEnvironmentBaselines(ctx, entity.EnvironmentID)
	s.recordCreated(ctx, entity, actorID)
	s.recordCodeBaselineWarnings(ctx, entity.ID, warnings, actorID)
	return s.Get(ctx, entity.ID)
}

// Get returns the version detail with the resolved environment catalog.
func (s *Service) Get(ctx context.Context, id int64) (VersionView, error) {
	if id <= 0 {
		return VersionView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, accounts, err := s.repo.Get(ctx, id)
	if err != nil {
		return VersionView{}, mapRepositoryError(err)
	}
	return s.buildView(ctx, entity, accounts)
}

// ResolveEnvironment returns the environment SUT catalog of one version. It is
// the BindingReader port the downstream contexts consume in place of the removed
// version_sut chain.
func (s *Service) ResolveEnvironment(ctx context.Context, versionID int64) (EnvironmentSUTCatalog, error) {
	if versionID <= 0 {
		return EnvironmentSUTCatalog{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, accounts, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return EnvironmentSUTCatalog{}, mapRepositoryError(err)
	}
	return s.environmentCatalog(ctx, entity, accounts)
}

var _ BindingReader = (*Service)(nil)

// List returns one page of version summaries ordered by ID descending.
func (s *Service) List(ctx context.Context, limit int, beforeID *int64) ([]VersionSummary, bool, error) {
	limit = normalizeLimit(limit)
	summaries, err := s.repo.List(ctx, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(summaries) > limit
	if hasMore {
		summaries = summaries[:limit]
	}
	views := make([]VersionSummary, 0, len(summaries))
	for _, summary := range summaries {
		views = append(views, VersionSummary{
			ID:        summary.ID,
			Title:     summary.Title,
			Status:    summary.Status,
			SUTCount:  summary.SUTCount,
			CreatedAt: summary.CreatedAt,
			UpdatedAt: summary.UpdatedAt,
		})
	}
	return views, hasMore, nil
}

// Update applies the P0-allowed partial update (title and/or the auto-design
// flag). Status and the environment binding are never changed by this API. An
// audit entry is recorded.
func (s *Service) Update(ctx context.Context, id int64, patch Patch, actorID int64) (VersionView, error) {
	if id <= 0 {
		return VersionView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if patch.Title == nil && patch.AutoDesignNewRequirements == nil {
		return VersionView{}, platform.Invalid("body", "at least one of title or auto_design_new_requirements is required")
	}
	entity, accounts, err := s.repo.Get(ctx, id)
	if err != nil {
		return VersionView{}, mapRepositoryError(err)
	}
	if patch.Title != nil {
		entity.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.AutoDesignNewRequirements != nil {
		entity.AutoDesignNewRequirements = *patch.AutoDesignNewRequirements
	}
	entity.UpdatedAt = s.clock.Now()
	if err := entity.Validate(); err != nil {
		return VersionView{}, err
	}
	updated, err := s.repo.Update(ctx, entity)
	if err != nil {
		return VersionView{}, mapRepositoryError(err)
	}
	s.recordUpdated(ctx, updated, actorID)
	return s.buildView(ctx, updated, accounts)
}

// MarkRunning projects the version from initial to running. It is idempotent:
// only the first call that flips the status publishes version.started and
// version.status_changed (to version:{id} and system). The conversation and
// requirement services call it on the first message/requirement/confirmation;
// the P1 reconciler is the fallback.
func (s *Service) MarkRunning(ctx context.Context, id int64) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	changed, err := s.repo.MarkRunning(ctx, id, s.clock.Now())
	if err != nil {
		return mapRepositoryError(err)
	}
	if !changed {
		// Distinguish "already running" from "missing version".
		if _, _, err := s.repo.Get(ctx, id); err != nil {
			return mapRepositoryError(err)
		}
		return nil
	}
	s.recordStatusProjection(ctx, id)
	return nil
}

// defaultAccounts returns the active accounts of the global test-account pool,
// which every new version binds by default (R-C3-3/DB-15).
func (s *Service) defaultAccounts(ctx context.Context) ([]version.AccountBinding, error) {
	accounts, err := s.catalog.ListAllAccounts(ctx)
	if err != nil {
		return nil, err
	}
	bindings := make([]version.AccountBinding, 0, len(accounts))
	for _, account := range accounts {
		if account.Status != "active" {
			continue
		}
		id, err := parseCatalogID(account.ID)
		if err != nil {
			continue
		}
		bindings = append(bindings, version.AccountBinding{AccountID: id})
	}
	return bindings, nil
}

// validateAccounts resolves an explicit version account selection against the
// global pool. Only active accounts are allowed (execution threshold C-4).
func (s *Service) validateAccounts(ctx context.Context, accountIDs []int64) ([]version.AccountBinding, error) {
	unique, err := positiveUniqueIDs(accountIDs, "account_ids")
	if err != nil {
		return nil, err
	}
	accounts := make([]version.AccountBinding, 0, len(unique))
	for _, accountID := range unique {
		account, err := s.catalog.GetAccount(ctx, accountID)
		if err != nil {
			return nil, invalidCatalogLookup(err, "account_ids", fmt.Sprintf("account %d does not exist", accountID))
		}
		if account.Status != "active" {
			return nil, platform.Invalid("account_ids", fmt.Sprintf("account %d is not active", accountID))
		}
		accounts = append(accounts, version.AccountBinding{AccountID: accountID})
	}
	return accounts, nil
}

// environmentRepositories returns the environment code pool (v0.2.4-B GD-03).
// The resource catalog owns the union/de-duplication/ordering rules; this only
// projects them into the version read model, so the two contexts cannot
// disagree about what the pool is.
func (s *Service) environmentRepositories(ctx context.Context, environmentID int64) ([]BindingRepository, error) {
	associations, err := s.catalog.ListEnvironmentRepositories(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	repositories := make([]BindingRepository, 0, len(associations))
	for _, association := range associations {
		if _, err := parseCatalogID(association.Repository.ID); err != nil {
			continue
		}
		sutID, err := parseCatalogID(association.SUTID)
		if err != nil {
			continue
		}
		repositories = append(repositories, BindingRepository{
			Repository:         association.Repository,
			SUTID:              sutID,
			Role:               association.Role,
			CodeBaselineCommit: association.CodeBaselineCommit,
		})
	}
	return repositories, nil
}

func positiveUniqueIDs(ids []int64, field string) ([]int64, error) {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, platform.Invalid(field, "must contain positive decimal IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique, nil
}

func parseCatalogID(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

func parseCatalogIDOrZero(raw string) int64 {
	id, err := parseCatalogID(raw)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// environmentCatalog resolves the whole resource projection of one version's
// environment binding: the environment with its SUT catalog, the flattened asset
// unions, the environment code pool, the version account subset and the pinned
// code baseline.
func (s *Service) environmentCatalog(
	ctx context.Context, entity version.Version, accounts []version.AccountBinding,
) (EnvironmentSUTCatalog, error) {
	catalog := EnvironmentSUTCatalog{
		VersionID:     entity.ID,
		EnvironmentID: entity.EnvironmentID,
		Pages:         make([]resourceapp.PageView, 0),
		APIs:          make([]resourceapp.APIView, 0),
		Databases:     make([]resourceapp.DatabaseView, 0),
		Accounts:      make([]resourceapp.AccountView, 0, len(accounts)),
		Repositories:  make([]BindingRepository, 0),
	}
	if entity.EnvironmentID <= 0 {
		return catalog, platform.NotFound("version binding")
	}
	environment, err := s.catalog.GetEnvironment(ctx, entity.EnvironmentID)
	if err != nil {
		return EnvironmentSUTCatalog{}, err
	}
	catalog.Environment = environment
	// The flattened unions are the version's effective scope, and they are exactly
	// the union of each SUT's effective assets: one implementation of the rule, in
	// the resource projection layer (resourceapp.SUTView.EffectiveAssets), so the
	// per-SUT projection the adapters read and the flat projection the binding
	// overview and requirement validation read can never disagree. Proposed and
	// rejected rows stay visible on the nested SUT views (they are catalog state,
	// not version scope) but never enter the effective lists. Exclusions no longer
	// exist, so every in-range asset of every SUT of the environment is in scope.
	scope := environment.EffectiveScope()
	catalog.Pages = scope.Pages
	catalog.APIs = scope.APIs
	catalog.Databases = scope.Databases
	repositories, err := s.environmentRepositories(ctx, entity.EnvironmentID)
	if err != nil {
		return EnvironmentSUTCatalog{}, err
	}
	catalog.Repositories = repositories
	catalog.CodeBaselineCommit = baselineCommitOf(repositories)

	for _, account := range accounts {
		accountView, err := s.catalog.GetAccount(ctx, account.AccountID)
		if err != nil {
			return EnvironmentSUTCatalog{}, err
		}
		if accountView.Status != "active" {
			continue
		}
		catalog.Accounts = append(catalog.Accounts, accountView)
	}
	return catalog, nil
}

// baselineCommitOf returns the commit of the deterministic baseline repository:
// the first row of the pool, which environmentRepositories orders by repository
// ID ascending (P5 §5.5).
//
// V-0024: it is THAT repository's own pinned commit. The former first-non-empty
// scan returned a commit that could belong to a DIFFERENT repository while the
// caller paired it with Repositories[0]'s id — a repo_id/commit mismatch. An
// unresolved baseline repository yields "" (its own empty commit), never a
// borrowed one.
func baselineCommitOf(repositories []BindingRepository) string {
	if len(repositories) == 0 {
		return ""
	}
	return repositories[0].CodeBaselineCommit
}

func (s *Service) buildView(
	ctx context.Context, entity version.Version, accounts []version.AccountBinding,
) (VersionView, error) {
	stats, err := s.repo.Stats(ctx, entity.ID)
	if err != nil {
		return VersionView{}, err
	}
	pendingDesign := stats.Requirements.Clarified
	caseCounts := CaseCounts{}
	if s.metrics != nil {
		// Projections are read-only conveniences: a design-store failure must
		// never break the version detail (P2 spec §9.2 fallback).
		if pending, metricErr := s.metrics.CountPendingDesign(ctx, entity.ID); metricErr == nil {
			pendingDesign = pending
		}
		if counts, metricErr := s.metrics.CaseCounts(ctx, entity.ID); metricErr == nil {
			caseCounts = counts
		}
	}
	binding, err := s.environmentCatalog(ctx, entity, accounts)
	if err != nil {
		return VersionView{}, err
	}
	return VersionView{
		ID:                        entity.ID,
		Title:                     entity.Title,
		Description:               entity.Description,
		Goal:                      entity.Goal,
		Status:                    entity.Status,
		AutoDesignNewRequirements: entity.AutoDesignNewRequirements,
		EnvironmentID:             entity.EnvironmentID,
		Binding:                   binding,
		RequirementCounts:         stats.Requirements,
		ConversationCount:         stats.ConversationCount,
		PendingDesignCount:        pendingDesign,
		CaseCounts:                caseCounts,
		ClosedAt:                  entity.ClosedAt,
		CloseReason:               entity.CloseReason,
		ClosureKind:               entity.ClosureKind,
		Unverified:                entity.Unverified,
		CreatedBy:                 entity.CreatedBy,
		CreatedAt:                 entity.CreatedAt,
		UpdatedAt:                 entity.UpdatedAt,
	}, nil
}

// GetBinding returns the API-16 binding overview of one version (R-C4-1). The
// binding is always read from the version aggregate so a missing version is a
// typed 404.
func (s *Service) GetBinding(ctx context.Context, versionID int64) (BindingOverview, error) {
	if versionID <= 0 {
		return BindingOverview{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, accounts, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return BindingOverview{}, mapRepositoryError(err)
	}
	return s.bindingOverview(ctx, entity, accounts)
}

// UpdateBinding applies one API-16 partial binding update inside the stage gate
// (R-C4-2). It can rebind the version to another environment and rewrite the
// version account subset; the SUT catalog, the assets and the code pool are all
// derived, so there is nothing else to persist. Rebinding an environment
// re-resolves the code baselines of the new pool AFTER the binding write
// succeeded, so a rejected rebind (optimistic-lock 409 or a database error) can
// never leave a stray pin in the resource context. An empty patch, or one that
// repeats the current binding state, is a true no-op: no rows are written, no
// event is published and no baseline is resolved.
func (s *Service) UpdateBinding(ctx context.Context, versionID int64, patch BindingPatch, actorID int64) (BindingOverview, error) {
	if versionID <= 0 {
		return BindingOverview{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, accounts, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return BindingOverview{}, mapRepositoryError(err)
	}
	if entity.Status == version.StatusCompleted {
		return BindingOverview{}, platform.StateConflict("version binding is read-only after completion")
	}
	stage, editable, err := s.bindingStage(ctx, entity)
	if err != nil {
		return BindingOverview{}, err
	}
	if !editable {
		return BindingOverview{}, platform.StateConflict(fmt.Sprintf(
			"version binding is read-only at stage %s", stage,
		))
	}

	previousUpdatedAt := entity.UpdatedAt
	environmentID := entity.EnvironmentID
	environmentChanged := false
	if patch.EnvironmentID != nil {
		if *patch.EnvironmentID <= 0 {
			return BindingOverview{}, platform.Invalid("environment_id", "must be a positive decimal ID")
		}
		if _, err := s.catalog.GetEnvironment(ctx, *patch.EnvironmentID); err != nil {
			return BindingOverview{}, invalidCatalogLookup(err, "environment_id", "environment does not exist")
		}
		if *patch.EnvironmentID != environmentID {
			environmentID = *patch.EnvironmentID
			environmentChanged = true
		}
	}

	changed := make(map[string]any)
	if environmentChanged {
		changed["environment_id"] = changeRecord(
			strconv.FormatInt(entity.EnvironmentID, 10),
			strconv.FormatInt(environmentID, 10),
		)
		entity.EnvironmentID = environmentID
	}

	if patch.AccountIDs != nil {
		nextAccounts, err := s.validateAccounts(ctx, *patch.AccountIDs)
		if err != nil {
			return BindingOverview{}, err
		}
		if !accountBindingsEqual(nextAccounts, accounts) {
			changed["account_ids"] = changeRecord(
				accountBindingIDs(accounts),
				accountBindingIDs(nextAccounts),
			)
			accounts = nextAccounts
		}
	}

	if len(changed) == 0 {
		// Empty patch or a patch that repeats the persisted state: do not touch
		// version.updated_at, the binding rows, the baselines or the event stream.
		return s.bindingOverview(ctx, entity, accounts)
	}

	if err := version.ValidateAccounts(accounts); err != nil {
		return BindingOverview{}, err
	}
	entity.UpdatedAt = s.clock.Now()
	if err := s.repo.UpdateBinding(ctx, entity, accounts, previousUpdatedAt); err != nil {
		return BindingOverview{}, mapRepositoryError(err)
	}
	// The baseline is resolved only AFTER the binding write succeeded, exactly like
	// Create does. It lives in the resource context (sut_repository
	// .code_baseline_commit), outside this transaction, and a pinned commit is
	// never overwritten — so resolving it before the write would leave a permanent,
	// unaudited pin on an environment this version never actually bound whenever
	// the optimistic lock (409) or the database rejected the rebind.
	var warnings []codeBaselineWarning
	if environmentChanged {
		// Only an environment change can move the code pool, and with it the
		// deterministic baseline. Already pinned baselines are never rewritten.
		warnings = s.resolveEnvironmentBaselines(ctx, environmentID)
	}
	s.recordCodeBaselineWarnings(ctx, entity.ID, warnings, actorID)
	s.recordBindingChanged(ctx, entity.ID, changed, actorID)
	return s.bindingOverview(ctx, entity, accounts)
}

func (s *Service) bindingOverview(
	ctx context.Context, entity version.Version, accounts []version.AccountBinding,
) (BindingOverview, error) {
	catalog, err := s.environmentCatalog(ctx, entity, accounts)
	if err != nil {
		return BindingOverview{}, err
	}
	stage, editable, err := s.bindingStage(ctx, entity)
	if err != nil {
		return BindingOverview{}, err
	}
	overview := BindingOverview{
		EnvironmentSUTCatalog: catalog,
		Stage:                 stage,
		Editable:              editable,
	}
	overview.Counts.Pages = countResources(environmentPages(catalog.Environment))
	overview.Counts.APIs = countResources(environmentAPIs(catalog.Environment))
	overview.Counts.Databases = countResources(environmentDatabases(catalog.Environment))
	overview.Snapshot = BindingSnapshot{ScopeVersion: bindingScopeVersion(catalog)}
	return overview, nil
}

// bindingStageReader is the narrow stage projection the binding gate needs. The
// PostgreSQL repository implements the full version.StageRepository.
type bindingStageReader interface {
	GetStage(ctx context.Context, versionID int64) (version.StageState, error)
}

// bindingStage resolves the effective workflow stage of one version. A
// completed version is terminal and always reports completed/read-only even
// when the persisted stage projection lags behind (status is the close gate).
// Suspended versions use the recorded suspended_from stage so the binding gate
// follows the stage the version was suspended from (R-C4-2). Repositories that
// do not implement the stage read port fall back to the version status.
func (s *Service) bindingStage(ctx context.Context, entity version.Version) (version.Stage, bool, error) {
	if entity.Status == version.StatusCompleted {
		return version.StageCompleted, false, nil
	}
	stage := version.StageCreated
	reader, ok := s.repo.(bindingStageReader)
	if !ok {
		return stage, version.BindingEditable(stage), nil
	}
	state, err := reader.GetStage(ctx, entity.ID)
	if err != nil {
		return "", false, mapRepositoryError(err)
	}
	stage = state.Stage
	effective := stage
	if stage.IsSuspended() && state.SuspendedFrom != nil {
		effective = *state.SuspendedFrom
	}
	return stage, version.BindingEditable(effective), nil
}

// environmentPages/environmentAPIs/environmentDatabases flatten every asset of
// the environment's SUT catalog regardless of status, so the binding counters can
// report the proposed rows alongside the approved ones.
func environmentPages(environment resourceapp.EnvironmentView) []resourceapp.PageView {
	out := make([]resourceapp.PageView, 0)
	for _, sut := range environment.SUTs {
		out = append(out, sut.Pages...)
	}
	return out
}

func environmentAPIs(environment resourceapp.EnvironmentView) []resourceapp.APIView {
	out := make([]resourceapp.APIView, 0)
	for _, sut := range environment.SUTs {
		out = append(out, sut.APIs...)
	}
	return out
}

func environmentDatabases(environment resourceapp.EnvironmentView) []resourceapp.DatabaseView {
	out := make([]resourceapp.DatabaseView, 0)
	for _, sut := range environment.SUTs {
		out = append(out, sut.Databases...)
	}
	return out
}

// countResources projects one asset union into its approved/proposed counters.
// Every approved asset of the bound environment is in scope, so Approved is also
// the included count.
func countResources[T any](resources []T) BindingResourceCounts {
	counts := BindingResourceCounts{}
	for _, resource := range resources {
		switch resourceStatus(resource) {
		case "approved", "active", "":
			counts.Approved++
		case "proposed":
			counts.Proposed++
		}
	}
	return counts
}

func resourceStatus[T any](resource T) string {
	switch value := any(resource).(type) {
	case resourceapp.PageView:
		return value.Status
	case resourceapp.APIView:
		return value.Status
	case resourceapp.DatabaseView:
		return value.Status
	default:
		return ""
	}
}

func idSet(ids []int64) map[int64]struct{} {
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// changeRecord renders one old/new pair of the version.binding.changed diff.
func changeRecord(oldValue, newValue any) map[string]any {
	return map[string]any{"old": oldValue, "new": newValue}
}

func accountBindingIDs(accounts []version.AccountBinding) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, strconv.FormatInt(account.AccountID, 10))
	}
	return ids
}

func accountBindingsEqual(left, right []version.AccountBinding) bool {
	leftIDs := make([]int64, 0, len(left))
	for _, account := range left {
		leftIDs = append(leftIDs, account.AccountID)
	}
	rightIDs := make([]int64, 0, len(right))
	for _, account := range right {
		rightIDs = append(rightIDs, account.AccountID)
	}
	return sameIDSet(leftIDs, rightIDs)
}

// sameIDSet compares two ID slices as sets, so a duplicate or reordered patch
// value is not reported as a binding change.
func sameIDSet(left, right []int64) bool {
	leftSet := idSet(left)
	rightSet := idSet(right)
	if len(leftSet) != len(rightSet) {
		return false
	}
	for id := range leftSet {
		if _, present := rightSet[id]; !present {
			return false
		}
	}
	return true
}

// effectiveRepositoryIDs returns the pool repository IDs in pool order.
func effectiveRepositoryIDs(rows []BindingRepository) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Repository.ID)
	}
	return ids
}

// bindingScopeVersion is the deterministic snapshot reference of the current
// effective binding scope (R-C4-1). It deliberately covers only the frozen scope
// dimensions (bound environment, its SUT catalog, effective page/API/database/
// account/repository IDs and the pinned baseline), so equal scopes always compare
// equal and a binding change always moves the reference.
func bindingScopeVersion(catalog EnvironmentSUTCatalog) string {
	hasher := sha256.New()
	fmt.Fprintf(hasher, "scope-v2|version:%d|environment:%d|baseline:%s|",
		catalog.VersionID, catalog.EnvironmentID, catalog.CodeBaselineCommit)
	writeScopeIDs := func(kind string, ids []string) {
		fmt.Fprintf(hasher, "%s=", kind)
		for _, id := range ids {
			fmt.Fprintf(hasher, "%s,", id)
		}
		fmt.Fprint(hasher, "|")
	}
	writeScopeIDs("suts", resourceViewIDs(catalog.Environment.SUTs))
	writeScopeIDs("pages", resourceViewIDs(catalog.Pages))
	writeScopeIDs("apis", resourceViewIDs(catalog.APIs))
	writeScopeIDs("databases", resourceViewIDs(catalog.Databases))
	accountIDs := make([]string, 0, len(catalog.Accounts))
	for _, account := range catalog.Accounts {
		accountIDs = append(accountIDs, account.ID)
	}
	writeScopeIDs("accounts", accountIDs)
	writeScopeIDs("repositories", effectiveRepositoryIDs(catalog.Repositories))
	return "scope-v2:" + hex.EncodeToString(hasher.Sum(nil))
}

func resourceViewIDs[T any](items []T) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := catalogViewID(item); id > 0 {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
	}
	return ids
}

// catalogViewID reads the decimal ID of one resource view of the catalog.
func catalogViewID[T any](item T) int64 {
	switch value := any(item).(type) {
	case resourceapp.SUTView:
		return parseCatalogIDOrZero(value.ID)
	case resourceapp.PageView:
		return parseCatalogIDOrZero(value.ID)
	case resourceapp.APIView:
		return parseCatalogIDOrZero(value.ID)
	case resourceapp.DatabaseView:
		return parseCatalogIDOrZero(value.ID)
	default:
		return 0
	}
}

// KnowledgeBindingKeys is the structured page-route/account-role projection of
// one version's environment binding. Knowledge items store the same strings as
// their page_key/account_role bindings, so a page/route/account change is
// detected by comparing a captured key set with the current one and passing the
// removed keys to the knowledge application's MarkBindingsStale (Task 33). The
// read is cross-context but exposes only SUT identifiers and route templates,
// never a credential.
type KnowledgeBindingKeys struct {
	VersionID      int64
	SystemIDs      []int64
	EnvironmentIDs []int64
	PageKeys       []string
	AccountRoles   []string
}

// KnowledgeBindingKeys returns the current page/route/account key set of one
// version's environment binding. Pages without a route template are omitted
// (they cannot be named by a knowledge page_key), and the result is
// deduplicated in stable catalog order so two reads of unchanged bindings
// compare equal.
func (s *Service) KnowledgeBindingKeys(ctx context.Context, id int64) (KnowledgeBindingKeys, error) {
	if id <= 0 {
		return KnowledgeBindingKeys{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, accounts, err := s.repo.Get(ctx, id)
	if err != nil {
		return KnowledgeBindingKeys{}, mapRepositoryError(err)
	}
	keys := KnowledgeBindingKeys{
		VersionID:      id,
		SystemIDs:      make([]int64, 0),
		EnvironmentIDs: make([]int64, 0),
		PageKeys:       make([]string, 0),
		AccountRoles:   make([]string, 0),
	}
	if entity.EnvironmentID > 0 {
		keys.EnvironmentIDs = append(keys.EnvironmentIDs, entity.EnvironmentID)
	}
	catalog, err := s.environmentCatalog(ctx, entity, accounts)
	if err != nil {
		return KnowledgeBindingKeys{}, err
	}
	for _, sut := range catalog.Environment.SUTs {
		if sutID := parseCatalogIDOrZero(sut.ID); sutID > 0 {
			keys.SystemIDs = append(keys.SystemIDs, sutID)
		}
	}
	for _, page := range catalog.Pages {
		if route := strings.TrimSpace(page.RouteTemplate); route != "" {
			keys.PageKeys = append(keys.PageKeys, route)
		}
	}
	for _, account := range catalog.Accounts {
		for _, role := range account.Roles {
			if role = strings.TrimSpace(role); role != "" {
				keys.AccountRoles = append(keys.AccountRoles, role)
			}
		}
	}
	keys.SystemIDs = uniqueInt64s(keys.SystemIDs)
	keys.EnvironmentIDs = uniqueInt64s(keys.EnvironmentIDs)
	keys.PageKeys = uniqueStrings(keys.PageKeys)
	keys.AccountRoles = uniqueStrings(keys.AccountRoles)
	return keys, nil
}

func uniqueInt64s(values []int64) []int64 {
	out := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// ---------- resourceapp.UsageChecker ----------

// SUTInUse reports whether any version binds an environment that contains the
// SUT.
func (s *Service) SUTInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.SUTInUse(ctx, id)
}

// EnvironmentInUse reports whether any version binds the environment.
func (s *Service) EnvironmentInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.EnvironmentInUse(ctx, id)
}

// PageInUse reports whether the page belongs to a SUT of a bound environment and
// is approved, so it is inside the effective scope of a version.
func (s *Service) PageInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.PageInUse(ctx, id)
}

// APIInUse reports whether the API is inside the effective scope of a version
// binding.
func (s *Service) APIInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.APIInUse(ctx, id)
}

// DatabaseInUse reports whether the target database is inside the effective
// scope of a version binding (active and owned by a SUT of a bound environment).
func (s *Service) DatabaseInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.DatabaseInUse(ctx, id)
}

// AccountInUse reports whether any version enables the test account.
func (s *Service) AccountInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.AccountInUse(ctx, id)
}

// RepositoryInUse reports whether any SUT association references the code
// repository.
func (s *Service) RepositoryInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.RepositoryInUse(ctx, id)
}

// ReferencingVersions lists the versions whose binding references the resource
// of one kind, carrying the version title plus the lifecycle status, effective
// stage and binding editability for the resource delete conflict details. The
// resource context only understands its own VersionRef shape, so this adapter
// converts the version-domain rows.
func (s *Service) ReferencingVersions(ctx context.Context, kind string, id int64) ([]resourceapp.VersionRef, error) {
	refs, err := s.repo.ReferencingVersions(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	out := make([]resourceapp.VersionRef, 0, len(refs))
	for _, ref := range refs {
		// A completed version is terminal even when its persisted stage lags
		// behind; a suspended version uses the stage it was suspended from, so
		// a version suspended out of executing stays read-only.
		effectiveStage := ref.Stage
		if ref.Status == version.StatusCompleted {
			effectiveStage = version.StageCompleted
		} else if effectiveStage.IsSuspended() && ref.SuspendedFrom != nil {
			effectiveStage = *ref.SuspendedFrom
		}
		editable := ref.Status != version.StatusCompleted && version.BindingEditable(effectiveStage)
		out = append(out, resourceapp.VersionRef{
			ID:       ref.ID,
			Title:    ref.Title,
			Status:   ref.Status,
			Stage:    string(effectiveStage),
			Editable: editable,
		})
	}
	return out, nil
}

var _ resourceapp.UsageChecker = (*Service)(nil)

// ---------- events, audit and errors ----------

// recordCreated publishes version.created to version:{id} and system and
// records the audit entry. Delivery is best effort and never fails the business
// write.
func (s *Service) recordCreated(ctx context.Context, entity version.Version, actorID int64) {
	targetID := platformid.Format(entity.ID)
	requestID := requestid.From(ctx)
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}

	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    actor,
			Action:     "version.created",
			TargetKind: "version",
			TargetID:   targetID,
			RequestID:  requestID,
			Metadata: map[string]any{
				"id":             targetID,
				"title":          entity.Title,
				"status":         entity.Status,
				"environment_id": platformid.Format(entity.EnvironmentID),
			},
		})
	}
	if s.events != nil {
		scope := map[string]any{"version_id": targetID}
		payload := map[string]any{
			"title":          entity.Title,
			"status":         entity.Status,
			"environment_id": platformid.Format(entity.EnvironmentID),
		}
		for _, channel := range []string{"version:" + targetID, "system"} {
			_ = s.events.Publish(ctx, eventingapp.Event{
				Channel:   channel,
				Type:      "version.created",
				Scope:     scope,
				Payload:   payload,
				ActorID:   actor,
				RequestID: requestID,
			})
		}
	}
}

// recordBindingChanged writes the version.binding.changed audit entry and
// publishes the same event to version:{id} and system (R-C4-3). changed carries
// only the fields the request actually modified (with old/new values), so the
// audit trail names the real diff instead of stamping the environment on every
// update. Delivery is best effort and never fails the business write.
func (s *Service) recordBindingChanged(
	ctx context.Context, versionID int64, changed map[string]any, actorID int64,
) {
	targetID := platformid.Format(versionID)
	requestID := requestid.From(ctx)
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	payload := map[string]any{
		"version_id": targetID,
		"changed":    changed,
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    actor,
			Action:     "version.binding.changed",
			TargetKind: "version",
			TargetID:   targetID,
			RequestID:  requestID,
			Metadata:   payload,
		})
	}
	if s.events != nil {
		scope := map[string]any{"version_id": targetID}
		for _, channel := range []string{"version:" + targetID, "system"} {
			_ = s.events.Publish(ctx, eventingapp.Event{
				Channel:   channel,
				Type:      "version.binding.changed",
				Scope:     scope,
				Payload:   payload,
				ActorID:   actor,
				RequestID: requestID,
			})
		}
	}
}

// recordUpdated writes the version.updated audit entry. P0 publishes no update
// event.
func (s *Service) recordUpdated(ctx context.Context, entity version.Version, actorID int64) {
	if s.audit == nil {
		return
	}
	targetID := platformid.Format(entity.ID)
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actor,
		Action:     "version.updated",
		TargetKind: "version",
		TargetID:   targetID,
		RequestID:  requestid.From(ctx),
		Metadata: map[string]any{
			"id":                           targetID,
			"title":                        entity.Title,
			"auto_design_new_requirements": entity.AutoDesignNewRequirements,
		},
	})
}

// recordStatusProjection publishes version.started and version.status_changed
// for the initial -> running transition. Delivery is best effort.
func (s *Service) recordStatusProjection(ctx context.Context, versionID int64) {
	if s.events == nil {
		return
	}
	targetID := platformid.Format(versionID)
	scope := map[string]any{"version_id": targetID}
	payload := map[string]any{"from": version.StatusInitial, "to": version.StatusRunning}
	for _, eventType := range []string{"version.started", "version.status_changed"} {
		for _, channel := range []string{"version:" + targetID, "system"} {
			_ = s.events.Publish(ctx, eventingapp.Event{
				Channel:   channel,
				Type:      eventType,
				Scope:     scope,
				Payload:   payload,
				RequestID: requestid.From(ctx),
			})
		}
	}
}

// invalidCatalogLookup turns a resource-catalog not-found into a field-level
// 400 for create validation. Unexpected catalog failures stay 5xx.
func invalidCatalogLookup(err error, field, message string) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return platform.Invalid(field, message)
	}
	return err
}

func mapRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrNotFound):
		return platform.NotFound("version")
	case errors.Is(err, version.ErrDuplicate):
		return platform.Conflict("version binding already exists")
	case errors.Is(err, version.ErrBindingConflict):
		return platform.StateConflict("version binding changed concurrently; reload and retry")
	default:
		return err
	}
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return pagination.DefaultLimit
	}
	if limit > pagination.MaxLimit {
		return pagination.MaxLimit
	}
	return limit
}
