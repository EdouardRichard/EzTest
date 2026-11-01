// Package resourceapi exposes the resource-catalog HTTP API.
package resourceapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

const (
	bodyLimit       = int64(1 << 20)
	importBodyLimit = int64(4 << 20)
)

// Service is the application surface the resource handlers need.
type Service interface {
	Catalog(ctx context.Context) ([]resourceapp.SUTView, error)

	CreateSUT(ctx context.Context, input resourceapp.SUTInput, actorID int64) (resourceapp.SUTView, error)
	UpdateSUT(ctx context.Context, id int64, patch resourceapp.SUTPatch, actorID int64) (resourceapp.SUTView, error)
	DeleteSUT(ctx context.Context, id int64, actorID int64) error
	ListSUTs(ctx context.Context, limit int, beforeID *int64) ([]resourceapp.SUTView, bool, error)
	GetSUT(ctx context.Context, id int64) (resourceapp.SUTView, error)

	CreateEnvironment(ctx context.Context, sutID int64, input resourceapp.EnvironmentInput, actorID int64) (resourceapp.EnvironmentView, error)
	UpdateEnvironment(ctx context.Context, id int64, patch resourceapp.EnvironmentPatch, actorID int64) (resourceapp.EnvironmentView, error)
	DeleteEnvironment(ctx context.Context, id int64, actorID int64) error
	ListEnvironments(ctx context.Context, sutID int64) ([]resourceapp.EnvironmentView, error)

	CreatePage(ctx context.Context, environmentID int64, input resourceapp.PageInput, actorID int64) (resourceapp.PageView, error)
	UpdatePage(ctx context.Context, id int64, patch resourceapp.PagePatch, actorID int64) (resourceapp.PageView, error)
	DeletePage(ctx context.Context, id int64, actorID int64) error
	ListPages(ctx context.Context, environmentID int64) ([]resourceapp.PageView, error)
	ImportPages(ctx context.Context, environmentID int64, items []resourceapp.PageInput, actorID int64) (resourceapp.ImportPagesResult, error)

	CreateAPI(ctx context.Context, environmentID int64, input resourceapp.APIInput, actorID int64) (resourceapp.APIView, error)
	UpdateAPI(ctx context.Context, id int64, patch resourceapp.APIPatch, actorID int64) (resourceapp.APIView, error)
	DeleteAPI(ctx context.Context, id int64, actorID int64) error
	ListAPIs(ctx context.Context, environmentID int64) ([]resourceapp.APIView, error)
	ImportAPIs(ctx context.Context, environmentID int64, items []resourceapp.APIInput, actorID int64) (resourceapp.ImportAPIsResult, error)

	CreateDatabase(ctx context.Context, sutID int64, input resourceapp.DatabaseInput, actorID int64) (resourceapp.DatabaseView, error)
	UpdateDatabase(ctx context.Context, id int64, patch resourceapp.DatabasePatch, actorID int64) (resourceapp.DatabaseView, error)
	DeleteDatabase(ctx context.Context, id int64, actorID int64) error
	ListDatabases(ctx context.Context, sutID int64) ([]resourceapp.DatabaseView, error)

	CreateAccount(ctx context.Context, sutID int64, input resourceapp.AccountInput, actorID int64) (resourceapp.AccountView, error)
	UpdateAccount(ctx context.Context, id int64, patch resourceapp.AccountPatch, actorID int64) (resourceapp.AccountView, error)
	DeleteAccount(ctx context.Context, id int64, actorID int64) error
	ListAccounts(ctx context.Context, sutID int64) ([]resourceapp.AccountView, error)

	CreateRepository(ctx context.Context, input resourceapp.RepositoryInput, actorID int64) (resourceapp.CodeRepositoryView, error)
	UpdateRepository(ctx context.Context, id int64, patch resourceapp.RepositoryPatch, actorID int64) (resourceapp.CodeRepositoryView, error)
	DeleteRepository(ctx context.Context, id int64, actorID int64) error
	ListRepositories(ctx context.Context, limit int, beforeID *int64) ([]resourceapp.CodeRepositoryView, bool, error)
	GetRepository(ctx context.Context, id int64) (resourceapp.CodeRepositoryView, error)

	ConfirmPage(ctx context.Context, pageID, actorID int64) (resourceapp.PageView, error)
	RejectPage(ctx context.Context, pageID, actorID int64) (resourceapp.PageView, error)
	ConfirmAPI(ctx context.Context, apiID, actorID int64) (resourceapp.APIView, error)
	RejectAPI(ctx context.Context, apiID, actorID int64) (resourceapp.APIView, error)
}

// Handler serves /api/resources/*.
type Handler struct {
	svc Service
}

// New builds the resource handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// ---------- helpers ----------

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := decoder.Decode(dst); err != nil {
		return platform.Invalid("body", "request body must be valid JSON")
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	return validation.NonEmptyID("id", r.PathValue("id"))
}

func bodyID(field, raw string) (int64, error) {
	return validation.NonEmptyID(field, raw)
}

func actorID(r *http.Request) (int64, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		return 0, platform.Unauthenticated()
	}
	return actor.ID, nil
}

func writeList[T any](w http.ResponseWriter, items []T, hasMore bool, lastID func(T) string) {
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = lastID(items[len(items)-1])
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// ---------- catalog ----------

// Catalog GET /api/resources/catalog.
func (h *Handler) Catalog(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.Catalog(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	repositories, _, err := h.svc.ListRepositories(r.Context(), 1000, nil)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	suts := make([]sutDTO, 0, len(views))
	for _, view := range views {
		suts = append(suts, toSUTDTO(view))
	}
	repos := make([]codeRepositoryDTO, 0, len(repositories))
	for _, view := range repositories {
		repos = append(repos, toCodeRepositoryDTO(view))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"suts":         suts,
		"repositories": repos,
	})
}

// ---------- SUT ----------

// CreateSUT POST /api/resources/suts.
func (h *Handler) CreateSUT(w http.ResponseWriter, r *http.Request) {
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body sutCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.CreateSUT(r.Context(), resourceapp.SUTInput{
		Name: body.Name, Intro: body.Intro, Family: body.Family,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSUTDTO(view))
}

// ListSUTs GET /api/resources/suts.
func (h *Handler) ListSUTs(w http.ResponseWriter, r *http.Request) {
	limit := pagination.Limit(r.URL.Query().Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	cursor, err := pagination.Cursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.Error(w, r, platform.Invalid("cursor", "cursor must be a decimal ID"))
		return
	}
	var beforeID *int64
	if cursor > 0 {
		beforeID = &cursor
	}
	views, hasMore, err := h.svc.ListSUTs(r.Context(), limit, beforeID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]sutDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toSUTDTO(view))
	}
	writeList(w, items, hasMore, func(item sutDTO) string { return item.ID })
}

// GetSUT GET /api/resources/suts/{id}.
func (h *Handler) GetSUT(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.GetSUT(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSUTDTO(view))
}

// UpdateSUT PATCH /api/resources/suts/{id}.
func (h *Handler) UpdateSUT(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body sutPatchRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.UpdateSUT(r.Context(), id, resourceapp.SUTPatch{
		Name: body.Name, Intro: body.Intro, Family: body.Family,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSUTDTO(view))
}

// DeleteSUT DELETE /api/resources/suts/{id}.
func (h *Handler) DeleteSUT(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteSUT(r.Context(), id, actor); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- environments ----------

// CreateEnvironment POST /api/resources/suts/{id}/environments.
func (h *Handler) CreateEnvironment(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body environmentCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.CreateEnvironment(r.Context(), sutID, resourceapp.EnvironmentInput{
		Name: body.Name, BaseURL: body.BaseURL, IsDefault: body.IsDefault, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toEnvironmentDTO(view))
}

// ListEnvironments GET /api/resources/suts/{id}/environments.
func (h *Handler) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListEnvironments(r.Context(), sutID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]environmentDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toEnvironmentDTO(view))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// UpdateEnvironment PATCH /api/resources/environments/{id}.
func (h *Handler) UpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body environmentPatchRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.UpdateEnvironment(r.Context(), id, resourceapp.EnvironmentPatch{
		Name: body.Name, BaseURL: body.BaseURL, IsDefault: body.IsDefault, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toEnvironmentDTO(view))
}

// DeleteEnvironment DELETE /api/resources/environments/{id}.
func (h *Handler) DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteEnvironment(r.Context(), id, actor); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- pages ----------

// CreatePage POST /api/resources/environments/{id}/pages.
func (h *Handler) CreatePage(w http.ResponseWriter, r *http.Request) {
	environmentID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body pageCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.CreatePage(r.Context(), environmentID, toPageInput(body), actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toPageDTO(view))
}

// ListPages GET /api/resources/environments/{id}/pages.
func (h *Handler) ListPages(w http.ResponseWriter, r *http.Request) {
	environmentID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListPages(r.Context(), environmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]pageDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toPageDTO(view))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// ImportPages POST /api/resources/environments/{id}/pages/import.
func (h *Handler) ImportPages(w http.ResponseWriter, r *http.Request) {
	environmentID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body importPagesRequest
	if err := decodeJSON(w, r, &body, importBodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]resourceapp.PageInput, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, toPageInput(item))
	}
	result, err := h.svc.ImportPages(r.Context(), environmentID, items, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]pageDTO, 0, len(result.Items))
	for _, view := range result.Items {
		views = append(views, toPageDTO(view))
	}
	httpx.JSON(w, http.StatusOK, importPagesDTO{Created: result.Created, Skipped: result.Skipped, Items: views})
}

// UpdatePage PATCH /api/resources/pages/{id}.
func (h *Handler) UpdatePage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body pagePatchRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.UpdatePage(r.Context(), id, resourceapp.PagePatch{
		Name: body.Name, URL: body.URL, RouteTemplate: body.RouteTemplate,
		PageType: body.PageType, AuthRole: body.AuthRole, Module: body.Module,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPageDTO(view))
}

// DeletePage DELETE /api/resources/pages/{id}.
func (h *Handler) DeletePage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeletePage(r.Context(), id, actor); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toPageInput(body pageCreateRequest) resourceapp.PageInput {
	return resourceapp.PageInput{
		Name: body.Name, URL: body.URL, RouteTemplate: body.RouteTemplate,
		PageType: body.PageType, AuthRole: body.AuthRole, Module: body.Module,
	}
}

// ---------- APIs ----------

// CreateAPI POST /api/resources/environments/{id}/apis.
func (h *Handler) CreateAPI(w http.ResponseWriter, r *http.Request) {
	environmentID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body apiCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.CreateAPI(r.Context(), environmentID, toAPIInput(body), actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toAPIDTO(view))
}

// ListAPIs GET /api/resources/environments/{id}/apis.
func (h *Handler) ListAPIs(w http.ResponseWriter, r *http.Request) {
	environmentID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListAPIs(r.Context(), environmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]apiDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toAPIDTO(view))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// ImportAPIs POST /api/resources/environments/{id}/apis/import.
func (h *Handler) ImportAPIs(w http.ResponseWriter, r *http.Request) {
	environmentID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body importAPIsRequest
	if err := decodeJSON(w, r, &body, importBodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]resourceapp.APIInput, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, toAPIInput(item))
	}
	result, err := h.svc.ImportAPIs(r.Context(), environmentID, items, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]apiDTO, 0, len(result.Items))
	for _, view := range result.Items {
		views = append(views, toAPIDTO(view))
	}
	httpx.JSON(w, http.StatusOK, importAPIsDTO{Created: result.Created, Skipped: result.Skipped, Items: views})
}

// UpdateAPI PATCH /api/resources/apis/{id}.
func (h *Handler) UpdateAPI(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body apiPatchRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.UpdateAPI(r.Context(), id, resourceapp.APIPatch{
		Method: body.Method, PathTemplate: body.PathTemplate, Name: body.Name,
		AuthRole: body.AuthRole, SchemaRef: body.SchemaRef,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIDTO(view))
}

// DeleteAPI DELETE /api/resources/apis/{id}.
func (h *Handler) DeleteAPI(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteAPI(r.Context(), id, actor); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toAPIInput(body apiCreateRequest) resourceapp.APIInput {
	return resourceapp.APIInput{
		Method: body.Method, PathTemplate: body.PathTemplate, Name: body.Name,
		AuthRole: body.AuthRole, SchemaRef: body.SchemaRef,
	}
}

// ---------- proposals: confirm / reject ----------

// proposalPathID parses the contract path parameter ({pageId}/{apiId}), falling
// back to {id} so direct handler calls keep working.
func proposalPathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	if raw == "" {
		raw = r.PathValue("id")
	}
	return validation.NonEmptyID("id", raw)
}

// requireAdmin is the defense-in-depth guard: the server also mounts every
// confirm/reject route behind the admin middleware.
func requireAdmin(w http.ResponseWriter, r *http.Request) (int64, bool) {
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return 0, false
	}
	current, ok := platform.ActorFrom(r.Context())
	if !ok || !current.IsAdmin() {
		httpx.Error(w, r, platform.Forbidden())
		return 0, false
	}
	return actor, true
}

// ConfirmPage POST /api/resources/pages/{pageId}/confirm.
func (h *Handler) ConfirmPage(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := proposalPathID(r, "pageId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.ConfirmPage(r.Context(), id, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPageDTO(view))
}

// RejectPage POST /api/resources/pages/{pageId}/reject.
func (h *Handler) RejectPage(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := proposalPathID(r, "pageId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.RejectPage(r.Context(), id, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPageDTO(view))
}

// ConfirmAPI POST /api/resources/apis/{apiId}/confirm.
func (h *Handler) ConfirmAPI(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := proposalPathID(r, "apiId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.ConfirmAPI(r.Context(), id, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIDTO(view))
}

// RejectAPI POST /api/resources/apis/{apiId}/reject.
func (h *Handler) RejectAPI(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := proposalPathID(r, "apiId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.RejectAPI(r.Context(), id, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIDTO(view))
}

// ---------- target databases ----------

// CreateDatabase POST /api/resources/suts/{id}/databases.
func (h *Handler) CreateDatabase(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body databaseCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	environmentID, err := bodyID("environment_id", body.EnvironmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.CreateDatabase(r.Context(), sutID, resourceapp.DatabaseInput{
		EnvironmentID: environmentID, Name: body.Name, DSN: body.DSN,
		Username: body.Username, Password: body.Password,
		ReadOnly: body.ReadOnly, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toDatabaseDTO(view))
}

// ListDatabases GET /api/resources/suts/{id}/databases.
func (h *Handler) ListDatabases(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListDatabases(r.Context(), sutID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]databaseDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toDatabaseDTO(view))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// UpdateDatabase PATCH /api/resources/databases/{id}.
func (h *Handler) UpdateDatabase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body databasePatchRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.UpdateDatabase(r.Context(), id, resourceapp.DatabasePatch{
		Name: body.Name, DSN: body.DSN, Username: body.Username, Password: body.Password,
		ReadOnly: body.ReadOnly, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDatabaseDTO(view))
}

// DeleteDatabase DELETE /api/resources/databases/{id}.
func (h *Handler) DeleteDatabase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteDatabase(r.Context(), id, actor); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- test accounts ----------

// CreateAccount POST /api/resources/suts/{id}/accounts.
func (h *Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body accountCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	environmentID, err := bodyID("environment_id", body.EnvironmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.CreateAccount(r.Context(), sutID, resourceapp.AccountInput{
		EnvironmentID: environmentID, Username: body.Username, Password: body.Password,
		Roles: body.Roles, Notes: body.Notes, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toAccountDTO(view))
}

// ListAccounts GET /api/resources/suts/{id}/accounts.
func (h *Handler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListAccounts(r.Context(), sutID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]accountDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toAccountDTO(view))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// UpdateAccount PATCH /api/resources/accounts/{id}.
func (h *Handler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body accountPatchRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.UpdateAccount(r.Context(), id, resourceapp.AccountPatch{
		Username: body.Username, Password: body.Password, Roles: body.Roles,
		Notes: body.Notes, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAccountDTO(view))
}

// DeleteAccount DELETE /api/resources/accounts/{id}.
func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteAccount(r.Context(), id, actor); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- code repositories ----------

// CreateRepository POST /api/resources/code-repos.
func (h *Handler) CreateRepository(w http.ResponseWriter, r *http.Request) {
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body repositoryCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.CreateRepository(r.Context(), resourceapp.RepositoryInput{
		Name: body.Name, Path: body.Path, Branch: body.Branch,
		Include: body.Include, Exclude: body.Exclude, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toCodeRepositoryDTO(view))
}

// ListRepositories GET /api/resources/code-repos.
func (h *Handler) ListRepositories(w http.ResponseWriter, r *http.Request) {
	limit := pagination.Limit(r.URL.Query().Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	cursor, err := pagination.Cursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.Error(w, r, platform.Invalid("cursor", "cursor must be a decimal ID"))
		return
	}
	var beforeID *int64
	if cursor > 0 {
		beforeID = &cursor
	}
	views, hasMore, err := h.svc.ListRepositories(r.Context(), limit, beforeID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]codeRepositoryDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toCodeRepositoryDTO(view))
	}
	writeList(w, items, hasMore, func(item codeRepositoryDTO) string { return item.ID })
}

// GetRepository GET /api/resources/code-repos/{id}.
func (h *Handler) GetRepository(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.GetRepository(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCodeRepositoryDTO(view))
}

// UpdateRepository PATCH /api/resources/code-repos/{id}.
func (h *Handler) UpdateRepository(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body repositoryPatchRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.UpdateRepository(r.Context(), id, resourceapp.RepositoryPatch{
		Name: body.Name, Path: body.Path, Branch: body.Branch,
		Include: body.Include, Exclude: body.Exclude, Status: body.Status,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCodeRepositoryDTO(view))
}

// DeleteRepository DELETE /api/resources/code-repos/{id}.
func (h *Handler) DeleteRepository(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteRepository(r.Context(), id, actor); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
