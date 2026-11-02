package resourceapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

type sutDTO struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Intro        string           `json:"intro"`
	Family       *string          `json:"family"`
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

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
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
		Family:       view.Family,
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

type sutCreateRequest struct {
	Name   string  `json:"name"`
	Intro  string  `json:"intro"`
	Family *string `json:"family"`
}

type sutPatchRequest struct {
	Name   *string `json:"name"`
	Intro  *string `json:"intro"`
	Family *string `json:"family"`
}

type environmentCreateRequest struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	IsDefault bool   `json:"is_default"`
	Status    string `json:"status"`
}

type environmentPatchRequest struct {
	Name      *string `json:"name"`
	BaseURL   *string `json:"base_url"`
	IsDefault *bool   `json:"is_default"`
	Status    *string `json:"status"`
}

type pageCreateRequest struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	RouteTemplate string `json:"route_template"`
	PageType      string `json:"page_type"`
	AuthRole      string `json:"auth_role"`
	Module        string `json:"module"`
}

type pagePatchRequest struct {
	Name          *string `json:"name"`
	URL           *string `json:"url"`
	RouteTemplate *string `json:"route_template"`
	PageType      *string `json:"page_type"`
	AuthRole      *string `json:"auth_role"`
	Module        *string `json:"module"`
}

type apiCreateRequest struct {
	Method       string `json:"method"`
	PathTemplate string `json:"path_template"`
	Name         string `json:"name"`
	AuthRole     string `json:"auth_role"`
	SchemaRef    string `json:"schema_ref"`
}

type apiPatchRequest struct {
	Method       *string `json:"method"`
	PathTemplate *string `json:"path_template"`
	Name         *string `json:"name"`
	AuthRole     *string `json:"auth_role"`
	SchemaRef    *string `json:"schema_ref"`
}

type databaseCreateRequest struct {
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
	DSN           string `json:"dsn"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	ReadOnly      *bool  `json:"read_only"`
	Status        string `json:"status"`
}

type databasePatchRequest struct {
	Name     *string `json:"name"`
	DSN      *string `json:"dsn"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	ReadOnly *bool   `json:"read_only"`
	Status   *string `json:"status"`
}

type accountCreateRequest struct {
	EnvironmentID string   `json:"environment_id"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	Roles         []string `json:"roles"`
	Notes         string   `json:"notes"`
	Status        string   `json:"status"`
}

type accountPatchRequest struct {
	Username *string   `json:"username"`
	Password *string   `json:"password"`
	Roles    *[]string `json:"roles"`
	Notes    *string   `json:"notes"`
	Status   *string   `json:"status"`
}

type repositoryCreateRequest struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Branch  string   `json:"branch"`
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
	Status  string   `json:"status"`
}

type repositoryPatchRequest struct {
	Name    *string   `json:"name"`
	Path    *string   `json:"path"`
	Branch  *string   `json:"branch"`
	Include *[]string `json:"include"`
	Exclude *[]string `json:"exclude"`
	Status  *string   `json:"status"`
}

type importPagesRequest struct {
	Items []pageCreateRequest `json:"items"`
}

type importAPIsRequest struct {
	Items []apiCreateRequest `json:"items"`
}

type importPagesDTO struct {
	Created int       `json:"created"`
	Skipped int       `json:"skipped"`
	Items   []pageDTO `json:"items"`
}

type importAPIsDTO struct {
	Created int      `json:"created"`
	Skipped int      `json:"skipped"`
	Items   []apiDTO `json:"items"`
}
