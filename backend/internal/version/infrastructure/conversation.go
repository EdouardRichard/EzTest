package versionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const conversationColumns = `c.id, c.version_id, c.kind, c.title, c.status, c.model_override,
	c.active_turn_id, c.active_agent, c.lease_until, c.created_by,
	c.created_at, c.updated_at,
	(SELECT count(*) FROM conversation_message m WHERE m.conversation_id = c.id),
	(SELECT max(m.created_at) FROM conversation_message m WHERE m.conversation_id = c.id)`

const conversationMessageColumns = `id, conversation_id, role, parts, refs, token_usage,
	actor_id, agent_run_id, client_message_id, reply_to_message_id, created_at,
	author_agent, kind, stream_state`

// conversationMessageSelectColumns reads a message together with the model that
// actually generated it. actual_model is empty for user messages and for
// assistant messages whose run link is absent; the DTO maps empty to null.
const conversationMessageSelectColumns = `m.id, m.conversation_id, m.role, m.parts, m.refs, m.token_usage,
	m.actor_id, m.agent_run_id, m.client_message_id, m.reply_to_message_id, m.created_at,
	m.author_agent, m.kind, m.stream_state,
	COALESCE(run.model, '')`

const conversationSummaryColumns = `id, conversation_id, summary, pinned_facts,
	token_estimate, version, COALESCE(covered_message_id, 0),
	COALESCE(compaction_id, ''), summary_meta, updated_at`

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
		INSERT INTO conversation (id, version_id, kind, title, status, model_override, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, conversation.ID, conversation.VersionID, conversation.Kind, conversation.Title, conversation.Status,
		conversation.ModelOverride, conversation.CreatedBy, conversation.CreatedAt, conversation.UpdatedAt)
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

// UpdateModelOverride sets or clears the conversation-level model override and
// returns the updated row (including its message counters).
func (r *ConversationRepository) UpdateModelOverride(ctx context.Context, versionID, conversationID int64, modelOverride *string, updatedAt time.Time) (version.Conversation, error) {
	conversation, err := scanConversation(r.pool.QueryRow(ctx, `
		UPDATE conversation c
		SET model_override = $3, updated_at = $4
		WHERE c.id = $1 AND c.version_id = $2
		RETURNING `+conversationColumns,
		conversationID, versionID, modelOverride, updatedAt))
	if err != nil {
		return version.Conversation{}, wrapConversationDB("update conversation model override", err)
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
		SELECT `+conversationMessageSelectColumns+`
		FROM conversation_message m
		LEFT JOIN agent_run run ON run.id = m.agent_run_id
		WHERE m.conversation_id = $1 AND m.client_message_id = $2
	`, conversationID, clientMessageID))
	if err != nil {
		return nil, wrapMessageDB("find message by client id", err)
	}
	return &message, nil
}

// Messages returns messages with id > after in ascending order.
func (r *ConversationRepository) Messages(ctx context.Context, conversationID int64, after int64, limit int) ([]version.Message, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+conversationMessageSelectColumns+`
		FROM conversation_message m
		LEFT JOIN agent_run run ON run.id = m.agent_run_id
		WHERE m.conversation_id = $1 AND m.id > $2
		ORDER BY m.id
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

// MessageParts returns the streaming parts of one conversation message in seq
// order. The message must belong to the version-scoped conversation: a foreign
// or missing message is ErrMessageNotFound (404 at the API edge).
func (r *ConversationRepository) MessageParts(ctx context.Context, versionID, conversationID, messageID int64) ([]version.ConversationMessagePart, error) {
	var owned bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM conversation_message m
			JOIN conversation c ON c.id = m.conversation_id
			WHERE m.id = $1 AND m.conversation_id = $2 AND c.version_id = $3
		)
	`, messageID, conversationID, versionID).Scan(&owned); err != nil {
		return nil, fmt.Errorf("check conversation message scope: %w", err)
	}
	if !owned {
		return nil, version.ErrMessageNotFound
	}
	rows, err := r.pool.Query(ctx, `
		SELECT seq, part_type, payload, created_at
		FROM conversation_message_part
		WHERE message_id = $1
		ORDER BY seq
	`, messageID)
	if err != nil {
		return nil, fmt.Errorf("list conversation message parts: %w", err)
	}
	defer rows.Close()
	parts := make([]version.ConversationMessagePart, 0)
	for rows.Next() {
		var part version.ConversationMessagePart
		var payload []byte
		if err := rows.Scan(&part.Seq, &part.Type, &payload, &part.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation message part: %w", err)
		}
		part.Payload = json.RawMessage(payload)
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list conversation message parts: %w", err)
	}
	return parts, nil
}

// FindJobCardMessage returns the assistant message carrying jobID's job_card
// part in one version-scoped conversation. It is the idempotency lookup behind
// the one-card-per-job contract: a repeated start/update rewrites this message
// instead of appending a second card.
func (r *ConversationRepository) FindJobCardMessage(ctx context.Context, versionID, conversationID, jobID int64) (*version.Message, error) {
	message, err := scanMessage(r.pool.QueryRow(ctx, `
		SELECT `+conversationMessageSelectColumns+`
		FROM conversation_message m
		JOIN conversation c ON c.id = m.conversation_id
		LEFT JOIN agent_run run ON run.id = m.agent_run_id
		WHERE c.version_id = $1 AND m.conversation_id = $2 AND m.role = $4
		  AND EXISTS (
			SELECT 1
			FROM jsonb_array_elements(m.parts) AS part
			WHERE part->>'type' = $5 AND part->>'job_id' = $3
		  )
		ORDER BY m.id DESC
		LIMIT 1
	`, versionID, conversationID, platformid.Format(jobID), version.MessageRoleAssistant, version.MessagePartJobCard))
	if err != nil {
		return nil, wrapMessageDB("find job card message", err)
	}
	return &message, nil
}

// UpdateJobCardMessage rewrites the stored snapshot part of jobID and its
// conversation_message_part payload inside messageID. The card is the one
// mutable part of the conversation: no new message or part row is appended and
// the append-only seq/created_at stay untouched.
//
// A cancelled card is terminal and sticky: a rewrite that would replace it with
// a running/open/done/failed snapshot is rejected with version.ErrJobCardCancelled
// and nothing is written. The stored status is re-read under FOR UPDATE, so a
// late runner projection cannot overwrite a cancel that committed after the
// caller's lookup. ReopenJobCardMessage is the explicit retry bypass.
func (r *ConversationRepository) UpdateJobCardMessage(ctx context.Context, versionID, conversationID, messageID, jobID int64, part version.MessagePart) error {
	return r.updateJobCardMessage(ctx, versionID, conversationID, messageID, jobID, part, false)
}

// ReopenJobCardMessage rewrites the stored job card even when it is currently
// cancelled: the explicit user-retry reopen, so the new attempt can reuse the
// same card.
func (r *ConversationRepository) ReopenJobCardMessage(ctx context.Context, versionID, conversationID, messageID, jobID int64, part version.MessagePart) error {
	return r.updateJobCardMessage(ctx, versionID, conversationID, messageID, jobID, part, true)
}

func (r *ConversationRepository) updateJobCardMessage(ctx context.Context, versionID, conversationID, messageID, jobID int64, part version.MessagePart, allowCancelled bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("update job card message: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var raw []byte
	if err := tx.QueryRow(ctx, `
		SELECT m.parts
		FROM conversation_message m
		JOIN conversation c ON c.id = m.conversation_id
		WHERE m.id = $1 AND m.conversation_id = $2 AND c.version_id = $3
		  AND m.role = $4
		FOR UPDATE OF m
	`, messageID, conversationID, versionID, version.MessageRoleAssistant).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return version.ErrMessageNotFound
		}
		return fmt.Errorf("update job card message: lock message: %w", err)
	}
	var parts []version.MessagePart
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parts); err != nil {
			return fmt.Errorf("update job card message: decode parts: %w", err)
		}
	}
	replaced := -1
	for index := range parts {
		if parts[index].Type == version.MessagePartJobCard && parts[index].JobID == jobID {
			// Re-check the sticky cancelled snapshot under the row lock. The
			// cancel's own cancelled rewrite stays allowed (idempotent), and
			// the explicit retry reopen bypasses the rule.
			if !allowCancelled && !version.JobCardRewriteAllowed(parts[index].Status, part.Status) {
				return version.ErrJobCardCancelled
			}
			replaced = index
			break
		}
	}
	if replaced < 0 {
		return version.ErrMessageNotFound
	}
	parts[replaced] = part
	encodedParts, err := json.Marshal(parts)
	if err != nil {
		return fmt.Errorf("update job card message: encode parts: %w", err)
	}
	encodedPart, err := json.Marshal(part)
	if err != nil {
		return fmt.Errorf("update job card message: encode card: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message SET parts = $2::jsonb WHERE id = $1
	`, messageID, string(encodedParts)); err != nil {
		return wrapConversationDB("update job card message snapshot", err)
	}
	// A missing streaming row is tolerated: the message snapshot is the primary
	// read path and an existing card may predate its conversation_message_part.
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message_part
		SET payload = $3::jsonb
		WHERE message_id = $1 AND part_type = $4 AND payload->>'job_id' = $2
	`, messageID, platformid.Format(jobID), string(encodedPart), version.MessagePartJobCard); err != nil {
		return fmt.Errorf("update job card message: update part row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("update job card message: commit: %w", err)
	}
	return nil
}

// PostUserMessage inserts the user message and bumps the conversation
// timestamp. A client_message_id conflict returns the existing message with
// created=false.
func (r *ConversationRepository) PostUserMessage(ctx context.Context, conversationID int64, message version.Message) (*version.Message, bool, error) {
	kind, err := normalizeMessageKindForWrite(message.Kind)
	if err != nil {
		return nil, false, err
	}
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
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (conversation_id, client_message_id) DO NOTHING
	`, message.ID, conversationID, message.Role, parts, refs, tokenUsage,
		message.ActorID, message.AgentRunID, message.ClientMessageID,
		message.ReplyToMessageID, message.CreatedAt,
		message.AuthorAgent, kind, version.NormalizeMessageStreamState(message.StreamState))
	if err != nil {
		return nil, false, wrapConversationDB("insert conversation message", err)
	}
	if tag.RowsAffected() == 0 {
		if message.ClientMessageID == nil {
			return nil, false, fmt.Errorf("post user message: insert was ignored without a client message id")
		}
		existing, err := scanMessage(tx.QueryRow(ctx, `
			SELECT `+conversationMessageSelectColumns+`
			FROM conversation_message m
			LEFT JOIN agent_run run ON run.id = m.agent_run_id
			WHERE m.conversation_id = $1 AND m.client_message_id = $2
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

// AppendAssistantMessage inserts one server-generated assistant message together
// with its append-only conversation_message_part rows (0.2.1 streaming parts) in
// one transaction, then bumps the conversation timestamp. It is the
// unconditional artifact/card writer: Host replies go through
// AppendAssistantMessageAsWriter and background text through the fenced
// AppendAssistantMessageAsBackground. The (message_id, seq) unique index makes a
// duplicate/restarted seq a transaction failure instead of a silent overwrite:
// append-only parts are never rewritten.
func (r *ConversationRepository) AppendAssistantMessage(ctx context.Context, conversationID int64, message version.Message) error {
	kind, err := normalizeMessageKindForWrite(message.Kind)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("append assistant message: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.insertAssistantMessage(ctx, tx, conversationID, message, kind); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("append assistant message: commit: %w", err)
	}
	return nil
}

// AppendAssistantMessageAsBackground inserts one background/server assistant
// message iff no live Host writer lease exists. The conversation row is locked
// FOR UPDATE, so the lease re-check and the insert are one serialized decision:
// a Host that acquires between the caller's pre-read and this write cannot be
// handed background assistant text. A live lease is ErrNotConversationWriter
// (the background path is never a writer); an idle conversation or an expired
// non-NULL lease stays writable and the stored lease fields are left untouched.
func (r *ConversationRepository) AppendAssistantMessageAsBackground(ctx context.Context, conversationID int64, message version.Message, now time.Time) error {
	kind, err := normalizeMessageKindForWrite(message.Kind)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("append background assistant message: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var activeTurnID *int64
	var leaseUntil *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT active_turn_id, lease_until
		FROM conversation WHERE id = $1
		FOR UPDATE
	`, conversationID).Scan(&activeTurnID, &leaseUntil); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return version.ErrConversationNotFound
		}
		return fmt.Errorf("append background assistant message: lock conversation: %w", err)
	}
	if activeTurnID != nil && leaseUntil != nil && !leaseUntil.Before(now) {
		return version.ErrNotConversationWriter
	}
	if err := r.insertAssistantMessage(ctx, tx, conversationID, message, kind); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("append background assistant message: commit: %w", err)
	}
	return nil
}

// insertAssistantMessage writes one assistant message head, its append-only
// conversation_message_part rows and the conversation timestamp inside tx. Every
// assistant-write path shares it so the column/kind contract cannot drift
// between the writer, background and artifact paths.
func (r *ConversationRepository) insertAssistantMessage(ctx context.Context, tx pgx.Tx, conversationID int64, message version.Message, kind string) error {
	parts, refs, tokenUsage, err := encodeMessageJSON(message)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversation_message (`+conversationMessageColumns+`)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14)
	`, message.ID, conversationID, message.Role, parts, refs, tokenUsage,
		message.ActorID, message.AgentRunID, message.ClientMessageID,
		message.ReplyToMessageID, message.CreatedAt,
		message.AuthorAgent, kind, version.NormalizeMessageStreamState(message.StreamState)); err != nil {
		return wrapConversationDB("insert assistant message", err)
	}
	for _, part := range message.StreamParts {
		payload := part.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		createdAt := part.CreatedAt
		if createdAt.IsZero() {
			createdAt = message.CreatedAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO conversation_message_part (id, message_id, seq, part_type, payload, created_at)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		`, r.ids.Next(), message.ID, part.Seq, part.Type, string(payload), createdAt); err != nil {
			return wrapConversationDB("insert conversation message part", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE conversation SET updated_at = $2 WHERE id = $1`, conversationID, message.CreatedAt); err != nil {
		return wrapConversationDB("touch conversation", err)
	}
	return nil
}

// normalizeMessageKindForWrite normalizes the legacy empty kind and validates
// the DB-3 conversation_message kind enum before any INSERT, so an invalid kind
// is the typed 400 platform.Invalid instead of a database CHECK violation
// surfacing as a 500.
func normalizeMessageKindForWrite(kind string) (string, error) {
	kind = version.NormalizeMessageKind(kind)
	if err := version.ValidateConversationMessageKind(kind); err != nil {
		return "", err
	}
	return kind, nil
}

// ---------- single-writer writer lease (B §5.1, R-B2-1..R-B2-5) ----------

// AcquireWriter claims the conversation's single-writer lease with one atomic
// compare-and-set: idle conversations, expired leases and the same owner's
// retry are claimable; a different live turn is ErrConversationBusy. The
// partial unique index uk_conversation_active_turn keeps the row shape honest,
// while this UPDATE is the cross-writer serialization point.
func (r *ConversationRepository) AcquireWriter(ctx context.Context, conversationID, turnID int64, agent string, now, leaseUntil time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation
		SET active_turn_id = $2, active_agent = $3, lease_until = $4
		WHERE id = $1
		  AND (active_turn_id IS NULL OR lease_until IS NULL OR lease_until < $5
		       OR (active_turn_id = $2 AND active_agent = $3))
	`, conversationID, turnID, agent, leaseUntil, now)
	if err != nil {
		return fmt.Errorf("acquire conversation writer: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	exists, err := r.conversationExists(ctx, conversationID)
	if err != nil {
		return err
	}
	if !exists {
		return version.ErrConversationNotFound
	}
	return version.ErrConversationBusy
}

// RenewWriterLease extends the lease of the current owner only. A zero-row
// update means the lease expired, was released, or belongs to another
// turn/agent: ErrNotConversationWriter.
func (r *ConversationRepository) RenewWriterLease(ctx context.Context, conversationID, turnID int64, agent string, now, leaseUntil time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation
		SET lease_until = $5
		WHERE id = $1 AND active_turn_id = $2 AND active_agent = $3
		  AND lease_until IS NOT NULL AND lease_until >= $4
	`, conversationID, turnID, agent, now, leaseUntil)
	if err != nil {
		return fmt.Errorf("renew conversation writer lease: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return r.notWriterOrNotFound(ctx, conversationID)
}

// ReleaseWriter clears the current owner's lease. A foreign turn never clears
// another turn's lease; an already-released conversation is an idempotent no-op.
func (r *ConversationRepository) ReleaseWriter(ctx context.Context, conversationID, turnID int64, agent string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation
		SET active_turn_id = NULL, active_agent = NULL, lease_until = NULL
		WHERE id = $1 AND active_turn_id = $2 AND active_agent = $3
	`, conversationID, turnID, agent)
	if err != nil {
		return fmt.Errorf("release conversation writer: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var activeTurnID *int64
	if err := r.pool.QueryRow(ctx, `SELECT active_turn_id FROM conversation WHERE id = $1`, conversationID).Scan(&activeTurnID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return version.ErrConversationNotFound
		}
		return fmt.Errorf("release conversation writer: %w", err)
	}
	if activeTurnID == nil {
		return nil
	}
	return version.ErrNotConversationWriter
}

// AppendAssistantMessageAsWriter inserts one assistant message iff (turnID,
// agent) owns the conversation's live lease. The conversation row is locked FOR
// UPDATE, so the lease check, the crash-recovery takeover and the insert are
// one serialized compare-and-set: concurrent writers cannot both append. An
// idle conversation is NOT silently claimed here — the caller must Acquire
// first; only an expired non-NULL lease may be taken over implicitly (crash
// recovery). A repeated client_message_id (the Host turn idempotency key)
// returns the stored message with created=false.
func (r *ConversationRepository) AppendAssistantMessageAsWriter(ctx context.Context, conversationID int64, message version.Message, turnID int64, agent string, now, leaseUntil time.Time) (*version.Message, bool, error) {
	return r.appendAssistantMessageAsWriter(ctx, conversationID, message, turnID, agent, now, leaseUntil)
}

// BeginAssistantStream inserts the placeholder head of one incremental Host
// reply under the same fenced writer lease as AppendAssistantMessageAsWriter
// (the head carries stream_state=streaming and empty parts; the streaming part
// rows are written later by UpdateStreamPart). Sharing the lease/idempotency
// transaction keeps the single-writer compare-and-set in one place.
func (r *ConversationRepository) BeginAssistantStream(ctx context.Context, conversationID int64, message version.Message, turnID int64, agent string, now, leaseUntil time.Time) (*version.Message, bool, error) {
	return r.appendAssistantMessageAsWriter(ctx, conversationID, message, turnID, agent, now, leaseUntil)
}

func (r *ConversationRepository) appendAssistantMessageAsWriter(ctx context.Context, conversationID int64, message version.Message, turnID int64, agent string, now, leaseUntil time.Time) (*version.Message, bool, error) {
	kind, err := normalizeMessageKindForWrite(message.Kind)
	if err != nil {
		return nil, false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("append assistant message as writer: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var activeTurnID *int64
	var activeAgent *string
	var activeLease *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT active_turn_id, active_agent, lease_until
		FROM conversation WHERE id = $1
		FOR UPDATE
	`, conversationID).Scan(&activeTurnID, &activeAgent, &activeLease); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, version.ErrConversationNotFound
		}
		return nil, false, fmt.Errorf("append assistant message as writer: lock conversation: %w", err)
	}
	leaseActive := activeTurnID != nil && activeAgent != nil && activeLease != nil && !activeLease.Before(now)
	switch {
	case leaseActive:
		if *activeTurnID != turnID || *activeAgent != agent {
			return nil, false, version.ErrConversationBusy
		}
	case activeTurnID != nil && activeLease != nil && activeLease.Before(now):
		// The previous turn stopped heartbeating: take its expired lease over.
		if _, err := tx.Exec(ctx, `
			UPDATE conversation
			SET active_turn_id = $2, active_agent = $3, lease_until = $4
			WHERE id = $1
		`, conversationID, turnID, agent, leaseUntil); err != nil {
			return nil, false, fmt.Errorf("append assistant message as writer: take over lease: %w", err)
		}
	default:
		// Idle (NULL active_turn) or a lease-less active turn: appending must not
		// silently make this turn the writer. The caller has to Acquire the
		// lease explicitly; only an expired non-NULL lease is recoverable here.
		return nil, false, version.ErrNotConversationWriter
	}

	if message.ClientMessageID != nil {
		existing, err := scanMessage(tx.QueryRow(ctx, `
			SELECT `+conversationMessageSelectColumns+`
			FROM conversation_message m
			LEFT JOIN agent_run run ON run.id = m.agent_run_id
			WHERE m.conversation_id = $1 AND m.client_message_id = $2 AND m.role = $3
		`, conversationID, *message.ClientMessageID, version.MessageRoleAssistant))
		switch {
		case err == nil:
			if err := tx.Commit(ctx); err != nil {
				return nil, false, fmt.Errorf("append assistant message as writer: commit: %w", err)
			}
			return &existing, false, nil
		case errors.Is(err, pgx.ErrNoRows):
			// No prior turn write: continue with the insert.
		default:
			return nil, false, wrapMessageDB("find idempotent assistant message", err)
		}
	}

	if err := r.insertAssistantMessage(ctx, tx, conversationID, message, kind); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("append assistant message as writer: commit: %w", err)
	}
	return nil, true, nil
}

// ---------- incremental conversation stream (v0.2.4-D, R-D2/SC-D2) ----------

// lockStreamHead locks the conversation and the addressed assistant message head
// FOR UPDATE and re-checks the single-writer lease against the message's owning
// Host turn. The lease holder is derived from stored server facts: the head's
// reserved turn idempotency key (client_message_id == HostTurnClientMessageID of
// the lease active_turn_id) and author_agent == active_agent. Begin wrote those
// under the lease, so only that turn may update/finish the stream — never a
// caller claim. A foreign live turn is ErrConversationBusy; no live lease
// (idle/released/expired-and-taken-over) is ErrNotConversationWriter. It returns
// the head parts JSON and the current stream_state.
func (r *ConversationRepository) lockStreamHead(ctx context.Context, tx pgx.Tx, conversationID, messageID int64, now time.Time) ([]byte, string, error) {
	var activeTurnID *int64
	var activeAgent *string
	var activeLease *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT active_turn_id, active_agent, lease_until
		FROM conversation WHERE id = $1
		FOR UPDATE
	`, conversationID).Scan(&activeTurnID, &activeAgent, &activeLease); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", version.ErrConversationNotFound
		}
		return nil, "", fmt.Errorf("lock stream conversation: %w", err)
	}
	var raw []byte
	var streamState, authorAgent string
	var clientMessageID *string
	if err := tx.QueryRow(ctx, `
		SELECT m.parts, m.stream_state, m.author_agent, m.client_message_id
		FROM conversation_message m
		WHERE m.id = $1 AND m.conversation_id = $2 AND m.role = $3
		FOR UPDATE
	`, messageID, conversationID, version.MessageRoleAssistant).Scan(&raw, &streamState, &authorAgent, &clientMessageID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", version.ErrMessageNotFound
		}
		return nil, "", fmt.Errorf("lock stream message: %w", err)
	}
	live := activeTurnID != nil && activeAgent != nil && activeLease != nil && !activeLease.Before(now)
	if !live {
		return nil, "", version.ErrNotConversationWriter
	}
	turnKey := version.HostTurnClientMessageID(*activeTurnID)
	if clientMessageID == nil || *clientMessageID != turnKey || authorAgent != *activeAgent {
		return nil, "", version.ErrConversationBusy
	}
	return raw, streamState, nil
}

// UpdateStreamPart rewrites the full snapshot of one streaming part (seq) in
// place: the conversation_message_part payload (upsert by the unique
// (message_id, seq)) and the matching part of the conversation_message.parts head
// snapshot, in one row-locked transaction. The first update for a seq inserts its
// row; later updates never grow rows. It returns the previous part payload (nil
// when the seq had no row) so the application can derive the SSE increment.
func (r *ConversationRepository) UpdateStreamPart(ctx context.Context, conversationID, messageID int64, seq int, part version.MessagePart, now time.Time) (json.RawMessage, error) {
	if seq <= 0 {
		return nil, platform.Invalid("seq", "must be a positive integer")
	}
	if !version.IsStreamingMessagePartType(part.Type) {
		return nil, platform.Invalid("part_type", "must be a streaming conversation_message_part type")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update stream part: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	raw, streamState, err := r.lockStreamHead(ctx, tx, conversationID, messageID, now)
	if err != nil {
		return nil, err
	}
	if streamState != version.MessageStreamStateStreaming {
		return nil, version.ErrMessageStreamClosed
	}
	var parts []version.MessagePart
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil, fmt.Errorf("update stream part: decode head parts: %w", err)
		}
	}
	streamingCount := 0
	for _, existing := range parts {
		if version.IsStreamingMessagePartType(existing.Type) {
			streamingCount++
		}
	}
	if seq > streamingCount+1 {
		return nil, platform.Invalid("seq", "must address an existing part or the next contiguous seq")
	}

	var previous []byte
	if err := tx.QueryRow(ctx, `
		SELECT payload FROM conversation_message_part WHERE message_id = $1 AND seq = $2
	`, messageID, seq).Scan(&previous); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("update stream part: read previous payload: %w", err)
	}
	encodedPart, err := json.Marshal(part)
	if err != nil {
		return nil, fmt.Errorf("update stream part: encode part: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversation_message_part (id, message_id, seq, part_type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (message_id, seq) DO UPDATE
		SET payload = EXCLUDED.payload, part_type = EXCLUDED.part_type
	`, r.ids.Next(), messageID, seq, part.Type, string(encodedPart), now); err != nil {
		return nil, wrapConversationDB("upsert stream part", err)
	}
	parts = replaceStreamPartBySeq(parts, seq, part)
	encodedParts, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("update stream part: encode head parts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message SET parts = $2::jsonb WHERE id = $1
	`, messageID, string(encodedParts)); err != nil {
		return nil, wrapConversationDB("update stream head snapshot", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("update stream part: commit: %w", err)
	}
	if len(previous) == 0 {
		return nil, nil
	}
	return json.RawMessage(previous), nil
}

// FinishAssistantStream sets the terminal stream_state of one streaming head and
// reconciles its conversation_message_part rows to finalParts (upsert each final
// streaming seq, delete any extra seq the stream accumulated but the final
// snapshot dropped). A head already terminal is an idempotent no-op (false). It
// returns the stored head and a bool reporting whether the head transitioned
// streaming->terminal so the application publishes the terminal frame exactly
// once, with the head's original created_at/refs/kind.
func (r *ConversationRepository) FinishAssistantStream(ctx context.Context, conversationID, messageID int64, finalParts []version.MessagePart, streamState string, now time.Time) (version.Message, bool, error) {
	if err := version.ValidateMessageStreamState(streamState); err != nil {
		return version.Message{}, false, err
	}
	if streamState == version.MessageStreamStateStreaming {
		return version.Message{}, false, platform.Invalid("stream_state", "finish target must be complete or interrupted")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.Message{}, false, fmt.Errorf("finish assistant stream: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, current, err := r.lockStreamHead(ctx, tx, conversationID, messageID, now)
	if err != nil {
		return version.Message{}, false, err
	}
	if current != version.MessageStreamStateStreaming {
		head, err := r.scanStreamHead(ctx, tx, messageID)
		if err != nil {
			return version.Message{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return version.Message{}, false, fmt.Errorf("finish assistant stream: commit no-op: %w", err)
		}
		return head, false, nil
	}
	headParts := finalParts
	if headParts == nil {
		headParts = []version.MessagePart{}
	}
	encodedParts, err := json.Marshal(headParts)
	if err != nil {
		return version.Message{}, false, fmt.Errorf("finish assistant stream: encode head parts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message SET parts = $2::jsonb, stream_state = $3 WHERE id = $1
	`, messageID, string(encodedParts), streamState); err != nil {
		return version.Message{}, false, wrapConversationDB("finish stream head", err)
	}
	seq := 0
	for _, part := range finalParts {
		if !version.IsStreamingMessagePartType(part.Type) {
			continue
		}
		seq++
		encodedPart, err := json.Marshal(part)
		if err != nil {
			return version.Message{}, false, fmt.Errorf("finish assistant stream: encode part: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO conversation_message_part (id, message_id, seq, part_type, payload, created_at)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)
			ON CONFLICT (message_id, seq) DO UPDATE
			SET payload = EXCLUDED.payload, part_type = EXCLUDED.part_type
		`, r.ids.Next(), messageID, seq, part.Type, string(encodedPart), now); err != nil {
			return version.Message{}, false, wrapConversationDB("finish stream part", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM conversation_message_part WHERE message_id = $1 AND seq > $2
	`, messageID, seq); err != nil {
		return version.Message{}, false, fmt.Errorf("finish assistant stream: trim extra parts: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE conversation SET updated_at = $2 WHERE id = $1`, conversationID, now); err != nil {
		return version.Message{}, false, wrapConversationDB("touch conversation", err)
	}
	head, err := r.scanStreamHead(ctx, tx, messageID)
	if err != nil {
		return version.Message{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return version.Message{}, false, fmt.Errorf("finish assistant stream: commit: %w", err)
	}
	return head, true, nil
}

// InterruptOrphanStreams seals the streaming heads left behind by Host turns
// that no longer own the conversation lease (v0.2.4-D T6, SC-D4 crash recovery).
// The caller must hold the live lease: the single-writer invariant then makes
// every OTHER streaming head an orphan by construction, so no run-status guess
// is needed. Each orphan keeps its accumulated parts and gains errorPart as the
// next streaming seq; the head becomes interrupted, never complete. The whole
// sweep is one row-locked transaction and is idempotent (a second call finds no
// streaming head).
//
// H-E2: the sweep is PER-HEAD tolerant. One head the validator refuses (or whose
// stored snapshot cannot be decoded) used to abort the transaction, leaving EVERY
// head of the conversation streaming. Such a head is now skipped, left exactly as
// stored, and reported through *version.OrphanSealError alongside the heads that
// WERE sealed — a partial recovery is never silent (constitution 七.6).
func (r *ConversationRepository) InterruptOrphanStreams(ctx context.Context, conversationID, turnID int64, agent string, errorPart version.MessagePart, now time.Time) ([]version.Message, error) {
	if errorPart.Type != version.MessagePartError {
		return nil, platform.Invalid("error_part", "must be an error part")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("interrupt orphan streams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var activeTurnID *int64
	var activeAgent *string
	var activeLease *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT active_turn_id, active_agent, lease_until
		FROM conversation WHERE id = $1
		FOR UPDATE
	`, conversationID).Scan(&activeTurnID, &activeAgent, &activeLease); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, version.ErrConversationNotFound
		}
		return nil, fmt.Errorf("interrupt orphan streams: lock conversation: %w", err)
	}
	live := activeTurnID != nil && activeAgent != nil && activeLease != nil && !activeLease.Before(now)
	if !live {
		return nil, version.ErrNotConversationWriter
	}
	if *activeTurnID != turnID || *activeAgent != agent {
		return nil, version.ErrConversationBusy
	}

	turnKey := version.HostTurnClientMessageID(turnID)
	heads, err := collectOrphanHeads(ctx, tx, `
		SELECT id, parts FROM conversation_message
		WHERE conversation_id = $1 AND role = $4 AND stream_state = $2
		  AND client_message_id IS DISTINCT FROM $3
		ORDER BY id
		FOR UPDATE
	`, conversationID, version.MessageStreamStateStreaming, turnKey, version.MessageRoleAssistant)
	if err != nil {
		return nil, err
	}
	return r.commitOrphanSeal(ctx, tx, conversationID, heads, errorPart, now)
}

// InterruptRunOrphanStreams seals the streaming heads of ONE agent run in this
// conversation (H-E2). It is the recovery entry for a run the agent crash sweep
// has proven orphaned — its work item is settled or its lease was taken over —
// in a conversation that may never get another turn, so no lease holder exists to
// run InterruptOrphanStreams. The fence is therefore the run binding rather than
// the lease: the agent context owns agent_run's terminal truth and is the only
// caller. Heads of other runs are never selected.
//
// Content contract and per-head tolerance are shared with InterruptOrphanStreams
// through sealOrphanHeads, so both recovery paths write the seal exactly one way.
func (r *ConversationRepository) InterruptRunOrphanStreams(ctx context.Context, conversationID, agentRunID int64, errorPart version.MessagePart, now time.Time) ([]version.Message, error) {
	if errorPart.Type != version.MessagePartError {
		return nil, platform.Invalid("error_part", "must be an error part")
	}
	if conversationID <= 0 || agentRunID <= 0 {
		return nil, platform.Invalid("id", "must be a positive decimal ID")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("interrupt run orphan streams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The conversation row is locked so a concurrent lease acquisition and this
	// recovery serialize, but its lease is deliberately NOT required: the run is
	// already known to be dead and nobody may hold the lease at all.
	var exists int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM conversation WHERE id = $1 FOR UPDATE
	`, conversationID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, version.ErrConversationNotFound
		}
		return nil, fmt.Errorf("interrupt run orphan streams: lock conversation: %w", err)
	}
	heads, err := collectOrphanHeads(ctx, tx, `
		SELECT id, parts FROM conversation_message
		WHERE conversation_id = $1 AND role = $4 AND stream_state = $2
		  AND agent_run_id = $3
		ORDER BY id
		FOR UPDATE
	`, conversationID, version.MessageStreamStateStreaming, agentRunID, version.MessageRoleAssistant)
	if err != nil {
		return nil, err
	}
	return r.commitOrphanSeal(ctx, tx, conversationID, heads, errorPart, now)
}

// orphanHead is one stored streaming head selected by a recovery sweep. The
// snapshot stays raw until its own seal step: decoding it must not be able to
// abort the sweep for the other heads.
type orphanHead struct {
	id  int64
	raw []byte
}

// collectOrphanHeads runs one recovery selection and closes the rows before any
// write (the seal statements need the same connection).
func collectOrphanHeads(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]orphanHead, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapConversationDB("select orphan stream heads", err)
	}
	heads := make([]orphanHead, 0)
	for rows.Next() {
		var head orphanHead
		if err := rows.Scan(&head.id, &head.raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("interrupt orphan streams: scan head: %w", err)
		}
		heads = append(heads, head)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("interrupt orphan streams: list heads: %w", err)
	}
	rows.Close()
	return heads, nil
}

// commitOrphanSeal seals the selected heads and commits the recovery transaction.
// It is the ONE commit path of both recovery sweeps.
func (r *ConversationRepository) commitOrphanSeal(ctx context.Context, tx pgx.Tx, conversationID int64, heads []orphanHead, errorPart version.MessagePart, now time.Time) ([]version.Message, error) {
	sealed, failures, err := r.sealOrphanHeads(ctx, tx, heads, errorPart, now)
	if err != nil {
		return nil, err
	}
	if len(sealed) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE conversation SET updated_at = $2 WHERE id = $1`, conversationID, now); err != nil {
			return nil, wrapConversationDB("touch conversation", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("interrupt orphan streams: commit: %w", err)
	}
	if len(failures) > 0 {
		return sealed, &version.OrphanSealError{Failures: failures}
	}
	return sealed, nil
}

// sealOrphanHeads seals each selected head inside tx, PER HEAD. A head whose
// stored snapshot cannot be decoded, or whose parts plus errorPart the domain
// validator refuses, is left untouched and reported: the sweep never fabricates a
// terminal seal, never drops stored content, and never lets one bad head block
// the others (H-E2). A transport/DB failure is still fatal for the transaction.
func (r *ConversationRepository) sealOrphanHeads(ctx context.Context, tx pgx.Tx, heads []orphanHead, errorPart version.MessagePart, now time.Time) ([]version.Message, []version.OrphanSealFailure, error) {
	sealed := make([]version.Message, 0, len(heads))
	failures := make([]version.OrphanSealFailure, 0)
	for _, candidate := range heads {
		var stored []version.MessagePart
		if len(candidate.raw) > 0 {
			if err := json.Unmarshal(candidate.raw, &stored); err != nil {
				failures = append(failures, version.OrphanSealFailure{
					MessageID: candidate.id,
					Err:       fmt.Errorf("decode head parts: %w", err),
				})
				continue
			}
		}
		parts := append(append([]version.MessagePart(nil), stored...), errorPart)
		if err := version.ValidateAssistantMessageParts(parts); err != nil {
			failures = append(failures, version.OrphanSealFailure{MessageID: candidate.id, Err: err})
			continue
		}
		head, err := r.sealOneOrphanHead(ctx, tx, candidate.id, stored, parts, errorPart, now)
		if err != nil {
			return nil, nil, err
		}
		sealed = append(sealed, head)
	}
	return sealed, failures, nil
}

// sealOneOrphanHead writes one interrupted head: the full snapshot, the terminal
// stream_state and the error part at the next contiguous streaming seq.
func (r *ConversationRepository) sealOneOrphanHead(ctx context.Context, tx pgx.Tx, messageID int64, stored, parts []version.MessagePart, errorPart version.MessagePart, now time.Time) (version.Message, error) {
	encodedParts, err := json.Marshal(parts)
	if err != nil {
		return version.Message{}, fmt.Errorf("interrupt orphan streams: encode head parts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message SET parts = $2::jsonb, stream_state = $3 WHERE id = $1
	`, messageID, string(encodedParts), version.MessageStreamStateInterrupted); err != nil {
		return version.Message{}, wrapConversationDB("interrupt orphan head", err)
	}
	seq := 0
	for _, part := range stored {
		if version.IsStreamingMessagePartType(part.Type) {
			seq++
		}
	}
	encodedPart, err := json.Marshal(errorPart)
	if err != nil {
		return version.Message{}, fmt.Errorf("interrupt orphan streams: encode error part: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversation_message_part (id, message_id, seq, part_type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (message_id, seq) DO UPDATE
		SET payload = EXCLUDED.payload, part_type = EXCLUDED.part_type
	`, r.ids.Next(), messageID, seq+1, errorPart.Type, string(encodedPart), now); err != nil {
		return version.Message{}, wrapConversationDB("interrupt orphan error part", err)
	}
	return r.scanStreamHead(ctx, tx, messageID)
}

// scanStreamHead reads one message head inside tx for the terminal full-frame
// republish (created_at/refs/kind/author preserved from the stored row).
func (r *ConversationRepository) scanStreamHead(ctx context.Context, tx pgx.Tx, messageID int64) (version.Message, error) {
	head, err := scanMessage(tx.QueryRow(ctx, `
		SELECT `+conversationMessageSelectColumns+`
		FROM conversation_message m
		LEFT JOIN agent_run run ON run.id = m.agent_run_id
		WHERE m.id = $1
	`, messageID))
	if err != nil {
		return version.Message{}, wrapMessageDB("read stream head", err)
	}
	return head, nil
}

// replaceStreamPartBySeq returns a copy of parts with the seq-th streaming-type
// part (1-based, counting only IsStreamingMessagePartType parts) replaced by
// part; a seq one past the current streaming count appends. Snapshot-only parts
// (image/rollback_proposal/question) keep their position and are never sequenced.
func replaceStreamPartBySeq(parts []version.MessagePart, seq int, part version.MessagePart) []version.MessagePart {
	updated := append([]version.MessagePart(nil), parts...)
	streaming := 0
	for index := range updated {
		if !version.IsStreamingMessagePartType(updated[index].Type) {
			continue
		}
		streaming++
		if streaming == seq {
			updated[index] = part
			return updated
		}
	}
	return append(updated, part)
}

// conversationExists reports whether the conversation row exists.
func (r *ConversationRepository) conversationExists(ctx context.Context, conversationID int64) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conversation WHERE id = $1)`, conversationID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check conversation exists: %w", err)
	}
	return exists, nil
}

// notWriterOrNotFound classifies a zero-row lease write: a missing conversation
// is ErrConversationNotFound, an existing one is ErrNotConversationWriter.
func (r *ConversationRepository) notWriterOrNotFound(ctx context.Context, conversationID int64) error {
	exists, err := r.conversationExists(ctx, conversationID)
	if err != nil {
		return err
	}
	if !exists {
		return version.ErrConversationNotFound
	}
	return version.ErrNotConversationWriter
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
// stored row. R-A5-12: a per-conversation advisory lock serializes the watermark
// check and the insert, so a covered_message_id can never move backwards and
// two concurrent compactions cannot fork the revision sequence.
func (r *ConversationRepository) SaveSummary(ctx context.Context, summary version.ConversationSummary) (version.ConversationSummary, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.ConversationSummary{}, fmt.Errorf("save conversation summary: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('eztest:conversation:' || ($1::bigint)::text))`,
		summary.ConversationID); err != nil {
		return version.ConversationSummary{}, fmt.Errorf("save conversation summary: lock: %w", err)
	}
	var previousCovered int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(covered_message_id), 0)
		FROM conversation_summary
		WHERE conversation_id = $1
	`, summary.ConversationID).Scan(&previousCovered); err != nil {
		return version.ConversationSummary{}, wrapConversationDB("read summary watermark", err)
	}
	if summary.CoveredMessageID > 0 && summary.CoveredMessageID < previousCovered {
		return version.ConversationSummary{}, fmt.Errorf("%w: previous=%d candidate=%d",
			version.ErrSummaryWatermarkStale, previousCovered, summary.CoveredMessageID)
	}
	summaryMeta, err := encodeSummaryMeta(summary.SummaryMeta)
	if err != nil {
		return version.ConversationSummary{}, err
	}
	pinnedFacts, err := encodeStringSlice(summary.PinnedFacts)
	if err != nil {
		return version.ConversationSummary{}, err
	}

	saved := summary
	saved.ID = r.ids.Next()
	if err := tx.QueryRow(ctx, `
		INSERT INTO conversation_summary (
			id, conversation_id, summary, pinned_facts, token_estimate, version,
			covered_message_id, compaction_id, summary_meta, updated_at
		)
		VALUES ($1, $2, $3, $4::jsonb, $5,
			COALESCE((SELECT max(version) FROM conversation_summary WHERE conversation_id = $2), 0) + 1,
			NULLIF($6, 0), NULLIF($7, ''), $8::jsonb, $9)
		RETURNING version, updated_at
	`, saved.ID, summary.ConversationID, summary.Summary, pinnedFacts,
		summary.TokenEstimate, summary.CoveredMessageID, summary.CompactionID, summaryMeta,
		summary.UpdatedAt).Scan(&saved.Version, &saved.UpdatedAt); err != nil {
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

// encodeSummaryMeta renders the summary_meta object. A nil map stores {}.
func encodeSummaryMeta(meta map[string]any) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("encode summary meta: %w", err)
	}
	return string(encoded), nil
}

// ListReconcilable returns the most recent conversations (including
// zero-message conversations, so the opening fallback can fire) with all their
// messages (ascending ID) annotated with the agent profile of the run that
// produced them. The kind tells the reconciler whether to apply clarification
// rules or the design rules. Read-only reconciler support.
func (r *ConversationRepository) ListReconcilable(ctx context.Context, limit int) ([]version.ReconcilerConversation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.version_id, c.kind
		FROM conversation c
		WHERE c.kind IN ($2, $3)
		ORDER BY c.id DESC
		LIMIT $1
	`, limit, version.ConversationClarification, version.ConversationDesign)
	if err != nil {
		return nil, fmt.Errorf("list reconcilable conversations: %w", err)
	}
	defer rows.Close()
	conversations := make([]version.ReconcilerConversation, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var conversation version.ReconcilerConversation
		if err := rows.Scan(&conversation.ID, &conversation.VersionID, &conversation.Kind); err != nil {
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
		&conversation.Status, &conversation.ModelOverride,
		&conversation.ActiveTurnID, &conversation.ActiveAgent, &conversation.LeaseUntil,
		&conversation.CreatedBy, &conversation.CreatedAt, &conversation.UpdatedAt,
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
		&message.ReplyToMessageID, &message.CreatedAt,
		&message.AuthorAgent, &message.Kind, &message.StreamState, &message.ActualModel); err != nil {
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
	var meta []byte
	if err := row.Scan(&summary.ID, &summary.ConversationID, &summary.Summary, &pinned,
		&summary.TokenEstimate, &summary.Version, &summary.CoveredMessageID, &summary.CompactionID,
		&meta, &summary.UpdatedAt); err != nil {
		return version.ConversationSummary{}, err
	}
	pinnedFacts, err := decodeStringSlice(pinned)
	if err != nil {
		return version.ConversationSummary{}, err
	}
	summary.PinnedFacts = pinnedFacts
	if len(meta) > 0 {
		if err := json.Unmarshal(meta, &summary.SummaryMeta); err != nil {
			return version.ConversationSummary{}, fmt.Errorf("decode summary meta: %w", err)
		}
	}
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
