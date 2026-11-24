package agentapi_test

// v0.2.4-D T8 (SC-D3/R-D4, security): access path 3 of the admin-only run
// transcript — the Agent Center snapshot endpoint. It mirrors the context
// packet read API (admin only, because the payload is the model transcript),
// and it must refuse a logged-in ordinary user BEFORE the service is called.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentpkg "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func transcriptRunID() int64 { return 4242 }

func transcriptFixture() *stubAgentCenterService {
	runID := transcriptRunID()
	return &stubAgentCenterService{transcript: []agentpkg.AgentMessage{
		{
			ID: 9101, RunID: &runID, Role: agentpkg.MessageRoleAssistant,
			Parts:       []agentpkg.MessagePart{{Type: "text", Text: "正在生成"}},
			StreamState: agentpkg.MessageStreamStateStreaming,
			CreatedAt:   time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		},
	}}
}

// TestRunTranscriptEndpointIsAdminOnly is the negative path: an ordinary
// logged-in user gets 403 and the transcript is never read.
func TestRunTranscriptEndpointIsAdminOnly(t *testing.T) {
	service := transcriptFixture()
	handler := newAgentCenterServer(t, service)

	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/transcript", map[string]string{"X-Test-Role": "user"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d (%s), want 403", rec.Code, rec.Body.String())
	}
	if service.transcriptCalls != 0 {
		t.Fatalf("a refused non-admin read the transcript %d times", service.transcriptCalls)
	}
}

func TestRunTranscriptEndpointRejectsAnonymous(t *testing.T) {
	service := transcriptFixture()
	handler := newAgentCenterServer(t, service)
	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/transcript", map[string]string{"X-Test-Anon": "1"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if service.transcriptCalls != 0 {
		t.Fatalf("anonymous caller read the transcript %d times", service.transcriptCalls)
	}
}

// TestRunTranscriptEndpointReturnsTheSnapshotForAdmin pins the positive path and
// the wire shape the frontend consumes (the same row projection as the delta).
func TestRunTranscriptEndpointReturnsTheSnapshotForAdmin(t *testing.T) {
	service := transcriptFixture()
	handler := newAgentCenterServer(t, service)

	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/transcript", map[string]string{"X-Test-Role": "admin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if service.transcriptCalls != 1 || service.lastTranscriptRunID != 4242 {
		t.Fatalf("service calls = %d for run %d", service.transcriptCalls, service.lastTranscriptRunID)
	}
	var body struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("data = %s, want one row", rec.Body.String())
	}
	row := body.Data[0]
	for _, key := range []string{"id", "run_id", "role", "parts", "tool_calls", "usage", "stream_state", "created_at"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("transcript row is missing %q: %s", key, rec.Body.String())
		}
	}
	var id, state string
	if err := json.Unmarshal(row["id"], &id); err != nil || id != "9101" {
		t.Fatalf("id = %s, want the decimal string 9101", row["id"])
	}
	if err := json.Unmarshal(row["stream_state"], &state); err != nil || state != agentpkg.MessageStreamStateStreaming {
		t.Fatalf("stream_state = %s, want streaming", row["stream_state"])
	}
}

func TestRunTranscriptEndpointValidatesTheID(t *testing.T) {
	for _, id := range []string{"abc", "0", "-1"} {
		service := transcriptFixture()
		handler := newAgentCenterServer(t, service)
		rec := agentCenterRequest(t, handler, "/api/agent-center/runs/"+id+"/transcript", map[string]string{"X-Test-Role": "admin"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q: status = %d, want 400", id, rec.Code)
		}
		if service.transcriptCalls != 0 {
			t.Fatalf("id %q reached the service", id)
		}
	}
}

func TestRunTranscriptEndpointSurfacesServiceErrors(t *testing.T) {
	service := transcriptFixture()
	service.transcriptErr = platform.Unavailable("agent center reader is not configured")
	handler := newAgentCenterServer(t, service)
	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/transcript", map[string]string{"X-Test-Role": "admin"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%s), want 503", rec.Code, rec.Body.String())
	}
}

// TestAgentCenterMetadataStaysOpenToUsers is the SC-D3 regression: only the
// transcript is admin-only; the run metadata view is unchanged.
func TestAgentCenterMetadataStaysOpenToUsers(t *testing.T) {
	service := &stubAgentCenterService{runs: []agentapp.RunView{{ID: 4242, ProfileKey: "case_executor", Status: "running"}}}
	handler := newAgentCenterServer(t, service)
	rec := agentCenterRequest(t, handler, "/api/agent-center/runs", map[string]string{"X-Test-Role": "user"})
	if rec.Code != http.StatusOK {
		t.Fatalf("runs status = %d (%s), want 200 for an ordinary user", rec.Code, rec.Body.String())
	}
}
