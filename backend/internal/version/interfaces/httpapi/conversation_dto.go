package versionapi

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- response DTOs (OpenAPI P1 Conversation* schemas) ----------

type conversationSummaryDTO struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	MessageCount  int     `json:"message_count"`
	LastMessageAt *string `json:"last_message_at"`
	CreatedAt     string  `json:"created_at"`
}

type conversationDTO struct {
	conversationSummaryDTO
	Summary   *string `json:"summary"`
	UpdatedAt string  `json:"updated_at"`
}

type conversationMessageDTO struct {
	ID             string                `json:"id"`
	ConversationID string                `json:"conversation_id"`
	Role           string                `json:"role"`
	Parts          []version.MessagePart `json:"parts"`
	Refs           []version.MessageRef  `json:"refs"`
	CreatedAt      string                `json:"created_at"`
}

// ---------- 0.2.1 streaming parts ----------

// MessagePartType is the closed part-type enum of the 0.2.1 parts contract
// (OpenAPI MessagePartView.part_type, DB chk_conversation_message_part_type).
type MessagePartType string

const (
	MessagePartTypeText            MessagePartType = "text"
	MessagePartTypeThinking        MessagePartType = "thinking"
	MessagePartTypeToolCall        MessagePartType = "tool_call"
	MessagePartTypeJobCard         MessagePartType = "job_card"
	MessagePartTypeAttachment      MessagePartType = "attachment"
	MessagePartTypeRequirementCard MessagePartType = "requirement_card"
	MessagePartTypeCaseCard        MessagePartType = "case_card"
	MessagePartTypeError           MessagePartType = "error"
)

// MessagePartTypes returns the closed part-type enum in contract order.
func MessagePartTypes() []string {
	return []string{
		string(MessagePartTypeText),
		string(MessagePartTypeThinking),
		string(MessagePartTypeToolCall),
		string(MessagePartTypeJobCard),
		string(MessagePartTypeAttachment),
		string(MessagePartTypeRequirementCard),
		string(MessagePartTypeCaseCard),
		string(MessagePartTypeError),
	}
}

// MessagePart is one append-only streaming part row returned by
// GET /api/versions/{id}/conversations/{cid}/messages/{mid}/parts
// (OpenAPI MessagePartView). Type is the DB part_type serialized as
// `part_type`; Payload is the opaque part-shaped JSON object.
type MessagePart struct {
	Seq       int             `json:"seq"`
	Type      MessagePartType `json:"part_type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// ---------- request payloads ----------

type createConversationRequest struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

type postMessageRequest struct {
	ClientMessageID string            `json:"client_message_id"`
	Parts           []json.RawMessage `json:"parts"`
}

// messageParts decodes the discriminated-union parts with per-part errors.
func (r postMessageRequest) messageParts() ([]version.MessagePart, error) {
	parts := make([]version.MessagePart, 0, len(r.Parts))
	for index, raw := range r.Parts {
		var part version.MessagePart
		if err := json.Unmarshal(raw, &part); err != nil {
			return nil, platform.Invalid(fmt.Sprintf("parts[%d]", index),
				"must be a text, attachment, image, requirement_card or rollback_proposal part")
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// ---------- view -> DTO conversions ----------

func toConversationSummaryDTO(view versionapp.ConversationView) conversationSummaryDTO {
	return conversationSummaryDTO{
		ID:            strconv.FormatInt(view.ID, 10),
		Title:         view.Title,
		Kind:          view.Kind,
		Status:        view.Status,
		MessageCount:  view.MessageCount,
		LastMessageAt: formatOptionalTime(view.LastMessageAt),
		CreatedAt:     formatTime(view.CreatedAt),
	}
}

func toConversationDTO(view versionapp.ConversationView) conversationDTO {
	return conversationDTO{
		conversationSummaryDTO: toConversationSummaryDTO(view),
		Summary:                view.Summary,
		UpdatedAt:              formatTime(view.UpdatedAt),
	}
}

func toConversationMessageDTO(view versionapp.MessageView) conversationMessageDTO {
	parts := view.Parts
	if parts == nil {
		parts = []version.MessagePart{}
	}
	refs := view.Refs
	if refs == nil {
		refs = []version.MessageRef{}
	}
	return conversationMessageDTO{
		ID:             strconv.FormatInt(view.ID, 10),
		ConversationID: strconv.FormatInt(view.ConversationID, 10),
		Role:           view.Role,
		Parts:          parts,
		Refs:           refs,
		CreatedAt:      formatTime(view.CreatedAt),
	}
}

func toMessagePart(view versionapp.MessagePartView) MessagePart {
	payload := view.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return MessagePart{
		Seq:       view.Seq,
		Type:      MessagePartType(view.Type),
		Payload:   payload,
		CreatedAt: formatTime(view.CreatedAt),
	}
}
