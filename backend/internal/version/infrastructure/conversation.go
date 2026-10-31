package versionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const conversationColumns = `c.id, c.version_id, c.kind, c.title, c.status, c.created_by,
	c.created_at, c.updated_at,
	(SELECT count(*) FROM conversation_message m WHERE m.conversation_id = c.id),
	(SELECT max(m.created_at) FROM conversation_message m WHERE m.conversation_id = c.id)`

const conversationMessageColumns = `id, conversation_id, role, parts, refs, token_usage,
	actor_id, agent_run_id, client_message_id, reply_to_message_id, created_at`

const conversationSummaryColumns = `id, conversation_id, summary, pinned_facts,
	token_estimate, version, updated_at`

// ConversationRepository stores conversations, their UI-thread messages and
// the versioned thread summaries.
type ConversationRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewConversationRepository wraps a pgx pool. The generator supplies summary
// row IDs; business message/conversation IDs are assigned by the application
// service.
func NewConversationRepository(pool *pgxpool.Pool, ids platformid.Generator) *ConversationRepository {
	return &ConversationRepository{pool: pool, ids: ids}
}

// Create inserts one conversation.
func (r *ConversationRepository) Create(ctx context.Context, conversation version.Conversation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO conversation (id, version_id, kind, title, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, conversation.ID, conversation.VersionID, conversation.Kind, conversation.Title, conversation.Status,
		conversation.CreatedBy, conversation.CreatedAt, conversation.UpdatedAt)
	if err != nil {
		return wrapConversationDB("insert conversation", err)
	}
	return nil
}

// Get loads one conversation scoped to a version plus its latest summary.
func (r *ConversationRepository) Get(ctx context.Context, versionID, conversationID int64) (version.Conversation, *version.ConversationSummary, error) {
	conversation, err := scanConversation(r.pool.QueryRow(ctx, `
		SELECT `+conversationColumns+` FROM conversation c
		WHERE c.id = $1 AND c.version_id = $2
	`, conversationID, versionID))
	if err != nil {
		return version.Conversation{}, nil, wrapConversationDB("get conversation", err)
	}
	summary, err := r.Summary(ctx, conversationID)
	if err != nil {
		return version.Conversation{}, nil, err
	}
	return conversation, summary, nil
}

// GetByID loads one conversation without a version scope (used by the runtime
// message-write port).
func (r *ConversationRepository) GetByID(ctx context.Context, conversationID int64) (version.Conversation, error) {
	conversation, err := scanConversation(r.pool.QueryRow(ctx, `
		SELECT `+conversationColumns+` FROM conversation c WHERE c.id = $1
	`, conversationID))
	if err != nil {
		return version.Conversation{}, wrapConversationDB("get conversation", err)
	}
	return conversation, nil
}

// List returns conversations ordered by ID descending.
func (r *ConversationRepository) List(ctx context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]version.Conversation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+conversationColumns+`
		FROM conversation c
		WHERE c.version_id = $1
		  AND ($2 = '' OR c.kind = $2)
		  AND ($3::BIGINT IS NULL OR c.id < $3)
		ORDER BY c.id DESC
		LIMIT $4
	`, versionID, kind, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	conversations := make([]version.Conversation, 0)
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	return conversations, nil
}

// FindMessageByClientID resolves the idempotency key inside one conversation.
func (r *ConversationRepository) FindMessageByClientID(ctx context.Context, conversationID int64, clientMessageID string) (*version.Message, error) {
	message, err := scanMessage(r.pool.QueryRow(ctx, `
		SELECT `+conversationMessageColumns+` FROM conversation_message
		WHERE conversation_id = $1 AND client_message_id = $2
	`, conversationID, clientMessageID))
	if err != nil {
		return nil, wrapMessageDB("find message by client id", err)
	}
	return &message, nil
}

// Messages returns messages with id > after in ascending order.
func (r *ConversationRepository) Messages(ctx context.Context, conversationID int64, after int64, limit int) ([]version.Message, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+conversationMessageColumns+` FROM conversation_message
		WHERE conversation_id = $1 AND id > $2
		ORDER BY id
		LIMIT $3
	`, conversationID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()
	messages := make([]version.Message, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan conversation message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list conversation messages: %w", err)
	}
	return messages, nil
}

// PostUserMessage inserts the user message and bumps the conversation
// timestamp. A client_message_id conflict returns the existing message with
// created=false.
func (r *ConversationRepository) PostUserMessage(ctx context.Context, conversationID int64, message version.Message) (*version.Message, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("post user message: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	parts, refs, tokenUsage, err := encodeMessageJSON(message)
	if err != nil {
		return nil, false, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO conversation_message (`+conversationMessageColumns+`)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11)
		ON CONFLICT (conversation_id, client_message_id) DO NOTHING
	`, message.ID, conversationID, message.Role, parts, refs, tokenUsage,
		message.ActorID, message.AgentRunID, message.ClientMessageID,
		message.ReplyToMessageID, message.CreatedAt)
	if err != nil {
		return nil, false, wrapConversationDB("insert conversation message", err)
	}
	if tag.RowsAffected() == 0 {
		if message.ClientMessageID == nil {
			return nil, false, fmt.Errorf("post user message: insert was ignored without a client message id")
		}
		existing, err := scanMessage(tx.QueryRow(ctx, `
			SELECT `+conversationMessageColumns+` FROM conversation_message
			WHERE conversation_id = $1 AND client_message_id = $2
		`, conversationID, *message.ClientMessageID))
		if err != nil {
			return nil, false, wrapMessageDB("get existing message", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("post user message: commit: %w", err)
		}
		return &existing, false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE conversation SET updated_at = $2 WHERE id = $1`, conversationID, message.CreatedAt); err != nil {
		return nil, false, wrapConversationDB("touch conversation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("post user message: commit: %w", err)
	}
	return nil, true, nil
}

// AppendAssistantMessage inserts one assistant message and bumps the
// conversation timestamp.
func (r *ConversationRepository) AppendAssistantMessage(ctx context.Context, conversationID int64, message version.Message) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("append assistant message: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	parts, refs, tokenUsage, err := encodeMessageJSON(message)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversation_message (`+conversationMessageColumns+`)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11)
	`, message.ID, conversationID, message.Role, parts, refs, tokenUsage,
		message.ActorID, message.AgentRunID, message.ClientMessageID,
		message.ReplyToMessageID, message.CreatedAt); err != nil {
		return wrapConversationDB("insert assistant message", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE conversation SET updated_at = $2 WHERE id = $1`, conversationID, message.CreatedAt); err != nil {
		return wrapConversationDB("touch conversation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("append assistant message: commit: %w", err)
	}
	return nil
}

// Summary returns the latest summary revision or nil when none exists.
func (r *ConversationRepository) Summary(ctx context.Context, conversationID int64) (*version.ConversationSummary, error) {
	summary, err := scanSummary(r.pool.QueryRow(ctx, `
		SELECT `+conversationSummaryColumns+` FROM conversation_summary
		WHERE conversation_id = $1 ORDER BY version DESC LIMIT 1
	`, conversationID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get conversation summary: %w", err)
	}
	return &summary, nil
}

// SaveSummary inserts the next summary version atomically and returns the
// stored row.
func (r *ConversationRepository) SaveSummary(ctx context.Context, summary version.ConversationSummary) (version.ConversationSummary, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.ConversationSummary{}, fmt.Errorf("save conversation summary: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	saved := summary
	saved.ID = r.ids.Next()
	if err := tx.QueryRow(ctx, `
		INSERT INTO conversation_summary (
			id, conversation_id, summary, pinned_facts, token_estimate, version, updated_at
		)
		VALUES ($1, $2, $3, $4::jsonb, $5,
			COALESCE((SELECT max(version) FROM conversation_summary WHERE conversation_id = $2), 0) + 1, $6)
		RETURNING version, updated_at
	`, saved.ID, summary.ConversationID, summary.Summary, encodeStringSlice(summary.PinnedFacts),
		summary.TokenEstimate, summary.UpdatedAt).Scan(&saved.Version, &saved.UpdatedAt); err != nil {
		return version.ConversationSummary{}, wrapConversationDB("insert conversation summary", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE conversation SET updated_at = $2 WHERE id = $1`, summary.ConversationID, summary.UpdatedAt); err != nil {
		return version.ConversationSummary{}, wrapConversationDB("touch conversation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return version.ConversationSummary{}, fmt.Errorf("save conversation summary: commit: %w", err)
	}
	return saved, nil
}

// ListReconcilable returns the most recent clarification conversations with
// messages plus all their messages (ascending ID) annotated with the agent
// profile of the run that produced them. Read-only reconciler support.
func (r *ConversationRepository) ListReconcilable(ctx context.Context, limit int) ([]version.ReconcilerConversation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.version_id
		FROM conversation c
		WHERE c.kind = 'clarification'
		  AND EXISTS (SELECT 1 FROM conversation_message m WHERE m.conversation_id = c.id)
		ORDER BY c.id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list reconcilable conversations: %w", err)
	}
	defer rows.Close()
	conversations := make([]version.ReconcilerConversation, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var conversation version.ReconcilerConversation
		if err := rows.Scan(&conversation.ID, &conversation.VersionID); err != nil {
			return nil, fmt.Errorf("scan reconcilable conversation: %w", err)
		}
		conversation.Messages = []version.ReconcilerMessage{}
		conversations = append(conversations, conversation)
		ids = append(ids, conversation.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reconcilable conversations: %w", err)
	}
	if len(conversations) == 0 {
		return conversations, nil
	}

	messageRows, err := r.pool.Query(ctx, `
		SELECT m.id, m.conversation_id, m.role, COALESCE(run.profile_key, '')
		FROM conversation_message m
		LEFT JOIN agent_run run ON run.id = m.agent_run_id
		WHERE m.conversation_id = ANY($1::BIGINT[])
		ORDER BY m.id
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("list reconcilable messages: %w", err)
	}
	defer messageRows.Close()
	index := make(map[int64]int, len(conversations))
	for position := range conversations {
		index[conversations[position].ID] = position
	}
	for messageRows.Next() {
		var message version.ReconcilerMessage
		var conversationID int64
		if err := messageRows.Scan(&message.ID, &conversationID, &message.Role, &message.AgentProfileKey); err != nil {
			return nil, fmt.Errorf("scan reconcilable message: %w", err)
		}
		if position, ok := index[conversationID]; ok {
			conversations[position].Messages = append(conversations[position].Messages, message)
		}
	}
	if err := messageRows.Err(); err != nil {
		return nil, fmt.Errorf("list reconcilable messages: %w", err)
	}
	return conversations, nil
}

// ---------- scanning and codecs ----------

func scanConversation(row rowScanner) (version.Conversation, error) {
	var conversation version.Conversation
	if err := row.Scan(&conversation.ID, &conversation.VersionID, &conversation.Kind, &conversation.Title,
		&conversation.Status, &conversation.CreatedBy, &conversation.CreatedAt, &conversation.UpdatedAt,
		&conversation.MessageCount, &conversation.LastMessageAt); err != nil {
		return version.Conversation{}, err
	}
	return conversation, nil
}

func scanMessage(row rowScanner) (version.Message, error) {
	var message version.Message
	var parts, refs, tokenUsage []byte
	if err := row.Scan(&message.ID, &message.ConversationID, &message.Role, &parts, &refs, &tokenUsage,
		&message.ActorID, &message.AgentRunID, &message.ClientMessageID,
		&message.ReplyToMessageID, &message.CreatedAt); err != nil {
		return version.Message{}, err
	}
	if len(parts) > 0 {
		if err := json.Unmarshal(parts, &message.Parts); err != nil {
			return version.Message{}, fmt.Errorf("decode message parts: %w", err)
		}
	}
	if len(refs) > 0 {
		if err := json.Unmarshal(refs, &message.Refs); err != nil {
			return version.Message{}, fmt.Errorf("decode message refs: %w", err)
		}
	}
	if len(tokenUsage) > 0 {
		if err := json.Unmarshal(tokenUsage, &message.TokenUsage); err != nil {
			return version.Message{}, fmt.Errorf("decode message token usage: %w", err)
		}
	}
	if message.Parts == nil {
		message.Parts = []version.MessagePart{}
	}
	if message.Refs == nil {
		message.Refs = []version.MessageRef{}
	}
	if message.TokenUsage == nil {
		message.TokenUsage = map[string]any{}
	}
	return message, nil
}

func scanSummary(row rowScanner) (version.ConversationSummary, error) {
	var summary version.ConversationSummary
	var pinned []byte
	if err := row.Scan(&summary.ID, &summary.ConversationID, &summary.Summary, &pinned,
		&summary.TokenEstimate, &summary.Version, &summary.UpdatedAt); err != nil {
		return version.ConversationSummary{}, err
	}
	summary.PinnedFacts = decodeStringSlice(pinned)
	return summary, nil
}

func encodeMessageJSON(message version.Message) (parts, refs, tokenUsage string, err error) {
	partsValue := message.Parts
	if partsValue == nil {
		partsValue = []version.MessagePart{}
	}
	partsRaw, err := json.Marshal(partsValue)
	if err != nil {
		return "", "", "", fmt.Errorf("encode message parts: %w", err)
	}
	refsValue := message.Refs
	if refsValue == nil {
		refsValue = []version.MessageRef{}
	}
	refsRaw, err := json.Marshal(refsValue)
	if err != nil {
		return "", "", "", fmt.Errorf("encode message refs: %w", err)
	}
	usageValue := message.TokenUsage
	if usageValue == nil {
		usageValue = map[string]any{}
	}
	usageRaw, err := json.Marshal(usageValue)
	if err != nil {
		return "", "", "", fmt.Errorf("encode message token usage: %w", err)
	}
	return string(partsRaw), string(refsRaw), string(usageRaw), nil
}

// ---------- error mapping ----------

func wrapConversationDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return version.ErrConversationNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return version.ErrConversationNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

func wrapMessageDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return version.ErrMessageNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

var _ version.ConversationRepository = (*ConversationRepository)(nil)
