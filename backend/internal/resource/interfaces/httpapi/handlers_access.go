package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// maxAccessBodyBytes caps account/access request bodies (spec §7).
const maxAccessBodyBytes int64 = 64 << 10

// AccessHandler serves the account and account/system access endpoints. Read
// routes are mounted plain (the global auth middleware still requires a login);
// write routes are wrapped by the caller's admin guard. Credentials are
// write-only: no response DTO ever carries the stored value, only
// credentialSet (spec §7.4).
type AccessHandler struct {
	svc *application.AccessService
}

// NewAccessHandler returns the account/access handlers for svc.
func NewAccessHandler(svc *application.AccessService) *AccessHandler {
	return &AccessHandler{svc: svc}
}

// RegisterRoutes mounts the account and access endpoints on mux. guard wraps
// every write route; the server passes an admin guard built from RequireAdmin.
func (h *AccessHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}

	mux.HandleFunc("GET /api/resources/accounts", h.ListAccounts)
	mux.HandleFunc("GET /api/resources/accounts/{id}", h.GetAccount)
	mux.HandleFunc("GET /api/resources/accounts/{id}/accesses", h.ListAccesses)
	mux.HandleFunc("GET /api/resources/accesses/{id}", h.GetAccess)

	mux.Handle("POST /api/resources/accounts", guard(http.HandlerFunc(h.CreateAccount)))
	mux.Handle("PATCH /api/resources/accounts/{id}", guard(http.HandlerFunc(h.UpdateAccount)))
	mux.Handle("DELETE /api/resources/accounts/{id}", guard(http.HandlerFunc(h.DeleteAccount)))
	mux.Handle("POST /api/resources/accounts/{id}/accesses", guard(http.HandlerFunc(h.CreateAccess)))
	mux.Handle("PATCH /api/resources/accesses/{id}", guard(http.HandlerFunc(h.UpdateAccess)))
	mux.Handle("DELETE /api/resources/accesses/{id}", guard(http.HandlerFunc(h.DeleteAccess)))
}

// accountDTO is the public Account representation (camelCase, spec §7.4). It
// carries no company id: every P2 resource belongs to the seeded company.
type accountDTO struct {
	ID       int64  `json:"id"`
	Key      string `json:"key"`
	Label    string `json:"label"`
	Username string `json:"username"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Notes    string `json:"notes"`
}

// newAccountDTO maps an account to its API shape.
func newAccountDTO(account domain.Account) accountDTO {
	return accountDTO{
		ID:       account.ID,
		Key:      account.Key,
		Label:    account.Label,
		Username: account.Username,
		Kind:     account.Kind,
		Status:   account.Status,
		Notes:    account.Notes,
	}
}

// accessDTO is the public Access representation (camelCase, spec §7.4). The
// stored credential is never part of it: credentialSet reports whether one is
// stored so the UI can offer "clear". login_flow_id keeps the snake_case name
// of the P6 login-recipe API (spec §3.4) and is a decimal string or null
// (spec §6.1).
type accessDTO struct {
	ID                int64   `json:"id"`
	AccountID         int64   `json:"accountId"`
	SystemID          int64   `json:"systemId"`
	AuthType          string  `json:"authType"`
	CredentialSet     bool    `json:"credentialSet"`
	UsernameOverride  string  `json:"usernameOverride"`
	LoginURL          string  `json:"loginUrl"`
	Enabled           bool    `json:"enabled"`
	ConcurrencyLimit  int     `json:"concurrencyLimit"`
	SessionTTLSeconds *int    `json:"sessionTtlSeconds"`
	PermissionNote    string  `json:"permissionNote"`
	LoginFlowID       *string `json:"login_flow_id"`
}

// newAccessDTO maps an access to its API shape, reducing the plaintext
// credential to its presence flag.
func newAccessDTO(access domain.Access) accessDTO {
	return accessDTO{
		ID:                access.ID,
		AccountID:         access.AccountID,
		SystemID:          access.SystemID,
		AuthType:          access.AuthType,
		CredentialSet:     access.Credential != "",
		UsernameOverride:  access.UsernameOverride,
		LoginURL:          access.LoginURL,
		Enabled:           access.Enabled,
		ConcurrencyLimit:  access.ConcurrencyLimit,
		SessionTTLSeconds: access.SessionTTLSeconds,
		PermissionNote:    access.PermissionNote,
		LoginFlowID:       formatAccessLoginFlowID(access.LoginFlowID),
	}
}

// formatAccessLoginFlowID renders the nullable login_flow_id as a decimal
// string, keeping nil as an explicit JSON null.
func formatAccessLoginFlowID(id *int64) *string {
	if id == nil {
		return nil
	}
	formatted := strconv.FormatInt(*id, 10)
	return &formatted
}

// parseAccessLoginFlowID decodes the optional login_flow_id body value. An
// omitted field returns present=false; JSON null returns present=true with a nil
// id; a decimal string returns the parsed positive id. Everything else is the
// shared 422 auth_flow_invalid error.
func parseAccessLoginFlowID(raw json.RawMessage) (id *int64, present bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, true, domain.NewInvalid("login_flow_id", "auth_flow_invalid",
			"login_flow_id must be a decimal string or null")
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, true, domain.NewInvalid("login_flow_id", "auth_flow_invalid",
			"login_flow_id must be a positive decimal string or null")
	}
	return &parsed, true, nil
}

// createAccountRequest is the POST /accounts body.
type createAccountRequest struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Username string `json:"username"`
	Kind     string `json:"kind"`
	Notes    string `json:"notes"`
}

// updateAccountRequest is the PATCH /accounts/{id} body. Nil fields are
// unchanged; key and company are not accepted because they are immutable.
type updateAccountRequest struct {
	Label    *string `json:"label"`
	Username *string `json:"username"`
	Kind     *string `json:"kind"`
	Notes    *string `json:"notes"`
	Status   *string `json:"status"`
}

// createAccessRequest is the POST /accounts/{id}/accesses body. An omitted
// enabled flag adopts the DDL default (true); an omitted credential leaves
// credentialSet=false; an omitted or null login_flow_id leaves the access
// unbound.
type createAccessRequest struct {
	SystemID          int64           `json:"systemId"`
	AuthType          string          `json:"authType"`
	Credential        string          `json:"credential"`
	UsernameOverride  string          `json:"usernameOverride"`
	LoginURL          string          `json:"loginUrl"`
	Enabled           *bool           `json:"enabled"`
	ConcurrencyLimit  int             `json:"concurrencyLimit"`
	SessionTTLSeconds *int            `json:"sessionTtlSeconds"`
	PermissionNote    string          `json:"permissionNote"`
	LoginFlowID       json.RawMessage `json:"login_flow_id"`
}

// updateAccessRequest is the PATCH /accesses/{id} body. Nil fields are
// unchanged. credential follows spec §7.4 (omitted=keep, ""=clear,
// non-empty=replace); sessionTtlSeconds is decoded separately so an explicit
// JSON null can be told apart from an omitted field. login_flow_id follows the
// same rule: omitted=keep, null=unbind, decimal string=bind.
type updateAccessRequest struct {
	AuthType         *string `json:"authType"`
	Credential       *string `json:"credential"`
	UsernameOverride *string `json:"usernameOverride"`
	LoginURL         *string `json:"loginUrl"`
	PermissionNote   *string `json:"permissionNote"`
	Enabled          *bool   `json:"enabled"`
	ConcurrencyLimit *int    `json:"concurrencyLimit"`
	// SystemID is accepted because the shared create/edit form always submits
	// every writable field; the account/system link is immutable after
	// creation, so the value is parsed and discarded.
	SystemID          *int64          `json:"systemId"`
	SessionTTLSeconds json.RawMessage `json:"sessionTtlSeconds"`
	LoginFlowID       json.RawMessage `json:"login_flow_id"`
}

// ListAccounts returns one cursor page of accounts.
func (h *AccessHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	accounts, hasMore, err := h.svc.ListAccounts(r.Context(), domain.AccountFilter{
		Status: r.URL.Query().Get("status"),
		Kind:   r.URL.Query().Get("kind"),
		Page:   page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]accountDTO, len(accounts))
	for i, account := range accounts {
		items[i] = newAccountDTO(account)
	}
	next := ""
	if hasMore && len(accounts) > 0 {
		next = platform.EncodeCursor(accounts[len(accounts)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// GetAccount returns one account.
func (h *AccessHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	account, err := h.svc.GetAccount(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAccountDTO(*account))
}

// CreateAccount stores a new account under the seeded default company.
func (h *AccessHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if err := DecodeJSON(w, r, &req, maxAccessBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	account, err := h.svc.CreateAccount(r.Context(), application.CreateAccountInput{
		Key:      req.Key,
		Label:    req.Label,
		Username: req.Username,
		Kind:     req.Kind,
		Notes:    req.Notes,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newAccountDTO(*account))
}

// UpdateAccount applies a partial update to an account.
func (h *AccessHandler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req updateAccountRequest
	if err := DecodeJSON(w, r, &req, maxAccessBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	account, err := h.svc.UpdateAccount(r.Context(), id, application.UpdateAccountInput{
		Label:    req.Label,
		Username: req.Username,
		Kind:     req.Kind,
		Notes:    req.Notes,
		Status:   req.Status,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAccountDTO(*account))
}

// DeleteAccount deletes an account unless it still has accesses or sessions.
func (h *AccessHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.DeleteAccount(r.Context(), id); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAccesses returns every access of an account. The response never carries
// a credential.
func (h *AccessHandler) ListAccesses(w http.ResponseWriter, r *http.Request) {
	accountID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	accesses, err := h.svc.ListAccesses(r.Context(), accountID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]accessDTO, len(accesses))
	for i, access := range accesses {
		items[i] = newAccessDTO(access)
	}
	WriteData(w, http.StatusOK, items)
}

// GetAccess returns one access without its credential.
func (h *AccessHandler) GetAccess(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	access, err := h.svc.GetAccess(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAccessDTO(*access))
}

// CreateAccess stores a new access of an account to a system.
func (h *AccessHandler) CreateAccess(w http.ResponseWriter, r *http.Request) {
	accountID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req createAccessRequest
	if err := DecodeJSON(w, r, &req, maxAccessBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	loginFlowID, _, err := parseAccessLoginFlowID(req.LoginFlowID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	access, err := h.svc.CreateAccess(r.Context(), accountID, application.CreateAccessInput{
		SystemID:          req.SystemID,
		AuthType:          req.AuthType,
		Credential:        req.Credential,
		UsernameOverride:  req.UsernameOverride,
		LoginURL:          req.LoginURL,
		LoginFlowID:       loginFlowID,
		PermissionNote:    req.PermissionNote,
		Enabled:           enabled,
		ConcurrencyLimit:  req.ConcurrencyLimit,
		SessionTTLSeconds: req.SessionTTLSeconds,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newAccessDTO(*access))
}

// UpdateAccess applies a partial update to an access. An omitted credential
// keeps the stored one, an empty string clears it and a value replaces it; an
// explicit JSON null on sessionTtlSeconds clears the TTL override.
func (h *AccessHandler) UpdateAccess(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req updateAccessRequest
	if err := DecodeJSON(w, r, &req, maxAccessBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	loginFlowID, loginFlowPresent, err := parseAccessLoginFlowID(req.LoginFlowID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	in := application.UpdateAccessInput{
		AuthType:         req.AuthType,
		Credential:       req.Credential,
		UsernameOverride: req.UsernameOverride,
		LoginURL:         req.LoginURL,
		PermissionNote:   req.PermissionNote,
		Enabled:          req.Enabled,
		ConcurrencyLimit: req.ConcurrencyLimit,
	}
	if loginFlowPresent {
		if loginFlowID == nil {
			in.ClearLoginFlow = true
		} else {
			in.LoginFlowID = loginFlowID
		}
	}
	if len(req.SessionTTLSeconds) > 0 {
		if string(req.SessionTTLSeconds) == "null" {
			in.ClearSessionTTL = true
		} else {
			var ttl int
			if err := json.Unmarshal(req.SessionTTLSeconds, &ttl); err != nil {
				WriteServiceError(w, r, domain.NewInvalid("sessionTtlSeconds", "invalid_session_ttl",
					"sessionTtlSeconds must be a number between 60 and 604800 or null"))
				return
			}
			in.SessionTTLSeconds = &ttl
		}
	}
	access, err := h.svc.UpdateAccess(r.Context(), id, in)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAccessDTO(*access))
}

// DeleteAccess deletes an access unless it still has sessions.
func (h *AccessHandler) DeleteAccess(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.DeleteAccess(r.Context(), id); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
