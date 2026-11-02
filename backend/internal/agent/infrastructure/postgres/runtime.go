package agentpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const profileColumns = `id, key, name, system_prompt, allowed_tools, model, budgets, enabled,
	created_at, updated_at`

const runColumns = `id, profile_key, work_item_id, conversation_id, status, terminal_reason,
	turns, usage, owner_instance, lease_epoch, context_packet_id, started_at, finished_at`

const messageColumns = `id, run_id, conversation_id, role, parts, tool_calls, tool_call_id,
	usage, created_at`

const packetColumns = `id, run_id, version_id, sections, token_counts, hash, created_at`

// RuntimeRepository stores agent_profile, agent_run, agent_message and
// context_packet rows.
type RuntimeRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewRuntimeRepository wires the agent runtime repository.
func NewRuntimeRepository(pool *pgxpool.Pool, ids platformid.Generator) *RuntimeRepository {
	return &RuntimeRepository{pool: pool, ids: ids}
}

// EnsureProfile inserts a profile by key when missing; existing profiles are
// returned unchanged so the startup seed never overwrites admin edits.
func (r *RuntimeRepository) EnsureProfile(ctx context.Context, profile agent.AgentProfile) (agent.AgentProfile, bool, error) {
	if err := profile.Validate(); err != nil {
		return agent.AgentProfile{}, false, err
	}
	if profile.ID <= 0 {
		profile.ID = r.ids.Next()
	}
	tools := profile.AllowedTools
	if tools == nil {
		tools = []string{}
	}
	encodedTools, err := encodeJSONArray("allowed tools", tools)
	if err != nil {
		return agent.AgentProfile{}, false, err
	}
	encodedBudgets, err := encodeJSONObject("profile budgets", profile.Budgets)
	if err != nil {
		return agent.AgentProfile{}, false, err
	}
	createdAt := timeOrNow(profile.CreatedAt)
	updatedAt := timeOrNow(profile.UpdatedAt)

	stored, err := scanProfile(r.pool.QueryRow(ctx, `
		INSERT INTO agent_profile (id, key, name, system_prompt, allowed_tools, model,
			budgets, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, $8, $9, $10)
		ON CONFLICT (key) DO NOTHING
		RETURNING `+profileColumns,
		profile.ID, profile.Key, profile.Name, profile.SystemPrompt, encodedTools,
		profile.Model, encodedBudgets, profile.Enabled, createdAt, updatedAt))
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agent.AgentProfile{}, false, wrapDB("insert agent profile", err)
	}
	// ON CONFLICT DO NOTHING produced no row: the profile already exists.
	existing, getErr := r.GetProfile(ctx, profile.Key)
	if getErr != nil {
		return agent.AgentProfile{}, false, getErr
	}
	return existing, false, nil
}

// GetProfile loads a profile by key, or agent.ErrNotFound.
func (r *RuntimeRepository) GetProfile(ctx context.Context, key string) (agent.AgentProfile, error) {
	profile, err := scanProfile(r.pool.QueryRow(ctx, `
		SELECT `+profileColumns+` FROM agent_profile WHERE key = $1
	`, key))
	if err != nil {
		return agent.AgentProfile{}, wrapDB("get agent profile", err)
	}
	return profile, nil
}

// ListProfiles returns every profile ordered by key.
func (r *RuntimeRepository) ListProfiles(ctx context.Context) ([]agent.AgentProfile, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+profileColumns+` FROM agent_profile ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]agent.AgentProfile, 0)
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	return profiles, nil
}

// CreateRun inserts a running agent_run row.
func (r *RuntimeRepository) CreateRun(ctx context.Context, run agent.AgentRun) (agent.AgentRun, error) {
	if err := run.Validate(); err != nil {
		return agent.AgentRun{}, err
	}
	if run.ID <= 0 {
		run.ID = r.ids.Next()
	}
	usage := run.Usage
	if usage == nil {
		usage = map[string]int{}
	}
	encodedUsage, err := encodeJSONObject("run usage", usage)
	if err != nil {
		return agent.AgentRun{}, err
	}
	startedAt := timeOrNow(run.StartedAt)

	stored, err := scanRun(r.pool.QueryRow(ctx, `
		INSERT INTO agent_run (id, profile_key, work_item_id, conversation_id, status,
			terminal_reason, turns, usage, owner_instance, lease_epoch, context_packet_id,
			started_at, finished_at)
		VALUES ($1, $2, $3, $4, 'running', NULL, 0, $5::jsonb, $6, $7, NULL, $8, NULL)
		RETURNING `+runColumns,
		run.ID, run.ProfileKey, run.WorkItemID, run.ConversationID, encodedUsage,
		run.OwnerInstance, run.LeaseEpoch, startedAt))
	if err != nil {
		return agent.AgentRun{}, wrapDB("create agent run", err)
	}
	return stored, nil
}

// FinishRun persists the terminal status and counters, fenced by lease_epoch.
// It only requires the run identity and status so a worker that holds the
// stored run ID can finish without re-supplying the immutable run fields.
func (r *RuntimeRepository) FinishRun(ctx context.Context, run agent.AgentRun) error {
	if run.ID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if !run.Status.Valid() {
		return platform.Invalid("status", "must be one of running, done, failed, aborted, interrupted")
	}
	usage := run.Usage
	if usage == nil {
		usage = map[string]int{}
	}
	encodedUsage, err := encodeJSONObject("run usage", usage)
	if err != nil {
		return err
	}
	finishedAt := time.Now().UTC()
	if run.FinishedAt != nil {
		finishedAt = timeOrNow(*run.FinishedAt)
	}
	var reason *string
	if run.TerminalReason != nil && *run.TerminalReason != "" {
		reason = run.TerminalReason
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE agent_run
		SET status = $3, terminal_reason = $4, turns = $5, usage = $6::jsonb,
		    finished_at = $7
		WHERE id = $1 AND lease_epoch = $2
	`, run.ID, run.LeaseEpoch, string(run.Status), reason, run.Turns,
		encodedUsage, finishedAt)
	if err != nil {
		return wrapDB("finish agent run", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrLeaseLost
	}
	return nil
}

// AppendMessage inserts one transcript row.
func (r *RuntimeRepository) AppendMessage(ctx context.Context, message agent.AgentMessage) (agent.AgentMessage, error) {
	if err := message.Validate(); err != nil {
		return agent.AgentMessage{}, err
	}
	if message.ID <= 0 {
		message.ID = r.ids.Next()
	}
	parts := message.Parts
	if parts == nil {
		parts = []agent.MessagePart{}
	}
	encodedParts, err := encodeJSONArray("message parts", parts)
	if err != nil {
		return agent.AgentMessage{}, err
	}
	calls := message.ToolCalls
	if calls == nil {
		calls = []agent.ToolCall{}
	}
	encodedCalls, err := encodeJSONArray("message tool calls", calls)
	if err != nil {
		return agent.AgentMessage{}, err
	}
	usage := message.Usage
	if usage == nil {
		usage = map[string]int{}
	}
	encodedUsage, err := encodeJSONObject("message usage", usage)
	if err != nil {
		return agent.AgentMessage{}, err
	}
	createdAt := timeOrNow(message.CreatedAt)

	stored, err := scanMessage(r.pool.QueryRow(ctx, `
		INSERT INTO agent_message (id, run_id, conversation_id, role, parts, tool_calls,
			tool_call_id, usage, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8::jsonb, $9)
		RETURNING `+messageColumns,
		message.ID, message.RunID, message.ConversationID, string(message.Role),
		encodedParts, encodedCalls, message.ToolCallID, encodedUsage, createdAt))
	if err != nil {
		return agent.AgentMessage{}, wrapDB("append agent message", err)
	}
	return stored, nil
}

// ListRunMessages returns a run's transcript in ID order.
func (r *RuntimeRepository) ListRunMessages(ctx context.Context, runID int64, limit int) ([]agent.AgentMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+messageColumns+` FROM agent_message
		WHERE run_id = $1
		ORDER BY id ASC
		LIMIT $2
	`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("list agent messages: %w", err)
	}
	defer rows.Close()
	messages := make([]agent.AgentMessage, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent messages: %w", err)
	}
	return messages, nil
}

// SaveContextPacket inserts a packet and points its run at it.
func (r *RuntimeRepository) SaveContextPacket(ctx context.Context, packet agent.ContextPacket) (agent.ContextPacket, error) {
	if packet.ID <= 0 {
		packet.ID = r.ids.Next()
	}
	packet.CreatedAt = timeOrNow(packet.CreatedAt)
	if err := packet.Validate(); err != nil {
		return agent.ContextPacket{}, err
	}
	encodedSections, err := encodeJSONArray("context sections", packet.Sections)
	if err != nil {
		return agent.ContextPacket{}, err
	}
	encodedCounts, err := encodeJSONObject("context token counts", packet.TokenCounts)
	if err != nil {
		return agent.ContextPacket{}, err
	}

	stored, err := scanPacket(r.pool.QueryRow(ctx, `
		INSERT INTO context_packet (id, run_id, version_id, sections, token_counts, hash, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7)
		RETURNING `+packetColumns,
		packet.ID, packet.RunID, packet.VersionID, encodedSections, encodedCounts,
		packet.Hash, packet.CreatedAt))
	if err != nil {
		return agent.ContextPacket{}, wrapDB("save context packet", err)
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE agent_run SET context_packet_id = $1 WHERE id = $2
	`, stored.ID, stored.RunID); err != nil {
		return agent.ContextPacket{}, fmt.Errorf("attach context packet to run: %w", err)
	}
	return stored, nil
}

// GetContextPacket loads one packet, or agent.ErrNotFound.
func (r *RuntimeRepository) GetContextPacket(ctx context.Context, id int64) (agent.ContextPacket, error) {
	packet, err := scanPacket(r.pool.QueryRow(ctx, `
		SELECT `+packetColumns+` FROM context_packet WHERE id = $1
	`, id))
	if err != nil {
		return agent.ContextPacket{}, wrapDB("get context packet", err)
	}
	return packet, nil
}

func scanProfile(row rowScanner) (agent.AgentProfile, error) {
	var profile agent.AgentProfile
	var toolsRaw, budgetsRaw []byte
	if err := row.Scan(&profile.ID, &profile.Key, &profile.Name, &profile.SystemPrompt,
		&toolsRaw, &profile.Model, &budgetsRaw, &profile.Enabled,
		&profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return agent.AgentProfile{}, err
	}
	profile.AllowedTools = []string{}
	if len(toolsRaw) > 0 {
		if err := json.Unmarshal(toolsRaw, &profile.AllowedTools); err != nil {
			return agent.AgentProfile{}, fmt.Errorf("decode allowed tools: %w", err)
		}
	}
	if len(budgetsRaw) > 0 {
		if err := json.Unmarshal(budgetsRaw, &profile.Budgets); err != nil {
			return agent.AgentProfile{}, fmt.Errorf("decode profile budgets: %w", err)
		}
	}
	return profile, nil
}

func scanRun(row rowScanner) (agent.AgentRun, error) {
	var run agent.AgentRun
	var status string
	var usageRaw []byte
	if err := row.Scan(&run.ID, &run.ProfileKey, &run.WorkItemID, &run.ConversationID,
		&status, &run.TerminalReason, &run.Turns, &usageRaw, &run.OwnerInstance,
		&run.LeaseEpoch, &run.ContextPacketID, &run.StartedAt, &run.FinishedAt); err != nil {
		return agent.AgentRun{}, err
	}
	run.Status = agent.AgentRunStatus(status)
	run.Usage = map[string]int{}
	if len(usageRaw) > 0 {
		if err := json.Unmarshal(usageRaw, &run.Usage); err != nil {
			return agent.AgentRun{}, fmt.Errorf("decode run usage: %w", err)
		}
	}
	return run, nil
}

func scanMessage(row rowScanner) (agent.AgentMessage, error) {
	var message agent.AgentMessage
	var role string
	var partsRaw, callsRaw, usageRaw []byte
	if err := row.Scan(&message.ID, &message.RunID, &message.ConversationID, &role,
		&partsRaw, &callsRaw, &message.ToolCallID, &usageRaw, &message.CreatedAt); err != nil {
		return agent.AgentMessage{}, err
	}
	message.Role = agent.MessageRole(role)
	message.Parts = []agent.MessagePart{}
	message.ToolCalls = []agent.ToolCall{}
	message.Usage = map[string]int{}
	if len(partsRaw) > 0 {
		if err := json.Unmarshal(partsRaw, &message.Parts); err != nil {
			return agent.AgentMessage{}, fmt.Errorf("decode message parts: %w", err)
		}
	}
	if len(callsRaw) > 0 {
		if err := json.Unmarshal(callsRaw, &message.ToolCalls); err != nil {
			return agent.AgentMessage{}, fmt.Errorf("decode message tool calls: %w", err)
		}
	}
	if len(usageRaw) > 0 {
		if err := json.Unmarshal(usageRaw, &message.Usage); err != nil {
			return agent.AgentMessage{}, fmt.Errorf("decode message usage: %w", err)
		}
	}
	return message, nil
}

func scanPacket(row rowScanner) (agent.ContextPacket, error) {
	var packet agent.ContextPacket
	var sectionsRaw, countsRaw []byte
	if err := row.Scan(&packet.ID, &packet.RunID, &packet.VersionID, &sectionsRaw,
		&countsRaw, &packet.Hash, &packet.CreatedAt); err != nil {
		return agent.ContextPacket{}, err
	}
	packet.Sections = []agent.ContextSection{}
	packet.TokenCounts = map[string]int{}
	if len(sectionsRaw) > 0 {
		if err := json.Unmarshal(sectionsRaw, &packet.Sections); err != nil {
			return agent.ContextPacket{}, fmt.Errorf("decode context sections: %w", err)
		}
	}
	if len(countsRaw) > 0 {
		if err := json.Unmarshal(countsRaw, &packet.TokenCounts); err != nil {
			return agent.ContextPacket{}, fmt.Errorf("decode context token counts: %w", err)
		}
	}
	return packet, nil
}

var _ agent.RuntimeRepository = (*RuntimeRepository)(nil)
