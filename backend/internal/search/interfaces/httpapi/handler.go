// Package httpapi exposes the P6 global-search endpoint (GET /api/search,
// spec §4.3.1/§6). Handlers only validate the query parameters, call the search
// application service and project the snapshot onto the frozen wire DTO; they
// never read another context's data and never build deep links themselves.
package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// Searcher is the global-search use case consumed by the handler.
type Searcher interface {
	Search(ctx context.Context, q string, types []string, limit int) (searchapp.Snapshot, error)
}

// Handler serves GET /api/search. The server mounts it behind the bearer
// authentication middleware; the handler itself never parses credentials.
type Handler struct {
	svc Searcher
}

// New builds the global-search handler.
func New(svc Searcher) *Handler {
	return &Handler{svc: svc}
}

// Search serves GET /api/search?q=&types=&limit=.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.svc == nil {
		httpx.Error(w, r, platform.Unavailable("search service is not configured"))
		return
	}
	query, err := searchapp.NormalizeQuery(r.URL.Query().Get("q"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	types, err := parseTypes(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	snapshot, err := h.svc.Search(r.Context(), query, types, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSearchResultDTO(snapshot))
}

// parseTypes validates the optional comma-separated `types` filter. An absent
// parameter selects every type; a present parameter must name at least one
// supported type (an explicit empty filter is a 400, not the all-types
// default).
func parseTypes(r *http.Request) ([]string, error) {
	values, present := r.URL.Query()["types"]
	if !present {
		return nil, nil
	}
	tokens := make([]string, 0, len(values)+len(searchapp.AllTypes))
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(token); trimmed != "" {
				tokens = append(tokens, trimmed)
			}
		}
	}
	if len(tokens) == 0 {
		return nil, platform.Invalid("types",
			"types must be a subset of version,requirement,case,issue,report")
	}
	return searchapp.NormalizeTypes(tokens)
}

// parseLimit validates the optional per-type limit (default 5, range 1–20).
func parseLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return searchapp.DefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < searchapp.MinLimit || limit > searchapp.MaxLimit {
		return 0, platform.Invalid("limit", "limit must be between 1 and 20")
	}
	return limit, nil
}

var _ Searcher = (*searchapp.Service)(nil)
