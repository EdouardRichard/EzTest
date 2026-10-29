package httpapi

import (
	"errors"
	"net/http"
	"time"

	eventinghttp "github.com/EdouardRichard/EzTest/backend/internal/eventing/interfaces/httpapi"
)

// VersionStreamHandler serves GET /api/versions/{id}/stream. It validates that
// the version exists, fixes the event channel to version:{id} (a client
// ?channel= is ignored, spec §7.7) and delegates the SSE mechanics to the P1
// eventing handler: Last-Event-ID before since, 20s heartbeats, replay,
// stream.reset and live delivery are inherited unchanged.
type VersionStreamHandler struct {
	versions VersionService
	stream   *eventinghttp.SSEHandler
}

// NewVersionStreamHandler returns the version SSE wrapper. versions performs the
// 404 existence check; reader and subscriber are the eventing history and live
// delivery ports; heartbeat is injectable for tests (0 means the 20s default).
func NewVersionStreamHandler(versions VersionService, reader eventinghttp.EventReader, subscriber eventinghttp.EventSubscriber, heartbeat time.Duration) *VersionStreamHandler {
	return &VersionStreamHandler{
		versions: versions,
		stream:   eventinghttp.NewSSEHandler(reader, subscriber, heartbeat),
	}
}

// RegisterRoutes mounts the version stream route. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *VersionStreamHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/stream", h.ServeHTTP)
}

// ServeHTTP validates the version and forwards a copy of the request whose
// channel parameter is forced to the version channel.
func (h *VersionStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if h.stream == nil {
		WriteServiceError(w, r, errors.New("version: event stream is not configured"))
		return
	}

	forwarded := new(http.Request)
	*forwarded = *r
	query := forwarded.URL.Query()
	// Set overwrites a client-supplied channel, so the channel can never be
	// pointed at another version.
	query.Set("channel", versionChannel(versionID))
	urlCopy := *forwarded.URL
	urlCopy.RawQuery = query.Encode()
	forwarded.URL = &urlCopy
	h.stream.ServeHTTP(w, forwarded)
}
