package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

// fakeContextPacketReader is the narrow read port double of the route test.
type fakeContextPacketReader struct {
	packet agentcontext.ContextPacket
	err    error
	calls  int
}

func (f *fakeContextPacketReader) LatestContextPacket(_ context.Context, _ int64) (agentcontext.ContextPacket, error) {
	f.calls++
	if f.err != nil {
		return agentcontext.ContextPacket{}, f.err
	}
	return f.packet, nil
}

// packetAdminGate mirrors RequireAdmin with an in-memory actor: X-Test-Anon
// simulates a missing session and X-Test-Role selects the actor role (admin by
// default), so the test can prove the route is mounted behind the admin gate
// without a database-backed identity service.
func packetAdminGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Anon") != "" {
			httpx.Error(w, r, platform.Unauthenticated())
			return
		}
		role := r.Header.Get("X-Test-Role")
		if role == "" {
			role = "admin"
		}
		if role != "admin" {
			httpx.Error(w, r, platform.Forbidden())
			return
		}
		next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: role})))
	})
}

func contextPacketRequest(t *testing.T, handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestContextPacketRouteAdmin pins the Task 48 composition: the context packet
// read route is mounted behind the admin gate, a non-admin never reaches the
// reader, an unwired reader answers 503 instead of panicking, and the route is
// part of the real server route table (401 without a session, never 404).
func TestContextPacketRouteAdmin(t *testing.T) {
	built, err := agentcontext.NewAssembler().Build(context.Background(), agentcontext.AssemblyRequest{
		System:     "system prompt",
		Invariants: []string{"版本目标"},
		Stage:      "clarifying",
		Briefing:   "version briefing",
	})
	if err != nil {
		t.Fatalf("assemble packet: %v", err)
	}
	reader := &fakeContextPacketReader{packet: built.Packet}
	mux := http.NewServeMux()
	registerContextPacketRoutes(mux, packetAdminGate, reader)

	const path = "/api/versions/7/context-packets/latest"

	admin := contextPacketRequest(t, mux, path, nil)
	if admin.Code != http.StatusOK {
		t.Fatalf("admin status = %d, body = %s", admin.Code, admin.Body.String())
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d, want 1", reader.calls)
	}
	var payload struct {
		Data struct {
			PrefixHash string `json:"prefix_hash"`
			Segments   []struct {
				Name    string `json:"name"`
				Prefix  bool   `json:"prefix"`
				Content string `json:"content"`
			} `json:"segments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(admin.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body %q: %v", admin.Body.String(), err)
	}
	if payload.Data.PrefixHash != built.Packet.PrefixHash {
		t.Fatalf("prefix_hash = %q, want %q", payload.Data.PrefixHash, built.Packet.PrefixHash)
	}
	if len(payload.Data.Segments) != len(built.Packet.Segments) ||
		payload.Data.Segments[0].Name != agentcontext.SegmentPrefix || !payload.Data.Segments[0].Prefix {
		t.Fatalf("segments = %#v", payload.Data.Segments)
	}

	// The admin gate runs before the reader: a logged-in non-admin must not
	// receive the stable system prefix or the replayed transcript (spec R9).
	for _, role := range []string{"member", "user"} {
		reader.calls = 0
		forbidden := contextPacketRequest(t, mux, path, map[string]string{"X-Test-Role": role})
		if forbidden.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, body = %s", role, forbidden.Code, forbidden.Body.String())
		}
		if reader.calls != 0 {
			t.Fatalf("%s reached the reader", role)
		}
	}
	anonymous := contextPacketRequest(t, mux, path, map[string]string{"X-Test-Anon": "1"})
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}

	// An unwired reader is reported as unavailable, never a panic.
	unwired := http.NewServeMux()
	registerContextPacketRoutes(unwired, packetAdminGate, nil)
	unavailable := contextPacketRequest(t, unwired, path, nil)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired reader status = %d, body = %s", unavailable.Code, unavailable.Body.String())
	}

	// Invalid IDs are rejected before the read.
	reader.calls = 0
	bad := contextPacketRequest(t, mux, "/api/versions/not-an-id/context-packets/latest", nil)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid version status = %d", bad.Code)
	}
	if reader.calls != 0 {
		t.Fatalf("invalid version reached the reader")
	}

	// The real server route table carries the route: without a bearer the
	// admin middleware answers 401, which proves the path is mounted (a 404
	// would mean the route table is missing it).
	handler := New(Deps{Version: "0.2.1-task48"})
	mounted := contextPacketRequest(t, handler, path, nil)
	if mounted.Code != http.StatusUnauthorized {
		t.Fatalf("unwired production route status = %d, want 401 (mounted behind admin)", mounted.Code)
	}
}
