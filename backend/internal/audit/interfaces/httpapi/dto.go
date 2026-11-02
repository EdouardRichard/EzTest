package auditapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
)

// entryDTO is the public representation of one audit entry. It never carries
// credentials; the domain entry contract already excludes them.
type entryDTO struct {
	ID         string         `json:"id"`
	ActorID    string         `json:"actor_id,omitempty"`
	Action     string         `json:"action"`
	TargetKind string         `json:"target_kind"`
	TargetID   string         `json:"target_id"`
	RequestID  string         `json:"request_id"`
	IP         string         `json:"ip"`
	UserAgent  string         `json:"user_agent"`
	Metadata   map[string]any `json:"metadata"`
	CreatedAt  string         `json:"created_at"`
}

func toEntryDTO(entry audit.Entry) entryDTO {
	metadata := entry.Metadata
	if metadata == nil {
		// The contract returns an object, never null.
		metadata = map[string]any{}
	}
	dto := entryDTO{
		ID:         strconv.FormatInt(entry.ID, 10),
		Action:     entry.Action,
		TargetKind: entry.TargetKind,
		TargetID:   entry.TargetID,
		RequestID:  entry.RequestID,
		IP:         entry.IP,
		UserAgent:  entry.UserAgent,
		Metadata:   metadata,
		CreatedAt:  entry.CreatedAt.UTC().Format(time.RFC3339),
	}
	if entry.ActorID != nil {
		dto.ActorID = strconv.FormatInt(*entry.ActorID, 10)
	}
	return dto
}
