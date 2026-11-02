package auditapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
)

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type seqIDs struct{ next int64 }

func (s *seqIDs) Next() int64 { s.next++; return s.next }

type fakeRepo struct {
	inserted    []audit.Entry
	insertErr   error
	listEntries []audit.Entry
	listErr     error
	lastFilter  audit.Filter
	lastLimit   int
	lastBefore  *int64
}

func (f *fakeRepo) Insert(_ context.Context, entry audit.Entry) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, entry)
	return nil
}

func (f *fakeRepo) List(_ context.Context, filter audit.Filter, limit int, beforeID *int64) ([]audit.Entry, error) {
	f.lastFilter = filter
	f.lastLimit = limit
	f.lastBefore = beforeID
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listEntries) > limit {
		return f.listEntries[:limit], nil
	}
	return f.listEntries, nil
}

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))

func newTestService(repo *fakeRepo) *Service {
	return New(repo, &seqIDs{}, fixedClock{at: testNow})
}

func TestRecordAssignsIDTimestampAndKeepsFields(t *testing.T) {
	repo := &fakeRepo{}
	svc := newTestService(repo)
	actor := int64(42)

	err := svc.Record(context.Background(), Event{
		ActorID:    &actor,
		Action:     "auth.login",
		TargetKind: "user",
		TargetID:   "42",
		RequestID:  "req_12345678",
		IP:         "192.0.2.1",
		UserAgent:  "test-agent",
		Metadata:   map[string]any{"username": "admin"},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted %d entries", len(repo.inserted))
	}
	entry := repo.inserted[0]
	if entry.ID != 1 {
		t.Fatalf("id = %d, want 1", entry.ID)
	}
	if !entry.CreatedAt.Equal(testNow) || entry.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at = %v, want UTC %v", entry.CreatedAt, testNow)
	}
	if entry.ActorID == nil || *entry.ActorID != actor {
		t.Fatalf("actor = %v", entry.ActorID)
	}
	if entry.Action != "auth.login" || entry.TargetKind != "user" || entry.TargetID != "42" {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.RequestID != "req_12345678" || entry.IP != "192.0.2.1" || entry.UserAgent != "test-agent" {
		t.Fatalf("request metadata = %#v", entry)
	}
	if entry.Metadata["username"] != "admin" {
		t.Fatalf("metadata = %#v", entry.Metadata)
	}
}

func TestRecordDefaultsNilMetadataToEmptyObject(t *testing.T) {
	repo := &fakeRepo{}
	svc := newTestService(repo)

	if err := svc.Record(context.Background(), Event{Action: "auth.logout"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	entry := repo.inserted[0]
	if entry.Metadata == nil || len(entry.Metadata) != 0 {
		t.Fatalf("metadata = %#v, want empty object", entry.Metadata)
	}
	if entry.ActorID != nil {
		t.Fatalf("actor = %v, want nil", entry.ActorID)
	}
}

func TestRecordReturnsRepositoryError(t *testing.T) {
	repo := &fakeRepo{insertErr: errors.New("boom")}
	svc := newTestService(repo)

	if err := svc.Record(context.Background(), Event{Action: "auth.login"}); err == nil {
		t.Fatal("Record swallowed a repository error")
	}
}

func TestListAppliesLimitFilterAndCursor(t *testing.T) {
	repo := &fakeRepo{}
	entries := make([]audit.Entry, 0, 3)
	for i := 3; i >= 1; i-- {
		entries = append(entries, audit.Entry{ID: int64(i), Action: "user.created"})
	}
	repo.listEntries = entries
	svc := newTestService(repo)
	actor := int64(7)
	before := int64(99)

	list, hasMore, err := svc.List(context.Background(), Filter{Action: "user.created", ActorID: &actor}, 2, &before)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || !hasMore {
		t.Fatalf("list = %#v, hasMore = %v", list, hasMore)
	}
	if repo.lastLimit != 3 {
		t.Fatalf("repo limit = %d, want 3 (limit+1)", repo.lastLimit)
	}
	if repo.lastFilter.Action != "user.created" || repo.lastFilter.ActorID == nil || *repo.lastFilter.ActorID != actor {
		t.Fatalf("filter = %#v", repo.lastFilter)
	}
	if repo.lastBefore == nil || *repo.lastBefore != before {
		t.Fatalf("before = %v", repo.lastBefore)
	}
}

func TestListClampsLimit(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		{0, DefaultListLimit + 1},
		{-5, DefaultListLimit + 1},
		{1000, MaxListLimit + 1},
		{10, 11},
	}
	for _, tc := range cases {
		repo := &fakeRepo{}
		svc := newTestService(repo)
		if _, _, err := svc.List(context.Background(), Filter{}, tc.limit, nil); err != nil {
			t.Fatalf("List(%d): %v", tc.limit, err)
		}
		if repo.lastLimit != tc.want {
			t.Fatalf("List(%d) repo limit = %d, want %d", tc.limit, repo.lastLimit, tc.want)
		}
	}
}

func TestListPropagatesRepositoryError(t *testing.T) {
	repo := &fakeRepo{listErr: errors.New("boom")}
	svc := newTestService(repo)
	if _, _, err := svc.List(context.Background(), Filter{}, 10, nil); err == nil {
		t.Fatal("List swallowed a repository error")
	}
}
