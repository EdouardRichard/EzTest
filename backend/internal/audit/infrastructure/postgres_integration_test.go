//go:build integration

package auditpg_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func TestRepositoryInsertAndList(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := auditpg.New(pool)
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	actor := int64(42)
	other := int64(43)

	fixtures := []audit.Entry{
		{ID: 9001, Action: "auth.login_failed", RequestID: "req_a"},
		{ID: 9002, ActorID: &actor, Action: "auth.login", TargetKind: "user", TargetID: "42", RequestID: "req_b", Metadata: map[string]any{"username": "admin"}},
		{ID: 9003, ActorID: &actor, Action: "user.created", Metadata: map[string]any{"username": "alice", "role": "user"}},
		{ID: 9004, ActorID: &other, Action: "auth.logout"},
		{ID: 9005, ActorID: &actor, Action: "auth.login", IP: "192.0.2.1", UserAgent: "test-agent"},
	}
	for _, entry := range fixtures {
		entry.CreatedAt = at
		if err := repo.Insert(ctx, entry); err != nil {
			t.Fatalf("Insert(%d): %v", entry.ID, err)
		}
	}

	page, err := repo.List(ctx, audit.Filter{}, 3, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 3 || page[0].ID != 9005 || page[1].ID != 9004 || page[2].ID != 9003 {
		t.Fatalf("page = %#v", page)
	}
	if page[0].Metadata == nil || len(page[0].Metadata) != 0 {
		t.Fatalf("metadata = %#v, want empty object", page[0].Metadata)
	}
	if page[0].IP != "192.0.2.1" || page[0].UserAgent != "test-agent" {
		t.Fatalf("request metadata = %#v", page[0])
	}

	before := int64(9003)
	older, err := repo.List(ctx, audit.Filter{}, 10, &before)
	if err != nil {
		t.Fatalf("List before: %v", err)
	}
	if len(older) != 2 || older[0].ID != 9002 || older[1].ID != 9001 {
		t.Fatalf("older = %#v", older)
	}

	byAction, err := repo.List(ctx, audit.Filter{Action: "auth.login"}, 10, nil)
	if err != nil {
		t.Fatalf("List by action: %v", err)
	}
	if len(byAction) != 2 || byAction[0].ID != 9005 || byAction[1].ID != 9002 {
		t.Fatalf("by action = %#v", byAction)
	}

	byActor, err := repo.List(ctx, audit.Filter{ActorID: &actor}, 10, nil)
	if err != nil {
		t.Fatalf("List by actor: %v", err)
	}
	if len(byActor) != 3 {
		t.Fatalf("by actor = %#v", byActor)
	}
	for _, entry := range byActor {
		if entry.ActorID == nil || *entry.ActorID != actor {
			t.Fatalf("by actor matched %v", entry.ActorID)
		}
	}
}

func TestServiceRecordsAndListsThroughRepository(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := auditpg.New(pool)
	svc := auditapp.New(repo, ids{}, clock.System{})
	ctx := context.Background()

	actor := int64(7)
	if err := svc.Record(ctx, auditapp.Event{ActorID: &actor, Action: "user.updated", TargetKind: "user", TargetID: "9", Metadata: map[string]any{"status": "disabled"}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := svc.Record(ctx, auditapp.Event{Action: "auth.logout"}); err != nil {
		t.Fatalf("second Record: %v", err)
	}

	entries, hasMore, err := svc.List(ctx, auditapp.Filter{}, 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 || hasMore {
		t.Fatalf("entries = %#v, hasMore = %v", entries, hasMore)
	}
	if entries[0].ID <= entries[1].ID || entries[0].ID == 0 {
		t.Fatalf("ids are not generated newest-first: %#v", entries)
	}
	if entries[1].Metadata["status"] != "disabled" {
		t.Fatalf("metadata = %#v", entries[1].Metadata)
	}
}

type ids struct{}

func (ids) Next() int64 { return time.Now().UnixNano() }

func TestDeleteOlderThanRemovesOldAuditRows(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := auditpg.New(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	for _, entry := range []audit.Entry{
		{ID: 9101, Action: "retention.old", CreatedAt: old},
		{ID: 9102, Action: "retention.recent", CreatedAt: recent},
	} {
		if err := repo.Insert(ctx, entry); err != nil {
			t.Fatalf("Insert(%d): %v", entry.ID, err)
		}
	}
	deleted, err := repo.DeleteOlderThan(ctx, now.Add(-90*24*time.Hour), 10)
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	page, err := repo.List(ctx, audit.Filter{}, 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, entry := range page {
		if entry.ID == 9101 {
			t.Fatal("old audit row survived retention")
		}
		if entry.ID == 9102 {
			return
		}
	}
	t.Fatal("recent audit row was deleted")
}
