package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// messageColumns is the column list shared by every message query and the
// scan order scanMessage expects.
const messageColumns = `id, session_id, run_id, role, content, reasoning, tool_calls, tool_call_id, tool_name, usage, created_at`

// MessageRepository implements domain.MessageRepository on a pgx pool.
type MessageRepository struct {
	pool *pgxpool.Pool
}

// NewMessageRepository returns a message repository backed by pool.
func NewMessageRepository(pool *pgxpool.Pool) *MessageRepository {
	return &MessageRepository{pool: pool}
}

// scanMessage scans one row in messageColumns order and decodes the JSONB
// columns.
func scanMessage(row rowScanner) (agent.Message, error) {
	var (
		message    agent.Message
		content    []byte
		toolCalls  []byte
		toolCallID *string
		toolName   *string
		usage      []byte
	)
	if err := row.Scan(
		&message.ID,
		&message.SessionID,
		&message.RunID,
		&message.Role,
		&content,
		&message.Reasoning,
		&toolCalls,
		&toolCallID,
		&toolName,
		&usage,
		&message.CreatedAt,
	); err != nil {
		return agent.Message{}, err
	}
	message.Parts = []agent.Part{}
	if len(content) > 0 {
		if err := json.Unmarshal(content, &message.Parts); err != nil {
			return agent.Message{}, fmt.Errorf("decode message content: %w", err)
		}
		if message.Parts == nil {
			message.Parts = []agent.Part{}
		}
	}
	if len(toolCalls) > 0 {
		if err := json.Unmarshal(toolCalls, &message.ToolCalls); err != nil {
			return agent.Message{}, fmt.Errorf("decode message tool calls: %w", err)
		}
	}
	if len(usage) > 0 {
		var decoded agent.Usage
		if err := json.Unmarshal(usage, &decoded); err != nil {
			return agent.Message{}, fmt.Errorf("decode message usage: %w", err)
		}
		message.Usage = &decoded
	}
	if toolCallID != nil {
		message.ToolCallID = *toolCallID
	}
	if toolName != nil {
		message.ToolName = *toolName
	}
	return message, nil
}

// ListBySession returns the session's messages ordered by ID ascending, which
// is the order the history must be replayed in.
func (r *MessageRepository) ListBySession(ctx context.Context, sessionID int64) ([]agent.Message, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+messageColumns+` FROM agent_messages WHERE session_id = $1 ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list agent messages: %w", err)
	}
	defer rows.Close()

	messages := make([]agent.Message, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan agent message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list agent messages: %w", err)
	}
	return messages, nil
}

// Get returns one message by ID.
func (r *MessageRepository) Get(ctx context.Context, id int64) (agent.Message, error) {
	message, err := scanMessage(r.pool.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM agent_messages WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Message{}, agent.ErrNotFound
		}
		return agent.Message{}, fmt.Errorf("postgres: get agent message: %w", err)
	}
	return message, nil
}

// Append inserts a message and returns the stored row.
func (r *MessageRepository) Append(ctx context.Context, message agent.Message) (agent.Message, error) {
	content, err := encodeParts(message.Parts)
	if err != nil {
		return agent.Message{}, fmt.Errorf("postgres: encode message content: %w", err)
	}
	toolCalls, err := encodeToolCalls(message.ToolCalls)
	if err != nil {
		return agent.Message{}, fmt.Errorf("postgres: encode message tool calls: %w", err)
	}
	usage, err := encodeUsage(message.Usage)
	if err != nil {
		return agent.Message{}, fmt.Errorf("postgres: encode message usage: %w", err)
	}
	const query = `INSERT INTO agent_messages
		(session_id, run_id, role, content, reasoning, tool_calls, tool_call_id, tool_name, usage)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6::jsonb, $7, $8, $9::jsonb)
		RETURNING ` + messageColumns
	stored, err := scanMessage(r.pool.QueryRow(ctx, query,
		message.SessionID,
		message.RunID,
		message.Role,
		content,
		message.Reasoning,
		toolCalls,
		nullIfEmpty(message.ToolCallID),
		nullIfEmpty(message.ToolName),
		usage,
	))
	if err != nil {
		if mapped := mapForeignKeyViolation(err); mapped != nil {
			return agent.Message{}, mapped
		}
		return agent.Message{}, fmt.Errorf("postgres: append agent message: %w", err)
	}
	return stored, nil
}

// AppendAssistantPlaceholder inserts the empty assistant message that anchors
// one model turn's streamed parts and returns its ID.
func (r *MessageRepository) AppendAssistantPlaceholder(ctx context.Context, sessionID, runID int64) (int64, error) {
	var id int64
	if err := r.pool.QueryRow(ctx,
		`INSERT INTO agent_messages (session_id, run_id, role, content)
		 VALUES ($1, $2, 'assistant', '[]'::jsonb)
		 RETURNING id`,
		sessionID, runID).Scan(&id); err != nil {
		if mapped := mapForeignKeyViolation(err); mapped != nil {
			return 0, mapped
		}
		return 0, fmt.Errorf("postgres: append assistant placeholder: %w", err)
	}
	return id, nil
}

// CompleteAssistant replaces the placeholder's content, reasoning, tool calls
// and usage. The role guard makes completing a non-assistant message a
// not-found error instead of silently corrupting the history.
func (r *MessageRepository) CompleteAssistant(ctx context.Context, messageID int64, parts []agent.Part, reasoning string, calls []agent.ToolCall, usage *agent.Usage) error {
	content, err := encodeParts(parts)
	if err != nil {
		return fmt.Errorf("postgres: encode assistant content: %w", err)
	}
	toolCalls, err := encodeToolCalls(calls)
	if err != nil {
		return fmt.Errorf("postgres: encode assistant tool calls: %w", err)
	}
	encodedUsage, err := encodeUsage(usage)
	if err != nil {
		return fmt.Errorf("postgres: encode assistant usage: %w", err)
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE agent_messages
		    SET content = $2::jsonb, reasoning = $3, tool_calls = $4::jsonb, usage = $5::jsonb
		  WHERE id = $1 AND role = 'assistant'`,
		messageID, content, reasoning, toolCalls, encodedUsage)
	if err != nil {
		return fmt.Errorf("postgres: complete assistant message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrNotFound
	}
	return nil
}
