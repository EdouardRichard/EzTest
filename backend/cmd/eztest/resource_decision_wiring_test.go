package main

// W1-14B fix round 1: the resource decision observer must be registered on both
// event delivery paths. The local event service only sees events published by
// this replica; the cross-instance listener loads events persisted by another
// replica. Like the reconciler wake, the write-back must be wired to both,
// otherwise a confirm/reject performed against another API replica never
// settles the proposed observations of this replica. This pins the composition
// root because no database is available in unit tests.

import (
	"os"
	"strings"
	"testing"
)

func TestResourceDecisionObserverWiredToBothDeliveryPaths(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	content := string(source)
	for _, fragment := range []string{
		"decisionObserver := executionapp.NewResourceDecisionObserver(",
		"eventSvc.Observe(decisionObserver)",
		"p7rt.Listener.Observe(decisionObserver)",
	} {
		if !strings.Contains(content, fragment) {
			t.Errorf("main.go is missing the W1-14B decision-observer wiring %q", fragment)
		}
	}
}
