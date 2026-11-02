// Package eventingapi exposes the event snapshot and SSE stream endpoints.
package eventingapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
)

const (
	// heartbeatInterval is the SSE keep-alive comment cadence from the design.
	heartbeatInterval = 20 * time.Second
	// replayPageLimit bounds each replay query; the handler loops until the
	// persisted tail is drained.
	replayPageLimit = 100
	// LastEventIDHeader is the standard SSE reconnect cursor header.
	LastEventIDHeader = "Last-Event-ID"
)

// Service is the application surface the event handlers need.
type Service interface {
	Snapshot(ctx context.Context, channel string, after int64, limit int) (eventingapp.Snapshot, error)
	Bounds(ctx context.Context, channel string) (eventing.Bounds, error)
	Subscribe(channel string) (<-chan eventing.Event, func())
}

// Handler serves GET /api/events and GET /api/stream. The server mounts it
// behind bearer authentication; the handlers still return JSON 401s when the
// actor is missing (e.g. when tested standalone).
type Handler struct {
	svc       Service
	heartbeat time.Duration
}

// New builds the handler with the design's 20s heartbeat.
func New(svc Service) *Handler {
	return &Handler{svc: svc, heartbeat: heartbeatInterval}
}

type snapshotResponse struct {
	Data           []eventDTO `json:"data"`
	SnapshotCursor int64      `json:"snapshot_cursor"`
	HasMore        bool       `json:"has_more"`
}

// Snapshot GET /api/events?channel=&after=&limit=.
func (h *Handler) Snapshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := platform.ActorFrom(r.Context()); !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	query := r.URL.Query()
	channel := strings.TrimSpace(query.Get("channel"))
	if channel == "" {
		httpx.Error(w, r, platform.Invalid("channel", "channel is required"))
		return
	}
	if !eventingapp.ValidChannel(channel) {
		httpx.Error(w, r, platform.Invalid("channel", "unsupported event channel"))
		return
	}
	after, err := parseCursor(query.Get("after"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit := pagination.Limit(query.Get("limit"), eventingapp.DefaultSnapshotLimit, eventingapp.MaxSnapshotLimit)

	page, err := h.svc.Snapshot(r.Context(), channel, after, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]eventDTO, 0, len(page.Data))
	for _, event := range page.Data {
		items = append(items, toEventDTO(event))
	}
	cursor := strconv.FormatInt(page.Cursor, 10)
	w.Header().Set("X-Snapshot-Cursor", cursor)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snapshotResponse{Data: items, SnapshotCursor: page.Cursor, HasMore: page.HasMore})
}

// Stream GET /api/stream?channel=&after= with the SSE Last-Event-ID header.
//
// Ordering guarantees: the Hub subscription happens before the first replay
// query, so no event published during replay is lost; persisted and live
// events are de-duplicated by channel cursor (seq).
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	if _, ok := platform.ActorFrom(r.Context()); !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	query := r.URL.Query()
	channel := strings.TrimSpace(query.Get("channel"))
	if channel == "" {
		httpx.Error(w, r, platform.Invalid("channel", "channel is required"))
		return
	}
	if !eventingapp.ValidChannel(channel) {
		httpx.Error(w, r, platform.Invalid("channel", "unsupported event channel"))
		return
	}
	cursor, err := requestCursor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, r, platform.NewError(http.StatusInternalServerError, "internal", "streaming is not supported"))
		return
	}

	// Subscribe before reading the log so replay and live delivery overlap.
	events, cancel := h.svc.Subscribe(channel)
	defer cancel()

	bounds, err := h.svc.Bounds(r.Context(), channel)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	needsReset := false
	replay := false
	if cursor == 0 {
		// No cursor: live from the current tail; history is fetched separately
		// through GET /api/events.
		cursor = bounds.LastSeq
	} else if cursor > bounds.LastSeq || (bounds.FirstSeq > 0 && cursor < bounds.FirstSeq-1) {
		// The cursor is outside the retained range (e.g. after a future P7
		// cleanup); ask the client to reload its snapshot and continue live.
		needsReset = true
		cursor = bounds.LastSeq
	} else {
		replay = true
	}

	var firstPage *eventingapp.Snapshot
	if replay {
		page, err := h.svc.Snapshot(r.Context(), channel, cursor, replayPageLimit)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		firstPage = &page
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Snapshot-Cursor", strconv.FormatInt(bounds.LastSeq, 10))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if needsReset {
		data, err := json.Marshal(map[string]any{
			"channel":         channel,
			"snapshot_cursor": cursor,
			"reason":          "cursor_out_of_range",
		})
		if err != nil {
			return
		}
		if err := writeSSE(w, "", "stream.reset", data); err != nil {
			return
		}
		flusher.Flush()
	} else if firstPage != nil {
		cursor = h.writeReplayPage(w, cursor, *firstPage)
		flusher.Flush()
		if firstPage.HasMore {
			if err := h.replayRemaining(w, r.Context(), channel, &cursor, flusher); err != nil {
				return
			}
		}
	}

	h.live(w, r.Context(), events, &cursor, flusher)
}

// writeReplayPage emits the events of one snapshot page that are newer than
// cursor and returns the advanced cursor.
func (h *Handler) writeReplayPage(w io.Writer, cursor int64, page eventingapp.Snapshot) int64 {
	for _, event := range page.Data {
		if event.Seq <= cursor {
			continue
		}
		if err := writeSSEEvent(w, event); err != nil {
			return cursor
		}
		cursor = event.Seq
	}
	return cursor
}

func (h *Handler) replayRemaining(w io.Writer, ctx context.Context, channel string, cursor *int64, flusher http.Flusher) error {
	for {
		page, err := h.svc.Snapshot(ctx, channel, *cursor, replayPageLimit)
		if err != nil {
			return err
		}
		*cursor = h.writeReplayPage(w, *cursor, page)
		flusher.Flush()
		if !page.HasMore || len(page.Data) == 0 {
			return nil
		}
	}
}

// live streams broadcast events and 20s heartbeat comments until the client
// disconnects. Events at or below the cursor are duplicates of replayed rows.
func (h *Handler) live(w io.Writer, ctx context.Context, events <-chan eventing.Event, cursor *int64, flusher http.Flusher) {
	interval := h.heartbeat
	if interval <= 0 {
		interval = heartbeatInterval
	}
	heartbeat := time.NewTicker(interval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Seq <= *cursor {
				continue
			}
			if err := writeSSEEvent(w, event); err != nil {
				return
			}
			*cursor = event.Seq
			flusher.Flush()
		}
	}
}

func writeSSEEvent(w io.Writer, event eventing.Event) error {
	data, err := json.Marshal(toEventDTO(event))
	if err != nil {
		return err
	}
	return writeSSE(w, strconv.FormatInt(event.Seq, 10), sanitizeEventName(event.Type), data)
}

func writeSSE(w io.Writer, id, event string, data []byte) error {
	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	fmt.Fprintf(&b, "event: %s\n", event)
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func sanitizeEventName(name string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(name)
}

func requestCursor(r *http.Request) (int64, error) {
	if raw := strings.TrimSpace(r.Header.Get(LastEventIDHeader)); raw != "" {
		return parseCursor(raw)
	}
	return parseCursor(r.URL.Query().Get("after"))
}

func parseCursor(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, platform.Invalid("after", "cursor must be a non-negative decimal event sequence")
	}
	return value, nil
}
