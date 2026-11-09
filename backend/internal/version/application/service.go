package versionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
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
// resourceapp.UsageChecker for the resource delete guards.
type Service struct {
	repo     version.Repository
	catalog  resourceapp.CatalogReader
	ids      platformid.Generator
	clock    clock.Clock
	audit    auditapp.Recorder
	events   eventingapp.Publisher
	metrics  DesignMetrics
	resolver CodeBaselineResolver
}

// New wires a version service. audit and events may be nil in unit tests; the
// assembly root sets the real implementations. catalog is the resource
// application service (resourceapp.Service implements CatalogReader). resolver
// resolves the code baseline commit of bindings with a repository (P5 §5.5);
// nil keeps baselines unresolved, which never blocks version creation.
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

// CreateCommand is the v0.22 environment-only version creation payload
// (R-C3-2/API-15). The SUT is derived from the selected environment; pages,
// APIs, databases, accounts and repositories are no longer accepted here.
type CreateCommand struct {
	Title         string
	Description   string
	Goal          string
	EnvironmentID int64
}

// BindingPatch is the partial binding update of API-16 (R-C4-2). Nil fields are
// untouched; an explicit empty repository/account slice clears the selection.
type BindingPatch struct {
	EnvironmentID *int64
	RepositoryIDs *[]int64
	AccountIDs    *[]int64
	ScopeExclude  *version.ScopeExclude
}

// BindingResourceCounts is the approved/proposed/excluded/included projection of
// one environment-owned resource kind (R-C4-1).
type BindingResourceCounts struct {
	Approved int
	Proposed int
	Excluded int
	Included int
}

// BindingCounts groups the page/API/database counters of one binding overview.
type BindingCounts struct {
	Pages     BindingResourceCounts
	APIs      BindingResourceCounts
	Databases BindingResourceCounts
}

// BindingRepository is one repository row of the binding overview. Included
// reports whether the repository is effective for the version; Source is "sut"
// (inherited) or "override" (version-level include).
type BindingRepository struct {
	Repository resourceapp.CodeRepositoryView
	Role       string
	Included   bool
	Source     string
}

// BindingSnapshot is the R-C4-1 snapshot projection of one binding. W0 has no
// frozen wave snapshot in the version context (R-C5/API-19 is execution-owned:
// run_wave.scope_snapshot), so FrozenWaveID is nil and ScopeVersion is a
// deterministic digest of the current effective scope (pages/APIs/databases,
// accounts, effective repositories and the pinned code baseline). A client can
// compare ScopeVersion with the snapshot reference recorded by a frozen wave to
// detect that the version scope moved after that wave started.
type BindingSnapshot struct {
	ScopeVersion string
	FrozenWaveID *int64
}

// BindingOverview is the API-16 read model (R-C4-1): the derived SUT, the bound
// environment, the effective and excluded resources, the account subset, the
// repositories, the code baseline and the snapshot reference. The embedded
// Environment's Accounts are the version-bound subset (never the global pool).
type BindingOverview struct {
	VersionID          int64
	Stage              version.Stage
	Editable           bool
	SUT                resourceapp.SUTView
	Environment        resourceapp.EnvironmentView
	ScopeExclude       version.ScopeExclude
	Counts             BindingCounts
	Pages              []resourceapp.PageView
	APIs               []resourceapp.APIView
	Databases          []resourceapp.DatabaseView
	Accounts           []resourceapp.AccountView
	Repositories       []BindingRepository
	CodeBaselineCommit string
	Snapshot           BindingSnapshot
}

// Patch is a partial version update; nil fields are untouched. Status is not
// patchable in P0.
type Patch struct {
	Title                     *string
	AutoDesignNewRequirements *bool
}

// Create validates, persists and returns a new version. The selected environment
// determines the SUT (R-C3-2): the version writes one version_sut row, inherits
// the SUT repositories with no version override, and binds every active account
// of the global test-account pool (R-C3-3). A version.created event is published
// to version:{id} and system and an audit entry is recorded.
func (s *Service) Create(ctx context.Context, cmd CreateCommand, actorID int64) (VersionView, error) {
	now := s.clock.Now()
	entity := version.Version{
		Title:       strings.TrimSpace(cmd.Title),
		Description: strings.TrimSpace(cmd.Description),
		Goal:        strings.TrimSpace(cmd.Goal),
		Status:      version.StatusInitial,
		CreatedBy:   actorID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := entity.Validate(); err != nil {
		return VersionView{}, err
	}
	if cmd.EnvironmentID <= 0 {
		return VersionView{}, platform.Invalid("environment_id", "must be a positive decimal ID")
	}
	environment, err := s.catalog.GetEnvironment(ctx, cmd.EnvironmentID)
	if err != nil {
		return VersionView{}, invalidCatalogLookup(err, "environment_id", "environment does not exist")
	}
	sutID, err := parseCatalogID(environment.SUTID)
	if err != nil {
		return VersionView{}, platform.Invalid("environment_id", "environment does not derive a SUT")
	}
	if _, err := s.catalog.GetSUT(ctx, sutID); err != nil {
		return VersionView{}, invalidCatalogLookup(err, "environment_id", "the environment's sut does not exist")
	}

	accounts, err := s.defaultAccounts(ctx)
	if err != nil {
		return VersionView{}, err
	}
	if _, err := s.bindingRepositories(ctx, sutID, nil); err != nil {
		return VersionView{}, err
	}

	entity.ID = s.ids.Next()
	binding := version.Binding{
		ID:            s.ids.Next(),
		VersionID:     entity.ID,
		SUTID:         sutID,
		EnvironmentID: environmentID(environment),
		ScopeExclude:  version.NewScopeExclude(nil, nil, nil),
		Accounts:      accounts,
	}
	if err := binding.Validate(); err != nil {
		return VersionView{}, err
	}
	// Resolve the code baseline commit before persistence (P5 §5.5). A failure
	// leaves the column empty, is audited as a warning and never blocks Create.
	bindings := []version.Binding{binding}
	warnings := s.resolveCodeBaselines(ctx, bindings)

	if err := s.repo.Create(ctx, entity, bindings); err != nil {
		return VersionView{}, mapRepositoryError(err)
	}
	s.recordCreated(ctx, entity, 1, actorID)
	s.recordCodeBaselineWarnings(ctx, entity.ID, warnings, actorID)
	return s.Get(ctx, entity.ID)
}

// Get returns the version detail with nested resource views.
func (s *Service) Get(ctx context.Context, id int64) (VersionView, error) {
	if id <= 0 {
		return VersionView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, bindings, err := s.repo.Get(ctx, id)
	if err != nil {
		return VersionView{}, mapRepositoryError(err)
	}
	return s.buildView(ctx, entity, bindings)
}

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
// flag). Status is never changed by the P0 API. An audit entry is recorded.
func (s *Service) Update(ctx context.Context, id int64, patch Patch, actorID int64) (VersionView, error) {
	if id <= 0 {
		return VersionView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if patch.Title == nil && patch.AutoDesignNewRequirements == nil {
		return VersionView{}, platform.Invalid("body", "at least one of title or auto_design_new_requirements is required")
	}
	entity, bindings, err := s.repo.Get(ctx, id)
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
	return s.buildView(ctx, updated, bindings)
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

// Reopen projects a completed version back to running and clears its close
// fields (P3 §5.9 / P3-D-28). It is idempotent: only the call that flips
// completed -> running publishes version.status_changed and records the audit
// entry. Executing a new intent calls it before creating the intent.
func (s *Service) Reopen(ctx context.Context, id int64) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	changed, err := s.repo.Reopen(ctx, id, s.clock.Now())
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
	s.recordReopened(ctx, id)
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

// validateScopeExclude checks that every excluded page/API/database belongs to
// the bound environment (R-C1-3/C §2: only the bound environment's resources
// are selectable). It returns the normalized, deduplicated exclusion document.
func (s *Service) validateScopeExclude(
	ctx context.Context, scope version.ScopeExclude, sutID, environmentID int64,
) (version.ScopeExclude, error) {
	pages, err := positiveUniqueIDs(scope.Pages, "scope_exclude.pages")
	if err != nil {
		return version.ScopeExclude{}, err
	}
	for _, pageID := range pages {
		page, err := s.catalog.GetPage(ctx, pageID)
		if err != nil {
			return version.ScopeExclude{}, invalidCatalogLookup(err, "scope_exclude.pages", fmt.Sprintf("page %d does not exist", pageID))
		}
		pageSUT, pageEnv := parseCatalogIDOrZero(page.SUTID), parseCatalogIDOrZero(page.EnvironmentID)
		if pageSUT != sutID || pageEnv != environmentID {
			return version.ScopeExclude{}, platform.Invalid("scope_exclude.pages", fmt.Sprintf(
				"page %d does not belong to the bound SUT environment", pageID,
			))
		}
	}
	apis, err := positiveUniqueIDs(scope.APIs, "scope_exclude.apis")
	if err != nil {
		return version.ScopeExclude{}, err
	}
	for _, apiID := range apis {
		api, err := s.catalog.GetAPI(ctx, apiID)
		if err != nil {
			return version.ScopeExclude{}, invalidCatalogLookup(err, "scope_exclude.apis", fmt.Sprintf("api %d does not exist", apiID))
		}
		apiSUT, apiEnv := parseCatalogIDOrZero(api.SUTID), parseCatalogIDOrZero(api.EnvironmentID)
		if apiSUT != sutID || apiEnv != environmentID {
			return version.ScopeExclude{}, platform.Invalid("scope_exclude.apis", fmt.Sprintf(
				"api %d does not belong to the bound SUT environment", apiID,
			))
		}
	}
	databases, err := positiveUniqueIDs(scope.Databases, "scope_exclude.databases")
	if err != nil {
		return version.ScopeExclude{}, err
	}
	for _, databaseID := range databases {
		database, err := s.catalog.GetDatabase(ctx, databaseID)
		if err != nil {
			return version.ScopeExclude{}, invalidCatalogLookup(err, "scope_exclude.databases", fmt.Sprintf("database %d does not exist", databaseID))
		}
		databaseSUT, databaseEnv := parseCatalogIDOrZero(database.SUTID), parseCatalogIDOrZero(database.EnvironmentID)
		if databaseSUT != sutID || databaseEnv != environmentID {
			return version.ScopeExclude{}, platform.Invalid("scope_exclude.databases", fmt.Sprintf(
				"database %d does not belong to the bound SUT environment", databaseID,
			))
		}
	}
	return version.NewScopeExclude(pages, apis, databases), nil
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

// bindingRepositories resolves the effective repository set of one binding: the
// enabled SUT repositories plus/minus the version overrides. The returned rows
// are the union ordered by repository ID, each with its inclusion flag and
// source, so the binding overview can render the editable repository list.
func (s *Service) bindingRepositories(
	ctx context.Context, sutID int64, overrides []version.RepositoryOverride,
) ([]BindingRepository, error) {
	sutRepositories, err := s.catalog.ListSUTRepositories(ctx, sutID)
	if err != nil {
		return nil, err
	}
	overrideByRepo := make(map[int64]bool, len(overrides))
	for _, override := range overrides {
		overrideByRepo[override.RepoID] = override.Included
	}
	repositories := make(map[int64]BindingRepository)
	for _, association := range sutRepositories {
		repoID, err := parseCatalogID(association.Repository.ID)
		if err != nil {
			continue
		}
		included := association.Enabled
		if override, ok := overrideByRepo[repoID]; ok {
			included = override
		}
		if included {
			if association.Repository.Status != "active" {
				continue
			}
		} else if !association.Enabled {
			// A disabled SUT association that was not explicitly re-included is
			// not part of the union.
			continue
		}
		repositories[repoID] = BindingRepository{
			Repository: association.Repository,
			Role:       association.Role,
			Included:   included,
			Source:     "sut",
		}
	}
	for _, override := range overrides {
		if !override.Included {
			continue
		}
		if _, ok := repositories[override.RepoID]; ok {
			continue
		}
		repository, err := s.catalog.GetRepository(ctx, override.RepoID)
		if err != nil {
			return nil, invalidCatalogLookup(err, "repository_ids", fmt.Sprintf("repository %d does not exist", override.RepoID))
		}
		if repository.Status != "active" {
			return nil, platform.Invalid("repository_ids", fmt.Sprintf("repository %d is not active", override.RepoID))
		}
		repositories[override.RepoID] = BindingRepository{
			Repository: repository,
			Included:   true,
			Source:     "override",
		}
	}
	rows := make([]BindingRepository, 0, len(repositories))
	for _, repository := range repositories {
		rows = append(rows, repository)
	}
	sort.Slice(rows, func(left, right int) bool {
		leftID, _ := parseCatalogID(rows[left].Repository.ID)
		rightID, _ := parseCatalogID(rows[right].Repository.ID)
		return leftID < rightID
	})
	return rows, nil
}

// repositoriesForSelection turns an explicit effective repository selection
// into the version override rows: inherited rows that are not selected are
// excluded, and selected rows outside the enabled SUT inheritance are appended.
func (s *Service) repositoriesForSelection(
	ctx context.Context, sutID int64, selected []int64,
) ([]version.RepositoryOverride, error) {
	unique, err := positiveUniqueIDs(selected, "repository_ids")
	if err != nil {
		return nil, err
	}
	selectedSet := make(map[int64]struct{}, len(unique))
	for _, repoID := range unique {
		selectedSet[repoID] = struct{}{}
		repository, err := s.catalog.GetRepository(ctx, repoID)
		if err != nil {
			return nil, invalidCatalogLookup(err, "repository_ids", fmt.Sprintf("repository %d does not exist", repoID))
		}
		if repository.Status != "active" {
			return nil, platform.Invalid("repository_ids", fmt.Sprintf("repository %d is not active", repoID))
		}
	}
	sutRepositories, err := s.catalog.ListSUTRepositories(ctx, sutID)
	if err != nil {
		return nil, err
	}
	inherited := make(map[int64]resourceapp.SUTRepositoryView, len(sutRepositories))
	for _, association := range sutRepositories {
		repoID, err := parseCatalogID(association.Repository.ID)
		if err != nil {
			continue
		}
		inherited[repoID] = association
	}
	overrides := make([]version.RepositoryOverride, 0)
	for repoID, association := range inherited {
		_, want := selectedSet[repoID]
		if association.Enabled && !want {
			overrides = append(overrides, version.RepositoryOverride{RepoID: repoID, Included: false})
			continue
		}
		if !association.Enabled && want {
			overrides = append(overrides, version.RepositoryOverride{RepoID: repoID, Included: true})
		}
	}
	for _, repoID := range unique {
		association, ok := inherited[repoID]
		if ok && association.Enabled {
			continue
		}
		if ok && !association.Enabled {
			// Already emitted as a re-include above.
			continue
		}
		overrides = append(overrides, version.RepositoryOverride{RepoID: repoID, Included: true})
	}
	sort.Slice(overrides, func(left, right int) bool { return overrides[left].RepoID < overrides[right].RepoID })
	return overrides, nil
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

func environmentID(environment resourceapp.EnvironmentView) int64 {
	id, err := parseCatalogID(environment.ID)
	if err != nil {
		return 0
	}
	return id
}

func (s *Service) buildView(ctx context.Context, entity version.Version, bindings []version.Binding) (VersionView, error) {
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
	view := VersionView{
		ID:                        entity.ID,
		Title:                     entity.Title,
		Description:               entity.Description,
		Goal:                      entity.Goal,
		Status:                    entity.Status,
		AutoDesignNewRequirements: entity.AutoDesignNewRequirements,
		Bindings:                  make([]BindingView, 0, len(bindings)),
		RequirementCounts:         stats.Requirements,
		ConversationCount:         stats.ConversationCount,
		PendingDesignCount:        pendingDesign,
		CaseCounts:                caseCounts,
		ClosedAt:                  entity.ClosedAt,
		CloseReason:               entity.CloseReason,
		CreatedBy:                 entity.CreatedBy,
		CreatedAt:                 entity.CreatedAt,
		UpdatedAt:                 entity.UpdatedAt,
	}
	for _, binding := range bindings {
		bindingView, err := s.buildBindingView(ctx, binding)
		if err != nil {
			return VersionView{}, err
		}
		view.Bindings = append(view.Bindings, bindingView)
	}
	return view, nil
}

func (s *Service) buildBindingView(ctx context.Context, binding version.Binding) (BindingView, error) {
	sut, err := s.catalog.GetSUT(ctx, binding.SUTID)
	if err != nil {
		return BindingView{}, err
	}
	environment, err := s.catalog.GetEnvironment(ctx, binding.EnvironmentID)
	if err != nil {
		return BindingView{}, err
	}
	repositories, err := s.bindingRepositories(ctx, binding.SUTID, binding.RepositoryOverrides)
	if err != nil {
		return BindingView{}, err
	}

	view := BindingView{
		ID:                 binding.ID,
		CodeBaselineCommit: binding.CodeBaselineCommit,
		SUT:                sut,
		Environment:        environment,
		ScopeExclude:       binding.ScopeExclude,
		Repositories:       make([]BindingRepositoryView, 0, len(repositories)),
		Pages:              make([]resourceapp.PageView, 0),
		APIs:               make([]resourceapp.APIView, 0),
		Databases:          make([]resourceapp.DatabaseView, 0),
		Accounts:           make([]resourceapp.AccountView, 0, len(binding.Accounts)),
	}
	for _, repository := range repositories {
		if repository.Included {
			if view.Repository == nil {
				repositoryCopy := repository.Repository
				view.Repository = &repositoryCopy
			}
			view.Repositories = append(view.Repositories, BindingRepositoryView{
				Repository: repository.Repository,
				Role:       repository.Role,
				Source:     repository.Source,
			})
		}
	}
	excludedPages := idSet(binding.ScopeExclude.Pages)
	for _, page := range environment.Pages {
		if page.Status != "approved" {
			continue
		}
		if _, excluded := excludedPages[parseCatalogIDOrZero(page.ID)]; excluded {
			continue
		}
		view.Pages = append(view.Pages, page)
	}
	excludedAPIs := idSet(binding.ScopeExclude.APIs)
	for _, api := range environment.APIs {
		if api.Status != "approved" {
			continue
		}
		if _, excluded := excludedAPIs[parseCatalogIDOrZero(api.ID)]; excluded {
			continue
		}
		view.APIs = append(view.APIs, api)
	}
	excludedDatabases := idSet(binding.ScopeExclude.Databases)
	for _, database := range environment.Databases {
		if database.Status != "active" {
			continue
		}
		if _, excluded := excludedDatabases[parseCatalogIDOrZero(database.ID)]; excluded {
			continue
		}
		view.Databases = append(view.Databases, database)
	}
	for _, account := range binding.Accounts {
		accountView, err := s.catalog.GetAccount(ctx, account.AccountID)
		if err != nil {
			return BindingView{}, err
		}
		if accountView.Status != "active" {
			continue
		}
		view.Accounts = append(view.Accounts, accountView)
	}
	// The nested environment carries the version-bound account subset, not the
	// global test-account pool (DB-15/R-C1-2).
	environment.Accounts = append([]resourceapp.AccountView(nil), view.Accounts...)
	view.Environment = environment
	return view, nil
}

// GetBinding returns the API-16 binding overview of one version (R-C4-1). The
// binding is always read in the version aggregate so a missing version is a
// typed 404.
func (s *Service) GetBinding(ctx context.Context, versionID int64) (BindingOverview, error) {
	if versionID <= 0 {
		return BindingOverview{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, bindings, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return BindingOverview{}, mapRepositoryError(err)
	}
	if len(bindings) == 0 {
		return BindingOverview{}, platform.NotFound("version binding")
	}
	return s.bindingOverview(ctx, entity, bindings[0])
}

// UpdateBinding applies one API-16 partial binding update inside the stage gate
// (R-C4-2). It validates resources against the bound environment, recomputes the
// repository overrides and the deterministic baseline only when the effective
// repository set changed, persists the binding and records the
// version.binding.changed audit/event with the fields the patch actually
// changed. An empty patch, or one that repeats the current binding state, is a
// true no-op: no rows are written, no event is published and the baseline is
// never re-resolved.
func (s *Service) UpdateBinding(ctx context.Context, versionID int64, patch BindingPatch, actorID int64) (BindingOverview, error) {
	if versionID <= 0 {
		return BindingOverview{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	entity, bindings, err := s.repo.Get(ctx, versionID)
	if err != nil {
		return BindingOverview{}, mapRepositoryError(err)
	}
	if len(bindings) == 0 {
		return BindingOverview{}, platform.NotFound("version binding")
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

	// v0.22 single-writer invariant: creation writes exactly one
	// environment-derived version_sut row, so the lowest-ID row is the binding.
	// A legacy multi-row version is still read deterministically through it and
	// only that binding is rewritten below.
	binding := bindings[0]
	previousUpdatedAt := entity.UpdatedAt

	environmentID := binding.EnvironmentID
	if patch.EnvironmentID != nil {
		if *patch.EnvironmentID <= 0 {
			return BindingOverview{}, platform.Invalid("environment_id", "must be a positive decimal ID")
		}
		next, err := s.catalog.GetEnvironment(ctx, *patch.EnvironmentID)
		if err != nil {
			return BindingOverview{}, invalidCatalogLookup(err, "environment_id", "environment does not exist")
		}
		if parseCatalogIDOrZero(next.SUTID) != binding.SUTID {
			return BindingOverview{}, platform.Invalid("environment_id", "environment does not belong to the bound SUT")
		}
		environmentID = *patch.EnvironmentID
	}

	scope := binding.ScopeExclude
	if patch.ScopeExclude != nil {
		scope = *patch.ScopeExclude
	}
	// Exclusions are always re-validated against the (possibly new) environment
	// so switching environments cannot leave an exclusion pointing outside it.
	normalizedScope, err := s.validateScopeExclude(ctx, scope, binding.SUTID, environmentID)
	if err != nil {
		return BindingOverview{}, err
	}

	changed := make(map[string]any)
	if environmentID != binding.EnvironmentID {
		changed["environment_id"] = changeRecord(
			strconv.FormatInt(binding.EnvironmentID, 10),
			strconv.FormatInt(environmentID, 10),
		)
	}
	if patch.ScopeExclude != nil && !scopeExcludeEqual(normalizedScope, binding.ScopeExclude) {
		changed["scope_exclude"] = changeRecord(
			scopeExcludeSummary(binding.ScopeExclude),
			scopeExcludeSummary(normalizedScope),
		)
	}
	binding.EnvironmentID = environmentID
	binding.ScopeExclude = normalizedScope

	repositoriesChanged := false
	var repositories []BindingRepository
	if patch.RepositoryIDs != nil {
		overrides, err := s.repositoriesForSelection(ctx, binding.SUTID, *patch.RepositoryIDs)
		if err != nil {
			return BindingOverview{}, err
		}
		currentRepositories, err := s.bindingRepositories(ctx, binding.SUTID, binding.RepositoryOverrides)
		if err != nil {
			return BindingOverview{}, err
		}
		nextRepositories, err := s.bindingRepositories(ctx, binding.SUTID, overrides)
		if err != nil {
			return BindingOverview{}, err
		}
		if !sameRepositorySelection(currentRepositories, nextRepositories) {
			changed["repository_ids"] = changeRecord(
				effectiveRepositoryIDs(currentRepositories),
				effectiveRepositoryIDs(nextRepositories),
			)
			binding.RepositoryOverrides = overrides
			repositoriesChanged = true
			repositories = nextRepositories
		}
	}
	if patch.AccountIDs != nil {
		accounts, err := s.validateAccounts(ctx, *patch.AccountIDs)
		if err != nil {
			return BindingOverview{}, err
		}
		if !accountBindingsEqual(accounts, binding.Accounts) {
			changed["account_ids"] = changeRecord(
				accountBindingIDs(binding.Accounts),
				accountBindingIDs(accounts),
			)
			binding.Accounts = accounts
		}
	}

	if len(changed) == 0 {
		// Empty patch or a patch that repeats the persisted state: do not touch
		// version.updated_at, the binding rows, the baseline or the event stream.
		return s.bindingOverview(ctx, entity, binding)
	}

	var warnings []codeBaselineWarning
	if repositoriesChanged {
		// Only an effective repository change can move the deterministic
		// baseline (the lowest included repository ID). Account/scope/environment
		// updates keep the pinned commit untouched.
		commit, warning := s.resolveBaseline(ctx, binding.ID, repositories)
		switch {
		case warning != nil:
			// A resolver failure never clears a previously pinned commit; the
			// warning is audited and the old baseline stays stable for reruns.
			warnings = append(warnings, *warning)
		case commit == "":
			binding.CodeBaselineCommit = ""
		default:
			binding.CodeBaselineCommit = commit
		}
	}
	if err := binding.Validate(); err != nil {
		return BindingOverview{}, err
	}
	entity.UpdatedAt = s.clock.Now()
	if err := s.repo.UpdateBinding(ctx, entity, binding, previousUpdatedAt); err != nil {
		return BindingOverview{}, mapRepositoryError(err)
	}
	s.recordCodeBaselineWarnings(ctx, entity.ID, warnings, actorID)
	s.recordBindingChanged(ctx, entity.ID, changed, actorID)
	return s.bindingOverview(ctx, entity, binding)
}

// effectiveBindingPages returns the approved pages of the bound environment
// minus the version exclusions, in catalog order.
func (s *Service) effectiveBindingPages(ctx context.Context, binding version.Binding) ([]resourceapp.PageView, error) {
	environment, err := s.catalog.GetEnvironment(ctx, binding.EnvironmentID)
	if err != nil {
		return nil, err
	}
	excluded := idSet(binding.ScopeExclude.Pages)
	pages := make([]resourceapp.PageView, 0, len(environment.Pages))
	for _, page := range environment.Pages {
		if page.Status != "approved" {
			continue
		}
		if _, skip := excluded[parseCatalogIDOrZero(page.ID)]; skip {
			continue
		}
		pages = append(pages, page)
	}
	return pages, nil
}

func (s *Service) bindingOverview(
	ctx context.Context, entity version.Version, binding version.Binding,
) (BindingOverview, error) {
	sut, err := s.catalog.GetSUT(ctx, binding.SUTID)
	if err != nil {
		return BindingOverview{}, err
	}
	environment, err := s.catalog.GetEnvironment(ctx, binding.EnvironmentID)
	if err != nil {
		return BindingOverview{}, err
	}
	stage, editable, err := s.bindingStage(ctx, entity)
	if err != nil {
		return BindingOverview{}, err
	}
	repositories, err := s.bindingRepositories(ctx, binding.SUTID, binding.RepositoryOverrides)
	if err != nil {
		return BindingOverview{}, err
	}
	overview := BindingOverview{
		VersionID:          entity.ID,
		Stage:              stage,
		Editable:           editable,
		SUT:                sut,
		Environment:        environment,
		ScopeExclude:       binding.ScopeExclude,
		Repositories:       repositories,
		CodeBaselineCommit: binding.CodeBaselineCommit,
		Pages:              make([]resourceapp.PageView, 0),
		APIs:               make([]resourceapp.APIView, 0),
		Databases:          make([]resourceapp.DatabaseView, 0),
		Accounts:           make([]resourceapp.AccountView, 0),
	}
	overview.Counts.Pages = countResources(environment.Pages, binding.ScopeExclude.Pages)
	overview.Counts.APIs = countResources(environment.APIs, binding.ScopeExclude.APIs)
	overview.Counts.Databases = countResources(environment.Databases, binding.ScopeExclude.Databases)

	excludedPages := idSet(binding.ScopeExclude.Pages)
	for _, page := range environment.Pages {
		if page.Status == "approved" {
			if _, excluded := excludedPages[parseCatalogIDOrZero(page.ID)]; !excluded {
				overview.Pages = append(overview.Pages, page)
			}
		}
	}
	excludedAPIs := idSet(binding.ScopeExclude.APIs)
	for _, api := range environment.APIs {
		if api.Status == "approved" {
			if _, excluded := excludedAPIs[parseCatalogIDOrZero(api.ID)]; !excluded {
				overview.APIs = append(overview.APIs, api)
			}
		}
	}
	excludedDatabases := idSet(binding.ScopeExclude.Databases)
	for _, database := range environment.Databases {
		if database.Status != "active" {
			continue
		}
		if _, excluded := excludedDatabases[parseCatalogIDOrZero(database.ID)]; !excluded {
			overview.Databases = append(overview.Databases, database)
		}
	}
	for _, account := range binding.Accounts {
		accountView, err := s.catalog.GetAccount(ctx, account.AccountID)
		if err != nil {
			return BindingOverview{}, err
		}
		if accountView.Status != "active" {
			continue
		}
		overview.Accounts = append(overview.Accounts, accountView)
	}
	// The nested environment carries the version-bound account subset, not the
	// global test-account pool (DB-15/R-C1-2).
	environment.Accounts = append([]resourceapp.AccountView(nil), overview.Accounts...)
	overview.Environment = environment
	overview.Snapshot = BindingSnapshot{ScopeVersion: bindingScopeVersion(overview)}
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

func countResources[T any](resources []T, excluded []int64) BindingResourceCounts {
	counts := BindingResourceCounts{}
	excludedSet := idSet(excluded)
	for _, resource := range resources {
		approved := false
		switch resourceStatus(resource) {
		case "approved", "active", "":
			counts.Approved++
			approved = true
		case "proposed":
			counts.Proposed++
		}
		if !approved {
			continue
		}
		if _, excluded := excludedSet[resourceID(resource)]; excluded {
			counts.Excluded++
		}
	}
	counts.Included = counts.Approved - counts.Excluded
	if counts.Included < 0 {
		counts.Included = 0
	}
	return counts
}

func resourceID[T any](resource T) int64 {
	switch value := any(resource).(type) {
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

func int64sToStrings(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

// changeRecord renders one old/new pair of the version.binding.changed diff.
func changeRecord(oldValue, newValue any) map[string]any {
	return map[string]any{"old": oldValue, "new": newValue}
}

// scopeExcludeSummary renders the pages/apis/databases summary of one exclusion
// document for the audit diff.
func scopeExcludeSummary(scope version.ScopeExclude) map[string]any {
	return map[string]any{
		"pages":     int64sToStrings(scope.Pages),
		"apis":      int64sToStrings(scope.APIs),
		"databases": int64sToStrings(scope.Databases),
	}
}

func scopeExcludeEqual(left, right version.ScopeExclude) bool {
	return sameIDSet(left.Pages, right.Pages) &&
		sameIDSet(left.APIs, right.APIs) &&
		sameIDSet(left.Databases, right.Databases)
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

// effectiveRepositoryIDs returns the sorted IDs of the included repositories.
func effectiveRepositoryIDs(rows []BindingRepository) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Included {
			ids = append(ids, row.Repository.ID)
		}
	}
	return ids
}

func sameRepositorySelection(left, right []BindingRepository) bool {
	return sameStringSet(effectiveRepositoryIDs(left), effectiveRepositoryIDs(right))
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

// sameStringSet compares two string slices as sets so repository selections are
// not reported as changed when only their order differs.
func sameStringSet(left, right []string) bool {
	leftSet := make(map[string]struct{}, len(left))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for value := range leftSet {
		if _, present := rightSet[value]; !present {
			return false
		}
	}
	return true
}

// bindingScopeVersion is the deterministic snapshot reference of the current
// effective binding scope (R-C4-1). It deliberately covers only the frozen
// scope dimensions (bound environment, effective page/API/database/account/
// repository IDs and the pinned baseline), so equal scopes always compare equal
// and a binding change always moves the reference.
func bindingScopeVersion(overview BindingOverview) string {
	hasher := sha256.New()
	fmt.Fprintf(hasher, "scope-v1|version:%d|sut:%s|environment:%s|baseline:%s|",
		overview.VersionID, overview.SUT.ID, overview.Environment.ID, overview.CodeBaselineCommit)
	writeScopeIDs := func(kind string, ids []string) {
		fmt.Fprintf(hasher, "%s=", kind)
		for _, id := range ids {
			fmt.Fprintf(hasher, "%s,", id)
		}
		fmt.Fprint(hasher, "|")
	}
	writeScopeIDs("pages", resourceViewIDs(overview.Pages))
	writeScopeIDs("apis", resourceViewIDs(overview.APIs))
	writeScopeIDs("databases", resourceViewIDs(overview.Databases))
	accountIDs := make([]string, 0, len(overview.Accounts))
	for _, account := range overview.Accounts {
		accountIDs = append(accountIDs, account.ID)
	}
	writeScopeIDs("accounts", accountIDs)
	writeScopeIDs("repositories", effectiveRepositoryIDs(overview.Repositories))
	return "scope-v1:" + hex.EncodeToString(hasher.Sum(nil))
}

func resourceViewIDs[T any](items []T) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := resourceID(item); id > 0 {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
	}
	return ids
}

// KnowledgeBindingKeys is the structured page-route/account-role projection of
// one version's bindings. Knowledge items store the same strings as their
// page_key/account_role bindings, so a page/route/account change is detected by
// comparing a captured key set with the current one and passing the removed
// keys to the knowledge application's MarkBindingsStale (Task 33). The read is
// cross-context but exposes only SUT identifiers and route templates, never a
// credential.
type KnowledgeBindingKeys struct {
	VersionID      int64
	SystemIDs      []int64
	EnvironmentIDs []int64
	PageKeys       []string
	AccountRoles   []string
}

// KnowledgeBindingKeys returns the current page/route/account key set of one
// version's bindings. Pages without a route template are omitted (they cannot
// be named by a knowledge page_key), and the result is deduplicated in stable
// binding order so two reads of unchanged bindings compare equal.
func (s *Service) KnowledgeBindingKeys(ctx context.Context, id int64) (KnowledgeBindingKeys, error) {
	if id <= 0 {
		return KnowledgeBindingKeys{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	_, bindings, err := s.repo.Get(ctx, id)
	if err != nil {
		return KnowledgeBindingKeys{}, mapRepositoryError(err)
	}
	keys := KnowledgeBindingKeys{
		VersionID:      id,
		SystemIDs:      make([]int64, 0, len(bindings)),
		EnvironmentIDs: make([]int64, 0, len(bindings)),
		PageKeys:       make([]string, 0, len(bindings)),
		AccountRoles:   make([]string, 0, len(bindings)),
	}
	for _, binding := range bindings {
		keys.SystemIDs = append(keys.SystemIDs, binding.SUTID)
		keys.EnvironmentIDs = append(keys.EnvironmentIDs, binding.EnvironmentID)
		pages, err := s.effectiveBindingPages(ctx, binding)
		if err != nil {
			return KnowledgeBindingKeys{}, err
		}
		for _, page := range pages {
			if route := strings.TrimSpace(page.RouteTemplate); route != "" {
				keys.PageKeys = append(keys.PageKeys, route)
			}
		}
		for _, account := range binding.Accounts {
			view, err := s.catalog.GetAccount(ctx, account.AccountID)
			if err != nil {
				return KnowledgeBindingKeys{}, err
			}
			if view.Status != "active" {
				continue
			}
			for _, role := range view.Roles {
				if role = strings.TrimSpace(role); role != "" {
					keys.AccountRoles = append(keys.AccountRoles, role)
				}
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

// SUTInUse reports whether any version binding references the SUT.
func (s *Service) SUTInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.SUTInUse(ctx, id)
}

// EnvironmentInUse reports whether any version binding references the
// environment.
func (s *Service) EnvironmentInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.EnvironmentInUse(ctx, id)
}

// PageInUse reports whether the page is inside the effective scope of a version
// binding (belongs to a bound environment as an approved page and is not
// excluded). Deleting a resource that is in range is blocked; an excluded
// resource is not part of the version range and stays deletable.
func (s *Service) PageInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.PageInUse(ctx, id)
}

// APIInUse reports whether the API is inside the effective scope of a version
// binding.
func (s *Service) APIInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.APIInUse(ctx, id)
}

// DatabaseInUse reports whether the target database is inside the effective
// scope of a version binding (active and not excluded).
func (s *Service) DatabaseInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.DatabaseInUse(ctx, id)
}

// AccountInUse reports whether any binding references the test account.
func (s *Service) AccountInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.AccountInUse(ctx, id)
}

// RepositoryInUse reports whether any binding references the code repository.
func (s *Service) RepositoryInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.RepositoryInUse(ctx, id)
}

// ReferencingVersions lists the versions whose binding references the resource
// of one kind, carrying the version title for the resource delete conflict
// details. The resource context only understands its own VersionRef shape, so
// this adapter converts the version-domain rows.
func (s *Service) ReferencingVersions(ctx context.Context, kind string, id int64) ([]resourceapp.VersionRef, error) {
	refs, err := s.repo.ReferencingVersions(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	out := make([]resourceapp.VersionRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, resourceapp.VersionRef{ID: ref.ID, Title: ref.Title})
	}
	return out, nil
}

var _ resourceapp.UsageChecker = (*Service)(nil)

// ---------- events, audit and errors ----------

// recordCreated publishes version.created to version:{id} and system and
// records the audit entry. Delivery is best effort and never fails the
// business write.
func (s *Service) recordCreated(ctx context.Context, entity version.Version, sutCount int, actorID int64) {
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
				"id":        targetID,
				"title":     entity.Title,
				"status":    entity.Status,
				"sut_count": sutCount,
			},
		})
	}
	if s.events != nil {
		scope := map[string]any{"version_id": targetID}
		payload := map[string]any{
			"title":     entity.Title,
			"status":    entity.Status,
			"sut_count": sutCount,
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

// recordReopened publishes version.status_changed completed -> running and
// records the version.reopened audit entry. Delivery is best effort.
func (s *Service) recordReopened(ctx context.Context, versionID int64) {
	targetID := platformid.Format(versionID)
	requestID := requestid.From(ctx)
	if s.events != nil {
		scope := map[string]any{"version_id": targetID}
		payload := map[string]any{"from": version.StatusCompleted, "to": version.StatusRunning}
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   "version:" + targetID,
			Type:      "version.status_changed",
			Scope:     scope,
			Payload:   payload,
			RequestID: requestID,
		})
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			Action:     "version.reopened",
			TargetKind: "version",
			TargetID:   targetID,
			RequestID:  requestID,
			Metadata:   map[string]any{"id": targetID, "from": version.StatusCompleted, "to": version.StatusRunning},
		})
	}
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
