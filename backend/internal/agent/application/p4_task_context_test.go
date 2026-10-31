package agentapp

import (
	"context"
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestP4TaskContextUnregisteredReturnsFalse(t *testing.T) {
	value, ok := P4TaskContext(context.Background(), TaskRef{
		VersionID: 42,
		Type:      "p4.test.unregistered",
		Payload:   map[string]any{"issue_id": "501"},
	})
	if ok {
		t.Fatalf("P4TaskContext(unregistered) returned %#v, want no match", value)
	}
	if value != nil {
		t.Fatalf("P4TaskContext(unregistered) value = %#v, want nil", value)
	}
}

func TestP4TaskContextFallbackWhenP3SwitchMisses(t *testing.T) {
	var seen TaskRef
	RegisterP4TaskContext(func(_ context.Context, task TaskRef) (any, bool) {
		if task.Type != WorkTypeIssueTriage {
			return nil, false
		}
		seen = task
		return map[string]any{"issue_id": "501"}, true
	})

	state := &runState{item: agent.WorkItem{
		VersionID: 42,
		Type:      WorkTypeIssueTriage,
		Payload:   json.RawMessage(`{"issue_id":"501"}`),
	}}
	task := (&Runner{}).taskContext(context.Background(), state)

	if task.Type != WorkTypeIssueTriage {
		t.Fatalf("task context type = %q, want %q", task.Type, WorkTypeIssueTriage)
	}
	if seen.VersionID != 42 || seen.Type != WorkTypeIssueTriage {
		t.Fatalf("provider task ref = %#v", seen)
	}
	if seen.Payload["issue_id"] != "501" {
		t.Fatalf("provider task payload = %#v, want raw payload issue_id=501", seen.Payload)
	}
	details, ok := state.input.P4Task.(map[string]any)
	if !ok {
		t.Fatalf("input P4 task = %#v, want registered payload", state.input.P4Task)
	}
	if details["issue_id"] != "501" {
		t.Fatalf("input P4 task payload = %#v", details)
	}
}

func TestP4TaskContextDoesNotOverrideHandledP3Types(t *testing.T) {
	RegisterP4TaskContext(func(_ context.Context, task TaskRef) (any, bool) {
		if task.Type != WorkTypeAnalyzeCoverage {
			return nil, false
		}
		return map[string]any{"unexpected": true}, true
	})

	state := &runState{item: agent.WorkItem{VersionID: 7, Type: WorkTypeAnalyzeCoverage}}
	task := (&Runner{}).taskContext(context.Background(), state)

	if task.Goal == "" {
		t.Fatal("P3 task context description was not applied")
	}
	if state.input.P4Task != nil {
		t.Fatalf("registered P4 provider overrode a P3 task context: %#v", state.input.P4Task)
	}
}

func TestP4ProfileTeams(t *testing.T) {
	cases := map[string]agent.Team{
		ProfileKeyIssueTriager:   agent.TeamDelivery,
		ProfileKeyReportWriter:   agent.TeamDelivery,
		ProfileKeyVersionSteward: agent.TeamSteward,
	}
	for profileKey, want := range cases {
		if got := teamForProfile(profileKey); got != want {
			t.Fatalf("teamForProfile(%q) = %q, want %q", profileKey, got, want)
		}
	}
}
