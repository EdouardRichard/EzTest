package agent

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
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
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ProfileBudgets is the budgets JSONB shape from the P1 profile contract.
type ProfileBudgets struct {
	MaxTurns    int `json:"max_turns,omitempty"`
	SoftSeconds int `json:"soft_seconds,omitempty"`
	HardSeconds int `json:"hard_seconds,omitempty"`
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

// AgentRun is one run of an agent profile, fenced by lease_epoch.
type AgentRun struct {
	ID              int64
	ProfileKey      string
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

// AgentMessage is one immutable LLM transcript row (system/user/assistant/tool).
type AgentMessage struct {
	ID             int64
	RunID          *int64
	ConversationID *int64
	Role           MessageRole
	Parts          []MessagePart
	ToolCalls      []ToolCall
	ToolCallID     *string
	Usage          map[string]int
	CreatedAt      time.Time
}

// Validate checks the transcript role and tool-call identity.
func (m AgentMessage) Validate() error {
	if !m.Role.Valid() {
		return platform.Invalid("role", "must be one of system, user, assistant, tool")
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
