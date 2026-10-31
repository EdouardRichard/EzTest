// Package resourceapp implements the resource-catalog use cases: SUT,
// environment, page, API, target database, test account and code repository
// CRUD, catalog aggregation and bulk import.
package resourceapp

import (
	"context"
	"errors"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Service is the resource-catalog application service. It implements
// CatalogReader for the version context.
type Service struct {
	repo   resource.Repository
	ids    platformid.Generator
	clock  clock.Clock
	usage  UsageChecker
	audit  auditapp.Recorder
	events eventingapp.Publisher
}

// New wires a resource service. usage, audit and events may be nil in unit
// tests; the assembly root sets the real implementations.
func New(
	repo resource.Repository,
	ids platformid.Generator,
	clk clock.Clock,
	usage UsageChecker,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
) *Service {
	return &Service{repo: repo, ids: ids, clock: clk, usage: usage, audit: audit, events: events}
}

// SetUsageChecker injects the version-binding usage guard after construction.
// The version service needs a CatalogReader, which is this service, so the
// assembly root wires the two contexts through this setter.
func (s *Service) SetUsageChecker(usage UsageChecker) {
	s.usage = usage
}

// SUTInput is the editable payload for creating a SUT.
type SUTInput struct {
	Name  string
	Intro string
}

// EnvironmentInput is the editable payload for creating an environment.
type EnvironmentInput struct {
	Name      string
	BaseURL   string
	IsDefault bool
	Status    string
}

// EnvironmentPatch is a partial environment update; nil fields are untouched.
type EnvironmentPatch struct {
	Name      *string
	BaseURL   *string
	IsDefault *bool
	Status    *string
}

// PageInput is the editable payload for creating a page.
type PageInput struct {
	Name          string
	URL           string
	RouteTemplate string
	PageType      string
	AuthRole      string
	Module        string
}

// PagePatch is a partial page update; nil fields are untouched.
type PagePatch struct {
	Name          *string
	URL           *string
	RouteTemplate *string
	PageType      *string
	AuthRole      *string
	Module        *string
}

// APIInput is the editable payload for creating an API.
type APIInput struct {
	Method       string
	PathTemplate string
	Name         string
	AuthRole     string
	SchemaRef    string
}

// APIPatch is a partial API update; nil fields are untouched.
type APIPatch struct {
	Method       *string
	PathTemplate *string
	Name         *string
	AuthRole     *string
	SchemaRef    *string
}

// ImportPagesResult reports a page import.
type ImportPagesResult struct {
	Created int
	Skipped int
	Items   []PageView
}

// ImportAPIsResult reports an API import.
type ImportAPIsResult struct {
	Created int
	Skipped int
	Items   []APIView
}

// DatabaseInput is the editable payload for creating a target database.
type DatabaseInput struct {
	EnvironmentID int64
	Name          string
	DSN           string
	Username      string
	Password      string
	ReadOnly      *bool
	Status        string
}

// DatabasePatch is a partial target-database update; nil fields are untouched.
type DatabasePatch struct {
	Name     *string
	DSN      *string
	Username *string
	Password *string
	ReadOnly *bool
	Status   *string
}

// AccountInput is the editable payload for creating a test account.
type AccountInput struct {
	EnvironmentID int64
	Username      string
	Password      string
	Roles         []string
	Notes         string
	Status        string
}

// AccountPatch is a partial test-account update; a nil Password keeps the
// stored password.
type AccountPatch struct {
	Username *string
	Password *string
	Roles    *[]string
	Notes    *string
	Status   *string
}

// RepositoryInput is the editable payload for creating a code repository.
type RepositoryInput struct {
	Name    string
	Path    string
	Branch  string
	Include []string
	Exclude []string
	Status  string
}

// RepositoryPatch is a partial code-repository update.
type RepositoryPatch struct {
	Name    *string
	Path    *string
	Branch  *string
	Include *[]string
	Exclude *[]string
	Status  *string
}

// SUTPatch is a partial SUT update; nil fields are left untouched.
type SUTPatch struct {
	Name  *string
	Intro *string
}

// CreateSUT validates and stores a new SUT.
func (s *Service) CreateSUT(ctx context.Context, input SUTInput, actorID int64) (SUTView, error) {
	at := s.clock.Now()
	sut := resource.SUT{
		Name:      input.Name,
		Intro:     input.Intro,
		CreatedBy: actorID,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := sut.Validate(); err != nil {
		return SUTView{}, err
	}
	sut.ID = s.ids.Next()
	created, err := s.repo.CreateSUT(ctx, sut)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "sut name already exists")
	}
	s.record(ctx, actorID, "resource.sut.created", "sut", platformid.Format(created.ID), map[string]any{
		"name": created.Name,
	})
	return toSUTView(created), nil
}

// UpdateSUT applies a partial update to a SUT.
func (s *Service) UpdateSUT(ctx context.Context, id int64, patch SUTPatch, actorID int64) (SUTView, error) {
	sut, err := s.repo.GetSUT(ctx, id)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "")
	}
	if patch.Name != nil {
		sut.Name = *patch.Name
	}
	if patch.Intro != nil {
		sut.Intro = *patch.Intro
	}
	sut.UpdatedAt = s.clock.Now()
	if err := sut.Validate(); err != nil {
		return SUTView{}, err
	}
	updated, err := s.repo.UpdateSUT(ctx, sut)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "sut name already exists")
	}
	s.record(ctx, actorID, "resource.sut.updated", "sut", platformid.Format(updated.ID), map[string]any{
		"name": updated.Name,
	})
	return toSUTView(updated), nil
}

// DeleteSUT deletes a SUT when no version binding references it.
func (s *Service) DeleteSUT(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) { return s.usage.SUTInUse(ctx, id) }, "sut"); err != nil {
		return err
	}
	if err := s.repo.DeleteSUT(ctx, id); err != nil {
		return mapRepoError(err, "sut", "")
	}
	s.record(ctx, actorID, "resource.sut.deleted", "sut", platformid.Format(id), nil)
	return nil
}

// ListSUTs returns one page of SUTs ordered by ID descending.
func (s *Service) ListSUTs(ctx context.Context, limit int, beforeID *int64) ([]SUTView, bool, error) {
	limit = normalizeLimit(limit)
	items, err := s.repo.ListSUTs(ctx, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	views := make([]SUTView, 0, len(items))
	for _, item := range items {
		views = append(views, toSUTView(item))
	}
	return views, hasMore, nil
}

// GetSUT returns the aggregate SUT view with every environment and its nested
// resources.
func (s *Service) GetSUT(ctx context.Context, id int64) (SUTView, error) {
	graph, err := s.repo.GetSUTGraph(ctx, id)
	if err != nil {
		return SUTView{}, mapRepoError(err, "sut", "")
	}
	return toSUTDetailView(graph), nil
}

// CreateEnvironment stores an environment under a SUT. Setting it as default
// unsets any previous default in the same SUT.
func (s *Service) CreateEnvironment(ctx context.Context, sutID int64, input EnvironmentInput, actorID int64) (EnvironmentView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return EnvironmentView{}, mapRepoError(err, "sut", "")
	}
	at := s.clock.Now()
	environment := resource.Environment{
		SUTID:     sutID,
		Name:      input.Name,
		BaseURL:   input.BaseURL,
		IsDefault: input.IsDefault,
		Status:    defaultStatus(input.Status),
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := environment.Validate(); err != nil {
		return EnvironmentView{}, err
	}
	environment.ID = s.ids.Next()
	if environment.IsDefault {
		if err := s.clearDefaultEnvironments(ctx, sutID, 0); err != nil {
			return EnvironmentView{}, err
		}
	}
	created, err := s.repo.CreateEnvironment(ctx, environment)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "environment name already exists")
	}
	s.record(ctx, actorID, "resource.environment.created", "environment", platformid.Format(created.ID), map[string]any{
		"sut_id": platformid.Format(created.SUTID),
		"name":   created.Name,
	})
	return toEnvironmentView(created, nil, nil, nil, nil), nil
}

// UpdateEnvironment applies a partial environment update.
func (s *Service) UpdateEnvironment(ctx context.Context, id int64, patch EnvironmentPatch, actorID int64) (EnvironmentView, error) {
	environment, err := s.repo.GetEnvironment(ctx, id)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "")
	}
	if patch.Name != nil {
		environment.Name = *patch.Name
	}
	if patch.BaseURL != nil {
		environment.BaseURL = *patch.BaseURL
	}
	if patch.IsDefault != nil {
		environment.IsDefault = *patch.IsDefault
	}
	if patch.Status != nil {
		environment.Status = *patch.Status
	}
	environment.UpdatedAt = s.clock.Now()
	if err := environment.Validate(); err != nil {
		return EnvironmentView{}, err
	}
	if environment.IsDefault {
		if err := s.clearDefaultEnvironments(ctx, environment.SUTID, environment.ID); err != nil {
			return EnvironmentView{}, err
		}
	}
	updated, err := s.repo.UpdateEnvironment(ctx, environment)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "environment name already exists")
	}
	s.record(ctx, actorID, "resource.environment.updated", "environment", platformid.Format(updated.ID), map[string]any{
		"sut_id": platformid.Format(updated.SUTID),
		"name":   updated.Name,
	})
	return s.environmentView(ctx, updated)
}

// DeleteEnvironment deletes an environment when no version binding references
// it.
func (s *Service) DeleteEnvironment(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.EnvironmentInUse(ctx, id)
	}, "environment"); err != nil {
		return err
	}
	if err := s.repo.DeleteEnvironment(ctx, id); err != nil {
		return mapRepoError(err, "environment", "")
	}
	s.record(ctx, actorID, "resource.environment.deleted", "environment", platformid.Format(id), nil)
	return nil
}

// ListEnvironments returns every environment of a SUT with nested resources.
func (s *Service) ListEnvironments(ctx context.Context, sutID int64) ([]EnvironmentView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	environments, err := s.repo.ListEnvironments(ctx, sutID)
	if err != nil {
		return nil, err
	}
	views := make([]EnvironmentView, 0, len(environments))
	for _, environment := range environments {
		view, err := s.environmentView(ctx, environment)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// GetEnvironment returns one environment with nested pages, APIs, databases
// and accounts.
func (s *Service) GetEnvironment(ctx context.Context, id int64) (EnvironmentView, error) {
	environment, err := s.repo.GetEnvironment(ctx, id)
	if err != nil {
		return EnvironmentView{}, mapRepoError(err, "environment", "")
	}
	return s.environmentView(ctx, environment)
}

func (s *Service) environmentView(ctx context.Context, environment resource.Environment) (EnvironmentView, error) {
	pages, err := s.repo.ListPages(ctx, environment.ID)
	if err != nil {
		return EnvironmentView{}, err
	}
	apis, err := s.repo.ListAPIs(ctx, environment.ID)
	if err != nil {
		return EnvironmentView{}, err
	}
	databases, err := s.repo.ListDatabases(ctx, environment.SUTID)
	if err != nil {
		return EnvironmentView{}, err
	}
	accounts, err := s.repo.ListAccounts(ctx, environment.SUTID)
	if err != nil {
		return EnvironmentView{}, err
	}
	return toEnvironmentView(environment, pages, apis, databases, accounts), nil
}

func (s *Service) clearDefaultEnvironments(ctx context.Context, sutID, exceptID int64) error {
	environments, err := s.repo.ListEnvironments(ctx, sutID)
	if err != nil {
		return err
	}
	for _, environment := range environments {
		if environment.ID == exceptID || !environment.IsDefault {
			continue
		}
		environment.IsDefault = false
		environment.UpdatedAt = s.clock.Now()
		if _, err := s.repo.UpdateEnvironment(ctx, environment); err != nil {
			return mapRepoError(err, "environment", "")
		}
	}
	return nil
}

func defaultStatus(status string) string {
	if status == "" {
		return resource.StatusActive
	}
	return status
}

// CreatePage stores a page under an environment. The route template is derived
// from the URL when omitted; source is admin and status approved in P0.
func (s *Service) CreatePage(ctx context.Context, environmentID int64, input PageInput, actorID int64) (PageView, error) {
	environment, err := s.repo.GetEnvironment(ctx, environmentID)
	if err != nil {
		return PageView{}, mapRepoError(err, "environment", "")
	}
	page, err := s.buildPage(environment, input, resource.PageSourceAdmin)
	if err != nil {
		return PageView{}, err
	}
	created, err := s.insertPage(ctx, page, actorID)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "page route already exists in this environment")
	}
	return toPageView(created), nil
}

func (s *Service) buildPage(environment resource.Environment, input PageInput, source string) (resource.Page, error) {
	routeTemplate := input.RouteTemplate
	if routeTemplate == "" {
		derived, err := resource.DeriveRouteTemplate(input.URL)
		if err != nil {
			return resource.Page{}, err
		}
		routeTemplate = derived
	}
	pageType := input.PageType
	if pageType == "" {
		pageType = "page"
	}
	at := s.clock.Now()
	page := resource.Page{
		SUTID:         environment.SUTID,
		EnvironmentID: environment.ID,
		Name:          input.Name,
		URL:           input.URL,
		RouteTemplate: routeTemplate,
		PageType:      pageType,
		AuthRole:      input.AuthRole,
		Module:        input.Module,
		Status:        resource.PageStatusApproved,
		Source:        source,
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := page.Validate(); err != nil {
		return resource.Page{}, err
	}
	page.ID = s.ids.Next()
	return page, nil
}

func (s *Service) insertPage(ctx context.Context, page resource.Page, actorID int64) (resource.Page, error) {
	created, err := s.repo.CreatePage(ctx, page)
	if err != nil {
		return resource.Page{}, err
	}
	s.record(ctx, actorID, "resource.page.created", "page", platformid.Format(created.ID), map[string]any{
		"sut_id":         platformid.Format(created.SUTID),
		"environment_id": platformid.Format(created.EnvironmentID),
		"name":           created.Name,
		"route_template": created.RouteTemplate,
	})
	return created, nil
}

// UpdatePage applies a partial page update.
func (s *Service) UpdatePage(ctx context.Context, id int64, patch PagePatch, actorID int64) (PageView, error) {
	page, err := s.repo.GetPage(ctx, id)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "")
	}
	if patch.Name != nil {
		page.Name = *patch.Name
	}
	if patch.URL != nil {
		page.URL = *patch.URL
	}
	if patch.RouteTemplate != nil {
		page.RouteTemplate = *patch.RouteTemplate
	}
	if patch.PageType != nil {
		page.PageType = *patch.PageType
	}
	if patch.AuthRole != nil {
		page.AuthRole = *patch.AuthRole
	}
	if patch.Module != nil {
		page.Module = *patch.Module
	}
	page.UpdatedAt = s.clock.Now()
	if err := page.Validate(); err != nil {
		return PageView{}, err
	}
	updated, err := s.repo.UpdatePage(ctx, page)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "page route already exists in this environment")
	}
	s.record(ctx, actorID, "resource.page.updated", "page", platformid.Format(updated.ID), map[string]any{
		"sut_id":         platformid.Format(updated.SUTID),
		"environment_id": platformid.Format(updated.EnvironmentID),
		"name":           updated.Name,
		"route_template": updated.RouteTemplate,
	})
	return toPageView(updated), nil
}

// DeletePage deletes a page when no version binding references it.
func (s *Service) DeletePage(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.PageInUse(ctx, id)
	}, "page"); err != nil {
		return err
	}
	if err := s.repo.DeletePage(ctx, id); err != nil {
		return mapRepoError(err, "page", "")
	}
	s.record(ctx, actorID, "resource.page.deleted", "page", platformid.Format(id), nil)
	return nil
}

// ListPages returns every page of an environment.
func (s *Service) ListPages(ctx context.Context, environmentID int64) ([]PageView, error) {
	if _, err := s.repo.GetEnvironment(ctx, environmentID); err != nil {
		return nil, mapRepoError(err, "environment", "")
	}
	pages, err := s.repo.ListPages(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	views := make([]PageView, 0, len(pages))
	for _, page := range pages {
		views = append(views, toPageView(page))
	}
	return views, nil
}

// GetPage returns one page.
func (s *Service) GetPage(ctx context.Context, id int64) (PageView, error) {
	page, err := s.repo.GetPage(ctx, id)
	if err != nil {
		return PageView{}, mapRepoError(err, "page", "")
	}
	return toPageView(page), nil
}

// ImportPages validates the whole batch first, then inserts it. Duplicate
// route templates are skipped; any invalid item rejects the batch with 400.
func (s *Service) ImportPages(ctx context.Context, environmentID int64, inputs []PageInput, actorID int64) (ImportPagesResult, error) {
	environment, err := s.repo.GetEnvironment(ctx, environmentID)
	if err != nil {
		return ImportPagesResult{}, mapRepoError(err, "environment", "")
	}
	pages := make([]resource.Page, 0, len(inputs))
	for index, input := range inputs {
		page, err := s.buildPage(environment, input, resource.PageSourceImport)
		if err != nil {
			return ImportPagesResult{}, importItemError(index, err)
		}
		pages = append(pages, page)
	}

	result := ImportPagesResult{Items: []PageView{}}
	for _, page := range pages {
		created, err := s.insertPage(ctx, page, actorID)
		if err != nil {
			if errors.Is(err, resource.ErrDuplicate) {
				result.Skipped++
				continue
			}
			return ImportPagesResult{}, err
		}
		result.Created++
		result.Items = append(result.Items, toPageView(created))
	}
	return result, nil
}

// CreateAPI stores an API under an environment. Source is manual and status
// approved in P0.
func (s *Service) CreateAPI(ctx context.Context, environmentID int64, input APIInput, actorID int64) (APIView, error) {
	environment, err := s.repo.GetEnvironment(ctx, environmentID)
	if err != nil {
		return APIView{}, mapRepoError(err, "environment", "")
	}
	api, err := s.buildAPI(environment, input, resource.APISourceManual)
	if err != nil {
		return APIView{}, err
	}
	created, err := s.insertAPI(ctx, api, actorID)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "api route already exists in this environment")
	}
	return toAPIView(created), nil
}

func (s *Service) buildAPI(environment resource.Environment, input APIInput, source string) (resource.API, error) {
	at := s.clock.Now()
	api := resource.API{
		SUTID:         environment.SUTID,
		EnvironmentID: environment.ID,
		Method:        input.Method,
		PathTemplate:  input.PathTemplate,
		Name:          input.Name,
		AuthRole:      input.AuthRole,
		Source:        source,
		Status:        resource.APIStatusApproved,
		SchemaRef:     input.SchemaRef,
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := api.Validate(); err != nil {
		return resource.API{}, err
	}
	api.ID = s.ids.Next()
	return api, nil
}

func (s *Service) insertAPI(ctx context.Context, api resource.API, actorID int64) (resource.API, error) {
	created, err := s.repo.CreateAPI(ctx, api)
	if err != nil {
		return resource.API{}, err
	}
	s.record(ctx, actorID, "resource.api.created", "api", platformid.Format(created.ID), map[string]any{
		"sut_id":         platformid.Format(created.SUTID),
		"environment_id": platformid.Format(created.EnvironmentID),
		"name":           created.Name,
		"method":         created.Method,
		"path_template":  created.PathTemplate,
	})
	return created, nil
}

// UpdateAPI applies a partial API update.
func (s *Service) UpdateAPI(ctx context.Context, id int64, patch APIPatch, actorID int64) (APIView, error) {
	api, err := s.repo.GetAPI(ctx, id)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "")
	}
	if patch.Method != nil {
		api.Method = *patch.Method
	}
	if patch.PathTemplate != nil {
		api.PathTemplate = *patch.PathTemplate
	}
	if patch.Name != nil {
		api.Name = *patch.Name
	}
	if patch.AuthRole != nil {
		api.AuthRole = *patch.AuthRole
	}
	if patch.SchemaRef != nil {
		api.SchemaRef = *patch.SchemaRef
	}
	api.UpdatedAt = s.clock.Now()
	if err := api.Validate(); err != nil {
		return APIView{}, err
	}
	updated, err := s.repo.UpdateAPI(ctx, api)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "api route already exists in this environment")
	}
	s.record(ctx, actorID, "resource.api.updated", "api", platformid.Format(updated.ID), map[string]any{
		"sut_id":         platformid.Format(updated.SUTID),
		"environment_id": platformid.Format(updated.EnvironmentID),
		"name":           updated.Name,
		"method":         updated.Method,
		"path_template":  updated.PathTemplate,
	})
	return toAPIView(updated), nil
}

// DeleteAPI deletes an API when no version binding references it.
func (s *Service) DeleteAPI(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.APIInUse(ctx, id)
	}, "api"); err != nil {
		return err
	}
	if err := s.repo.DeleteAPI(ctx, id); err != nil {
		return mapRepoError(err, "api", "")
	}
	s.record(ctx, actorID, "resource.api.deleted", "api", platformid.Format(id), nil)
	return nil
}

// ListAPIs returns every API of an environment.
func (s *Service) ListAPIs(ctx context.Context, environmentID int64) ([]APIView, error) {
	if _, err := s.repo.GetEnvironment(ctx, environmentID); err != nil {
		return nil, mapRepoError(err, "environment", "")
	}
	apis, err := s.repo.ListAPIs(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	views := make([]APIView, 0, len(apis))
	for _, api := range apis {
		views = append(views, toAPIView(api))
	}
	return views, nil
}

// GetAPI returns one API.
func (s *Service) GetAPI(ctx context.Context, id int64) (APIView, error) {
	api, err := s.repo.GetAPI(ctx, id)
	if err != nil {
		return APIView{}, mapRepoError(err, "api", "")
	}
	return toAPIView(api), nil
}

// ImportAPIs validates the whole batch first, then inserts it. Duplicate
// method/path pairs are skipped; any invalid item rejects the batch with 400.
func (s *Service) ImportAPIs(ctx context.Context, environmentID int64, inputs []APIInput, actorID int64) (ImportAPIsResult, error) {
	environment, err := s.repo.GetEnvironment(ctx, environmentID)
	if err != nil {
		return ImportAPIsResult{}, mapRepoError(err, "environment", "")
	}
	apis := make([]resource.API, 0, len(inputs))
	for index, input := range inputs {
		api, err := s.buildAPI(environment, input, resource.APISourceImport)
		if err != nil {
			return ImportAPIsResult{}, importItemError(index, err)
		}
		apis = append(apis, api)
	}

	result := ImportAPIsResult{Items: []APIView{}}
	for _, api := range apis {
		created, err := s.insertAPI(ctx, api, actorID)
		if err != nil {
			if errors.Is(err, resource.ErrDuplicate) {
				result.Skipped++
				continue
			}
			return ImportAPIsResult{}, err
		}
		result.Created++
		result.Items = append(result.Items, toAPIView(created))
	}
	return result, nil
}

func importItemError(index int, err error) error {
	details := map[string]any{"field": "items", "index": index}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		details["reason"] = apiErr.Message
		if field, ok := apiErr.Details["field"]; ok {
			details["item_field"] = field
		}
	}
	return &platform.Error{
		Status:  400,
		Code:    "invalid_argument",
		Message: "import item is invalid",
		Details: details,
	}
}

// CreateDatabase stores a target database under a SUT environment.
func (s *Service) CreateDatabase(ctx context.Context, sutID int64, input DatabaseInput, actorID int64) (DatabaseView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return DatabaseView{}, mapRepoError(err, "sut", "")
	}
	environment, err := s.repo.GetEnvironment(ctx, input.EnvironmentID)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "environment", "")
	}
	if environment.SUTID != sutID {
		return DatabaseView{}, platform.Invalid("environment_id", "environment does not belong to the SUT")
	}
	readOnly := true
	if input.ReadOnly != nil {
		readOnly = *input.ReadOnly
	}
	at := s.clock.Now()
	database := resource.TargetDatabase{
		SUTID:         sutID,
		EnvironmentID: environment.ID,
		Name:          input.Name,
		Kind:          "postgres",
		DSN:           input.DSN,
		Username:      input.Username,
		Password:      input.Password,
		ReadOnly:      readOnly,
		Status:        defaultStatus(input.Status),
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := database.Validate(); err != nil {
		return DatabaseView{}, err
	}
	database.ID = s.ids.Next()
	created, err := s.repo.CreateDatabase(ctx, database)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "database name already exists in this environment")
	}
	s.record(ctx, actorID, "resource.database.created", "database", platformid.Format(created.ID), map[string]any{
		"sut_id":         platformid.Format(created.SUTID),
		"environment_id": platformid.Format(created.EnvironmentID),
		"name":           created.Name,
	})
	return toDatabaseView(created), nil
}

// UpdateDatabase applies a partial target-database update. A nil password
// keeps the stored value.
func (s *Service) UpdateDatabase(ctx context.Context, id int64, patch DatabasePatch, actorID int64) (DatabaseView, error) {
	database, err := s.repo.GetDatabase(ctx, id)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "")
	}
	if patch.Name != nil {
		database.Name = *patch.Name
	}
	if patch.DSN != nil {
		database.DSN = *patch.DSN
	}
	if patch.Username != nil {
		database.Username = *patch.Username
	}
	if patch.Password != nil {
		database.Password = *patch.Password
	}
	if patch.ReadOnly != nil {
		database.ReadOnly = *patch.ReadOnly
	}
	if patch.Status != nil {
		database.Status = *patch.Status
	}
	database.Kind = "postgres"
	database.UpdatedAt = s.clock.Now()
	if err := database.Validate(); err != nil {
		return DatabaseView{}, err
	}
	updated, err := s.repo.UpdateDatabase(ctx, database)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "database name already exists in this environment")
	}
	s.record(ctx, actorID, "resource.database.updated", "database", platformid.Format(updated.ID), map[string]any{
		"sut_id":         platformid.Format(updated.SUTID),
		"environment_id": platformid.Format(updated.EnvironmentID),
		"name":           updated.Name,
	})
	return toDatabaseView(updated), nil
}

// DeleteDatabase deletes a target database when no version binding references
// it.
func (s *Service) DeleteDatabase(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.DatabaseInUse(ctx, id)
	}, "database"); err != nil {
		return err
	}
	if err := s.repo.DeleteDatabase(ctx, id); err != nil {
		return mapRepoError(err, "database", "")
	}
	s.record(ctx, actorID, "resource.database.deleted", "database", platformid.Format(id), nil)
	return nil
}

// ListDatabases returns every target database of a SUT.
func (s *Service) ListDatabases(ctx context.Context, sutID int64) ([]DatabaseView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	databases, err := s.repo.ListDatabases(ctx, sutID)
	if err != nil {
		return nil, err
	}
	views := make([]DatabaseView, 0, len(databases))
	for _, database := range databases {
		views = append(views, toDatabaseView(database))
	}
	return views, nil
}

// GetDatabase returns one target database.
func (s *Service) GetDatabase(ctx context.Context, id int64) (DatabaseView, error) {
	database, err := s.repo.GetDatabase(ctx, id)
	if err != nil {
		return DatabaseView{}, mapRepoError(err, "database", "")
	}
	return toDatabaseView(database), nil
}

// CreateAccount stores a test account under a SUT environment.
func (s *Service) CreateAccount(ctx context.Context, sutID int64, input AccountInput, actorID int64) (AccountView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return AccountView{}, mapRepoError(err, "sut", "")
	}
	environment, err := s.repo.GetEnvironment(ctx, input.EnvironmentID)
	if err != nil {
		return AccountView{}, mapRepoError(err, "environment", "")
	}
	if environment.SUTID != sutID {
		return AccountView{}, platform.Invalid("environment_id", "environment does not belong to the SUT")
	}
	roles := input.Roles
	if roles == nil {
		roles = []string{}
	}
	at := s.clock.Now()
	account := resource.TestAccount{
		SUTID:         sutID,
		EnvironmentID: environment.ID,
		Username:      input.Username,
		Password:      input.Password,
		Roles:         roles,
		Notes:         input.Notes,
		Status:        defaultStatus(input.Status),
		CreatedAt:     at,
		UpdatedAt:     at,
	}
	if err := account.Validate(); err != nil {
		return AccountView{}, err
	}
	account.ID = s.ids.Next()
	created, err := s.repo.CreateAccount(ctx, account)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "account username already exists in this environment")
	}
	s.record(ctx, actorID, "resource.account.created", "account", platformid.Format(created.ID), map[string]any{
		"sut_id":         platformid.Format(created.SUTID),
		"environment_id": platformid.Format(created.EnvironmentID),
		"username":       created.Username,
	})
	return toAccountView(created), nil
}

// UpdateAccount applies a partial test-account update. A nil password keeps the
// stored password.
func (s *Service) UpdateAccount(ctx context.Context, id int64, patch AccountPatch, actorID int64) (AccountView, error) {
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "")
	}
	if patch.Username != nil {
		account.Username = *patch.Username
	}
	if patch.Password != nil {
		account.Password = *patch.Password
	}
	if patch.Roles != nil {
		account.Roles = *patch.Roles
	}
	if patch.Notes != nil {
		account.Notes = *patch.Notes
	}
	if patch.Status != nil {
		account.Status = *patch.Status
	}
	account.UpdatedAt = s.clock.Now()
	if err := account.ValidateUpdate(); err != nil {
		return AccountView{}, err
	}
	updated, err := s.repo.UpdateAccount(ctx, account)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "account username already exists in this environment")
	}
	s.record(ctx, actorID, "resource.account.updated", "account", platformid.Format(updated.ID), map[string]any{
		"sut_id":         platformid.Format(updated.SUTID),
		"environment_id": platformid.Format(updated.EnvironmentID),
		"username":       updated.Username,
	})
	return toAccountView(updated), nil
}

// DeleteAccount deletes a test account when no version binding references it.
func (s *Service) DeleteAccount(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.AccountInUse(ctx, id)
	}, "account"); err != nil {
		return err
	}
	if err := s.repo.DeleteAccount(ctx, id); err != nil {
		return mapRepoError(err, "account", "")
	}
	s.record(ctx, actorID, "resource.account.deleted", "account", platformid.Format(id), nil)
	return nil
}

// ListAccounts returns every test account of a SUT.
func (s *Service) ListAccounts(ctx context.Context, sutID int64) ([]AccountView, error) {
	if _, err := s.repo.GetSUT(ctx, sutID); err != nil {
		return nil, mapRepoError(err, "sut", "")
	}
	accounts, err := s.repo.ListAccounts(ctx, sutID)
	if err != nil {
		return nil, err
	}
	views := make([]AccountView, 0, len(accounts))
	for _, account := range accounts {
		views = append(views, toAccountView(account))
	}
	return views, nil
}

// GetAccount returns one test account.
func (s *Service) GetAccount(ctx context.Context, id int64) (AccountView, error) {
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return AccountView{}, mapRepoError(err, "account", "")
	}
	return toAccountView(account), nil
}

// GetAccountSecret returns the stored username/password of one test account.
// It is an application-only port for the execution context (P3 §6.6) and is
// never exposed through HTTP or logged. Callers resolve the account through the
// version SUT binding first.
func (s *Service) GetAccountSecret(ctx context.Context, accountID int64) (username, password string, err error) {
	if accountID <= 0 {
		return "", "", platform.Invalid("account_id", "must be a positive decimal ID")
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return "", "", mapRepoError(err, "account", "")
	}
	return account.Username, account.Password, nil
}

// GetDatabaseSecret returns the stored DSN/username/password of one target
// database. It is an application-only port for the execution context (P3 §6.5)
// and is never exposed through HTTP or logged.
func (s *Service) GetDatabaseSecret(ctx context.Context, databaseID int64) (dsn, username, password string, err error) {
	if databaseID <= 0 {
		return "", "", "", platform.Invalid("database_id", "must be a positive decimal ID")
	}
	database, err := s.repo.GetDatabase(ctx, databaseID)
	if err != nil {
		return "", "", "", mapRepoError(err, "database", "")
	}
	return database.DSN, database.Username, database.Password, nil
}

// CreateRepository stores a code repository. Any authenticated user may create
// one; CreatedBy records the actor.
func (s *Service) CreateRepository(ctx context.Context, input RepositoryInput, actorID int64) (CodeRepositoryView, error) {
	include := input.Include
	if include == nil {
		include = []string{}
	}
	exclude := input.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	at := s.clock.Now()
	repository := resource.CodeRepository{
		Name:      input.Name,
		Path:      input.Path,
		Branch:    input.Branch,
		Include:   include,
		Exclude:   exclude,
		CreatedBy: actorID,
		Status:    defaultStatus(input.Status),
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := repository.Validate(); err != nil {
		return CodeRepositoryView{}, err
	}
	repository.ID = s.ids.Next()
	created, err := s.repo.CreateRepository(ctx, repository)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "repository name already exists")
	}
	s.record(ctx, actorID, "resource.code_repo.created", "code_repository", platformid.Format(created.ID), map[string]any{
		"name": created.Name,
	})
	return toCodeRepositoryView(created), nil
}

// UpdateRepository applies a partial code-repository update.
func (s *Service) UpdateRepository(ctx context.Context, id int64, patch RepositoryPatch, actorID int64) (CodeRepositoryView, error) {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "")
	}
	if patch.Name != nil {
		repository.Name = *patch.Name
	}
	if patch.Path != nil {
		repository.Path = *patch.Path
	}
	if patch.Branch != nil {
		repository.Branch = *patch.Branch
	}
	if patch.Include != nil {
		repository.Include = *patch.Include
	}
	if patch.Exclude != nil {
		repository.Exclude = *patch.Exclude
	}
	if patch.Status != nil {
		repository.Status = *patch.Status
	}
	repository.UpdatedAt = s.clock.Now()
	if err := repository.Validate(); err != nil {
		return CodeRepositoryView{}, err
	}
	updated, err := s.repo.UpdateRepository(ctx, repository)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "repository name already exists")
	}
	s.record(ctx, actorID, "resource.code_repo.updated", "code_repository", platformid.Format(updated.ID), map[string]any{
		"name": updated.Name,
	})
	return toCodeRepositoryView(updated), nil
}

// DeleteRepository deletes a code repository when no version binding
// references it.
func (s *Service) DeleteRepository(ctx context.Context, id int64, actorID int64) error {
	if err := s.checkUsage(ctx, func(ctx context.Context) (bool, error) {
		return s.usage.RepositoryInUse(ctx, id)
	}, "repository"); err != nil {
		return err
	}
	if err := s.repo.DeleteRepository(ctx, id); err != nil {
		return mapRepoError(err, "code repository", "")
	}
	s.record(ctx, actorID, "resource.code_repo.deleted", "code_repository", platformid.Format(id), nil)
	return nil
}

// ListRepositories returns one page of code repositories ordered by ID
// descending.
func (s *Service) ListRepositories(ctx context.Context, limit int, beforeID *int64) ([]CodeRepositoryView, bool, error) {
	limit = normalizeLimit(limit)
	items, err := s.repo.ListRepositories(ctx, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	views := make([]CodeRepositoryView, 0, len(items))
	for _, item := range items {
		views = append(views, toCodeRepositoryView(item))
	}
	return views, hasMore, nil
}

// GetRepository returns one code repository.
func (s *Service) GetRepository(ctx context.Context, id int64) (CodeRepositoryView, error) {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return CodeRepositoryView{}, mapRepoError(err, "code repository", "")
	}
	return toCodeRepositoryView(repository), nil
}

// Catalog returns every SUT with its full nested resource tree, ordered by SUT
// ID ascending.
func (s *Service) Catalog(ctx context.Context) ([]SUTView, error) {
	graphs, err := s.repo.ListCatalog(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]SUTView, 0, len(graphs))
	for _, graph := range graphs {
		views = append(views, toSUTDetailView(graph))
	}
	return views, nil
}

var _ CatalogReader = (*Service)(nil)

// record writes the best-effort audit entry and system event for a mutation.
func (s *Service) record(ctx context.Context, actorID int64, action, kind, targetID string, metadata map[string]any) {
	payload := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		payload[key] = value
	}
	payload["id"] = targetID

	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	requestID := requestid.From(ctx)

	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    actor,
			Action:     action,
			TargetKind: kind,
			TargetID:   targetID,
			RequestID:  requestID,
			Metadata:   payload,
		})
	}
	if s.events != nil {
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   "system",
			Type:      action,
			Scope:     map[string]any{},
			Payload:   payload,
			ActorID:   actor,
			RequestID: requestID,
		})
	}
}

// checkUsage runs a usage guard; a nil guard means "not referenced".
func (s *Service) checkUsage(ctx context.Context, check func(context.Context) (bool, error), kind string) error {
	if s.usage == nil {
		return nil
	}
	inUse, err := check(ctx)
	if err != nil {
		return err
	}
	if inUse {
		return platform.Conflict(kind + " is referenced by a version binding")
	}
	return nil
}

func mapRepoError(err error, kind, conflictMessage string) error {
	switch {
	case errors.Is(err, resource.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, resource.ErrDuplicate):
		if conflictMessage == "" {
			conflictMessage = kind + " already exists"
		}
		return platform.Conflict(conflictMessage)
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

func toSUTView(sut resource.SUT) SUTView {
	return SUTView{
		ID:           platformid.Format(sut.ID),
		Name:         sut.Name,
		Intro:        sut.Intro,
		Environments: []EnvironmentView{},
		CreatedAt:    sut.CreatedAt,
		UpdatedAt:    sut.UpdatedAt,
	}
}

func toSUTDetailView(graph resource.SUTGraph) SUTView {
	view := toSUTView(graph.SUT)
	environments := make([]EnvironmentView, 0, len(graph.Environments))
	for _, environment := range graph.Environments {
		environments = append(environments, toEnvironmentView(
			environment, graph.Pages, graph.APIs, graph.Databases, graph.Accounts,
		))
	}
	view.Environments = environments
	return view
}

func toEnvironmentView(
	environment resource.Environment,
	pages []resource.Page,
	apis []resource.API,
	databases []resource.TargetDatabase,
	accounts []resource.TestAccount,
) EnvironmentView {
	view := EnvironmentView{
		ID:        platformid.Format(environment.ID),
		SUTID:     platformid.Format(environment.SUTID),
		Name:      environment.Name,
		BaseURL:   environment.BaseURL,
		IsDefault: environment.IsDefault,
		Status:    environment.Status,
		Pages:     []PageView{},
		APIs:      []APIView{},
		Databases: []DatabaseView{},
		Accounts:  []AccountView{},
		CreatedAt: environment.CreatedAt,
		UpdatedAt: environment.UpdatedAt,
	}
	for _, page := range pages {
		if page.EnvironmentID == environment.ID {
			view.Pages = append(view.Pages, toPageView(page))
		}
	}
	for _, api := range apis {
		if api.EnvironmentID == environment.ID {
			view.APIs = append(view.APIs, toAPIView(api))
		}
	}
	for _, database := range databases {
		if database.EnvironmentID == environment.ID {
			view.Databases = append(view.Databases, toDatabaseView(database))
		}
	}
	for _, account := range accounts {
		if account.EnvironmentID == environment.ID {
			view.Accounts = append(view.Accounts, toAccountView(account))
		}
	}
	return view
}

func toPageView(page resource.Page) PageView {
	return PageView{
		ID:            platformid.Format(page.ID),
		SUTID:         platformid.Format(page.SUTID),
		EnvironmentID: platformid.Format(page.EnvironmentID),
		Name:          page.Name,
		URL:           page.URL,
		RouteTemplate: page.RouteTemplate,
		PageType:      page.PageType,
		AuthRole:      page.AuthRole,
		Module:        page.Module,
		Status:        page.Status,
		Source:        page.Source,
		CreatedAt:     page.CreatedAt,
		UpdatedAt:     page.UpdatedAt,
	}
}

func toAPIView(api resource.API) APIView {
	return APIView{
		ID:            platformid.Format(api.ID),
		SUTID:         platformid.Format(api.SUTID),
		EnvironmentID: platformid.Format(api.EnvironmentID),
		Method:        api.Method,
		PathTemplate:  api.PathTemplate,
		Name:          api.Name,
		AuthRole:      api.AuthRole,
		Source:        api.Source,
		Status:        api.Status,
		SchemaRef:     api.SchemaRef,
		CreatedAt:     api.CreatedAt,
		UpdatedAt:     api.UpdatedAt,
	}
}

func toDatabaseView(database resource.TargetDatabase) DatabaseView {
	return DatabaseView{
		ID:            platformid.Format(database.ID),
		SUTID:         platformid.Format(database.SUTID),
		EnvironmentID: platformid.Format(database.EnvironmentID),
		Name:          database.Name,
		DSN:           database.DSN,
		Username:      database.Username,
		PasswordSet:   database.Password != "",
		ReadOnly:      database.ReadOnly,
		Status:        database.Status,
		CreatedAt:     database.CreatedAt,
		UpdatedAt:     database.UpdatedAt,
	}
}

func toAccountView(account resource.TestAccount) AccountView {
	roles := account.Roles
	if roles == nil {
		roles = []string{}
	}
	return AccountView{
		ID:            platformid.Format(account.ID),
		SUTID:         platformid.Format(account.SUTID),
		EnvironmentID: platformid.Format(account.EnvironmentID),
		Username:      account.Username,
		Roles:         roles,
		Notes:         account.Notes,
		Status:        account.Status,
		PasswordSet:   account.Password != "",
		CreatedAt:     account.CreatedAt,
		UpdatedAt:     account.UpdatedAt,
	}
}

func toCodeRepositoryView(repository resource.CodeRepository) CodeRepositoryView {
	include := repository.Include
	if include == nil {
		include = []string{}
	}
	exclude := repository.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	return CodeRepositoryView{
		ID:        platformid.Format(repository.ID),
		Name:      repository.Name,
		Path:      repository.Path,
		Branch:    repository.Branch,
		Include:   include,
		Exclude:   exclude,
		CreatedBy: repository.CreatedBy,
		Status:    repository.Status,
		CreatedAt: repository.CreatedAt,
		UpdatedAt: repository.UpdatedAt,
	}
}
