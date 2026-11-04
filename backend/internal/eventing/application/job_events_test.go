package eventingapp_test

// 0.2.1 job progress events (contracts/events.md §18.1): job.progress carries
// job_id/kind/current/total/status on the version channel; job.finished is the
// one-time terminal event. The SSE handler maps the type name and the flattened
// payload unchanged, so these tests pin the contract shape the UI consumes.

import (
	"context"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeJobPublisher struct {
	events []eventingapp.Event
	err    error
}

func (p *fakeJobPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return p.err
}

func TestJobProgressEventPayload(t *testing.T) {
	progress := eventingapp.JobProgress{
		JobID:   77,
		Kind:    "execute.batch",
		Current: 3,
		Total:   9,
		Status:  eventingapp.JobStatusRunning,
		Message: "执行第 3/9 条用例",
	}
	event, err := progress.Event(42)
	if err != nil {
		t.Fatalf("Event: %v", err)
	}
	if event.Channel != "version:42" {
		t.Fatalf("channel = %q, want version:42", event.Channel)
	}
	if event.Type != eventingapp.EventJobProgress {
		t.Fatalf("type = %q, want %q", event.Type, eventingapp.EventJobProgress)
	}
	if event.Scope["version_id"] != "42" {
		t.Fatalf("scope = %#v", event.Scope)
	}
	want := map[string]any{
		"job_id":  "77",
		"kind":    "execute.batch",
		"current": 3,
		"total":   9,
		"status":  "running",
		"message": "执行第 3/9 条用例",
	}
	for key, value := range want {
		if event.Payload[key] != value {
			t.Fatalf("payload[%q] = %#v, want %#v (payload %#v)", key, event.Payload[key], value, event.Payload)
		}
	}

	// total=0 means "unknown": it is allowed and must not be turned into a
	// fabricated percentage by the publisher.
	unknown, err := eventingapp.JobProgress{
		JobID: 77, Kind: "execute.batch", Current: 0, Total: 0, Status: eventingapp.JobStatusOpen,
	}.Event(42)
	if err != nil {
		t.Fatalf("unknown-total Event: %v", err)
	}
	if unknown.Payload["total"] != 0 || unknown.Payload["current"] != 0 {
		t.Fatalf("unknown-total payload = %#v", unknown.Payload)
	}

	// Optional fields are omitted when empty.
	if _, ok := unknown.Payload["message"]; ok {
		t.Fatalf("empty optional message was emitted: %#v", unknown.Payload)
	}

	invalid := []struct {
		name     string
		progress eventingapp.JobProgress
		version  int64
	}{
		{"missing job", eventingapp.JobProgress{Kind: "execute.batch", Status: "running"}, 42},
		{"missing kind", eventingapp.JobProgress{JobID: 7, Status: "running"}, 42},
		{"negative current", eventingapp.JobProgress{JobID: 7, Kind: "execute.batch", Current: -1, Status: "running"}, 42},
		{"negative total", eventingapp.JobProgress{JobID: 7, Kind: "execute.batch", Total: -1, Status: "running"}, 42},
		{"unknown status", eventingapp.JobProgress{JobID: 7, Kind: "execute.batch", Status: "exploded"}, 42},
		{"missing version", eventingapp.JobProgress{JobID: 7, Kind: "execute.batch", Status: "running"}, 0},
	}
	for _, testCase := range invalid {
		if _, err := testCase.progress.Event(testCase.version); err == nil {
			t.Fatalf("%s: Event accepted an invalid payload", testCase.name)
		} else if platform.AsError(err) == nil {
			t.Fatalf("%s: error is not a typed platform error: %v", testCase.name, err)
		}
	}

	finished := eventingapp.JobFinished{
		JobID:     77,
		Kind:      "execute.batch",
		Status:    eventingapp.JobStatusDone,
		ResultRef: "content:1234",
	}
	finishedEvent, err := finished.Event(42)
	if err != nil {
		t.Fatalf("finished Event: %v", err)
	}
	if finishedEvent.Type != eventingapp.EventJobFinished {
		t.Fatalf("finished type = %q, want %q", finishedEvent.Type, eventingapp.EventJobFinished)
	}
	if finishedEvent.Payload["result_ref"] != "content:1234" || finishedEvent.Payload["status"] != "done" {
		t.Fatalf("finished payload = %#v", finishedEvent.Payload)
	}
	if _, ok := finishedEvent.Payload["error"]; ok {
		t.Fatalf("empty error was emitted: %#v", finishedEvent.Payload)
	}
	if _, err := (eventingapp.JobFinished{JobID: 7, Kind: "execute.batch", Status: "running"}).Event(42); err == nil {
		t.Fatal("job.finished accepted a non-terminal status")
	}

	publisher := &fakeJobPublisher{}
	if err := eventingapp.PublishJobProgress(context.Background(), publisher, 42, progress); err != nil {
		t.Fatalf("PublishJobProgress: %v", err)
	}
	if err := eventingapp.PublishJobFinished(context.Background(), publisher, 42, finished); err != nil {
		t.Fatalf("PublishJobFinished: %v", err)
	}
	if len(publisher.events) != 2 || publisher.events[0].Type != eventingapp.EventJobProgress ||
		publisher.events[1].Type != eventingapp.EventJobFinished {
		t.Fatalf("published events = %#v", publisher.events)
	}
}
