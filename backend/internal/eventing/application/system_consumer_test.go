package eventingapp

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// TestSystemEventConsumerCountsAndLogsSystemEvents pins the generic system
// observer contract: a system-channel event is counted per type and recorded
// with its channel, type and seq.
func TestSystemEventConsumerCountsAndLogsSystemEvents(t *testing.T) {
	var captured bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&captured, &slog.HandlerOptions{Level: slog.LevelDebug}))
	consumer := NewSystemEventConsumer(logger)

	const eventType = "test.system_consumer.counted"
	before := SystemEventCount(eventType)
	if err := consumer(context.Background(), eventing.Event{
		Channel: "system",
		Seq:     42,
		Type:    eventType,
	}); err != nil {
		t.Fatalf("observer returned error: %v", err)
	}

	if got := SystemEventCount(eventType); got != before+1 {
		t.Fatalf("SystemEventCount(%q) = %d, want %d", eventType, got, before+1)
	}

	logged := captured.String()
	for _, want := range []string{"channel=system", "type=" + eventType, "seq=42"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("system event log %q does not contain %q", logged, want)
		}
	}
}

// TestSystemEventConsumerIgnoresNonSystemChannels pins the no-side-effect rule:
// the system consumer must never count a version- or user-channel event.
func TestSystemEventConsumerIgnoresNonSystemChannels(t *testing.T) {
	consumer := NewSystemEventConsumer(slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, eventType := range []string{"test.system_consumer.version_channel", "test.system_consumer.user_channel"} {
		channel := "version:7"
		if strings.Contains(eventType, "user_channel") {
			channel = "user:7"
		}
		if err := consumer(context.Background(), eventing.Event{
			Channel: channel,
			Seq:     3,
			Type:    eventType,
		}); err != nil {
			t.Fatalf("observer returned error for %s: %v", channel, err)
		}
		if got := SystemEventCount(eventType); got != 0 {
			t.Fatalf("SystemEventCount(%q) = %d after %s event, want 0", eventType, got, channel)
		}
	}
}
