// Package eventing adapts the P5 mindmap EventPublisher port to the P0 eventing
// use case (spec §4.3/§6.11). Every mindmap event is published on the fixed
// channel version:<id> with the decimal string version id, the case ids mapped
// onto refs.case and the actor/source pair stamped into detail. The envelope
// limits are enforced here so an oversized event never reaches the event store.
package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
)

const (
	// maxSummaryRunes is the frozen envelope summary limit (spec §6.11).
	maxSummaryRunes = 500
	// maxDetailBytes is the frozen envelope detail limit (spec §6.11).
	maxDetailBytes = 64 << 10
	// maxCaseRefs is the refs.case cap of the P5 events (spec §6.11).
	maxCaseRefs = 20
)

// EventPublisher is the narrow slice of the eventing use case the adapter needs.
// *eventing/application.Service satisfies it.
type EventPublisher interface {
	Publish(ctx context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error)
}

// Publisher publishes mindmap events through the P0 event bus.
type Publisher struct {
	events EventPublisher
}

// NewEventPublisher returns the adapter. A nil publisher disables publishing
// (used by partially wired processes and tests).
func NewEventPublisher(events EventPublisher) *Publisher {
	return &Publisher{events: events}
}

var _ mindmapapp.EventPublisher = (*Publisher)(nil)

// Publish maps one mindmap event onto the frozen version-channel envelope. The
// summary is truncated to the rune limit and the case refs are capped; an
// oversized detail is rejected instead of being silently dropped.
func (p *Publisher) Publish(ctx context.Context, event mindmapapp.Event) error {
	if p == nil || p.events == nil {
		return nil
	}
	detail, err := encodeDetail(event)
	if err != nil {
		return err
	}
	versionID := strconv.FormatInt(event.VersionID, 10)
	_, err = p.events.Publish(ctx, eventingdomain.NewEvent{
		Channel:   fmt.Sprintf("version:%d", event.VersionID),
		Type:      event.Type,
		VersionID: &versionID,
		Summary:   truncateRunes(event.Summary, maxSummaryRunes),
		Detail:    detail,
		Refs:      eventingdomain.Refs{Case: caseRefs(event.RefCaseIDs)},
	})
	return err
}

// encodeDetail marshals the event detail and stamps the actor/source pair spec
// §6.11 requires when the caller did not already carry it. A nil detail becomes
// the empty object the envelope expects, never JSON null.
func encodeDetail(event mindmapapp.Event) (json.RawMessage, error) {
	detail := make(map[string]any, len(event.Detail)+2)
	for key, value := range event.Detail {
		detail[key] = value
	}
	if _, exists := detail["actorId"]; !exists && event.ActorID > 0 {
		detail["actorId"] = event.ActorID
	}
	if _, exists := detail["source"]; !exists && event.Source != "" {
		detail["source"] = event.Source
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("mindmap: encode event detail: %w", err)
	}
	if len(encoded) > maxDetailBytes {
		return nil, fmt.Errorf("mindmap: event detail is %d bytes, limit is %d", len(encoded), maxDetailBytes)
	}
	return encoded, nil
}

// caseRefs renders the case ids as the decimal strings of the frozen envelope
// and caps the list at the documented refs limit.
func caseRefs(ids []int64) []string {
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		if len(refs) >= maxCaseRefs {
			break
		}
		refs = append(refs, strconv.FormatInt(id, 10))
	}
	return refs
}

// truncateRunes cuts value to at most limit runes without splitting a rune.
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
