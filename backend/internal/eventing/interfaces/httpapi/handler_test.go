package eventingapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var fixedTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func testEvent(seq int64, eventType string) eventing.Event {
	actor := int64(9001)
	return eventing.Event{
		Channel:   "system",
		Seq:       seq,
		Type:      eventType,
		Scope:     map[string]any{"version_id": "1001"},
		Payload:   map[string]any{"title": "demo"},
		ActorID:   &actor,
		RequestID: "req_abc",
		CreatedAt: fixedTime,
	}
}

type fakeService struct {
	mu            sync.Mutex
	snapshotFn    func(context.Context, string, int64, int) (eventingapp.Snapshot, error)
	snapshotCalls int
	snapshotCh    string
	snapshotAfter int64
	snapshotLimit int
	snapshotOrder []string
	bounds        eventing.Bounds
	boundsErr     error
	subCh         chan eventing.Event
	subscribed    bool
	subChannel    string
	cancelCalls   int
}

func newFakeService() *fakeService {
	return &fakeService{bounds: eventing.Bounds{FirstSeq: 1, LastSeq: 10}, subCh: make(chan eventing.Event)}
}

func (f *fakeService) Snapshot(ctx context.Context, channel string, after int64, limit int) (eventingapp.Snapshot, error) {
	f.mu.Lock()
	f.snapshotCalls++
	f.snapshotCh, f.snapshotAfter, f.snapshotLimit = channel, after, limit
	f.snapshotOrder = append(f.snapshotOrder, "snapshot")
	fn := f.snapshotFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, channel, after, limit)
	}
	return eventingapp.Snapshot{}, nil
}

func (f *fakeService) Subscribe(channel string) (<-chan eventing.Event, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribed = true
	f.subChannel = channel
	return f.subCh, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cancelCalls++
	}
}

func (f *fakeService) Bounds(context.Context, string) (eventing.Bounds, error) {
	return f.bounds, f.boundsErr
}

func (f *fakeService) snapshotArgs() (calls int, channel string, after int64, limit int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshotCalls, f.snapshotCh, f.snapshotAfter, f.snapshotLimit
}

func (f *fakeService) subscription() (bool, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribed, f.subChannel, f.cancelCalls
}

func (f *fakeService) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.snapshotOrder...)
}

func actorRequest(r *http.Request, id int64) *http.Request {
	return r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: id, Role: "user"}))
}

type sseFrame struct {
	id      string
	event   string
	data    string
	comment string
}

func readFrame(t *testing.T, scanner *bufio.Scanner) sseFrame {
	t.Helper()
	frame := sseFrame{}
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		t.Fatalf("stream ended before an SSE frame was received (err=%v)", scanner.Err())
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "id: "):
			frame.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			frame.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			frame.data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, ":"):
			frame.comment = line
		}
	}
	return frame
}

// startStream starts an SSE request against a handler that injects an actor and
// returns a scanner plus a cleanup function.
func startStream(t *testing.T, handler *Handler, path string, headers map[string]string) (*bufio.Scanner, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.Stream(w, actorRequest(r, 1))
	}))
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("new request: %v", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		resp.Body.Close()
		server.Close()
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	cleanup := func() {
		cancel()
		_ = resp.Body.Close()
		server.Close()
	}
	return bufio.NewScanner(resp.Body), cleanup
}

func TestSnapshotReturnsCursorPage(t *testing.T) {
	svc := newFakeService()
	svc.snapshotFn = func(context.Context, string, int64, int) (eventingapp.Snapshot, error) {
		return eventingapp.Snapshot{Data: []eventing.Event{testEvent(1, "settings.updated"), testEvent(2, "version.created")}, Cursor: 7, HasMore: true}, nil
	}
	handler := New(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/events?channel=system&after=1&limit=2", nil)
	req = actorRequest(req, 3)
	rec := httptest.NewRecorder()

	handler.Snapshot(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Snapshot-Cursor"); got != "7" {
		t.Fatalf("X-Snapshot-Cursor = %q", got)
	}
	var body struct {
		Data           []eventDTO `json:"data"`
		SnapshotCursor string     `json:"snapshot_cursor"`
		HasMore        bool       `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 || body.SnapshotCursor != "7" || !body.HasMore {
		t.Fatalf("body = %#v", body)
	}
	if body.Data[0].Seq != "1" || body.Data[0].Channel != "system" || body.Data[0].Type != "settings.updated" {
		t.Fatalf("event = %#v", body.Data[0])
	}
	if body.Data[0].ActorID != "9001" || body.Data[0].RequestID != "req_abc" || body.Data[0].CreatedAt == "" {
		t.Fatalf("event envelope = %#v", body.Data[0])
	}
	if calls, channel, after, limit := svc.snapshotArgs(); calls != 1 || channel != "system" || after != 1 || limit != 2 {
		t.Fatalf("args = %d/%q/%d/%d", calls, channel, after, limit)
	}
}

func TestSnapshotValidatesQuery(t *testing.T) {
	svc := newFakeService()
	handler := New(svc)
	cases := []string{
		"/api/events",
		"/api/events?channel=bogus",
		"/api/events?channel=version:0",
		"/api/events?channel=system&after=-1",
		"/api/events?channel=system&after=abc",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = actorRequest(req, 1)
		rec := httptest.NewRecorder()
		handler.Snapshot(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
	if calls, _, _, _ := svc.snapshotArgs(); calls != 0 {
		t.Fatalf("service called %d times", calls)
	}
}

func TestSnapshotLimits(t *testing.T) {
	svc := newFakeService()
	handler := New(svc)
	cases := []struct {
		path string
		want int
	}{
		{"/api/events?channel=system", eventingapp.DefaultSnapshotLimit},
		{"/api/events?channel=system&limit=5", 5},
		{"/api/events?channel=system&limit=9999", eventingapp.MaxSnapshotLimit},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req = actorRequest(req, 1)
		rec := httptest.NewRecorder()
		handler.Snapshot(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", tc.path, rec.Code)
		}
		if _, _, _, limit := svc.snapshotArgs(); limit != tc.want {
			t.Fatalf("%s limit = %d, want %d", tc.path, limit, tc.want)
		}
	}
}

func TestSnapshotRequiresAuth(t *testing.T) {
	handler := New(newFakeService())
	rec := httptest.NewRecorder()
	handler.Snapshot(rec, httptest.NewRequest(http.MethodGet, "/api/events?channel=system", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestStreamReplaysAfterCursorBeforeLive(t *testing.T) {
	svc := newFakeService()
	svc.bounds = eventing.Bounds{FirstSeq: 1, LastSeq: 5}
	svc.snapshotFn = func(context.Context, string, int64, int) (eventingapp.Snapshot, error) {
		if subscribed, _, _ := svc.subscription(); !subscribed {
			t.Error("snapshot ran before the hub subscription (subscribe-before-replay violated)")
		}
		return eventingapp.Snapshot{Data: []eventing.Event{testEvent(2, "settings.updated"), testEvent(3, "version.created")}, Cursor: 5}, nil
	}
	handler := New(svc)
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=system&after=1", nil)
	defer cleanup()

	first := readFrame(t, scanner)
	if first.id != "2" || first.event != "settings.updated" {
		t.Fatalf("first frame = %#v", first)
	}
	var payload eventDTO
	if err := json.Unmarshal([]byte(first.data), &payload); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if payload.Seq != "2" {
		t.Fatalf("payload seq = %q", payload.Seq)
	}
	second := readFrame(t, scanner)
	if second.id != "3" || second.event != "version.created" {
		t.Fatalf("second frame = %#v", second)
	}
	if _, channel, after, _ := svc.snapshotArgs(); channel != "system" || after != 1 {
		t.Fatalf("replay args = %q/%d", channel, after)
	}
}

func TestStreamWithoutCursorSkipsReplay(t *testing.T) {
	svc := newFakeService()
	svc.bounds = eventing.Bounds{FirstSeq: 1, LastSeq: 5}
	handler := New(svc)
	handler.heartbeat = 10 * time.Millisecond
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=system", nil)
	defer cleanup()

	frame := readFrame(t, scanner)
	if !strings.HasPrefix(frame.comment, ":") {
		t.Fatalf("expected heartbeat comment, got %#v", frame)
	}
	if calls, _, _, _ := svc.snapshotArgs(); calls != 0 {
		t.Fatalf("snapshot called %d times without a cursor", calls)
	}
	if subscribed, channel, _ := svc.subscription(); !subscribed || channel != "system" {
		t.Fatalf("subscription = %v/%q", subscribed, channel)
	}
}

func TestStreamDedupesReplayAndLive(t *testing.T) {
	svc := newFakeService()
	svc.bounds = eventing.Bounds{FirstSeq: 1, LastSeq: 5}
	svc.subCh = make(chan eventing.Event, 4)
	svc.subCh <- testEvent(2, "version.created")
	svc.subCh <- testEvent(3, "version.created")
	svc.snapshotFn = func(context.Context, string, int64, int) (eventingapp.Snapshot, error) {
		return eventingapp.Snapshot{Data: []eventing.Event{testEvent(2, "version.created")}, Cursor: 5}, nil
	}
	handler := New(svc)
	handler.heartbeat = 10 * time.Millisecond
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=system&after=1", nil)
	defer cleanup()

	first := readFrame(t, scanner)
	if first.id != "2" {
		t.Fatalf("first frame = %#v", first)
	}
	second := readFrame(t, scanner)
	if second.id != "3" {
		t.Fatalf("second frame = %#v", second)
	}
	third := readFrame(t, scanner)
	if !strings.HasPrefix(third.comment, ":") {
		t.Fatalf("duplicate seq was replayed: %#v", third)
	}
}

func TestStreamResetWhenCursorAhead(t *testing.T) {
	svc := newFakeService()
	svc.bounds = eventing.Bounds{FirstSeq: 1, LastSeq: 5}
	handler := New(svc)
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=system&after=9", nil)
	defer cleanup()

	frame := readFrame(t, scanner)
	if frame.event != "stream.reset" || frame.id != "" {
		t.Fatalf("reset frame = %#v", frame)
	}
	var reset struct {
		SnapshotCursor string `json:"snapshot_cursor"`
	}
	if err := json.Unmarshal([]byte(frame.data), &reset); err != nil {
		t.Fatalf("decode reset: %v", err)
	}
	if reset.SnapshotCursor != "5" {
		t.Fatalf("reset cursor = %q", reset.SnapshotCursor)
	}
	if calls, _, _, _ := svc.snapshotArgs(); calls != 0 {
		t.Fatalf("snapshot called %d times after reset", calls)
	}
}

func TestStreamResetWhenCursorTooOld(t *testing.T) {
	svc := newFakeService()
	svc.bounds = eventing.Bounds{FirstSeq: 5, LastSeq: 9}
	handler := New(svc)
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=system&after=2", nil)
	defer cleanup()

	frame := readFrame(t, scanner)
	if frame.event != "stream.reset" || frame.id != "" {
		t.Fatalf("reset frame = %#v", frame)
	}
}

func TestStreamHeartbeatComment(t *testing.T) {
	svc := newFakeService()
	handler := New(svc)
	handler.heartbeat = 10 * time.Millisecond
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=user:42", nil)
	defer cleanup()

	frame := readFrame(t, scanner)
	if !strings.HasPrefix(frame.comment, ":") || !strings.Contains(frame.comment, "heartbeat") {
		t.Fatalf("heartbeat frame = %#v", frame)
	}
}

func TestStreamLastEventIDHeaderWins(t *testing.T) {
	svc := newFakeService()
	svc.bounds = eventing.Bounds{FirstSeq: 1, LastSeq: 10}
	svc.subCh = make(chan eventing.Event, 1)
	svc.subCh <- testEvent(4, "settings.updated")
	handler := New(svc)
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=system&after=1", map[string]string{"Last-Event-ID": "3"})
	defer cleanup()

	frame := readFrame(t, scanner)
	if frame.id != "4" {
		t.Fatalf("frame = %#v", frame)
	}
	if _, _, after, _ := svc.snapshotArgs(); after != 3 {
		t.Fatalf("replay after = %d, want Last-Event-ID 3", after)
	}
}

func TestStreamAuthAndValidation(t *testing.T) {
	handler := New(newFakeService())

	rec := httptest.NewRecorder()
	handler.Stream(rec, httptest.NewRequest(http.MethodGet, "/api/stream?channel=system", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing actor status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("401 content type = %q", got)
	}

	cases := []struct{ path, header string }{
		{"/api/stream", ""},
		{"/api/stream?channel=bogus", ""},
		{"/api/stream?channel=system&after=abc", ""},
		{"/api/stream?channel=system", "abc"},
		{"/api/stream?channel=system", "-3"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.header != "" {
			req.Header.Set("Last-Event-ID", tc.header)
		}
		req = actorRequest(req, 1)
		rec := httptest.NewRecorder()
		handler.Stream(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s header=%q status = %d: %s", tc.path, tc.header, rec.Code, rec.Body.String())
		}
	}
}

func TestStreamBoundsFailure(t *testing.T) {
	svc := newFakeService()
	svc.boundsErr = platform.Unavailable("event log unavailable")
	handler := New(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/stream?channel=system&after=1", nil)
	req = actorRequest(req, 1)
	rec := httptest.NewRecorder()
	handler.Stream(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q", got)
	}
}
