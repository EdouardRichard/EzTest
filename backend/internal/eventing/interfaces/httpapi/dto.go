package eventingapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// eventDTO is the public event shape: the event columns plus the envelope
// fields. Seq and actor_id are decimal strings like every other ID.
type eventDTO struct {
	Channel   string         `json:"channel"`
	Seq       string         `json:"seq"`
	Type      string         `json:"type"`
	Scope     map[string]any `json:"scope"`
	Payload   map[string]any `json:"payload"`
	ActorID   string         `json:"actor_id,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	CreatedAt string         `json:"created_at"`
}

func toEventDTO(event eventing.Event) eventDTO {
	scope := event.Scope
	if scope == nil {
		scope = map[string]any{}
	}
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	dto := eventDTO{
		Channel:   event.Channel,
		Seq:       strconv.FormatInt(event.Seq, 10),
		Type:      event.Type,
		Scope:     scope,
		Payload:   payload,
		RequestID: event.RequestID,
		CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339),
	}
	if event.ActorID != nil {
		dto.ActorID = strconv.FormatInt(*event.ActorID, 10)
	}
	return dto
}
