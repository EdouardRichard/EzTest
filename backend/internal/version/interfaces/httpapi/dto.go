package versionapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// ---------- response DTOs ----------

// versionDTO is the version detail projection. v0.2.4-B: the version binds one
// environment directly, so the derived bindings array is replaced by
// environment_id plus the resolved environment catalog in binding.
type versionDTO struct {
	ID                        string               `json:"id"`
	Title                     string               `json:"title"`
	Description               string               `json:"description"`
	Goal                      string               `json:"goal"`
	Status                    string               `json:"status"`
	AutoDesignNewRequirements bool                 `json:"auto_design_new_requirements"`
	EnvironmentID             string               `json:"environment_id"`
	Binding                   bindingDTO           `json:"binding"`
	RequirementCounts         requirementCountsDTO `json:"requirement_counts"`
	ConversationCount         int                  `json:"conversation_count"`
	PendingDesignCount        int                  `json:"pending_design_count"`
	CaseCounts                caseCountsDTO        `json:"case_counts"`
	ClosedAt                  *string              `json:"closed_at"`
	CloseReason               *string              `json:"close_reason"`
	ClosureKind               string               `json:"closure_kind"`
	Unverified                bool                 `json:"unverified"`
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

// bindingDTO is the resolved environment catalog of one version: the bound
// environment with its SUT catalog, the flattened asset unions, the version
// account subset, the environment code pool and the pinned baseline commit.
// code_baseline_commit is omitted while the baseline is unresolved.
type bindingDTO struct {
	Environment        environmentDTO         `json:"environment"`
	Pages              []pageDTO              `json:"pages"`
	APIs               []apiDTO               `json:"apis"`
	Databases          []databaseDTO          `json:"databases"`
	Accounts           []accountDTO           `json:"accounts"`
	Repositories       []bindingRepositoryDTO `json:"repositories"`
	CodeBaselineCommit *string                `json:"code_baseline_commit,omitempty"`
}

// bindingRepositoryDTO is one repository of the environment code pool (GD-03).
// SUTID names the SUT whose association supplied the row, which is also the key
// the resolved code baseline is stored under.
type bindingRepositoryDTO struct {
	Repository         codeRepositoryDTO `json:"repository"`
	SUTID              string            `json:"sut_id"`
	Role               string            `json:"role"`
	CodeBaselineCommit *string           `json:"code_baseline_commit"`
}

type versionSummaryDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	SUTCount  int    `json:"sut_count"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// environmentDTO is one top-level environment (v0.2.4-B): it owns SUTs and no
// longer carries base_url or assets.
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
	Pages         []pageDTO     `json:"pages"`
	APIs          []apiDTO      `json:"apis"`
	Databases     []databaseDTO `json:"databases"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
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

func toVersionDTO(view versionapp.VersionView) versionDTO {
	return versionDTO{
		ID:                        strconv.FormatInt(view.ID, 10),
		Title:                     view.Title,
		Description:               view.Description,
		Goal:                      view.Goal,
		Status:                    view.Status,
		AutoDesignNewRequirements: view.AutoDesignNewRequirements,
		EnvironmentID:             strconv.FormatInt(view.EnvironmentID, 10),
		Binding:                   toBindingDTO(view.Binding),
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
		ClosureKind: view.ClosureKind,
		Unverified:  view.Unverified,
		CreatedBy:   strconv.FormatInt(view.CreatedBy, 10),
		CreatedAt:   formatTime(view.CreatedAt),
		UpdatedAt:   formatTime(view.UpdatedAt),
	}
}

func toBindingDTO(catalog versionapp.EnvironmentSUTCatalog) bindingDTO {
	return bindingDTO{
		Environment:        toEnvironmentDTO(catalog.Environment),
		Pages:              toPageDTOs(catalog.Pages),
		APIs:               toAPIDTOs(catalog.APIs),
		Databases:          toDatabaseDTOs(catalog.Databases),
		Accounts:           toAccountDTOs(catalog.Accounts),
		Repositories:       toBindingRepositoryDTOs(catalog.Repositories),
		CodeBaselineCommit: formatOptionalString(catalog.CodeBaselineCommit),
	}
}

func toBindingRepositoryDTOs(rows []versionapp.BindingRepository) []bindingRepositoryDTO {
	out := make([]bindingRepositoryDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, bindingRepositoryDTO{
			Repository:         toCodeRepositoryDTO(row.Repository),
			SUTID:              strconv.FormatInt(row.SUTID, 10),
			Role:               row.Role,
			CodeBaselineCommit: formatOptionalString(row.CodeBaselineCommit),
		})
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
	return sutDTO{
		ID:            view.ID,
		EnvironmentID: view.EnvironmentID,
		Name:          view.Name,
		Intro:         view.Intro,
		BaseURL:       view.BaseURL,
		Family:        view.Family,
		Pages:         toPageDTOs(view.Pages),
		APIs:          toAPIDTOs(view.APIs),
		Databases:     toDatabaseDTOs(view.Databases),
		CreatedAt:     formatTime(view.CreatedAt),
		UpdatedAt:     formatTime(view.UpdatedAt),
	}
}

func toPageDTOs(views []resourceapp.PageView) []pageDTO {
	out := make([]pageDTO, 0, len(views))
	for _, view := range views {
		out = append(out, toPageDTO(view))
	}
	return out
}

func toAPIDTOs(views []resourceapp.APIView) []apiDTO {
	out := make([]apiDTO, 0, len(views))
	for _, view := range views {
		out = append(out, toAPIDTO(view))
	}
	return out
}

func toDatabaseDTOs(views []resourceapp.DatabaseView) []databaseDTO {
	out := make([]databaseDTO, 0, len(views))
	for _, view := range views {
		out = append(out, toDatabaseDTO(view))
	}
	return out
}

func toAccountDTOs(views []resourceapp.AccountView) []accountDTO {
	out := make([]accountDTO, 0, len(views))
	for _, view := range views {
		out = append(out, toAccountDTO(view))
	}
	return out
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

// ---------- request payloads ----------

// createVersionRequest is the environment-only version payload (API-15). The
// shape is unchanged by v0.2.4-B: the version binds the environment directly and
// nothing is derived into a per-version SUT row any more.
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
