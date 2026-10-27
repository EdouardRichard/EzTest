package application

import "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"

// contextMessageOverheadChars is the per-message character allowance charged by
// EstimateChars for fields the estimate does not measure (role, tool names,
// ids, JSON framing).
const contextMessageOverheadChars = 16

// ContextBuilder assembles the LLM context for one model turn: the profile's
// system prompt followed by the session history, truncated turn by turn to fit
// the character budget. It is stateless and safe to reuse across runs.
type ContextBuilder struct{}

// NewContextBuilder returns a stateless context builder.
func NewContextBuilder() *ContextBuilder {
	return &ContextBuilder{}
}

// BuildResult is the outcome of one Build call.
type BuildResult struct {
	// Messages is [system] followed by the history messages that survived
	// truncation, in conversation order.
	Messages []LLMMessage
	// DroppedMessages counts the history messages removed by truncation; the
	// system prompt is never counted.
	DroppedMessages int
}

// Build converts the session history into LLM messages and truncates it to
// budgetChars.
//
// Messages are grouped by user turn: a user message starts a new group and the
// assistant/tool messages that follow it belong to that group. Any history
// before the first user message (abnormal data) forms its own leading group.
// Truncation drops complete groups from the oldest one, so an assistant tool
// call and its tool results are always dropped together; the system prompt and
// the latest group are never dropped. If the system prompt plus the latest
// group alone exceeds budgetChars, Build returns domain.ErrContextOverflow
// instead of silently truncating the user's input.
func (b *ContextBuilder) Build(systemPrompt string, history []domain.Message, budgetChars int) (BuildResult, error) {
	system := LLMMessage{
		Role:  "system",
		Parts: []domain.Part{{Type: "text", Text: systemPrompt}},
	}

	groups := make([][]LLMMessage, 0, len(history))
	for _, group := range groupHistoryByUserTurn(history) {
		messages := make([]LLMMessage, 0, len(group))
		for _, message := range group {
			messages = append(messages, convertHistoryMessage(message))
		}
		groups = append(groups, messages)
	}

	if len(groups) == 0 {
		if EstimateChars([]LLMMessage{system}) > budgetChars {
			return BuildResult{}, domain.ErrContextOverflow
		}
		return BuildResult{Messages: []LLMMessage{system}}, nil
	}

	latest := len(groups) - 1
	required := make([]LLMMessage, 0, 1+len(groups[latest]))
	required = append(required, system)
	required = append(required, groups[latest]...)
	if EstimateChars(required) > budgetChars {
		return BuildResult{}, domain.ErrContextOverflow
	}

	// Keep the latest group unconditionally, then include older groups for as
	// long as the whole suffix still fits. Groups are contiguous: once one
	// older group does not fit, none of the even older ones are kept.
	firstKept := latest
	for firstKept > 0 {
		candidate := []LLMMessage{system}
		for i := firstKept - 1; i <= latest; i++ {
			candidate = append(candidate, groups[i]...)
		}
		if EstimateChars(candidate) > budgetChars {
			break
		}
		firstKept--
	}

	messages := make([]LLMMessage, 0, len(history)+1)
	messages = append(messages, system)
	for i := firstKept; i <= latest; i++ {
		messages = append(messages, groups[i]...)
	}
	dropped := 0
	for i := 0; i < firstKept; i++ {
		dropped += len(groups[i])
	}
	return BuildResult{Messages: messages, DroppedMessages: dropped}, nil
}

// EstimateChars estimates the character cost of messages as the sum of text
// part lengths, reasoning lengths and tool-call argument lengths, plus a fixed
// overhead per message. Byte length is used deliberately: it charges a CJK
// character three units instead of one, which keeps the budget conservative.
func EstimateChars(messages []LLMMessage) int {
	total := 0
	for _, message := range messages {
		total += contextMessageOverheadChars
		for _, part := range message.Parts {
			total += len(part.Text)
		}
		total += len(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			total += len(call.Arguments)
		}
	}
	return total
}

// groupHistoryByUserTurn splits history into turns. A user message starts a new
// group; assistant and tool messages are appended to the current group. History
// before the first user message forms its own leading group.
func groupHistoryByUserTurn(history []domain.Message) [][]domain.Message {
	groups := make([][]domain.Message, 0)
	current := make([]domain.Message, 0)
	for _, message := range history {
		if message.Role == "user" && len(current) > 0 {
			groups = append(groups, current)
			current = make([]domain.Message, 0)
		}
		current = append(current, message)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

// convertHistoryMessage maps one stored message to the LLM wire form. Reasoning
// is echoed only for assistant messages and only when non-empty (the upstream
// rejects empty reasoning_content); tool calls and tool_call_id stay on the
// roles that own them.
func convertHistoryMessage(message domain.Message) LLMMessage {
	converted := LLMMessage{
		Role:  message.Role,
		Parts: message.Parts,
	}
	switch message.Role {
	case "assistant":
		converted.ReasoningContent = message.Reasoning
		converted.ToolCalls = message.ToolCalls
	case "tool":
		converted.ToolCallID = message.ToolCallID
	}
	return converted
}
