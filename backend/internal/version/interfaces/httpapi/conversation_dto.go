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
				"must be a text, attachment, requirement_card or rollback_proposal part")
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
