package eventing_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapeventing "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/eventing"
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

// TestEventPublisherMapsVersionChannelAndRefs pins the frozen envelope: channel
// version:<id>, the decimal string version id, the case refs and the actor/source
// detail stamp.
func TestEventPublisherMapsVersionChannelAndRefs(t *testing.T) {
	ctx := context.Background()
	recorder := &fakeEventPublisher{}
	publisher := mindmapeventing.NewEventPublisher(recorder)
	var _ mindmapapp.EventPublisher = publisher

	err := publisher.Publish(ctx, mindmapapp.Event{
		VersionID:  42,
		Type:       "case.created",
		Summary:    "case created",
		RefCaseIDs: []int64{1001, 1002},
		ActorID:    7,
		Source:     "ai",
		Detail:     map[string]any{"count": 2},
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
	if event.Type != "case.created" || event.Summary != "case created" {
		t.Fatalf("type/summary=%q/%q", event.Type, event.Summary)
	}
	if len(event.Refs.Case) != 2 || event.Refs.Case[0] != "1001" || event.Refs.Case[1] != "1002" {
		t.Fatalf("refs.case=%v want [1001 1002]", event.Refs.Case)
	}
	detail := eventDetail(t, event.Detail)
	if detail["count"] != float64(2) {
		t.Fatalf("detail count=%v want 2", detail["count"])
	}
	if detail["actorId"] != float64(7) || detail["source"] != "ai" {
		t.Fatalf("detail=%v want actorId 7 and source ai", detail)
	}
}

// TestEventPublisherEnforcesEnvelopeLimits pins the summary truncation, the refs
// cap and the detail size rejection.
func TestEventPublisherEnforcesEnvelopeLimits(t *testing.T) {
	ctx := context.Background()

	t.Run("summary is truncated to 500 runes", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := mindmapeventing.NewEventPublisher(recorder)
		summary := strings.Repeat("图", 600)
		if err := publisher.Publish(ctx, mindmapapp.Event{VersionID: 1, Type: "case.updated", Summary: summary}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		got := recorder.events[0].Summary
		if runes := []rune(got); len(runes) != 500 {
			t.Fatalf("summary is %d runes, want 500", len(runes))
		}
		if !strings.HasPrefix(summary, got) {
			t.Fatal("truncated summary is not a prefix of the original")
		}
	})

	t.Run("case refs are capped at 20", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := mindmapeventing.NewEventPublisher(recorder)
		ids := make([]int64, 0, 25)
		for i := int64(1); i <= 25; i++ {
			ids = append(ids, i)
		}
		if err := publisher.Publish(ctx, mindmapapp.Event{VersionID: 1, Type: "case.updated", Summary: "u", RefCaseIDs: ids}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if len(recorder.events[0].Refs.Case) != 20 {
			t.Fatalf("refs.case=%d want 20", len(recorder.events[0].Refs.Case))
		}
	})

	t.Run("requirement and code refs are mapped", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := mindmapeventing.NewEventPublisher(recorder)
		if err := publisher.Publish(ctx, mindmapapp.Event{
			VersionID: 1, Type: "case.revalidation_marked", Summary: "u",
			RefCaseIDs:        []int64{9},
			RefRequirementIDs: []int64{7, 8},
			RefCodeRefs:       []string{"src/refund.ts"},
		}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		refs := recorder.events[0].Refs
		if len(refs.Case) != 1 || refs.Case[0] != "9" {
			t.Fatalf("refs.case=%v want [9]", refs.Case)
		}
		if len(refs.Requirement) != 2 || refs.Requirement[0] != "7" || refs.Requirement[1] != "8" {
			t.Fatalf("refs.requirement=%v want [7 8]", refs.Requirement)
		}
		if len(refs.Code) != 1 || refs.Code[0] != "src/refund.ts" {
			t.Fatalf("refs.code=%v want [src/refund.ts]", refs.Code)
		}
	})

	t.Run("oversized detail is rejected", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := mindmapeventing.NewEventPublisher(recorder)
		err := publisher.Publish(ctx, mindmapapp.Event{
			VersionID: 1,
			Type:      "case.updated",
			Summary:   "u",
			Detail:    map[string]any{"blob": strings.Repeat("x", 70<<10)},
		})
		if err == nil {
			t.Fatal("Publish error=nil want the detail size rejection")
		}
		if len(recorder.events) != 0 {
			t.Fatalf("published %d events, want none", len(recorder.events))
		}
	})
}

// TestEventPublisherDetailAndFailureContract pins the nil-detail default, the
// caller's actor/source precedence, publish failures and the disabled no-op.
func TestEventPublisherDetailAndFailureContract(t *testing.T) {
	ctx := context.Background()

	t.Run("nil detail becomes the empty object", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := mindmapeventing.NewEventPublisher(recorder)
		if err := publisher.Publish(ctx, mindmapapp.Event{VersionID: 5, Type: "case.deleted", Summary: "deleted"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if got := string(recorder.events[0].Detail); got != "{}" {
			t.Fatalf("detail=%q want {}", got)
		}
		if len(recorder.events[0].Refs.Case) != 0 {
			t.Fatalf("refs.case=%v want empty", recorder.events[0].Refs.Case)
		}
	})

	t.Run("caller detail wins over the actor and source stamp", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := mindmapeventing.NewEventPublisher(recorder)
		if err := publisher.Publish(ctx, mindmapapp.Event{
			VersionID: 5, Type: "case.updated", Summary: "u", ActorID: 3, Source: "user",
			Detail: map[string]any{"actorId": 9, "source": "ai"},
		}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		detail := eventDetail(t, recorder.events[0].Detail)
		if detail["actorId"] != float64(9) || detail["source"] != "ai" {
			t.Fatalf("detail=%v want the caller's values preserved", detail)
		}
	})

	t.Run("publish failures are returned", func(t *testing.T) {
		publisher := mindmapeventing.NewEventPublisher(&fakeEventPublisher{publishErr: errors.New("event store down")})
		if err := publisher.Publish(ctx, mindmapapp.Event{VersionID: 5, Type: "case.updated"}); err == nil {
			t.Fatal("Publish error=nil want the event store failure")
		}
	})

	t.Run("a nil publisher is a disabled no-op", func(t *testing.T) {
		if err := mindmapeventing.NewEventPublisher(nil).Publish(ctx, mindmapapp.Event{VersionID: 5, Type: "case.updated"}); err != nil {
			t.Fatalf("Publish(nil publisher)=%v want nil", err)
		}
	})
}

// eventDetail decodes one event detail for assertions.
func eventDetail(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("detail %s is not a JSON object: %v", raw, err)
	}
	return detail
}
