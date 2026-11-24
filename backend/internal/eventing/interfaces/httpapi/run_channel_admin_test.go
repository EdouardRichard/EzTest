package eventingapi

// v0.2.4-D T8 (SC-D3/R-D4, security): the run:{id} transcript channel is
// admin-only on EVERY generic event access path, not only on the new Agent
// Center endpoint. Both handlers below are the whole HTTP surface of the
// eventing context (GET /api/events snapshot and GET /api/stream SSE), so a
// non-admin must be refused on both — and refused BEFORE any read/subscription
// happens, never merely hidden in the frontend.
//
// The expectations come from the spec (403 for a logged-in non-admin, 401 for a
// missing actor, 400 for an unsupported channel) and from the admin/user
// functional split (constitution Constraints A.1), not from observed output.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// roleRequest attaches an actor with an explicit role.
func roleRequest(r *http.Request, id int64, role string) *http.Request {
	return r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: id, Role: role}))
}

func errorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

// TestSnapshotRunChannelIsAdminOnly pins access path 1: the persisted-history
// snapshot of a run transcript. A non-admin gets 403 and the service is never
// queried (the refusal is a gate, not a filtered result).
func TestSnapshotRunChannelIsAdminOnly(t *testing.T) {
	svc := newFakeService()
	handler := New(svc)
	req := roleRequest(httptest.NewRequest(http.MethodGet, "/api/events?channel=run:42", nil), 7, "user")
	rec := httptest.NewRecorder()

	handler.Snapshot(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d (%s), want 403", rec.Code, rec.Body.String())
	}
	if code := errorEnvelope(t, rec); code != "forbidden" {
		t.Fatalf("error code = %q, want forbidden", code)
	}
	if calls, _, _, _ := svc.snapshotArgs(); calls != 0 {
		t.Fatalf("snapshot was queried %d times for a refused non-admin", calls)
	}
}

// TestStreamRunChannelIsAdminOnly pins access path 2: the live SSE subscription.
// A non-admin gets 403 and no hub subscription is created.
func TestStreamRunChannelIsAdminOnly(t *testing.T) {
	svc := newFakeService()
	handler := New(svc)
	// A cancelled context keeps a REGRESSED gate from hanging the suite in the
	// live SSE loop: without the gate the handler would answer 200 and stream
	// forever, so the failure must be a status assertion, not a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := roleRequest(httptest.NewRequest(http.MethodGet, "/api/stream?channel=run:42", nil).WithContext(ctx), 7, "user")
	rec := httptest.NewRecorder()

	handler.Stream(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d (%s), want 403", rec.Code, rec.Body.String())
	}
	if code := errorEnvelope(t, rec); code != "forbidden" {
		t.Fatalf("error code = %q, want forbidden", code)
	}
	if subscribed, channel, _ := svc.subscription(); subscribed {
		t.Fatalf("a refused non-admin subscribed to %q", channel)
	}
}

// TestRunChannelRejectsMissingActor keeps the 401 path ahead of the role gate on
// both handlers: an unauthenticated caller is never told about roles.
func TestRunChannelRejectsMissingActor(t *testing.T) {
	for _, path := range []string{"/api/events?channel=run:42", "/api/stream?channel=run:42"} {
		svc := newFakeService()
		handler := New(svc)
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		if path == "/api/events?channel=run:42" {
			handler.Snapshot(rec, req)
		} else {
			handler.Stream(rec, req)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", path, rec.Code)
		}
	}
}

// TestRunChannelAllowsAdmin pins the positive path on both handlers: an admin
// reads the snapshot and opens the live stream of any run.
func TestRunChannelAllowsAdmin(t *testing.T) {
	svc := newFakeService()
	handler := New(svc)
	req := roleRequest(httptest.NewRequest(http.MethodGet, "/api/events?channel=run:42", nil), 1, "admin")
	rec := httptest.NewRecorder()
	handler.Snapshot(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin snapshot status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if _, channel, _, _ := svc.snapshotArgs(); channel != "run:42" {
		t.Fatalf("admin snapshot channel = %q, want run:42", channel)
	}

	streamSvc := newFakeService()
	streamHandler := New(streamSvc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the live loop ends immediately; only the admission is under test
	streamReq := roleRequest(httptest.NewRequest(http.MethodGet, "/api/stream?channel=run:42", nil).WithContext(ctx), 1, "admin")
	streamRec := httptest.NewRecorder()
	streamHandler.Stream(streamRec, streamReq)
	if streamRec.Code != http.StatusOK {
		t.Fatalf("admin stream status = %d (%s), want 200", streamRec.Code, streamRec.Body.String())
	}
	if subscribed, channel, _ := streamSvc.subscription(); !subscribed || channel != "run:42" {
		t.Fatalf("admin subscription = %v/%q, want true/run:42", subscribed, channel)
	}
}

// TestOtherChannelsStayOpenToLoggedInUsers is the SC-D3 regression: the new gate
// must not touch system/version:{id}/user:{id} on either handler.
func TestOtherChannelsStayOpenToLoggedInUsers(t *testing.T) {
	for _, channel := range []string{"system", "version:7", "user:42"} {
		svc := newFakeService()
		handler := New(svc)
		req := roleRequest(httptest.NewRequest(http.MethodGet, "/api/events?channel="+channel, nil), 7, "user")
		rec := httptest.NewRecorder()
		handler.Snapshot(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("snapshot %s: status = %d (%s), want 200", channel, rec.Code, rec.Body.String())
		}

		streamSvc := newFakeService()
		streamHandler := New(streamSvc)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		streamReq := roleRequest(httptest.NewRequest(http.MethodGet, "/api/stream?channel="+channel, nil).WithContext(ctx), 7, "user")
		streamRec := httptest.NewRecorder()
		streamHandler.Stream(streamRec, streamReq)
		if streamRec.Code != http.StatusOK {
			t.Fatalf("stream %s: status = %d (%s), want 200", channel, streamRec.Code, streamRec.Body.String())
		}
	}
}

// TestRunChannelGateRunsAfterChannelValidation pins the ordering the brief
// requires: an unsupported channel is a 400 field error for everyone, so the
// role gate never turns a malformed channel into a role disclosure.
func TestRunChannelGateRunsAfterChannelValidation(t *testing.T) {
	for _, channel := range []string{"run:abc", "run:0", "run:"} {
		svc := newFakeService()
		handler := New(svc)
		req := roleRequest(httptest.NewRequest(http.MethodGet, "/api/events?channel="+channel, nil), 7, "user")
		rec := httptest.NewRecorder()
		handler.Snapshot(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("snapshot %q: status = %d, want 400", channel, rec.Code)
		}

		streamReq := roleRequest(httptest.NewRequest(http.MethodGet, "/api/stream?channel="+channel, nil), 7, "user")
		streamRec := httptest.NewRecorder()
		handler.Stream(streamRec, streamReq)
		if streamRec.Code != http.StatusBadRequest {
			t.Fatalf("stream %q: status = %d, want 400", channel, streamRec.Code)
		}
	}
}
