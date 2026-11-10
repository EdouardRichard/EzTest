package agentapi

import (
	"context"
	"errors"
	"net/http"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// RegisterAgentCenterRoutes mounts the three P6 Agent center read-only
// operations. The assembly root owns the middleware and mounting call (Task
// 22), so this package stays independent from server. Every route requires a
// logged-in user (spec §4.3.4/§8.5); prompts and transcripts are not exposed.
func RegisterAgentCenterRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *AgentCenterHandler) {
	if mux == nil || auth == nil || h == nil {
		return
	}
	mux.Handle("GET /api/agent-center/catalog", auth(http.HandlerFunc(h.Catalog)))
	mux.Handle("GET /api/agent-center/runs", auth(http.HandlerFunc(h.Runs)))
	mux.Handle("GET /api/agent-center/quality", auth(http.HandlerFunc(h.Quality)))
}

// ContextPacketReader is the narrow read port behind
// GET /api/versions/{id}/context-packets/latest. The production adapter reads
// the latest persisted context_packet of one version (resolving the version via
// agent_run when the stored row only carries run_id); it is wired together with
// context_packet persistence by the P3-T6 runner wiring (Task 48), because the
// existing context_packet row stores the P1 builder shape, not the cache-first
// packet. Tests inject an in-memory fake. It returns ErrContextPacketNotFound
// when the version has no persisted packet, and any other error is surfaced
// as-is.
type ContextPacketReader interface {
	LatestContextPacket(ctx context.Context, versionID int64) (agentcontext.ContextPacket, error)
}

// ContextPacketHandler serves the context_packet read API (spec §6.3). It only
// renders the persisted packet; it never assembles or compacts a new one.
type ContextPacketHandler struct {
	reader ContextPacketReader
}

// NewContextPacketHandler builds the context packet read handler. A nil reader
// is allowed and reported as 503 instead of panicking.
func NewContextPacketHandler(reader ContextPacketReader) *ContextPacketHandler {
	return &ContextPacketHandler{reader: reader}
}

// RegisterContextPacketRoutes mounts the context_packet read API. The assembly
// root owns the mount call and middleware (as with RegisterAgentCenterRoutes),
// so this package stays independent from server. The composition root must pass
// the Task 48 production reader; a nil reader is reported as 503, never mounted
// as a working endpoint.
func RegisterContextPacketRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *ContextPacketHandler) {
	if mux == nil || auth == nil || h == nil {
		return
	}
	mux.Handle("GET /api/versions/{id}/context-packets/latest", auth(http.HandlerFunc(h.Latest)))
}

// Latest GET /api/versions/{id}/context-packets/latest (admin only). The packet
// contains the stable system-prompt prefix and the replayed transcript, so it
// follows the profile prompt read APIs: ordinary logged-in users must not
// receive prompt content (spec R9). It returns the latest persisted cache-first
// context packet of one version, or 200 with a null data body when no packet
// has been recorded yet (G-09: "no packet" is an empty read, not a 404).
func (h *ContextPacketHandler) Latest(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.reader == nil {
		httpx.Error(w, r, platform.Unavailable("context packet reader is not configured"))
		return
	}
	packet, err := h.reader.LatestContextPacket(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, agentcontext.ErrContextPacketNotFound) {
			// Empty read: the version has no persisted packet yet. Answering
			// 200 with a null data body keeps the browser network layer free
			// of spurious 404s (G-09); invalid ids and a missing reader still
			// fail with their typed 4xx/503.
			httpx.JSON(w, http.StatusOK, nil)
			return
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toContextPacketDTO(packet))
}

// contextPacketDTO is the wire projection of the frozen ContextPacket. The
// field names follow spec §6.3 so persisted packets and the read API use one
// vocabulary.
type contextPacketDTO struct {
	PrefixHash         string              `json:"prefix_hash"`
	Segments           []contextSegmentDTO `json:"segments"`
	CachedTokens       int                 `json:"cached_tokens"`
	CacheHit           bool                `json:"cache_hit"`
	InvalidationReason string              `json:"invalidation_reason"`
	BriefingHash       string              `json:"briefing_hash"`
}

// contextSegmentDTO is one ordered packet segment. Content is included because
// this admin-only endpoint is the read API of the persisted packet; the route
// is never part of the ordinary Agent center read model, which never exposes
// prompts or transcripts.
type contextSegmentDTO struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Prefix     bool   `json:"prefix"`
	Tokens     int    `json:"tokens"`
}

func toContextPacketDTO(packet agentcontext.ContextPacket) contextPacketDTO {
	segments := make([]contextSegmentDTO, 0, len(packet.Segments))
	for _, segment := range packet.Segments {
		segments = append(segments, contextSegmentDTO{
			Name:       segment.Name,
			Role:       segment.Role,
			Content:    segment.Content,
			ToolCallID: segment.ToolCallID,
			Prefix:     segment.Prefix,
			Tokens:     segment.Tokens,
		})
	}
	return contextPacketDTO{
		PrefixHash:         packet.PrefixHash,
		Segments:           segments,
		CachedTokens:       packet.CachedTokens,
		CacheHit:           packet.CacheHit,
		InvalidationReason: packet.InvalidationReason,
		BriefingHash:       packet.BriefingHash,
	}
}
