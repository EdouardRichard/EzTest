package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var fixedTS = time.Date(2026, 10, 27, 2, 30, 0, 0, time.UTC)

// TestSSEReplayNoGapAndHeartbeat is the task's SSE acceptance test: replay from
// a since cursor delivers every following event exactly once, the periodic
// heartbeat comment keeps the connection observable, and an event published
// while connected is delivered live.
func TestSSEReplayNoGapAndHeartbeat(t *testing.T) {
	reader := newFakeEventReader(testEvent(1), testEvent(2), testEvent(3))
	hub := infrastructure.NewHub()
	srv := httptest.NewServer(NewSSEHandler(reader, hub, 20*time.Millisecond))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openStream(t, ctx, srv.URL+"/api/stream?channel=system&since=1")
	defer resp.Body.Close()

	assertSSEHeaders(t, resp, "3")
	stream := bufio.NewReader(resp.Body)

	seen := make([]string, 0, 3)
	first := readEventFrame(t, stream)
	assertEventFrame(t, first, 2, "system.test")
	seen = append(seen, first.ID)
	second := readEventFrame(t, stream)
	assertEventFrame(t, second, 3, "system.test")
	seen = append(seen, second.ID)

	if comment := readFrame(t, stream).Comment; comment != "ping" {
		t.Fatalf("heartbeat comment=%q want ping", comment)
	}

	hub.Publish(testEvent(4))
	live := readEventFrame(t, stream)
	assertEventFrame(t, live, 4, "system.test")
	seen = append(seen, live.ID)

	if fmt.Sprint(seen) != fmt.Sprint([]string{"2", "3", "4"}) {
		t.Fatalf("event ids=%v want [2 3 4]", seen)
	}
}

// TestSSELastEventIDTakesPrecedence proves Last-Event-ID wins over since.
func TestSSELastEventIDTakesPrecedence(t *testing.T) {
	reader := newFakeEventReader(testEvent(1), testEvent(2), testEvent(3))
	srv := httptest.NewServer(NewSSEHandler(reader, infrastructure.NewHub(), time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?channel=system&since=3", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "1")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	stream := bufio.NewReader(resp.Body)
	assertEventFrame(t, readEventFrame(t, stream), 2, "system.test")
	assertEventFrame(t, readEventFrame(t, stream), 3, "system.test")
	if reader.gotAfterSeq != 1 {
		t.Fatalf("replay started after seq %d, want header cursor 1 (not since=3)", reader.gotAfterSeq)
	}
}

// TestSSELiveOnlyWithoutCursor proves a connection without since/Last-Event-ID
// does not replay history: it announces the current end via X-Snapshot-Cursor
// and only pushes events published after the subscription.
func TestSSELiveOnlyWithoutCursor(t *testing.T) {
	reader := newFakeEventReader(testEvent(1), testEvent(2), testEvent(3))
	hub := infrastructure.NewHub()
	srv := httptest.NewServer(NewSSEHandler(reader, hub, time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openStream(t, ctx, srv.URL+"/api/stream?channel=system")
	defer resp.Body.Close()

	assertSSEHeaders(t, resp, "3")
	if reader.afterCalls != 0 {
		t.Fatalf("history was queried %d times without a cursor", reader.afterCalls)
	}
	hub.Publish(testEvent(4))
	assertEventFrame(t, readEventFrame(t, bufio.NewReader(resp.Body)), 4, "system.test")
}

// TestSSEDedupesOverlappingLiveEvents proves an event buffered during replay
// whose seq was already delivered from history is skipped.
func TestSSEDedupesOverlappingLiveEvents(t *testing.T) {
	reader := newFakeEventReader(testEvent(1), testEvent(2), testEvent(3))
	hub := infrastructure.NewHub()
	srv := httptest.NewServer(NewSSEHandler(reader, hub, time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openStream(t, ctx, srv.URL+"/api/stream?channel=system&since=3")
	defer resp.Body.Close()
	stream := bufio.NewReader(resp.Body)

	hub.Publish(testEvent(3)) // duplicate of the replay cursor
	hub.Publish(testEvent(4))
	assertEventFrame(t, readEventFrame(t, stream), 4, "system.test")
}

// TestSSEResetOnTooOldCursor proves a cursor that no longer joins the retained
// sequence yields the synthetic stream.reset frame: no id, not persisted, and
// carrying the current snapshot cursor. The stream then continues live.
func TestSSEResetOnTooOldCursor(t *testing.T) {
	reader := newFakeEventReader(testEvent(1), testEvent(2), testEvent(3))
	reader.latest = 9
	reader.listAfterErr = eventing.ErrCursorTooOld
	hub := infrastructure.NewHub()
	srv := httptest.NewServer(NewSSEHandler(reader, hub, time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openStream(t, ctx, srv.URL+"/api/stream?channel=system&since=1")
	defer resp.Body.Close()
	stream := bufio.NewReader(resp.Body)

	reset := readFrame(t, stream)
	if reset.Event != "stream.reset" {
		t.Fatalf("event=%q want stream.reset", reset.Event)
	}
	if reset.ID != "" {
		t.Fatalf("stream.reset must not carry an id, got %q", reset.ID)
	}
	var payload struct {
		Channel        string `json:"channel"`
		SnapshotCursor int64  `json:"snapshot_cursor"`
		Reason         string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(reset.Data), &payload); err != nil {
		t.Fatalf("decode reset data %q: %v", reset.Data, err)
	}
	if payload.Channel != "system" || payload.SnapshotCursor != 9 || payload.Reason != "cursor_too_old" {
		t.Fatalf("reset payload=%+v", payload)
	}

	// After the reset the stream remains live and does not repeat the reset.
	hub.Publish(testEvent(10))
	assertEventFrame(t, readEventFrame(t, stream), 10, "system.test")
}

// TestSSEUnknownChannelStartsEmpty proves subscribing to a channel that has no
// events yet is not an error: the cursor is 0 and the first live event arrives.
func TestSSEUnknownChannelStartsEmpty(t *testing.T) {
	reader := newFakeEventReader()
	reader.latestErr = eventing.ErrChannelNotFound
	hub := infrastructure.NewHub()
	srv := httptest.NewServer(NewSSEHandler(reader, hub, time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openStream(t, ctx, srv.URL+"/api/stream?channel=system")
	defer resp.Body.Close()
	assertSSEHeaders(t, resp, "0")

	hub.Publish(testEvent(1))
	assertEventFrame(t, readEventFrame(t, bufio.NewReader(resp.Body)), 1, "system.test")
}

// TestSSERejectsInvalidCursor covers the request validation before a stream is
// opened: channel and cursor are required to be well formed.
func TestSSERejectsInvalidCursor(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		header  string
		wantMsg string
	}{
		{name: "missing channel", target: "/api/stream", wantMsg: "invalid channel"},
		{name: "invalid since", target: "/api/stream?channel=system&since=nope", wantMsg: "invalid cursor"},
		{name: "negative since", target: "/api/stream?channel=system&since=-1", wantMsg: "invalid cursor"},
		{name: "invalid header", target: "/api/stream?channel=system", header: "nope", wantMsg: "invalid cursor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewSSEHandler(newFakeEventReader(), infrastructure.NewHub(), time.Hour)
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.header != "" {
				req.Header.Set("Last-Event-ID", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assertErrorEnvelope(t, rec, http.StatusBadRequest, platform.CodeInvalidArgument, tc.wantMsg)
		})
	}
}

// TestSnapshotEndpoint proves the snapshot response shape and that before_seq is
// applied to the store.
func TestSnapshotEndpoint(t *testing.T) {
	svc := &fakeSnapshotter{items: []eventing.Event{testEvent(5), testEvent(6)}, cursor: 7, more: true}
	rec := httptest.NewRecorder()
	NewSnapshotHandler(svc).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/stream/events?channel=system&limit=2&before_seq=7", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if svc.gotChannel != "system" || svc.gotBeforeSeq != 7 || svc.gotLimit != 2 {
		t.Fatalf("store got channel=%q before_seq=%d limit=%d", svc.gotChannel, svc.gotBeforeSeq, svc.gotLimit)
	}
	var body struct {
		Data struct {
			Items []struct {
				Seq     int64  `json:"seq"`
				Channel string `json:"channel"`
				Type    string `json:"type"`
			} `json:"items"`
			SnapshotCursor int64 `json:"snapshot_cursor"`
			HasMore        bool  `json:"has_more"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body, err)
	}
	if len(body.Data.Items) != 2 || body.Data.Items[0].Seq != 5 || body.Data.Items[1].Seq != 6 {
		t.Fatalf("items=%+v", body.Data.Items)
	}
	if body.Data.SnapshotCursor != 7 || !body.Data.HasMore {
		t.Fatalf("snapshot_cursor=%d has_more=%v", body.Data.SnapshotCursor, body.Data.HasMore)
	}
}

// TestSnapshotEventEnvelope pins the frozen V1 envelope: every field present,
// absent identifiers null, detail an object and refs four arrays.
func TestSnapshotEventEnvelope(t *testing.T) {
	event := testEvent(1)
	event.Detail = json.RawMessage(`{"index":1}`)
	event.Refs = eventing.Refs{Evidence: []string{"e1"}, Case: []string{}, Requirement: []string{"r1"}, Code: []string{}}
	svc := &fakeSnapshotter{items: []eventing.Event{event}, cursor: 1}

	rec := httptest.NewRecorder()
	NewSnapshotHandler(svc).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/stream/events?channel=system", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	var raw struct {
		Data struct {
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body=%q: %v", rec.Body, err)
	}
	if len(raw.Data.Items) != 1 {
		t.Fatalf("items=%d", len(raw.Data.Items))
	}
	item := raw.Data.Items[0]
	for _, field := range []string{
		"seq", "channel", "type", "version_id", "batch_id", "case_id", "step_id",
		"agent_role", "session_id", "ts", "summary", "detail", "refs",
	} {
		if _, ok := item[field]; !ok {
			t.Fatalf("envelope is missing %q: %s", field, rec.Body)
		}
	}
	for _, field := range []string{"version_id", "batch_id", "case_id", "step_id", "agent_role", "session_id"} {
		if string(item[field]) != "null" {
			t.Fatalf("%s=%s want null", field, item[field])
		}
	}
	var detail map[string]any
	if err := json.Unmarshal(item["detail"], &detail); err != nil {
		t.Fatalf("detail is not an object: %s", item["detail"])
	}
	if detail["index"] != float64(1) {
		t.Fatalf("detail=%s", item["detail"])
	}
	var refs struct {
		Evidence    []string `json:"evidence"`
		Case        []string `json:"case"`
		Requirement []string `json:"requirement"`
		Code        []string `json:"code"`
	}
	if err := json.Unmarshal(item["refs"], &refs); err != nil {
		t.Fatalf("refs=%s: %v", item["refs"], err)
	}
	if fmt.Sprint(refs.Evidence) != "[e1]" || refs.Case == nil || refs.Requirement == nil || refs.Code == nil {
		t.Fatalf("refs=%s want all four arrays with evidence [e1]", item["refs"])
	}
	var ts time.Time
	if err := json.Unmarshal(item["ts"], &ts); err != nil {
		t.Fatalf("ts=%s: %v", item["ts"], err)
	}
	if ts.Location() != time.UTC {
		t.Fatalf("ts=%s is not UTC", item["ts"])
	}
}

// TestSnapshotEmptyItemsIsArray proves an empty page serialises as [] and not
// null, so the frontend can iterate unconditionally.
func TestSnapshotEmptyItemsIsArray(t *testing.T) {
	svc := &fakeSnapshotter{items: nil, cursor: 4}
	rec := httptest.NewRecorder()
	NewSnapshotHandler(svc).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/stream/events?channel=system", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("body=%s want an empty items array", rec.Body)
	}
}

// TestSnapshotDefaultsAndErrors covers limit/before_seq parsing, the default
// limit, the unknown-channel 404 and the malformed channel 400.
func TestSnapshotDefaultsAndErrors(t *testing.T) {
	svc := &fakeSnapshotter{}
	rec := httptest.NewRecorder()
	NewSnapshotHandler(svc).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/stream/events?channel=system", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if svc.gotLimit != platform.DefaultLimit || svc.gotBeforeSeq != 0 {
		t.Fatalf("default limit=%d before_seq=%d want %d/0", svc.gotLimit, svc.gotBeforeSeq, platform.DefaultLimit)
	}

	tests := []struct {
		name       string
		target     string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "missing channel", target: "/api/stream/events", wantStatus: http.StatusBadRequest, wantCode: platform.CodeInvalidArgument},
		{name: "long channel", target: "/api/stream/events?channel=" + strings.Repeat("a", 65), wantStatus: http.StatusBadRequest, wantCode: platform.CodeInvalidArgument},
		{name: "invalid limit", target: "/api/stream/events?channel=system&limit=0", wantStatus: http.StatusBadRequest, wantCode: platform.CodeInvalidArgument},
		{name: "invalid before_seq", target: "/api/stream/events?channel=system&before_seq=nope", wantStatus: http.StatusBadRequest, wantCode: platform.CodeInvalidArgument},
		{name: "unknown channel", target: "/api/stream/events?channel=nope", err: eventing.ErrChannelNotFound, wantStatus: http.StatusNotFound, wantCode: platform.CodeNotFound},
		{name: "store failure", target: "/api/stream/events?channel=system", err: errors.New("boom"), wantStatus: http.StatusInternalServerError, wantCode: platform.CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeSnapshotter{err: tc.err}
			rec := httptest.NewRecorder()
			NewSnapshotHandler(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
			assertErrorEnvelopeCode(t, rec, tc.wantStatus, tc.wantCode)
		})
	}
}

// TestTestPublisherPublishesEvents proves the admin test endpoint publishes the
// requested number of system.test events and records one audit entry carrying
// the actor and request metadata.
func TestTestPublisherPublishesEvents(t *testing.T) {
	publisher := &fakePublisher{}
	auditor := &fakeAuditRecorder{}
	handler := NewTestPublisher(publisher, auditor, true)
	handler.now = func() time.Time { return fixedTS }

	ctx := auditapp.WithActorID(auditapp.WithMeta(context.Background(), auditapp.RequestMeta{
		RequestID: "req_sse_1234",
		IP:        "192.0.2.1",
		UserAgent: "eztest-sse-test",
	}), 1)
	body := `{"count":3,"summary":"admin test event","detail":{"index":1}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/stream/test-events", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var response struct {
		Data struct {
			Seqs []int64 `json:"seqs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("body=%q: %v", rec.Body, err)
	}
	if fmt.Sprint(response.Data.Seqs) != fmt.Sprint([]int64{1, 2, 3}) {
		t.Fatalf("seqs=%v want [1 2 3]", response.Data.Seqs)
	}
	if len(publisher.published) != 3 {
		t.Fatalf("published=%d want 3", len(publisher.published))
	}
	for _, event := range publisher.published {
		if event.Channel != "system" || event.Type != "system.test" {
			t.Fatalf("event channel/type=%q/%q", event.Channel, event.Type)
		}
		if event.Summary != "admin test event" {
			t.Fatalf("summary=%q", event.Summary)
		}
		var detail map[string]any
		if err := json.Unmarshal(event.Detail, &detail); err != nil || detail["index"] != float64(1) {
			t.Fatalf("detail=%s err=%v", event.Detail, err)
		}
		if !event.TS.Equal(fixedTS) {
			t.Fatalf("ts=%v want %v", event.TS, fixedTS)
		}
	}
	if len(auditor.entries) != 1 {
		t.Fatalf("audit entries=%d want 1", len(auditor.entries))
	}
	entry := auditor.entries[0]
	if entry.Action != AuditActionTestPublished {
		t.Fatalf("audit action=%q want %q", entry.Action, AuditActionTestPublished)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 1 {
		t.Fatalf("audit actor=%v want 1", entry.ActorUserID)
	}
	if entry.RequestID != "req_sse_1234" || entry.IP != "192.0.2.1" || entry.UserAgent != "eztest-sse-test" {
		t.Fatalf("audit meta=(%q,%q,%q)", entry.RequestID, entry.IP, entry.UserAgent)
	}
	if entry.Metadata["count"] != 3 {
		t.Fatalf("audit metadata=%v", entry.Metadata)
	}
}

// TestTestPublisherCountBounds proves count is validated to 1-100, an omitted
// count defaults to 1, and the endpoint is absent when test endpoints are
// disabled.
func TestTestPublisherCountBounds(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		enabled    bool
		wantStatus int
		wantCount  int
	}{
		{name: "omitted defaults to one", body: `{}`, enabled: true, wantStatus: http.StatusOK, wantCount: 1},
		{name: "one", body: `{"count":1}`, enabled: true, wantStatus: http.StatusOK, wantCount: 1},
		{name: "hundred", body: `{"count":100}`, enabled: true, wantStatus: http.StatusOK, wantCount: 100},
		{name: "zero", body: `{"count":0}`, enabled: true, wantStatus: http.StatusBadRequest},
		{name: "negative", body: `{"count":-1}`, enabled: true, wantStatus: http.StatusBadRequest},
		{name: "too many", body: `{"count":101}`, enabled: true, wantStatus: http.StatusBadRequest},
		{name: "malformed json", body: `{`, enabled: true, wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"count":1,"channel":"system"}`, enabled: true, wantStatus: http.StatusBadRequest},
		{name: "disabled", body: `{"count":1}`, enabled: false, wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &fakePublisher{}
			rec := httptest.NewRecorder()
			NewTestPublisher(publisher, &fakeAuditRecorder{}, tc.enabled).ServeHTTP(rec,
				httptest.NewRequest(http.MethodPost, "/api/admin/stream/test-events", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tc.wantStatus, rec.Body)
			}
			if len(publisher.published) != tc.wantCount {
				t.Fatalf("published=%d want %d", len(publisher.published), tc.wantCount)
			}
			if tc.wantStatus == http.StatusBadRequest {
				assertErrorEnvelopeCode(t, rec, http.StatusBadRequest, platform.CodeInvalidArgument)
			}
		})
	}
}

// TestTestPublisherEnforcesEnvelopeLimits proves the §7.2 envelope limits are
// rejected as 400 invalid_argument by the test publisher instead of surfacing as
// 500s or being persisted, and that boundary values are still accepted.
func TestTestPublisherEnforcesEnvelopeLimits(t *testing.T) {
	atLimitDetail := `{"pad":"` + strings.Repeat("a", (64<<10)-len(`{"pad":""}`)) + `"}`
	overLimitDetail := `{"pad":"` + strings.Repeat("a", (64<<10)+1-len(`{"pad":""}`)) + `"}`
	tests := []struct {
		name          string
		body          string
		wantStatus    int
		wantPublished int
	}{
		{name: "summary at 500 runes", body: `{"summary":"` + strings.Repeat("通", 500) + `"}`, wantStatus: http.StatusOK, wantPublished: 1},
		{name: "summary over 500 runes", body: `{"summary":"` + strings.Repeat("通", 501) + `"}`, wantStatus: http.StatusBadRequest},
		{name: "detail at 64KiB", body: `{"detail":` + atLimitDetail + `}`, wantStatus: http.StatusOK, wantPublished: 1},
		{name: "detail over 64KiB", body: `{"detail":` + overLimitDetail + `}`, wantStatus: http.StatusBadRequest},
		{name: "detail array", body: `{"detail":[1,2,3]}`, wantStatus: http.StatusBadRequest},
		{name: "detail string", body: `{"detail":"not-an-object"}`, wantStatus: http.StatusBadRequest},
		{name: "detail number", body: `{"detail":42}`, wantStatus: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &fakePublisher{}
			auditor := &fakeAuditRecorder{}
			rec := httptest.NewRecorder()
			NewTestPublisher(publisher, auditor, true).ServeHTTP(rec,
				httptest.NewRequest(http.MethodPost, "/api/admin/stream/test-events", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tc.wantStatus, rec.Body)
			}
			if len(publisher.published) != tc.wantPublished {
				t.Fatalf("published=%d want %d", len(publisher.published), tc.wantPublished)
			}
			if tc.wantStatus == http.StatusBadRequest {
				assertErrorEnvelopeCode(t, rec, http.StatusBadRequest, platform.CodeInvalidArgument)
				if len(auditor.entries) != 0 {
					t.Fatalf("invalid request was audited: %+v", auditor.entries)
				}
			}
		})
	}
}

// TestTestPublisherMapsPublisherValidationError proves a validation failure that
// escapes to the Publisher port is still a 400 invalid_argument, not a 500.
func TestTestPublisherMapsPublisherValidationError(t *testing.T) {
	publisher := &fakePublisher{err: fmt.Errorf("%w: summary is too long", eventing.ErrInvalidEvent)}
	auditor := &fakeAuditRecorder{}
	rec := httptest.NewRecorder()
	NewTestPublisher(publisher, auditor, true).ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/admin/stream/test-events", strings.NewReader(`{"count":1}`)))
	assertErrorEnvelopeCode(t, rec, http.StatusBadRequest, platform.CodeInvalidArgument)
	if len(auditor.entries) != 0 {
		t.Fatalf("invalid request was audited: %+v", auditor.entries)
	}
}

// TestSSELiveGapReplaysMissingPredecessor proves a live broadcast can arrive
// before its predecessor without hiding it: seq 5 delivered before seq 4 makes
// the handler replay the persisted seq 4 from the store before writing 5, and
// the late seq 4 broadcast is deduped.
func TestSSELiveGapReplaysMissingPredecessor(t *testing.T) {
	reader := newFakeEventReader(testEvent(1), testEvent(2), testEvent(3))
	subscriber := newFakeSubscriber()
	srv := httptest.NewServer(NewSSEHandler(reader, subscriber, time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openStream(t, ctx, srv.URL+"/api/stream?channel=system")
	defer resp.Body.Close()
	stream := bufio.NewReader(resp.Body)
	if got := resp.Header.Get("X-Snapshot-Cursor"); got != "3" {
		t.Fatalf("X-Snapshot-Cursor=%q want 3", got)
	}

	// Both events commit after the connection snapshot. Their broadcasts race;
	// simulate the hub delivering the higher seq first.
	reader.appendEvent(testEvent(4), testEvent(5))
	subscriber.send(testEvent(5))
	subscriber.send(testEvent(4))

	assertEventFrame(t, readEventFrame(t, stream), 4, "system.test")
	assertEventFrame(t, readEventFrame(t, stream), 5, "system.test")

	// The stream keeps moving forward: the next live event is delivered, and
	// the late duplicate seq 4 was not re-emitted before it.
	reader.appendEvent(testEvent(6))
	subscriber.send(testEvent(6))
	assertEventFrame(t, readEventFrame(t, stream), 6, "system.test")
}

// TestTestPublisherStoreFailure proves a publish failure is reported as a 500
// and no success audit entry is written.
func TestTestPublisherStoreFailure(t *testing.T) {
	publisher := &fakePublisher{err: errors.New("database down")}
	auditor := &fakeAuditRecorder{}
	rec := httptest.NewRecorder()
	NewTestPublisher(publisher, auditor, true).ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/admin/stream/test-events", strings.NewReader(`{"count":2}`)))
	assertErrorEnvelopeCode(t, rec, http.StatusInternalServerError, platform.CodeInternal)
	if len(auditor.entries) != 0 {
		t.Fatalf("failed publish was audited: %+v", auditor.entries)
	}
}

// TestTestPublisherAuditFailureIsBestEffort proves a failed audit write does
// not turn a completed publish into an error (the events are already durable).
func TestTestPublisherAuditFailureIsBestEffort(t *testing.T) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	publisher := &fakePublisher{}
	auditor := &fakeAuditRecorder{err: errors.New("audit down")}
	rec := httptest.NewRecorder()
	NewTestPublisher(publisher, auditor, true).ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/admin/stream/test-events", strings.NewReader(`{"count":1}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("published=%d", len(publisher.published))
	}
}

// openStream issues the SSE request and returns the live response.
func openStream(t *testing.T, ctx context.Context, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("stream status=%d body=%s", resp.StatusCode, body)
	}
	return resp
}

// assertSSEHeaders checks the streaming headers the client relies on.
func assertSSEHeaders(t *testing.T, resp *http.Response, wantCursor string) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type=%q", ct)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering=%q", got)
	}
	if got := resp.Header.Get("X-Snapshot-Cursor"); got != wantCursor {
		t.Fatalf("X-Snapshot-Cursor=%q want %q", got, wantCursor)
	}
}

// sseFrame is one parsed SSE frame (or a single comment frame).
type sseFrame struct {
	Comment string
	ID      string
	Event   string
	Data    string
}

// readFrame reads one frame from an SSE stream. A comment-only frame
// (": ping") comes back with Comment set and no event/id.
func readFrame(t *testing.T, stream *bufio.Reader) sseFrame {
	t.Helper()
	frame := sseFrame{}
	for {
		line, err := stream.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return frame
		}
		if strings.HasPrefix(line, ":") {
			frame.Comment = strings.TrimSpace(line[1:])
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			frame.ID = value
		case "event":
			frame.Event = value
		case "data":
			if frame.Data == "" {
				frame.Data = value
			} else {
				frame.Data += "\n" + value
			}
		}
	}
}

// readEventFrame reads frames until an event (not a heartbeat comment) arrives.
func readEventFrame(t *testing.T, stream *bufio.Reader) sseFrame {
	t.Helper()
	for {
		frame := readFrame(t, stream)
		if frame.Comment == "" {
			return frame
		}
	}
}

func assertEventFrame(t *testing.T, frame sseFrame, wantSeq int64, wantType string) {
	t.Helper()
	if frame.Event != wantType {
		t.Fatalf("event=%q want %q", frame.Event, wantType)
	}
	if frame.ID != fmt.Sprint(wantSeq) {
		t.Fatalf("id=%q want %d", frame.ID, wantSeq)
	}
	var payload struct {
		Seq     int64  `json:"seq"`
		Channel string `json:"channel"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		t.Fatalf("decode data %q: %v", frame.Data, err)
	}
	if payload.Seq != wantSeq || payload.Channel != "system" || payload.Type != wantType {
		t.Fatalf("payload=%+v want seq=%d channel=system type=%s", payload, wantSeq, wantType)
	}
}

func assertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode, wantMessage string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("code=%d want %d body=%s", rec.Code, wantStatus, rec.Body)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body, err)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("code=%q want %q", body.Error.Code, wantCode)
	}
	if !strings.Contains(body.Error.Message, wantMessage) {
		t.Fatalf("message=%q want containing %q", body.Error.Message, wantMessage)
	}
}

func assertErrorEnvelopeCode(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("code=%d want %d body=%s", rec.Code, wantStatus, rec.Body)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body, err)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("code=%q want %q", body.Error.Code, wantCode)
	}
}

// testEvent builds a complete system.test event at seq.
func testEvent(seq int64) eventing.Event {
	return eventing.Event{
		Seq:     seq,
		Channel: "system",
		Type:    "system.test",
		TS:      fixedTS.Add(time.Duration(seq) * time.Second),
		Summary: fmt.Sprintf("event-%d", seq),
		Detail:  json.RawMessage(fmt.Sprintf(`{"index":%d}`, seq)),
		Refs: eventing.Refs{
			Evidence:    []string{},
			Case:        []string{},
			Requirement: []string{},
			Code:        []string{},
		},
	}
}

// fakeEventReader is an in-memory EventReader for handler tests.
type fakeEventReader struct {
	mu           sync.Mutex
	events       []eventing.Event
	latest       int64
	latestErr    error
	listAfterErr error
	afterCalls   int
	gotAfterSeq  int64
	gotLimit     int
}

func newFakeEventReader(events ...eventing.Event) *fakeEventReader {
	reader := &fakeEventReader{events: events}
	if len(events) > 0 {
		reader.latest = events[len(events)-1].Seq
	}
	return reader
}

// appendEvent simulates events committing after a handler has started; it also
// advances the channel's latest seq.
func (f *fakeEventReader) appendEvent(events ...eventing.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, events...)
	if len(events) > 0 {
		f.latest = events[len(events)-1].Seq
	}
}

func (f *fakeEventReader) ListAfter(_ context.Context, _ string, afterSeq int64, limit int) ([]eventing.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterCalls++
	f.gotAfterSeq = afterSeq
	f.gotLimit = limit
	if f.listAfterErr != nil {
		return nil, f.listAfterErr
	}
	var events []eventing.Event
	for _, event := range f.events {
		if event.Seq > afterSeq {
			events = append(events, event)
			if len(events) == limit {
				break
			}
		}
	}
	return events, nil
}

func (f *fakeEventReader) LatestSeq(context.Context, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latest, f.latestErr
}

// fakeSubscriber is a controllable EventSubscriber: the test decides the order
// in which the broker delivers live events.
type fakeSubscriber struct {
	events chan eventing.Event
}

func newFakeSubscriber() *fakeSubscriber {
	return &fakeSubscriber{events: make(chan eventing.Event, 16)}
}

func (s *fakeSubscriber) Subscribe(string) (<-chan eventing.Event, func()) {
	return s.events, func() {}
}

func (s *fakeSubscriber) send(events ...eventing.Event) {
	for _, event := range events {
		s.events <- event
	}
}

// fakeSnapshotter is an in-memory Snapshotter for handler tests.
type fakeSnapshotter struct {
	items        []eventing.Event
	cursor       int64
	more         bool
	err          error
	gotChannel   string
	gotBeforeSeq int64
	gotLimit     int
}

func (f *fakeSnapshotter) Snapshot(_ context.Context, channel string, beforeSeq int64, limit int) ([]eventing.Event, int64, bool, error) {
	f.gotChannel = channel
	f.gotBeforeSeq = beforeSeq
	f.gotLimit = limit
	if f.err != nil {
		return nil, 0, false, f.err
	}
	return f.items, f.cursor, f.more, nil
}

// fakePublisher records published events and assigns sequential seqs.
type fakePublisher struct {
	published []eventing.NewEvent
	err       error
}

func (f *fakePublisher) Publish(_ context.Context, event eventing.NewEvent) (eventing.Event, error) {
	if f.err != nil {
		return eventing.Event{}, f.err
	}
	f.published = append(f.published, event)
	return eventing.Event{Seq: int64(len(f.published)), Channel: event.Channel, Type: event.Type, TS: event.TS}, nil
}

// fakeAuditRecorder captures audit entries for handler tests.
type fakeAuditRecorder struct {
	entries []auditdomain.Entry
	err     error
}

func (r *fakeAuditRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return r.err
}

func (r *fakeAuditRecorder) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return nil, false, nil
}

func (r *fakeAuditRecorder) ListFiltered(context.Context, auditdomain.ListFilter, int, int64) ([]auditdomain.Entry, bool, error) {
	return nil, false, nil
}
