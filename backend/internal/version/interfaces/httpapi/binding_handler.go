package versionapi

// The version binding read/update routes (API-16).
//
//	GET /api/versions/{id}/binding
//	PUT /api/versions/{id}/binding
//
// The handler only parses the path/body, validates decimal IDs and renders the
// overview: environment existence, the derived SUT catalog and code pool, the
// account threshold and the stage gate all belong to *versionapp.Service
// (GetBinding/UpdateBinding). v0.2.4-B removed the repository selection and the
// scope_exclude document together with the version_sut chain, so the only
// editable fields left are the bound environment and the version account subset.

import (
	"context"
	"net/http"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
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

// RegisterBindingRoutes mounts the binding read/update routes on mux. Both PUT
// and PATCH are mounted because the design spec names the write as PATCH while
// the original task description named PUT; both share the same partial-update
// semantics. A nil mux or authentication middleware is a wiring bug (an
// authenticated route must never be silently unmounted), so the helper panics; a
// nil handler keeps the route table of a deployment without the binding context.
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
// omitted field is untouched, while an explicit empty account list clears the
// selection.
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

// bindingUpdateRequest is the API-16 partial binding update. v0.2.4-B: the code
// pool is derived from the bound environment (GD-03) and every approved asset of
// its SUTs is in scope, so repository_ids and scope_exclude are no longer part
// of the contract.
type bindingUpdateRequest struct {
	EnvironmentID *string   `json:"environment_id"`
	AccountIDs    *[]string `json:"account_ids"`
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
	if r.AccountIDs != nil {
		ids, err := parseDecimalIDList(*r.AccountIDs, "account_ids")
		if err != nil {
			return versionapp.BindingPatch{}, err
		}
		patch.AccountIDs = &ids
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

// bindingResourceCountsDTO is the approved/proposed projection of one asset kind
// of the bound environment. Exclusions disappeared with the version_sut chain,
// so approved is also the included count.
type bindingResourceCountsDTO struct {
	Approved int `json:"approved"`
	Proposed int `json:"proposed"`
}

// bindingSnapshotDTO is the R-C4-1 snapshot projection. scope_version is the
// deterministic reference of the current effective scope; frozen_wave_id stays
// null because the frozen wave snapshot is execution-owned (R-C5/API-19,
// run_wave.scope_snapshot) and is not read by the version context.
type bindingSnapshotDTO struct {
	ScopeVersion string  `json:"scope_version"`
	FrozenWaveID *string `json:"frozen_wave_id"`
}

type bindingOverviewDTO struct {
	VersionID          string                 `json:"version_id"`
	EnvironmentID      string                 `json:"environment_id"`
	Stage              string                 `json:"stage"`
	Editable           bool                   `json:"editable"`
	Environment        environmentDTO         `json:"environment"`
	Counts             bindingCountsDTO       `json:"counts"`
	Pages              []pageDTO              `json:"pages"`
	APIs               []apiDTO               `json:"apis"`
	Databases          []databaseDTO          `json:"databases"`
	Accounts           []accountDTO           `json:"accounts"`
	Repositories       []bindingRepositoryDTO `json:"repositories"`
	CodeBaselineCommit *string                `json:"code_baseline_commit"`
	Snapshot           bindingSnapshotDTO     `json:"snapshot"`
}

func toBindingOverviewDTO(overview versionapp.BindingOverview) bindingOverviewDTO {
	var frozenWaveID *string
	if overview.Snapshot.FrozenWaveID != nil {
		formatted := strconv.FormatInt(*overview.Snapshot.FrozenWaveID, 10)
		frozenWaveID = &formatted
	}
	return bindingOverviewDTO{
		VersionID:     strconv.FormatInt(overview.VersionID, 10),
		EnvironmentID: strconv.FormatInt(overview.EnvironmentID, 10),
		Stage:         string(overview.Stage),
		Editable:      overview.Editable,
		Environment:   toEnvironmentDTO(overview.Environment),
		Counts: bindingCountsDTO{
			Pages:     toBindingResourceCountsDTO(overview.Counts.Pages),
			APIs:      toBindingResourceCountsDTO(overview.Counts.APIs),
			Databases: toBindingResourceCountsDTO(overview.Counts.Databases),
		},
		Pages:              toPageDTOs(overview.Pages),
		APIs:               toAPIDTOs(overview.APIs),
		Databases:          toDatabaseDTOs(overview.Databases),
		Accounts:           toAccountDTOs(overview.Accounts),
		Repositories:       toBindingRepositoryDTOs(overview.Repositories),
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
	}
}
