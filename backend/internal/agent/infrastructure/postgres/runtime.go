package agentpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
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
	usage, stream_state, created_at`

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

// ApplyProfileSeedBackfill writes the one-time seed reconciliation. The
// update is fenced by seed_version so a concurrent writer or a second startup
// cannot apply it twice. The tool increment is unioned against the row being
// updated, the exact removal names are dropped from that same union (v0.2.4-C
// seed 3 retired-tool cleanup), and the budget upgrade only applies while the
// stored budgets still equal the legacy snapshot, so a concurrent administrator
// PATCH is never overwritten with values read before it. It reports whether a
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
	increment := update.AddAllowedTools
	if increment == nil {
		increment = []string{}
	}
	encodedTools, err := encodeJSONArray("profile backfill allowed tools", increment)
	if err != nil {
		return agent.AgentProfile{}, false, err
	}
	removals := update.RemoveAllowedTools
	if removals == nil {
		removals = []string{}
	}
	var encodedLegacyBudgets, encodedCurrentBudgets any
	if update.BudgetUpgrade != nil {
		encodedLegacyBudgets, err = encodeJSONObject(
			"profile backfill legacy budgets", update.BudgetUpgrade.Legacy)
		if err != nil {
			return agent.AgentProfile{}, false, err
		}
		encodedCurrentBudgets, err = encodeJSONObject(
			"profile backfill budgets", update.BudgetUpgrade.Current)
		if err != nil {
			return agent.AgentProfile{}, false, err
		}
	}
	stored, err := scanProfile(r.pool.QueryRow(ctx, `
		UPDATE agent_profile
		SET allowed_tools = CASE
		        WHEN jsonb_array_length($2::jsonb) = 0 AND cardinality($6::text[]) = 0 THEN allowed_tools
		        ELSE (
		            SELECT COALESCE(jsonb_agg(value ORDER BY first_ord), '[]'::jsonb)
		            FROM (
		                SELECT DISTINCT ON (value) value, ord AS first_ord
		                FROM jsonb_array_elements_text(allowed_tools || $2::jsonb)
		                     WITH ORDINALITY AS entries(value, ord)
		                WHERE NOT (value = ANY($6::text[]))
		                ORDER BY value, ord
		            ) deduped
		        )
		    END,
		    budgets = CASE
		        WHEN $3::jsonb IS NOT NULL AND budgets = $3::jsonb THEN $4::jsonb
		        ELSE budgets
		    END,
		    seed_version = $5,
		    updated_at = now()
		WHERE key = $1 AND seed_version < $5
		RETURNING `+profileColumns,
		key, encodedTools, encodedLegacyBudgets, encodedCurrentBudgets, update.SeedVersion, removals))
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

// FinishRun persists the terminal status and counters, fenced by lease_epoch AND
// by the run still being running. It only requires the run identity and status so
// a worker that holds the stored run ID can finish without re-supplying the
// immutable run fields.
//
// The running-only condition (v0.2.4-H T12) makes a terminal run FROZEN, exactly
// as UpdateMessage freezes a terminal transcript row. It is required because
// agent_run.lease_epoch is never bumped after CreateRun: an attempt whose WORK
// ITEM lease expired and was recovered can still write its own terminal, so the
// run-level crash sweep (H-E2) — which selects on status='running' and writes a
// few round trips later — must not be able to overwrite a terminal that the
// attempt itself wrote in that window. Zero matching rows keeps reporting
// ErrLeaseLost: the fenced terminal write did not land, and the caller must not
// retry it (a run that became terminal is no longer selected by the sweep).
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
		WHERE id = $1 AND lease_epoch = $2 AND status = $10
	`, run.ID, run.LeaseEpoch, string(run.Status), reason, run.Turns,
		encodedUsage, encodedTokens, encodedCache, finishedAt, string(agent.AgentRunRunning))
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
	encodedParts, encodedCalls, encodedUsage, err := encodeMessageContent(message)
	if err != nil {
		return agent.AgentMessage{}, err
	}
	createdAt := timeOrNow(message.CreatedAt)

	stored, err := scanMessage(r.pool.QueryRow(ctx, `
		INSERT INTO agent_message (id, run_id, conversation_id, role, parts, tool_calls,
			tool_call_id, usage, stream_state, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8::jsonb, $9, $10)
		RETURNING `+messageColumns,
		message.ID, message.RunID, message.ConversationID, string(message.Role),
		encodedParts, encodedCalls, message.ToolCallID, encodedUsage,
		agent.NormalizeMessageStreamState(message.StreamState), createdAt))
	if err != nil {
		return agent.AgentMessage{}, wrapDB("append agent message", err)
	}
	return stored, nil
}

// UpdateMessage rewrites one streaming transcript row in place (v0.2.4-D T8).
// The guard is the row's own stream_state: only a streaming row may be rewritten
// or sealed, so a terminal row is frozen against every late write — including a
// batch/legacy row, which is complete from insert. The streaming predicate is
// passed as the domain constant, never re-spelled as a SQL literal (constitution
// 五.4). Zero matching rows is disambiguated into "missing" vs "frozen" so the
// caller can tell a lost row from a sealed one.
func (r *RuntimeRepository) UpdateMessage(ctx context.Context, message agent.AgentMessage) error {
	if message.ID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if err := message.Validate(); err != nil {
		return err
	}
	encodedParts, encodedCalls, encodedUsage, err := encodeMessageContent(message)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE agent_message
		SET parts = $2::jsonb, tool_calls = $3::jsonb, usage = $4::jsonb, stream_state = $5
		WHERE id = $1 AND stream_state = $6
	`, message.ID, encodedParts, encodedCalls, encodedUsage,
		agent.NormalizeMessageStreamState(message.StreamState), agent.MessageStreamStateStreaming)
	if err != nil {
		return wrapDB("update agent message", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var state string
	if err := r.pool.QueryRow(ctx, `SELECT stream_state FROM agent_message WHERE id = $1`, message.ID).Scan(&state); err != nil {
		return wrapDB("update agent message", err)
	}
	return agent.ErrMessageStreamClosed
}

// encodeMessageContent renders the three mutable JSON columns of a transcript
// row, defaulting to empty collections so a nil slice never becomes SQL NULL.
func encodeMessageContent(message agent.AgentMessage) (parts string, calls string, usage string, err error) {
	partsValue := message.Parts
	if partsValue == nil {
		partsValue = []agent.MessagePart{}
	}
	if parts, err = encodeJSONArray("message parts", partsValue); err != nil {
		return "", "", "", err
	}
	callsValue := message.ToolCalls
	if callsValue == nil {
		callsValue = []agent.ToolCall{}
	}
	if calls, err = encodeJSONArray("message tool calls", callsValue); err != nil {
		return "", "", "", err
	}
	usageValue := message.Usage
	if usageValue == nil {
		usageValue = map[string]int{}
	}
	if usage, err = encodeJSONObject("message usage", usageValue); err != nil {
		return "", "", "", err
	}
	return parts, calls, usage, nil
}

// noLimit is the caller-facing sentinel that tells queryRunMessages to read the
// WHOLE run (no SQL LIMIT). It is spelled as a name, not a bare 0, because `0`
// reads to a human as "zero rows" while it means "unlimited" to the query
// (H-E20 double meaning). Only the admin Agent Center transcript passes it; the
// runtime repository always passes a bounded positive page (defaultRunMessagePage).
const noLimit = 0

// defaultRunMessagePage is the bounded default ListRunMessages applies when its
// caller passes a non-positive limit — a safety page, NOT "unlimited". Keeping it
// distinct from noLimit makes the two limit semantics unambiguous.
const defaultRunMessagePage = 200

// ListRunMessages returns a run's transcript in ID order.
func (r *RuntimeRepository) ListRunMessages(ctx context.Context, runID int64, limit int) ([]agent.AgentMessage, error) {
	if limit <= 0 {
		limit = defaultRunMessagePage
	}
	return queryRunMessages(ctx, r.pool, runID, limit)
}

// queryRunMessages is the ONE transcript read of the agent context, shared by
// the runtime repository (prompt assembly, bounded page) and the admin-only
// Agent Center transcript (limit == noLimit reads the whole run: a hidden default
// page would silently truncate what the administrator is shown — constitution 七.6).
func queryRunMessages(ctx context.Context, pool *pgxpool.Pool, runID int64, limit int) ([]agent.AgentMessage, error) {
	query := `
		SELECT ` + messageColumns + ` FROM agent_message
		WHERE run_id = $1
		ORDER BY id ASC`
	args := []any{runID}
	if limit > noLimit {
		query += `
		LIMIT $2`
		args = append(args, limit)
	}
	rows, err := pool.Query(ctx, query, args...)
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

// crashedRunUnleasedStatuses are the work-item statuses that prove the item is no
// longer leased by ANY attempt: reopened for a retry (open), settled at the
// attempt cap (dead for the legacy queue, failed for the 0.2.1 queue), or finished
// (done). waiting_human is deliberately absent: a parked run awaiting a human is
// not a crash. Bound as a query parameter so the status vocabulary has one
// authority (agent/domain), never a SQL-inline mirror.
var crashedRunUnleasedStatuses = []string{
	string(agent.WorkItemOpen),
	string(agent.WorkItemDead),
	string(agent.WorkItemDone),
	string(agent.WorkItemFailed),
}

// ListCrashedRuns returns the runs whose attempt no longer owns their work item
// (v0.2.4-H T12, H-E2). The predicate is a lease fact, not an age heuristic:
// either the item is in a state that holds no lease at all, or its lease_epoch
// moved past the epoch this run was created with (a later attempt took over).
// A live attempt — claimed/running with the matching epoch — is never returned,
// so the sweep can never finish a run that is still executing.
//
// The read is bounded (limit must be positive): a backlog is drained over
// successive recovery ticks rather than through one unbounded scan.
func (r *RuntimeRepository) ListCrashedRuns(ctx context.Context, limit int) ([]agentapp.CrashedRun, error) {
	if limit <= 0 {
		return nil, platform.Invalid("limit", "must be a positive integer")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT run.id, run.lease_epoch, run.work_item_id, item.version_id,
		       COALESCE(run.conversation_id, 0), run.profile_key, run.turns,
		       run.usage, run.tokens, run.cache
		FROM agent_run run
		JOIN work_item item ON item.id = run.work_item_id
		WHERE run.status = $1
		  AND (item.status = ANY($2::text[]) OR item.lease_epoch <> run.lease_epoch)
		ORDER BY run.id
		LIMIT $3
	`, string(agent.AgentRunRunning), crashedRunUnleasedStatuses, limit)
	if err != nil {
		return nil, wrapDB("list crashed agent runs", err)
	}
	defer rows.Close()
	runs := make([]agentapp.CrashedRun, 0)
	for rows.Next() {
		var run agentapp.CrashedRun
		var usageRaw, tokensRaw, cacheRaw []byte
		if err := rows.Scan(&run.ID, &run.LeaseEpoch, &run.WorkItemID, &run.VersionID,
			&run.ConversationID, &run.ProfileKey, &run.Turns, &usageRaw, &tokensRaw, &cacheRaw); err != nil {
			return nil, fmt.Errorf("scan crashed agent run: %w", err)
		}
		counters := []struct {
			name   string
			raw    []byte
			target *map[string]int
		}{
			{"usage", usageRaw, &run.Usage},
			{"tokens", tokensRaw, &run.Tokens},
			{"cache", cacheRaw, &run.Cache},
		}
		for _, counter := range counters {
			*counter.target = map[string]int{}
			if len(counter.raw) == 0 {
				continue
			}
			if err := json.Unmarshal(counter.raw, counter.target); err != nil {
				return nil, fmt.Errorf("decode crashed run %s: %w", counter.name, err)
			}
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list crashed agent runs: %w", err)
	}
	return runs, nil
}

// ListStreamingRunMessages returns EVERY still-streaming transcript row of one
// run, oldest first. It is the crash sweep's read: unlike ListRunMessages it has
// no page bound, because a hidden cap would leave heads streaming forever
// (constitution 七.6). In practice a run holds at most one streaming row per
// interrupted turn; the read is indexed by run_id.
func (r *RuntimeRepository) ListStreamingRunMessages(ctx context.Context, runID int64) ([]agent.AgentMessage, error) {
	if runID <= 0 {
		return nil, platform.Invalid("run_id", "must be a positive decimal ID")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+messageColumns+` FROM agent_message
		WHERE run_id = $1 AND stream_state = $2
		ORDER BY id ASC
	`, runID, agent.MessageStreamStateStreaming)
	if err != nil {
		return nil, wrapDB("list streaming agent messages", err)
	}
	defer rows.Close()
	messages := make([]agent.AgentMessage, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan streaming agent message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list streaming agent messages: %w", err)
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
		&partsRaw, &callsRaw, &message.ToolCallID, &usageRaw, &message.StreamState, &message.CreatedAt); err != nil {
		return agent.AgentMessage{}, err
	}
	message.Role = agent.MessageRole(role)
	message.StreamState = agent.NormalizeMessageStreamState(message.StreamState)
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
