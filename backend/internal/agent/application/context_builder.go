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
// The history is first sanitized so it is wire-valid for the chat completions
// protocol: empty assistant turns are dropped and tool calls are kept only with
// their complete tool-message answers (see sanitizeHistory).
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
	for _, group := range groupHistoryByUserTurn(sanitizeHistory(history)) {
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

// sanitizeHistory drops history rows that would make the upstream reject the
// whole request, while keeping the user turns and the valid tool pairs:
//
//   - an assistant message with no non-empty text part and no tool calls is
//     never replayed. The upstream answers such a message with HTTP 400
//     "Invalid assistant message"; it is left behind by an immediate abort
//     before the first token, a thought-only abort, a synchronous stream error
//     or an empty successful answer. Reasoning alone does not make it valid.
//   - empty text parts are filtered from every kept message.
//   - an assistant message with tool calls is kept only when a tool message
//     directly following it answers every call ID. Otherwise the assistant and
//     its partial tool messages are dropped together: the upstream answers an
//     unanswered tool_calls message with HTTP 400 "insufficient tool messages".
//   - tool messages with no matching assistant call (crash history, a dropped
//     assistant) are dropped.
//
// The removed rows are not reported in DroppedMessages: that counter measures
// budget truncation only.
func sanitizeHistory(history []domain.Message) []domain.Message {
	sanitized := make([]domain.Message, 0, len(history))
	for index := 0; index < len(history); index++ {
		message := history[index]
		switch message.Role {
		case "user", "system":
			sanitized = append(sanitized, filterEmptyTextParts(message))
		case "assistant":
			message = filterEmptyTextParts(message)
			if len(message.ToolCalls) == 0 {
				if !hasTextPart(message.Parts) {
					continue
				}
				sanitized = append(sanitized, message)
				continue
			}
			// The tool answers must directly follow their assistant message.
			end := index + 1
			for end < len(history) && history[end].Role == "tool" {
				end++
			}
			callIDs := make(map[string]struct{}, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				callIDs[call.ID] = struct{}{}
			}
			toolIDs := make(map[string]struct{}, end-index-1)
			for _, tool := range history[index+1 : end] {
				toolIDs[tool.ToolCallID] = struct{}{}
			}
			complete := true
			for callID := range callIDs {
				if _, ok := toolIDs[callID]; !ok || callID == "" {
					complete = false
					break
				}
			}
			if !complete {
				// Drop the assistant and every partial tool message that
				// belongs to its turn; the next non-tool message resumes.
				index = end - 1
				continue
			}
			sanitized = append(sanitized, message)
			for _, tool := range history[index+1 : end] {
				if _, ok := callIDs[tool.ToolCallID]; ok {
					sanitized = append(sanitized, filterEmptyTextParts(tool))
				}
			}
			index = end - 1
		default:
			// A tool message here has no assistant tool_calls message in front
			// of it; any other role is not a chat completions role.
			continue
		}
	}
	return sanitized
}

// hasTextPart reports whether parts contains a non-empty text part.
func hasTextPart(parts []domain.Part) bool {
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			return true
		}
	}
	return false
}

// filterEmptyTextParts removes text parts with no content. Other part types
// (images) are kept as-is.
func filterEmptyTextParts(message domain.Message) domain.Message {
	if len(message.Parts) == 0 {
		return message
	}
	filtered := make([]domain.Part, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part.Type == "text" && part.Text == "" {
			continue
		}
		filtered = append(filtered, part)
	}
	if len(filtered) == len(message.Parts) {
		return message
	}
	message.Parts = filtered
	return message
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
