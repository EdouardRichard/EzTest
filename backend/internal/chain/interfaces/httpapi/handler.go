// Package httpapi exposes the read-only exploration-chain endpoint
// (GET /api/versions/{versionId}/exploration-chain, spec §4.2/§8). The handlers
// only parse the query, call the chain application service and map the view
// onto the wire DTO; they never read another context's tables.
package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Service is the chain application surface the handler needs.
type Service interface {
	Read(ctx context.Context, versionID int64, filter chain.ChainFilter) (chain.ChainView, error)
}

// Handler serves GET /api/versions/{versionId}/exploration-chain.
type Handler struct {
	svc Service
}

// New builds the exploration-chain handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts the exploration-chain route on mux. The route is read-only
// and must be mounted behind the server's bearer authentication middleware
// (Task 23 wiring); the handler never parses credentials itself.
func Register(mux *http.ServeMux, h *Handler) {
	mux.Handle("GET /api/versions/{versionId}/exploration-chain", http.HandlerFunc(h.Read))
}

// Read GET /api/versions/{versionId}/exploration-chain.
func (h *Handler) Read(w http.ResponseWriter, r *http.Request) {
	versionID, err := validation.NonEmptyID("versionId", r.PathValue("versionId"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	filter, err := parseChainFilter(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Read(r.Context(), versionID, filter)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toChainViewDTO(view))
}

// parseChainFilter maps the documented query parameters onto a ChainFilter.
// Normalization (trim/lower-case/limit bounds) stays in the domain/service so
// the HTTP layer and the service share one canonical semantic. Numeric
// parameters must already be valid decimals, and a reversed event interval is
// accepted and normalized by the service.
func parseChainFilter(r *http.Request) (chain.ChainFilter, error) {
	query := r.URL.Query()
	var filter chain.ChainFilter
	var err error
	if filter.VersionSUTID, err = optionalPositiveID(query.Get("sut_id"), "sut_id"); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.PageID, err = optionalPositiveID(query.Get("page_id"), "page_id"); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.WaveID, err = optionalPositiveID(query.Get("wave_id"), "wave_id"); err != nil {
		return chain.ChainFilter{}, err
	}
	filter.Status = query.Get("status")
	filter.Result = query.Get("result")
	if filter.EventFrom, err = optionalEventSeq(query.Get("event_from"), "event_from"); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.EventTo, err = optionalEventSeq(query.Get("event_to"), "event_to"); err != nil {
		return chain.ChainFilter{}, err
	}
	filter.FocusNode = query.Get("focus_node")
	limit, err := optionalChainLimit(query.Get("limit"))
	if err != nil {
		return chain.ChainFilter{}, err
	}
	filter.Limit = limit
	return filter, nil
}

func optionalPositiveID(raw, field string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := validation.NonEmptyID(field, raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func optionalEventSeq(raw, field string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, platform.Invalid(field, "must be a non-negative decimal event sequence")
	}
	return &value, nil
}

func optionalChainLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > chain.MaxNodesLimit {
		return 0, platform.Invalid("limit", "must be an integer between 1 and 1000")
	}
	return value, nil
}

var _ Service = (*chainapp.ChainService)(nil)
