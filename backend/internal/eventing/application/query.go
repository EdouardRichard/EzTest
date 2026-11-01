package eventingapp

import (
	"context"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// QueryReader is the eventing context's P6 read-only count source. channel is
// empty for the global count across every channel; the implementation counts
// persisted events by type.
type QueryReader interface {
	CountEvents(ctx context.Context, eventType, channel string) (int, error)
}

// QueryService is the P6 read-only query port of the eventing context. It is
// consumed by the quality operational metrics aggregator (spec §5.2).
type QueryService struct {
	reader QueryReader
}

// NewQueryService wires the eventing read-only query port.
func NewQueryService(reader QueryReader) *QueryService {
	return &QueryService{reader: reader}
}

func (s *QueryService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("eventing query reader is not configured")
	}
	return nil
}

// CountEvents counts persisted events of one type, optionally restricted to one
// channel. An empty channel means the global event log. The channel is
// validated with the same rules as the publish/snapshot path.
func (s *QueryService) CountEvents(ctx context.Context, eventType string, channel string) (int, error) {
	if err := s.requireReader(); err != nil {
		return 0, err
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return 0, platform.Invalid("event_type", "event type is required")
	}
	channel = strings.TrimSpace(channel)
	if channel != "" && !ValidChannel(channel) {
		return 0, platform.Invalid("channel", "unsupported event channel")
	}
	return s.reader.CountEvents(ctx, eventType, channel)
}
