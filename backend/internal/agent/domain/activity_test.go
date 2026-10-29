package domain_test

import (
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestChannelForSession(t *testing.T) {
	if agent.EventType != "agent.activity" {
		t.Fatalf("EventType=%q want agent.activity", agent.EventType)
	}
	cases := []struct {
		sessionID int64
		want      string
	}{
		{sessionID: 1, want: "agent:1"},
		{sessionID: 42, want: "agent:42"},
		{sessionID: 9223372036854775807, want: "agent:9223372036854775807"},
	}
	for _, tc := range cases {
		if got := agent.ChannelForSession(tc.sessionID); got != tc.want {
			t.Fatalf("ChannelForSession(%d)=%q want %q", tc.sessionID, got, tc.want)
		}
		// The events table limits channels to 64 characters; the numeric
		// suffix is bounded by int64 so this always holds.
		if len(agent.ChannelForSession(tc.sessionID)) > 64 {
			t.Fatalf("channel for session %d exceeds 64 characters", tc.sessionID)
		}
	}
}

func TestActivityKindsAreDistinct(t *testing.T) {
	kinds := []string{
		agent.KindRunStarted,
		agent.KindUserMessage,
		agent.KindThinking,
		agent.KindText,
		agent.KindToolUse,
		agent.KindToolResult,
		agent.KindUsage,
		agent.KindDecision,
		agent.KindRunFinished,
	}
	if len(kinds) != 9 {
		t.Fatalf("activity kind count=%d want 9", len(kinds))
	}
	seen := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if kind == "" {
			t.Fatal("activity kind must not be empty")
		}
		if strings.ContainsAny(kind, " \t\n") {
			t.Fatalf("activity kind %q must be a single token", kind)
		}
		if seen[kind] {
			t.Fatalf("activity kind %q appears twice", kind)
		}
		seen[kind] = true
	}
}
