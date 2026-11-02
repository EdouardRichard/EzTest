package platform_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestAgentRunContextRoundTrip(t *testing.T) {
	ctx := platform.WithAgentRun(context.Background(), 7001, 8001)
	run, ok := platform.AgentRunFrom(ctx)
	if !ok || run.RunID != 7001 || run.WorkItemID != 8001 {
		t.Fatalf("agent run = %#v, %v", run, ok)
	}
	if _, ok := platform.AgentRunFrom(context.Background()); ok {
		t.Fatal("empty context reported an agent run")
	}
	if got := platform.WithAgentRun(ctx, 0, 0); got != ctx {
		t.Fatal("zero run id replaced the context")
	}
}
