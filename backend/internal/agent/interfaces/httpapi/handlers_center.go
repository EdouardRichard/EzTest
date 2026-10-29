package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// handleCatalog serves GET /api/agent-center/catalog: the 13 logical roles with
// their profile projection and run statistics. No prompt or credential crosses
// this boundary.
func (h *CenterHandler) handleCatalog(w http.ResponseWriter, r *http.Request) {
	catalog, err := h.reader.Catalog(r.Context())
	if err != nil {
		writeCenterInternalError(w, r)
		return
	}
	writeCenterData(w, http.StatusOK, catalog)
}

// handleSessions serves GET /api/agent-center/sessions?role=&status=&limit=.
// The query is validated at the HTTP boundary; the service re-validates the
// role/status sentinels and bounds the limit for direct callers.
func (h *CenterHandler) handleSessions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	role := strings.TrimSpace(query.Get("role"))
	status := strings.TrimSpace(query.Get("status"))
	limit, ok := parseCenterLimit(w, r, query.Get("limit"))
	if !ok {
		return
	}

	page, err := h.reader.Sessions(r.Context(), application.CenterSessionQuery{
		Role:   role,
		Status: status,
		Limit:  limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrUnknownCenterRole):
			platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
				"unknown agent role", platform.WithDetails(map[string]any{"field": "role"}))
		case errors.Is(err, application.ErrUnknownCenterStatus):
			platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument,
				"unknown run status", platform.WithDetails(map[string]any{"field": "status"}))
		default:
			writeCenterInternalError(w, r)
		}
		return
	}
	writeCenterData(w, http.StatusOK, page)
}

// handleQuality serves GET /api/agent-center/quality. Before Task 23 wires the
// evaluation context the body is {"data":{"latest":null}}.
func (h *CenterHandler) handleQuality(w http.ResponseWriter, r *http.Request) {
	quality, err := h.reader.Quality(r.Context())
	if err != nil {
		writeCenterInternalError(w, r)
		return
	}
	writeCenterData(w, http.StatusOK, quality)
}

// parseCenterLimit validates the optional limit parameter against the public
// 1–100 window. An empty value uses the default.
func parseCenterLimit(w http.ResponseWriter, r *http.Request, raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return application.CenterDefaultSessionLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > application.CenterMaxSessionLimit {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
			"limit must be between 1 and 100", platform.WithDetails(map[string]any{"field": "limit"}))
		return 0, false
	}
	return limit, true
}

// writeCenterData writes the shared {"data": ...} success envelope with no
// caching: the catalogue and run observation are live read models.
func writeCenterData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, struct {
		Data any `json:"data"`
	}{Data: data})
}

// writeCenterInternalError writes the shared opaque 500 envelope. The
// underlying error may carry database details and is never leaked.
func writeCenterInternalError(w http.ResponseWriter, r *http.Request) {
	platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
}
