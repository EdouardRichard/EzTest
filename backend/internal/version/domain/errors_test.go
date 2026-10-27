package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorConstructors(t *testing.T) {
	invalid := NewInvalid("name", "invalid_name", "name is required")
	if invalid.Kind != KindInvalid || invalid.Field != "name" || invalid.Reason != "invalid_name" {
		t.Fatalf("NewInvalid = %#v", invalid)
	}
	if invalid.Error() == "" {
		t.Fatal("NewInvalid.Error() must not be empty")
	}

	notFound := NewNotFound("version", 42)
	if notFound.Kind != KindNotFound || notFound.Reason != "not_found" {
		t.Fatalf("NewNotFound = %#v", notFound)
	}
	if !strings.Contains(notFound.Error(), "version") || !strings.Contains(notFound.Error(), "42") {
		t.Fatalf("NewNotFound.Error() = %q, want resource and id", notFound.Error())
	}

	conflict := NewConflict("duplicate_relation", "relation already exists")
	if conflict.Kind != KindConflict || conflict.Reason != "duplicate_relation" {
		t.Fatalf("NewConflict = %#v", conflict)
	}

	state := NewStateConflict("version_state_conflict", "version is not in draft")
	if state.Kind != KindStateConflict || state.Reason != "version_state_conflict" {
		t.Fatalf("NewStateConflict = %#v", state)
	}

	dependency := NewDependency("has_dependencies", "requirement has dependents")
	if dependency.Kind != KindDependency || dependency.Reason != "has_dependencies" {
		t.Fatalf("NewDependency = %#v", dependency)
	}

	// errors.As must find the domain error through a wrap: the HTTP adapter
	// relies on it to map kinds onto the shared error envelope.
	wrapped := fmt.Errorf("service: %w", invalid)
	var target *Error
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As did not find the wrapped *domain.Error")
	}
	if target != invalid {
		t.Fatal("errors.As unwrapped a different error")
	}
}
