package httpapi

import (
	"errors"
	"net/http"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// EventHandler serves GET /api/versions/{id}/events: the flat P3 snapshot of the
// version channel (spec §7.7).
type EventHandler struct {
	versions VersionService
	events   EventSnapshotter
}

// RegisterRoutes mounts the version events route. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *EventHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/events", h.Snapshot)
}

// Snapshot returns the newest persisted events before before_seq in the flat
// {data:[...], snapshot_cursor, has_more} shape. The flat shape is the P3
// difference from the nested P0 snapshot; the EventDTO stays the V1 envelope.
func (h *EventHandler) Snapshot(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if h.versions != nil {
		if _, err := h.versions.Get(r.Context(), versionID); err != nil {
			WriteServiceError(w, r, err)
			return
		}
	}
	limit, err := ParseQueryLimit(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	beforeSeq, err := ParseBeforeSeq(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if h.events == nil {
		WriteServiceError(w, r, errors.New("version: event snapshot reader is not configured"))
		return
	}
	events, cursor, hasMore, err := h.events.Snapshot(r.Context(), versionChannel(versionID), beforeSeq, limit)
	if err != nil {
		// A version that has not published an event yet has no event channel
		// row: that is an empty snapshot, not a missing version (which the
		// existence check above already answered).
		if errors.Is(err, eventing.ErrChannelNotFound) {
			writeJSON(w, http.StatusOK, versionEventSnapshotEnvelope{
				Data: []versionEventDTO{}, SnapshotCursor: 0, HasMore: false,
			})
			return
		}
		WriteServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, versionEventSnapshotEnvelope{
		Data:           newVersionEventDTOs(events),
		SnapshotCursor: cursor,
		HasMore:        hasMore,
	})
}
