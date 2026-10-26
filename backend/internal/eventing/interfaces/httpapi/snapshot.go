package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// errInvalidBeforeSeq marks a before_seq that is not a non-negative integer.
var errInvalidBeforeSeq = errors.New("httpapi: invalid before_seq")

// Snapshotter is the read use case consumed by the snapshot endpoint. The
// eventing application Service implements it.
type Snapshotter interface {
	Snapshot(ctx context.Context, channel string, beforeSeq int64, limit int) ([]eventing.Event, int64, bool, error)
}

// SnapshotHandler serves GET /api/stream/events: the newest events before a
// cursor plus the channel's current seq, so a client can render history and then
// connect the SSE stream without a gap.
type SnapshotHandler struct {
	svc Snapshotter
}

// NewSnapshotHandler returns the snapshot handler for svc.
func NewSnapshotHandler(svc Snapshotter) *SnapshotHandler {
	return &SnapshotHandler{svc: svc}
}

// ServeHTTP returns the event page in the streaming-specific shape:
// {"data":{"items":[...],"snapshot_cursor":N,"has_more":bool}}.
func (h *SnapshotHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	channel, ok := channelFromQuery(w, r)
	if !ok {
		return
	}
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid limit")
		return
	}
	beforeSeq, err := parseBeforeSeq(r.URL.Query().Get("before_seq"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid before_seq")
		return
	}

	events, cursor, more, err := h.svc.Snapshot(r.Context(), channel, beforeSeq, limit)
	if err != nil {
		if errors.Is(err, eventing.ErrChannelNotFound) {
			platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "channel not found")
			return
		}
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}
	items := make([]eventPayload, len(events))
	for i, event := range events {
		items[i] = newEventPayload(event)
	}
	writeJSON(w, http.StatusOK, snapshotEnvelope{
		Data: snapshotData{
			Items:          items,
			SnapshotCursor: cursor,
			HasMore:        more,
		},
	})
}

// parseBeforeSeq parses the optional before_seq filter. An empty value means the
// latest events; 0 is accepted as the same "no upper bound".
func parseBeforeSeq(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errInvalidBeforeSeq
	}
	return value, nil
}

// snapshotEnvelope is the event-stream-specific snapshot shape.
type snapshotEnvelope struct {
	Data snapshotData `json:"data"`
}

type snapshotData struct {
	Items          []eventPayload `json:"items"`
	SnapshotCursor int64          `json:"snapshot_cursor"`
	HasMore        bool           `json:"has_more"`
}
