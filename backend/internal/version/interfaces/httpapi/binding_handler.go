package versionapi

// Task 29 (v0.22 C-1..C-4/API-16): the version binding read/update routes.
//
//	GET /api/versions/{id}/binding
//	PUT /api/versions/{id}/binding
//
// The handler only parses the path/body, validates decimal IDs and renders the
// overview: resource ownership, environment/SUT derivation, repository
// overrides, account threshold, scope_exclude persistence and the stage gate all
// belong to *versionapp.Service (GetBinding/UpdateBinding). Route registration
// and the OpenAPI schema are wired by the server task (Task 37).

import (
	"context"
	"net/http"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// BindingService is the binding surface the handler needs.
// *versionapp.Service implements it.
type BindingService interface {
	GetBinding(ctx context.Context, versionID int64) (versionapp.BindingOverview, error)
	UpdateBinding(ctx context.Context, versionID int64, patch versionapp.BindingPatch, actorID int64) (versionapp.BindingOverview, error)
}

var _ BindingService = (*versionapp.Service)(nil)

// BindingHandler serves /api/versions/{id}/binding. The server mounts it behind
// the shared bearer-authentication middleware; the update route enforces the
// authenticated actor like the sibling mutating handlers.
type BindingHandler struct {
	svc BindingService
}

// NewBindingHandler builds the binding handler. A nil service answers 503.
func NewBindingHandler(svc BindingService) *BindingHandler {
	return &BindingHandler{svc: svc}
}

// RegisterBindingRoutes mounts the binding read/update routes on mux for Task
// 37. Both PUT and PATCH are mounted because the design spec names the write as
// PATCH while the W0 task description names PUT; both share the same
// partial-update semantics. A nil mux or authentication middleware is a wiring
// bug (an authenticated route must never be silently unmounted), so the helper
// panics; a nil handler keeps the route table of a deployment without the
// binding context.
func RegisterBindingRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *BindingHandler) {
	if mux == nil {
		panic("versionapi: RegisterBindingRoutes requires a non-nil mux")
	}
	if auth == nil {
		panic("versionapi: RegisterBindingRoutes requires an authentication middleware")
	}
	if h == nil {
		return
	}
	mux.Handle("GET /api/versions/{id}/binding", auth(http.HandlerFunc(h.Get)))
	mux.Handle("PUT /api/versions/{id}/binding", auth(http.HandlerFunc(h.Update)))
	mux.Handle("PATCH /api/versions/{id}/binding", auth(http.HandlerFunc(h.Update)))
}

// Get GET /api/versions/{id}/binding. A missing version is a typed 404.
func (h *BindingHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, err := bindingPathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.svc == nil {
		httpx.Error(w, r, platform.Unavailable("version binding is not configured"))
		return
	}
	overview, err := h.svc.GetBinding(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBindingOverviewDTO(overview))
}

// Update PUT|PATCH /api/versions/{id}/binding. Every field is optional: an
// omitted field is untouched, while an explicit empty repository/account list
// clears the selection.
func (h *BindingHandler) Update(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := bindingPathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request bindingUpdateRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	patch, err := request.patch()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.svc == nil {
		httpx.Error(w, r, platform.Unavailable("version binding is not configured"))
		return
	}
	overview, err := h.svc.UpdateBinding(r.Context(), versionID, patch, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBindingOverviewDTO(overview))
}

func bindingPathID(r *http.Request) (int64, error) {
	return validation.NonEmptyID("id", r.PathValue("id"))
}

// ---------- request DTOs ----------

type bindingUpdateRequest struct {
	EnvironmentID *string                `json:"environment_id"`
	RepositoryIDs *[]string              `json:"repository_ids"`
	AccountIDs    *[]string              `json:"account_ids"`
	ScopeExclude  *scopeExcludeInputJSON `json:"scope_exclude"`
}

type scopeExcludeInputJSON struct {
	Pages     []string `json:"pages"`
	APIs      []string `json:"apis"`
	Databases []string `json:"databases"`
}

func (r bindingUpdateRequest) patch() (versionapp.BindingPatch, error) {
	patch := versionapp.BindingPatch{}
	if r.EnvironmentID != nil {
		if *r.EnvironmentID == "" {
			return versionapp.BindingPatch{}, platform.Invalid("environment_id", "must be a positive decimal ID")
		}
		environmentID, err := validation.NonEmptyID("environment_id", *r.EnvironmentID)
		if err != nil {
			return versionapp.BindingPatch{}, err
		}
		patch.EnvironmentID = &environmentID
	}
	if r.RepositoryIDs != nil {
		ids, err := parseDecimalIDList(*r.RepositoryIDs, "repository_ids")
		if err != nil {
			return versionapp.BindingPatch{}, err
		}
		patch.RepositoryIDs = &ids
	}
	if r.AccountIDs != nil {
		ids, err := parseDecimalIDList(*r.AccountIDs, "account_ids")
		if err != nil {
			return versionapp.BindingPatch{}, err
		}
		patch.AccountIDs = &ids
	}
	if r.ScopeExclude != nil {
		pages, err := parseDecimalIDList(r.ScopeExclude.Pages, "scope_exclude.pages")
		if err != nil {
			return versionapp.BindingPatch{}, err
		}
		apis, err := parseDecimalIDList(r.ScopeExclude.APIs, "scope_exclude.apis")
		if err != nil {
			return versionapp.BindingPatch{}, err
		}
		databases, err := parseDecimalIDList(r.ScopeExclude.Databases, "scope_exclude.databases")
		if err != nil {
			return versionapp.BindingPatch{}, err
		}
		scope := version.NewScopeExclude(pages, apis, databases)
		patch.ScopeExclude = &scope
	}
	return patch, nil
}

func parseDecimalIDList(raw []string, field string) ([]int64, error) {
	ids := make([]int64, 0, len(raw))
	for index, value := range raw {
		id, err := validation.NonEmptyID(field+"["+strconv.Itoa(index)+"]", value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ---------- response DTOs ----------

type bindingCountsDTO struct {
	Pages     bindingResourceCountsDTO `json:"pages"`
	APIs      bindingResourceCountsDTO `json:"apis"`
	Databases bindingResourceCountsDTO `json:"databases"`
}

type bindingResourceCountsDTO struct {
	Approved int `json:"approved"`
	Proposed int `json:"proposed"`
	Excluded int `json:"excluded"`
	Included int `json:"included"`
}

type bindingRepoRowDTO struct {
	Repository codeRepositoryDTO `json:"repository"`
	Role       string            `json:"role"`
	Included   bool              `json:"included"`
	Source     string            `json:"source"`
}

// bindingSnapshotDTO is the R-C4-1 snapshot projection. scope_version is the
// deterministic reference of the current effective scope; frozen_wave_id stays
// null in W0 because the frozen wave snapshot is execution-owned (R-C5/API-19,
// run_wave.scope_snapshot) and is not read by the version context.
type bindingSnapshotDTO struct {
	ScopeVersion string  `json:"scope_version"`
	FrozenWaveID *string `json:"frozen_wave_id"`
}

type bindingOverviewDTO struct {
	VersionID          string              `json:"version_id"`
	Stage              string              `json:"stage"`
	Editable           bool                `json:"editable"`
	SUT                sutDTO              `json:"sut"`
	Environment        environmentDTO      `json:"environment"`
	ScopeExclude       scopeExcludeDTO     `json:"scope_exclude"`
	Counts             bindingCountsDTO    `json:"counts"`
	Pages              []pageDTO           `json:"pages"`
	APIs               []apiDTO            `json:"apis"`
	Databases          []databaseDTO       `json:"databases"`
	Accounts           []accountDTO        `json:"accounts"`
	Repositories       []bindingRepoRowDTO `json:"repositories"`
	CodeBaselineCommit *string             `json:"code_baseline_commit"`
	Snapshot           bindingSnapshotDTO  `json:"snapshot"`
}

func toBindingOverviewDTO(overview versionapp.BindingOverview) bindingOverviewDTO {
	pages := make([]pageDTO, 0, len(overview.Pages))
	for _, page := range overview.Pages {
		pages = append(pages, toPageDTO(page))
	}
	apis := make([]apiDTO, 0, len(overview.APIs))
	for _, api := range overview.APIs {
		apis = append(apis, toAPIDTO(api))
	}
	databases := make([]databaseDTO, 0, len(overview.Databases))
	for _, database := range overview.Databases {
		databases = append(databases, toDatabaseDTO(database))
	}
	accounts := make([]accountDTO, 0, len(overview.Accounts))
	for _, account := range overview.Accounts {
		accounts = append(accounts, toAccountDTO(account))
	}
	repositories := make([]bindingRepoRowDTO, 0, len(overview.Repositories))
	for _, repository := range overview.Repositories {
		repositories = append(repositories, bindingRepoRowDTO{
			Repository: toCodeRepositoryDTO(repository.Repository),
			Role:       repository.Role,
			Included:   repository.Included,
			Source:     repository.Source,
		})
	}
	var frozenWaveID *string
	if overview.Snapshot.FrozenWaveID != nil {
		formatted := strconv.FormatInt(*overview.Snapshot.FrozenWaveID, 10)
		frozenWaveID = &formatted
	}
	return bindingOverviewDTO{
		VersionID:    strconv.FormatInt(overview.VersionID, 10),
		Stage:        string(overview.Stage),
		Editable:     overview.Editable,
		SUT:          toSUTDTO(overview.SUT),
		Environment:  toEnvironmentDTO(overview.Environment),
		ScopeExclude: toScopeExcludeDTO(overview.ScopeExclude),
		Counts: bindingCountsDTO{
			Pages:     toBindingResourceCountsDTO(overview.Counts.Pages),
			APIs:      toBindingResourceCountsDTO(overview.Counts.APIs),
			Databases: toBindingResourceCountsDTO(overview.Counts.Databases),
		},
		Pages:              pages,
		APIs:               apis,
		Databases:          databases,
		Accounts:           accounts,
		Repositories:       repositories,
		CodeBaselineCommit: formatOptionalString(overview.CodeBaselineCommit),
		Snapshot: bindingSnapshotDTO{
			ScopeVersion: overview.Snapshot.ScopeVersion,
			FrozenWaveID: frozenWaveID,
		},
	}
}

func toBindingResourceCountsDTO(counts versionapp.BindingResourceCounts) bindingResourceCountsDTO {
	return bindingResourceCountsDTO{
		Approved: counts.Approved,
		Proposed: counts.Proposed,
		Excluded: counts.Excluded,
		Included: counts.Included,
	}
}
