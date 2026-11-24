package agentapi

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// agentProfileKeyPattern is the frozen ProfileKey schema used by the runs
// filter too (OpenAPI AgentProfileKeyFilter).
var agentProfileKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// AgentCenterService is the application surface the read-only handler needs.
// *agentapp.AgentCenterService satisfies it.
type AgentCenterService interface {
	Catalog(ctx context.Context) (agentapp.Catalog, error)
	Runs(ctx context.Context, filter agentapp.RunFilter) ([]agentapp.RunView, bool, error)
	Quality(ctx context.Context) (agentapp.QualityView, error)
	// RunTranscript is the admin-only live transcript read (v0.2.4-D T8).
	RunTranscript(ctx context.Context, runID int64) ([]agent.AgentMessage, error)
	// RunActivity is the admin-only activity fact read (v0.2.4-D T10).
	RunActivity(ctx context.Context, runID int64) ([]agent.Activity, error)
}

// AgentCenterHandler serves the logged-in Agent center read API. The catalog,
// run list and quality projections carry no prompts or transcripts; the run
// transcript route is a separate admin-only surface (v0.2.4-D T8, SC-D3).
type AgentCenterHandler struct {
	service AgentCenterService
}

// NewAgentCenterHandler builds the Agent center read handler.
func NewAgentCenterHandler(service AgentCenterService) *AgentCenterHandler {
	return &AgentCenterHandler{service: service}
}

var _ AgentCenterService = (*agentapp.AgentCenterService)(nil)

// Catalog GET /api/agent-center/catalog (any logged-in user).
func (h *AgentCenterHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent center service is not configured"))
		return
	}
	catalog, err := h.service.Catalog(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAgentCatalogDTO(catalog))
}

// Runs GET /api/agent-center/runs (any logged-in user).
func (h *AgentCenterHandler) Runs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	filter, err := parseRunFilter(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent center service is not configured"))
		return
	}
	views, hasMore, err := h.service.Runs(r.Context(), filter)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]agentRunDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toAgentRunDTO(view))
	}
	cursor := ""
	if hasMore && len(items) > 0 {
		cursor = items[len(items)-1].ID
	}
	httpx.List(w, http.StatusOK, items, cursor, hasMore)
}

// Quality GET /api/agent-center/quality (any logged-in user).
func (h *AgentCenterHandler) Quality(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent center service is not configured"))
		return
	}
	view, err := h.service.Quality(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAgentCenterQualityDTO(view))
}

// RunTranscript GET /api/agent-center/runs/{id}/transcript (admin only,
// v0.2.4-D T8/SC-D3). It returns the run's current transcript snapshot including
// rows still streaming; the live increments arrive on the run:{id} event
// channel, which is gated for admins on both generic event endpoints as well.
// The admin gate mirrors ContextPacketHandler.Latest: the payload is the model
// transcript, so it belongs to the management surface, not the ordinary run
// metadata view (constitution Constraints A.1, Q08=C).
func (h *AgentCenterHandler) RunTranscript(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	runID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent center service is not configured"))
		return
	}
	messages, err := h.service.RunTranscript(r.Context(), runID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]agentapp.TranscriptMessage, 0, len(messages))
	for _, message := range messages {
		items = append(items, agentapp.NewTranscriptMessage(message))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// RunActivity GET /api/agent-center/runs/{id}/activity (admin only,
// v0.2.4-D T10/SC-D5). It returns the run's coarse fact ledger — one row per tool
// call and per compaction boundary, keyed (run_id, seq) — which is the internal
// audit/debug read of agent_activity and deliberately separate from the
// transcript (R-D6). The admin gate mirrors RunTranscript: the ledger is a
// management surface, not part of the ordinary run metadata view (constitution
// Constraints A.1, Q08=C).
func (h *AgentCenterHandler) RunActivity(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	runID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("agent center service is not configured"))
		return
	}
	activities, err := h.service.RunActivity(r.Context(), runID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]agentActivityDTO, 0, len(activities))
	for _, activity := range activities {
		items = append(items, toAgentActivityDTO(activity))
	}
	httpx.JSON(w, http.StatusOK, items)
}

/* -------------------------------- parsing -------------------------------- */

func parseRunFilter(r *http.Request) (agentapp.RunFilter, error) {
	query := r.URL.Query()
	filter := agentapp.RunFilter{
		ProfileKey: strings.TrimSpace(query.Get("profile_key")),
		Status:     strings.TrimSpace(query.Get("status")),
	}
	if filter.ProfileKey != "" && !agentProfileKeyPattern.MatchString(filter.ProfileKey) {
		return agentapp.RunFilter{}, platform.Invalid("profile_key", "must match ^[a-z][a-z0-9_]{0,63}$")
	}
	if filter.Status != "" && !agent.AgentRunStatus(filter.Status).Valid() {
		return agentapp.RunFilter{}, platform.Invalid("status", "must be one of running, done, failed, aborted, interrupted")
	}

	versionID, err := parseOptionalPositiveID("version_id", query.Get("version_id"))
	if err != nil {
		return agentapp.RunFilter{}, err
	}
	filter.VersionID = versionID

	beforeID, err := parseOptionalPositiveID("before", query.Get("before"))
	if err != nil {
		return agentapp.RunFilter{}, err
	}
	filter.BeforeID = beforeID

	limit := agentapp.DefaultAgentRunLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > agentapp.MaxAgentRunLimit {
			return agentapp.RunFilter{}, platform.Invalid("limit", "must be between 1 and 100")
		}
		limit = parsed
	}
	filter.Limit = limit
	return filter, nil
}

func parseOptionalPositiveID(field, raw string) (*int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, platform.Invalid(field, "must be a positive decimal ID")
	}
	return &parsed, nil
}
