package version

import (
	"bytes"
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

// Conversation message kinds (design DB-3, B §5.1/§5.2). The Host reply is
// text; a structured question is question; the DB CHECK constraint is the final
// guard, so the application validates before writing.
const (
	MessageKindText     = "text"
	MessageKindQuestion = "question"
	MessageKindAnswer   = "answer"
	MessageKindSystem   = "system"
	MessageKindArtifact = "artifact"
)

// ConversationWriterLeaseDuration is the single-writer lease of one
// conversation (B §5.1): 90 seconds, renewed by the Host's heartbeat. A crashed
// turn is recoverable once the lease expires.
const ConversationWriterLeaseDuration = 90 * time.Second

// hostTurnClientMessageIDPrefix is the reserved client_message_id namespace of
// a Host turn's idempotency key. User-supplied client ids must not collide with
// it, otherwise a retried turn could resolve a user message (or vice versa).
const hostTurnClientMessageIDPrefix = "host-turn:"

// Message roles and part types.
const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"

	MessagePartText             = "text"
	MessagePartThinking         = "thinking"
	MessagePartToolCall         = "tool_call"
	MessagePartJobCard          = "job_card"
	MessagePartAttachment       = "attachment"
	MessagePartImage            = "image"
	MessagePartRequirementCard  = "requirement_card"
	MessagePartCaseCard         = "case_card"
	MessagePartRollbackProposal = "rollback_proposal"
	MessagePartError            = "error"
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

	// Streamed part bounds (spec §9.1). The streaming table only stores these
	// eight DB-checked types; rollback_proposal/image stay snapshot-only.
	MaxMessageStreamParts  = 256
	ToolCallIDMax          = 128
	ToolCallNameMax        = 200
	ToolCallErrorMax       = 2000
	MessageErrorCodeMax    = 64
	MessageErrorMessageMax = 2000
	MessageErrorActionMax  = 500
	CaseCardTitleMax       = 255
)

// Conversation domain errors.
var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrMessageNotFound      = errors.New("message not found")
	// ErrConversationBusy reports that another Host turn holds the conversation's
	// single-writer lease (typed 409 conversation_busy at the API edge).
	ErrConversationBusy = errors.New("conversation is being written by another turn")
	// ErrNotConversationWriter reports that the caller does not own the active
	// writer lease (typed 403 not_writer at the API edge). Background jobs have
	// no conversation.append capability; only the lease owner may write Host
	// replies (R-B2-3/R-B2-4).
	ErrNotConversationWriter = errors.New("not the active writer of the conversation")
	// ErrJobCardCancelled reports that a job-card rewrite was rejected because
	// the stored card is already cancelled: a late runner projection must never
	// overwrite the terminal cancel snapshot. Callers treat it as an ignored,
	// best-effort write-back.
	ErrJobCardCancelled = errors.New("job card is cancelled")
)

var messageSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Conversation is one human/AI clarification thread.
type Conversation struct {
	ID        int64
	VersionID int64
	Kind      string
	Title     string
	Status    string
	// ModelOverride is the 0.2.1 conversation-level model identifier
	// (`llm_model.model`). Nil means "use the LLM config default_model_id";
	// the runtime never switches models on its own (D24).
	ModelOverride *string
	// ActiveTurnID/ActiveAgent/LeaseUntil are the single-writer lease of the
	// conversation (B §5.1 DB-2): while ActiveTurnID is non-nil that Host turn is
	// the only writer allowed to append assistant messages, and LeaseUntil gates
	// recovery after a crash. All three are cleared together.
	ActiveTurnID  *int64
	ActiveAgent   *string
	LeaseUntil    *time.Time
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

// ConversationMessagePart is one append-only streaming part row of a
// conversation message (`conversation_message_part`, spec §9.1/§12.2). Type is
// the DB `part_type`; Payload is the opaque JSON payload owned by the part kind
// (the read API forwards it without interpreting or rewriting it).
type ConversationMessagePart struct {
	Seq       int
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
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
	// AuthorAgent is the agent profile key that authored an assistant message
	// (DB conversation_message.author_agent, B §5.1). It is empty for user
	// messages and for server-generated cards (job_card/requirement_card).
	AuthorAgent string
	// Kind is the conversation_message kind enum (text/question/answer/system/
	// artifact, DB-3). Empty is normalized to text for legacy writers.
	Kind string
	// ActualModel is the model that actually generated an assistant message,
	// read from the linked agent_run. Empty for user messages and for
	// assistant messages without a run (contracts `actual_model`).
	ActualModel string
	CreatedAt   time.Time

	// StreamParts is the append-only conversation_message_part aggregate written
	// in the same transaction as an assistant message (0.2.1 streaming parts).
	// Seq is 1-based and strictly increasing within the message; the repository
	// enforces the (message_id, seq) uniqueness. It is not part of the message
	// snapshot column.
	StreamParts []ConversationMessagePart
}

// MessagePart is the discriminated union of message parts. The JSON shape is
// fixed by the P1 OpenAPI MessagePart oneOf (extended by 0.2.1 with the
// thinking/tool_call/job_card/case_card/error branches).
type MessagePart struct {
	Type          string
	Text          string
	SHA256        string
	Name          string
	MIME          string
	Size          int64
	DataRef       string
	Width         int
	Height        int
	RequirementID int64
	Title         string
	Status        string
	Revision      int
	// Reason is the rollback_proposal explanation and, on a terminal failed
	// job_card, the user-facing failure reason (Task 75).
	Reason string

	// thinking
	DurationMS int64
	// tool_call
	CallID    string
	Arguments json.RawMessage
	Result    json.RawMessage
	Error     string
	// job_card
	JobID      int64
	JobKind    string
	Current    int
	Total      int
	Cancelable *bool
	// case_card
	CaseID        int64
	CaseVersionID int64
	// error; job_card reuses Code as the typed failure code (Task 75)
	Code      string
	Message   string
	Retryable bool
	Action    string
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
	case MessagePartImage:
		return json.Marshal(struct {
			Type    string `json:"type"`
			MIME    string `json:"mime"`
			DataRef string `json:"data_ref"`
			Width   int    `json:"width"`
			Height  int    `json:"height"`
		}{Type: p.Type, MIME: p.MIME, DataRef: p.DataRef, Width: p.Width, Height: p.Height})
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
	case MessagePartThinking:
		return json.Marshal(struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			DurationMS int64  `json:"duration_ms"`
		}{Type: p.Type, Text: p.Text, DurationMS: p.DurationMS})
	case MessagePartToolCall:
		payload := struct {
			Type       string          `json:"type"`
			CallID     string          `json:"call_id"`
			Name       string          `json:"name"`
			Status     string          `json:"status"`
			Arguments  json.RawMessage `json:"arguments,omitempty"`
			Result     json.RawMessage `json:"result,omitempty"`
			DurationMS int64           `json:"duration_ms"`
			Error      string          `json:"error,omitempty"`
		}{
			Type: p.Type, CallID: p.CallID, Name: p.Name, Status: p.Status,
			Arguments: p.Arguments, Result: p.Result, DurationMS: p.DurationMS, Error: p.Error,
		}
		return json.Marshal(payload)
	case MessagePartJobCard:
		payload := struct {
			Type       string `json:"type"`
			JobID      string `json:"job_id"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			Current    int    `json:"current"`
			Total      int    `json:"total"`
			Cancelable *bool  `json:"cancelable,omitempty"`
			// Code/Reason explain a terminal failed job (Task 75); they are
			// omitted on every other snapshot so a healthy card stays unchanged.
			Code   string `json:"code,omitempty"`
			Reason string `json:"reason,omitempty"`
		}{
			Type: p.Type, JobID: strconv.FormatInt(p.JobID, 10), Kind: p.JobKind,
			Status: p.Status, Current: p.Current, Total: p.Total, Cancelable: p.Cancelable,
			Code: p.Code, Reason: p.Reason,
		}
		return json.Marshal(payload)
	case MessagePartCaseCard:
		payload := struct {
			Type          string `json:"type"`
			CaseID        string `json:"case_id"`
			CaseVersionID string `json:"case_version_id,omitempty"`
			Title         string `json:"title"`
			Status        string `json:"status"`
		}{
			Type: p.Type, CaseID: strconv.FormatInt(p.CaseID, 10), Title: p.Title, Status: p.Status,
		}
		if p.CaseVersionID > 0 {
			payload.CaseVersionID = strconv.FormatInt(p.CaseVersionID, 10)
		}
		return json.Marshal(payload)
	case MessagePartError:
		return json.Marshal(struct {
			Type      string `json:"type"`
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
			Action    string `json:"action,omitempty"`
		}{Type: p.Type, Code: p.Code, Message: p.Message, Retryable: p.Retryable, Action: p.Action})
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
	case MessagePartImage:
		var payload struct {
			MIME    string `json:"mime"`
			DataRef string `json:"data_ref"`
			Width   int    `json:"width"`
			Height  int    `json:"height"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, MIME: payload.MIME, DataRef: payload.DataRef, Width: payload.Width, Height: payload.Height}
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
	case MessagePartThinking:
		var payload struct {
			Text       string `json:"text"`
			DurationMS int64  `json:"duration_ms"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, Text: payload.Text, DurationMS: payload.DurationMS}
	case MessagePartToolCall:
		var payload struct {
			CallID     string          `json:"call_id"`
			Name       string          `json:"name"`
			Status     string          `json:"status"`
			Arguments  json.RawMessage `json:"arguments"`
			Result     json.RawMessage `json:"result"`
			DurationMS int64           `json:"duration_ms"`
			Error      string          `json:"error"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		*p = MessagePart{
			Type: probe.Type, CallID: payload.CallID, Name: payload.Name, Status: payload.Status,
			Arguments: payload.Arguments, Result: payload.Result, DurationMS: payload.DurationMS, Error: payload.Error,
		}
	case MessagePartJobCard:
		var payload struct {
			JobID      json.RawMessage `json:"job_id"`
			Kind       string          `json:"kind"`
			Status     string          `json:"status"`
			Current    int             `json:"current"`
			Total      int             `json:"total"`
			Cancelable *bool           `json:"cancelable"`
			Code       string          `json:"code"`
			Reason     string          `json:"reason"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		jobID, err := parseJSONID(payload.JobID)
		if err != nil {
			return err
		}
		*p = MessagePart{
			Type: probe.Type, JobID: jobID, JobKind: payload.Kind, Status: payload.Status,
			Current: payload.Current, Total: payload.Total, Cancelable: payload.Cancelable,
			Code: payload.Code, Reason: payload.Reason,
		}
	case MessagePartCaseCard:
		var payload struct {
			CaseID        json.RawMessage `json:"case_id"`
			CaseVersionID json.RawMessage `json:"case_version_id"`
			Title         string          `json:"title"`
			Status        string          `json:"status"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		caseID, err := parseJSONID(payload.CaseID)
		if err != nil {
			return err
		}
		caseVersionID, err := parseJSONID(payload.CaseVersionID)
		if err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, CaseID: caseID, CaseVersionID: caseVersionID, Title: payload.Title, Status: payload.Status}
	case MessagePartError:
		var payload struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
			Action    string `json:"action"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		*p = MessagePart{Type: probe.Type, Code: payload.Code, Message: payload.Message, Retryable: payload.Retryable, Action: payload.Action}
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
	// UpdateModelOverride sets or clears the conversation-level model override
	// and bumps updated_at, returning the stored conversation.
	UpdateModelOverride(ctx context.Context, versionID, conversationID int64, modelOverride *string, updatedAt time.Time) (Conversation, error)
	List(ctx context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]Conversation, error)
	FindMessageByClientID(ctx context.Context, conversationID int64, clientMessageID string) (*Message, error)
	Messages(ctx context.Context, conversationID int64, after int64, limit int) ([]Message, error)
	// MessageParts returns the append-only streaming parts of one UI message,
	// ascending by seq. The message must belong to the conversation and the
	// conversation to the version; otherwise it returns ErrMessageNotFound.
	MessageParts(ctx context.Context, versionID, conversationID, messageID int64) ([]ConversationMessagePart, error)
	// FindJobCardMessage returns the assistant message carrying jobID's
	// job_card part in one version-scoped conversation, or ErrMessageNotFound.
	// It is the idempotency lookup behind the one-card-per-job contract.
	FindJobCardMessage(ctx context.Context, versionID, conversationID, jobID int64) (*Message, error)
	// UpdateJobCardMessage rewrites the stored snapshot part of jobID and its
	// conversation_message_part payload inside messageID without appending a
	// new message or part row. ErrMessageNotFound when the scoped message or
	// its card is absent. A stored cancelled card is sticky: a rewrite whose
	// part is not cancelled is rejected with ErrJobCardCancelled and nothing is
	// written (a late runner projection must not overwrite a cancel). The check
	// runs under the row lock, so it cannot lose a race with the cancel write.
	UpdateJobCardMessage(ctx context.Context, versionID, conversationID, messageID, jobID int64, part MessagePart) error
	// ReopenJobCardMessage rewrites the stored card of a retried job even when
	// it is currently cancelled — the explicit retry reopen, and the only write
	// the sticky cancelled rule above lets replace a cancelled card. ErrMessageNotFound
	// when the scoped message or its card is absent.
	ReopenJobCardMessage(ctx context.Context, versionID, conversationID, messageID, jobID int64, part MessagePart) error
	// PostUserMessage inserts the message and bumps the conversation
	// timestamp. When the client_message_id uniqueness constraint fires it
	// returns the already-stored message instead of an error.
	PostUserMessage(ctx context.Context, conversationID int64, message Message) (*Message, bool, error)
	AppendAssistantMessage(ctx context.Context, conversationID int64, message Message) error
	// AcquireWriter atomically claims the conversation's single-writer lease for
	// (turnID, agent) until leaseUntil. It succeeds when the conversation is idle
	// (no active turn), when the stored lease already expired at now, or when the
	// same turn/agent re-acquires (idempotent retry). A different live turn is
	// ErrConversationBusy; an unknown conversation is ErrConversationNotFound.
	AcquireWriter(ctx context.Context, conversationID, turnID int64, agent string, now, leaseUntil time.Time) error
	// RenewWriterLease extends the lease of the current owner (heartbeat). It
	// fails with ErrNotConversationWriter when turnID/agent no longer own a
	// non-expired lease, and ErrConversationNotFound for an unknown conversation.
	RenewWriterLease(ctx context.Context, conversationID, turnID int64, agent string, now, leaseUntil time.Time) error
	// ReleaseWriter clears the lease of the current owner (clean turn end). A
	// foreign turn is ErrNotConversationWriter and an already-released lease is a
	// no-op; an unknown conversation is ErrConversationNotFound.
	ReleaseWriter(ctx context.Context, conversationID, turnID int64, agent string) error
	// AppendAssistantMessageAsWriter inserts one assistant message only when
	// (turnID, agent) owns the live lease. An idle conversation or an expired
	// lease is taken over atomically inside the write transaction (crash
	// recovery); a different live writer is ErrConversationBusy. A repeated
	// client_message_id returns the stored message with created=false instead of
	// appending a duplicate (turn-level idempotency).
	AppendAssistantMessageAsWriter(ctx context.Context, conversationID int64, message Message, turnID int64, agent string, now, leaseUntil time.Time) (*Message, bool, error)
	Summary(ctx context.Context, conversationID int64) (*ConversationSummary, error)
	// SaveSummary inserts a new summary version computed by the adapter.
	SaveSummary(ctx context.Context, summary ConversationSummary) (ConversationSummary, error)
	// ListReconcilable returns the most recent clarification conversations that
	// have messages, newest conversation first.
	ListReconcilable(ctx context.Context, limit int) ([]ReconcilerConversation, error)
}

// DefaultConversationTitle renders the default clarification title.
func DefaultConversationTitle(now time.Time) string {
	return "需求澄清 " + now.Format("2006-01-02 15:04")
}

// DefaultDesignConversationTitle renders the default design-chat title.
func DefaultDesignConversationTitle(now time.Time) string {
	return "用例设计 " + now.Format("2006-01-02 15:04")
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

// ValidateConversationMessageKind validates the DB-3 conversation_message kind
// enum. Empty is the legacy default: NormalizeMessageKind maps it to text before
// a write, and callers validate the normalized value.
func ValidateConversationMessageKind(kind string) error {
	switch kind {
	case MessageKindText, MessageKindQuestion, MessageKindAnswer, MessageKindSystem, MessageKindArtifact:
		return nil
	default:
		return platform.Invalid("kind", "must be one of text, question, answer, system, artifact")
	}
}

// NormalizeMessageKind maps the empty legacy value to text and leaves explicit
// kinds untouched.
func NormalizeMessageKind(kind string) string {
	if kind == "" {
		return MessageKindText
	}
	return kind
}

// HostTurnClientMessageID derives the reserved idempotency key of one Host
// turn. Assistant message writes store it in conversation_message.
// client_message_id, so a retried turn resolves the stored message instead of
// appending a duplicate (B §5.1: client_message_id / turn_id).
func HostTurnClientMessageID(turnID int64) string {
	return hostTurnClientMessageIDPrefix + strconv.FormatInt(turnID, 10)
}

// IsReservedClientMessageID reports whether raw collides with the Host-turn
// idempotency namespace. User client ids may not use it.
func IsReservedClientMessageID(raw string) bool {
	return strings.HasPrefix(raw, hostTurnClientMessageIDPrefix)
}

// ValidateClientMessageID validates the optional idempotency key length and
// keeps the reserved Host-turn namespace out of user input.
func ValidateClientMessageID(raw string) error {
	if utf8.RuneCountInString(raw) > ClientMessageIDMax {
		return platform.Invalid("client_message_id", "must be at most 64 characters")
	}
	if IsReservedClientMessageID(raw) {
		return platform.Invalid("client_message_id", "must not use the reserved host-turn idempotency namespace")
	}
	return nil
}

// ValidateUserMessageParts validates one user message: text, attachment, image
// and case_card parts only, at most ten attachments/images, and text within the
// contract bounds. A user case_card is a reference to an existing case (the
// "hand to AI" action), not a secret or an uploaded artifact, so it follows the
// same payload rules as the assistant case_card branch; the runtime-only
// requirement_card/rollback_proposal parts stay rejected.
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
		case MessagePartImage:
			if err := validateImageMessagePart(part, field); err != nil {
				return err
			}
			attachments++
		case MessagePartCaseCard:
			if err := validateCaseCardPart(part, field); err != nil {
				return err
			}
		default:
			return platform.Invalid(field("type"), "must be text, attachment, image or case_card")
		}
	}
	if attachments > MaxMessageAttachments {
		return platform.Invalid("parts", "must contain at most 10 attachment parts")
	}
	return nil
}

// ValidateAssistantMessageParts validates one runtime-authored assistant
// message. It accepts the eight conversation_message_part contract types plus
// the P1 rollback_proposal part (snapshot-only) and enforces each branch's
// required payload fields and bounds (spec §9.1).
func ValidateAssistantMessageParts(parts []MessagePart) error {
	if len(parts) == 0 {
		return platform.Invalid("parts", "must contain at least one part")
	}
	if len(parts) > MaxMessageStreamParts {
		return platform.Invalid("parts", "must contain at most 256 parts")
	}
	for index, part := range parts {
		field := func(name string) string { return fmt.Sprintf("parts[%d].%s", index, name) }
		switch part.Type {
		case MessagePartText:
			if err := validation.String(field("text"), part.Text, 1, MessageTextMax); err != nil {
				return err
			}
		case MessagePartThinking:
			if err := validation.String(field("text"), part.Text, 1, MessageTextMax); err != nil {
				return err
			}
			if part.DurationMS < 0 {
				return platform.Invalid(field("duration_ms"), "must be a non-negative integer")
			}
		case MessagePartToolCall:
			if err := validation.String(field("call_id"), part.CallID, 1, ToolCallIDMax); err != nil {
				return err
			}
			if err := validation.String(field("name"), part.Name, 1, ToolCallNameMax); err != nil {
				return err
			}
			switch part.Status {
			case "running", "done", "failed":
			default:
				return platform.Invalid(field("status"), "must be one of running, done, failed")
			}
			if part.DurationMS < 0 {
				return platform.Invalid(field("duration_ms"), "must be a non-negative integer")
			}
			if err := validateJSONObjectField(field("arguments"), part.Arguments, false); err != nil {
				return err
			}
			if err := validateJSONObjectField(field("result"), part.Result, true); err != nil {
				return err
			}
			if err := validation.String(field("error"), part.Error, 0, ToolCallErrorMax); err != nil {
				return err
			}
		case MessagePartJobCard:
			if !IsValidJobCardKind(part.JobKind) {
				return platform.Invalid(field("kind"), "must be a job kind of the 0.2.1 contract")
			}
			if err := validateJobCardPayload(part, field); err != nil {
				return err
			}
		case MessagePartCaseCard:
			if err := validateCaseCardPart(part, field); err != nil {
				return err
			}
		case MessagePartError:
			if err := validation.String(field("code"), part.Code, 1, MessageErrorCodeMax); err != nil {
				return err
			}
			if err := validation.String(field("message"), part.Message, 1, MessageErrorMessageMax); err != nil {
				return err
			}
			if err := validation.String(field("action"), part.Action, 0, MessageErrorActionMax); err != nil {
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
			return platform.Invalid(field("type"),
				"must be text, thinking, tool_call, job_card, attachment, requirement_card, case_card, error or rollback_proposal")
		}
	}
	return nil
}

// IsStreamingMessagePartType reports whether a part type has a
// conversation_message_part row (the DB part_type CHECK enum). image and
// rollback_proposal are snapshot-only message parts.
func IsStreamingMessagePartType(partType string) bool {
	switch partType {
	case MessagePartText, MessagePartThinking, MessagePartToolCall, MessagePartJobCard,
		MessagePartAttachment, MessagePartRequirementCard, MessagePartCaseCard, MessagePartError:
		return true
	default:
		return false
	}
}

// StreamingMessagePartTypes returns the conversation_message_part type enum in
// contract order (openapi MessagePartView.part_type).
func StreamingMessagePartTypes() []string {
	return []string{
		MessagePartText,
		MessagePartThinking,
		MessagePartToolCall,
		MessagePartJobCard,
		MessagePartAttachment,
		MessagePartRequirementCard,
		MessagePartCaseCard,
		MessagePartError,
	}
}

var jobCardKinds = map[string]struct{}{
	"explore.login": {}, "explore.page": {}, "explore.api": {}, "explore.coverage": {},
	"explore.gap": {}, "design.cases": {}, "review.cases": {}, "execute.batch": {},
	"analyze.failure": {}, "report.version": {}, "knowledge.propose": {},
}

// IsValidJobCardKind reports whether kind is one of the frozen 0.2.1 job kinds.
func IsValidJobCardKind(kind string) bool {
	_, ok := jobCardKinds[kind]
	return ok
}

// conversationJobCardKinds are the P1/P2 conversation-writing work types that
// run as background jobs for one conversation and therefore appear in the
// thread as job cards. They are work item types, not frozen 0.2.1 contract job
// kinds, so they stay out of IsValidJobCardKind: the contract enum sync for
// them is owned by the contract task (Task 50).
var conversationJobCardKinds = map[string]struct{}{
	"chat.turn": {}, "requirement.curate": {}, "design.round": {},
}

// IsConversationJobCardKind reports whether kind is a conversation-writing work
// type projected as a background job card (chat.turn/requirement.curate/
// design.round).
func IsConversationJobCardKind(kind string) bool {
	_, ok := conversationJobCardKinds[kind]
	return ok
}

// JobCardStatusCancelled is the terminal cancelled status of a job_card part
// (contract §9.1).
const JobCardStatusCancelled = "cancelled"

// JobCardRewriteAllowed reports whether a stored job-card snapshot may be
// replaced by a part with nextStatus. A cancelled card is terminal and sticky:
// a late runner projection (running/open/done/failed) must never overwrite it,
// while the repeated cancel snapshot is allowed. The repository re-checks this
// rule under its row lock so a concurrent late write cannot win;
// ReopenJobCardMessage is the one explicit bypass used by a user-initiated
// retry.
func JobCardRewriteAllowed(storedStatus, nextStatus string) bool {
	return storedStatus != JobCardStatusCancelled || nextStatus == JobCardStatusCancelled
}

// ValidateJobCardMessage validates the single mutable job_card part stored for
// one background job in a conversation. It accepts the contract job kinds plus
// the conversation work types above; every other field follows the MessagePart
// job_card contract. Generic assistant parts stay restricted to the contract
// kinds through ValidateAssistantMessageParts.
func ValidateJobCardMessage(part MessagePart) error {
	if part.Type != MessagePartJobCard {
		return platform.Invalid("type", "must be job_card")
	}
	field := func(name string) string { return "parts[0]." + name }
	kind := strings.TrimSpace(part.JobKind)
	if !IsValidJobCardKind(kind) && !IsConversationJobCardKind(kind) {
		return platform.Invalid(field("kind"), "must be a contract job kind or a conversation work type")
	}
	return validateJobCardPayload(part, field)
}

// validateJobCardPayload validates the job_card fields shared by the generic
// assistant-part validator and the conversation card snapshot. The caller owns
// the accepted kind set.
func validateJobCardPayload(part MessagePart, field func(string) string) error {
	if part.JobID <= 0 {
		return platform.Invalid(field("job_id"), "must be a positive decimal ID")
	}
	cancelable, ok := jobCardStatusCancelable(part.Status)
	if !ok {
		return platform.Invalid(field("status"), "must be one of open, claimed, running, waiting_human, done, failed, cancelled")
	}
	if part.Current < 0 || part.Total < 0 {
		return platform.Invalid(field("current"), "current/total must be non-negative integers")
	}
	if part.Total > 0 && part.Current > part.Total {
		return platform.Invalid(field("current"), "must not exceed total")
	}
	if part.Cancelable != nil && *part.Cancelable && !cancelable {
		return platform.Invalid(field("cancelable"), "must be false for a terminal job status")
	}
	// Task 75: a terminal failed card may carry the typed failure code and the
	// user-facing reason. They are optional so every earlier snapshot and every
	// successful card keeps its previous shape.
	if part.Code != "" {
		if err := validation.String(field("code"), part.Code, 0, MessageErrorCodeMax); err != nil {
			return err
		}
	}
	if part.Reason != "" {
		if err := validation.String(field("reason"), part.Reason, 0, MessageErrorMessageMax); err != nil {
			return err
		}
	}
	return nil
}

// jobCardStatusCancelable validates the job_card status enum and reports
// whether the derived UI may offer a cancel action for it.
func jobCardStatusCancelable(status string) (bool, bool) {
	switch status {
	case "open", "claimed", "running", "waiting_human":
		return true, true
	case "done", "failed", "cancelled":
		return false, true
	default:
		return false, false
	}
}

// validateJSONObjectField checks that an optional raw JSON field is a JSON
// object. allowNull additionally accepts a literal null (tool_call.result).
func validateJSONObjectField(field string, raw json.RawMessage, allowNull bool) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if allowNull && bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return platform.Invalid(field, "must be a JSON object")
	}
	return nil
}

// validateCaseCardPart validates the case_card payload shared by the user and
// assistant part validators: a positive case ID, an optional positive case
// version ID, a bounded title and the conversation case status enum.
func validateCaseCardPart(part MessagePart, field func(string) string) error {
	if part.CaseID <= 0 {
		return platform.Invalid(field("case_id"), "must be a positive decimal ID")
	}
	if part.CaseVersionID < 0 {
		return platform.Invalid(field("case_version_id"), "must be a positive decimal ID or null")
	}
	if err := validation.String(field("title"), part.Title, 1, CaseCardTitleMax); err != nil {
		return err
	}
	switch part.Status {
	case "draft", "confirmed", "stale", "superseded":
	default:
		return platform.Invalid(field("status"), "must be one of draft, confirmed, stale, superseded")
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

// validateImageMessagePart validates the R5 image payload: a supported image
// MIME, a content-ledger SHA-256 data_ref and bounded pixel dimensions.
func validateImageMessagePart(part MessagePart, field func(string) string) error {
	if !IsSupportedImageMIME(part.MIME) {
		return platform.Invalid(field("mime"), "must be image/png, image/jpeg or image/webp")
	}
	if !messageSHA256Pattern.MatchString(part.DataRef) {
		return platform.Invalid(field("data_ref"), "must be the 64 lowercase hex content sha256")
	}
	if err := ValidateImageDimensions(part.Width, part.Height); err != nil {
		return err
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

// ReconcilerMessage is one message with the agent profile that wrote it (empty
// for user messages or assistant messages without a run).
type ReconcilerMessage struct {
	ID              int64
	Role            string
	AgentProfileKey string
}

// ReconcilerConversation is one conversation with its messages, used by the
// reconciler to detect runs that never finished. Kind distinguishes
// clarification (P1 rules) from design (P2 rules).
type ReconcilerConversation struct {
	ID        int64
	VersionID int64
	Kind      string
	Messages  []ReconcilerMessage
}
