package agentapp

import (
	"reflect"
	"testing"
)

func TestP4ClosureTaskContextRequirementClose(t *testing.T) {
	value, ok := ClosureTaskContext(TaskRef{
		VersionID: 42,
		Type:      WorkTypeRequirementClose,
		Payload: map[string]any{
			"requirement_id":         "1001",
			"requirement_version_id": "1002",
			"close_round":            float64(1),
		},
	})
	if !ok {
		t.Fatal("ClosureTaskContext did not claim a requirement.close task")
	}
	want := map[string]any{
		"version_id":             "42",
		"requirement_id":         "1001",
		"requirement_version_id": "1002",
		"close_round":            float64(1),
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("ClosureTaskContext(requirement.close) = %#v, want %#v", value, want)
	}
}

func TestP4ClosureTaskContextVersionClose(t *testing.T) {
	value, ok := ClosureTaskContext(TaskRef{
		VersionID: 42,
		Type:      WorkTypeVersionClose,
		Payload: map[string]any{
			"version_id":            "42",
			"report_revision_id":    "9001",
			"predicate_fingerprint": "abc123",
		},
	})
	if !ok {
		t.Fatal("ClosureTaskContext did not claim a version.close task")
	}
	want := map[string]any{
		"version_id":            "42",
		"report_revision_id":    "9001",
		"predicate_fingerprint": "abc123",
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("ClosureTaskContext(version.close) = %#v, want %#v", value, want)
	}
}

func TestP4ClosureTaskContextIgnoresOtherTypes(t *testing.T) {
	for _, workType := range []string{WorkTypeIssueTriage, WorkTypeReportWave, WorkTypeExecuteCase, "unknown"} {
		value, ok := ClosureTaskContext(TaskRef{VersionID: 42, Type: workType})
		if ok {
			t.Fatalf("ClosureTaskContext claimed %q: %#v", workType, value)
		}
		if value != nil {
			t.Fatalf("ClosureTaskContext(%q) value = %#v, want nil", workType, value)
		}
	}
}

func TestP4ClosureTaskContextIsPayloadOnly(t *testing.T) {
	// The provider must never invent data that is not in the work-item
	// reference: missing keys stay missing instead of being materialized as
	// zero IDs.
	value, ok := ClosureTaskContext(TaskRef{
		VersionID: 42,
		Type:      WorkTypeRequirementClose,
		Payload:   map[string]any{},
	})
	if !ok {
		t.Fatal("ClosureTaskContext did not claim a requirement.close task")
	}
	want := map[string]any{"version_id": "42"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("ClosureTaskContext(empty payload) = %#v, want %#v", value, want)
	}
}
