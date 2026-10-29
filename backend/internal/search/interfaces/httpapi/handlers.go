// Package httpapi exposes the P8 global search over HTTP (design spec §6.2): one
// read-only endpoint that aggregates the version, requirement, case and issue
// providers. Handlers stay thin: they validate the query, forward it to the
// search application service and render the shared envelopes.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// Handlers is the global-search HTTP handler group. The composition root stores
// it in server.Dependencies.Search; a nil group mounts nothing.
type Handlers struct {
	searcher Searcher
}

// NewHandlers builds the handler group. It returns nil when no searcher is
// wired, mirroring the other contexts' partially-wired-process behaviour.
func NewHandlers(searcher Searcher) *Handlers {
	if searcher == nil {
		return nil
	}
	return &Handlers{searcher: searcher}
}

// RegisterRoutes mounts GET /api/search. Authentication is the server's global
// bearer middleware; the endpoint adds no per-route guard (spec §6.2: every
// logged-in user may search).
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || h.searcher == nil || mux == nil {
		return
	}
	mux.Handle("GET /api/search", http.HandlerFunc(h.handleSearch))
}

// RegisterRoutes mounts handler on mux. It is the package-level form of the
// method, for composition roots that prefer an explicit handler argument.
func RegisterRoutes(mux *http.ServeMux, handler *Handlers) {
	handler.RegisterRoutes(mux)
}

// handleSearch serves GET /api/search?q=&types=&limit=.
func (h *Handlers) handleSearch(w http.ResponseWriter, r *http.Request) {
	rawQuery := r.URL.Query().Get("q")
	text, err := searchapp.ValidateQueryText(rawQuery)
	if err != nil {
		writeInvalidArgument(w, r, "q", "q must be between 1 and 100 characters", nil)
		return
	}

	types, typeErr := parseTypes(r)
	if typeErr != nil {
		writeInvalidArgument(w, r, "types",
			"types must be a subset of version,requirement,case,issue",
			map[string]any{"allowed": searchapp.AllTypes})
		return
	}

	limit, limitErr := parseLimit(r)
	if limitErr != nil {
		writeInvalidArgument(w, r, "limit", "limit must be between 1 and 20",
			map[string]any{
				"min": searchapp.MinLimit,
				"max": searchapp.MaxLimit,
			})
		return
	}

	snapshot, err := h.searcher.Search(r.Context(), searchapp.Query{
		Text:  text,
		Types: types,
		Limit: limit,
	})
	if err != nil {
		var invalid *searchapp.ErrInvalidArgument
		if errors.As(err, &invalid) {
			writeInvalidArgument(w, r, invalid.Field, invalid.Error(), nil)
			return
		}
		// Never leak the provider message: it may carry database details.
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal,
			"internal server error")
		return
	}

	// Search results are real-time data: no cross-request or browser caching.
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, snapshot)
}

// parseTypes validates the optional comma-separated `types` filter. A present
// but empty value means the default all-types filter (nil).
func parseTypes(r *http.Request) ([]string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("types"))
	if raw == "" {
		return nil, nil
	}
	tokens := make([]string, 0, 4)
	for _, token := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		tokens = append(tokens, trimmed)
	}
	if err := searchapp.ValidateTypes(tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

// parseLimit validates the optional per-type limit (default 5, range 1–20).
func parseLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return searchapp.DefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > searchapp.MaxLimit {
		return 0, errInvalidLimit
	}
	return limit, nil
}

// errInvalidLimit is the sentinel of a rejected limit parameter; the handler
// renders the public details map itself.
var errInvalidLimit = errors.New("invalid limit")

// writeInvalidArgument renders the shared 400 invalid_argument envelope with the
// rejected parameter name in details.field.
func writeInvalidArgument(w http.ResponseWriter, r *http.Request, field, message string, extra map[string]any) {
	details := map[string]any{
		"field":  field,
		"reason": searchapp.ReasonInvalidArgument,
	}
	for key, value := range extra {
		details[key] = value
	}
	platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, message,
		platform.WithDetails(details))
}

// dataEnvelope is the success shape of the search endpoint: {"data": snapshot}.
type dataEnvelope struct {
	Data any `json:"data"`
}

// writeData writes {"data": data} with the shared JSON content type.
func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dataEnvelope{Data: data})
}
