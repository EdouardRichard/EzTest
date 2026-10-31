package agentapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// TeamRoomService is the application surface the read-only handler needs.
type TeamRoomService interface {
	Read(ctx context.Context, versionID int64, team string, limit int) (agentapp.TeamRoomView, error)
}

// VersionReader reports whether a version exists. It is a local port so the
// agent context never imports the version application package; cmd/eztest
// adapts versionapp.Service to it.
type VersionReader interface {
	Exists(ctx context.Context, id int64) (bool, error)
}

// TeamRoomHandler serves GET /api/versions/{id}/team-rooms/{team}.
type TeamRoomHandler struct {
	svc      TeamRoomService
	versions VersionReader
}

// NewTeamRoom builds the team-room read handler. versions is required: the
// endpoint must return 404 for an unknown version instead of an empty view.
func NewTeamRoom(svc TeamRoomService, versions VersionReader) *TeamRoomHandler {
	return &TeamRoomHandler{svc: svc, versions: versions}
}

var _ TeamRoomService = (*agentapp.TeamRoom)(nil)

// Read GET /api/versions/{id}/team-rooms/{team}?limit=.
func (h *TeamRoomHandler) Read(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	team, err := agent.ParseTeam(r.PathValue("team"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	exists, err := h.versions.Exists(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !exists {
		httpx.Error(w, r, platform.NotFound("version"))
		return
	}
	view, err := h.svc.Read(r.Context(), versionID, string(team), limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTeamRoomDTO(view))
}

func requireActor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return 0, false
	}
	return actor.ID, true
}

func parseLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return agentapp.DefaultTeamRoomLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > agentapp.MaxTeamRoomLimit {
		return 0, platform.Invalid("limit", "must be between 1 and 100")
	}
	return value, nil
}

func toTeamRoomDTO(view agentapp.TeamRoomView) teamRoomDTO {
	posts := make([]teamRoomPostDTO, 0, len(view.Posts))
	for _, post := range view.Posts {
		posts = append(posts, toTeamRoomPostDTO(post))
	}
	var digest *teamRoomDigestDTO
	if view.Digest != nil {
		converted := toTeamRoomDigestDTO(*view.Digest)
		digest = &converted
	}
	return teamRoomDTO{Team: view.Team, Digest: digest, Posts: posts}
}

func toTeamRoomDigestDTO(digest agent.TeamRoomDigest) teamRoomDigestDTO {
	pinned := make([]string, 0, len(digest.PinnedPostIDs))
	for _, id := range digest.PinnedPostIDs {
		pinned = append(pinned, strconv.FormatInt(id, 10))
	}
	return teamRoomDigestDTO{
		Summary:       digest.Summary,
		PinnedPostIDs: pinned,
		TokenEstimate: digest.TokenEstimate,
		Version:       digest.Version,
		UpdatedAt:     formatTime(digest.UpdatedAt),
	}
}

func toTeamRoomPostDTO(post agent.TeamRoomPost) teamRoomPostDTO {
	refs := make([]teamRoomPostRefDTO, 0, len(post.Refs))
	for _, ref := range post.Refs {
		refs = append(refs, teamRoomPostRefDTO{Kind: ref.Kind, ID: ref.ID})
	}
	var agentRunID *string
	if post.AgentRunID != nil {
		formatted := strconv.FormatInt(*post.AgentRunID, 10)
		agentRunID = &formatted
	}
	return teamRoomPostDTO{
		ID:         strconv.FormatInt(post.ID, 10),
		Kind:       string(post.Kind),
		Content:    post.Content,
		Refs:       refs,
		AgentRunID: agentRunID,
		CreatedAt:  formatTime(post.CreatedAt),
	}
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
