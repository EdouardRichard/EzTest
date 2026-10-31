package versionapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	repo    version.Repository
	catalog resourceapp.CatalogReader
	ids     platformid.Generator
	clock   clock.Clock
	audit   auditapp.Recorder
	events  eventingapp.Publisher
}

// New wires a version service. audit and events may be nil in unit tests; the
// assembly root sets the real implementations. catalog is the resource
// application service (resourceapp.Service implements CatalogReader).
func New(
	repo version.Repository,
	catalog resourceapp.CatalogReader,
	ids platformid.Generator,
	clk clock.Clock,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
) *Service {
	return &Service{repo: repo, catalog: catalog, ids: ids, clock: clk, audit: audit, events: events}
}

// CreateCommand is the payload for creating a version.
type CreateCommand struct {
	Title    string
	Bindings []BindingInput
}

// BindingInput is one SUT binding. IDs are already parsed from their decimal
// string form by the HTTP layer.
type BindingInput struct {
	SUTID            int64
	EnvironmentID    int64
	CodeRepositoryID *int64
	PageIDs          []int64
	APIIDs           []int64
	DatabaseIDs      []int64
	Accounts         []AccountInput
}

// AccountInput is one selected test account.
type AccountInput struct {
	AccountID int64
	Role      string
}

// Patch is a partial version update; nil fields are untouched. Status is not
// patchable in P0.
type Patch struct {
	Title                     *string
	AutoDesignNewRequirements *bool
}

// Create validates, persists and returns a new version. Version plus every
// version_sut and version_sut_resource row are written in one transaction. A
// version.created event is published to version:{id} and system and an audit
// entry is recorded.
func (s *Service) Create(ctx context.Context, cmd CreateCommand, actorID int64) (VersionView, error) {
	now := s.clock.Now()
	entity := version.Version{
		Title:     strings.TrimSpace(cmd.Title),
		Status:    version.StatusInitial,
		CreatedBy: actorID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := entity.Validate(); err != nil {
		return VersionView{}, err
	}

	bindings, err := s.validateBindings(ctx, cmd.Bindings)
	if err != nil {
		return VersionView{}, err
	}

	entity.ID = s.ids.Next()
	for i := range bindings {
		bindings[i].VersionID = entity.ID
		bindings[i].ID = s.ids.Next()
	}

	if err := s.repo.Create(ctx, entity, bindings); err != nil {
		return VersionView{}, mapRepositoryError(err)
	}
	s.recordCreated(ctx, entity, len(bindings), actorID)
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

// validateBindings checks the 1-20 binding cardinality, SUT uniqueness and
// resource ownership against the resource catalog. It returns the normalized
// domain bindings (deduplicated IDs, stable order) ready for persistence.
func (s *Service) validateBindings(ctx context.Context, inputs []BindingInput) ([]version.Binding, error) {
	if len(inputs) < version.MinBindings || len(inputs) > version.MaxBindings {
		return nil, platform.Invalid("bindings", fmt.Sprintf(
			"must contain between %d and %d bindings", version.MinBindings, version.MaxBindings,
		))
	}

	seenSUTs := make(map[int64]struct{}, len(inputs))
	bindings := make([]version.Binding, 0, len(inputs))
	for index, input := range inputs {
		field := func(name string) string { return fmt.Sprintf("bindings[%d].%s", index, name) }

		if input.SUTID <= 0 {
			return nil, platform.Invalid(field("sut_id"), "must be a positive decimal ID")
		}
		if _, duplicate := seenSUTs[input.SUTID]; duplicate {
			return nil, platform.Invalid(field("sut_id"), "sut is already bound in this version")
		}
		seenSUTs[input.SUTID] = struct{}{}
		sut, err := s.catalog.GetSUT(ctx, input.SUTID)
		if err != nil {
			return nil, invalidCatalogLookup(err, field("sut_id"), "sut does not exist")
		}

		if input.EnvironmentID <= 0 {
			return nil, platform.Invalid(field("environment_id"), "must be a positive decimal ID")
		}
		environment, err := s.catalog.GetEnvironment(ctx, input.EnvironmentID)
		if err != nil {
			return nil, invalidCatalogLookup(err, field("environment_id"), "environment does not exist")
		}
		if environment.SUTID != sut.ID {
			return nil, platform.Invalid(field("environment_id"), "environment does not belong to the SUT")
		}

		binding := version.Binding{
			SUTID:         input.SUTID,
			EnvironmentID: input.EnvironmentID,
			Scope:         version.NewScope(nil, nil),
			DatabaseIDs:   []int64{},
			Accounts:      []version.AccountBinding{},
		}

		if input.CodeRepositoryID != nil {
			if *input.CodeRepositoryID <= 0 {
				return nil, platform.Invalid(field("code_repository_id"), "must be a positive decimal ID")
			}
			if _, err := s.catalog.GetRepository(ctx, *input.CodeRepositoryID); err != nil {
				return nil, invalidCatalogLookup(err, field("code_repository_id"), "code repository does not exist")
			}
			repositoryID := *input.CodeRepositoryID
			binding.CodeRepositoryID = &repositoryID
		}

		pages, err := s.validatePages(ctx, input.PageIDs, sut.ID, environment.ID, field("page_ids"))
		if err != nil {
			return nil, err
		}
		binding.Scope.Pages = pages

		apis, err := s.validateAPIs(ctx, input.APIIDs, sut.ID, environment.ID, field("api_ids"))
		if err != nil {
			return nil, err
		}
		binding.Scope.APIs = apis

		databases, err := s.validateDatabases(ctx, input.DatabaseIDs, sut.ID, environment.ID, field("database_ids"))
		if err != nil {
			return nil, err
		}
		binding.DatabaseIDs = databases

		accounts, err := s.validateAccounts(ctx, input.Accounts, sut.ID, environment.ID, field("accounts"))
		if err != nil {
			return nil, err
		}
		binding.Accounts = accounts

		if err := binding.Validate(); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func (s *Service) validatePages(ctx context.Context, ids []int64, sutID string, environmentID string, field string) ([]int64, error) {
	unique, err := positiveUniqueIDs(ids, field)
	if err != nil {
		return nil, err
	}
	for _, id := range unique {
		page, err := s.catalog.GetPage(ctx, id)
		if err != nil {
			return nil, invalidCatalogLookup(err, field, fmt.Sprintf("page %d does not exist", id))
		}
		if page.SUTID != sutID || page.EnvironmentID != environmentID {
			return nil, platform.Invalid(field, fmt.Sprintf(
				"page %d does not belong to the bound SUT environment", id,
			))
		}
	}
	return unique, nil
}

func (s *Service) validateAPIs(ctx context.Context, ids []int64, sutID string, environmentID string, field string) ([]int64, error) {
	unique, err := positiveUniqueIDs(ids, field)
	if err != nil {
		return nil, err
	}
	for _, id := range unique {
		api, err := s.catalog.GetAPI(ctx, id)
		if err != nil {
			return nil, invalidCatalogLookup(err, field, fmt.Sprintf("api %d does not exist", id))
		}
		if api.SUTID != sutID || api.EnvironmentID != environmentID {
			return nil, platform.Invalid(field, fmt.Sprintf(
				"api %d does not belong to the bound SUT environment", id,
			))
		}
	}
	return unique, nil
}

func (s *Service) validateDatabases(ctx context.Context, ids []int64, sutID string, environmentID string, field string) ([]int64, error) {
	unique, err := positiveUniqueIDs(ids, field)
	if err != nil {
		return nil, err
	}
	for _, id := range unique {
		database, err := s.catalog.GetDatabase(ctx, id)
		if err != nil {
			return nil, invalidCatalogLookup(err, field, fmt.Sprintf("database %d does not exist", id))
		}
		if database.SUTID != sutID || database.EnvironmentID != environmentID {
			return nil, platform.Invalid(field, fmt.Sprintf(
				"database %d does not belong to the bound SUT environment", id,
			))
		}
	}
	return unique, nil
}

func (s *Service) validateAccounts(ctx context.Context, inputs []AccountInput, sutID string, environmentID string, field string) ([]version.AccountBinding, error) {
	seen := make(map[version.AccountBinding]struct{}, len(inputs))
	accounts := make([]version.AccountBinding, 0, len(inputs))
	for _, input := range inputs {
		if input.AccountID <= 0 {
			return nil, platform.Invalid(field, "account_id must be a positive decimal ID")
		}
		account := version.AccountBinding{AccountID: input.AccountID, Role: strings.TrimSpace(input.Role)}
		if _, duplicate := seen[account]; duplicate {
			continue
		}
		seen[account] = struct{}{}
		view, err := s.catalog.GetAccount(ctx, input.AccountID)
		if err != nil {
			return nil, invalidCatalogLookup(err, field, fmt.Sprintf("account %d does not exist", input.AccountID))
		}
		if view.SUTID != sutID || view.EnvironmentID != environmentID {
			return nil, platform.Invalid(field, fmt.Sprintf(
				"account %d does not belong to the bound SUT environment", input.AccountID,
			))
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
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

func (s *Service) buildView(ctx context.Context, entity version.Version, bindings []version.Binding) (VersionView, error) {
	stats, err := s.repo.Stats(ctx, entity.ID)
	if err != nil {
		return VersionView{}, err
	}
	view := VersionView{
		ID:                        entity.ID,
		Title:                     entity.Title,
		Status:                    entity.Status,
		AutoDesignNewRequirements: entity.AutoDesignNewRequirements,
		Bindings:                  make([]BindingView, 0, len(bindings)),
		RequirementCounts:         stats.Requirements,
		ConversationCount:         stats.ConversationCount,
		PendingDesignCount:        stats.Requirements.Clarified,
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

	view := BindingView{
		ID:          binding.ID,
		SUT:         sut,
		Environment: environment,
		Pages:       make([]resourceapp.PageView, 0, len(binding.Scope.Pages)),
		APIs:        make([]resourceapp.APIView, 0, len(binding.Scope.APIs)),
		Databases:   make([]resourceapp.DatabaseView, 0, len(binding.DatabaseIDs)),
		Accounts:    make([]resourceapp.AccountView, 0, len(binding.Accounts)),
	}

	if binding.CodeRepositoryID != nil {
		repository, err := s.catalog.GetRepository(ctx, *binding.CodeRepositoryID)
		if err != nil {
			return BindingView{}, err
		}
		view.Repository = &repository
	}
	for _, pageID := range binding.Scope.Pages {
		page, err := s.catalog.GetPage(ctx, pageID)
		if err != nil {
			return BindingView{}, err
		}
		view.Pages = append(view.Pages, page)
	}
	for _, apiID := range binding.Scope.APIs {
		api, err := s.catalog.GetAPI(ctx, apiID)
		if err != nil {
			return BindingView{}, err
		}
		view.APIs = append(view.APIs, api)
	}
	for _, databaseID := range binding.DatabaseIDs {
		database, err := s.catalog.GetDatabase(ctx, databaseID)
		if err != nil {
			return BindingView{}, err
		}
		view.Databases = append(view.Databases, database)
	}
	for _, account := range binding.Accounts {
		accountView, err := s.catalog.GetAccount(ctx, account.AccountID)
		if err != nil {
			return BindingView{}, err
		}
		view.Accounts = append(view.Accounts, accountView)
	}
	return view, nil
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

// PageInUse reports whether any binding scope references the page.
func (s *Service) PageInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.PageInUse(ctx, id)
}

// APIInUse reports whether any binding scope references the API.
func (s *Service) APIInUse(ctx context.Context, id int64) (bool, error) {
	return s.repo.APIInUse(ctx, id)
}

// DatabaseInUse reports whether any binding references the target database.
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
