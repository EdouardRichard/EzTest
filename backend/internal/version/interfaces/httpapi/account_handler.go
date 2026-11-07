package versionapi

// Task 29 (v0.22 R-C1-2/API-18): version account association.
//
//	GET  /api/versions/{id}/accounts
//	POST /api/versions/{id}/accounts
//
// Accounts are a global pool and a version opts into a subset through
// version_account (DB-14/DB-15); execution only accepts the bound subset. The
// handlers reuse the existing binding read/update service methods: GET projects
// BindingOverview.Accounts and POST adds account IDs by updating the binding
// account selection. Ownership/active-account validation and the
// version.binding.changed audit event stay in *versionapp.Service. Task 37
// mounts the routes through RegisterVersionAccountRoutes.

import (
	"net/http"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VersionAccountHandler serves /api/versions/{id}/accounts. The server mounts it
// behind the shared bearer-authentication middleware; the write route enforces
// the authenticated actor like the sibling binding handler.
type VersionAccountHandler struct {
	svc BindingService
}

// NewVersionAccountHandler builds the account handler. A nil service answers
// 503 on the write path and leaves the read path to the service error.
func NewVersionAccountHandler(svc BindingService) *VersionAccountHandler {
	return &VersionAccountHandler{svc: svc}
}

// RegisterVersionAccountRoutes mounts API-18 on mux for Task 37. A nil mux or
// authentication middleware is a wiring bug (an authenticated route must never
// be silently unmounted), so the helper panics; a nil handler keeps the route
// table of a deployment without the version context.
func RegisterVersionAccountRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *VersionAccountHandler) {
	if mux == nil {
		panic("versionapi: RegisterVersionAccountRoutes requires a non-nil mux")
	}
	if auth == nil {
		panic("versionapi: RegisterVersionAccountRoutes requires an authentication middleware")
	}
	if h == nil {
		return
	}
	mux.Handle("GET /api/versions/{id}/accounts", auth(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/versions/{id}/accounts", auth(http.HandlerFunc(h.Add)))
}

// List GET /api/versions/{id}/accounts. A missing version is a typed 404.
func (h *VersionAccountHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := bindingPathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.svc == nil {
		httpx.Error(w, r, platform.Unavailable("version accounts are not configured"))
		return
	}
	overview, err := h.svc.GetBinding(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.List(w, http.StatusOK, toVersionAccountDTOs(overview), "", false)
}

// Add POST /api/versions/{id}/accounts. The body carries account_ids (required,
// at least one positive decimal ID); the IDs are appended to the current bound
// subset. Unknown/inactive accounts are a typed 400, a missing version a 404 and
// a read-only stage a 409. Adding an already-bound account is an idempotent 200.
func (h *VersionAccountHandler) Add(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := bindingPathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request versionAccountAddRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(request.AccountIDs) == 0 {
		httpx.Error(w, r, platform.Invalid("account_ids", "at least one account ID is required"))
		return
	}
	if h == nil || h.svc == nil {
		httpx.Error(w, r, platform.Unavailable("version accounts are not configured"))
		return
	}
	requested := make([]int64, 0, len(request.AccountIDs))
	for index, raw := range request.AccountIDs {
		accountID, err := validation.NonEmptyID("account_ids["+strconv.Itoa(index)+"]", raw)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		requested = append(requested, accountID)
	}
	current, err := h.svc.GetBinding(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	accountIDs := make([]int64, 0, len(current.Accounts)+len(requested))
	for _, account := range current.Accounts {
		accountID, err := strconv.ParseInt(account.ID, 10, 64)
		if err != nil || accountID <= 0 {
			continue
		}
		accountIDs = append(accountIDs, accountID)
	}
	accountIDs = append(accountIDs, requested...)
	overview, err := h.svc.UpdateBinding(r.Context(), versionID, versionapp.BindingPatch{
		AccountIDs: &accountIDs,
	}, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.List(w, http.StatusOK, toVersionAccountDTOs(overview), "", false)
}

type versionAccountAddRequest struct {
	AccountIDs []string `json:"account_ids"`
}

func toVersionAccountDTOs(overview versionapp.BindingOverview) []accountDTO {
	accounts := make([]accountDTO, 0, len(overview.Accounts))
	for _, account := range overview.Accounts {
		accounts = append(accounts, toAccountDTO(account))
	}
	return accounts
}
