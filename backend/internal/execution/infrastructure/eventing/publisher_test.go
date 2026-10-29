package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// fakePublisher captures the events handed to the P0 event bus.
type fakePublisher struct {
	events []eventingdomain.NewEvent
	err    error
}

func (f *fakePublisher) Publish(_ context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error) {
	f.events = append(f.events, event)
	return eventingdomain.Event{}, f.err
}

func TestPublishMapsExecutionEventEnvelope(t *testing.T) {
	publisher := &fakePublisher{}
	adapter := New(publisher)

	event := executionapp.Event{
		Type:      "step.finished",
		VersionID: 7,
		BatchID:   8,
		CaseID:    9,
		StepID:    10,
		AgentRole: "executor",
		SessionID: "11",
		Summary:   "step 1 finished",
		Detail:    map[string]any{"seq": 1, "status": "passed"},
		Refs: executionapp.EventRefs{
			EvidenceIDs:    []int64{21, 22},
			CaseIDs:        []int64{9},
			RequirementIDs: []int64{31},
			Code:           []string{"backend/a.go"},
		},
	}
	if err := adapter.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("events = %+v", publisher.events)
	}
	got := publisher.events[0]
	if got.Channel != "version:7" || got.Type != "step.finished" || got.Summary != "step 1 finished" {
		t.Fatalf("envelope = %+v", got)
	}
	if got.VersionID == nil || *got.VersionID != "7" ||
		got.BatchID == nil || *got.BatchID != "8" ||
		got.CaseID == nil || *got.CaseID != "9" ||
		got.StepID == nil || *got.StepID != "10" ||
		got.AgentRole == nil || *got.AgentRole != "executor" ||
		got.SessionID == nil || *got.SessionID != "11" {
		t.Fatalf("ids = %+v", got)
	}
	if !reflect.DeepEqual(got.Refs.Evidence, []string{"21", "22"}) ||
		!reflect.DeepEqual(got.Refs.Case, []string{"9"}) ||
		!reflect.DeepEqual(got.Refs.Requirement, []string{"31"}) ||
		!reflect.DeepEqual(got.Refs.Code, []string{"backend/a.go"}) {
		t.Fatalf("refs = %+v", got.Refs)
	}
	var detail map[string]any
	if err := json.Unmarshal(got.Detail, &detail); err != nil {
		t.Fatalf("detail = %s: %v", got.Detail, err)
	}
	if detail["seq"] != float64(1) || detail["status"] != "passed" {
		t.Fatalf("detail = %#v", detail)
	}
}

func TestPublishOmitsZeroIDsAndDefaultsDetail(t *testing.T) {
	publisher := &fakePublisher{}
	adapter := New(publisher)

	if err := adapter.Publish(context.Background(), executionapp.Event{
		Type:      "batch.created",
		VersionID: 7,
		Summary:   "batch created",
	}); err != nil {
		t.Fatal(err)
	}
	got := publisher.events[0]
	if got.BatchID != nil || got.CaseID != nil || got.StepID != nil ||
		got.AgentRole != nil || got.SessionID != nil {
		t.Fatalf("zero ids must stay nil: %+v", got)
	}
	if string(got.Detail) != "{}" {
		t.Fatalf("detail = %s, want the empty object", got.Detail)
	}
}

func TestPublishCapsEachRefKindAtTwenty(t *testing.T) {
	publisher := &fakePublisher{}
	adapter := New(publisher)

	evidence := make([]int64, 0, 25)
	for id := int64(1); id <= 25; id++ {
		evidence = append(evidence, id)
	}
	cases := make([]int64, 0, 25)
	requirements := make([]int64, 0, 25)
	code := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		cases = append(cases, int64(100+i))
		requirements = append(requirements, int64(200+i))
		code = append(code, "file")
	}

	err := adapter.Publish(context.Background(), executionapp.Event{
		Type:      "run.finished",
		VersionID: 7,
		Refs: executionapp.EventRefs{
			EvidenceIDs:    evidence,
			CaseIDs:        cases,
			RequirementIDs: requirements,
			Code:           code,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := publisher.events[0].Refs
	if len(got.Evidence) != 20 || got.Evidence[0] != "1" || got.Evidence[19] != "20" {
		t.Fatalf("evidence refs = %v", got.Evidence)
	}
	if len(got.Case) != 20 || len(got.Requirement) != 20 || len(got.Code) != 20 {
		t.Fatalf("ref lengths = %d/%d/%d", len(got.Case), len(got.Requirement), len(got.Code))
	}
}

func TestPublishRejectsMissingVersion(t *testing.T) {
	publisher := &fakePublisher{}
	err := New(publisher).Publish(context.Background(), executionapp.Event{Type: "batch.created"})
	if err == nil {
		t.Fatal("Publish without a version = nil error")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("events = %+v", publisher.events)
	}
}

func TestPublishReturnsPublisherError(t *testing.T) {
	boom := errors.New("boom")
	publisher := &fakePublisher{err: boom}
	err := New(publisher).Publish(context.Background(), executionapp.Event{Type: "batch.created", VersionID: 7})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
}

func TestPublishRejectsUnencodableDetail(t *testing.T) {
	publisher := &fakePublisher{}
	err := New(publisher).Publish(context.Background(), executionapp.Event{
		Type:      "batch.created",
		VersionID: 7,
		Detail:    map[string]any{"bad": func() {}},
	})
	if err == nil {
		t.Fatal("Publish with an unencodable detail = nil error")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("events = %+v", publisher.events)
	}
}

func TestPublishNilPublisherIsNoop(t *testing.T) {
	if err := New(nil).Publish(context.Background(), executionapp.Event{Type: "batch.created", VersionID: 7}); err != nil {
		t.Fatalf("error = %v", err)
	}
}

var _ executionapp.EventPublisher = (*Adapter)(nil)
