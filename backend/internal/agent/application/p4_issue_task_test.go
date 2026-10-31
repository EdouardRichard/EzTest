package agentapp

import (
	"context"
	"reflect"
	"testing"
)

func TestP4IssueTaskContextIsPayloadOnly(t *testing.T) {
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

func TestP4IssueTaskContextMissingPayloadKeepsOnlyIDs(t *testing.T) {
	value, ok := IssueTaskContext(TaskRef{VersionID: 42, Type: WorkTypeIssueTriage, Payload: map[string]any{}})
	if !ok {
		t.Fatal("IssueTaskContext did not claim an issue.triage task")
	}
	want := map[string]any{"version_id": "42"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("IssueTaskContext(empty payload) = %#v, want %#v", value, want)
	}
}

func TestP4IssueTaskContextIgnoresOtherTypes(t *testing.T) {
	for _, workType := range []string{WorkTypeReportWave, WorkTypeReportVersion, WorkTypeRequirementClose, "unknown"} {
		value, ok := IssueTaskContext(TaskRef{VersionID: 42, Type: workType, Payload: map[string]any{"issue_id": "501"}})
		if ok || value != nil {
			t.Fatalf("IssueTaskContext claimed %q: %#v", workType, value)
		}
	}
}

func TestP4IssueTaskContextRegistersThroughPayloadOnlyHook(t *testing.T) {
	resetP4TaskContextsForTest(t)
	RegisterP4TaskContext(func(_ context.Context, ref TaskRef) (any, bool) {
		return IssueTaskContext(ref)
	})

	value, ok := P4TaskContext(context.Background(), TaskRef{
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
