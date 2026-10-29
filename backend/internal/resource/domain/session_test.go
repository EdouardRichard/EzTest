package domain

import (
	"testing"
	"time"
)

// ptrTime returns a pointer to t, for the nullable Session timestamps.
func ptrTime(t time.Time) *time.Time { return &t }

func TestSessionExpiring(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := Session{State: StateValid, CreatedAt: now.Add(-time.Hour), ExpiresAt: ptrTime(now.Add(4 * time.Minute))}
	if !s.Expiring(now) {
		t.Fatal("want expiring")
	}
}

func TestSessionExpiringUsesTenPercentForLongLivedSession(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	// TTL is 10h, so the threshold is 1h, which is larger than 5min.
	expiring := Session{
		State:     StateValid,
		CreatedAt: now.Add(-10 * time.Hour),
		ExpiresAt: ptrTime(now.Add(30 * time.Minute)),
	}
	if !expiring.Expiring(now) {
		t.Fatal("want expiring with 30min left out of a 10h TTL")
	}
	notYet := Session{
		State:     StateValid,
		CreatedAt: now.Add(-10 * time.Hour),
		ExpiresAt: ptrTime(now.Add(2 * time.Hour)),
	}
	if notYet.Expiring(now) {
		t.Fatal("want not expiring with 2h left out of a 10h TTL")
	}
}

func TestSessionExpiringRequiresValidState(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := Session{State: StateNeedsManual, CreatedAt: now.Add(-time.Hour), ExpiresAt: ptrTime(now.Add(time.Minute))}
	if s.Expiring(now) {
		t.Fatal("expiring is derived only for valid sessions")
	}
}

func TestSessionExpiringWithoutExpiresAt(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := Session{State: StateValid, CreatedAt: now.Add(-time.Hour)}
	if s.Expiring(now) {
		t.Fatal("a session without expires_at is never expiring")
	}
}

func TestSessionIsExpired(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	expired := Session{ExpiresAt: ptrTime(now.Add(-time.Second))}
	if !expired.IsExpired(now) {
		t.Fatal("want expired")
	}
	live := Session{ExpiresAt: ptrTime(now.Add(time.Second))}
	if live.IsExpired(now) {
		t.Fatal("want not expired")
	}
	if (Session{}).IsExpired(now) {
		t.Fatal("a session without expires_at is never expired")
	}
	// The boundary is inclusive: expires_at == now is already expired.
	boundary := Session{ExpiresAt: ptrTime(now)}
	if !boundary.IsExpired(now) {
		t.Fatal("want expired at the expires_at boundary")
	}
}
