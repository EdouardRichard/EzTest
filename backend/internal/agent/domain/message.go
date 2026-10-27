package domain

import "time"

// Part is one piece of a message's content. Type is "text" or "image_url"; the
// JSON tags make the persisted JSONB and the API shape identical.
//
// ImageRef is the P3 multimodal form of an image part: it references a stored
// attachment ("attachment:<id>") and is what the message store keeps. ImageURL
// is kept for P1 compatibility and is only ever populated transiently by the
// LLM gateway right before a call (D30: no data URLs in the database).
type Part struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	ImageRef string `json:"image_ref,omitempty"`
}

// ToolCall is one tool invocation requested by the model. Arguments is the raw
// JSON string returned by the upstream, unparsed.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Usage is the token accounting for one model turn or a whole run.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Message is one stored conversation message. Reasoning is the model's
// thinking content (only assistant messages have it); ToolCalls belongs to an
// assistant message and ToolCallID/ToolName to a tool message; Usage is set on
// the assistant message that completed a model turn.
type Message struct {
	ID         int64
	SessionID  int64
	RunID      *int64
	Role       string
	Parts      []Part
	Reasoning  string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
	Usage      *Usage
	CreatedAt  time.Time
}
