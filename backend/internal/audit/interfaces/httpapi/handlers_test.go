package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	auditAdminToken = "ezt_audit_admin"
	auditUserToken  = "ezt_audit_user"
	auditTestReqID  = "req_audit_test_1234"
)

var (
	auditAdmin = identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	auditUser  = identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
)

// TestAuditList proves the admin audit endpoint returns the page envelope with
// every entry field and that it sends Task 6's parsed default limit (20) to the
// service instead of letting the service pick its own default.
func TestAuditList(t *testing.T) {
	entries := []auditdomain.Entry{
		{
			ID:          12,
			ActorUserID: int64Ptr(1),
			Action:      "user.created",
			TargetType:  "user",
			TargetID:    "2",
			RequestID:   "req_recorded_1234",
			IP:          "203.0.113.7",
			UserAgent:   "eztest/1.0",
			Metadata:    map[string]any{"username": "qa01"},
			CreatedAt:   time.Date(2026, 10, 27, 5, 0, 0, 0, time.UTC),
		},
		{
			ID:         11,
			Action:     "auth.login_failed",
			TargetType: "user",
			// A nil metadata map must be returned as an object, not null.
			CreatedAt: time.Date(2026, 10, 27, 4, 59, 0, 0, time.UTC),
		},
	}
	lister := &fakeAuditLister{entries: entries}
	h := newAuditTestHandler(lister)

	rec := getAudit(h, "/api/admin/audit", auditAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit list=%d %s", rec.Code, rec.Body)
	}
	if lister.lastLimit != platform.DefaultLimit || lister.lastAfterID != 0 {
		t.Fatalf("lister got limit=%d afterID=%d want %d/0", lister.lastLimit, lister.lastAfterID, platform.DefaultLimit)
	}

	page := decodeAuditList(t, rec.Body)
	if len(page.Data) != 2 || page.HasMore || page.NextCursor != "" {
		t.Fatalf("page=%+v want 2 entries, no more", page)
	}
	first := page.Data[0]
	if first.ID != 12 || first.Action != "user.created" || first.TargetType != "user" || first.TargetID != "2" {
		t.Fatalf("first entry=%+v", first)
	}
	if first.ActorUserID == nil || *first.ActorUserID != 1 {
		t.Fatalf("actor=%v want 1", first.ActorUserID)
	}
	if first.RequestID != "req_recorded_1234" || first.IP != "203.0.113.7" || first.UserAgent != "eztest/1.0" {
		t.Fatalf("request meta=%+v", first)
	}
	if first.Metadata["username"] != "qa01" {
		t.Fatalf("metadata=%v", first.Metadata)
	}
	second := page.Data[1]
	if second.ActorUserID != nil {
		t.Fatalf("anonymous actor=%v want null", *second.ActorUserID)
	}
	if second.Metadata == nil || len(second.Metadata) != 0 {
		t.Fatalf("nil metadata=%v want {}", second.Metadata)
	}
	if second.CreatedAt.IsZero() {
		t.Fatal("created_at was not decoded")
	}
}

// TestAuditListPagination pins the cursor convention: the handler parses
// limit/cursor and returns the last visible ID as the next cursor.
func TestAuditListPagination(t *testing.T) {
	entries := []auditdomain.Entry{
		{ID: 9, Action: "user.updated"},
		{ID: 8, Action: "user.created"},
	}
	lister := &fakeAuditLister{entries: entries, more: true}
	h := newAuditTestHandler(lister)

	path := "/api/admin/audit?limit=2&cursor=" + platform.EncodeCursor(10)
	rec := getAudit(h, path, auditAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit list=%d %s", rec.Code, rec.Body)
	}
	if lister.lastLimit != 2 || lister.lastAfterID != 10 {
		t.Fatalf("lister got limit=%d afterID=%d want 2/10", lister.lastLimit, lister.lastAfterID)
	}
	page := decodeAuditList(t, rec.Body)
	if !page.HasMore || page.NextCursor != platform.EncodeCursor(8) {
		t.Fatalf("page=%+v want has_more and cursor of the last ID", page)
	}
}

// TestAuditListRejectsInvalidPagination proves the handler validates pagination
// with the shared platform helpers.
func TestAuditListRejectsInvalidPagination(t *testing.T) {
	h := newAuditTestHandler(&fakeAuditLister{})
	for _, query := range []string{"limit=0", "limit=101", "limit=abc", "cursor=!!!not-base64!!!"} {
		rec := getAudit(h, "/api/admin/audit?"+query, auditAdminToken)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d body=%s want 400", query, rec.Code, rec.Body)
		}
		if code := decodeAuditErrorCode(t, rec.Body); code != "invalid_argument" {
			t.Fatalf("%s: error code=%q want invalid_argument", query, code)
		}
	}
}

// TestAuditListRequiresAdmin proves the endpoint is admin-only: anonymous calls
// get 401, regular users get 403.
func TestAuditListRequiresAdmin(t *testing.T) {
	h := newAuditTestHandler(&fakeAuditLister{})

	rec := getAudit(h, "/api/admin/audit", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous=%d want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate=%q want Bearer", got)
	}
	if code := decodeAuditErrorCode(t, rec.Body); code != "unauthenticated" {
		t.Fatalf("anonymous error code=%q", code)
	}

	rec = getAudit(h, "/api/admin/audit", auditUserToken)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user token=%d want 403", rec.Code)
	}
	if code := decodeAuditErrorCode(t, rec.Body); code != "forbidden" {
		t.Fatalf("user token error code=%q", code)
	}
}

// TestAuditListRepositoryError proves a storage failure becomes the shared 500
// envelope instead of leaking the repository error.
func TestAuditListRepositoryError(t *testing.T) {
	lister := &fakeAuditLister{err: errors.New("database down")}
	h := newAuditTestHandler(lister)
	rec := getAudit(h, "/api/admin/audit", auditAdminToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("error=%d %s want 500", rec.Code, rec.Body)
	}
	if code := decodeAuditErrorCode(t, rec.Body); code != "internal" {
		t.Fatalf("error code=%q want internal", code)
	}
}

// fakeAuditLister is an in-memory Lister that records the page arguments.
type fakeAuditLister struct {
	entries     []auditdomain.Entry
	more        bool
	err         error
	lastLimit   int
	lastAfterID int64
}

func (l *fakeAuditLister) List(_ context.Context, limit int, afterID int64) ([]auditdomain.Entry, bool, error) {
	l.lastLimit = limit
	l.lastAfterID = afterID
	if l.err != nil {
		return nil, false, l.err
	}
	return l.entries, l.more, nil
}

// staticAuthenticator resolves the two fixed test tokens.
type staticAuthenticator map[string]identity.User

func (a staticAuthenticator) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

// newAuditTestHandler builds the audit handler behind the same auth chain it
// runs in production: global bearer auth, then the per-route admin guard.
func newAuditTestHandler(lister Lister) http.Handler {
	auth := staticAuthenticator{auditAdminToken: auditAdmin, auditUserToken: auditUser}
	return identityhttp.AuthMiddleware(auth)(identityhttp.RequireAdmin(NewHandler(lister)))
}

func getAudit(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(platform.WithRequestID(req.Context(), auditTestReqID))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type auditListPayload struct {
	Data []struct {
		ID          int64          `json:"id"`
		ActorUserID *int64         `json:"actor_user_id"`
		Action      string         `json:"action"`
		TargetType  string         `json:"target_type"`
		TargetID    string         `json:"target_id"`
		RequestID   string         `json:"request_id"`
		IP          string         `json:"ip"`
		UserAgent   string         `json:"user_agent"`
		Metadata    map[string]any `json:"metadata"`
		CreatedAt   time.Time      `json:"created_at"`
	} `json:"data"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

func decodeAuditList(t *testing.T, body io.Reader) auditListPayload {
	t.Helper()
	var payload auditListPayload
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		t.Fatalf("decode audit list: %v", err)
	}
	return payload
}

func decodeAuditErrorCode(t *testing.T, body io.Reader) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		t.Fatalf("decode audit error: %v", err)
	}
	if payload.Error.RequestID != auditTestReqID {
		t.Fatalf("error request_id=%q want %q", payload.Error.RequestID, auditTestReqID)
	}
	return payload.Error.Code
}

func int64Ptr(v int64) *int64 { return &v }
