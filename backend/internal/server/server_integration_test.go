//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventinginfra "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	eventpostgres "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestUserAndAuditAPIsAgainstDatabase is the Task 7 end-to-end check on the
// real PostgreSQL repositories: admin login, user create/list/pagination,
// non-admin 403, status update, password reset with token revocation and the
// persisted audit trail carrying the actor and the request metadata.
func TestUserAndAuditAPIsAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	userRepo := identitypostgres.NewUserRepository(pool)
	tokenRepo := identitypostgres.NewTokenRepository(pool)
	hasher := password.NewHasher(12)
	auditor := auditapp.NewService(auditpostgres.NewRepository(pool))

	userSvc := application.NewUserService(userRepo, hasher, auditor, tokenRepo)
	if err := userSvc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	authSvc := application.NewAuthService(userRepo, tokenRepo, hasher, time.Now, auditor)

	h := New(config.Config{Env: "test"}, testLogger(), Dependencies{
		Pinger: fakePinger{},
		Auth:   authSvc,
		Users:  userSvc,
		Audit:  auditor,
	})

	adminToken := doLogin(t, h, "admin", "admin-pass-123")

	// The admin creates a regular user.
	rec := doJSON(t, h, http.MethodPost, "/api/users",
		`{"username":"qa01","password":"qa-pass-1234"}`, adminToken, "req_int_create_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body)
	}
	var created struct {
		Data struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
			Status   string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create %q: %v", rec.Body, err)
	}
	if created.Data.ID != 2 || created.Data.Username != "qa01" ||
		created.Data.Role != string(identity.RoleUser) || created.Data.Status != string(identity.StatusActive) {
		t.Fatalf("created=%+v", created.Data)
	}

	// The list is paginated with an opaque cursor and has_more.
	rec = doJSON(t, h, http.MethodGet, "/api/users?limit=1", "", adminToken, "req_int_list_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d %s", rec.Code, rec.Body)
	}
	var firstPage struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor string            `json:"next_cursor"`
		HasMore    bool              `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode list %q: %v", rec.Body, err)
	}
	if len(firstPage.Data) != 1 || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("first page=%s", rec.Body)
	}
	rec = doJSON(t, h, http.MethodGet,
		"/api/users?limit=1&cursor="+url.QueryEscape(firstPage.NextCursor), "", adminToken, "req_int_list_2")
	if rec.Code != http.StatusOK {
		t.Fatalf("second page=%d %s", rec.Code, rec.Body)
	}
	var secondPage struct {
		Data []struct {
			Username string `json:"username"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page %q: %v", rec.Body, err)
	}
	if len(secondPage.Data) != 1 || secondPage.Data[0].Username != "qa01" || secondPage.HasMore {
		t.Fatalf("second page=%s", rec.Body)
	}

	// A regular user cannot reach the admin users API.
	userToken := doLogin(t, h, "qa01", "qa-pass-1234")
	rec = doJSON(t, h, http.MethodGet, "/api/users", "", userToken, "req_int_forbidden_1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user list=%d %s want 403", rec.Code, rec.Body)
	}

	// Disabling the user immediately invalidates its sessions.
	rec = doJSON(t, h, http.MethodPatch, "/api/users/2", `{"status":"disabled"}`, adminToken, "req_int_disable_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("disable=%d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodGet, "/api/auth/me", "", userToken, "req_int_me_disabled")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user me=%d %s want 401", rec.Code, rec.Body)
	}
	// The single admin cannot disable itself.
	rec = doJSON(t, h, http.MethodPatch, "/api/users/1", `{"status":"disabled"}`, adminToken, "req_int_self_disable")
	if rec.Code != http.StatusConflict {
		t.Fatalf("self-disable=%d %s want 409", rec.Code, rec.Body)
	}

	// Re-enable the user, log in again, then reset its password: every token
	// of that user must be revoked.
	rec = doJSON(t, h, http.MethodPatch, "/api/users/2", `{"status":"active"}`, adminToken, "req_int_enable_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("enable=%d %s", rec.Code, rec.Body)
	}
	// The disable revoked the token; the re-enable must not resurrect it.
	rec = doJSON(t, h, http.MethodGet, "/api/auth/me", "", userToken, "req_int_me_reenabled")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token issued before disable became valid again after re-enable: me=%d %s", rec.Code, rec.Body)
	}
	userToken = doLogin(t, h, "qa01", "qa-pass-1234")
	rec = doJSON(t, h, http.MethodPost, "/api/users/2/reset-password",
		`{"new_password":"new-pass-1234"}`, adminToken, "req_int_reset_1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reset=%d %s want 204", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodGet, "/api/auth/me", "", userToken, "req_int_me_reset")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token survived password reset: me=%d %s", rec.Code, rec.Body)
	}
	doLogin(t, h, "qa01", "new-pass-1234")

	// The audit log is persisted and carries the actor and the request
	// metadata the HTTP handlers attached.
	rec = doJSON(t, h, http.MethodGet, "/api/admin/audit?limit=100", "", adminToken, "req_int_audit_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit=%d %s", rec.Code, rec.Body)
	}
	var auditPage struct {
		Data []struct {
			ActorUserID *int64         `json:"actor_user_id"`
			Action      string         `json:"action"`
			TargetID    string         `json:"target_id"`
			RequestID   string         `json:"request_id"`
			IP          string         `json:"ip"`
			UserAgent   string         `json:"user_agent"`
			Metadata    map[string]any `json:"metadata"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &auditPage); err != nil {
		t.Fatalf("decode audit %q: %v", rec.Body, err)
	}
	if len(auditPage.Data) == 0 {
		t.Fatal("audit log is empty")
	}
	byAction := make(map[string]int, len(auditPage.Data))
	for i, entry := range auditPage.Data {
		byAction[entry.Action] = i
	}
	for _, action := range []string{
		auditapp.ActionUserCreated,
		auditapp.ActionUserUpdated,
		auditapp.ActionUserPasswordReset,
		auditapp.ActionLoginSucceeded,
	} {
		if _, ok := byAction[action]; !ok {
			t.Fatalf("audit log has no %s entry: %+v", action, auditPage.Data)
		}
	}
	createdEntry := auditPage.Data[byAction[auditapp.ActionUserCreated]]
	if createdEntry.ActorUserID == nil || *createdEntry.ActorUserID != 1 {
		t.Fatalf("user.created actor=%v want admin 1", createdEntry.ActorUserID)
	}
	if createdEntry.TargetID != "2" {
		t.Fatalf("user.created target=%q want 2", createdEntry.TargetID)
	}
	if createdEntry.RequestID != "req_int_create_1" {
		t.Fatalf("user.created request_id=%q want req_int_create_1", createdEntry.RequestID)
	}
	if createdEntry.IP != "192.0.2.1" {
		t.Fatalf("user.created ip=%q want 192.0.2.1", createdEntry.IP)
	}
	if createdEntry.UserAgent != "eztest-integration/1.0" {
		t.Fatalf("user.created user_agent=%q", createdEntry.UserAgent)
	}
	if createdEntry.Metadata["username"] != "qa01" {
		t.Fatalf("user.created metadata=%v", createdEntry.Metadata)
	}
	resetEntry := auditPage.Data[byAction[auditapp.ActionUserPasswordReset]]
	if resetEntry.ActorUserID == nil || *resetEntry.ActorUserID != 1 {
		t.Fatalf("password reset actor=%v want admin 1", resetEntry.ActorUserID)
	}
}

// doJSON sends one JSON request through h and returns the recorder. requestID
// is sent as X-Request-ID so the audit tests can correlate the persisted entry
// with the request that produced it.
func doJSON(t *testing.T, h http.Handler, method, path, body, token, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("User-Agent", "eztest-integration/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// doLogin logs in over HTTP and returns the raw bearer token.
func doLogin(t *testing.T, h http.Handler, username, password string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPost, "/api/auth/login", string(body), "", "req_int_login_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("login %q=%d %s", username, rec.Code, rec.Body)
	}
	var payload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login %q: %v", rec.Body, err)
	}
	if payload.Data.Token == "" {
		t.Fatalf("login %q returned no token: %s", username, rec.Body)
	}
	return payload.Data.Token
}

// TestEventStreamAPIsAgainstDatabase is the Task 8 end-to-end check on the real
// PostgreSQL store and the in-process hub: unknown-channel 404, the admin test
// publisher (seqs, audit, 403/401), the snapshot envelope and cursor, and an SSE
// reconnect that replays without duplicates or gaps.
func TestEventStreamAPIsAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	userRepo := identitypostgres.NewUserRepository(pool)
	tokenRepo := identitypostgres.NewTokenRepository(pool)
	hasher := password.NewHasher(12)
	auditor := auditapp.NewService(auditpostgres.NewRepository(pool))

	userSvc := application.NewUserService(userRepo, hasher, auditor, tokenRepo)
	if err := userSvc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	authSvc := application.NewAuthService(userRepo, tokenRepo, hasher, time.Now, auditor)

	eventStore := eventpostgres.NewEventStore(pool)
	eventHub := eventinginfra.NewHub()
	eventSvc := eventingapp.NewService(eventStore, eventHub)

	h := New(config.Config{Env: "test", EnableTestEndpoints: true}, testLogger(), Dependencies{
		Pinger:      fakePinger{},
		Auth:        authSvc,
		Users:       userSvc,
		Audit:       auditor,
		Events:      eventSvc,
		EventReader: eventStore,
		EventHub:    eventHub,
	})

	adminToken := doLogin(t, h, "admin", "admin-pass-123")
	rec := doJSON(t, h, http.MethodPost, "/api/users",
		`{"username":"qa01","password":"qa-pass-1234"}`, adminToken, "req_int_stream_create_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user=%d %s", rec.Code, rec.Body)
	}
	userToken := doLogin(t, h, "qa01", "qa-pass-1234")

	// Unknown channels are reported as 404 by the snapshot endpoint.
	rec = doJSON(t, h, http.MethodGet, "/api/stream/events?channel=system", "", adminToken,
		"req_int_stream_snapshot_empty")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty channel snapshot=%d %s want 404", rec.Code, rec.Body)
	}

	// The admin test publisher writes three synthetic events and returns seqs.
	rec = doJSON(t, h, http.MethodPost, "/api/admin/stream/test-events",
		`{"count":3,"summary":"admin test event","detail":{"index":1}}`, adminToken,
		"req_int_stream_publish_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("test publish=%d %s", rec.Code, rec.Body)
	}
	var published struct {
		Data struct {
			Seqs []int64 `json:"seqs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &published); err != nil {
		t.Fatalf("decode publish %q: %v", rec.Body, err)
	}
	if len(published.Data.Seqs) != 3 || published.Data.Seqs[0] != 1 ||
		published.Data.Seqs[1] != 2 || published.Data.Seqs[2] != 3 {
		t.Fatalf("seqs=%v want [1 2 3]", published.Data.Seqs)
	}

	// The publish is audited with the admin actor and the request metadata.
	rec = doJSON(t, h, http.MethodGet, "/api/admin/audit?limit=100", "", adminToken,
		"req_int_stream_audit_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit=%d %s", rec.Code, rec.Body)
	}
	var auditPage struct {
		Data []struct {
			ActorUserID *int64         `json:"actor_user_id"`
			Action      string         `json:"action"`
			RequestID   string         `json:"request_id"`
			IP          string         `json:"ip"`
			Metadata    map[string]any `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &auditPage); err != nil {
		t.Fatalf("decode audit %q: %v", rec.Body, err)
	}
	var publishEntry *struct {
		ActorUserID *int64         `json:"actor_user_id"`
		Action      string         `json:"action"`
		RequestID   string         `json:"request_id"`
		IP          string         `json:"ip"`
		Metadata    map[string]any `json:"metadata"`
	}
	for i := range auditPage.Data {
		if auditPage.Data[i].Action == "sse.test_published" {
			publishEntry = &auditPage.Data[i]
			break
		}
	}
	if publishEntry == nil {
		t.Fatalf("audit log has no sse.test_published entry: %+v", auditPage.Data)
	}
	if publishEntry.ActorUserID == nil || *publishEntry.ActorUserID != 1 {
		t.Fatalf("publish audit actor=%v want admin 1", publishEntry.ActorUserID)
	}
	if publishEntry.RequestID != "req_int_stream_publish_1" {
		t.Fatalf("publish audit request_id=%q", publishEntry.RequestID)
	}
	if publishEntry.IP != "192.0.2.1" {
		t.Fatalf("publish audit ip=%q", publishEntry.IP)
	}
	if publishEntry.Metadata["count"] != float64(3) {
		t.Fatalf("publish audit metadata=%v", publishEntry.Metadata)
	}

	// The snapshot returns the newest events below the cursor, ascending, with
	// the channel's latest seq and a has_more flag.
	rec = doJSON(t, h, http.MethodGet, "/api/stream/events?channel=system&limit=2", "", userToken,
		"req_int_stream_snapshot_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot=%d %s", rec.Code, rec.Body)
	}
	var snapshot struct {
		Data struct {
			Items []struct {
				Seq     int64          `json:"seq"`
				Channel string         `json:"channel"`
				Type    string         `json:"type"`
				Summary string         `json:"summary"`
				Detail  map[string]any `json:"detail"`
				Refs    map[string]any `json:"refs"`
			} `json:"items"`
			SnapshotCursor int64 `json:"snapshot_cursor"`
			HasMore        bool  `json:"has_more"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot %q: %v", rec.Body, err)
	}
	if snapshot.Data.SnapshotCursor != 3 || !snapshot.Data.HasMore || len(snapshot.Data.Items) != 2 {
		t.Fatalf("snapshot=%s", rec.Body)
	}
	if snapshot.Data.Items[0].Seq != 2 || snapshot.Data.Items[1].Seq != 3 {
		t.Fatalf("snapshot seqs=%+v want [2 3]", snapshot.Data.Items)
	}
	if snapshot.Data.Items[0].Summary != "admin test event" ||
		snapshot.Data.Items[0].Detail["index"] != float64(1) ||
		snapshot.Data.Items[0].Refs["evidence"] == nil {
		t.Fatalf("snapshot item=%+v", snapshot.Data.Items[0])
	}
	rec = doJSON(t, h, http.MethodGet, "/api/stream/events?channel=system&limit=1&before_seq=3", "", adminToken,
		"req_int_stream_snapshot_2")
	if rec.Code != http.StatusOK {
		t.Fatalf("before_seq snapshot=%d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode before_seq snapshot %q: %v", rec.Body, err)
	}
	if len(snapshot.Data.Items) != 1 || snapshot.Data.Items[0].Seq != 2 ||
		snapshot.Data.SnapshotCursor != 3 || !snapshot.Data.HasMore {
		t.Fatalf("before_seq snapshot=%s", rec.Body)
	}

	// The test publisher is admin-only and requires authentication.
	rec = doJSON(t, h, http.MethodPost, "/api/admin/stream/test-events", `{"count":1}`, userToken,
		"req_int_stream_forbidden_1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("test publish as user=%d %s want 403", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodPost, "/api/admin/stream/test-events", `{"count":1}`, "",
		"req_int_stream_unauth_1")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("test publish anonymous=%d %s want 401", rec.Code, rec.Body)
	}

	// Open the SSE stream from seq 1: the persisted events 2 and 3 replay, then
	// an event published while connected arrives live, exactly once.
	srv := httptest.NewServer(h)
	defer srv.Close()
	streamCtx, cancelStream := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStream()
	stream := openEventStream(t, streamCtx, srv.URL+"/api/stream?channel=system&since=1", adminToken, "")
	defer stream.Body.Close()
	if got := stream.Header.Get("X-Snapshot-Cursor"); got != "3" {
		t.Fatalf("X-Snapshot-Cursor=%q want 3", got)
	}
	reader := bufio.NewReader(stream.Body)
	assertStreamEvent(t, readStreamFrame(t, reader), "2")
	assertStreamEvent(t, readStreamFrame(t, reader), "3")

	rec = doJSON(t, h, http.MethodPost, "/api/admin/stream/test-events", `{"count":1,"summary":"live"}`, adminToken,
		"req_int_stream_publish_2")
	if rec.Code != http.StatusOK {
		t.Fatalf("live publish=%d %s", rec.Code, rec.Body)
	}
	assertStreamEvent(t, readStreamFrame(t, reader), "4")
	stream.Body.Close()

	// Reconnect with Last-Event-ID: only the missing event is replayed.
	reconnectCtx, cancelReconnect := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReconnect()
	reconnected := openEventStream(t, reconnectCtx, srv.URL+"/api/stream?channel=system&since=1", adminToken, "3")
	defer reconnected.Body.Close()
	if got := reconnected.Header.Get("X-Snapshot-Cursor"); got != "4" {
		t.Fatalf("reconnect X-Snapshot-Cursor=%q want 4", got)
	}
	assertStreamEvent(t, readStreamFrame(t, bufio.NewReader(reconnected.Body)), "4")
}

// openEventStream opens an SSE response and fails on a non-200 status.
func openEventStream(t *testing.T, ctx context.Context, url, token, lastEventID string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "eztest-integration/1.0")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
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

// streamFrame is one parsed SSE frame.
type streamFrame struct {
	ID    string
	Event string
	Data  string
}

// readStreamFrame reads one event frame, skipping comment-only heartbeats.
func readStreamFrame(t *testing.T, reader *bufio.Reader) streamFrame {
	t.Helper()
	for {
		frame := streamFrame{}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE frame: %v", err)
			}
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				break
			}
			if strings.HasPrefix(line, ":") {
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
				frame.Data = value
			}
		}
		if frame.ID != "" || frame.Event != "" {
			return frame
		}
	}
}

// assertStreamEvent checks one replayed event frame's id, type and payload.
func assertStreamEvent(t *testing.T, frame streamFrame, wantSeq string) {
	t.Helper()
	if frame.ID != wantSeq {
		t.Fatalf("stream id=%q want %q (event=%q data=%q)", frame.ID, wantSeq, frame.Event, frame.Data)
	}
	if frame.Event != "system.test" {
		t.Fatalf("stream event=%q want system.test", frame.Event)
	}
	var payload struct {
		Seq     int64  `json:"seq"`
		Channel string `json:"channel"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		t.Fatalf("decode stream data %q: %v", frame.Data, err)
	}
	if fmt.Sprint(payload.Seq) != wantSeq {
		t.Fatalf("stream payload seq=%d want %s", payload.Seq, wantSeq)
	}
	if payload.Channel != "system" || payload.Type != "system.test" {
		t.Fatalf("stream payload=%+v", payload)
	}
}
