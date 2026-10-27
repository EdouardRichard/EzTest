package eventing_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versioneventing "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/eventing"
)

// fakeEventPublisher captures the mapped eventing envelope.
type fakeEventPublisher struct {
	events     []eventingdomain.NewEvent
	publishErr error
}

// Publish records the event and returns the configured failure.
func (f *fakeEventPublisher) Publish(_ context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error) {
	f.events = append(f.events, event)
	if f.publishErr != nil {
		return eventingdomain.Event{}, f.publishErr
	}
	return eventingdomain.Event{Channel: event.Channel, Type: event.Type}, nil
}

// detailMap decodes one event detail for assertions.
func detailMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("detail %s is not a JSON object: %v", raw, err)
	}
	return detail
}

// TestEventPublisherSetsVersionChannelAndRefs pins the version-channel mapping:
// the adapter fixes channel=version:<id>, sets the string version_id and maps
// the single requirement reference, while actor/source travel in detail.
func TestEventPublisherSetsVersionChannelAndRefs(t *testing.T) {
	ctx := context.Background()
	recorder := &fakeEventPublisher{}
	publisher := versioneventing.NewEventPublisher(recorder)

	requirementID := int64(77)
	sessionID := int64(88)
	decisionID := int64(99)
	attachmentID := int64(111)
	err := publisher.Publish(ctx, application.VersionEvent{
		VersionID:        42,
		Type:             "requirement.added",
		Summary:          "requirement added",
		RefRequirementID: &requirementID,
		RefSessionID:     &sessionID,
		RefDecisionID:    &decisionID,
		RefAttachmentID:  &attachmentID,
		ActorID:          7,
		Source:           "ai",
		Detail:           map[string]any{"status": "draft", "changed": []string{"title"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("published %d events, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Channel != "version:42" {
		t.Fatalf("channel=%q want version:42", event.Channel)
	}
	if event.VersionID == nil || *event.VersionID != "42" {
		t.Fatalf("version_id=%v want \"42\"", event.VersionID)
	}
	if event.Type != "requirement.added" || event.Summary != "requirement added" {
		t.Fatalf("type/summary=%q/%q", event.Type, event.Summary)
	}
	if len(event.Refs.Requirement) != 1 || event.Refs.Requirement[0] != "77" {
		t.Fatalf("refs.requirement=%v want [77]", event.Refs.Requirement)
	}
	if len(event.Refs.Case) != 0 || len(event.Refs.Evidence) != 0 || len(event.Refs.Code) != 0 {
		t.Fatalf("unexpected refs: %+v", event.Refs)
	}
	detail := detailMap(t, event.Detail)
	if detail["status"] != "draft" {
		t.Fatalf("detail status=%v want draft", detail["status"])
	}
	if detail["actorId"] != float64(7) {
		t.Fatalf("detail actorId=%v want 7", detail["actorId"])
	}
	if detail["source"] != "ai" {
		t.Fatalf("detail source=%v want ai", detail["source"])
	}
	// P3 session/decision/attachment ids belong to detail, not refs, because the
	// envelope's frozen refs only carry requirement/code/case/evidence.
	if _, ok := detail["sessionId"]; ok {
		t.Fatalf("detail unexpectedly carries sessionId: %v", detail)
	}
}

// TestEventPublisherDetailContract covers the nil-detail default, the caller's
// detail preservation and publish failures.
func TestEventPublisherDetailContract(t *testing.T) {
	ctx := context.Background()

	t.Run("nil detail becomes the empty object", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := versioneventing.NewEventPublisher(recorder)
		if err := publisher.Publish(ctx, application.VersionEvent{VersionID: 5, Type: "version.created", Summary: "created"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		event := recorder.events[0]
		if string(event.Detail) != "{}" {
			t.Fatalf("detail=%q want {}", event.Detail)
		}
		if event.VersionID == nil || *event.VersionID != "5" {
			t.Fatalf("version_id=%v want \"5\"", event.VersionID)
		}
		if len(event.Refs.Requirement) != 0 {
			t.Fatalf("refs=%v want no requirement reference", event.Refs.Requirement)
		}
	})

	t.Run("an existing actorId and source in detail are not overwritten", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := versioneventing.NewEventPublisher(recorder)
		if err := publisher.Publish(ctx, application.VersionEvent{
			VersionID: 5, Type: "version.updated", Summary: "updated", ActorID: 3, Source: "user",
			Detail: map[string]any{"source": "ai", "actorId": 9},
		}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		detail := detailMap(t, recorder.events[0].Detail)
		if detail["source"] != "ai" || detail["actorId"] != float64(9) {
			t.Fatalf("detail=%v want the caller's values preserved", detail)
		}
	})

	t.Run("publish failures are returned", func(t *testing.T) {
		publisher := versioneventing.NewEventPublisher(&fakeEventPublisher{publishErr: errors.New("event store down")})
		if err := publisher.Publish(ctx, application.VersionEvent{VersionID: 5, Type: "version.created"}); err == nil {
			t.Fatal("Publish error=nil want the event store failure")
		}
	})

	t.Run("a nil publisher is a disabled no-op", func(t *testing.T) {
		if err := versioneventing.NewEventPublisher(nil).Publish(ctx, application.VersionEvent{VersionID: 5, Type: "version.created"}); err != nil {
			t.Fatalf("Publish(nil publisher)=%v want nil", err)
		}
	})
}
