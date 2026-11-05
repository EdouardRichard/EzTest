package executionapp_test

// W1-14B: the resource lifecycle events are the trigger that settles a proposed
// discovery observation. The observer is best effort: it only reacts to the
// system channel, maps the four confirm/reject event types, and never fails a
// publish because the decision write failed.

import (
	"context"
	"errors"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

type decisionCall struct {
	resourceID int64
	kind       string
	decision   string
}

// fakeDecisionMarker records the (resource_id, decision) pair the observer
// hands to the repository. The context state is sampled inside the call because
// the observer cancels its own timeout as soon as the write returns.
type fakeDecisionMarker struct {
	calls    []decisionCall
	rows     int64
	err      error
	panics   bool
	lastCtx  context.Context
	ctxErr   error
	deadline time.Time
	hasDl    bool
}

func (f *fakeDecisionMarker) MarkDecisionByResource(ctx context.Context, resourceID int64, kind, decision string) (int64, error) {
	if f.panics {
		panic("unexpected marker call")
	}
	f.lastCtx = ctx
	f.ctxErr = ctx.Err()
	f.deadline, f.hasDl = ctx.Deadline()
	f.calls = append(f.calls, decisionCall{resourceID: resourceID, kind: kind, decision: decision})
	return f.rows, f.err
}

func newResourceDecisionObserver(marker executionapp.ResourceDecisionMarker) eventingapp.EventObserver {
	return executionapp.NewResourceDecisionObserver(marker, nil)
}

func systemResourceEvent(eventType string, payload map[string]any) eventing.Event {
	return eventing.Event{Channel: "system", Type: eventType, Payload: payload}
}

// TestResourceDecisionObserverMapsConfirmAndReject: the two confirmed event
// types settle the observation as approved, the two rejected types as rejected,
// the event type carries the page/api kind the repository matches on, and the
// decimal-string resource id is parsed into the repository argument.
func TestResourceDecisionObserverMapsConfirmAndReject(t *testing.T) {
	cases := []struct {
		eventType string
		kind      string
		decision  string
	}{
		{"resource.page.confirmed", "page", "approved"},
		{"resource.api.confirmed", "api", "approved"},
		{"resource.page.rejected", "page", "rejected"},
		{"resource.api.rejected", "api", "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			marker := &fakeDecisionMarker{rows: 2}
			observe := newResourceDecisionObserver(marker)
			err := observe(context.Background(), systemResourceEvent(tc.eventType, map[string]any{"id": "11"}))
			if err != nil {
				t.Fatalf("observer returned error for best-effort write: %v", err)
			}
			if len(marker.calls) != 1 {
				t.Fatalf("marker calls = %d, want 1: %+v", len(marker.calls), marker.calls)
			}
			got := marker.calls[0]
			if got.resourceID != 11 || got.kind != tc.kind || got.decision != tc.decision {
				t.Fatalf("marker call = %+v, want {11 %s %s}", got, tc.kind, tc.decision)
			}
			if marker.lastCtx == nil {
				t.Fatal("observer must pass a non-nil context to the repository")
			}
		})
	}
}

// TestResourceDecisionObserverAcceptsInt64ID: the payload may carry the id as an
// int64 when the event is produced in-process rather than as a decimal string.
func TestResourceDecisionObserverAcceptsInt64ID(t *testing.T) {
	marker := &fakeDecisionMarker{}
	observe := newResourceDecisionObserver(marker)
	if err := observe(context.Background(), systemResourceEvent(
		"resource.page.confirmed", map[string]any{"id": int64(42)},
	)); err != nil {
		t.Fatalf("observer: %v", err)
	}
	if len(marker.calls) != 1 || marker.calls[0].resourceID != 42 || marker.calls[0].kind != "page" {
		t.Fatalf("marker calls = %+v, want one page resource 42", marker.calls)
	}
}

// TestResourceDecisionObserverIgnoresUnrelatedEvents: every other system event
// and every other channel is a no-op without a marker call.
func TestResourceDecisionObserverIgnoresUnrelatedEvents(t *testing.T) {
	marker := &fakeDecisionMarker{}
	observe := newResourceDecisionObserver(marker)
	events := []eventing.Event{
		systemResourceEvent("resource.page.created", map[string]any{"id": "11"}),
		systemResourceEvent("resource.page.proposed", map[string]any{"id": "11"}),
		systemResourceEvent("settings.updated", map[string]any{"id": "11"}),
		{Channel: "version:7", Type: "resource.page.confirmed", Payload: map[string]any{"id": "11"}},
		{Channel: "system", Type: "resource.page.confirmed", Payload: nil},
	}
	for _, event := range events {
		if err := observe(context.Background(), event); err != nil {
			t.Fatalf("observer returned error for ignored event %s: %v", event.Type, err)
		}
	}
	if len(marker.calls) != 0 {
		t.Fatalf("marker calls = %+v, want none", marker.calls)
	}
}

// TestResourceDecisionObserverIgnoresUnparseableID: a missing, blank,
// non-numeric, zero or negative id is ignored and never panics.
func TestResourceDecisionObserverIgnoresUnparseableID(t *testing.T) {
	marker := &fakeDecisionMarker{panics: true}
	observe := newResourceDecisionObserver(marker)
	payloads := []map[string]any{
		{},
		{"id": ""},
		{"id": "  "},
		{"id": "abc"},
		{"id": "0"},
		{"id": "-3"},
		{"id": "1.5"},
		{"id": int64(0)},
		{"id": int64(-3)},
		{"id": 11.0},
		{"id": true},
	}
	for _, payload := range payloads {
		if err := observe(context.Background(), systemResourceEvent("resource.page.confirmed", payload)); err != nil {
			t.Fatalf("observer returned error for invalid payload %+v: %v", payload, err)
		}
	}
}

// TestResourceDecisionObserverSwallowsRepositoryError: the write is best effort
// (and the publish path ignores observer errors), so a repository failure is
// logged and reported as nil.
func TestResourceDecisionObserverSwallowsRepositoryError(t *testing.T) {
	marker := &fakeDecisionMarker{err: errors.New("db unavailable")}
	observe := newResourceDecisionObserver(marker)
	if err := observe(context.Background(), systemResourceEvent(
		"resource.api.rejected", map[string]any{"id": "77"},
	)); err != nil {
		t.Fatalf("observer must not propagate the repository error, got %v", err)
	}
	if len(marker.calls) != 1 || marker.calls[0].resourceID != 77 ||
		marker.calls[0].kind != "api" || marker.calls[0].decision != "rejected" {
		t.Fatalf("marker calls = %+v, want one rejected api resource 77", marker.calls)
	}
}

// TestResourceDecisionObserverDetachesCanceledRequestContext: the observer runs
// after the request has been handled, so the request context may already be
// canceled (client abort / streamed response). The write-back must still
// complete: it detaches from the request cancellation and bounds itself with its
// own timeout instead of inheriting the aborted context.
func TestResourceDecisionObserverDetachesCanceledRequestContext(t *testing.T) {
	marker := &fakeDecisionMarker{}
	observe := newResourceDecisionObserver(marker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := observe(ctx, systemResourceEvent(
		"resource.page.confirmed", map[string]any{"id": "11"},
	)); err != nil {
		t.Fatalf("observer returned error for a canceled request context: %v", err)
	}
	if len(marker.calls) != 1 {
		t.Fatalf("marker calls = %d, want the write-back to survive cancellation", len(marker.calls))
	}
	if marker.lastCtx == nil {
		t.Fatal("observer must pass a non-nil context to the repository")
	}
	if marker.ctxErr != nil {
		t.Fatalf("write-back context inherits the request cancellation (%v), want a detached context", marker.ctxErr)
	}
	if !marker.hasDl {
		t.Fatal("write-back context must carry its own timeout")
	}
	if remaining := time.Until(marker.deadline); remaining <= 0 || remaining > 6*time.Second {
		t.Fatalf("write-back timeout remaining = %v, want about 5s", remaining)
	}
}

// TestResourceDecisionObserverNilMarkerIsNoOp: a degraded assembly without a
// marker must not panic on a matching event.
func TestResourceDecisionObserverNilMarkerIsNoOp(t *testing.T) {
	observe := newResourceDecisionObserver(nil)
	if err := observe(context.Background(), systemResourceEvent(
		"resource.page.confirmed", map[string]any{"id": "11"},
	)); err != nil {
		t.Fatalf("observer: %v", err)
	}
}
