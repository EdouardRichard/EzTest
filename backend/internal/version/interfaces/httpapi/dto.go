package versionapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- response DTOs ----------

type versionDTO struct {
	ID                        string               `json:"id"`
	Title                     string               `json:"title"`
	Description               string               `json:"description"`
	Goal                      string               `json:"goal"`
	Status                    string               `json:"status"`
	AutoDesignNewRequirements bool                 `json:"auto_design_new_requirements"`
	Bindings                  []bindingDTO         `json:"bindings"`
	RequirementCounts         requirementCountsDTO `json:"requirement_counts"`
	ConversationCount         int                  `json:"conversation_count"`
	PendingDesignCount        int                  `json:"pending_design_count"`
	CaseCounts                caseCountsDTO        `json:"case_counts"`
	ClosedAt                  *string              `json:"closed_at"`
	CloseReason               *string              `json:"close_reason"`
	CreatedBy                 string               `json:"created_by"`
	CreatedAt                 string               `json:"created_at"`
	UpdatedAt                 string               `json:"updated_at"`
}

// caseCountsDTO is the P2 version-detail increment (current versions only).
type caseCountsDTO struct {
	Draft     int `json:"draft"`
	Confirmed int `json:"confirmed"`
	Stale     int `json:"stale"`
}

// requirementCountsDTO is the P1 version-detail increment.
type requirementCountsDTO struct {
	Total     int `json:"total"`
	Draft     int `json:"draft"`
	Clarified int `json:"clarified"`
	Closed    int `json:"closed"`
	Obsolete  int `json:"obsolete"`
}

type bindingDTO struct {
	ID                 string                 `json:"id"`
	CodeBaselineCommit *string                `json:"codeBaselineCommit,omitempty"`
	SUT                sutDTO                 `json:"sut"`
	Environment        environmentDTO         `json:"environment"`
	Repository         *codeRepositoryDTO     `json:"repository"`
	Repositories       []bindingRepositoryDTO `json:"repositories"`
	ScopeExclude       scopeExcludeDTO        `json:"scope_exclude"`
	Pages              []pageDTO              `json:"pages"`
	APIs               []apiDTO               `json:"apis"`
	Databases          []databaseDTO          `json:"databases"`
	Accounts           []accountDTO           `json:"accounts"`
}

// scopeExcludeDTO is the v0.22 environment-level exclusion document (DB-11).
type scopeExcludeDTO struct {
	Pages     []string `json:"pages"`
	APIs      []string `json:"apis"`
	Databases []string `json:"databases"`
}

// bindingRepositoryDTO is one effective repository of a version binding.
type bindingRepositoryDTO struct {
	Repository codeRepositoryDTO `json:"repository"`
	Role       string            `json:"role"`
	Source     string            `json:"source"`
}

type versionSummaryDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	SUTCount  int    `json:"sut_count"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type sutDTO struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Intro        string           `json:"intro"`
	Environments []environmentDTO `json:"environments"`
	CreatedAt    string           `json:"created_at"`
	UpdatedAt    string           `json:"updated_at"`
}

type environmentDTO struct {
	ID        string        `json:"id"`
	SUTID     string        `json:"sut_id"`
	Name      string        `json:"name"`
	BaseURL   string        `json:"base_url"`
	IsDefault bool          `json:"is_default"`
	Status    string        `json:"status"`
	Pages     []pageDTO     `json:"pages"`
	APIs      []apiDTO      `json:"apis"`
	Databases []databaseDTO `json:"databases"`
	Accounts  []accountDTO  `json:"accounts"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
}

type pageDTO struct {
	ID            string `json:"id"`
	SUTID         string `json:"sut_id"`
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	RouteTemplate string `json:"route_template"`
	PageType      string `json:"page_type"`
	AuthRole      string `json:"auth_role"`
	Module        string `json:"module"`
	Status        string `json:"status"`
	Source        string `json:"source"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type apiDTO struct {
	ID            string `json:"id"`
	SUTID         string `json:"sut_id"`
	EnvironmentID string `json:"environment_id"`
	Method        string `json:"method"`
	PathTemplate  string `json:"path_template"`
	Name          string `json:"name"`
	AuthRole      string `json:"auth_role"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	SchemaRef     string `json:"schema_ref"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type databaseDTO struct {
	ID            string `json:"id"`
	SUTID         string `json:"sut_id"`
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
	DSN           string `json:"dsn"`
	Username      string `json:"username"`
	PasswordSet   bool   `json:"password_set"`
	ReadOnly      bool   `json:"read_only"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type accountDTO struct {
	ID            string   `json:"id"`
	SUTID         string   `json:"sut_id"`
	EnvironmentID string   `json:"environment_id"`
	Username      string   `json:"username"`
	Roles         []string `json:"roles"`
	Notes         string   `json:"notes"`
	Status        string   `json:"status"`
	PasswordSet   bool     `json:"password_set"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type codeRepositoryDTO struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Branch    string   `json:"branch"`
	Include   []string `json:"include"`
	Exclude   []string `json:"exclude"`
	CreatedBy string   `json:"created_by"`
	Status    string   `json:"status"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

func toVersionDTO(view versionapp.VersionView) versionDTO {
	bindings := make([]bindingDTO, 0, len(view.Bindings))
	for _, binding := range view.Bindings {
		bindings = append(bindings, toBindingDTO(binding))
	}
	return versionDTO{
		ID:                        strconv.FormatInt(view.ID, 10),
		Title:                     view.Title,
		Description:               view.Description,
		Goal:                      view.Goal,
		Status:                    view.Status,
		AutoDesignNewRequirements: view.AutoDesignNewRequirements,
		Bindings:                  bindings,
		RequirementCounts: requirementCountsDTO{
			Total:     view.RequirementCounts.Total,
			Draft:     view.RequirementCounts.Draft,
			Clarified: view.RequirementCounts.Clarified,
			Closed:    view.RequirementCounts.Closed,
			Obsolete:  view.RequirementCounts.Obsolete,
		},
		ConversationCount:  view.ConversationCount,
		PendingDesignCount: view.PendingDesignCount,
		CaseCounts: caseCountsDTO{
			Draft:     view.CaseCounts.Draft,
			Confirmed: view.CaseCounts.Confirmed,
			Stale:     view.CaseCounts.Stale,
		},
		ClosedAt:    formatOptionalTime(view.ClosedAt),
		CloseReason: formatOptionalString(view.CloseReason),
		CreatedBy:   strconv.FormatInt(view.CreatedBy, 10),
		CreatedAt:   formatTime(view.CreatedAt),
		UpdatedAt:   formatTime(view.UpdatedAt),
	}
}

func toBindingDTO(view versionapp.BindingView) bindingDTO {
	pages := make([]pageDTO, 0, len(view.Pages))
	for _, page := range view.Pages {
		pages = append(pages, toPageDTO(page))
	}
	apis := make([]apiDTO, 0, len(view.APIs))
	for _, api := range view.APIs {
		apis = append(apis, toAPIDTO(api))
	}
	databases := make([]databaseDTO, 0, len(view.Databases))
	for _, database := range view.Databases {
		databases = append(databases, toDatabaseDTO(database))
	}
	accounts := make([]accountDTO, 0, len(view.Accounts))
	for _, account := range view.Accounts {
		accounts = append(accounts, toAccountDTO(account))
	}
	var repository *codeRepositoryDTO
	if view.Repository != nil {
		converted := toCodeRepositoryDTO(*view.Repository)
		repository = &converted
	}
	repositories := make([]bindingRepositoryDTO, 0, len(view.Repositories))
	for _, item := range view.Repositories {
		repositories = append(repositories, bindingRepositoryDTO{
			Repository: toCodeRepositoryDTO(item.Repository),
			Role:       item.Role,
			Source:     item.Source,
		})
	}
	return bindingDTO{
		ID:                 strconv.FormatInt(view.ID, 10),
		CodeBaselineCommit: formatOptionalString(view.CodeBaselineCommit),
		SUT:                toSUTDTO(view.SUT),
		Environment:        toEnvironmentDTO(view.Environment),
		Repository:         repository,
		Repositories:       repositories,
		ScopeExclude:       toScopeExcludeDTO(view.ScopeExclude),
		Pages:              pages,
		APIs:               apis,
		Databases:          databases,
		Accounts:           accounts,
	}
}

func toScopeExcludeDTO(scope version.ScopeExclude) scopeExcludeDTO {
	return scopeExcludeDTO{
		Pages:     decimalIDs(scope.Pages),
		APIs:      decimalIDs(scope.APIs),
		Databases: decimalIDs(scope.Databases),
	}
}

func decimalIDs(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

func toVersionSummaryDTO(view versionapp.VersionSummary) versionSummaryDTO {
	return versionSummaryDTO{
		ID:        strconv.FormatInt(view.ID, 10),
		Title:     view.Title,
		Status:    view.Status,
		SUTCount:  view.SUTCount,
		CreatedAt: formatTime(view.CreatedAt),
		UpdatedAt: formatTime(view.UpdatedAt),
	}
}

func toSUTDTO(view resourceapp.SUTView) sutDTO {
	environments := make([]environmentDTO, 0, len(view.Environments))
	for _, environment := range view.Environments {
		environments = append(environments, toEnvironmentDTO(environment))
	}
	return sutDTO{
		ID:           view.ID,
		Name:         view.Name,
		Intro:        view.Intro,
		Environments: environments,
		CreatedAt:    formatTime(view.CreatedAt),
		UpdatedAt:    formatTime(view.UpdatedAt),
	}
}

func toEnvironmentDTO(view resourceapp.EnvironmentView) environmentDTO {
	pages := make([]pageDTO, 0, len(view.Pages))
	for _, page := range view.Pages {
		pages = append(pages, toPageDTO(page))
	}
	apis := make([]apiDTO, 0, len(view.APIs))
	for _, api := range view.APIs {
		apis = append(apis, toAPIDTO(api))
	}
	databases := make([]databaseDTO, 0, len(view.Databases))
	for _, database := range view.Databases {
		databases = append(databases, toDatabaseDTO(database))
	}
	accounts := make([]accountDTO, 0, len(view.Accounts))
	for _, account := range view.Accounts {
		accounts = append(accounts, toAccountDTO(account))
	}
	return environmentDTO{
		ID:        view.ID,
		SUTID:     view.SUTID,
		Name:      view.Name,
		BaseURL:   view.BaseURL,
		IsDefault: view.IsDefault,
		Status:    view.Status,
		Pages:     pages,
		APIs:      apis,
		Databases: databases,
		Accounts:  accounts,
		CreatedAt: formatTime(view.CreatedAt),
		UpdatedAt: formatTime(view.UpdatedAt),
	}
}

func toPageDTO(view resourceapp.PageView) pageDTO {
	return pageDTO{
		ID:            view.ID,
		SUTID:         view.SUTID,
		EnvironmentID: view.EnvironmentID,
		Name:          view.Name,
		URL:           view.URL,
		RouteTemplate: view.RouteTemplate,
		PageType:      view.PageType,
		AuthRole:      view.AuthRole,
		Module:        view.Module,
		Status:        view.Status,
		Source:        view.Source,
		CreatedAt:     formatTime(view.CreatedAt),
		UpdatedAt:     formatTime(view.UpdatedAt),
	}
}

func toAPIDTO(view resourceapp.APIView) apiDTO {
	return apiDTO{
		ID:            view.ID,
		SUTID:         view.SUTID,
		EnvironmentID: view.EnvironmentID,
		Method:        view.Method,
		PathTemplate:  view.PathTemplate,
		Name:          view.Name,
		AuthRole:      view.AuthRole,
		Source:        view.Source,
		Status:        view.Status,
		SchemaRef:     view.SchemaRef,
		CreatedAt:     formatTime(view.CreatedAt),
		UpdatedAt:     formatTime(view.UpdatedAt),
	}
}

func toDatabaseDTO(view resourceapp.DatabaseView) databaseDTO {
	return databaseDTO{
		ID:            view.ID,
		SUTID:         view.SUTID,
		EnvironmentID: view.EnvironmentID,
		Name:          view.Name,
		DSN:           view.DSN,
		Username:      view.Username,
		PasswordSet:   view.PasswordSet,
		ReadOnly:      view.ReadOnly,
		Status:        view.Status,
		CreatedAt:     formatTime(view.CreatedAt),
		UpdatedAt:     formatTime(view.UpdatedAt),
	}
}

func toAccountDTO(view resourceapp.AccountView) accountDTO {
	roles := view.Roles
	if roles == nil {
		roles = []string{}
	}
	return accountDTO{
		ID:            view.ID,
		SUTID:         view.SUTID,
		EnvironmentID: view.EnvironmentID,
		Username:      view.Username,
		Roles:         roles,
		Notes:         view.Notes,
		Status:        view.Status,
		PasswordSet:   view.PasswordSet,
		CreatedAt:     formatTime(view.CreatedAt),
		UpdatedAt:     formatTime(view.UpdatedAt),
	}
}

func toCodeRepositoryDTO(view resourceapp.CodeRepositoryView) codeRepositoryDTO {
	include := view.Include
	if include == nil {
		include = []string{}
	}
	exclude := view.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	return codeRepositoryDTO{
		ID:        view.ID,
		Name:      view.Name,
		Path:      view.Path,
		Branch:    view.Branch,
		Include:   include,
		Exclude:   exclude,
		CreatedBy: strconv.FormatInt(view.CreatedBy, 10),
		Status:    view.Status,
		CreatedAt: formatTime(view.CreatedAt),
		UpdatedAt: formatTime(view.UpdatedAt),
	}
}

// ---------- request payloads ----------

// createVersionRequest is the v0.22 API-15 environment-only version payload.
// The SUT is derived from the environment; pages, APIs, databases, accounts and
// repositories are no longer accepted at creation (R-C3-2).
type createVersionRequest struct {
	Title         string `json:"title"`
	Description   string `json:"description"`
	Goal          string `json:"goal"`
	EnvironmentID string `json:"environment_id"`
}

type updateVersionRequest struct {
	Title                     *string `json:"title"`
	AutoDesignNewRequirements *bool   `json:"auto_design_new_requirements"`
}

// reopenVersionRequest is the P4 manual reopen payload; reason is required
// (openapi VersionReopenRequest, 1..2000).
type reopenVersionRequest struct {
	Reason string `json:"reason"`
}

func (r createVersionRequest) command() (versionapp.CreateCommand, error) {
	environmentID, err := validation.NonEmptyID("environment_id", r.EnvironmentID)
	if err != nil {
		return versionapp.CreateCommand{}, err
	}
	return versionapp.CreateCommand{
		Title:         r.Title,
		Description:   r.Description,
		Goal:          r.Goal,
		EnvironmentID: environmentID,
	}, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

// formatOptionalString renders an optional string projection as JSON null when
// the value is empty.
func formatOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
