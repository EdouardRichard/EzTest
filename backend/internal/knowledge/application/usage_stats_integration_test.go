//go:build integration

package knowledgeapp_test

import (
	"context"
	"testing"
	"time"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	knowledgepg "github.com/EdouardRichard/EzTest/backend/internal/knowledge/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// stepClock is a mutable test clock so two production RecordUsage calls can
// land at distinct, known timestamps.
type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }

// TestItemUsageReadsRecordedUsage drives the full production chain against a
// real database: RecordUsage (the existing write side) appends knowledge_usage
// rows, and ItemUsage must read those exact aggregates back through the real
// UsageRepository.StatsByItem SQL. Both reader-existence mutations fail here:
// deleting the service→reader call and deleting the infra reader→SQL each
// collapse the aggregates to zero (V-0019 H share, R-H8).
func TestItemUsageReadsRecordedUsage(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	clk := &stepClock{now: testNow}
	svc := knowledgeapp.NewService(
		knowledgepg.NewRepository(pool),
		knowledgepg.NewUsageRepository(pool),
		nil, nil, nil, nil, clk, &seqIDs{next: 5000})
	actor := platform.Actor{ID: 7, Role: "admin"}

	used, err := svc.Create(ctx, actor, knowledgeapp.CreateInput{
		Title: "Usage read surface", Content: "injection accounting", Scope: knowledge.ScopeGlobal,
	})
	if err != nil {
		t.Fatalf("create used item: %v", err)
	}
	unused, err := svc.Create(ctx, actor, knowledgeapp.CreateInput{
		Title: "Never injected", Content: "no usage rows", Scope: knowledge.ScopeGlobal,
	})
	if err != nil {
		t.Fatalf("create unused item: %v", err)
	}

	if err := svc.RecordUsage(ctx, 900, 901, []knowledgeapp.Injected{{ID: used.ID, Tokens: 4}}); err != nil {
		t.Fatalf("first RecordUsage: %v", err)
	}
	second := testNow.Add(time.Hour)
	clk.now = second
	if err := svc.RecordUsage(ctx, 900, 902, []knowledgeapp.Injected{{ID: used.ID, Tokens: 4}}); err != nil {
		t.Fatalf("second RecordUsage: %v", err)
	}

	stats, err := svc.ItemUsage(ctx, used.ID)
	if err != nil {
		t.Fatalf("ItemUsage: %v", err)
	}
	if stats.InjectionCount != 2 {
		t.Fatalf("InjectionCount = %d, want 2 recorded injections", stats.InjectionCount)
	}
	if stats.LastUsedAt == nil || !stats.LastUsedAt.Equal(second) {
		t.Fatalf("LastUsedAt = %v, want the second injection time %v", stats.LastUsedAt, second)
	}

	empty, err := svc.ItemUsage(ctx, unused.ID)
	if err != nil {
		t.Fatalf("ItemUsage unused: %v", err)
	}
	if empty.InjectionCount != 0 || empty.LastUsedAt != nil {
		t.Fatalf("unused stats = %+v, want {0, nil}", empty)
	}
}
