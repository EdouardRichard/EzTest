// Package httpapi exposes the read-only exploration-chain endpoint
// (GET /api/versions/{versionId}/exploration-chain, spec §4.2/§8). The handlers
// only parse the query, call the chain application service and map the view
// onto the wire DTO; they never read another context's tables.
package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// chainStatusEnums is the union of the status values the chain can expose
// across its node kinds: requirement, case version, wave, case run, issue,
// report and closure. P6 validates the filter value before it reaches the
// builder so a typo cannot silently return an empty graph.
var chainStatusEnums = map[string]bool{
	"pending": true, "running": true, "paused": true, "passed": true, "failed": true,
	"blocked": true, "inconclusive": true, "skipped": true, "interrupted": true,
	"superseded": true, "planned": true, "done": true, "cancelled": true,
	"draft": true, "clarified": true, "closed": true, "obsolete": true,
	"open": true, "confirmed": true, "dismissed": true, "merged": true,
	"published": true, "stale": true, "reopened": true, "active": true,
}

// chainResultEnums is the case_run result vocabulary exposed by the builder.
var chainResultEnums = map[string]bool{
	"passed": true, "failed": true, "blocked": true, "inconclusive": true,
	"skipped": true, "interrupted": true,
}

var chainIssueStatusEnums = map[string]bool{
	"open": true, "confirmed": true, "dismissed": true, "merged": true,
}

var chainIssueSeverityEnums = map[string]bool{
	"major": true, "minor": true, "trivial": true,
}

// focusNodePattern is the contract pattern of ChainFocusNode
// (openapi.yaml, `^[a-z_]+:[0-9]+$`). The handler rejects anything else with
// 400 before the builder can silently ignore a malformed deep link.
var focusNodePattern = regexp.MustCompile(`^[a-z_]+:[0-9]+$`)

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
	if filter.Status, err = optionalEnum(query.Get("status"), "status", chainStatusEnums); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.Result, err = optionalEnum(query.Get("result"), "result", chainResultEnums); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.Q, err = optionalQueryText(query.Get("q"), 100); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.AbnormalOnly, err = optionalBool(query.Get("abnormal_only"), "abnormal_only"); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.IssueStatus, err = optionalEnum(query.Get("issue_status"), "issue_status", chainIssueStatusEnums); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.IssueSeverity, err = optionalEnum(query.Get("issue_severity"), "issue_severity", chainIssueSeverityEnums); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.RequirementLimit, err = optionalRequirementLimit(query.Get("requirement_limit")); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.EventFrom, err = optionalEventSeq(query.Get("event_from"), "event_from"); err != nil {
		return chain.ChainFilter{}, err
	}
	if filter.EventTo, err = optionalEventSeq(query.Get("event_to"), "event_to"); err != nil {
		return chain.ChainFilter{}, err
	}
	filter.FocusNode = strings.TrimSpace(query.Get("focus_node"))
	if filter.FocusNode != "" && !focusNodePattern.MatchString(filter.FocusNode) {
		return chain.ChainFilter{}, platform.Invalid("focus_node", "must match {kind}:{ref_id} with a lowercase kind and a decimal id")
	}
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

// optionalEventSeq parses an event seq. Zero is accepted as the documented
// "unset" value (the service normalizes it away); values below zero are 400.
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

// optionalEnum normalizes and validates one documented query enum. Empty means
// unset. The normalized value is what the domain filter echoes back.
func optionalEnum(raw, field string, allowed map[string]bool) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "", nil
	}
	if !allowed[raw] {
		return "", platform.Invalid(field, "must be one of the documented enum values")
	}
	return raw, nil
}

// optionalQueryText validates the <=100-character q filter.
func optionalQueryText(raw string, maxRunes int) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if utf8.RuneCountInString(raw) > maxRunes {
		return "", platform.Invalid("q", "must be at most 100 characters")
	}
	return raw, nil
}

// optionalBool parses the abnormal_only query flag. Empty means false/unset.
func optionalBool(raw, field string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, platform.Invalid(field, "must be a boolean")
	}
	return value, nil
}

// optionalRequirementLimit parses the P6 root override (1..500).
func optionalRequirementLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > chain.MaxRequirementsLimit {
		return 0, platform.Invalid("requirement_limit", "must be an integer between 1 and 500")
	}
	return value, nil
}

var _ Service = (*chainapp.ChainService)(nil)
