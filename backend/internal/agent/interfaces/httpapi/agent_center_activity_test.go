package agentapi_test

// v0.2.4-D T10 (SC-D5): the admin-only read endpoint of the agent_activity fact
// ledger. It mirrors the transcript route's gate — the ledger is an internal
// audit/debug surface (management plane), while the ordinary run metadata view
// stays open to every logged-in user (constitution Constraints A.1, Q08=C).

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	agentpkg "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func activityFixture() *stubAgentCenterService {
	ended := time.Date(2026, 11, 17, 10, 0, 2, 0, time.UTC)
	return &stubAgentCenterService{activity: []agentpkg.Activity{
		{
			ID: 9201, RunID: 4242, Seq: 1, Type: agentpkg.ActivityTypeToolCall,
			Title:     "requirement.read",
			StartedAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
			EndedAt:   &ended,
			Payload:   map[string]any{"tool": "requirement.read", "duration_ms": 2000},
		},
		{
			ID: 9202, RunID: 4242, Seq: 2, Type: agentpkg.ActivityTypeCompaction,
			Title:     "context compaction (overflow)",
			StartedAt: time.Date(2026, 11, 17, 10, 0, 3, 0, time.UTC),
			Payload:   map[string]any{"trigger": "overflow", "before_tokens": 4000, "after_tokens": 900},
			Error:     "summary model refused the request",
		},
	}}
}

// TestRunActivityEndpointIsAdminOnly is the negative path: an ordinary logged-in
// user gets 403 and the ledger is never read.
func TestRunActivityEndpointIsAdminOnly(t *testing.T) {
	service := activityFixture()
	handler := newAgentCenterServer(t, service)

	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/activity", map[string]string{"X-Test-Role": "user"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d (%s), want 403", rec.Code, rec.Body.String())
	}
	if service.activityCalls != 0 {
		t.Fatalf("a refused non-admin read the ledger %d times", service.activityCalls)
	}
}

func TestRunActivityEndpointRejectsAnonymous(t *testing.T) {
	service := activityFixture()
	handler := newAgentCenterServer(t, service)
	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/activity", map[string]string{"X-Test-Anon": "1"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if service.activityCalls != 0 {
		t.Fatalf("anonymous caller read the ledger %d times", service.activityCalls)
	}
}

// TestRunActivityEndpointReturnsTheLedgerForAdmin pins the positive path and the
// wire shape: decimal-string ids, the fact type, the per-run seq, the coarse
// payload and the optional end/error fields.
func TestRunActivityEndpointReturnsTheLedgerForAdmin(t *testing.T) {
	service := activityFixture()
	handler := newAgentCenterServer(t, service)

	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/activity", map[string]string{"X-Test-Role": "admin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if service.activityCalls != 1 || service.lastActivityRunID != 4242 {
		t.Fatalf("service calls = %d for run %d", service.activityCalls, service.lastActivityRunID)
	}
	var body struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data = %s, want both fact rows", rec.Body.String())
	}
	first := body.Data[0]
	for _, key := range []string{"id", "run_id", "seq", "type", "title", "started_at", "ended_at", "payload", "created_at", "updated_at"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("activity row is missing %q: %s", key, rec.Body.String())
		}
	}
	var id, runID, kind string
	if err := json.Unmarshal(first["id"], &id); err != nil || id != "9201" {
		t.Fatalf("id = %s, want the decimal string 9201", first["id"])
	}
	if err := json.Unmarshal(first["run_id"], &runID); err != nil || runID != "4242" {
		t.Fatalf("run_id = %s, want the decimal string 4242", first["run_id"])
	}
	if err := json.Unmarshal(first["type"], &kind); err != nil || kind != string(agentpkg.ActivityTypeToolCall) {
		t.Fatalf("type = %s, want tool_call", first["type"])
	}
	var seq int
	if err := json.Unmarshal(first["seq"], &seq); err != nil || seq != 1 {
		t.Fatalf("seq = %s, want 1", first["seq"])
	}
	var payload map[string]any
	if err := json.Unmarshal(first["payload"], &payload); err != nil {
		t.Fatalf("payload = %s: %v", first["payload"], err)
	}
	if payload["tool"] != "requirement.read" {
		t.Fatalf("payload.tool = %#v", payload["tool"])
	}
	// A finished call carries ended_at; the failed compaction carries error.
	var ended string
	if err := json.Unmarshal(first["ended_at"], &ended); err != nil || ended == "" {
		t.Fatalf("ended_at = %s, want the recorded end instant", first["ended_at"])
	}
	var failure string
	if err := json.Unmarshal(body.Data[1]["error"], &failure); err != nil || failure == "" {
		t.Fatalf("error = %s, want the recorded failure reason", body.Data[1]["error"])
	}
	// An in-flight fact has no end and no error: the keys are omitted, never
	// fabricated.
	if _, ok := body.Data[1]["ended_at"]; ok {
		t.Fatalf("an unfinished fact rendered ended_at: %s", body.Data[1]["ended_at"])
	}
}

func TestRunActivityEndpointValidatesTheID(t *testing.T) {
	for _, id := range []string{"abc", "0", "-1"} {
		service := activityFixture()
		handler := newAgentCenterServer(t, service)
		rec := agentCenterRequest(t, handler, "/api/agent-center/runs/"+id+"/activity", map[string]string{"X-Test-Role": "admin"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q: status = %d, want 400", id, rec.Code)
		}
		if service.activityCalls != 0 {
			t.Fatalf("id %q reached the service", id)
		}
	}
}

func TestRunActivityEndpointSurfacesServiceErrors(t *testing.T) {
	service := activityFixture()
	service.activityErr = platform.Unavailable("agent center reader is not configured")
	handler := newAgentCenterServer(t, service)
	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/activity", map[string]string{"X-Test-Role": "admin"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%s), want 503", rec.Code, rec.Body.String())
	}
}

// TestRunActivityEndpointIsEmptyNotNull pins the empty ledger: a run with no
// recorded boundary answers 200 with an empty array, never null.
func TestRunActivityEndpointIsEmptyNotNull(t *testing.T) {
	service := &stubAgentCenterService{}
	handler := newAgentCenterServer(t, service)
	rec := agentCenterRequest(t, handler, "/api/agent-center/runs/4242/activity", map[string]string{"X-Test-Role": "admin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if body.Data == nil || len(body.Data) != 0 {
		t.Fatalf("data = %s, want an empty array", rec.Body.String())
	}
}
