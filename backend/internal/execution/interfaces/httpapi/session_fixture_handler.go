package httpapi

import (
	"net/http"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// SessionFixtureView is one saved login-state fixture as the admin listing
// shows it (v0.2.4-F P18, SC-F5): METADATA only — the reuse key, the
// content-addressed storage-state reference and provenance. The state bytes
// never leave the content ledger through this API.
type SessionFixtureView struct {
	ID              string `json:"id"`
	VersionID       string `json:"version_id"`
	SUTID           string `json:"sut_id"`
	EnvironmentID   string `json:"environment_id"`
	Role            string `json:"role"`
	StorageStateRef string `json:"storage_state_ref"`
	CreatedAt       string `json:"created_at"`
	CreatedBy       string `json:"created_by"`
}

// SessionFixtures GET /api/versions/{id}/session-fixtures (admin only). Lists
// the version's session fixture facts newest first (the derived current fixture
// of each reuse key is its newest row), paginated by before/limit. Fixture facts
// are append-only and unbounded, so the listing pages honestly (hasMore +
// nextCursor) rather than silently truncating (constitution art.7.6). A degraded
// assembly (no reader wired) answers 503 instead of panicking.
func (h *Handler) SessionFixtures(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.SessionFixtures == nil {
		httpx.Error(w, r, platform.Unavailable("session fixture listing is not configured"))
		return
	}
	limit, beforeID, err := pageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	fixtures, hasMore, err := h.deps.SessionFixtures.ListSessionFixturesByVersion(ctx, versionID, limit, beforeID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "session_fixture"))
		return
	}
	items := make([]SessionFixtureView, 0, len(fixtures))
	for _, fixture := range fixtures {
		items = append(items, SessionFixtureView{
			ID:              platformid.Format(fixture.ID),
			VersionID:       platformid.Format(fixture.VersionID),
			SUTID:           platformid.Format(fixture.SUTID),
			EnvironmentID:   platformid.Format(fixture.EnvironmentID),
			Role:            fixture.Role,
			StorageStateRef: fixture.StorageStateRef,
			CreatedAt:       fixture.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			CreatedBy:       platformid.Format(fixture.CreatedBy),
		})
	}
	httpx.List(w, http.StatusOK, items, nextCursor(hasMore, len(items), lastSessionFixtureID(fixtures)), hasMore)
}

// lastSessionFixtureID returns the newest-first page's last (lowest) id, the
// before-cursor of the next page (the lastIntentID precedent).
func lastSessionFixtureID(items []execution.SessionFixture) int64 {
	if len(items) == 0 {
		return 0
	}
	return items[len(items)-1].ID
}
