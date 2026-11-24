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
var _ func(func(context.Context, TaskRef) (any, bool)) = RegisterDeliveryTaskContext

// resetDeliveryTaskContextsForTest isolates a test from the process-global P4
// task-context provider list. Registration lasts for the whole test process, so
// without isolation a provider registered by one test could claim a work-item
// type in another test when the binary runs with -shuffle=on. The snapshot is
// restored on cleanup so other test files keep the state they started with.
func resetDeliveryTaskContextsForTest(t *testing.T) {
	t.Helper()
	taskContextMu.Lock()
	saved := taskContextProviders
	taskContextProviders = nil
	taskContextMu.Unlock()
	t.Cleanup(func() {
		taskContextMu.Lock()
		taskContextProviders = saved
		taskContextMu.Unlock()
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

func TestDeliveryTaskContextReturnsOnlyTaskRefDerivedData(t *testing.T) {
	resetDeliveryTaskContextsForTest(t)

	RegisterDeliveryTaskContext(func(_ context.Context, task TaskRef) (any, bool) {
		if task.Type != "p4.test.payload_only" {
			return nil, false
		}
		return map[string]any{
			"version_id": idText(task.VersionID),
			"issue_id":   task.Payload["issue_id"],
		}, true
	})

	value, ok := DeliveryTaskContext(context.Background(), TaskRef{
		VersionID: 42,
		Type:      "p4.test.payload_only",
		Payload:   map[string]any{"issue_id": "501"},
	})
	if !ok {
		t.Fatal("DeliveryTaskContext(payload_only) did not match its provider")
	}
	details, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("DeliveryTaskContext value = %#v, want a JSON-ready map", value)
	}
	want := map[string]any{"version_id": "42", "issue_id": "501"}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("DeliveryTaskContext payload = %#v, want %#v", details, want)
	}
}

func TestDeliveryTaskContextUnregisteredReturnsFalse(t *testing.T) {
	resetDeliveryTaskContextsForTest(t)

	value, ok := DeliveryTaskContext(context.Background(), TaskRef{
		VersionID: 42,
		Type:      "p4.test.unregistered",
		Payload:   map[string]any{"issue_id": "501"},
	})
	if ok {
		t.Fatalf("DeliveryTaskContext(unregistered) returned %#v, want no match", value)
	}
	if value != nil {
		t.Fatalf("DeliveryTaskContext(unregistered) value = %#v, want nil", value)
	}
}

func TestDeliveryTaskContextFallbackWhenExecutionSwitchMisses(t *testing.T) {
	resetDeliveryTaskContextsForTest(t)

	var seen TaskRef
	RegisterDeliveryTaskContext(func(_ context.Context, task TaskRef) (any, bool) {
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
	details, ok := state.input.DeliveryTask.(map[string]any)
	if !ok {
		t.Fatalf("input delivery task = %#v, want registered payload", state.input.DeliveryTask)
	}
	if details["issue_id"] != "501" {
		t.Fatalf("input delivery task payload = %#v", details)
	}
}

func TestDeliveryTaskContextDoesNotOverrideHandledExecutionTypes(t *testing.T) {
	resetDeliveryTaskContextsForTest(t)

	RegisterDeliveryTaskContext(func(_ context.Context, task TaskRef) (any, bool) {
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
	if state.input.DeliveryTask != nil {
		t.Fatalf("registered delivery provider overrode a base task context: %#v", state.input.DeliveryTask)
	}
}

func TestDeliveryProfileTeams(t *testing.T) {
	cases := map[string]agent.Team{
		ProfileKeyIssueTriager: agent.TeamDelivery,
		ProfileKeyReportWriter: agent.TeamDelivery,
	}
	for profileKey, want := range cases {
		if got := teamForProfile(profileKey); got != want {
			t.Fatalf("teamForProfile(%q) = %q, want %q", profileKey, got, want)
		}
	}
}
