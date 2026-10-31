//go:build integration

package eventingapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	eventingapi "github.com/EdouardRichard/EzTest/backend/internal/eventing/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

type streamFrame struct {
	id    string
	event string
	data  string
}

func readStreamFrame(t *testing.T, scanner *bufio.Scanner) streamFrame {
	t.Helper()
	frame := streamFrame{}
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		t.Fatalf("no SSE frame received: %v", scanner.Err())
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "id: "):
			frame.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			frame.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			frame.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return frame
}

func newEventsServer(t *testing.T) (*httptest.Server, *eventingapp.Service) {
	t.Helper()
	pool := testutil.NewDB(t)
	service := eventingapp.New(eventingpg.New(pool), eventingapp.NewHub(32))
	handler := eventingapi.New(service)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/events":
			handler.Snapshot(w, withActor(r))
		case r.URL.Path == "/api/stream":
			handler.Stream(w, withActor(r))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, service
}

func withActor(r *http.Request) *http.Request {
	return r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 1, Role: "user"}))
}

func TestEventsSnapshotAndSSEReplayWithPostgres(t *testing.T) {
	server, service := newEventsServer(t)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		if err := service.Publish(ctx, eventingapp.Event{Channel: "system", Type: fmt.Sprintf("event.%d", i)}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	resp, err := http.Get(server.URL + "/api/events?channel=system&limit=10")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
	var snapshot struct {
		Data           []json.RawMessage `json:"data"`
		SnapshotCursor int64             `json:"snapshot_cursor"`
		HasMore        bool              `json:"has_more"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snapshot.Data) != 2 || snapshot.SnapshotCursor != 2 || snapshot.HasMore {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	streamCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL+"/api/stream?channel=system&after=1", nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", streamResp.StatusCode)
	}
	frame := readStreamFrame(t, bufio.NewScanner(streamResp.Body))
	if frame.id != "2" || frame.event != "event.2" {
		t.Fatalf("replayed frame = %#v", frame)
	}
}

func TestEventsSSELiveAfterCatchingUp(t *testing.T) {
	server, service := newEventsServer(t)
	ctx := context.Background()
	if err := service.Publish(ctx, eventingapp.Event{Channel: "system", Type: "event.1"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	streamCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL+"/api/stream?channel=system&after=1", nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)

	published := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		published <- service.Publish(ctx, eventingapp.Event{Channel: "system", Type: "event.live"})
	}()
	frame := readStreamFrame(t, scanner)
	if err := <-published; err != nil {
		t.Fatalf("live publish: %v", err)
	}
	if frame.id != "2" || frame.event != "event.live" {
		t.Fatalf("live frame = %#v", frame)
	}
}
