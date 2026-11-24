package agentapp

import (
	"context"
	"reflect"
	"testing"
)

func TestDeliveryIssueTaskContextIsPayloadOnly(t *testing.T) {
	value, ok := IssueTaskContext(TaskRef{
		VersionID: 42,
		Type:      WorkTypeIssueTriage,
		Payload: map[string]any{
			"issue_id":   "501",
			"version_id": "42",
		},
	})
	if !ok {
		t.Fatal("IssueTaskContext did not claim an issue.triage task")
	}
	want := map[string]any{"version_id": "42", "issue_id": "501"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("IssueTaskContext = %#v, want %#v", value, want)
	}
}

func TestDeliveryIssueTaskContextMissingPayloadKeepsOnlyIDs(t *testing.T) {
	value, ok := IssueTaskContext(TaskRef{VersionID: 42, Type: WorkTypeIssueTriage, Payload: map[string]any{}})
	if !ok {
		t.Fatal("IssueTaskContext did not claim an issue.triage task")
	}
	want := map[string]any{"version_id": "42"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("IssueTaskContext(empty payload) = %#v, want %#v", value, want)
	}
}

func TestDeliveryIssueTaskContextIgnoresOtherTypes(t *testing.T) {
	for _, workType := range []string{WorkTypeReportWave, WorkTypeReportVersion, WorkTypeExecuteCase, "unknown"} {
		value, ok := IssueTaskContext(TaskRef{VersionID: 42, Type: workType, Payload: map[string]any{"issue_id": "501"}})
		if ok || value != nil {
			t.Fatalf("IssueTaskContext claimed %q: %#v", workType, value)
		}
	}
}

func TestDeliveryIssueTaskContextRegistersThroughPayloadOnlyHook(t *testing.T) {
	resetDeliveryTaskContextsForTest(t)
	RegisterDeliveryTaskContext(func(_ context.Context, ref TaskRef) (any, bool) {
		return IssueTaskContext(ref)
	})

	value, ok := DeliveryTaskContext(context.Background(), TaskRef{
		VersionID: 42,
		Type:      WorkTypeIssueTriage,
		Payload:   map[string]any{"issue_id": "501"},
	})
	if !ok {
		t.Fatal("registered issue task-context provider did not claim issue.triage")
	}
	want := map[string]any{"version_id": "42", "issue_id": "501"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("registered issue task context = %#v, want %#v", value, want)
	}
}
