package eventing_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// validNewEvent is the smallest publishable V1 envelope used by the validation
// tests.
func validNewEvent() eventing.NewEvent {
	return eventing.NewEvent{
		Channel: "system",
		Type:    "system.test",
		Summary: "admin test event",
		Detail:  json.RawMessage(`{"index":1}`),
	}
}

// TestValidateAcceptsEnvelopeAtLimits proves the §7.2 limits are inclusive and
// counted in runes, not bytes: 64/100/500 multi-byte characters are accepted.
func TestValidateAcceptsEnvelopeAtLimits(t *testing.T) {
	event := validNewEvent()
	event.Channel = strings.Repeat("通", 64)
	event.Type = strings.Repeat("通", 100)
	event.Summary = strings.Repeat("通", 500)
	event.Detail = detailObjectOfSize(t, 64<<10)

	if err := eventing.Validate(event); err != nil {
		t.Fatalf("Validate at the §7.2 limits: %v", err)
	}
}

// TestValidateAcceptsOmittedDetail proves an absent detail is valid: the store
// defaults it to the empty object the envelope requires.
func TestValidateAcceptsOmittedDetail(t *testing.T) {
	event := validNewEvent()
	event.Detail = nil
	if err := eventing.Validate(event); err != nil {
		t.Fatalf("Validate with omitted detail: %v", err)
	}

	event.Detail = json.RawMessage("  ")
	if err := eventing.Validate(event); err != nil {
		t.Fatalf("Validate with blank detail: %v", err)
	}
}

// TestValidateRejectsOverLimitEnvelope covers every §7.2 limit and the
// detail-is-a-JSON-object rule. Every failure must wrap ErrInvalidEvent so the
// HTTP layer can map it to 400 invalid_argument.
func TestValidateRejectsOverLimitEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*eventing.NewEvent)
		wantSub string
	}{
		{
			name:    "channel over 64 runes",
			mutate:  func(event *eventing.NewEvent) { event.Channel = strings.Repeat("通", 65) },
			wantSub: "channel",
		},
		{
			name:    "type over 100 runes",
			mutate:  func(event *eventing.NewEvent) { event.Type = strings.Repeat("t", 101) },
			wantSub: "type",
		},
		{
			name:    "summary over 500 runes",
			mutate:  func(event *eventing.NewEvent) { event.Summary = strings.Repeat("通", 501) },
			wantSub: "summary",
		},
		{
			name:    "detail over 64KiB",
			mutate:  func(event *eventing.NewEvent) { event.Detail = detailObjectOfSize(t, (64<<10)+1) },
			wantSub: "detail",
		},
		{
			name:    "detail array",
			mutate:  func(event *eventing.NewEvent) { event.Detail = json.RawMessage(`[1,2,3]`) },
			wantSub: "detail",
		},
		{
			name:    "detail string",
			mutate:  func(event *eventing.NewEvent) { event.Detail = json.RawMessage(`"not-an-object"`) },
			wantSub: "detail",
		},
		{
			name:    "detail number",
			mutate:  func(event *eventing.NewEvent) { event.Detail = json.RawMessage(`42`) },
			wantSub: "detail",
		},
		{
			name:    "detail null",
			mutate:  func(event *eventing.NewEvent) { event.Detail = json.RawMessage(`null`) },
			wantSub: "detail",
		},
		{
			name:    "detail malformed JSON",
			mutate:  func(event *eventing.NewEvent) { event.Detail = json.RawMessage(`{"broken":`) },
			wantSub: "detail",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := validNewEvent()
			tc.mutate(&event)
			err := eventing.Validate(event)
			if !errors.Is(err, eventing.ErrInvalidEvent) {
				t.Fatalf("Validate error=%v, want one wrapping ErrInvalidEvent", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate error=%q, want it to name %q", err, tc.wantSub)
			}
		})
	}
}

// detailObjectOfSize builds a valid JSON object of exactly size bytes so the
// 64 KiB boundary can be asserted without hand-counting padding.
func detailObjectOfSize(t *testing.T, size int) json.RawMessage {
	t.Helper()
	const prefix, suffix = `{"pad":"`, `"}`
	if size < len(prefix)+len(suffix) {
		t.Fatalf("size %d is too small for a padded object", size)
	}
	return json.RawMessage(prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix)
}
