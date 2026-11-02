package agentapp

import (
	"context"
	"errors"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

const (
	summaryPrompt = `你是 EzTest 的会话摘要器。把给定的历史对话压缩成简短中文摘要：
- 保留用户目标、已确认事实、未决问题与任务约束；
- 不编造未出现的信息，不使用 Markdown 标题；
- 最多 800 字。`
	maxSummaryInputMessages = 40
	// maxSummarizerOutputTokens bounds the summary completion.
	maxSummarizerOutputTokens = 1024
)

// LLMSummarizer adapts the P1 chat gateway to the ContextBuilder Summarizer
// port. Summarization is best effort: on any failure the builder falls back to
// its deterministic summary.
type LLMSummarizer struct {
	chat llmapp.ChatGateway
}

// NewLLMSummarizer wires the LLM-backed summarizer. A nil gateway is allowed;
// Summarize then returns an error so the caller falls back deterministically.
func NewLLMSummarizer(chat llmapp.ChatGateway) *LLMSummarizer {
	return &LLMSummarizer{chat: chat}
}

// Summarize compresses the oldest messages into a rolling summary.
func (s *LLMSummarizer) Summarize(ctx context.Context, messages []Message) (string, error) {
	if s == nil || s.chat == nil {
		return "", errors.New("llm summarizer is not configured")
	}
	if len(messages) == 0 {
		return "", nil
	}
	start := 0
	if len(messages) > maxSummaryInputMessages {
		start = len(messages) - maxSummaryInputMessages
	}
	var transcript strings.Builder
	for _, message := range messages[start:] {
		content := truncateRunes(strings.TrimSpace(message.Content), 300)
		if content == "" {
			continue
		}
		transcript.WriteString("[")
		transcript.WriteString(message.Role)
		transcript.WriteString("] ")
		transcript.WriteString(content)
		transcript.WriteString("\n")
	}
	if strings.TrimSpace(transcript.String()) == "" {
		return "", nil
	}

	response, err := s.chat.Chat(ctx, llmapp.ChatRequest{
		Messages: []llmapp.ChatMessage{
			{Role: "system", Content: summaryPrompt},
			{Role: "user", Content: transcript.String()},
		},
		MaxOutputTokens: maxSummarizerOutputTokens,
	})
	if err != nil {
		return "", err
	}
	summary := truncateRunes(strings.TrimSpace(response.Content), maxSummaryRunes)
	if summary == "" {
		return "", errors.New("llm summarizer returned an empty summary")
	}
	return summary, nil
}
