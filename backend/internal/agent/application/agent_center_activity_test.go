package agentapp_test

// v0.2.4-D T10 (SC-D5): AgentCenterService.RunActivity is the PRODUCTION read
// side of the agent_activity fact ledger. Without it the table stays write-only
// and V-0019 (dead/unwired tables) stays open.

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func activityLedgerRows(runID int64) []agent.Activity {
	ended := time.Date(2026, 11, 17, 10, 0, 2, 0, time.UTC)
	return []agent.Activity{
		{
			ID: 1, RunID: runID, Seq: 1, Type: agent.ActivityTypeToolCall,
			Title:     "requirement.read",
			StartedAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
			EndedAt:   &ended,
			Payload:   map[string]any{"tool": "requirement.read"},
		},
		{
			ID: 2, RunID: runID, Seq: 2, Type: agent.ActivityTypeCompaction,
			Title:     "context compaction (pressure)",
			StartedAt: time.Date(2026, 11, 17, 10, 0, 3, 0, time.UTC),
			Payload:   map[string]any{"trigger": "pressure", "before_tokens": 4000, "after_tokens": 900},
		},
	}
}

func TestRunActivityReturnsTheFactLedger(t *testing.T) {
	runID := int64(4242)
	reader := &fakeAgentCenterReader{activity: activityLedgerRows(runID)}
	service := agentapp.NewAgentCenterService(reader, nil, nil)

	rows, err := service.RunActivity(context.Background(), runID)
	if err != nil {
		t.Fatalf("RunActivity: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the whole ledger (no hidden truncation)", len(rows))
	}
	if rows[0].Type != agent.ActivityTypeToolCall || rows[1].Type != agent.ActivityTypeCompaction {
		t.Fatalf("row types = %q / %q", rows[0].Type, rows[1].Type)
	}
	if reader.activityCalls != 1 || reader.lastActivityRunID != runID {
		t.Fatalf("reader calls = %d for run %d, want 1 for %d",
			reader.activityCalls, reader.lastActivityRunID, runID)
	}
}

func TestRunActivityValidatesTheRunID(t *testing.T) {
	reader := &fakeAgentCenterReader{}
	service := agentapp.NewAgentCenterService(reader, nil, nil)
	for _, runID := range []int64{0, -1} {
		_, err := service.RunActivity(context.Background(), runID)
		var typed *platform.Error
		if !errors.As(err, &typed) || typed.Status != 400 {
			t.Fatalf("RunActivity(%d) err = %v, want a 400 field error", runID, err)
		}
	}
	if reader.activityCalls != 0 {
		t.Fatalf("an invalid run id reached the reader %d times", reader.activityCalls)
	}
}

func TestRunActivityWithoutReaderIsUnavailable(t *testing.T) {
	service := agentapp.NewAgentCenterService(nil, nil, nil)
	_, err := service.RunActivity(context.Background(), 42)
	var typed *platform.Error
	if !errors.As(err, &typed) || typed.Status != 503 {
		t.Fatalf("err = %v, want 503 when the reader is not wired", err)
	}
}

func TestRunActivityPropagatesReadErrors(t *testing.T) {
	wantErr := errors.New("store down")
	reader := &fakeAgentCenterReader{activityErr: wantErr}
	service := agentapp.NewAgentCenterService(reader, nil, nil)
	if _, err := service.RunActivity(context.Background(), 42); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the reader error surfaced", err)
	}
}
