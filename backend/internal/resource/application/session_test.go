package resourceapp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func seededSession(versionID, versionSUTID, environmentID int64, role, ref string, verifiedAt time.Time) LoginSession {
	return LoginSession{
		VersionID:       versionID,
		VersionSUTID:    versionSUTID,
		EnvironmentID:   environmentID,
		AccountRole:     role,
		StorageStateRef: ref,
		StorageState:    json.RawMessage(`{"cookies":[{"name":"sid","value":"t"}],"origins":[]}`),
		VerifiedAt:      verifiedAt,
	}
}

// TestSessionStoreScopesByVersion proves a captured session is never returned
// across version boundaries even when the nested version SUT binding ID matches:
// the cache key carries the owning version.
func TestSessionStoreScopesByVersion(t *testing.T) {
	store := NewSessionStore(time.Hour)
	now := time.Now()
	ctx := context.Background()
	if err := store.Save(ctx, seededSession(10, 7, 8, "admin", "ss_v10", now)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Save(ctx, seededSession(11, 7, 8, "admin", "ss_v11", now)); err != nil {
		t.Fatalf("save: %v", err)
	}

	first, ok := store.Find(ctx, 10, 7, 8, "admin")
	if !ok || first.StorageStateRef != "ss_v10" {
		t.Fatalf("version 10 session = %+v (ok=%v), want ss_v10", first, ok)
	}
	second, ok := store.Find(ctx, 11, 7, 8, "admin")
	if !ok || second.StorageStateRef != "ss_v11" {
		t.Fatalf("version 11 session = %+v (ok=%v), want ss_v11", second, ok)
	}
	if _, ok := store.Find(ctx, 12, 7, 8, "admin"); ok {
		t.Fatal("a session must not leak into another version")
	}

	store.Invalidate(ctx, 10, 7, 8, "admin")
	if _, ok := store.Find(ctx, 10, 7, 8, "admin"); ok {
		t.Fatal("invalidate must drop the matching version entry")
	}
	if _, ok := store.Find(ctx, 11, 7, 8, "admin"); !ok {
		t.Fatal("invalidating one version must not drop another version's entry")
	}
}

// TestSessionStoreCapsEntries proves the in-memory ref registry is bounded: the
// oldest verified session is evicted once the configured limit is reached, so a
// long-lived API process cannot grow the ref cache without bound.
func TestSessionStoreCapsEntries(t *testing.T) {
	store := NewSessionStoreWithLimit(time.Hour, 2)
	now := time.Now()
	ctx := context.Background()
	for i, versionID := range []int64{10, 11, 12} {
		verifiedAt := now.Add(time.Duration(i) * time.Minute)
		if err := store.Save(ctx, seededSession(versionID, 7, 8, "admin", fmt.Sprintf("ss_%d", versionID), verifiedAt)); err != nil {
			t.Fatalf("save %d: %v", versionID, err)
		}
	}

	if _, ok := store.Find(ctx, 10, 7, 8, "admin"); ok {
		t.Fatal("the oldest session must be evicted at the entry cap")
	}
	if _, ok := store.Find(ctx, 11, 7, 8, "admin"); !ok {
		t.Fatal("the second session must survive")
	}
	if _, ok := store.Find(ctx, 12, 7, 8, "admin"); !ok {
		t.Fatal("the newest session must survive")
	}
}

// TestSessionStorePrunesExpiredBeforeCap proves expired entries are removed
// before the cap is enforced, so stale entries cannot displace fresh ones.
func TestSessionStorePrunesExpiredBeforeCap(t *testing.T) {
	store := NewSessionStoreWithLimit(time.Hour, 2)
	now := time.Now()
	ctx := context.Background()
	expired := seededSession(10, 7, 8, "admin", "ss_expired", now.Add(-2*time.Hour))
	expired.ExpiresAt = now.Add(-time.Hour)
	if err := store.Save(ctx, expired); err != nil {
		t.Fatalf("save expired: %v", err)
	}
	if err := store.Save(ctx, seededSession(11, 7, 8, "admin", "ss_v11", now)); err != nil {
		t.Fatalf("save v11: %v", err)
	}
	if err := store.Save(ctx, seededSession(12, 7, 8, "admin", "ss_v12", now)); err != nil {
		t.Fatalf("save v12: %v", err)
	}

	if _, ok := store.Find(ctx, 10, 7, 8, "admin"); ok {
		t.Fatal("the expired session must be dropped")
	}
	for _, versionID := range []int64{11, 12} {
		if _, ok := store.Find(ctx, versionID, 7, 8, "admin"); !ok {
			t.Fatalf("fresh session for version %d must survive the cap", versionID)
		}
	}
}
