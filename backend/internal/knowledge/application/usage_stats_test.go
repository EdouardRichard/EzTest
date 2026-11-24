package knowledgeapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestKnowledgeItemDetailReadsUsage pins the H-A5/R-H8 read surface: the item
// detail use case must surface the real knowledge_usage aggregates through the
// UsageRepository reader (COUNT/MAX over the append-only usage rows), not a
// fabricated or zero-valued projection. Reader-existence mutation: deleting the
// service→reader call (returning zero stats instead) fails this test; deleting
// the infra reader→SQL fails TestUsageStatsByItem and
// TestItemUsageReadsRecordedUsage (D T10 precedent, V-0019 H share).
func TestKnowledgeItemDetailReadsUsage(t *testing.T) {
	f := newFixture(t)
	item := approvedItem(7001, knowledge.ScopeGlobal, "")
	f.repo.items[item.ID] = item
	lastUsed := testNow.Add(-30 * time.Minute)
	f.usage.stats = knowledgeapp.UsageStats{InjectionCount: 3, LastUsedAt: &lastUsed}

	stats, err := f.svc.ItemUsage(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("ItemUsage: %v", err)
	}
	if stats.InjectionCount != 3 {
		t.Fatalf("InjectionCount = %d, want the reader's 3", stats.InjectionCount)
	}
	if stats.LastUsedAt == nil || !stats.LastUsedAt.Equal(lastUsed) {
		t.Fatalf("LastUsedAt = %v, want the reader's %v", stats.LastUsedAt, lastUsed)
	}
	if f.usage.statsCalls != 1 || f.usage.statsID != item.ID {
		t.Fatalf("reader calls = %d id = %d, want one call for %d", f.usage.statsCalls, f.usage.statsID, item.ID)
	}
}

// TestItemUsageNeverUsedItem passes the reader's zero aggregates through: an
// item without usage rows reads as {0, nil}, never as an error.
func TestItemUsageNeverUsedItem(t *testing.T) {
	f := newFixture(t)
	item := approvedItem(7002, knowledge.ScopeGlobal, "")
	f.repo.items[item.ID] = item
	f.usage.stats = knowledgeapp.UsageStats{}

	stats, err := f.svc.ItemUsage(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("ItemUsage: %v", err)
	}
	if stats.InjectionCount != 0 || stats.LastUsedAt != nil {
		t.Fatalf("stats = %+v, want {0, nil}", stats)
	}
	if f.usage.statsCalls != 1 {
		t.Fatalf("reader calls = %d, want 1", f.usage.statsCalls)
	}
}

// TestItemUsageWithoutReaderIsUnavailable: a nil usage repository must fail
// loudly with the typed 503 unavailable (never silently report "never used",
// constitution VII.6). The error CLASS is pinned, not just err != nil (T9 minor②).
func TestItemUsageWithoutReaderIsUnavailable(t *testing.T) {
	f := newFixture(t)
	svc := knowledgeapp.NewService(f.repo, nil, nil, nil, nil, nil,
		fixedClock{now: testNow}, &seqIDs{next: 1000})
	_, err := svc.ItemUsage(context.Background(), 7001)
	if err == nil {
		t.Fatal("ItemUsage without a usage reader = nil error, want unavailable")
	}
	typed := platform.AsError(err)
	if typed.Status != 503 || typed.Code != "unavailable" || !typed.Retryable {
		t.Fatalf("ItemUsage without reader error = %d/%s retryable=%v, want 503/unavailable/retryable", typed.Status, typed.Code, typed.Retryable)
	}
}

// TestItemUsagePropagatesReaderError: a failing reader must surface the error,
// never degrade to zero stats that would read as "never used".
func TestItemUsagePropagatesReaderError(t *testing.T) {
	f := newFixture(t)
	f.usage.statsErr = errors.New("usage read failed")
	stats, err := f.svc.ItemUsage(context.Background(), 7001)
	if err == nil {
		t.Fatal("ItemUsage with a failing reader = nil error, want the reader error")
	}
	if stats.InjectionCount != 0 || stats.LastUsedAt != nil {
		t.Fatalf("stats on error = %+v, want the zero value", stats)
	}
}

// TestItemUsageRejectsInvalidID: a non-positive id is a 400 field error and
// must not reach the reader.
func TestItemUsageRejectsInvalidID(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ItemUsage(context.Background(), 0); err == nil {
		t.Fatal("ItemUsage(0) = nil error, want invalid id")
	}
	if f.usage.statsCalls != 0 {
		t.Fatalf("reader calls = %d, want 0 for an invalid id", f.usage.statsCalls)
	}
}
