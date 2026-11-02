package eventingapi

// 0.2.1 job events must reach the unified SSE stream unchanged: the event name
// is the persisted type and `data` is the flattened envelope with the contract
// payload keys (contracts/events.md §18.1). The frontend pairs job cards by
// job_id, so the mapping is an acceptance-visible surface.

import (
	"encoding/json"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

func TestJobProgressSSEMapping(t *testing.T) {
	progress := eventingapp.JobProgress{
		JobID:   77,
		Kind:    "execute.batch",
		Current: 3,
		Total:   9,
		Status:  eventingapp.JobStatusRunning,
	}
	event, err := progress.Event(1001)
	if err != nil {
		t.Fatalf("Event: %v", err)
	}
	stored := eventingdomain.Event{
		Channel:   event.Channel,
		Seq:       11,
		Type:      event.Type,
		Scope:     event.Scope,
		Payload:   event.Payload,
		CreatedAt: fixedTime,
	}

	svc := newFakeService()
	svc.bounds = eventingdomain.Bounds{FirstSeq: 1, LastSeq: 10}
	svc.subCh = make(chan eventingdomain.Event, 1)
	svc.subCh <- stored
	handler := New(svc)
	scanner, cleanup := startStream(t, handler, "/api/stream?channel=version:1001", nil)
	defer cleanup()

	frame := readFrame(t, scanner)
	if frame.id != "11" || frame.event != string(eventingapp.EventJobProgress) {
		t.Fatalf("frame = %#v", frame)
	}
	var dto eventDTO
	if err := json.Unmarshal([]byte(frame.data), &dto); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if dto.Channel != "version:1001" || dto.Type != eventingapp.EventJobProgress {
		t.Fatalf("dto = %#v", dto)
	}
	want := map[string]any{
		"job_id":  "77",
		"kind":    "execute.batch",
		"current": float64(3),
		"total":   float64(9),
		"status":  "running",
	}
	for key, value := range want {
		if dto.Payload[key] != value {
			t.Fatalf("SSE payload[%q] = %#v, want %#v (payload %#v)", key, dto.Payload[key], value, dto.Payload)
		}
	}
}
