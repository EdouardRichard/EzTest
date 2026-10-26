// Package httpapi is the eventing context's HTTP interface: the SSE live
// stream, the event snapshot endpoint and the admin-only test publisher. The
// package only talks to the application/domain ports; the server composition
// root mounts the routes and applies authentication.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// DefaultHeartbeat is the SSE comment interval from the design.
	DefaultHeartbeat = 20 * time.Second
	// sseReplayBatch bounds one history query while a reconnecting client
	// catches up; replayHistory loops until it reaches the live edge.
	sseReplayBatch = 500
	// maxChannelLength is the documented channel-name limit.
	maxChannelLength = 64
)

// EventReader is the history port the SSE handler replays from. The postgres
// event store implements it.
type EventReader interface {
	ListAfter(ctx context.Context, channel string, afterSeq int64, limit int) ([]eventing.Event, error)
	LatestSeq(ctx context.Context, channel string) (int64, error)
}

// EventSubscriber is the live-delivery port. The in-process hub (or the
// application Service) implements it.
type EventSubscriber interface {
	Subscribe(channel string) (<-chan eventing.Event, func())
}

// SSEHandler serves GET /api/stream as a Server-Sent Events stream.
type SSEHandler struct {
	reader     EventReader
	subscriber EventSubscriber
	heartbeat  time.Duration
}

// NewSSEHandler returns the stream handler. heartbeat is injectable for tests;
// a non-positive value means DefaultHeartbeat.
func NewSSEHandler(reader EventReader, subscriber EventSubscriber, heartbeat time.Duration) *SSEHandler {
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}
	return &SSEHandler{reader: reader, subscriber: subscriber, heartbeat: heartbeat}
}

// ServeHTTP opens the stream: it subscribes to the hub first, then replays the
// persisted history after the requested cursor and finally forwards live events.
// An overlapping event (already covered by replay) is skipped by seq, so a
// reconnect never duplicates an event. The response headers and every frame are
// flushed immediately.
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	channel, ok := channelFromQuery(w, r)
	if !ok {
		return
	}
	cursor, cursorSpecified, ok := cursorFromRequest(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "streaming is not supported")
		return
	}
	ctx := r.Context()

	// Subscribe before reading history: events committed while the replay
	// queries run are buffered by the hub and delivered after the history, so
	// the snapshot-to-stream window loses nothing.
	events, unsubscribe := h.subscriber.Subscribe(channel)
	defer unsubscribe()

	latest, err := h.reader.LatestSeq(ctx, channel)
	switch {
	case err == nil:
	case errors.Is(err, eventing.ErrChannelNotFound):
		latest = 0
	default:
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	header.Set("X-Snapshot-Cursor", strconv.FormatInt(latest, 10))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if cursorSpecified {
		cursor, err = h.replayHistory(ctx, w, flusher, channel, cursor)
		if err != nil {
			if !errors.Is(err, eventing.ErrCursorTooOld) {
				return
			}
			// The cursor cannot join the retained sequence: tell the client to
			// fetch a fresh snapshot instead of silently skipping events. The
			// reset frame is synthetic: no id, persisted nowhere.
			if refreshed, latestErr := h.reader.LatestSeq(ctx, channel); latestErr == nil {
				latest = refreshed
			}
			if err := writeSSEReset(w, flusher, channel, latest); err != nil {
				return
			}
			cursor = latest
		}
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				// The hub disconnected this slow consumer. The client reconnects
				// with Last-Event-ID and replays from the store.
				return
			}
			if event.Seq <= cursor {
				continue
			}
			if err := writeSSEEvent(w, flusher, event); err != nil {
				return
			}
			cursor = event.Seq
		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// replayHistory sends every persisted event after cursor in batches. It returns
// the new cursor. ErrCursorTooOld is passed to the caller, which turns it into a
// stream.reset frame.
func (h *SSEHandler) replayHistory(ctx context.Context, w io.Writer, flusher http.Flusher, channel string, cursor int64) (int64, error) {
	for {
		events, err := h.reader.ListAfter(ctx, channel, cursor, sseReplayBatch)
		if err != nil {
			return cursor, err
		}
		if len(events) == 0 {
			return cursor, nil
		}
		for _, event := range events {
			if event.Seq <= cursor {
				continue
			}
			if err := writeSSEEvent(w, flusher, event); err != nil {
				return cursor, err
			}
			cursor = event.Seq
		}
		if len(events) < sseReplayBatch {
			return cursor, nil
		}
	}
}

// channelFromQuery validates the channel query parameter. On failure it writes
// the 400 response and reports false.
func channelFromQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	channel := r.URL.Query().Get("channel")
	if channel == "" || len(channel) > maxChannelLength {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid channel")
		return "", false
	}
	return channel, true
}

// cursorFromRequest resolves the replay cursor. Last-Event-ID takes precedence
// over since; neither means "live only". specified distinguishes an explicit
// since=0 (replay everything) from no cursor at all.
func cursorFromRequest(w http.ResponseWriter, r *http.Request) (cursor int64, specified bool, ok bool) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("since"))
	}
	if raw == "" {
		return 0, false, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid cursor")
		return 0, false, false
	}
	return value, true, true
}

// writeSSEEvent writes one id/event/data frame and flushes it. Event types come
// from the domain, but stripping line breaks keeps them from breaking the frame
// if a future publisher writes an unexpected type.
func writeSSEEvent(w io.Writer, flusher http.Flusher, event eventing.Event) error {
	payload, err := json.Marshal(newEventPayload(event))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n",
		event.Seq, sanitizeSSEField(event.Type), payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// writeSSEReset writes the synthetic stream.reset control frame. It deliberately
// has no id (it is not a persisted event) and is never stored in `events`.
func writeSSEReset(w io.Writer, flusher http.Flusher, channel string, snapshotCursor int64) error {
	data, err := json.Marshal(struct {
		Channel        string `json:"channel"`
		SnapshotCursor int64  `json:"snapshot_cursor"`
		Reason         string `json:"reason"`
	}{
		Channel:        channel,
		SnapshotCursor: snapshotCursor,
		Reason:         "cursor_too_old",
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: stream.reset\ndata: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func sanitizeSSEField(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, value)
}

// eventPayload is the frozen V1 event envelope. It is shared by the SSE data
// line and the snapshot items.
type eventPayload struct {
	Seq       int64           `json:"seq"`
	Channel   string          `json:"channel"`
	Type      string          `json:"type"`
	VersionID *string         `json:"version_id"`
	BatchID   *string         `json:"batch_id"`
	CaseID    *string         `json:"case_id"`
	StepID    *string         `json:"step_id"`
	AgentRole *string         `json:"agent_role"`
	SessionID *string         `json:"session_id"`
	TS        time.Time       `json:"ts"`
	Summary   string          `json:"summary"`
	Detail    json.RawMessage `json:"detail"`
	Refs      refsPayload     `json:"refs"`
}

// refsPayload always carries the four arrays; absent references are [].
type refsPayload struct {
	Evidence    []string `json:"evidence"`
	Case        []string `json:"case"`
	Requirement []string `json:"requirement"`
	Code        []string `json:"code"`
}

// newEventPayload maps a domain event to the frozen API envelope.
func newEventPayload(event eventing.Event) eventPayload {
	detail := event.Detail
	if len(strings.TrimSpace(string(detail))) == 0 || !json.Valid(detail) {
		detail = json.RawMessage(`{}`)
	}
	return eventPayload{
		Seq:       event.Seq,
		Channel:   event.Channel,
		Type:      event.Type,
		VersionID: event.VersionID,
		BatchID:   event.BatchID,
		CaseID:    event.CaseID,
		StepID:    event.StepID,
		AgentRole: event.AgentRole,
		SessionID: event.SessionID,
		TS:        event.TS.UTC(),
		Summary:   event.Summary,
		Detail:    detail,
		Refs: refsPayload{
			Evidence:    nonNilStrings(event.Refs.Evidence),
			Case:        nonNilStrings(event.Refs.Case),
			Requirement: nonNilStrings(event.Refs.Requirement),
			Code:        nonNilStrings(event.Refs.Code),
		},
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// writeJSON writes a success response in the shared `{"data": ...}` style.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
