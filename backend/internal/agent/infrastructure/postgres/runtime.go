package agentpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const profileColumns = `id, key, name, system_prompt, allowed_tools, model, budgets, enabled,
	seed_version, created_at, updated_at`

const runColumns = `id, profile_key, work_item_id, conversation_id, status, terminal_reason,
	turns, usage, owner_instance, lease_epoch, context_packet_id, started_at, finished_at,
	model, config_id, fallback_from, tokens, cache`

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
			budgets, enabled, seed_version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, $8, $9, $10, $11)
		ON CONFLICT (key) DO NOTHING
		RETURNING `+profileColumns,
		profile.ID, profile.Key, profile.Name, profile.SystemPrompt, encodedTools,
		profile.Model, encodedBudgets, profile.Enabled, profile.SeedVersion, createdAt, updatedAt))
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

// ApplyProfileSeedBackfill writes the one-time v0.2.3 seed reconciliation. The
// update is fenced by seed_version so a concurrent writer or a second startup
// cannot apply it twice; nil fields keep the stored value. It reports whether a
// row changed and returns the current row when the marker already advanced.
func (r *RuntimeRepository) ApplyProfileSeedBackfill(
	ctx context.Context,
	key string,
	update agent.ProfileSeedBackfill,
) (agent.AgentProfile, bool, error) {
	if strings.TrimSpace(key) == "" {
		return agent.AgentProfile{}, false, platform.Invalid("key", "must not be empty")
	}
	if update.SeedVersion <= 0 {
		return agent.AgentProfile{}, false, platform.Invalid("seed_version", "must be a positive integer")
	}
	var encodedTools any
	if update.AllowedTools != nil {
		value, err := encodeJSONArray("profile backfill allowed tools", *update.AllowedTools)
		if err != nil {
			return agent.AgentProfile{}, false, err
		}
		encodedTools = value
	}
	var encodedBudgets any
	if update.Budgets != nil {
		value, err := encodeJSONObject("profile backfill budgets", *update.Budgets)
		if err != nil {
			return agent.AgentProfile{}, false, err
		}
		encodedBudgets = value
	}
	stored, err := scanProfile(r.pool.QueryRow(ctx, `
		UPDATE agent_profile
		SET allowed_tools = COALESCE($2::jsonb, allowed_tools),
		    budgets = COALESCE($3::jsonb, budgets),
		    seed_version = $4,
		    updated_at = now()
		WHERE key = $1 AND seed_version < $4
		RETURNING `+profileColumns,
		key, encodedTools, encodedBudgets, update.SeedVersion))
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agent.AgentProfile{}, false, wrapDB("apply profile seed backfill", err)
	}
	// The marker was already advanced (idempotent second pass).
	existing, getErr := r.GetProfile(ctx, key)
	if getErr != nil {
		return agent.AgentProfile{}, false, getErr
	}
	return existing, false, nil
}

// CreateRun inserts one agent_run row with the run's status. A dispatch-ready
// attempt is created running, while an attempt that failed before dispatch is
// recorded with its terminal snapshot in the same insert (Task 75), so the
// Agent center can list it without a transient running row.
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
	tokens := run.Tokens
	if tokens == nil {
		tokens = map[string]int{}
	}
	encodedTokens, err := encodeJSONObject("run tokens", tokens)
	if err != nil {
		return agent.AgentRun{}, err
	}
	cache := run.Cache
	if cache == nil {
		cache = map[string]int{}
	}
	encodedCache, err := encodeJSONObject("run cache", cache)
	if err != nil {
		return agent.AgentRun{}, err
	}
	startedAt := timeOrNow(run.StartedAt)
	var configID *int64
	if run.ConfigID > 0 {
		value := run.ConfigID
		configID = &value
	}
	var fallbackFrom *string
	if run.FallbackFrom != "" {
		value := run.FallbackFrom
		fallbackFrom = &value
	}
	var terminalReason *string
	if run.TerminalReason != nil && strings.TrimSpace(*run.TerminalReason) != "" {
		value := strings.TrimSpace(*run.TerminalReason)
		terminalReason = &value
	}

	stored, err := scanRun(r.pool.QueryRow(ctx, `
		INSERT INTO agent_run (id, profile_key, work_item_id, conversation_id, status,
			terminal_reason, turns, usage, owner_instance, lease_epoch, context_packet_id,
			started_at, finished_at, model, config_id, fallback_from, tokens, cache)
		VALUES ($1, $2, $3, $4, $5, $6, 0, $7::jsonb, $8, $9, NULL, $10, $11, $12, $13, $14, $15::jsonb, $16::jsonb)
		RETURNING `+runColumns,
		run.ID, run.ProfileKey, run.WorkItemID, run.ConversationID, string(run.Status),
		terminalReason, encodedUsage, run.OwnerInstance, run.LeaseEpoch, startedAt,
		run.FinishedAt, run.Model, configID, fallbackFrom, encodedTokens, encodedCache))
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
	tokens := run.Tokens
	if tokens == nil {
		tokens = map[string]int{}
	}
	encodedTokens, err := encodeJSONObject("run tokens", tokens)
	if err != nil {
		return err
	}
	cache := run.Cache
	if cache == nil {
		cache = map[string]int{}
	}
	encodedCache, err := encodeJSONObject("run cache", cache)
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
		    tokens = $7::jsonb, cache = $8::jsonb, finished_at = $9
		WHERE id = $1 AND lease_epoch = $2
	`, run.ID, run.LeaseEpoch, string(run.Status), reason, run.Turns,
		encodedUsage, encodedTokens, encodedCache, finishedAt)
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

// LatestVersionContextPacket returns the newest context packet persisted for one
// version, or agent.ErrNotFound. It is the read side of the 0.2.1 cache-first
// context packet API: read-only and version-scoped. The version is already
// denormalized on the row (context_packet.version_id), so no agent_run join is
// needed; ordering by the snowflake ID is the persisted creation order.
func (r *RuntimeRepository) LatestVersionContextPacket(ctx context.Context, versionID int64) (agent.ContextPacket, error) {
	if versionID <= 0 {
		return agent.ContextPacket{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	packet, err := scanPacket(r.pool.QueryRow(ctx, `
		SELECT `+packetColumns+` FROM context_packet
		WHERE version_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ContextPacket{}, agent.ErrNotFound
	}
	if err != nil {
		return agent.ContextPacket{}, wrapDB("latest context packet", err)
	}
	return packet, nil
}

// UpdateContextPacketCacheStats overwrites the provider-reported prompt cache
// bookkeeping of one persisted packet (contracts §18.2.6). Only token_counts
// changes; sections, hash and identity stay immutable. It returns
// agent.ErrNotFound when the packet does not exist.
func (r *RuntimeRepository) UpdateContextPacketCacheStats(ctx context.Context, id int64, cachedTokens int, cacheHit bool) error {
	if id <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if cachedTokens < 0 {
		return platform.Invalid("cached_tokens", "must not be negative")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE context_packet
		SET token_counts = jsonb_set(
			jsonb_set(token_counts, '{cached_tokens}', to_jsonb($2::int), true),
			'{cache_hit}', to_jsonb($3::int), true)
		WHERE id = $1
	`, id, cachedTokens, boolToInt(cacheHit))
	if err != nil {
		return wrapDB("update context packet cache stats", err)
	}
	if tag.RowsAffected() == 0 {
		return agent.ErrNotFound
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func scanProfile(row rowScanner) (agent.AgentProfile, error) {
	var profile agent.AgentProfile
	var toolsRaw, budgetsRaw []byte
	if err := row.Scan(&profile.ID, &profile.Key, &profile.Name, &profile.SystemPrompt,
		&toolsRaw, &profile.Model, &budgetsRaw, &profile.Enabled, &profile.SeedVersion,
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
	var usageRaw, tokensRaw, cacheRaw []byte
	var configID *int64
	var fallbackFrom *string
	if err := row.Scan(&run.ID, &run.ProfileKey, &run.WorkItemID, &run.ConversationID,
		&status, &run.TerminalReason, &run.Turns, &usageRaw, &run.OwnerInstance,
		&run.LeaseEpoch, &run.ContextPacketID, &run.StartedAt, &run.FinishedAt,
		&run.Model, &configID, &fallbackFrom, &tokensRaw, &cacheRaw); err != nil {
		return agent.AgentRun{}, err
	}
	if configID != nil {
		run.ConfigID = *configID
	}
	if fallbackFrom != nil {
		run.FallbackFrom = *fallbackFrom
	}
	run.Status = agent.AgentRunStatus(status)
	run.Usage = map[string]int{}
	if len(usageRaw) > 0 {
		if err := json.Unmarshal(usageRaw, &run.Usage); err != nil {
			return agent.AgentRun{}, fmt.Errorf("decode run usage: %w", err)
		}
	}
	run.Tokens = map[string]int{}
	if len(tokensRaw) > 0 {
		if err := json.Unmarshal(tokensRaw, &run.Tokens); err != nil {
			return agent.AgentRun{}, fmt.Errorf("decode run tokens: %w", err)
		}
	}
	run.Cache = map[string]int{}
	if len(cacheRaw) > 0 {
		if err := json.Unmarshal(cacheRaw, &run.Cache); err != nil {
			return agent.AgentRun{}, fmt.Errorf("decode run cache: %w", err)
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
