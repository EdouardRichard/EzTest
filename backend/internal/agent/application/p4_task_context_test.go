package agentapp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// P4 task-context providers are payload-only by ruling: the registration hook
// takes no service dependencies, so everything a provider returns must derive
// from TaskRef (version/type/raw payload). This compile-time pin fails the
// build if a deps parameter is ever added to the hook.
var _ func(func(context.Context, TaskRef) (any, bool)) = RegisterP4TaskContext

// resetP4TaskContextsForTest isolates a test from the process-global P4
// task-context provider list. Registration lasts for the whole test process, so
// without isolation a provider registered by one test could claim a work-item
// type in another test when the binary runs with -shuffle=on. The snapshot is
// restored on cleanup so other test files keep the state they started with.
func resetP4TaskContextsForTest(t *testing.T) {
	t.Helper()
	p4TaskContextMu.Lock()
	saved := p4TaskContextProviders
	p4TaskContextProviders = nil
	p4TaskContextMu.Unlock()
	t.Cleanup(func() {
		p4TaskContextMu.Lock()
		p4TaskContextProviders = saved
		p4TaskContextMu.Unlock()
	})
}

// TaskRef is the complete provider input: adding a service port here would
// re-introduce the deps problem the ruling excludes.
func TestTaskRefCarriesOnlyWorkItemReference(t *testing.T) {
	typ := reflect.TypeOf(TaskRef{})
	fields := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	want := []string{"VersionID", "Type", "Payload"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("TaskRef fields = %v, want %v (payload-only ruling)", fields, want)
	}
}

func TestP4TaskContextReturnsOnlyTaskRefDerivedData(t *testing.T) {
	resetP4TaskContextsForTest(t)

	RegisterP4TaskContext(func(_ context.Context, task TaskRef) (any, bool) {
		if task.Type != "p4.test.payload_only" {
			return nil, false
		}
		return map[string]any{
			"version_id": idText(task.VersionID),
			"issue_id":   task.Payload["issue_id"],
		}, true
	})

	value, ok := P4TaskContext(context.Background(), TaskRef{
		VersionID: 42,
		Type:      "p4.test.payload_only",
		Payload:   map[string]any{"issue_id": "501"},
	})
	if !ok {
		t.Fatal("P4TaskContext(payload_only) did not match its provider")
	}
	details, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("P4TaskContext value = %#v, want a JSON-ready map", value)
	}
	want := map[string]any{"version_id": "42", "issue_id": "501"}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("P4TaskContext payload = %#v, want %#v", details, want)
	}
}

func TestP4TaskContextUnregisteredReturnsFalse(t *testing.T) {
	resetP4TaskContextsForTest(t)

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
	resetP4TaskContextsForTest(t)

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
	resetP4TaskContextsForTest(t)

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
