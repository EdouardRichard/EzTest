package versionapi

import (
	"fmt"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// ---------- response DTOs ----------

type versionDTO struct {
	ID                        string       `json:"id"`
	Title                     string       `json:"title"`
	Status                    string       `json:"status"`
	AutoDesignNewRequirements bool         `json:"auto_design_new_requirements"`
	Bindings                  []bindingDTO `json:"bindings"`
	CreatedBy                 string       `json:"created_by"`
	CreatedAt                 string       `json:"created_at"`
	UpdatedAt                 string       `json:"updated_at"`
}

type bindingDTO struct {
	ID          string             `json:"id"`
	SUT         sutDTO             `json:"sut"`
	Environment environmentDTO     `json:"environment"`
	Repository  *codeRepositoryDTO `json:"repository"`
	Pages       []pageDTO          `json:"pages"`
	APIs        []apiDTO           `json:"apis"`
	Databases   []databaseDTO      `json:"databases"`
	Accounts    []accountDTO       `json:"accounts"`
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
		Status:                    view.Status,
		AutoDesignNewRequirements: view.AutoDesignNewRequirements,
		Bindings:                  bindings,
		CreatedBy:                 strconv.FormatInt(view.CreatedBy, 10),
		CreatedAt:                 formatTime(view.CreatedAt),
		UpdatedAt:                 formatTime(view.UpdatedAt),
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
	return bindingDTO{
		ID:          strconv.FormatInt(view.ID, 10),
		SUT:         toSUTDTO(view.SUT),
		Environment: toEnvironmentDTO(view.Environment),
		Repository:  repository,
		Pages:       pages,
		APIs:        apis,
		Databases:   databases,
		Accounts:    accounts,
	}
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

type createVersionRequest struct {
	Title    string         `json:"title"`
	Bindings []bindingInput `json:"bindings"`
}

type bindingInput struct {
	SUTID            string         `json:"sut_id"`
	EnvironmentID    string         `json:"environment_id"`
	CodeRepositoryID *string        `json:"code_repository_id"`
	PageIDs          []string       `json:"page_ids"`
	APIIDs           []string       `json:"api_ids"`
	DatabaseIDs      []string       `json:"database_ids"`
	Accounts         []accountInput `json:"accounts"`
}

type accountInput struct {
	AccountID string `json:"account_id"`
	Role      string `json:"role"`
}

type updateVersionRequest struct {
	Title                     *string `json:"title"`
	AutoDesignNewRequirements *bool   `json:"auto_design_new_requirements"`
}

func (r createVersionRequest) command() (versionapp.CreateCommand, error) {
	command := versionapp.CreateCommand{
		Title:    r.Title,
		Bindings: make([]versionapp.BindingInput, 0, len(r.Bindings)),
	}
	for index, binding := range r.Bindings {
		field := func(name string) string { return fmt.Sprintf("bindings[%d].%s", index, name) }

		sutID, err := validation.NonEmptyID(field("sut_id"), binding.SUTID)
		if err != nil {
			return versionapp.CreateCommand{}, err
		}
		environmentID, err := validation.NonEmptyID(field("environment_id"), binding.EnvironmentID)
		if err != nil {
			return versionapp.CreateCommand{}, err
		}
		var repositoryID *int64
		if binding.CodeRepositoryID != nil && *binding.CodeRepositoryID != "" {
			parsed, err := validation.NonEmptyID(field("code_repository_id"), *binding.CodeRepositoryID)
			if err != nil {
				return versionapp.CreateCommand{}, err
			}
			repositoryID = &parsed
		}
		pageIDs, err := parseIDs(binding.PageIDs, field("page_ids"))
		if err != nil {
			return versionapp.CreateCommand{}, err
		}
		apiIDs, err := parseIDs(binding.APIIDs, field("api_ids"))
		if err != nil {
			return versionapp.CreateCommand{}, err
		}
		databaseIDs, err := parseIDs(binding.DatabaseIDs, field("database_ids"))
		if err != nil {
			return versionapp.CreateCommand{}, err
		}
		accounts := make([]versionapp.AccountInput, 0, len(binding.Accounts))
		for accountIndex, account := range binding.Accounts {
			accountID, err := validation.NonEmptyID(
				fmt.Sprintf("%s[%d].account_id", field("accounts"), accountIndex), account.AccountID,
			)
			if err != nil {
				return versionapp.CreateCommand{}, err
			}
			accounts = append(accounts, versionapp.AccountInput{AccountID: accountID, Role: account.Role})
		}

		command.Bindings = append(command.Bindings, versionapp.BindingInput{
			SUTID:            sutID,
			EnvironmentID:    environmentID,
			CodeRepositoryID: repositoryID,
			PageIDs:          pageIDs,
			APIIDs:           apiIDs,
			DatabaseIDs:      databaseIDs,
			Accounts:         accounts,
		})
	}
	return command, nil
}

func parseIDs(raw []string, field string) ([]int64, error) {
	ids := make([]int64, 0, len(raw))
	for index, value := range raw {
		id, err := validation.NonEmptyID(fmt.Sprintf("%s[%d]", field, index), value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
