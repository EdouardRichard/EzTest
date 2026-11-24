package agent

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// AgentProfile is a seeded runtime role (Host/Curator/Scout in P1).
type AgentProfile struct {
	ID           int64
	Key          string
	Name         string
	SystemPrompt string
	AllowedTools []string
	Model        *string
	Budgets      ProfileBudgets
	Enabled      bool
	// SeedVersion is the built-in seed revision the profile row has been
	// reconciled to. Legacy rows start at 0; a fresh seed writes the current
	// version so the one-time backfill never rewrites an administrator edit.
	SeedVersion int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ProfileBudgets is the budgets JSONB shape from the P1 profile contract.
type ProfileBudgets struct {
	MaxTurns    int `json:"max_turns,omitempty"`
	SoftSeconds int `json:"soft_seconds,omitempty"`
	HardSeconds int `json:"hard_seconds,omitempty"`
}

// ProfileSeedBackfill is the marker-fenced update applied by the one-time
// profile backfill. The repository applies the tool increment as a set union
// against the row it is updating and the budget upgrade only while the stored
// budgets still equal the legacy triple, so an administrator edit that lands
// between the backfill snapshot and the write is never overwritten. It never
// carries a prompt body: prompt upgrades are append-only revision writes.
//
// RemoveAllowedTools (v0.2.4-C seed 3) drops exact tool names from the live row
// in the SAME fenced UPDATE as the union, so a retired tool (the 13 browser.*
// atomic actions) is removed and the domain-level increment added atomically.
// Removal is exact-name only: the caller derives the names from the stored row,
// and an administrator tool outside the removal list is never touched.
type ProfileSeedBackfill struct {
	AddAllowedTools    []string
	RemoveAllowedTools []string
	BudgetUpgrade      *ProfileBudgetUpgrade
	SeedVersion        int
}

// ProfileBudgetUpgrade raises one legacy budget envelope to the current one.
// Current is applied only when the stored budgets still equal Legacy.
type ProfileBudgetUpgrade struct {
	Legacy  ProfileBudgets
	Current ProfileBudgets
}

// Validate checks the profile key/prompt and budget bounds.
func (p AgentProfile) Validate() error {
	if strings.TrimSpace(p.Key) == "" {
		return platform.Invalid("key", "must not be empty")
	}
	if strings.TrimSpace(p.Name) == "" {
		return platform.Invalid("name", "must not be empty")
	}
	if strings.TrimSpace(p.SystemPrompt) == "" {
		return platform.Invalid("system_prompt", "must not be empty")
	}
	if err := validation.StringList("allowed_tools", p.AllowedTools, 0); err != nil {
		return err
	}
	if p.Budgets.MaxTurns < 0 {
		return platform.Invalid("budgets.max_turns", "must not be negative")
	}
	if p.Budgets.SoftSeconds < 0 || p.Budgets.HardSeconds < 0 {
		return platform.Invalid("budgets", "timeouts must not be negative")
	}
	if p.Budgets.SoftSeconds > 0 && p.Budgets.HardSeconds > 0 &&
		p.Budgets.SoftSeconds > p.Budgets.HardSeconds {
		return platform.Invalid("budgets", "soft_seconds must not exceed hard_seconds")
	}
	if p.SeedVersion < 0 {
		return platform.Invalid("seed_version", "must not be negative")
	}
	return nil
}

// AgentRunStatus is the lifecycle of one agent execution.
type AgentRunStatus string

// Agent run statuses.
const (
	AgentRunRunning     AgentRunStatus = "running"
	AgentRunDone        AgentRunStatus = "done"
	AgentRunFailed      AgentRunStatus = "failed"
	AgentRunAborted     AgentRunStatus = "aborted"
	AgentRunInterrupted AgentRunStatus = "interrupted"
)

// Valid reports whether the status is part of the frozen enum.
func (s AgentRunStatus) Valid() bool {
	switch s {
	case AgentRunRunning, AgentRunDone, AgentRunFailed, AgentRunAborted, AgentRunInterrupted:
		return true
	default:
		return false
	}
}

// Terminal reports whether the run reached a terminal status.
func (s AgentRunStatus) Terminal() bool {
	return s != AgentRunRunning && s.Valid()
}

// AgentRun is one run of an agent profile, fenced by lease_epoch. Model and
// ConfigID record the model/config actually used (spec §7.3); FallbackFrom is
// set only when a differently requested model was not used (P4 never switches
// models automatically, so it normally stays empty). Tokens and Cache are the
// 0.2.1 token/prompt-cache accounting persisted on the run (spec §7.5).
type AgentRun struct {
	ID              int64
	ProfileKey      string
	Model           string
	ConfigID        int64
	FallbackFrom    string
	Tokens          map[string]int
	Cache           map[string]int
	WorkItemID      *int64
	ConversationID  *int64
	Status          AgentRunStatus
	TerminalReason  *string
	Turns           int
	Usage           map[string]int
	OwnerInstance   string
	LeaseEpoch      int64
	ContextPacketID *int64
	StartedAt       time.Time
	FinishedAt      *time.Time
}

// Validate checks the run owner, status and counters.
func (r AgentRun) Validate() error {
	if strings.TrimSpace(r.ProfileKey) == "" {
		return platform.Invalid("profile_key", "must not be empty")
	}
	if !r.Status.Valid() {
		return platform.Invalid("status", "must be one of running, done, failed, aborted, interrupted")
	}
	if strings.TrimSpace(r.OwnerInstance) == "" {
		return platform.Invalid("owner_instance", "must not be empty")
	}
	if r.Turns < 0 {
		return platform.Invalid("turns", "must not be negative")
	}
	if r.LeaseEpoch < 0 {
		return platform.Invalid("lease_epoch", "must not be negative")
	}
	return nil
}

// MessageRole is the LLM transcript role of an agent_message row.
type MessageRole string

// Agent message roles.
const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

// Valid reports whether the role is part of the frozen enum.
func (r MessageRole) Valid() bool {
	switch r {
	case MessageRoleSystem, MessageRoleUser, MessageRoleAssistant, MessageRoleTool:
		return true
	default:
		return false
	}
}

// Agent transcript stream_state (v0.2.4-D T8, SC-D3). The project has ONE
// stream-state authority — version.MessageStreamStates() — shared by the Host
// conversation head and the agent transcript. These are DERIVED aliases, not a
// second enum (constitution 五.1/五.3): a change to the authority changes both
// surfaces at once, and db/agent_stream_state_enum_test.go pins the DB CHECK
// against the same authority. The cross-context domain import is ratified under
// R-H9 (pure-vocabulary reuse: no entity state, no side effects, compile-time
// alias + delegation) — see docs/design/architecture-constraints.md §5.1 and its
// ratification register (v0.2.4-H T14).
const (
	MessageStreamStateStreaming   = version.MessageStreamStateStreaming
	MessageStreamStateComplete    = version.MessageStreamStateComplete
	MessageStreamStateInterrupted = version.MessageStreamStateInterrupted
)

// MessageStreamStates returns the agent_message.stream_state enum, derived from
// the shared authority. The DB CHECK and migration gates compare against it.
func MessageStreamStates() []string { return version.MessageStreamStates() }

// NormalizeMessageStreamState maps the empty legacy value to complete: a row
// written without an explicit state is a finished batch/legacy message.
func NormalizeMessageStreamState(state string) string {
	return version.NormalizeMessageStreamState(state)
}

// MessagePart is one content part of an agent message.
type MessagePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolCall is one model-requested tool invocation recorded in the transcript.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// AgentMessage is one LLM transcript row (system/user/assistant/tool). It is
// immutable once terminal: a row written by the batch path is complete on
// insert, while a streaming assistant row (v0.2.4-D T8) is rewritten in place
// until its turn seals it complete or interrupted.
type AgentMessage struct {
	ID             int64
	RunID          *int64
	ConversationID *int64
	Role           MessageRole
	Parts          []MessagePart
	ToolCalls      []ToolCall
	ToolCallID     *string
	Usage          map[string]int
	StreamState    string
	CreatedAt      time.Time
}

// Validate checks the transcript role, tool-call identity and stream state.
func (m AgentMessage) Validate() error {
	if !m.Role.Valid() {
		return platform.Invalid("role", "must be one of system, user, assistant, tool")
	}
	if err := version.ValidateMessageStreamState(NormalizeMessageStreamState(m.StreamState)); err != nil {
		return err
	}
	if m.Role == MessageRoleTool {
		if m.ToolCallID == nil || strings.TrimSpace(*m.ToolCallID) == "" {
			return platform.Invalid("tool_call_id", "is required for tool messages")
		}
	}
	for _, call := range m.ToolCalls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
			return platform.Invalid("tool_calls", "id and name are required")
		}
	}
	return nil
}

// ContextSection is one named, ordered section of a context packet.
type ContextSection struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Refs     []string `json:"refs,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

// ContextPacket is the per-run snapshot persisted in context_packet.
type ContextPacket struct {
	ID          int64
	RunID       int64
	VersionID   int64
	Sections    []ContextSection
	TokenCounts map[string]int
	Hash        string
	CreatedAt   time.Time
}

// Validate checks the packet identity, section names, token counts and hash.
func (p ContextPacket) Validate() error {
	if p.RunID <= 0 {
		return platform.Invalid("run_id", "must be a positive decimal ID")
	}
	if p.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if len(p.Sections) == 0 {
		return platform.Invalid("sections", "must not be empty")
	}
	for _, section := range p.Sections {
		if strings.TrimSpace(section.Name) == "" {
			return platform.Invalid("sections", "section name must not be empty")
		}
	}
	if len(p.TokenCounts) == 0 {
		return platform.Invalid("token_counts", "must not be empty")
	}
	if !validSHA256Hex(p.Hash) {
		return platform.Invalid("hash", "must be a lowercase sha256 hex digest")
	}
	return nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}
