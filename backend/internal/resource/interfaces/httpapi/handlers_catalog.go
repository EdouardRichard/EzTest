package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// maxCatalogBodyBytes caps ordinary catalog request bodies (spec §7).
const maxCatalogBodyBytes int64 = 64 << 10

// maxCatalogImportBodyBytes caps the app model import body. A single import may
// carry up to 5000 nodes, so it needs more room than an ordinary catalog write;
// the frontend dialog allows body payloads up to 5 MiB.
const maxCatalogImportBodyBytes int64 = 5 << 20

// CatalogHandler serves the system, environment and app model endpoints. Read
// routes are mounted plain (the global auth middleware still requires a login);
// write routes are wrapped by the caller's admin guard.
type CatalogHandler struct {
	svc *application.CatalogService
}

// NewCatalogHandler returns the catalog handlers for svc.
func NewCatalogHandler(svc *application.CatalogService) *CatalogHandler {
	return &CatalogHandler{svc: svc}
}

// RegisterRoutes mounts the catalog endpoints on mux. guard wraps every write
// route; the server passes an admin guard built from RequireAdmin.
func (h *CatalogHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}

	mux.HandleFunc("GET /api/resources/systems", h.ListSystems)
	mux.HandleFunc("GET /api/resources/systems/{id}", h.GetSystem)
	mux.HandleFunc("GET /api/resources/systems/{id}/environments", h.ListEnvironments)
	mux.HandleFunc("GET /api/resources/environments/{id}", h.GetEnvironment)
	mux.HandleFunc("GET /api/resources/systems/{id}/app-models", h.ListAppModels)
	mux.HandleFunc("GET /api/resources/app-models/{id}", h.GetAppModel)

	mux.Handle("POST /api/resources/systems", guard(http.HandlerFunc(h.CreateSystem)))
	mux.Handle("PATCH /api/resources/systems/{id}", guard(http.HandlerFunc(h.UpdateSystem)))
	mux.Handle("DELETE /api/resources/systems/{id}", guard(http.HandlerFunc(h.DeleteSystem)))
	mux.Handle("POST /api/resources/systems/{id}/environments", guard(http.HandlerFunc(h.CreateEnvironment)))
	mux.Handle("PATCH /api/resources/environments/{id}", guard(http.HandlerFunc(h.UpdateEnvironment)))
	mux.Handle("DELETE /api/resources/environments/{id}", guard(http.HandlerFunc(h.DeleteEnvironment)))
	mux.Handle("POST /api/resources/systems/{id}/app-models", guard(http.HandlerFunc(h.CreateAppModel)))
	mux.Handle("POST /api/resources/systems/{id}/app-models/import", guard(http.HandlerFunc(h.ImportAppModels)))
	mux.Handle("PATCH /api/resources/app-models/{id}", guard(http.HandlerFunc(h.UpdateAppModel)))
	mux.Handle("DELETE /api/resources/app-models/{id}", guard(http.HandlerFunc(h.DeleteAppModel)))
}

// systemDTO is the public System representation (camelCase, spec §7.2).
type systemDTO struct {
	ID                       int64     `json:"id"`
	Key                      string    `json:"key"`
	Name                     string    `json:"name"`
	Kind                     string    `json:"kind"`
	BaseURL                  string    `json:"baseUrl"`
	DefaultSessionTTLSeconds int       `json:"defaultSessionTtlSeconds"`
	Notes                    string    `json:"notes"`
	Status                   string    `json:"status"`
	CreatedAt                time.Time `json:"createdAt"`
	UpdatedAt                time.Time `json:"updatedAt"`
}

// newSystemDTO maps a system to its API shape.
func newSystemDTO(system domain.System) systemDTO {
	return systemDTO{
		ID:                       system.ID,
		Key:                      system.Key,
		Name:                     system.Name,
		Kind:                     system.Kind,
		BaseURL:                  system.BaseURL,
		DefaultSessionTTLSeconds: system.DefaultSessionTTLSeconds,
		Notes:                    system.Notes,
		Status:                   system.Status,
		CreatedAt:                system.CreatedAt,
		UpdatedAt:                system.UpdatedAt,
	}
}

// loginCheckDTO is the public login_check document.
type loginCheckDTO struct {
	URL          string `json:"url"`
	ExpectStatus []int  `json:"expectStatus"`
	BodyContains string `json:"bodyContains"`
}

// toDomain maps the DTO to the stored document.
func (lc loginCheckDTO) toDomain() *domain.LoginCheck {
	statuses := make([]int, len(lc.ExpectStatus))
	copy(statuses, lc.ExpectStatus)
	return &domain.LoginCheck{URL: lc.URL, ExpectStatus: statuses, BodyContains: lc.BodyContains}
}

// newLoginCheckDTO maps a stored login_check to its API shape.
func newLoginCheckDTO(loginCheck *domain.LoginCheck) *loginCheckDTO {
	if loginCheck == nil {
		return nil
	}
	statuses := make([]int, len(loginCheck.ExpectStatus))
	copy(statuses, loginCheck.ExpectStatus)
	return &loginCheckDTO{URL: loginCheck.URL, ExpectStatus: statuses, BodyContains: loginCheck.BodyContains}
}

// environmentDTO is the public Environment representation (camelCase, spec §7.2).
type environmentDTO struct {
	ID              int64          `json:"id"`
	SystemID        int64          `json:"systemId"`
	Key             string         `json:"key"`
	Name            string         `json:"name"`
	BaseURL         string         `json:"baseUrl"`
	Kind            string         `json:"kind"`
	IsProd          bool           `json:"isProd"`
	AllowedHosts    []string       `json:"allowedHosts"`
	LoginCheck      *loginCheckDTO `json:"loginCheck"`
	AllowAutoLogin  bool           `json:"allowAutoLogin"`
	RequireApproval bool           `json:"requireApproval"`
}

// newEnvironmentDTO maps an environment to its API shape.
func newEnvironmentDTO(environment domain.Environment) environmentDTO {
	hosts := environment.AllowedHosts
	if hosts == nil {
		hosts = []string{}
	}
	return environmentDTO{
		ID:              environment.ID,
		SystemID:        environment.SystemID,
		Key:             environment.Key,
		Name:            environment.Name,
		BaseURL:         environment.BaseURL,
		Kind:            environment.Kind,
		IsProd:          environment.IsProd,
		AllowedHosts:    hosts,
		LoginCheck:      newLoginCheckDTO(environment.LoginCheck),
		AllowAutoLogin:  environment.AllowAutoLogin,
		RequireApproval: environment.RequireApproval,
	}
}

// appModelDTO is the public AppModelNode representation (camelCase, spec §7.3).
type appModelDTO struct {
	ID       int64           `json:"id"`
	SystemID int64           `json:"systemId"`
	ParentID *int64          `json:"parentId"`
	Kind     string          `json:"kind"`
	Key      string          `json:"key"`
	Name     string          `json:"name"`
	Path     string          `json:"path"`
	Method   string          `json:"method"`
	Source   string          `json:"source"`
	Meta     json.RawMessage `json:"meta"`
}

// newAppModelDTO maps an app model node to its API shape.
func newAppModelDTO(node domain.AppModelNode) appModelDTO {
	meta := node.Meta
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	return appModelDTO{
		ID:       node.ID,
		SystemID: node.SystemID,
		ParentID: node.ParentID,
		Kind:     node.Kind,
		Key:      node.Key,
		Name:     node.Name,
		Path:     node.Path,
		Method:   node.Method,
		Source:   node.Source,
		Meta:     meta,
	}
}

// createSystemRequest is the POST /systems body.
type createSystemRequest struct {
	Key                      string `json:"key"`
	Name                     string `json:"name"`
	Kind                     string `json:"kind"`
	BaseURL                  string `json:"baseUrl"`
	DefaultSessionTTLSeconds int    `json:"defaultSessionTtlSeconds"`
	Notes                    string `json:"notes"`
}

// updateSystemRequest is the PATCH /systems/{id} body. Nil fields are unchanged.
type updateSystemRequest struct {
	Name                     *string `json:"name"`
	Kind                     *string `json:"kind"`
	BaseURL                  *string `json:"baseUrl"`
	Notes                    *string `json:"notes"`
	DefaultSessionTTLSeconds *int    `json:"defaultSessionTtlSeconds"`
	Status                   *string `json:"status"`
}

// createEnvironmentRequest is the POST /systems/{id}/environments body.
type createEnvironmentRequest struct {
	Key             string         `json:"key"`
	Name            string         `json:"name"`
	BaseURL         string         `json:"baseUrl"`
	Kind            string         `json:"kind"`
	IsProd          bool           `json:"isProd"`
	AllowedHosts    []string       `json:"allowedHosts"`
	LoginCheck      *loginCheckDTO `json:"loginCheck"`
	AllowAutoLogin  bool           `json:"allowAutoLogin"`
	RequireApproval bool           `json:"requireApproval"`
}

// updateEnvironmentRequest is the PATCH /environments/{id} body. Nil fields are
// unchanged; an explicit "loginCheck": null clears the stored document.
type updateEnvironmentRequest struct {
	Name            *string         `json:"name"`
	BaseURL         *string         `json:"baseUrl"`
	Kind            *string         `json:"kind"`
	IsProd          *bool           `json:"isProd"`
	AllowedHosts    *[]string       `json:"allowedHosts"`
	LoginCheck      json.RawMessage `json:"loginCheck"`
	AllowAutoLogin  *bool           `json:"allowAutoLogin"`
	RequireApproval *bool           `json:"requireApproval"`
}

// appModelRequest is one app model node in a create or import body.
type appModelRequest struct {
	Key       string          `json:"key"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	Method    string          `json:"method"`
	ParentKey string          `json:"parentKey"`
	Meta      json.RawMessage `json:"meta"`
}

// toInput maps the request to the application input.
func (req appModelRequest) toInput() application.AppModelInput {
	return application.AppModelInput{
		Key: req.Key, Kind: req.Kind, Name: req.Name, Path: req.Path,
		Method: req.Method, ParentKey: req.ParentKey, Meta: req.Meta,
	}
}

// importAppModelsRequest is the POST /systems/{id}/app-models/import body.
type importAppModelsRequest struct {
	Nodes []appModelRequest `json:"nodes"`
}

// updateAppModelRequest is the PATCH /app-models/{id} body. Key and kind are not
// accepted: they are immutable after creation.
type updateAppModelRequest struct {
	Name      *string         `json:"name"`
	Path      *string         `json:"path"`
	Method    *string         `json:"method"`
	ParentKey *string         `json:"parentKey"`
	Meta      json.RawMessage `json:"meta"`
}

// ListSystems returns one cursor page of systems.
func (h *CatalogHandler) ListSystems(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	systems, hasMore, err := h.svc.ListSystems(r.Context(), domain.SystemFilter{
		Status: r.URL.Query().Get("status"),
		Kind:   r.URL.Query().Get("kind"),
		Page:   page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]systemDTO, len(systems))
	for i, system := range systems {
		items[i] = newSystemDTO(system)
	}
	next := ""
	if hasMore && len(systems) > 0 {
		next = platform.EncodeCursor(systems[len(systems)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// GetSystem returns one system.
func (h *CatalogHandler) GetSystem(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	system, err := h.svc.GetSystem(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newSystemDTO(*system))
}

// CreateSystem stores a new system.
func (h *CatalogHandler) CreateSystem(w http.ResponseWriter, r *http.Request) {
	var req createSystemRequest
	if err := DecodeJSON(w, r, &req, maxCatalogBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	system, err := h.svc.CreateSystem(r.Context(), application.CreateSystemInput{
		Key:                      req.Key,
		Name:                     req.Name,
		Kind:                     req.Kind,
		BaseURL:                  req.BaseURL,
		Notes:                    req.Notes,
		DefaultSessionTTLSeconds: req.DefaultSessionTTLSeconds,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newSystemDTO(*system))
}

// UpdateSystem applies a partial update to a system.
func (h *CatalogHandler) UpdateSystem(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req updateSystemRequest
	if err := DecodeJSON(w, r, &req, maxCatalogBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	system, err := h.svc.UpdateSystem(r.Context(), id, application.UpdateSystemInput{
		Name:                     req.Name,
		Kind:                     req.Kind,
		BaseURL:                  req.BaseURL,
		Notes:                    req.Notes,
		DefaultSessionTTLSeconds: req.DefaultSessionTTLSeconds,
		Status:                   req.Status,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newSystemDTO(*system))
}

// DeleteSystem deletes a system unless it still has dependents.
func (h *CatalogHandler) DeleteSystem(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.DeleteSystem(r.Context(), id); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListEnvironments returns every environment of a system.
func (h *CatalogHandler) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	systemID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	environments, err := h.svc.ListEnvironments(r.Context(), systemID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]environmentDTO, len(environments))
	for i, environment := range environments {
		items[i] = newEnvironmentDTO(environment)
	}
	WriteData(w, http.StatusOK, items)
}

// GetEnvironment returns one environment.
func (h *CatalogHandler) GetEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	environment, err := h.svc.GetEnvironment(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newEnvironmentDTO(*environment))
}

// CreateEnvironment stores a new environment of a system.
func (h *CatalogHandler) CreateEnvironment(w http.ResponseWriter, r *http.Request) {
	systemID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req createEnvironmentRequest
	if err := DecodeJSON(w, r, &req, maxCatalogBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	in := application.CreateEnvironmentInput{
		Key:             req.Key,
		Name:            req.Name,
		BaseURL:         req.BaseURL,
		Kind:            req.Kind,
		IsProd:          req.IsProd,
		AllowedHosts:    req.AllowedHosts,
		AllowAutoLogin:  req.AllowAutoLogin,
		RequireApproval: req.RequireApproval,
	}
	if req.LoginCheck != nil {
		in.LoginCheck = req.LoginCheck.toDomain()
	}
	environment, err := h.svc.CreateEnvironment(r.Context(), systemID, in)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newEnvironmentDTO(*environment))
}

// UpdateEnvironment applies a partial update to an environment.
func (h *CatalogHandler) UpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req updateEnvironmentRequest
	if err := DecodeJSON(w, r, &req, maxCatalogBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	in := application.UpdateEnvironmentInput{
		Name:            req.Name,
		BaseURL:         req.BaseURL,
		Kind:            req.Kind,
		IsProd:          req.IsProd,
		AllowedHosts:    req.AllowedHosts,
		AllowAutoLogin:  req.AllowAutoLogin,
		RequireApproval: req.RequireApproval,
	}
	if len(req.LoginCheck) > 0 {
		if string(req.LoginCheck) == "null" {
			in.ClearLoginCheck = true
		} else {
			var loginCheck loginCheckDTO
			if err := json.Unmarshal(req.LoginCheck, &loginCheck); err != nil {
				WriteServiceError(w, r, domain.NewInvalid("loginCheck", "invalid_login_check",
					"loginCheck must be an object or null"))
				return
			}
			in.LoginCheck = loginCheck.toDomain()
		}
	}
	environment, err := h.svc.UpdateEnvironment(r.Context(), id, in)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newEnvironmentDTO(*environment))
}

// DeleteEnvironment deletes an environment unless it still has sessions.
func (h *CatalogHandler) DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.DeleteEnvironment(r.Context(), id); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAppModels returns one cursor page of a system's app model nodes.
func (h *CatalogHandler) ListAppModels(w http.ResponseWriter, r *http.Request) {
	systemID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	nodes, hasMore, err := h.svc.ListAppModels(r.Context(), domain.AppModelFilter{
		SystemID:  systemID,
		Kind:      r.URL.Query().Get("kind"),
		ParentKey: r.URL.Query().Get("parentKey"),
		Page:      page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]appModelDTO, len(nodes))
	for i, node := range nodes {
		items[i] = newAppModelDTO(node)
	}
	next := ""
	if hasMore && len(nodes) > 0 {
		next = platform.EncodeCursor(nodes[len(nodes)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// GetAppModel returns one app model node.
func (h *CatalogHandler) GetAppModel(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	node, err := h.svc.GetAppModel(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAppModelDTO(*node))
}

// CreateAppModel stores a new app model node.
func (h *CatalogHandler) CreateAppModel(w http.ResponseWriter, r *http.Request) {
	systemID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req appModelRequest
	if err := DecodeJSON(w, r, &req, maxCatalogBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	node, err := h.svc.CreateAppModel(r.Context(), systemID, req.toInput())
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newAppModelDTO(*node))
}

// ImportAppModels upserts a batch of app model nodes and returns the counts.
func (h *CatalogHandler) ImportAppModels(w http.ResponseWriter, r *http.Request) {
	systemID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req importAppModelsRequest
	if err := DecodeJSON(w, r, &req, maxCatalogImportBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	inputs := make([]application.AppModelInput, len(req.Nodes))
	for i, node := range req.Nodes {
		inputs[i] = node.toInput()
	}
	created, updated, err := h.svc.ImportAppModels(r.Context(), systemID, inputs)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, map[string]int{"created": created, "updated": updated})
}

// UpdateAppModel applies a partial update to an app model node. Omitted fields
// keep their stored value; key and kind are immutable.
func (h *CatalogHandler) UpdateAppModel(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req updateAppModelRequest
	if err := DecodeJSON(w, r, &req, maxCatalogBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	node, err := h.svc.GetAppModel(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	in := application.AppModelInput{
		Key:    node.Key,
		Kind:   node.Kind,
		Name:   node.Name,
		Path:   node.Path,
		Method: node.Method,
		Meta:   node.Meta,
	}
	if node.ParentID != nil {
		parent, err := h.svc.GetAppModel(r.Context(), *node.ParentID)
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		in.ParentKey = parent.Key
	}
	if req.Name != nil {
		in.Name = *req.Name
	}
	if req.Path != nil {
		in.Path = *req.Path
	}
	if req.Method != nil {
		in.Method = *req.Method
	}
	if req.ParentKey != nil {
		in.ParentKey = *req.ParentKey
	}
	if len(req.Meta) > 0 {
		in.Meta = req.Meta
	}
	updated, err := h.svc.UpdateAppModel(r.Context(), id, in)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAppModelDTO(*updated))
}

// DeleteAppModel deletes a node and its subtree.
func (h *CatalogHandler) DeleteAppModel(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.DeleteAppModel(r.Context(), id); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
