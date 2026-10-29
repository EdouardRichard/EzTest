package agentevent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// capturingPublisher records the events the sink publishes.
type capturingPublisher struct {
	events []eventing.NewEvent
	err    error
}

func (p *capturingPublisher) Publish(_ context.Context, event eventing.NewEvent) (eventing.Event, error) {
	p.events = append(p.events, event)
	if p.err != nil {
		return eventing.Event{}, p.err
	}
	return eventing.Event{Seq: int64(len(p.events)), Channel: event.Channel, Type: event.Type}, nil
}

func TestPublishActivityMapsEnvelope(t *testing.T) {
	publisher := &capturingPublisher{}
	sink := NewSink(publisher)
	err := sink.PublishActivity(context.Background(), agentapp.ActivityEvent{
		Channel:   "agent:7",
		AgentRole: "runtime-diagnostic",
		Summary:   "run started",
		SessionID: 7,
		Detail: map[string]any{
			"kind":   "run_started",
			"run_id": 9,
		},
	})
	if err != nil {
		t.Fatalf("PublishActivity: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events=%d want 1", len(publisher.events))
	}
	event := publisher.events[0]
	if event.Type != "agent.activity" {
		t.Fatalf("Type=%q want agent.activity", event.Type)
	}
	if event.Channel != "agent:7" {
		t.Fatalf("Channel=%q want agent:7", event.Channel)
	}
	if event.AgentRole == nil || *event.AgentRole != "runtime-diagnostic" {
		t.Fatalf("AgentRole=%v want runtime-diagnostic", event.AgentRole)
	}
	if event.SessionID == nil || *event.SessionID != "7" {
		t.Fatalf("SessionID=%v want the string %q", event.SessionID, "7")
	}
	if event.Summary != "run started" {
		t.Fatalf("Summary=%q", event.Summary)
	}
	var detail map[string]any
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		t.Fatalf("Detail=%s is not a JSON object: %v", event.Detail, err)
	}
	if detail["kind"] != "run_started" {
		t.Fatalf("Detail.kind=%v", detail["kind"])
	}
	if detail["run_id"] != float64(9) {
		t.Fatalf("Detail.run_id=%v", detail["run_id"])
	}
}

func TestPublishActivityDerivesChannelAndEmptyDetail(t *testing.T) {
	publisher := &capturingPublisher{}
	sink := NewSink(publisher)
	err := sink.PublishActivity(context.Background(), agentapp.ActivityEvent{
		SessionID: 42,
		Summary:   "no channel",
	})
	if err != nil {
		t.Fatalf("PublishActivity: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events=%d want 1", len(publisher.events))
	}
	event := publisher.events[0]
	if event.Channel != "agent:42" {
		t.Fatalf("Channel=%q want the derived agent:42", event.Channel)
	}
	var detail map[string]any
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		t.Fatalf("empty Detail=%s is not a JSON object: %v", event.Detail, err)
	}
	if len(detail) != 0 {
		t.Fatalf("empty Detail=%s want {}", event.Detail)
	}
}

// TestPublishActivityCarriesVersionID pins the P3 envelope mapping: a non-empty
// version id is forwarded to the event, while an absent or empty one stays nil.
func TestPublishActivityCarriesVersionID(t *testing.T) {
	versionID := "42"
	emptyVersionID := ""
	cases := []struct {
		name      string
		versionID *string
		want      *string
	}{
		{name: "non-empty version id is forwarded", versionID: &versionID, want: &versionID},
		{name: "nil version id stays nil", versionID: nil, want: nil},
		{name: "empty version id stays nil", versionID: &emptyVersionID, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &capturingPublisher{}
			sink := NewSink(publisher)
			err := sink.PublishActivity(context.Background(), agentapp.ActivityEvent{
				Channel:   "version:42",
				SessionID: 7,
				VersionID: tc.versionID,
			})
			if err != nil {
				t.Fatalf("PublishActivity: %v", err)
			}
			if len(publisher.events) != 1 {
				t.Fatalf("published events=%d want 1", len(publisher.events))
			}
			got := publisher.events[0].VersionID
			if tc.want == nil {
				if got != nil {
					t.Fatalf("VersionID=%v want nil", got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("VersionID=%v want %q", got, *tc.want)
			}
		})
	}
}

func TestPublishActivityReturnsPublisherError(t *testing.T) {
	publishErr := errors.New("event store is down")
	publisher := &capturingPublisher{err: publishErr}
	sink := NewSink(publisher)
	err := sink.PublishActivity(context.Background(), agentapp.ActivityEvent{SessionID: 1})
	if !errors.Is(err, publishErr) {
		t.Fatalf("PublishActivity err=%v want the publisher error", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("publisher was not called")
	}
}

func TestPublishActivityRejectsUnencodableDetail(t *testing.T) {
	publisher := &capturingPublisher{}
	sink := NewSink(publisher)
	err := sink.PublishActivity(context.Background(), agentapp.ActivityEvent{
		SessionID: 1,
		Detail:    map[string]any{"value": math.NaN()},
	})
	if err == nil {
		t.Fatal("PublishActivity accepted an unencodable detail object")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("published events=%d want 0 after an encoding failure", len(publisher.events))
	}
}

func TestPublishActivityWithoutPublisherIsNoop(t *testing.T) {
	if err := NewSink(nil).PublishActivity(context.Background(), agentapp.ActivityEvent{SessionID: 1}); err != nil {
		t.Fatalf("PublishActivity with a nil publisher: %v", err)
	}
}
