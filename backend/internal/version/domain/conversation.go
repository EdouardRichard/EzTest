package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Conversation kinds and statuses.
const (
	ConversationClarification = "clarification"
	ConversationDesign        = "design"
	ConversationActive        = "active"
	ConversationClosed        = "closed"
)

// Message roles and part types.
const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"

	MessagePartText             = "text"
	MessagePartAttachment       = "attachment"
	MessagePartRequirementCard  = "requirement_card"
	MessagePartRollbackProposal = "rollback_proposal"
)

// Conversation and message limits.
const (
	ConversationTitleMax  = 120
	MessageTextMax        = 20000
	ClientMessageIDMax    = 64
	MaxMessageAttachments = 10
	PinnedFactsMax        = 20
	PinnedFactMaxLength   = 500
	SummaryTextMax        = 20000
	MessagesDefaultLimit  = 100
	MessagesMaxLimit      = 200
)

// Conversation domain errors.
var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrMessageNotFound      = errors.New("message not found")
)

var messageSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Conversation is one human/AI clarification thread.
type Conversation struct {
	ID            int64
	VersionID     int64
	Kind          string
	Title         string
	Status        string
	CreatedBy     *int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	MessageCount  int
	LastMessageAt *time.Time
}

// MessageRef is one structured reference attached to a message.
type MessageRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Message is one UI-thread message (user or assistant). The LLM transcript
// lives in the agent context, not here.
type Message struct {
	ID               int64
	ConversationID   int64
	Role             string
	Parts            []MessagePart
	Refs             []MessageRef
	TokenUsage       map[string]any
	ActorID          *int64
	AgentRunID       *int64
	ClientMessageID  *string
	ReplyToMessageID *int64
	CreatedAt        time.Time
}

// MessagePart is the discriminated union of message parts. The JSON shape is
// fixed by the P1 OpenAPI MessagePart oneOf.
type MessagePart struct {
	Type          string
	Text          string
	SHA256        string
	Name          string
	MIME          string
	Size          int64
	RequirementID int64
	Title         string
	Status        string
	Revision      int
	Reason        string
}

// MarshalJSON renders the exact OpenAPI part shape for the part type.
func (p MessagePart) MarshalJSON() ([]byte, error) {
	switch p.Type {
	case MessagePartText:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: p.Type, Text: p.Text})
	case MessagePartAttachment:
		return json.Marshal(struct {
			Type   string `json:"type"`
			SHA256 string `json:"sha256"`
			Name   string `json:"name"`
			MIME   string `json:"mime"`
			Size   int64  `json:"size"`
		}{Type: p.Type, SHA256: p.SHA256, Name: p.Name, MIME: p.MIME, Size: p.Size})
	case MessagePartRequirementCard:
		return json.Marshal(struct {
			Type          string `json:"type"`
			RequirementID string `json:"requirement_id"`
			Title         string `json:"title"`
			Status        string `json:"status"`
			Revision      int    `json:"revision"`
		}{
			Type:          p.Type,
			RequirementID: strconv.FormatInt(p.RequirementID, 10),
			Title:         p.Title,
			Status:        p.Status,
			Revision:      p.Revision,
		})
	case MessagePartRollbackProposal:
		return json.Marshal(struct {
			Type          string `json:"type"`
			RequirementID string `json:"requirement_id"`
			Reason        string `json:"reason"`
		}{
			Type:          p.Type,
			RequirementID: strconv.FormatInt(p.RequirementID, 10),
			Reason:        p.Reason,
		})
	default:
		return nil, fmt.Errorf("unsupported message part type %q", p.Type)
	}
}

// UnmarshalJSON reads the OpenAPI part shape back into the union.
func (p *MessagePart) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe.Type {
	case MessagePartText:
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, Text: payload.Text}
	case MessagePartAttachment:
		var payload struct {
			SHA256 string `json:"sha256"`
			Name   string `json:"name"`
			MIME   string `json:"mime"`
			Size   int64  `json:"size"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, SHA256: payload.SHA256, Name: payload.Name, MIME: payload.MIME, Size: payload.Size}
	case MessagePartRequirementCard:
		var payload struct {
			RequirementID json.RawMessage `json:"requirement_id"`
			Title         string          `json:"title"`
			Status        string          `json:"status"`
			Revision      int             `json:"revision"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		requirementID, err := parseJSONID(payload.RequirementID)
		if err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, RequirementID: requirementID, Title: payload.Title, Status: payload.Status, Revision: payload.Revision}
	case MessagePartRollbackProposal:
		var payload struct {
			RequirementID json.RawMessage `json:"requirement_id"`
			Reason        string          `json:"reason"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		requirementID, err := parseJSONID(payload.RequirementID)
		if err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, RequirementID: requirementID, Reason: payload.Reason}
	default:
		return fmt.Errorf("unsupported message part type %q", probe.Type)
	}
	return nil
}

func parseJSONID(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return strconv.ParseInt(asString, 10, 64)
	}
	var asNumber int64
	if err := json.Unmarshal(raw, &asNumber); err != nil {
		return 0, fmt.Errorf("invalid decimal ID %s", raw)
	}
	return asNumber, nil
}

// ConversationSummary is the latest thread-level memory snapshot of one
// conversation. Version increases monotonically per conversation.
type ConversationSummary struct {
	ID             int64
	ConversationID int64
	Summary        string
	PinnedFacts    []string
	TokenEstimate  int
	Version        int
	UpdatedAt      time.Time
}

// ConversationRepository persists conversations, messages and summaries.
type ConversationRepository interface {
	Create(ctx context.Context, conversation Conversation) error
	Get(ctx context.Context, versionID, conversationID int64) (Conversation, *ConversationSummary, error)
	GetByID(ctx context.Context, conversationID int64) (Conversation, error)
	List(ctx context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]Conversation, error)
	FindMessageByClientID(ctx context.Context, conversationID int64, clientMessageID string) (*Message, error)
	Messages(ctx context.Context, conversationID int64, after int64, limit int) ([]Message, error)
	// PostUserMessage inserts the message and bumps the conversation
	// timestamp. When the client_message_id uniqueness constraint fires it
	// returns the already-stored message instead of an error.
	PostUserMessage(ctx context.Context, conversationID int64, message Message) (*Message, bool, error)
	AppendAssistantMessage(ctx context.Context, conversationID int64, message Message) error
	Summary(ctx context.Context, conversationID int64) (*ConversationSummary, error)
	// SaveSummary inserts a new summary version computed by the adapter.
	SaveSummary(ctx context.Context, summary ConversationSummary) (ConversationSummary, error)
}

// DefaultConversationTitle renders the default clarification title.
func DefaultConversationTitle(now time.Time) string {
	return "需求澄清 " + now.Format("2006-01-02 15:04")
}

// ValidateConversationKind validates the conversation kind enum.
func ValidateConversationKind(kind string) error {
	switch kind {
	case ConversationClarification, ConversationDesign:
		return nil
	default:
		return platform.Invalid("kind", "must be one of clarification, design")
	}
}

// ValidateConversationTitle validates an explicit conversation title.
func ValidateConversationTitle(title string) error {
	return validation.String("title", title, 1, ConversationTitleMax)
}

// ValidateClientMessageID validates the optional idempotency key length.
func ValidateClientMessageID(raw string) error {
	if utf8.RuneCountInString(raw) > ClientMessageIDMax {
		return platform.Invalid("client_message_id", "must be at most 64 characters")
	}
	return nil
}

// ValidateUserMessageParts validates one user message: text and attachment
// parts only, at most ten attachments, and text within the contract bounds.
func ValidateUserMessageParts(parts []MessagePart) error {
	if len(parts) == 0 {
		return platform.Invalid("parts", "must contain at least one part")
	}
	attachments := 0
	for index, part := range parts {
		field := func(name string) string { return fmt.Sprintf("parts[%d].%s", index, name) }
		switch part.Type {
		case MessagePartText:
			if err := validation.String(field("text"), part.Text, 1, MessageTextMax); err != nil {
				return err
			}
		case MessagePartAttachment:
			if err := validateAttachmentPart(part, field); err != nil {
				return err
			}
			attachments++
		default:
			return platform.Invalid(field("type"), "must be text or attachment")
		}
	}
	if attachments > MaxMessageAttachments {
		return platform.Invalid("parts", "must contain at most 10 attachment parts")
	}
	return nil
}

// ValidateAssistantMessageParts validates one runtime-authored assistant
// message (text, requirement card or rollback proposal).
func ValidateAssistantMessageParts(parts []MessagePart) error {
	if len(parts) == 0 {
		return platform.Invalid("parts", "must contain at least one part")
	}
	for index, part := range parts {
		field := func(name string) string { return fmt.Sprintf("parts[%d].%s", index, name) }
		switch part.Type {
		case MessagePartText:
			if err := validation.String(field("text"), part.Text, 1, MessageTextMax); err != nil {
				return err
			}
		case MessagePartAttachment:
			if err := validateAttachmentPart(part, field); err != nil {
				return err
			}
		case MessagePartRequirementCard:
			if part.RequirementID <= 0 {
				return platform.Invalid(field("requirement_id"), "must be a positive decimal ID")
			}
			if err := validation.String(field("title"), part.Title, 1, RequirementTitleMax); err != nil {
				return err
			}
			switch RequirementStatus(part.Status) {
			case RequirementDraft, RequirementClarified, RequirementClosed, RequirementObsolete:
			default:
				return platform.Invalid(field("status"), "must be one of draft, clarified, closed, obsolete")
			}
			if part.Revision <= 0 {
				return platform.Invalid(field("revision"), "must be a positive integer")
			}
		case MessagePartRollbackProposal:
			if part.RequirementID <= 0 {
				return platform.Invalid(field("requirement_id"), "must be a positive decimal ID")
			}
		default:
			return platform.Invalid(field("type"), "must be text, attachment, requirement_card or rollback_proposal")
		}
	}
	return nil
}

func validateAttachmentPart(part MessagePart, field func(string) string) error {
	if !messageSHA256Pattern.MatchString(part.SHA256) {
		return platform.Invalid(field("sha256"), "sha256 must be 64 lowercase hex characters")
	}
	if err := validation.String(field("name"), part.Name, 1, 255); err != nil {
		return err
	}
	if err := validation.String(field("mime"), part.MIME, 1, 255); err != nil {
		return err
	}
	if part.Size <= 0 || part.Size > MaxAttachmentBytes {
		return platform.Invalid(field("size"), "must be between 1 and 10485760 bytes")
	}
	return nil
}

// ValidateConversationSummary validates a summary revision and its pinned
// facts (at most 20 facts of at most 500 characters each).
func ValidateConversationSummary(summary string, pinnedFacts []string) error {
	if err := validation.String("summary", summary, 1, SummaryTextMax); err != nil {
		return err
	}
	if len(pinnedFacts) > PinnedFactsMax {
		return platform.Invalid("pinned_facts", "must contain at most 20 facts")
	}
	for index, fact := range pinnedFacts {
		if err := validation.String(fmt.Sprintf("pinned_facts[%d]", index), fact, 1, PinnedFactMaxLength); err != nil {
			return err
		}
		if strings.TrimSpace(fact) == "" {
			return platform.Invalid(fmt.Sprintf("pinned_facts[%d]", index), "must not be empty")
		}
	}
	return nil
}

// EstimateTokens returns the conservative token estimate (runes/2, at least 1)
// used for summary bookkeeping.
func EstimateTokens(text string) int {
	estimate := utf8.RuneCountInString(text) / 2
	if estimate < 1 {
		return 1
	}
	return estimate
}
