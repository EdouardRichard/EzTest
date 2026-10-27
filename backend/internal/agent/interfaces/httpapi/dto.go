package httpapi

import (
	"encoding/json"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// profilePayload is the public profile representation from design spec §10.2.
// Key and Builtin are read-only projections: they have no matching PATCH field,
// so a request that tries to change them is rejected as an unknown field.
type profilePayload struct {
	ID                 int64     `json:"id"`
	Key                string    `json:"key"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	Builtin            bool      `json:"builtin"`
	Enabled            bool      `json:"enabled"`
	SystemPrompt       string    `json:"system_prompt"`
	Model              string    `json:"model"`
	AllowedTools       []string  `json:"allowed_tools"`
	MaxTurns           int       `json:"max_turns"`
	RunSeconds         int       `json:"run_seconds"`
	HardTimeoutSeconds int       `json:"hard_timeout_seconds"`
	Thinking           string    `json:"thinking"`
	ReasoningEffort    string    `json:"reasoning_effort"`
	MaxOutputTokens    int       `json:"max_output_tokens"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// newProfilePayload maps a domain profile to the API shape. A nil tool slice is
// exposed as an empty array: the contract says allowed_tools is always a list.
func newProfilePayload(profile domain.AgentProfile) profilePayload {
	tools := profile.AllowedTools
	if tools == nil {
		tools = []string{}
	}
	return profilePayload{
		ID:                 profile.ID,
		Key:                profile.Key,
		Name:               profile.Name,
		Description:        profile.Description,
		Builtin:            profile.Builtin,
		Enabled:            profile.Enabled,
		SystemPrompt:       profile.SystemPrompt,
		Model:              profile.Model,
		AllowedTools:       tools,
		MaxTurns:           profile.MaxTurns,
		RunSeconds:         profile.RunSeconds,
		HardTimeoutSeconds: profile.HardTimeoutSeconds,
		Thinking:           profile.Thinking,
		ReasoningEffort:    profile.ReasoningEffort,
		MaxOutputTokens:    profile.MaxOutputTokens,
		CreatedAt:          profile.CreatedAt,
		UpdatedAt:          profile.UpdatedAt,
	}
}

// runPayload is the public run representation from design spec §10.2. Empty
// optional values are encoded as null, matching the frontend contract.
type runPayload struct {
	ID              int64      `json:"id"`
	SessionID       int64      `json:"session_id"`
	Status          string     `json:"status"`
	TerminalReason  *string    `json:"terminal_reason"`
	Turns           int        `json:"turns"`
	InputTokens     int64      `json:"input_tokens"`
	OutputTokens    int64      `json:"output_tokens"`
	ReasoningTokens int64      `json:"reasoning_tokens"`
	AbortCode       *string    `json:"abort_code"`
	ErrorCode       *string    `json:"error_code"`
	LastToolName    *string    `json:"last_tool_name"`
	LastToolPending bool       `json:"last_tool_pending"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
}

// newRunPayload maps a domain run to the API shape; a nil run stays nil so the
// session payload encodes null.
func newRunPayload(run *domain.Run) *runPayload {
	if run == nil {
		return nil
	}
	return &runPayload{
		ID:              run.ID,
		SessionID:       run.SessionID,
		Status:          string(run.Status),
		TerminalReason:  optionalString(string(run.TerminalReason)),
		Turns:           run.Turns,
		InputTokens:     run.InputTokens,
		OutputTokens:    run.OutputTokens,
		ReasoningTokens: run.ReasoningTokens,
		AbortCode:       optionalString(run.AbortCode),
		ErrorCode:       optionalString(run.ErrorCode),
		LastToolName:    optionalString(run.LastToolName),
		LastToolPending: run.LastToolPending,
		StartedAt:       run.StartedAt,
		EndedAt:         run.EndedAt,
	}
}

// sessionPayload is the public session representation from design spec §10.2.
type sessionPayload struct {
	ID             int64       `json:"id"`
	ProfileID      int64       `json:"profile_id"`
	ProfileKey     string      `json:"profile_key"`
	Title          string      `json:"title"`
	CreatedBy      *int64      `json:"created_by"`
	Running        bool        `json:"running"`
	ActiveRun      *runPayload `json:"active_run"`
	LastRun        *runPayload `json:"last_run"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	LastActivityAt time.Time   `json:"last_activity_at"`
}

// newSessionPayload maps one session plus its two run projections to the API
// shape.
func newSessionPayload(session domain.Session, running bool, active, last *domain.Run) sessionPayload {
	return sessionPayload{
		ID:             session.ID,
		ProfileID:      session.ProfileID,
		ProfileKey:     session.ProfileKey,
		Title:          session.Title,
		CreatedBy:      session.CreatedBy,
		Running:        running,
		ActiveRun:      newRunPayload(active),
		LastRun:        newRunPayload(last),
		CreatedAt:      session.CreatedAt,
		UpdatedAt:      session.UpdatedAt,
		LastActivityAt: session.LastActivityAt,
	}
}

// messagePayload is the public message representation from design spec §10.2.
type messagePayload struct {
	ID         int64             `json:"id"`
	SessionID  int64             `json:"session_id"`
	RunID      *int64            `json:"run_id"`
	Role       string            `json:"role"`
	Content    []domain.Part     `json:"content"`
	Reasoning  string            `json:"reasoning"`
	ToolCalls  []domain.ToolCall `json:"tool_calls"`
	ToolCallID *string           `json:"tool_call_id"`
	ToolName   *string           `json:"tool_name"`
	Usage      *domain.Usage     `json:"usage"`
	CreatedAt  time.Time         `json:"created_at"`
}

// newMessagePayload maps a stored message to the API shape. Content is always a
// list; tool_calls, tool_call_id, tool_name and usage are null when absent,
// matching the frontend contract.
func newMessagePayload(message domain.Message) messagePayload {
	content := message.Parts
	if content == nil {
		content = []domain.Part{}
	}
	return messagePayload{
		ID:         message.ID,
		SessionID:  message.SessionID,
		RunID:      message.RunID,
		Role:       message.Role,
		Content:    content,
		Reasoning:  message.Reasoning,
		ToolCalls:  message.ToolCalls,
		ToolCallID: optionalString(message.ToolCallID),
		ToolName:   optionalString(message.ToolName),
		Usage:      message.Usage,
		CreatedAt:  message.CreatedAt,
	}
}

// toolPayload is the public tool representation from design spec §10.2.
type toolPayload struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// newToolPayload maps a registry tool description to the API shape.
func newToolPayload(info domain.ToolInfo) toolPayload {
	return toolPayload{Name: info.Name, Description: info.Description, Schema: info.Schema}
}

// sessionPage is the list body of GET /api/agents/sessions. next_cursor is null
// on the last page; when has_more is true it is the opaque cursor of the last
// returned id.
type sessionPage struct {
	Items      []sessionPayload `json:"items"`
	HasMore    bool             `json:"has_more"`
	NextCursor *string          `json:"next_cursor"`
}

// updateProfileRequest is the PATCH /api/agents/profiles/{id} body. Only the ten
// mutable fields from design spec §10.3 exist here, so DecodeJSON's
// DisallowUnknownFields rejects key, builtin and anything else.
type updateProfileRequest struct {
	SystemPrompt       *string   `json:"system_prompt"`
	Model              *string   `json:"model"`
	AllowedTools       *[]string `json:"allowed_tools"`
	MaxTurns           *int      `json:"max_turns"`
	RunSeconds         *int      `json:"run_seconds"`
	HardTimeoutSeconds *int      `json:"hard_timeout_seconds"`
	Thinking           *string   `json:"thinking"`
	ReasoningEffort    *string   `json:"reasoning_effort"`
	MaxOutputTokens    *int      `json:"max_output_tokens"`
	Enabled            *bool     `json:"enabled"`
}

// params converts the request DTO to the domain partial update. Pointer fields
// keep "omitted" distinct from an explicit zero, so a PATCH only touches the
// fields the client sent.
func (r updateProfileRequest) params() domain.UpdateParams {
	return domain.UpdateParams{
		SystemPrompt:       r.SystemPrompt,
		Model:              r.Model,
		AllowedTools:       r.AllowedTools,
		MaxTurns:           r.MaxTurns,
		RunSeconds:         r.RunSeconds,
		HardTimeoutSeconds: r.HardTimeoutSeconds,
		Thinking:           r.Thinking,
		ReasoningEffort:    r.ReasoningEffort,
		MaxOutputTokens:    r.MaxOutputTokens,
		Enabled:            r.Enabled,
	}
}

// createSessionRequest is the POST /api/agents/sessions body. A zero profile_id
// means the builtin profile.
type createSessionRequest struct {
	ProfileID int64  `json:"profile_id"`
	Title     string `json:"title"`
}

// updateSessionRequest is the PATCH /api/agents/sessions/{id} body. Title is the
// only mutable session field and is required.
type updateSessionRequest struct {
	Title *string `json:"title"`
}

// sendMessageRequest is the POST /api/agents/sessions/{id}/messages body.
type sendMessageRequest struct {
	Message string `json:"message"`
}

// sendMessageData is the 202 body of POST .../messages.
type sendMessageData struct {
	RunID  int64  `json:"run_id"`
	Status string `json:"status"`
}

// stopData is the 200 body of POST .../stop.
type stopData struct {
	Status string `json:"status"`
}

// optionalString maps an empty domain string to a JSON null pointer.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
