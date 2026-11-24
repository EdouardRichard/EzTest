//go:build integration

package agentpg

import (
	"context"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestQueryRunMessagesNoLimit pins the noLimit sentinel (H-E20): limit=noLimit
// reads the WHOLE run (the admin transcript must not be silently truncated),
// while a positive limit bounds the page. The double meaning this constant
// removes is that a bare `0` reads to a human as "zero rows" but means
// "unlimited" to the query.
func TestQueryRunMessagesNoLimit(t *testing.T) {
	pool := testutil.NewDB(t)
	generator, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	repo := NewRuntimeRepository(pool, generator)
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)

	run, err := repo.CreateRun(ctx, agent.AgentRun{
		ProfileKey:    "case_executor",
		Status:        agent.AgentRunRunning,
		OwnerInstance: "instance-1",
		LeaseEpoch:    1,
		Usage:         map[string]int{},
		StartedAt:     now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for index := 0; index < 3; index++ {
		if _, err := repo.AppendMessage(ctx, agent.AgentMessage{
			RunID:       &run.ID,
			Role:        agent.MessageRoleUser,
			Parts:       []agent.MessagePart{{Type: "text", Text: "m"}},
			StreamState: agent.MessageStreamStateComplete,
			CreatedAt:   now.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatalf("AppendMessage %d: %v", index, err)
		}
	}

	all, err := queryRunMessages(ctx, pool, run.ID, noLimit)
	if err != nil {
		t.Fatalf("queryRunMessages(noLimit): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("noLimit rows = %d, want 3 (the whole run)", len(all))
	}
	if all[0].ID >= all[1].ID || all[1].ID >= all[2].ID {
		t.Fatalf("rows are not in ascending id order: %d,%d,%d", all[0].ID, all[1].ID, all[2].ID)
	}

	one, err := queryRunMessages(ctx, pool, run.ID, 1)
	if err != nil {
		t.Fatalf("queryRunMessages(1): %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("limit=1 rows = %d, want 1", len(one))
	}
}
