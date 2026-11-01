package eventingapp_test

import (
	"context"
	"errors"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeP6EventCounter is the eventing-context P6 count source used by the query
// port tests.
type fakeP6EventCounter struct {
	count     int
	err       error
	calls     int
	eventType string
	channel   string
}

func (f *fakeP6EventCounter) CountEvents(_ context.Context, eventType, channel string) (int, error) {
	f.calls++
	f.eventType, f.channel = eventType, channel
	return f.count, f.err
}

func TestP6CountEvents(t *testing.T) {
	t.Run("empty event log returns zero", func(t *testing.T) {
		reader := &fakeP6EventCounter{}
		service := eventingapp.NewQueryService(reader)

		count, err := service.CountEvents(context.Background(), "context.compacted", "")
		if err != nil {
			t.Fatalf("CountEvents: %v", err)
		}
		if count != 0 {
			t.Fatalf("count = %d, want 0", count)
		}
		if reader.calls != 1 || reader.eventType != "context.compacted" || reader.channel != "" {
			t.Fatalf("reader call = %d type=%q channel=%q", reader.calls, reader.eventType, reader.channel)
		}
	})

	t.Run("type and channel filters are trimmed and forwarded", func(t *testing.T) {
		reader := &fakeP6EventCounter{count: 3}
		service := eventingapp.NewQueryService(reader)

		count, err := service.CountEvents(context.Background(), "  context.compacted ", " version:7 ")
		if err != nil {
			t.Fatalf("CountEvents: %v", err)
		}
		if count != 3 {
			t.Fatalf("count = %d, want 3", count)
		}
		if reader.eventType != "context.compacted" || reader.channel != "version:7" {
			t.Fatalf("reader got type=%q channel=%q", reader.eventType, reader.channel)
		}
	})

	t.Run("unknown channel is rejected before the read", func(t *testing.T) {
		reader := &fakeP6EventCounter{}
		service := eventingapp.NewQueryService(reader)

		if _, err := service.CountEvents(context.Background(), "context.compacted", "bogus"); err == nil {
			t.Fatal("expected invalid_argument for unsupported channel")
		}
		if reader.calls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.calls)
		}
	})

	t.Run("empty event type is rejected before the read", func(t *testing.T) {
		reader := &fakeP6EventCounter{}
		service := eventingapp.NewQueryService(reader)

		if _, err := service.CountEvents(context.Background(), "  ", ""); err == nil {
			t.Fatal("expected invalid_argument for empty event type")
		}
		if reader.calls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.calls)
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := eventingapp.NewQueryService(&fakeP6EventCounter{err: want})

		if _, err := service.CountEvents(context.Background(), "context.compacted", ""); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})

	t.Run("nil reader is unavailable", func(t *testing.T) {
		service := eventingapp.NewQueryService(nil)
		_, err := service.CountEvents(context.Background(), "context.compacted", "")
		if err == nil {
			t.Fatal("expected unavailable error without a reader")
		}
		if platform.AsError(err).Status != 503 {
			t.Fatalf("status = %d, want 503", platform.AsError(err).Status)
		}
	})
}
