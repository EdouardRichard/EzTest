package resource_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapresource "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/resource"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fixedClock returns a fixed instant so the session validity window is
// deterministic.
type fixedClock struct {
	now time.Time
}

// Now implements the mindmap Clock port.
func (f fixedClock) Now() time.Time { return f.now }

// fakeSessionService returns one configured P2 session detail tuple.
type fakeSessionService struct {
	session   *resourcedomain.Session
	snapshot  *resourcedomain.Snapshot
	lease     *resourcedomain.Lease
	takeover  *resourcedomain.Takeover
	err       error
	requested int64
}

// Get implements the P2 session read.
func (f *fakeSessionService) Get(_ context.Context, id int64) (*resourcedomain.Session, *resourcedomain.Snapshot, *resourcedomain.Lease, *resourcedomain.Takeover, error) {
	f.requested = id
	if f.err != nil {
		return nil, nil, nil, nil, f.err
	}
	return f.session, f.snapshot, f.lease, f.takeover, nil
}

// TestSessionAdapterReturnsStorageStateCookies pins the exploration gateway: a
// valid session yields only its cookies (no localStorage, no token headers).
func TestSessionAdapterReturnsStorageStateCookies(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	session := &resourcedomain.Session{
		ID:               5,
		EnvironmentID:    21,
		State:            resourcedomain.StateValid,
		ExpiresAt:        &expiresAt,
		StorageStateKind: resourcedomain.StorageStateKindStorageState,
		StorageState: json.RawMessage(`{
			"cookies": [
				{"name": "sid", "value": "abc", "domain": "example.com", "path": "/", "expires": 0},
				{"name": "csrftoken", "value": "xyz", "domain": "example.com", "path": "/", "expires": 0}
			],
			"origins": [
				{"origin": "https://example.com", "localStorage": [{"name": "accessToken", "value": "secret"}]}
			]
		}`),
	}
	adapter := mindmapresource.NewSessionAdapter(&fakeSessionService{session: session}, fixedClock{now: now})
	var _ mindmapapp.SessionGateway = adapter

	got, err := adapter.GetForExplore(ctx, 5)
	if err != nil {
		t.Fatalf("GetForExplore: %v", err)
	}
	if got.ID != 5 || got.State != resourcedomain.StateValid || got.EnvironmentID != 21 {
		t.Fatalf("snapshot=%+v want id 5, state valid, environment 21", got)
	}
	if len(got.Cookies) != 2 {
		t.Fatalf("cookies=%v want exactly the two cookie pairs", got.Cookies)
	}
	if got.Cookies["sid"] != "abc" || got.Cookies["csrftoken"] != "xyz" {
		t.Fatalf("cookies=%v want sid=abc and csrftoken=xyz", got.Cookies)
	}
	if _, leaked := got.Cookies["accessToken"]; leaked {
		t.Fatal("localStorage value leaked into the cookie map")
	}
}

// TestSessionAdapterRejectsUnusableSessions pins the validity gate: expired,
// non-valid and token-kind sessions are rejected with a mindmap state conflict.
func TestSessionAdapterRejectsUnusableSessions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	cases := []struct {
		name    string
		session *resourcedomain.Session
	}{
		{
			name: "expired",
			session: &resourcedomain.Session{
				ID: 5, State: resourcedomain.StateValid, ExpiresAt: &past,
				StorageStateKind: resourcedomain.StorageStateKindStorageState,
				StorageState:     json.RawMessage(`{"cookies":[]}`),
			},
		},
		{
			name: "needs manual",
			session: &resourcedomain.Session{
				ID: 5, State: resourcedomain.StateNeedsManual, ExpiresAt: &future,
				StorageStateKind: resourcedomain.StorageStateKindStorageState,
				StorageState:     json.RawMessage(`{"cookies":[]}`),
			},
		},
		{
			name: "token kind",
			session: &resourcedomain.Session{
				ID: 5, State: resourcedomain.StateValid, ExpiresAt: &future,
				StorageStateKind: resourcedomain.StorageStateKindToken,
				StorageState:     json.RawMessage(`{"headers":{"Authorization":"Bearer secret"}}`),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := mindmapresource.NewSessionAdapter(&fakeSessionService{session: tc.session}, fixedClock{now: now})
			_, err := adapter.GetForExplore(ctx, 5)
			var mapped *mindmapdomain.Error
			if !errors.As(err, &mapped) {
				t.Fatalf("GetForExplore error=%v want a mindmap domain error", err)
			}
			if mapped.Kind != mindmapdomain.KindStateConflict || mapped.Reason != "session_not_valid" {
				t.Fatalf("kind/reason=%q/%q want state_conflict/session_not_valid", mapped.Kind, mapped.Reason)
			}
		})
	}
}

// TestSessionAdapterMapsLookupErrors pins the not_found and invalid-document
// translations.
func TestSessionAdapterMapsLookupErrors(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

	notFound := mindmapresource.NewSessionAdapter(
		&fakeSessionService{err: resourcedomain.NewNotFound("session", 5)},
		fixedClock{now: now},
	)
	_, err := notFound.GetForExplore(ctx, 5)
	var mapped *mindmapdomain.Error
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound || mapped.Reason != "session_not_found" {
		t.Fatalf("not found error=%v want session_not_found", err)
	}

	expiresAt := now.Add(time.Hour)
	invalid := mindmapresource.NewSessionAdapter(&fakeSessionService{session: &resourcedomain.Session{
		ID: 5, State: resourcedomain.StateValid, ExpiresAt: &expiresAt,
		StorageStateKind: resourcedomain.StorageStateKindStorageState,
		StorageState:     json.RawMessage(`{"cookies":`),
	}}, fixedClock{now: now})
	_, err = invalid.GetForExplore(ctx, 5)
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindInvalid {
		t.Fatalf("invalid state error=%v want a mindmap invalid error", err)
	}
}
