package versionapp_test

import (
	"net/http"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestMarkRunningProjectsVersionOnce(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Projection")
	baselineEvents := len(env.publisher.events)

	if err := env.svc.MarkRunning(requestContext(), created.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	view, err := env.svc.Get(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if view.Status != version.StatusRunning {
		t.Fatalf("status = %q, want running", view.Status)
	}

	published := env.publisher.events[baselineEvents:]
	if len(published) != 4 {
		t.Fatalf("published events = %d, want 4 (started/status_changed on both channels): %#v", len(published), published)
	}
	byType := map[string][]eventingapp.Event{}
	for _, event := range published {
		byType[event.Type] = append(byType[event.Type], event)
	}
	for _, eventType := range []string{"version.started", "version.status_changed"} {
		events := byType[eventType]
		if len(events) != 2 {
			t.Fatalf("%s events = %d, want 2", eventType, len(events))
		}
		channels := map[string]bool{}
		for _, event := range events {
			channels[event.Channel] = true
			if event.Payload["from"] != "initial" || event.Payload["to"] != "running" {
				t.Fatalf("%s payload = %#v", eventType, event.Payload)
			}
			if event.Scope["version_id"] != idString(created.ID) {
				t.Fatalf("%s scope = %#v", eventType, event.Scope)
			}
			if event.ActorID != nil {
				t.Fatalf("%s actor = %#v, want nil", eventType, event.ActorID)
			}
		}
		if !channels["version:"+idString(created.ID)] || !channels["system"] {
			t.Fatalf("%s channels = %#v", eventType, channels)
		}
	}

	// Idempotent: a second projection publishes nothing.
	if err := env.svc.MarkRunning(requestContext(), created.ID); err != nil {
		t.Fatalf("second MarkRunning: %v", err)
	}
	if len(env.publisher.events) != baselineEvents+4 {
		t.Fatalf("second projection published %d extra events", len(env.publisher.events)-baselineEvents-4)
	}

	if err := env.svc.MarkRunning(requestContext(), 9999); err == nil {
		t.Fatal("MarkRunning on a missing version returned no error")
	} else if apiErr := apiError(t, err); apiErr.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", apiErr.Status)
	}
}

func TestGetIncludesP1Counts(t *testing.T) {
	env := newTestEnv(t)
	env.repo.stats = version.VersionStats{
		Requirements:      version.RequirementCounts{Total: 5, Draft: 2, Clarified: 3},
		ConversationCount: 4,
	}
	created := createVersion(t, env, "Counts")

	view, err := env.svc.Get(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if view.RequirementCounts.Total != 5 || view.RequirementCounts.Clarified != 3 || view.RequirementCounts.Draft != 2 {
		t.Fatalf("counts = %#v", view.RequirementCounts)
	}
	if view.ConversationCount != 4 {
		t.Fatalf("conversation count = %d", view.ConversationCount)
	}
	if view.PendingDesignCount != 3 {
		t.Fatalf("pending design count = %d, want clarified count", view.PendingDesignCount)
	}
}

func TestListInitialVersionsWithContentClampsLimit(t *testing.T) {
	env := newTestEnv(t)
	env.repo.initialWithContent = []int64{7, 3}

	ids, err := env.svc.ListInitialVersionsWithContent(requestContext(), 500)
	if err != nil {
		t.Fatalf("ListInitialVersionsWithContent: %v", err)
	}
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 3 {
		t.Fatalf("ids = %#v", ids)
	}
	if env.repo.lastInitialLimit != 100 {
		t.Fatalf("limit = %d, want clamped 100", env.repo.lastInitialLimit)
	}

	if _, err := env.svc.ListInitialVersionsWithContent(requestContext(), 0); err != nil {
		t.Fatalf("List with zero limit: %v", err)
	}
	if env.repo.lastInitialLimit != 1 {
		t.Fatalf("zero limit = %d, want clamped 1", env.repo.lastInitialLimit)
	}
}
