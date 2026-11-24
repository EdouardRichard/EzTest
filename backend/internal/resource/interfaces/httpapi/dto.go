package resourceapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// environmentDTO is one top-level environment (v0.2.4-B). SUTs is the nested SUT
// catalog; a list projection leaves it empty.
type environmentDTO struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	SUTs        []sutDTO `json:"suts"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type sutDTO struct {
	ID            string        `json:"id"`
	EnvironmentID string        `json:"environment_id"`
	Name          string        `json:"name"`
	Intro         string        `json:"intro"`
	BaseURL       string        `json:"base_url"`
	Family        *string       `json:"family"`
	Source        string        `json:"source"`
	Status        string        `json:"status"`
	Pages         []pageDTO     `json:"pages"`
	APIs          []apiDTO      `json:"apis"`
	Databases     []databaseDTO `json:"databases"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
}

// sutRegistrationDTO is one append-only SUT registration fact (v0.2.4-B R-B4),
// returned by GET /api/resources/suts/{id}/registrations.
type sutRegistrationDTO struct {
	ID               string   `json:"id"`
	SUTID            string   `json:"sut_id"`
	Source           string   `json:"source"`
	EvidenceRefs     []string `json:"evidence_refs"`
	ResolutionMethod string   `json:"resolution_method"`
	GateResult       string   `json:"gate_result"`
	CreatedAt        string   `json:"created_at"`
	CreatedBy        string   `json:"created_by"`
}

type pageDTO struct {
	ID            string `json:"id"`
	SUTID         string `json:"sut_id"`
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
	ID           string `json:"id"`
	SUTID        string `json:"sut_id"`
	Method       string `json:"method"`
	PathTemplate string `json:"path_template"`
	Name         string `json:"name"`
	AuthRole     string `json:"auth_role"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	SchemaRef    string `json:"schema_ref"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type databaseDTO struct {
	ID          string `json:"id"`
	SUTID       string `json:"sut_id"`
	Name        string `json:"name"`
	DSN         string `json:"dsn"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"password_set"`
	ReadOnly    bool   `json:"read_only"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// accountDTO is one row of the global test-account pool. SUTID is the optional
// affinity hint and is omitted when the account has none.
type accountDTO struct {
	ID          string   `json:"id"`
	SUTID       string   `json:"sut_id,omitempty"`
	Username    string   `json:"username"`
	Roles       []string `json:"roles"`
	Notes       string   `json:"notes"`
	Status      string   `json:"status"`
	PasswordSet bool     `json:"password_set"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
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

// sutRepositoryDTO is one API-17 SUT-level code-repository association.
// CodeBaselineCommit is omitted while the baseline is unresolved.
type sutRepositoryDTO struct {
	ID                 string            `json:"id"`
	SUTID              string            `json:"sut_id"`
	Repository         codeRepositoryDTO `json:"repository"`
	Role               string            `json:"role"`
	CodeBaselineCommit string            `json:"code_baseline_commit,omitempty"`
	Enabled            bool              `json:"enabled"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func toEnvironmentDTO(view resourceapp.EnvironmentView) environmentDTO {
	suts := make([]sutDTO, 0, len(view.SUTs))
	for _, sut := range view.SUTs {
		suts = append(suts, toSUTDTO(sut))
	}
	return environmentDTO{
		ID:          view.ID,
		Name:        view.Name,
		Description: view.Description,
		Status:      view.Status,
		SUTs:        suts,
		CreatedAt:   formatTime(view.CreatedAt),
		UpdatedAt:   formatTime(view.UpdatedAt),
	}
}

func toSUTDTO(view resourceapp.SUTView) sutDTO {
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
	return sutDTO{
		ID:            view.ID,
		EnvironmentID: view.EnvironmentID,
		Name:          view.Name,
		Intro:         view.Intro,
		BaseURL:       view.BaseURL,
		Family:        view.Family,
		Source:        view.Source,
		Status:        view.Status,
		Pages:         pages,
		APIs:          apis,
		Databases:     databases,
		CreatedAt:     formatTime(view.CreatedAt),
		UpdatedAt:     formatTime(view.UpdatedAt),
	}
}

func toSUTRegistrationDTO(view resourceapp.SUTRegistrationView) sutRegistrationDTO {
	evidence := view.EvidenceRefs
	if evidence == nil {
		evidence = []string{}
	}
	return sutRegistrationDTO{
		ID:               view.ID,
		SUTID:            view.SUTID,
		Source:           view.Source,
		EvidenceRefs:     evidence,
		ResolutionMethod: view.ResolutionMethod,
		GateResult:       view.GateResult,
		CreatedAt:        formatTime(view.CreatedAt),
		CreatedBy:        strconv.FormatInt(view.CreatedBy, 10),
	}
}

func toPageDTO(view resourceapp.PageView) pageDTO {
	return pageDTO{
		ID:            view.ID,
		SUTID:         view.SUTID,
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
		ID:           view.ID,
		SUTID:        view.SUTID,
		Method:       view.Method,
		PathTemplate: view.PathTemplate,
		Name:         view.Name,
		AuthRole:     view.AuthRole,
		Source:       view.Source,
		Status:       view.Status,
		SchemaRef:    view.SchemaRef,
		CreatedAt:    formatTime(view.CreatedAt),
		UpdatedAt:    formatTime(view.UpdatedAt),
	}
}

func toDatabaseDTO(view resourceapp.DatabaseView) databaseDTO {
	return databaseDTO{
		ID:          view.ID,
		SUTID:       view.SUTID,
		Name:        view.Name,
		DSN:         view.DSN,
		Username:    view.Username,
		PasswordSet: view.PasswordSet,
		ReadOnly:    view.ReadOnly,
		Status:      view.Status,
		CreatedAt:   formatTime(view.CreatedAt),
		UpdatedAt:   formatTime(view.UpdatedAt),
	}
}

func toAccountDTO(view resourceapp.AccountView) accountDTO {
	roles := view.Roles
	if roles == nil {
		roles = []string{}
	}
	return accountDTO{
		ID:          view.ID,
		SUTID:       view.SUTID,
		Username:    view.Username,
		Roles:       roles,
		Notes:       view.Notes,
		Status:      view.Status,
		PasswordSet: view.PasswordSet,
		CreatedAt:   formatTime(view.CreatedAt),
		UpdatedAt:   formatTime(view.UpdatedAt),
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

// sutRepositoryCreateRequest is the API-17 write payload.
type sutRepositoryCreateRequest struct {
	RepositoryID string `json:"repository_id"`
	Role         string `json:"role"`
}

func toSUTRepositoryDTO(view resourceapp.SUTRepositoryView) sutRepositoryDTO {
	return sutRepositoryDTO{
		ID:                 view.ID,
		SUTID:              view.SUTID,
		Repository:         toCodeRepositoryDTO(view.Repository),
		Role:               view.Role,
		CodeBaselineCommit: view.CodeBaselineCommit,
		Enabled:            view.Enabled,
		CreatedAt:          formatTime(view.CreatedAt),
		UpdatedAt:          formatTime(view.UpdatedAt),
	}
}

// ---------- request payloads ----------

type environmentCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type environmentPatchRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

type sutCreateRequest struct {
	Name    string  `json:"name"`
	Intro   string  `json:"intro"`
	BaseURL string  `json:"base_url"`
	Family  *string `json:"family"`
}

type sutPatchRequest struct {
	Name    *string `json:"name"`
	Intro   *string `json:"intro"`
	BaseURL *string `json:"base_url"`
	Family  *string `json:"family"`
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
	Name     string `json:"name"`
	DSN      string `json:"dsn"`
	Username string `json:"username"`
	Password string `json:"password"`
	ReadOnly *bool  `json:"read_only"`
	Status   string `json:"status"`
}

type databasePatchRequest struct {
	Name     *string `json:"name"`
	DSN      *string `json:"dsn"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	ReadOnly *bool   `json:"read_only"`
	Status   *string `json:"status"`
}

// accountCreateRequest creates a row of the global pool. SUTID is the optional
// affinity hint; an empty value means "no affinity".
type accountCreateRequest struct {
	SUTID    string   `json:"sut_id"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
	Notes    string   `json:"notes"`
	Status   string   `json:"status"`
}

type accountPatchRequest struct {
	SUTID    *string   `json:"sut_id"`
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
